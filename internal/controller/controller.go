// Package controller decides when the stack sleeps and wakes.
//
// Rules: inside the schedule window the stack stays up; outside it, the stack
// sleeps once idle_after has passed without traffic. A wake starts once, on the
// first visit or when the window opens, and is refused once a cap is reached.
package controller

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/simondelamarre/standby-bige-ops/internal/config"
	"github.com/simondelamarre/standby-bige-ops/internal/driver"
	"github.com/simondelamarre/standby-bige-ops/internal/manifest"
	"github.com/simondelamarre/standby-bige-ops/internal/schedule"
	"github.com/simondelamarre/standby-bige-ops/internal/sequencer"
	"github.com/simondelamarre/standby-bige-ops/internal/store"
)

type Phase string

const (
	Unknown  Phase = "unknown"
	Asleep   Phase = "asleep"
	Waking   Phase = "waking"
	Awake    Phase = "awake"
	Sleeping Phase = "sleeping"
	Partial  Phase = "partial"
	Failed   Phase = "failed"
)

// Status is public: it must never carry machine ids, addresses or error text.
type Status struct {
	Phase      Phase  `json:"phase"`
	Step       int    `json:"step"`
	Steps      int    `json:"steps"`
	ETASeconds int    `json:"eta_seconds,omitempty"`
	CanWake    bool   `json:"can_wake"`
	Reason     string `json:"reason,omitempty"`
	// Only with page.show_setup.
	SleepInSeconds *int   `json:"sleep_in_seconds,omitempty"`
	Setup          *Setup `json:"setup,omitempty"`
}

// Setup is the part of the configuration a visitor may see.
type Setup struct {
	IdleAfterSeconds int      `json:"idle_after_seconds"`
	Timezone         string   `json:"timezone,omitempty"`
	Days             []string `json:"days,omitempty"`
	Awake            []string `json:"awake,omitempty"`
	WakeOnVisit      bool     `json:"wake_on_visit"`
	WakeButton       bool     `json:"wake_button"`
}

type Options struct {
	Config   *config.Config
	Manifest *manifest.Manifest
	Seq      *sequencer.Sequencer
	Window   *schedule.Window // nil: on demand only
	Store    *store.Store
	// Activity returns the last moment traffic reached the app; nil without a tap.
	Activity func(context.Context) (time.Time, error)
	Now      func() time.Time
	Log      *slog.Logger
}

type Controller struct {
	opt Options
	ctx context.Context
	wg  sync.WaitGroup

	mu           sync.Mutex
	phase        Phase
	progress     sequencer.Progress
	wakeStart    time.Time
	readyAt      time.Time
	lastActivity time.Time
	lastTick     time.Time
	inWindow     bool
}

// New returns a controller; ctx bounds the wake and sleep sequences it starts.
func New(ctx context.Context, opt Options) *Controller {
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.Log == nil {
		opt.Log = slog.Default()
	}
	return &Controller{opt: opt, ctx: ctx, phase: Unknown}
}

// Healthy tells the load balancer whether to send visitors to the guardian:
// yes whenever the app is not fully up, no while it serves.
func (c *Controller) Healthy() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.phase != Awake && c.phase != Unknown
}

func (c *Controller) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.opt.Now()
	st := Status{Phase: c.phase, Step: c.progress.Step, Steps: c.progress.Steps}
	if c.phase == Waking {
		if median, ok := c.opt.Store.MedianResume(); ok {
			left := median - now.Sub(c.wakeStart).Seconds()
			if left < 5 {
				left = 5
			}
			st.ETASeconds = int(left)
		}
	}
	st.CanWake, st.Reason = c.canWakeLocked(now)
	if c.phase == Failed {
		st.Reason = "error"
	}
	cfg := c.opt.Config
	if cfg.Page.ShowSetup {
		st.Setup = &Setup{
			IdleAfterSeconds: int(cfg.IdleAfter.Seconds()),
			WakeOnVisit:      cfg.Wake.OnVisit,
			WakeButton:       cfg.Wake.Button,
		}
		if s := cfg.Schedule; s != nil {
			st.Setup.Timezone, st.Setup.Days, st.Setup.Awake = s.Timezone, s.Days, s.Awake
		}
		if c.phase == Awake && !c.inWindow {
			since := c.lastActivity
			if c.readyAt.After(since) {
				since = c.readyAt
			}
			if !since.IsZero() {
				left := int((cfg.IdleAfter.Duration - now.Sub(since)).Seconds())
				st.SleepInSeconds = &left
				if left < 0 {
					*st.SleepInSeconds = 0
				}
			}
		}
	}
	return st
}

func (c *Controller) canWakeLocked(now time.Time) (bool, string) {
	caps := c.opt.Config.Caps
	if c.opt.Store.WakesOn(now) >= caps.WakesPerDay || c.opt.Store.AwakeHours(now) >= float64(caps.AwakeHoursPerMonth) {
		return false, "cap_reached"
	}
	return true, ""
}

// RequestWake starts a wake unless one is running, the stack is up or a cap is
// reached. It reports whether this call started the wake.
func (c *Controller) RequestWake(reason string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch c.phase {
	case Waking, Awake, Sleeping, Unknown:
		return false
	}
	if ok, _ := c.canWakeLocked(c.opt.Now()); !ok {
		return false
	}
	c.startWakeLocked(reason)
	return true
}

func (c *Controller) startWakeLocked(reason string) {
	now := c.opt.Now()
	c.phase = Waking
	c.wakeStart = now
	c.progress = sequencer.Progress{Steps: len(c.opt.Manifest.Waves) + 1}
	c.opt.Store.CountWake(now)
	c.saveLocked()
	c.opt.Log.Info("wake started", "reason", reason)
	c.wg.Add(1)
	go c.runWake()
}

func (c *Controller) runWake() {
	defer c.wg.Done()
	err := c.opt.Seq.Wake(c.ctx, c.opt.Manifest, func(p sequencer.Progress) {
		c.mu.Lock()
		c.progress = p
		c.mu.Unlock()
	})
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.opt.Now()
	if err != nil {
		c.phase = Failed
		c.opt.Log.Error("wake failed", "err", err)
		return
	}
	took := now.Sub(c.wakeStart)
	c.opt.Store.AddResume(took.Seconds())
	c.saveLocked()
	c.phase = Awake
	c.readyAt = now
	c.lastActivity = now
	c.opt.Log.Info("stack awake", "seconds", int(took.Seconds()))
}

func (c *Controller) startSleepLocked(reason string) {
	c.phase = Sleeping
	c.opt.Log.Info("sleep started", "reason", reason)
	c.wg.Add(1)
	go c.runSleep()
}

func (c *Controller) runSleep() {
	defer c.wg.Done()
	// The guardian is already healthy: give the load balancer time to route
	// visitors to it before the app goes away.
	select {
	case <-time.After(c.opt.Config.LBSettle.Duration):
	case <-c.ctx.Done():
		return
	}
	err := c.opt.Seq.Sleep(c.ctx, c.opt.Manifest)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		c.phase = Failed
		c.opt.Log.Error("sleep failed", "err", err)
		return
	}
	c.phase = Asleep
	c.opt.Log.Info("stack asleep")
}

// Tick reconciles the observed machines with the rules. Call it periodically.
func (c *Controller) Tick(ctx context.Context) {
	ids := c.opt.Manifest.IDs()
	states, err := c.opt.Seq.Driver.States(ctx, ids)
	var activity time.Time
	var activityErr error
	if c.opt.Activity != nil {
		activity, activityErr = c.opt.Activity(ctx)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.opt.Now()
	previous := c.lastTick
	c.lastTick = now
	if err != nil {
		c.opt.Log.Warn("cannot read machine states", "err", err)
		return
	}
	observed := summarize(states, ids)
	if observed != Asleep && !previous.IsZero() {
		c.opt.Store.AddAwake(now, now.Sub(previous))
	}
	if activityErr == nil && activity.After(c.lastActivity) {
		c.lastActivity = activity
	}
	inWindow := c.opt.Window != nil && c.opt.Window.Contains(now)
	windowOpened := inWindow && !c.inWindow
	c.inWindow = inWindow
	defer c.saveLocked()

	switch c.phase {
	case Waking, Sleeping:
		return // the running sequence owns the phase
	case Failed:
		if observed == Partial {
			return // keep the failure visible until the machines settle
		}
	}
	if c.phase != observed {
		c.opt.Log.Info("observed", "phase", observed)
	}
	c.phase = observed

	switch c.phase {
	case Asleep, Partial:
		if windowOpened && c.opt.Config.Wake.Schedule {
			if ok, _ := c.canWakeLocked(now); ok {
				c.startWakeLocked("schedule")
			}
		}
	case Awake:
		if inWindow {
			return
		}
		if c.opt.Activity != nil && activityErr != nil {
			// Never cut a user we cannot see: stay up; the awake-hours cap bounds the cost.
			c.opt.Log.Warn("activity probe unreachable, staying awake", "err", activityErr)
			return
		}
		since := c.lastActivity
		if c.readyAt.After(since) {
			since = c.readyAt
		}
		if since.IsZero() {
			// The guardian restarted while the stack was up: start the idle clock now.
			c.lastActivity = now
			return
		}
		if now.Sub(since) >= c.opt.Config.IdleAfter.Duration {
			c.startSleepLocked("idle")
		} else if c.opt.Store.AwakeHours(now) >= float64(c.opt.Config.Caps.AwakeHoursPerMonth) {
			c.startSleepLocked("cap_reached")
		}
	}
}

// Wait blocks until running sequences finish.
func (c *Controller) Wait() { c.wg.Wait() }

func (c *Controller) saveLocked() {
	if err := c.opt.Store.Save(); err != nil {
		c.opt.Log.Warn("cannot save state", "err", err)
	}
}

func summarize(states map[string]driver.State, ids []string) Phase {
	running, stopped := 0, 0
	for _, id := range ids {
		switch states[id] {
		case driver.Running:
			running++
		case driver.Stopped:
			stopped++
		case driver.Missing:
			return Failed
		}
	}
	switch {
	case running == len(ids):
		return Awake
	case stopped == len(ids):
		return Asleep
	default:
		return Partial
	}
}

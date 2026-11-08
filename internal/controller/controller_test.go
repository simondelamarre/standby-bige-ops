package controller

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/simondelamarre/standby-bige-ops/internal/config"
	"github.com/simondelamarre/standby-bige-ops/internal/driver"
	"github.com/simondelamarre/standby-bige-ops/internal/driver/drivertest"
	"github.com/simondelamarre/standby-bige-ops/internal/manifest"
	"github.com/simondelamarre/standby-bige-ops/internal/schedule"
	"github.com/simondelamarre/standby-bige-ops/internal/sequencer"
	"github.com/simondelamarre/standby-bige-ops/internal/store"
)

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *clock) Add(d time.Duration) { c.mu.Lock(); c.now = c.now.Add(d); c.mu.Unlock() }

type rig struct {
	ctrl     *Controller
	fake     *drivertest.Fake
	clock    *clock
	activity time.Time
	actErr   error
	mu       sync.Mutex
}

// newRig builds a controller over a stopped two-wave stack. Saturday noon UTC:
// outside any weekday window.
func newRig(t *testing.T, mutate func(*config.Config), window *schedule.Window) *rig {
	t.Helper()
	cfg := config.Default()
	cfg.Tap.URL = "http://app:9090/activity"
	cfg.LBSettle = config.Duration{}
	if mutate != nil {
		mutate(&cfg)
	}
	m := &manifest.Manifest{
		Version: 1, Stack: "demo", Provider: "docker",
		Waves:    [][]manifest.Resource{{{ID: "db"}}, {{ID: "app"}}},
		AppProbe: manifest.Probe{Kind: "http", Target: "http://app/health"},
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{
		fake:  drivertest.New(map[string]driver.State{"db": driver.Stopped, "app": driver.Stopped}),
		clock: &clock{now: time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)},
	}
	seq := &sequencer.Sequencer{
		Driver: r.fake, Probe: func(context.Context, manifest.Probe) error { return nil },
		Poll: time.Millisecond, StateTimeout: time.Second, ProbeTimeout: time.Second,
	}
	r.ctrl = New(context.Background(), Options{
		Config: &cfg, Manifest: m, Seq: seq, Window: window, Store: st, Now: r.clock.Now,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Activity: func(context.Context) (time.Time, error) {
			r.mu.Lock()
			defer r.mu.Unlock()
			return r.activity, r.actErr
		},
	})
	r.tick()
	return r
}

func (r *rig) tick() { r.ctrl.Tick(context.Background()) }

func (r *rig) traffic(at time.Time) { r.mu.Lock(); r.activity = at; r.mu.Unlock() }

func (r *rig) waitPhase(t *testing.T, want Phase) {
	t.Helper()
	r.ctrl.Wait()
	if got := r.ctrl.Status().Phase; got != want {
		t.Fatalf("phase = %s, want %s", got, want)
	}
}

func TestFirstVisitWakesOnceAndHealthFollows(t *testing.T) {
	r := newRig(t, nil, nil)
	if r.ctrl.Status().Phase != Asleep || !r.ctrl.Healthy() {
		t.Fatalf("a sleeping stack must route visitors to the guardian: %+v", r.ctrl.Status())
	}
	if !r.ctrl.RequestWake("visit") {
		t.Fatal("first visit did not wake the stack")
	}
	if r.ctrl.RequestWake("visit") {
		t.Fatal("a second visit started another wake")
	}
	r.waitPhase(t, Awake)
	if r.ctrl.Healthy() {
		t.Fatal("the guardian stays in rotation while the app serves")
	}
	if got := len(r.fake.Calls()); got != 2 {
		t.Fatalf("machines started %d times, want 2", got)
	}
}

func TestSleepsOnlyAfterIdleWithoutTraffic(t *testing.T) {
	r := newRig(t, nil, nil)
	r.ctrl.RequestWake("visit")
	r.waitPhase(t, Awake)

	r.clock.Add(50 * time.Minute)
	r.traffic(r.clock.Now()) // someone uses the app at minute 50
	r.tick()
	r.clock.Add(59 * time.Minute)
	r.tick()
	if r.ctrl.Status().Phase != Awake {
		t.Fatal("slept 59 minutes after the last request")
	}
	r.clock.Add(2 * time.Minute)
	r.tick()
	r.waitPhase(t, Asleep)
}

func TestStaysAwakeWhenTheActivityProbeIsUnreachable(t *testing.T) {
	r := newRig(t, nil, nil)
	r.ctrl.RequestWake("visit")
	r.waitPhase(t, Awake)
	r.mu.Lock()
	r.actErr = io.ErrUnexpectedEOF
	r.mu.Unlock()
	r.clock.Add(3 * time.Hour)
	r.tick()
	if r.ctrl.Status().Phase != Awake {
		t.Fatal("slept without knowing whether someone uses the app")
	}
}

func TestTheIdleClockRestartsWhenTheActivityProbeComesBack(t *testing.T) {
	r := newRig(t, nil, nil)
	r.ctrl.RequestWake("visit")
	r.waitPhase(t, Awake)
	setProbe := func(err error) { r.mu.Lock(); r.actErr = err; r.mu.Unlock() }
	setProbe(io.ErrUnexpectedEOF)
	r.clock.Add(3 * time.Hour)
	r.tick()
	setProbe(nil)
	r.tick()
	if got := r.ctrl.Status().Phase; got != Awake {
		t.Fatalf("phase = %v right after the outage: it had no way to know the app was idle", got)
	}
	idle := config.Default().IdleAfter.Duration
	r.clock.Add(idle - time.Second)
	r.tick()
	if got := r.ctrl.Status().Phase; got != Awake {
		t.Fatalf("phase = %v before a full idle period", got)
	}
	r.clock.Add(time.Second)
	r.tick()
	r.waitPhase(t, Asleep)
}

func TestStaysAwakeInsideTheWindowAndWakesWhenItOpens(t *testing.T) {
	window, err := schedule.Parse("UTC", []string{"sat"}, []string{"12:30-18:00"})
	if err != nil {
		t.Fatal(err)
	}
	r := newRig(t, nil, window) // 12:00, before the window
	if r.ctrl.Status().Phase != Asleep {
		t.Fatal("woke before the window")
	}
	r.clock.Add(31 * time.Minute)
	r.tick()
	r.waitPhase(t, Awake)
	r.clock.Add(4 * time.Hour) // no traffic, still inside the window
	r.tick()
	if r.ctrl.Status().Phase != Awake {
		t.Fatal("slept inside the awake window")
	}
}

func TestSleepsOverLunchAndWakesWhenTheAfternoonOpens(t *testing.T) {
	window, err := schedule.Parse("UTC", []string{"sat"}, []string{"12:30-13:00", "14:00-18:00"})
	if err != nil {
		t.Fatal(err)
	}
	r := newRig(t, nil, window) // 12:00
	r.clock.Add(31 * time.Minute)
	r.tick() // 12:31, the morning range opens
	r.waitPhase(t, Awake)
	r.clock.Add(69 * time.Minute)
	r.tick() // 13:40, outside, 69 min without traffic
	r.waitPhase(t, Asleep)
	r.clock.Add(21 * time.Minute)
	r.tick() // 14:01, the afternoon range opens
	r.waitPhase(t, Awake)
}

func TestWakeCapPerDay(t *testing.T) {
	r := newRig(t, func(c *config.Config) { c.Caps.WakesPerDay = 1 }, nil)
	r.ctrl.RequestWake("visit")
	r.waitPhase(t, Awake)
	r.clock.Add(61 * time.Minute)
	r.tick()
	r.waitPhase(t, Asleep)
	if r.ctrl.RequestWake("visit") {
		t.Fatal("woke past the daily cap")
	}
	if st := r.ctrl.Status(); st.CanWake || st.Reason != "cap_reached" {
		t.Fatalf("status = %+v", st)
	}
}

func TestStatusCountsDownToSleepWhenSetupIsShown(t *testing.T) {
	r := newRig(t, func(c *config.Config) { c.Page.ShowSetup = true }, nil)
	if st := r.ctrl.Status(); st.Setup == nil || st.Setup.IdleAfterSeconds != 3600 || st.SleepInSeconds != nil {
		t.Fatalf("asleep status = %+v", st)
	}
	r.ctrl.RequestWake("button")
	r.waitPhase(t, Awake)
	r.clock.Add(20 * time.Minute)
	st := r.ctrl.Status()
	if st.SleepInSeconds == nil || *st.SleepInSeconds != 40*60 {
		t.Fatalf("sleep_in_seconds = %v, want 2400", st.SleepInSeconds)
	}
}

func TestStatusHidesSetupByDefault(t *testing.T) {
	r := newRig(t, nil, nil)
	if st := r.ctrl.Status(); st.Setup != nil || st.SleepInSeconds != nil {
		t.Fatalf("status exposes the setup: %+v", st)
	}
}

func TestWakesAgainInsideTheWindowWhenFoundStopped(t *testing.T) {
	window, err := schedule.Parse("UTC", []string{"sat"}, []string{"12:30-18:00"})
	if err != nil {
		t.Fatal(err)
	}
	r := newRig(t, nil, window)
	r.clock.Add(31 * time.Minute)
	r.tick() // 12:31, the window opens
	r.waitPhase(t, Awake)
	// Someone stops the machines in the middle of the window.
	r.fake.Stop(context.Background(), []string{"db", "app"})
	r.clock.Add(time.Minute)
	r.tick()
	r.waitPhase(t, Asleep) // too soon after the last schedule wake
	r.clock.Add(10 * time.Minute)
	r.tick()
	r.waitPhase(t, Awake)
}

func TestAStackStartedByHandStartsItsIdleClock(t *testing.T) {
	r := newRig(t, nil, nil)
	r.traffic(r.clock.Now()) // old traffic, long before the manual start
	r.clock.Add(3 * time.Hour)
	r.fake.Start(context.Background(), []string{"db", "app"})
	r.tick()
	if r.ctrl.Status().Phase != Awake {
		t.Fatalf("phase = %s, want awake", r.ctrl.Status().Phase)
	}
	r.clock.Add(30 * time.Minute)
	r.tick()
	if r.ctrl.Status().Phase != Awake {
		t.Fatal("a stack started by hand slept before idle_after")
	}
	r.clock.Add(31 * time.Minute)
	r.tick()
	r.waitPhase(t, Asleep)
}

func TestTheDailyCapCountsDaysInTheScheduleTimeZone(t *testing.T) {
	window, err := schedule.Parse("Europe/Paris", []string{"mon"}, []string{"08:00-09:00"})
	if err != nil {
		t.Fatal(err)
	}
	r := newRig(t, func(c *config.Config) { c.Caps.WakesPerDay = 1 }, window)
	r.clock.Add(11*time.Hour + 30*time.Minute) // Saturday 23:30 UTC, Sunday 00:30 in Paris
	if !r.ctrl.RequestWake("visit") {
		t.Fatal("first wake of the Paris day refused")
	}
	r.waitPhase(t, Awake)
	r.clock.Add(61 * time.Minute)
	r.tick()
	r.waitPhase(t, Asleep)
	// 00:31 UTC Sunday: still Sunday in Paris, the cap holds.
	if r.ctrl.RequestWake("visit") {
		t.Fatal("woke twice on the same Paris day")
	}
}

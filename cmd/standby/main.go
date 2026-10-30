// Command standby puts a stack to sleep when idle and wakes it on the next visit.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/simondelamarre/standby-bige-ops/internal/config"
	"github.com/simondelamarre/standby-bige-ops/internal/controller"
	"github.com/simondelamarre/standby-bige-ops/internal/driver"
	"github.com/simondelamarre/standby-bige-ops/internal/driver/docker"
	"github.com/simondelamarre/standby-bige-ops/internal/driver/outscale"
	"github.com/simondelamarre/standby-bige-ops/internal/manifest"
	"github.com/simondelamarre/standby-bige-ops/internal/preview"
	"github.com/simondelamarre/standby-bige-ops/internal/probe"
	"github.com/simondelamarre/standby-bige-ops/internal/schedule"
	"github.com/simondelamarre/standby-bige-ops/internal/sequencer"
	"github.com/simondelamarre/standby-bige-ops/internal/store"
	"github.com/simondelamarre/standby-bige-ops/internal/tap"
	"github.com/simondelamarre/standby-bige-ops/internal/web"
)

var version = "dev"

const usage = `usage: standby <command> [flags]

commands:
  run       run the guardian (--config config.json)
  tap       run the activity probe next to the app
  validate  check a configuration and its manifest
  preview   serve the waiting page with a simulated wake-up (--config config.json)
  version   print the version
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "run":
		err = run(os.Args[2:])
	case "tap":
		err = runTap(os.Args[2:])
	case "validate":
		err = validate(os.Args[2:])
	case "preview":
		err = runPreview(os.Args[2:])
	case "version":
		fmt.Println(version)
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "standby:", err)
		os.Exit(1)
	}
}

type loaded struct {
	cfg    *config.Config
	m      *manifest.Manifest
	window *schedule.Window
	dir    string
}

func load(path string) (*loaded, error) {
	cfg, err := config.Load(path)
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(path)
	m, err := manifest.Load(resolve(dir, cfg.Manifest))
	if err != nil {
		return nil, err
	}
	if m.Provider != cfg.Driver.Name {
		return nil, fmt.Errorf("manifest provider %q does not match driver %q", m.Provider, cfg.Driver.Name)
	}
	l := &loaded{cfg: cfg, m: m, dir: dir}
	if s := cfg.Schedule; s != nil {
		if l.window, err = schedule.Parse(s.Timezone, s.Days, s.Awake); err != nil {
			return nil, err
		}
	}
	return l, nil
}

func resolve(dir, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(dir, p)
}

func newDriver(l *loaded) (driver.Driver, error) {
	d := l.cfg.Driver
	switch d.Name {
	case "outscale":
		creds, err := outscale.LoadCredentials(resolve(l.dir, d.CredentialsFile))
		if err != nil {
			return nil, err
		}
		return outscale.New(d.Region, creds)
	default:
		return docker.NewUnix(d.DockerSocket), nil
	}
}

func validate(args []string) error {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	path := fs.String("config", "config.json", "configuration file")
	live := fs.Bool("live", false, "also read the machine states from the provider (read-only)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	l, err := load(*path)
	if err != nil {
		return err
	}
	fmt.Printf("ok: stack %s, %d machines in %d waves\n", l.m.Stack, len(l.m.IDs()), len(l.m.Waves))
	if !*live {
		return nil
	}
	// Read-only check of the credentials and the machines: one state read.
	inner, err := newDriver(l)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	states, err := driver.NewGuard(inner, l.m.IDs()).States(ctx, l.m.IDs())
	if err != nil {
		return err
	}
	for _, id := range l.m.IDs() {
		fmt.Printf("  %s: %s\n", id, states[id])
	}
	return nil
}

func run(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	path := fs.String("config", "config.json", "configuration file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	l, err := load(*path)
	if err != nil {
		return err
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	st, err := store.Open(resolve(l.dir, l.cfg.State))
	if err != nil {
		return err
	}
	inner, err := newDriver(l)
	if err != nil {
		return err
	}
	drv := driver.NewGuard(inner, l.m.IDs())
	seq := &sequencer.Sequencer{
		Driver: drv, Probe: probe.Check,
		Poll: 2 * time.Second, StateTimeout: 5 * time.Minute, ProbeTimeout: 5 * time.Minute,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctrl := controller.New(ctx, controller.Options{
		Config: l.cfg, Manifest: l.m, Seq: seq, Window: l.window, Store: st,
		Activity: tap.Client(l.cfg.Tap.URL), Log: log,
	})
	page, err := web.New(ctrl, l.cfg, l.dir)
	if err != nil {
		return err
	}
	servers := []*http.Server{
		newServer(l.cfg.Listen, page.Traffic()),
		newServer(l.cfg.HealthListen, web.Health(ctrl)),
	}
	errc := make(chan error, len(servers))
	for _, srv := range servers {
		go func(srv *http.Server) {
			if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
				errc <- err
			}
		}(srv)
	}
	log.Info("guardian started", "version", version, "stack", l.m.Stack, "machines", len(l.m.IDs()))

	tick := func() {
		tctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		ctrl.Tick(tctx)
	}
	tick()
	ticker := time.NewTicker(l.cfg.Tick.Duration)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			shutdown(servers)
			ctrl.Wait()
			return nil
		case err := <-errc:
			shutdown(servers)
			return err
		case <-ticker.C:
			tick()
		}
	}
}

func runTap(args []string) error {
	fs := flag.NewFlagSet("tap", flag.ContinueOnError)
	port := fs.Int("app-port", 3000, "port the app listens on")
	health := fs.String("app-health", "/health", "health path of the app, asked over loopback")
	listen := fs.String("listen", ":9090", "address for /activity and /health")
	interval := fs.Duration("interval", 5*time.Second, "sampling interval")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *port < 1 || *port > 65535 {
		return fmt.Errorf("invalid --app-port %d", *port)
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	t := tap.New(*port, fmt.Sprintf("http://127.0.0.1:%d%s", *port, *health))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go t.Run(ctx, *interval, func(err error) { log.Warn("sample failed", "err", err) })
	srv := newServer(*listen, t.Handler())
	errc := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
	}()
	log.Info("tap started", "version", version, "app_port", *port)
	select {
	case <-ctx.Done():
		shutdown([]*http.Server{srv})
		return nil
	case err := <-errc:
		return err
	}
}

// runPreview needs only the configuration: no manifest, no machine, no key.
func runPreview(args []string) error {
	fs := flag.NewFlagSet("preview", flag.ContinueOnError)
	path := fs.String("config", "config.json", "configuration file")
	listen := fs.String("listen", "127.0.0.1:8099", "address to serve the preview on")
	wake := fs.Duration("wake", 8*time.Second, "length of the simulated wake-up")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	ctrl := preview.New(3, *wake)
	page, err := web.New(ctrl, cfg, filepath.Dir(*path))
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	srv := newServer(*listen, preview.Handler(ctrl, page.Traffic()))
	errc := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
	}()
	fmt.Printf("preview of the waiting page on http://%s (Ctrl+C to stop)\n", *listen)
	select {
	case <-ctx.Done():
		shutdown([]*http.Server{srv})
		return nil
	case err := <-errc:
		return err
	}
}

func newServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
}

func shutdown(servers []*http.Server) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, srv := range servers {
		_ = srv.Shutdown(ctx)
	}
}

package docker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/simondelamarre/standby-bige-ops/internal/driver"
)

// fakeEngine emulates the three Engine API calls the driver uses.
func fakeEngine(t *testing.T) (*httptest.Server, *sync.Map) {
	t.Helper()
	status := &sync.Map{}
	status.Store("app", "exited")
	mux := http.NewServeMux()
	mux.HandleFunc("GET /containers/{id}/json", func(w http.ResponseWriter, r *http.Request) {
		s, ok := status.Load(r.PathValue("id"))
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"State":{"Status":"` + s.(string) + `"}}`))
	})
	mux.HandleFunc("POST /containers/{id}/start", func(w http.ResponseWriter, r *http.Request) {
		s, ok := status.Load(r.PathValue("id"))
		if !ok {
			http.NotFound(w, r)
			return
		}
		if s == "running" {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		status.Store(r.PathValue("id"), "running")
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /containers/{id}/stop", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("t") != "60" {
			t.Errorf("stop without the 60 s grace period: %s", r.URL)
		}
		status.Store(r.PathValue("id"), "exited")
		w.WriteHeader(http.StatusNoContent)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, status
}

func TestStartStopAndStates(t *testing.T) {
	srv, _ := fakeEngine(t)
	d := New(srv.Client(), srv.URL)
	ctx := context.Background()

	states, err := d.States(ctx, []string{"app", "gone"})
	if err != nil {
		t.Fatal(err)
	}
	if states["app"] != driver.Stopped || states["gone"] != driver.Missing {
		t.Fatalf("states = %v", states)
	}
	if err := d.Start(ctx, []string{"app"}); err != nil {
		t.Fatal(err)
	}
	if err := d.Start(ctx, []string{"app"}); err != nil { // already running: 304
		t.Fatal(err)
	}
	if s, _ := d.States(ctx, []string{"app"}); s["app"] != driver.Running {
		t.Fatalf("after start: %v", s)
	}
	if err := d.Stop(ctx, []string{"app"}); err != nil {
		t.Fatal(err)
	}
	if s, _ := d.States(ctx, []string{"app"}); s["app"] != driver.Stopped {
		t.Fatalf("after stop: %v", s)
	}
}

func TestStartFailureIsReported(t *testing.T) {
	srv, _ := fakeEngine(t)
	if err := New(srv.Client(), srv.URL).Start(context.Background(), []string{"gone"}); err == nil {
		t.Fatal("starting a missing container did not fail")
	}
}

func TestGuardRefusesMachinesOutsideTheManifest(t *testing.T) {
	srv, status := fakeEngine(t)
	status.Store("prod-db", "running")
	g := driver.NewGuard(New(srv.Client(), srv.URL), []string{"app"})
	if err := g.Stop(context.Background(), []string{"prod-db"}); err == nil {
		t.Fatal("guard let an unlisted machine through")
	}
	if s, _ := status.Load("prod-db"); s != "running" {
		t.Fatalf("prod-db was touched: %v", s)
	}
}

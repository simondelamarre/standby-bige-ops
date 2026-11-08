package tap

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Port 0BB8 = 3000, 2382 = 9090. 0200A8C0 = 192.168.0.2, 0100007F = 127.0.0.1.
const tcp4 = `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000:0BB8 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 1 1 0 100 0 0 10 0
   1: 0200A8C0:0BB8 0300A8C0:D431 01 00000000:00000000 00:00000000 00000000     0        0 2 1 0 20 4 30 10 -1
   2: 0100007F:0BB8 0100007F:A1B2 01 00000000:00000000 00:00000000 00000000     0        0 3 1 0 20 4 30 10 -1
   3: 0200A8C0:2382 0300A8C0:D432 01 00000000:00000000 00:00000000 00000000     0        0 4 1 0 20 4 30 10 -1
   4: 0200A8C0:0BB8 0300A8C0:D433 06 00000000:00000000 00:00000000 00000000     0        0 5 1 0 20 4 30 10 -1
`

// ::1 and ::ffff:192.168.0.3 on port 3000.
const tcp6 = `  sl  local_address                         remote_address                        st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000000000000000000001000000:0BB8 00000000000000000000000001000000:C001 01 00000000:00000000 00:00000000 00000000     0        0 6 1 0 20 4 30 10 -1
   1: 0000000000000000FFFF00000200A8C0:0BB8 0000000000000000FFFF00000300A8C0:C002 01 00000000:00000000 00:00000000 00000000     0        0 7 1 0 20 4 30 10 -1
`

func TestCountInboundIgnoresLoopbackListenersAndOtherPorts(t *testing.T) {
	if n, err := CountInbound(strings.NewReader(tcp4), 3000); err != nil || n != 1 {
		t.Fatalf("tcp4: %d, %v; want 1", n, err)
	}
	if n, err := CountInbound(strings.NewReader(tcp6), 3000); err != nil || n != 1 {
		t.Fatalf("tcp6: %d, %v; want 1", n, err)
	}
}

func TestSampleAndActivityEndpoint(t *testing.T) {
	dir := t.TempDir()
	f4 := filepath.Join(dir, "tcp")
	if err := os.WriteFile(f4, []byte(tcp4), 0o600); err != nil {
		t.Fatal(err)
	}
	at := time.Unix(1_800_000_000, 0)
	tp := New(3000, "http://127.0.0.1:1/health")
	tp.Files = []string{f4, filepath.Join(dir, "missing")}
	tp.Now = func() time.Time { return at }
	if err := tp.Sample(); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(tp.Handler())
	defer srv.Close()
	got, err := Client(srv.URL + "/activity")(context.Background())
	if err != nil || !got.Equal(at) {
		t.Fatalf("Client() = %v, %v; want %v", got, err, at)
	}
}

func TestHealthMirrorsTheApp(t *testing.T) {
	healthy := true
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if !healthy {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer app.Close()
	srv := httptest.NewServer(New(3000, app.URL+"/health").Handler())
	defer srv.Close()
	for _, want := range []int{http.StatusOK, http.StatusServiceUnavailable} {
		resp, err := http.Get(srv.URL + "/health")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Fatalf("health = %d, want %d", resp.StatusCode, want)
		}
		healthy = false
	}
}

func TestActivityBeforeAnyTraffic(t *testing.T) {
	srv := httptest.NewServer(New(3000, "http://127.0.0.1:1/health").Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/activity")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var a activity
	if err := json.NewDecoder(resp.Body).Decode(&a); err != nil || a.LastActiveUnix != 0 {
		t.Fatalf("activity = %+v, %v", a, err)
	}
}

func TestClientsKeepTheLatestTrafficAndFailOnAnyUnreachableTap(t *testing.T) {
	serve := func(unix int64) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(activity{LastActiveUnix: unix})
		}))
	}
	idle, busy := serve(0), serve(1_700_000_000)
	defer idle.Close()
	defer busy.Close()
	at, err := Clients([]string{idle.URL, busy.URL})(context.Background())
	if err != nil || at.Unix() != 1_700_000_000 {
		t.Fatalf("latest = %v, %v", at, err)
	}
	down := httptest.NewServer(http.NotFoundHandler())
	down.Close()
	if _, err := Clients([]string{busy.URL, down.URL})(context.Background()); err == nil || !strings.Contains(err.Error(), down.URL) {
		t.Fatalf("an unreachable tap must be an error naming it, got %v", err)
	}
}

// watched runs WatchLoopback on scripted dials, ten seconds apart; the last result repeats.
func watched(ctx context.Context, results ...error) (error, *int) {
	calls := 0
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	tp := New(3000, "http://127.0.0.1:3000/health")
	tp.Now = func() time.Time { now = now.Add(10 * time.Second); return now }
	tp.Dial = func(context.Context) error {
		calls++
		return results[min(calls, len(results))-1]
	}
	return tp.WatchLoopback(ctx, time.Millisecond, 30*time.Second), &calls
}

func TestLeavesOnceTheAppPortKeepsRefusing(t *testing.T) {
	err, calls := watched(context.Background(), syscall.ECONNREFUSED)
	if !errors.Is(err, ErrCutOff) || *calls != 4 {
		t.Fatalf("err = %v after %d dials", err, *calls)
	}
}

func TestAnAnsweringPortRestartsTheCount(t *testing.T) {
	refused := error(syscall.ECONNREFUSED)
	err, calls := watched(context.Background(), refused, refused, refused, nil, refused)
	if !errors.Is(err, ErrCutOff) || *calls != 8 {
		t.Fatalf("err = %v after %d dials", err, *calls)
	}
}

func TestOnlyARefusalCounts(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err, _ := watched(ctx, context.DeadlineExceeded); err != nil {
		t.Fatalf("left on a timeout: %v", err)
	}
}

func TestWatchRecognisesARealRefusedPort(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	listening := New(port, "")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	if err := listening.WatchLoopback(ctx, time.Millisecond, 20*time.Millisecond); err != nil {
		t.Fatalf("left while the app listens: %v", err)
	}
	ln.Close()
	if err := New(port, "").WatchLoopback(context.Background(), time.Millisecond, 20*time.Millisecond); !errors.Is(err, ErrCutOff) {
		t.Fatalf("err = %v", err)
	}
}

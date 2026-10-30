// Package tap runs next to the app and tells the guardian when traffic last
// reached it, without sitting in the request path.
//
// It counts established inbound TCP connections on the app port from
// /proc/net/tcp and /proc/net/tcp6, ignoring loopback peers. It also answers
// the load balancer health check on its own port by asking the app over
// loopback, so health checks never count as traffic.
package tap

import (
	"bufio"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultFiles are the kernel socket tables read on Linux.
var DefaultFiles = []string{"/proc/net/tcp", "/proc/net/tcp6"}

type Tap struct {
	Port      int
	Files     []string
	AppHealth string // e.g. http://127.0.0.1:3000/health
	Now       func() time.Time

	client *http.Client
	mu     sync.Mutex
	last   time.Time
	conns  int
}

func New(port int, appHealth string) *Tap {
	return &Tap{
		Port:      port,
		Files:     DefaultFiles,
		AppHealth: appHealth,
		Now:       time.Now,
		client:    &http.Client{Timeout: 3 * time.Second},
	}
}

// Sample reads the socket tables once.
func (t *Tap) Sample() error {
	total := 0
	var errs []error
	for _, name := range t.Files {
		f, err := os.Open(name)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		n, err := CountInbound(f, t.Port)
		f.Close()
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
			continue
		}
		total += n
	}
	t.mu.Lock()
	t.conns = total
	if total > 0 {
		t.last = t.Now()
	}
	t.mu.Unlock()
	return errors.Join(errs...)
}

// Run samples every interval until ctx ends.
func (t *Tap) Run(ctx context.Context, interval time.Duration, onErr func(error)) {
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		if err := t.Sample(); err != nil && onErr != nil {
			onErr(err)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

type activity struct {
	LastActiveUnix int64 `json:"last_active_unix"`
	Connections    int   `json:"connections"`
}

// Handler serves GET /activity for the guardian and GET /health for the load balancer.
func (t *Tap) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /activity", func(w http.ResponseWriter, _ *http.Request) {
		t.mu.Lock()
		a := activity{Connections: t.conns}
		if !t.last.IsZero() {
			a.LastActiveUnix = t.last.Unix()
		}
		t.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(a)
	})
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, t.AppHealth, nil)
		if err == nil {
			var resp *http.Response
			if resp, err = t.client.Do(req); err == nil {
				_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
				resp.Body.Close()
				if resp.StatusCode < 400 {
					w.WriteHeader(http.StatusOK)
					_, _ = io.WriteString(w, "app up\n")
					return
				}
			}
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, "app down\n")
	})
	return mux
}

// CountInbound counts established connections whose local port is port and
// whose peer is not a loopback address.
func CountInbound(r io.Reader, port int) (int, error) {
	sc := bufio.NewScanner(r)
	n := 0
	first := true
	for sc.Scan() {
		if first { // header line
			first = false
			continue
		}
		fields := strings.Fields(sc.Text())
		if len(fields) < 4 || fields[3] != "01" { // 01 = ESTABLISHED
			continue
		}
		_, lport, err := splitAddr(fields[1])
		if err != nil || lport != port {
			continue
		}
		rip, _, err := splitAddr(fields[2])
		if err != nil || rip.IsLoopback() {
			continue
		}
		n++
	}
	return n, sc.Err()
}

// splitAddr decodes "0100007F:0BB8": a little-endian hex address and a hex port.
func splitAddr(s string) (net.IP, int, error) {
	hexIP, hexPort, ok := strings.Cut(s, ":")
	if !ok {
		return nil, 0, fmt.Errorf("bad address %q", s)
	}
	port, err := strconv.ParseUint(hexPort, 16, 16)
	if err != nil {
		return nil, 0, err
	}
	b, err := hex.DecodeString(hexIP)
	if err != nil || (len(b) != 4 && len(b) != 16) {
		return nil, 0, fmt.Errorf("bad address %q", s)
	}
	for i := 0; i < len(b); i += 4 {
		b[i], b[i+1], b[i+2], b[i+3] = b[i+3], b[i+2], b[i+1], b[i]
	}
	return net.IP(b), int(port), nil
}

// Client returns the guardian side: the last moment traffic reached the app.
func Client(url string) func(context.Context) (time.Time, error) {
	c := &http.Client{Timeout: 3 * time.Second}
	return func(ctx context.Context) (time.Time, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return time.Time{}, err
		}
		resp, err := c.Do(req)
		if err != nil {
			return time.Time{}, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return time.Time{}, fmt.Errorf("tap answered HTTP %d", resp.StatusCode)
		}
		var a activity
		if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<10)).Decode(&a); err != nil {
			return time.Time{}, err
		}
		if a.LastActiveUnix == 0 {
			return time.Time{}, nil
		}
		return time.Unix(a.LastActiveUnix, 0), nil
	}
}

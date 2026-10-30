package outscale

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/simondelamarre/standby-bige-ops/internal/driver"
)

var testCreds = Credentials{AccessKey: "AKTEST000000000000", SecretKey: "SKTEST0000000000000000000000000000000000"}

// The expected value comes from bige-ops' Python client (_sign_sigv4), which
// talks to the real API, with the same fake keys, body and date.
func TestSignatureMatchesBigeOps(t *testing.T) {
	body := []byte(`{"Filters": {"VmIds": ["i-0123abcd"]}}`)
	req, _ := http.NewRequest(http.MethodPost, "https://api.eu-west-2.outscale.com/api/v1/ReadVms", bytes.NewReader(body))
	sign(req, body, testCreds, "eu-west-2", time.Date(2026, 10, 30, 12, 34, 56, 0, time.UTC))
	want := "OSC4-HMAC-SHA256 Credential=AKTEST000000000000/20261030/eu-west-2/api/osc4_request, SignedHeaders=content-type;host;x-osc-date, Signature=aaa9e46192f9e4bcca85bb9922c2cc46141daab23db575916dffaa22deed2c77"
	if got := req.Header.Get("Authorization"); got != want {
		t.Fatalf("Authorization\n got %s\nwant %s", got, want)
	}
	if req.Header.Get("X-Osc-Date") != "20261030T123456Z" {
		t.Fatalf("X-Osc-Date = %q", req.Header.Get("X-Osc-Date"))
	}
}

// fakeAPI keeps VM states and records the actions and bodies it receives.
type fakeAPI struct {
	mu     sync.Mutex
	states map[string]string
	calls  []string
	bodies map[string]map[string]any
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.Header.Get("Authorization"), "OSC4-HMAC-SHA256 Credential=AKTEST") {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"Errors":[{"Code":"1","Type":"AccessDenied","Details":"unsigned"}]}`)
		return
	}
	action := strings.TrimPrefix(r.URL.Path, "/api/v1/")
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, action)
	f.bodies[action] = body
	switch action {
	case "ReadVms":
		var vms []map[string]string
		for id, s := range f.states {
			vms = append(vms, map[string]string{"VmId": id, "State": s})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"Vms": vms})
	case "StartVms", "StopVms":
		next := "running"
		if action == "StopVms" {
			next = "stopped"
		}
		for _, id := range body["VmIds"].([]any) {
			if _, ok := f.states[id.(string)]; !ok {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"Errors":[{"Code":"5063","Type":"InvalidResource","Details":"unknown vm"}]}`)
				return
			}
			f.states[id.(string)] = next
		}
		_, _ = io.WriteString(w, `{}`)
	default:
		w.WriteHeader(http.StatusForbidden)
	}
}

func newFake(t *testing.T) (*Driver, *fakeAPI) {
	t.Helper()
	api := &fakeAPI{states: map[string]string{"i-db": "stopped", "i-app": "stopped", "i-other": "running", "i-gone": "terminated"}, bodies: map[string]map[string]any{}}
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)
	return NewWithEndpoint(srv.URL+"/api/v1", "eu-west-2", testCreds, srv.Client()), api
}

func TestStatesStartAndGracefulStop(t *testing.T) {
	d, api := newFake(t)
	ctx := context.Background()
	states, err := d.States(ctx, []string{"i-db", "i-gone", "i-never"})
	if err != nil {
		t.Fatal(err)
	}
	if states["i-db"] != driver.Stopped || states["i-gone"] != driver.Missing || states["i-never"] != driver.Missing {
		t.Fatalf("states = %v", states)
	}
	if _, leaked := states["i-other"]; leaked {
		t.Fatal("a VM outside the request leaked into the result")
	}
	if err := d.Start(ctx, []string{"i-db", "i-app"}); err != nil {
		t.Fatal(err)
	}
	if err := d.Stop(ctx, []string{"i-app"}); err != nil {
		t.Fatal(err)
	}
	if force, ok := api.bodies["StopVms"]["ForceStop"]; !ok || force != false {
		t.Fatalf("StopVms must send ForceStop=false, got %v", api.bodies["StopVms"])
	}
	states, _ = d.States(ctx, []string{"i-db", "i-app"})
	if states["i-db"] != driver.Running || states["i-app"] != driver.Stopped {
		t.Fatalf("after start/stop: %v", states)
	}
	for _, call := range api.calls {
		if call != "ReadVms" && call != "StartVms" && call != "StopVms" {
			t.Fatalf("unexpected API action %q", call)
		}
	}
}

func TestAPIErrorsAreReportedWithoutSecrets(t *testing.T) {
	d, _ := newFake(t)
	err := d.Start(context.Background(), []string{"i-unknown"})
	if err == nil || !strings.Contains(err.Error(), "InvalidResource") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), testCreds.SecretKey) || strings.Contains(err.Error(), testCreds.AccessKey) {
		t.Fatal("the error leaks a key")
	}
}

func TestLoadCredentialsRequiresOwnerOnlyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outscale.json")
	b, _ := json.Marshal(testCreds)
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCredentials(path); err == nil {
		t.Fatal("accepted a world-readable key file")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadCredentials(path)
	if err != nil || c != testCreds {
		t.Fatalf("LoadCredentials = %+v, %v", c, err)
	}
}

func TestNewRejectsOddRegions(t *testing.T) {
	for _, r := range []string{"eu-west-2", "cloudgouv-eu-west-1", "ap-northeast-1"} {
		if _, err := New(r, testCreds); err != nil {
			t.Errorf("%s rejected: %v", r, err)
		}
	}
	for _, r := range []string{"", "eu-west-2.evil.com/x", "EU-WEST-2"} {
		if _, err := New(r, testCreds); err == nil {
			t.Errorf("%q accepted", r)
		}
	}
}

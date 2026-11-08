package ovh

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/simondelamarre/standby-bige-ops/internal/driver"
)

const testProject = "0123456789abcdef0123456789abcdef"

var testCreds = Credentials{ClientID: "EU.testclient0000", ClientSecret: "testsecret00000000000000000000"}

// fakeAPI keeps instance statuses, issues tokens and records the calls it receives.
type fakeAPI struct {
	mu          sync.Mutex
	statuses    map[string]string
	calls       []string
	attempts    int
	issued      int
	valid       string
	expiresIn   int
	refuseAll   bool
	tokenForm   url.Values
	tokenFormat string
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.URL.Path == "/auth/oauth2/token" {
		_ = r.ParseForm()
		f.tokenForm = r.PostForm
		f.tokenFormat = r.Header.Get("Content-Type")
		f.issued++
		f.valid = fmt.Sprintf("token-%d", f.issued)
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": f.valid, "token_type": "Bearer", "expires_in": f.expiresIn})
		return
	}
	f.attempts++
	if f.refuseAll || r.Header.Get("Authorization") != "Bearer "+f.valid {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"class":"Client::Unauthorized","message":"Invalid token"}`)
		return
	}
	path, ok := strings.CutPrefix(r.URL.Path, "/1.0/cloud/project/"+testProject+"/instance")
	if !ok {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	f.calls = append(f.calls, r.Method+" "+path)
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	switch {
	case r.Method == http.MethodGet && path == "":
		var instances []map[string]string
		for id, s := range f.statuses {
			instances = append(instances, map[string]string{"id": id, "status": s, "name": "vm-" + id})
		}
		_ = json.NewEncoder(w).Encode(instances)
	case r.Method == http.MethodPost && len(parts) == 2:
		next, known := map[string]string{"shelve": "SHELVING", "unshelve": "UNSHELVING", "start": "ACTIVE"}[parts[1]]
		if _, exists := f.statuses[parts[0]]; !known || !exists {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"class":"Client::NotFound","message":"Instance not found"}`)
			return
		}
		f.statuses[parts[0]] = next
		_, _ = io.WriteString(w, `null`)
	default:
		w.WriteHeader(http.StatusForbidden)
	}
}

func newFake(t *testing.T, statuses map[string]string) (*Driver, *fakeAPI) {
	t.Helper()
	api := &fakeAPI{statuses: statuses, expiresIn: 3600}
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)
	return NewWithEndpoint(srv.URL+"/1.0", srv.URL+"/auth/oauth2/token", testProject, testCreds, srv.Client()), api
}

func TestStatusMapping(t *testing.T) {
	cases := map[string]driver.State{
		"ACTIVE":            driver.Running,
		"SHUTOFF":           driver.Stopped,
		"STOPPED":           driver.Stopped,
		"SHELVED":           driver.Stopped,
		"SHELVED_OFFLOADED": driver.Stopped,
		"DELETED":           driver.Missing,
		"DELETING":          driver.Missing,
		"SOFT_DELETED":      driver.Missing,
		"BUILD":             driver.Changing,
		"BUILDING":          driver.Changing,
		"ERROR":             driver.Changing,
		"HARD_REBOOT":       driver.Changing,
		"MIGRATING":         driver.Changing,
		"PASSWORD":          driver.Changing,
		"PAUSED":            driver.Changing,
		"REBOOT":            driver.Changing,
		"REBUILD":           driver.Changing,
		"RESCUE":            driver.Changing,
		"RESCUED":           driver.Changing,
		"RESCUING":          driver.Changing,
		"RESIZE":            driver.Changing,
		"RESIZED":           driver.Changing,
		"RESUMING":          driver.Changing,
		"REVERT_RESIZE":     driver.Changing,
		"SHELVING":          driver.Changing,
		"SNAPSHOTTING":      driver.Changing,
		"SUSPENDED":         driver.Changing,
		"UNKNOWN":           driver.Changing,
		"UNRESCUING":        driver.Changing,
		"UNSHELVING":        driver.Changing,
		"VERIFY_RESIZE":     driver.Changing,
	}
	statuses := map[string]string{"i-other": "ACTIVE"}
	ids := []string{"i-never"}
	for status := range cases {
		statuses["i-"+status] = status
		ids = append(ids, "i-"+status)
	}
	d, _ := newFake(t, statuses)
	got, err := d.States(context.Background(), ids)
	if err != nil {
		t.Fatal(err)
	}
	for status, want := range cases {
		if got["i-"+status] != want {
			t.Errorf("%s = %s, want %s", status, got["i-"+status], want)
		}
	}
	if got["i-never"] != driver.Missing {
		t.Errorf("an absent id = %s, want missing", got["i-never"])
	}
	if _, leaked := got["i-other"]; leaked {
		t.Error("an instance outside the request leaked into the result")
	}
}

func TestStopShelvesAndCallsNothingElse(t *testing.T) {
	d, api := newFake(t, map[string]string{"i-db": "ACTIVE", "i-app": "ACTIVE", "i-other": "ACTIVE"})
	if err := d.Stop(context.Background(), []string{"i-db", "i-app"}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"POST /i-db/shelve", "POST /i-app/shelve"}; !slices.Equal(api.calls, want) {
		t.Fatalf("calls = %v, want %v", api.calls, want)
	}
}

func TestStartFollowsTheCurrentStatus(t *testing.T) {
	cases := []struct{ status, action string }{
		{"SHELVED", "unshelve"},
		{"SHELVED_OFFLOADED", "unshelve"},
		{"SHUTOFF", "start"},
		{"STOPPED", "start"},
		{"ACTIVE", ""},
		{"BUILD", ""},
		{"SHELVING", ""},
		{"UNSHELVING", ""},
		{"ERROR", ""},
	}
	for _, c := range cases {
		t.Run(c.status, func(t *testing.T) {
			d, api := newFake(t, map[string]string{"i-db": c.status})
			if err := d.Start(context.Background(), []string{"i-db"}); err != nil {
				t.Fatal(err)
			}
			want := []string{"GET "}
			if c.action != "" {
				want = append(want, "POST /i-db/"+c.action)
			}
			if !slices.Equal(api.calls, want) {
				t.Fatalf("calls = %v, want %v", api.calls, want)
			}
		})
	}
}

func TestStartReportsAGoneMachineAndStillWakesTheOthers(t *testing.T) {
	d, api := newFake(t, map[string]string{"i-db": "SHELVED", "i-gone": "DELETED"})
	err := d.Start(context.Background(), []string{"i-gone", "i-never", "i-db"})
	if err == nil || !strings.Contains(err.Error(), "i-gone") || !strings.Contains(err.Error(), "i-never") {
		t.Fatalf("err = %v", err)
	}
	if want := []string{"GET ", "POST /i-db/unshelve"}; !slices.Equal(api.calls, want) {
		t.Fatalf("calls = %v, want %v", api.calls, want)
	}
}

func TestTokenRequestIsAClientCredentialsGrant(t *testing.T) {
	d, api := newFake(t, map[string]string{})
	if _, err := d.States(context.Background(), []string{"i-db"}); err != nil {
		t.Fatal(err)
	}
	if api.tokenFormat != "application/x-www-form-urlencoded" {
		t.Errorf("Content-Type = %q", api.tokenFormat)
	}
	want := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {testCreds.ClientID},
		"client_secret": {testCreds.ClientSecret},
		"scope":         {"all"},
	}
	if api.tokenForm.Encode() != want.Encode() {
		t.Errorf("form = %v", api.tokenForm)
	}
}

func TestTokenIsCachedUntilCloseToExpiry(t *testing.T) {
	cases := []struct {
		name    string
		elapsed time.Duration
		issued  int
	}{
		{"right away", 0, 1},
		{"before the margin", time.Hour - 2*tokenMargin, 1},
		{"inside the margin", time.Hour - tokenMargin/2, 2},
		{"after expiry", 2 * time.Hour, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, api := newFake(t, map[string]string{"i-db": "ACTIVE"})
			now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
			d.now = func() time.Time { return now }
			ctx := context.Background()
			if _, err := d.States(ctx, []string{"i-db"}); err != nil {
				t.Fatal(err)
			}
			now = now.Add(c.elapsed)
			if _, err := d.States(ctx, []string{"i-db"}); err != nil {
				t.Fatal(err)
			}
			if api.issued != c.issued {
				t.Fatalf("issued %d tokens, want %d", api.issued, c.issued)
			}
		})
	}
}

func TestConcurrentCallsShareOneToken(t *testing.T) {
	d, api := newFake(t, map[string]string{"i-db": "ACTIVE"})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := d.States(context.Background(), []string{"i-db"}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if api.issued != 1 {
		t.Fatalf("issued %d tokens, want 1", api.issued)
	}
}

func TestRefreshesTheTokenOnceOn401(t *testing.T) {
	d, api := newFake(t, map[string]string{"i-db": "ACTIVE"})
	ctx := context.Background()
	if _, err := d.States(ctx, []string{"i-db"}); err != nil {
		t.Fatal(err)
	}

	api.valid = "revoked-by-the-test"
	if err := d.Stop(ctx, []string{"i-db"}); err != nil {
		t.Fatalf("a revoked token must be replaced: %v", err)
	}
	if api.issued != 2 || api.attempts != 3 {
		t.Fatalf("issued %d tokens in %d attempts, want 2 in 3", api.issued, api.attempts)
	}
	if want := []string{"GET ", "POST /i-db/shelve"}; !slices.Equal(api.calls, want) {
		t.Fatalf("calls = %v, want %v", api.calls, want)
	}

	api.refuseAll = true
	issued, attempts := api.issued, api.attempts
	_, err := d.States(ctx, []string{"i-db"})
	if err == nil || !strings.Contains(err.Error(), "HTTP 401") {
		t.Fatalf("err = %v", err)
	}
	if api.issued != issued+1 || api.attempts != attempts+2 {
		t.Fatalf("issued %d more tokens in %d more attempts, want 1 in 2", api.issued-issued, api.attempts-attempts)
	}
}

func TestAPIErrorsAreReportedWithoutSecrets(t *testing.T) {
	d, api := newFake(t, map[string]string{"i-db": "ACTIVE"})
	err := d.Stop(context.Background(), []string{"i-unknown", "i-db"})
	if err == nil || !strings.Contains(err.Error(), "Instance not found") || !strings.Contains(err.Error(), "i-unknown") {
		t.Fatalf("err = %v", err)
	}
	if !slices.Contains(api.calls, "POST /i-db/shelve") {
		t.Fatalf("the machine after the failure was skipped: %v", api.calls)
	}
	if strings.Contains(err.Error(), testCreds.ClientSecret) || strings.Contains(err.Error(), api.valid) {
		t.Fatal("the error leaks a secret")
	}
}

func TestTokenErrorsDoNotRepeatTheSecret(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_client", "error_description": "refused " + r.PostForm.Get("client_secret")})
	}))
	t.Cleanup(srv.Close)
	d := NewWithEndpoint(srv.URL+"/1.0", srv.URL+"/token", testProject, testCreds, srv.Client())
	_, err := d.States(context.Background(), []string{"i-db"})
	if err == nil || !strings.Contains(err.Error(), "invalid_client") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), testCreds.ClientSecret) {
		t.Fatal("the error leaks the client secret")
	}
}

func TestLoadCredentialsRequiresOwnerOnlyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ovh.json")
	b, _ := json.Marshal(testCreds)
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCredentials(path); err == nil {
		t.Fatal("accepted a world-readable credentials file")
	}
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCredentials(path); err == nil {
		t.Fatal("accepted a group-readable credentials file")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadCredentials(path)
	if err != nil || c != testCreds {
		t.Fatalf("LoadCredentials = %+v, %v", c, err)
	}
}

func TestLoadCredentialsRequiresBothFields(t *testing.T) {
	for _, body := range []string{`{"client_id":"a"}`, `{"client_secret":"b"}`, `not json`} {
		path := filepath.Join(t.TempDir(), "ovh.json")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadCredentials(path); err == nil {
			t.Errorf("%s accepted", body)
		}
	}
}

func TestNewChoosesTheEndpointAndRejectsOddTargets(t *testing.T) {
	for _, c := range []struct{ name, api, token string }{
		{"ovh-eu", "https://eu.api.ovh.com/1.0", "https://www.ovh.com/auth/oauth2/token"},
		{"ovh-ca", "https://ca.api.ovh.com/1.0", "https://ca.ovh.com/auth/oauth2/token"},
		{"ovh-us", "https://api.us.ovhcloud.com/1.0", "https://us.ovhcloud.com/auth/oauth2/token"},
	} {
		d, err := New(c.name, testProject, testCreds)
		if err != nil {
			t.Errorf("%s rejected: %v", c.name, err)
			continue
		}
		if d.endpoint != c.api || d.tokenURL != c.token {
			t.Errorf("%s targets %s and %s", c.name, d.endpoint, d.tokenURL)
		}
	}
	for _, e := range []string{"", "ovh-fr", "OVH-EU", "https://evil.example/1.0"} {
		if _, err := New(e, testProject, testCreds); err == nil {
			t.Errorf("endpoint %q accepted", e)
		}
	}
	for _, s := range []string{"", "abc/../other", "abc?x=1", "abc%2fdef", "abc def", "abc\n", strings.Repeat("a", 65)} {
		if _, err := New("ovh-eu", s, testCreds); err == nil {
			t.Errorf("service name %q accepted", s)
		}
	}
}

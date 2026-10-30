package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/simondelamarre/standby-bige-ops/internal/config"
	"github.com/simondelamarre/standby-bige-ops/internal/controller"
)

type fakeCtrl struct {
	status  controller.Status
	wakes   []string
	healthy bool
}

func (f *fakeCtrl) Status() controller.Status { return f.status }
func (f *fakeCtrl) Healthy() bool             { return f.healthy }
func (f *fakeCtrl) RequestWake(reason string) bool {
	f.wakes = append(f.wakes, reason)
	return true
}

func newServer(t *testing.T, mutate func(*config.Config)) (*Server, *fakeCtrl) {
	t.Helper()
	cfg := config.Default()
	cfg.Tap.URL = "http://app:9090/activity"
	if mutate != nil {
		mutate(&cfg)
	}
	ctrl := &fakeCtrl{status: controller.Status{Phase: controller.Asleep, Steps: 3, CanWake: true}}
	s, err := New(ctrl, &cfg, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s, ctrl
}

func get(h http.Handler, path, accept string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestVisitWakesAndServesAnHonestWaitingPage(t *testing.T) {
	s, ctrl := newServer(t, nil)
	rec := get(s.Traffic(), "/dashboard?x=1", "text/html,application/xhtml+xml")
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("code %d, Retry-After %q", rec.Code, rec.Header().Get("Retry-After"))
	}
	if len(ctrl.wakes) != 1 || ctrl.wakes[0] != "visit" {
		t.Fatalf("wakes = %v", ctrl.wakes)
	}
	csp := rec.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "default-src 'none'") || !strings.Contains(csp, "script-src 'self'") {
		t.Fatalf("CSP = %q", csp)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `lang="fr"`) || !strings.Contains(body, "On rallume le service") {
		t.Fatalf("page body:\n%s", body)
	}
}

func TestTechnicalURLsNeverWake(t *testing.T) {
	s, ctrl := newServer(t, nil)
	for _, path := range []string{"/robots.txt", "/favicon.ico", "/.well-known/security.txt", "/_standby/status", "/_standby/app.js", "/_standby/unknown"} {
		get(s.Traffic(), path, "text/html")
	}
	if len(ctrl.wakes) != 0 {
		t.Fatalf("technical URLs woke the stack: %v", ctrl.wakes)
	}
	if body := get(s.Traffic(), "/robots.txt", "").Body.String(); !strings.Contains(body, "Disallow: /") {
		t.Fatalf("robots.txt = %q", body)
	}
}

func TestVisitWithoutOnVisitDoesNotWake(t *testing.T) {
	s, ctrl := newServer(t, func(c *config.Config) { c.Wake.OnVisit = false; c.Wake.Button = true })
	rec := get(s.Traffic(), "/", "text/html")
	if len(ctrl.wakes) != 0 {
		t.Fatalf("woke with on_visit disabled: %v", ctrl.wakes)
	}
	if !strings.Contains(rec.Body.String(), `action="/_standby/wake"`) {
		t.Fatal("the optional start button is missing")
	}
}

func TestStatusIsMinimalJSON(t *testing.T) {
	s, _ := newServer(t, nil)
	rec := get(s.Traffic(), "/_standby/status", "")
	if !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("content type %q", rec.Header().Get("Content-Type"))
	}
	var fields map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &fields); err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{"phase": true, "step": true, "steps": true, "eta_seconds": true, "can_wake": true, "reason": true}
	for k := range fields {
		if !allowed[k] {
			t.Errorf("status exposes %q", k)
		}
	}
}

func TestAPIClientsGetJSON(t *testing.T) {
	s, _ := newServer(t, nil)
	rec := get(s.Traffic(), "/api/items", "application/json")
	if rec.Code != http.StatusServiceUnavailable || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("code %d, type %q", rec.Code, rec.Header().Get("Content-Type"))
	}
}

func TestButtonTokenIsSignedAndExpires(t *testing.T) {
	s, ctrl := newServer(t, func(c *config.Config) { c.Wake.Button = true })
	now := time.Now()
	s.Now = func() time.Time { return now }
	tok := s.token()

	post := func(token string) int {
		req := httptest.NewRequest(http.MethodPost, "/_standby/wake", strings.NewReader(url.Values{"token": {token}}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		s.Traffic().ServeHTTP(rec, req)
		return rec.Code
	}
	if code := post(tok + "x"); code != http.StatusForbidden {
		t.Fatalf("tampered token: %d", code)
	}
	if code := post(tok); code != http.StatusSeeOther || len(ctrl.wakes) != 1 {
		t.Fatalf("valid token: %d, wakes %v", code, ctrl.wakes)
	}
	now = now.Add(tokenTTL + time.Second)
	if code := post(tok); code != http.StatusForbidden {
		t.Fatalf("expired token: %d", code)
	}
}

func TestHealthFollowsTheController(t *testing.T) {
	ctrl := &fakeCtrl{healthy: true}
	if code := get(Health(ctrl), "/health", "").Code; code != http.StatusOK {
		t.Fatalf("asleep: %d", code)
	}
	ctrl.healthy = false
	if code := get(Health(ctrl), "/health", "").Code; code != http.StatusServiceUnavailable {
		t.Fatalf("awake: %d", code)
	}
}

func TestLogoIsSandboxed(t *testing.T) {
	dir := t.TempDir()
	svg := `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`
	if err := os.WriteFile(filepath.Join(dir, "logo.svg"), []byte(svg), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Tap.URL = "http://app:9090/activity"
	cfg.Page.Logo = "logo.svg"
	s, err := New(&fakeCtrl{}, &cfg, dir)
	if err != nil {
		t.Fatal(err)
	}
	rec := get(s.Traffic(), "/_standby/logo", "")
	if !strings.Contains(rec.Header().Get("Content-Security-Policy"), "sandbox") || rec.Header().Get("Content-Type") != "image/svg+xml" {
		t.Fatalf("logo headers: %v", rec.Header())
	}
	if err := os.WriteFile(filepath.Join(dir, "logo.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.Page.Logo = "logo.txt"
	if _, err := New(&fakeCtrl{}, &cfg, dir); err == nil {
		t.Fatal("accepted a logo that is neither PNG nor SVG")
	}
}

func TestShowSetupDescribesTheRules(t *testing.T) {
	s, ctrl := newServer(t, func(c *config.Config) {
		c.Wake.OnVisit, c.Wake.Button, c.Page.ShowSetup = false, true, true
		c.Schedule = &config.Schedule{Timezone: "Europe/Paris", Days: []string{"mon", "fri"}, Awake: []string{"08:00-12:00", "14:00-18:00"}}
	})
	body := get(s.Traffic(), "/", "text/html").Body.String()
	for _, want := range []string{"Mise en pause après 1 h sans trafic", "Réveil avec le bouton Démarrer", "08:00-12:00, 14:00-18:00 (lun ven, Europe/Paris)", "Clique sur Démarrer"} {
		if !strings.Contains(body, want) {
			t.Errorf("page does not say %q", want)
		}
	}
	if len(ctrl.wakes) != 0 {
		t.Fatalf("a visit woke the stack in button mode: %v", ctrl.wakes)
	}
}

func TestPageIsPersonalised(t *testing.T) {
	dir := t.TempDir()
	png := []byte("\x89PNG\r\n\x1a\nrest")
	if err := os.WriteFile(filepath.Join(dir, "logo.png"), png, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Tap.URL = "http://app:9090/activity"
	cfg.Page.AppName = "TenderCrunch"
	cfg.Page.Logo = "logo.png"
	cfg.Page.Primary = "#3d9e8f"
	s, err := New(&fakeCtrl{status: controller.Status{Phase: controller.Asleep, CanWake: true}}, &cfg, dir)
	if err != nil {
		t.Fatal(err)
	}
	body := get(s.Traffic(), "/", "text/html").Body.String()
	for _, want := range []string{"On rallume TenderCrunch", "TenderCrunch s&#39;était mis en veille", "ne pas dépenser d&#39;énergie", `rel="icon" href="/_standby/logo"`, `class="logo"`} {
		if !strings.Contains(body, want) {
			t.Errorf("page does not contain %q", want)
		}
	}
	if css := get(s.Traffic(), "/_standby/style.css", "").Body.String(); !strings.Contains(css, "--primary:#3d9e8f") {
		t.Errorf("colour missing from the stylesheet")
	}
}

func TestReasonCanBeReplacedOrHidden(t *testing.T) {
	s, _ := newServer(t, func(c *config.Config) { c.Page.Reason = "Pour la planète <b>et</b> ta facture." })
	body := get(s.Traffic(), "/", "text/html").Body.String()
	if !strings.Contains(body, "Pour la planète &lt;b&gt;et&lt;/b&gt; ta facture.") {
		t.Fatal("custom reason missing or not escaped")
	}
	s, _ = newServer(t, func(c *config.Config) { c.Page.ShowReason = false })
	if body := get(s.Traffic(), "/", "text/html").Body.String(); strings.Contains(body, `class="reason"`) {
		t.Fatal("reason shown although show_reason is false")
	}
}

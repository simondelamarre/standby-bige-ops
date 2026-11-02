package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadAppliesDefaults(t *testing.T) {
	cfg, err := Load(write(t, `{"version": 1, "tap": {"url": "http://app:9090/activity"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.IdleAfter.Duration != 60*time.Minute || !cfg.Wake.OnVisit || cfg.Wake.Button || cfg.Page.Lang != "fr" {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
}

func TestLoadRejects(t *testing.T) {
	cases := map[string]string{
		"unknown field":   `{"version": 1, "tap": {"url": "http://a:1/x"}, "debug": true}`,
		"missing tap":     `{"version": 1}`,
		"short idle":      `{"version": 1, "tap": {"url": "http://a:1/x"}, "idle_after": "30s"}`,
		"bad colour":      `{"version": 1, "tap": {"url": "http://a:1/x"}, "page": {"primary_color": "red;}"}}`,
		"script contact":  `{"version": 1, "tap": {"url": "http://a:1/x"}, "page": {"contact": "javascript:alert(1)"}}`,
		"nothing wakes":   `{"version": 1, "tap": {"url": "http://a:1/x"}, "wake": {"on_visit": false}}`,
		"same ports":      `{"version": 1, "tap": {"url": "http://a:1/x"}, "listen": ":80", "health_listen": ":80"}`,
		"outscale no key": `{"version": 1, "tap": {"url": "http://a:1/x"}, "driver": {"name": "outscale", "region": "eu-west-2"}}`,
		"unknown driver":  `{"version": 1, "tap": {"url": "http://a:1/x"}, "driver": {"name": "gcp"}}`,
		"trailing data":   `{"version": 1, "tap": {"url": "http://a:1/x"}} {}`,
		"crossing window": `{"version": 1, "tap": {"url": "http://a:1/x"}, "schedule": {"timezone": "UTC", "days": ["mon"], "awake": ["22:00-06:00"]}}`,
		"empty taps":      `{"version": 1, "tap": {"urls": []}}`,
		"blank tap":       `{"version": 1, "tap": {"urls": ["http://a:1/x", ""]}}`,
		"script tap":      `{"version": 1, "tap": {"urls": ["http://a:1/x", "file:///etc/passwd"]}}`,
	}
	for name, body := range cases {
		if _, err := Load(write(t, body)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestValidateReportsEveryProblem(t *testing.T) {
	cfg := Default()
	cfg.Caps.WakesPerDay = 0
	cfg.Page.Lang = "de"
	err := cfg.Validate()
	if err == nil {
		t.Fatal("accepted")
	}
	for _, want := range []string{"tap.url", "wakes_per_day", "page.lang"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %s", err, want)
		}
	}
}

func TestSeveralTapsWhenSeveralMachinesServeTheApp(t *testing.T) {
	cfg, err := Load(write(t, `{"version": 1, "tap": {"urls": ["http://10.0.0.5:9090/activity", "http://10.0.10.7:9090/activity"]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Tap.All(); len(got) != 2 || got[1] != "http://10.0.10.7:9090/activity" {
		t.Fatalf("taps = %v", got)
	}
	both := Tap{URL: "http://a:1/x", URLs: []string{"http://b:1/x"}}
	if got := both.All(); len(got) != 2 || got[0] != "http://a:1/x" {
		t.Fatalf("taps = %v", got)
	}
}

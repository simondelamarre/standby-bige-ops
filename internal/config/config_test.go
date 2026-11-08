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

func TestOVHDriver(t *testing.T) {
	const project = `"service_name": "0123456789abcdef0123456789abcdef"`
	const key = `"credentials_file": "/etc/standby/ovh.json"`
	cases := []struct {
		name     string
		driver   string
		endpoint string
		wantErr  string
	}{
		{name: "default endpoint", driver: `{"name": "ovh", ` + project + `, ` + key + `}`, endpoint: "ovh-eu"},
		{name: "explicit endpoint", driver: `{"name": "ovh", "endpoint": "ovh-ca", ` + project + `, ` + key + `}`, endpoint: "ovh-ca"},
		{name: "missing service name", driver: `{"name": "ovh", ` + key + `}`, wantErr: "driver.service_name"},
		{name: "missing credentials", driver: `{"name": "ovh", ` + project + `}`, wantErr: "driver.credentials_file"},
		{name: "bad endpoint", driver: `{"name": "ovh", "endpoint": "ovh-fr", ` + project + `, ` + key + `}`, wantErr: "driver.endpoint"},
		{name: "empty endpoint", driver: `{"name": "ovh", "endpoint": "", ` + project + `, ` + key + `}`, wantErr: "driver.endpoint"},
		{name: "path in service name", driver: `{"name": "ovh", "service_name": "abc/../other", ` + key + `}`, wantErr: "driver.service_name"},
		{name: "unknown field", driver: `{"name": "ovh", "application_key": "x", ` + project + `, ` + key + `}`, wantErr: "application_key"},
		{name: "unknown driver lists ovh", driver: `{"name": "gcp"}`, wantErr: "docker, outscale, ovh"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg, err := Load(write(t, `{"version": 1, "tap": {"url": "http://a:1/x"}, "driver": `+c.driver+`}`))
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("err = %v, want it to mention %s", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Driver.Endpoint != c.endpoint {
				t.Fatalf("endpoint = %q, want %q", cfg.Driver.Endpoint, c.endpoint)
			}
		})
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

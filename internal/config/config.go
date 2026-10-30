// Package config loads and validates the guardian configuration.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/simondelamarre/standby-bige-ops/internal/schedule"
)

// MaxFileSize bounds every JSON file the guardian reads.
const MaxFileSize = 1 << 20

// Duration is a time.Duration written as a Go duration string, such as "60m".
type Duration struct{ time.Duration }

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("duration must be a string such as \"60m\": %w", err)
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	d.Duration = v
	return nil
}

func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(d.String()) }

type Config struct {
	Version      int       `json:"version"`
	Listen       string    `json:"listen"`
	HealthListen string    `json:"health_listen"`
	Manifest     string    `json:"manifest"`
	State        string    `json:"state"`
	Driver       Driver    `json:"driver"`
	Schedule     *Schedule `json:"schedule,omitempty"`
	IdleAfter    Duration  `json:"idle_after"`
	LBSettle     Duration  `json:"lb_settle"`
	Tick         Duration  `json:"tick"`
	Wake         Wake      `json:"wake"`
	Caps         Caps      `json:"caps"`
	Tap          Tap       `json:"tap"`
	Page         Page      `json:"page"`
}

type Driver struct {
	Name         string `json:"name"`
	DockerSocket string `json:"docker_socket,omitempty"`
	// Outscale: region and a JSON key pair readable by its owner only.
	Region          string `json:"region,omitempty"`
	CredentialsFile string `json:"credentials_file,omitempty"`
}

type Schedule struct {
	Timezone string   `json:"timezone"`
	Days     []string `json:"days"`
	Awake    []string `json:"awake"`
}

type Wake struct {
	OnVisit  bool `json:"on_visit"`
	Button   bool `json:"button"`
	Schedule bool `json:"schedule"`
}

type Caps struct {
	WakesPerDay        int `json:"wakes_per_day"`
	AwakeHoursPerMonth int `json:"awake_hours_per_month"`
}

type Tap struct {
	URL string `json:"url,omitempty"`
}

type Page struct {
	Title      string `json:"title"`
	Message    string `json:"message"`
	Lang       string `json:"lang"`
	Contact    string `json:"contact,omitempty"`
	Logo       string `json:"logo,omitempty"`
	Primary    string `json:"primary_color"`
	Background string `json:"background_color"`
	Text       string `json:"text_color"`
	ShowETA    bool   `json:"show_eta"`
	// ShowSetup publishes the schedule and idle rule on the page and in the status.
	ShowSetup bool `json:"show_setup"`
}

// Default returns the configuration used for every field the file leaves out.
func Default() Config {
	return Config{
		Version:      1,
		Listen:       ":8080",
		HealthListen: ":9090",
		Manifest:     "manifest.json",
		State:        "state.json",
		Driver:       Driver{Name: "docker", DockerSocket: "/var/run/docker.sock"},
		IdleAfter:    Duration{60 * time.Minute},
		LBSettle:     Duration{15 * time.Second},
		Tick:         Duration{10 * time.Second},
		Wake:         Wake{OnVisit: true, Schedule: true},
		Caps:         Caps{WakesPerDay: 10, AwakeHoursPerMonth: 300},
		Page: Page{
			Lang:       "fr",
			Primary:    "#2563eb",
			Background: "#0b1020",
			Text:       "#e5e7eb",
			ShowETA:    true,
		},
	}
}

var colorRe = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// ReadFile reads at most MaxFileSize bytes.
func ReadFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, MaxFileSize+1))
	if err != nil {
		return nil, err
	}
	if len(b) > MaxFileSize {
		return nil, fmt.Errorf("%s is larger than %d bytes", path, MaxFileSize)
	}
	return b, nil
}

// DecodeStrict decodes one JSON document and rejects unknown fields.
func DecodeStrict(b []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("unexpected data after the JSON document")
	}
	return nil
}

// Load reads, completes and validates a configuration file.
func Load(path string) (*Config, error) {
	b, err := ReadFile(path)
	if err != nil {
		return nil, err
	}
	cfg := Default()
	if err := DecodeStrict(b, &cfg); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &cfg, nil
}

// Validate checks every field and reports all problems at once.
func (c *Config) Validate() error {
	var errs []error
	add := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }
	if c.Version != 1 {
		add("version: expected 1, got %d", c.Version)
	}
	if c.Listen == "" || c.HealthListen == "" {
		add("listen and health_listen are required")
	} else if c.Listen == c.HealthListen {
		add("listen and health_listen must differ")
	}
	switch c.Driver.Name {
	case "docker":
		if c.Driver.DockerSocket == "" {
			add("driver.docker_socket is required")
		}
	case "outscale":
		if c.Driver.Region == "" || c.Driver.CredentialsFile == "" {
			add("driver.region and driver.credentials_file are required for outscale")
		}
	default:
		add("driver.name %q is not supported (docker, outscale)", c.Driver.Name)
	}
	if c.Schedule != nil {
		if _, err := schedule.Parse(c.Schedule.Timezone, c.Schedule.Days, c.Schedule.Awake); err != nil {
			errs = append(errs, err)
		}
	}
	if c.IdleAfter.Duration < time.Minute {
		add("idle_after must be at least 1m")
	}
	if c.LBSettle.Duration < 0 || c.LBSettle.Duration > 5*time.Minute {
		add("lb_settle must be between 0s and 5m")
	}
	if c.Tick.Duration < time.Second || c.Tick.Duration > time.Minute {
		add("tick must be between 1s and 1m")
	}
	if !c.Wake.OnVisit && !c.Wake.Button && !(c.Wake.Schedule && c.Schedule != nil) {
		add("wake: nothing can wake the stack (enable on_visit, button or a schedule)")
	}
	if c.Tap.URL == "" {
		add("tap.url is required: without the activity probe the guardian cannot tell whether someone uses the app")
	} else if u, err := url.Parse(c.Tap.URL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		add("tap.url must be an http(s) URL")
	}
	if c.Caps.WakesPerDay < 1 {
		add("caps.wakes_per_day must be at least 1")
	}
	if c.Caps.AwakeHoursPerMonth < 1 || c.Caps.AwakeHoursPerMonth > 744 {
		add("caps.awake_hours_per_month must be between 1 and 744")
	}
	p := c.Page
	if p.Lang != "fr" && p.Lang != "en" {
		add("page.lang must be fr or en")
	}
	if len(p.Title) > 80 || len(p.Message) > 500 {
		add("page.title is limited to 80 characters and page.message to 500")
	}
	if p.Contact != "" && !strings.HasPrefix(p.Contact, "https://") && !strings.HasPrefix(p.Contact, "mailto:") {
		add("page.contact must start with https:// or mailto:")
	}
	for name, v := range map[string]string{"primary_color": p.Primary, "background_color": p.Background, "text_color": p.Text} {
		if !colorRe.MatchString(v) {
			add("page.%s must look like #1a2b3c", name)
		}
	}
	return errors.Join(errs...)
}

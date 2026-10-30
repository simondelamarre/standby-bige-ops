// Package manifest describes the machines a guardian may drive, in wake order.
package manifest

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"

	"github.com/simondelamarre/standby-bige-ops/internal/config"
)

type Probe struct {
	Kind   string `json:"kind"`
	Target string `json:"target"`
}

type Resource struct {
	ID        string  `json:"id"`
	Name      string  `json:"name,omitempty"`
	Role      string  `json:"role,omitempty"`
	Probe     *Probe  `json:"probe,omitempty"`
	HourlyEUR float64 `json:"hourly_eur,omitempty"`
}

// Manifest lists the machines in wake order; sleep runs the waves in reverse.
type Manifest struct {
	Version  int          `json:"version"`
	Stack    string       `json:"stack"`
	Provider string       `json:"provider"`
	Waves    [][]Resource `json:"waves"`
	AppProbe Probe        `json:"app_probe"`
}

var idRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

// Load reads and validates a manifest file.
func Load(path string) (*Manifest, error) {
	b, err := config.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := config.DecodeStrict(b, &m); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := m.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &m, nil
}

// Validate checks ids, probes and wave structure.
func (m *Manifest) Validate() error {
	var errs []error
	add := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }
	if m.Version != 1 {
		add("version: expected 1, got %d", m.Version)
	}
	if m.Stack == "" || len(m.Stack) > 128 {
		add("stack: 1 to 128 characters")
	}
	if m.Provider == "" {
		add("provider is required")
	}
	if len(m.Waves) == 0 {
		add("waves: at least one wave is required")
	}
	seen := map[string]bool{}
	for i, wave := range m.Waves {
		if len(wave) == 0 {
			add("waves[%d] is empty", i)
		}
		for _, r := range wave {
			if !idRe.MatchString(r.ID) {
				add("waves[%d]: invalid id %q", i, r.ID)
			}
			if seen[r.ID] {
				add("waves[%d]: duplicate id %q", i, r.ID)
			}
			seen[r.ID] = true
			if r.HourlyEUR < 0 {
				add("%s: hourly_eur must not be negative", r.ID)
			}
			if r.Probe != nil {
				if err := r.Probe.Validate(); err != nil {
					add("%s: %v", r.ID, err)
				}
			}
		}
	}
	if err := m.AppProbe.Validate(); err != nil {
		add("app_probe: %v", err)
	}
	return errors.Join(errs...)
}

// Validate checks that the probe target matches its kind.
func (p Probe) Validate() error {
	if len(p.Target) == 0 || len(p.Target) > 512 {
		return errors.New("probe target: 1 to 512 characters")
	}
	switch p.Kind {
	case "tcp":
		if _, _, err := net.SplitHostPort(p.Target); err != nil {
			return fmt.Errorf("tcp probe target must be host:port: %w", err)
		}
	case "http":
		u, err := url.Parse(p.Target)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("http probe target must be an http(s) URL, got %q", p.Target)
		}
	default:
		return fmt.Errorf("probe kind must be tcp or http, got %q", p.Kind)
	}
	return nil
}

// IDs returns every machine id in wake order.
func (m *Manifest) IDs() []string {
	var ids []string
	for _, wave := range m.Waves {
		for _, r := range wave {
			ids = append(ids, r.ID)
		}
	}
	return ids
}

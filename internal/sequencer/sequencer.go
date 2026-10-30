// Package sequencer wakes a stack wave by wave and puts it to sleep in reverse.
package sequencer

import (
	"context"
	"fmt"
	"time"

	"github.com/simondelamarre/standby-bige-ops/internal/driver"
	"github.com/simondelamarre/standby-bige-ops/internal/manifest"
)

// Progress counts finished steps: one per wave, plus the app probe.
type Progress struct {
	Step  int
	Steps int
}

type Sequencer struct {
	Driver       driver.Driver
	Probe        func(context.Context, manifest.Probe) error
	Poll         time.Duration
	StateTimeout time.Duration
	ProbeTimeout time.Duration
}

// Wake starts each wave once the previous one runs and passes its probes,
// then waits for the app probe.
func (s *Sequencer) Wake(ctx context.Context, m *manifest.Manifest, report func(Progress)) error {
	steps := len(m.Waves) + 1
	for i, wave := range m.Waves {
		report(Progress{Step: i, Steps: steps})
		ids := idsOf(wave)
		states, err := s.Driver.States(ctx, ids)
		if err != nil {
			return fmt.Errorf("wave %d: %w", i+1, err)
		}
		var todo []string
		for _, id := range ids {
			switch states[id] {
			case driver.Missing:
				return fmt.Errorf("wave %d: %s no longer exists", i+1, id)
			case driver.Running:
			default:
				todo = append(todo, id)
			}
		}
		if len(todo) > 0 {
			if err := s.Driver.Start(ctx, todo); err != nil {
				return fmt.Errorf("wave %d: %w", i+1, err)
			}
		}
		if err := s.waitState(ctx, ids, driver.Running); err != nil {
			return fmt.Errorf("wave %d: %w", i+1, err)
		}
		for _, r := range wave {
			if r.Probe == nil {
				continue
			}
			if err := s.waitProbe(ctx, *r.Probe); err != nil {
				return fmt.Errorf("wave %d: %s: %w", i+1, r.ID, err)
			}
		}
	}
	report(Progress{Step: len(m.Waves), Steps: steps})
	if err := s.waitProbe(ctx, m.AppProbe); err != nil {
		return fmt.Errorf("app: %w", err)
	}
	report(Progress{Step: steps, Steps: steps})
	return nil
}

// Sleep stops the waves from the last to the first.
func (s *Sequencer) Sleep(ctx context.Context, m *manifest.Manifest) error {
	for i := len(m.Waves) - 1; i >= 0; i-- {
		ids := idsOf(m.Waves[i])
		states, err := s.Driver.States(ctx, ids)
		if err != nil {
			return fmt.Errorf("wave %d: %w", i+1, err)
		}
		var todo []string
		for _, id := range ids {
			switch states[id] {
			case driver.Missing:
				return fmt.Errorf("wave %d: %s no longer exists", i+1, id)
			case driver.Stopped:
			default:
				todo = append(todo, id)
			}
		}
		if len(todo) > 0 {
			if err := s.Driver.Stop(ctx, todo); err != nil {
				return fmt.Errorf("wave %d: %w", i+1, err)
			}
		}
		if err := s.waitState(ctx, ids, driver.Stopped); err != nil {
			return fmt.Errorf("wave %d: %w", i+1, err)
		}
	}
	return nil
}

func (s *Sequencer) waitState(ctx context.Context, ids []string, want driver.State) error {
	ctx, cancel := context.WithTimeout(ctx, s.StateTimeout)
	defer cancel()
	for {
		states, err := s.Driver.States(ctx, ids)
		if err == nil && all(states, ids, want) {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for %s", want)
		case <-time.After(s.Poll):
		}
	}
}

func (s *Sequencer) waitProbe(ctx context.Context, p manifest.Probe) error {
	ctx, cancel := context.WithTimeout(ctx, s.ProbeTimeout)
	defer cancel()
	for {
		err := s.Probe(ctx, p)
		if err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("probe %s %s never answered: %w", p.Kind, p.Target, err)
		case <-time.After(s.Poll):
		}
	}
}

func idsOf(wave []manifest.Resource) []string {
	ids := make([]string, len(wave))
	for i, r := range wave {
		ids[i] = r.ID
	}
	return ids
}

func all(states map[string]driver.State, ids []string, want driver.State) bool {
	for _, id := range ids {
		if states[id] != want {
			return false
		}
	}
	return true
}

// Package drivertest provides an in-memory driver for tests.
package drivertest

import (
	"context"
	"sync"

	"github.com/simondelamarre/standby-bige-ops/internal/driver"
)

// Fake switches states instantly and records every call as "start:id" or "stop:id".
type Fake struct {
	mu     sync.Mutex
	states map[string]driver.State
	calls  []string
}

func New(states map[string]driver.State) *Fake {
	return &Fake{states: states}
}

func (f *Fake) States(_ context.Context, ids []string) (map[string]driver.State, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]driver.State, len(ids))
	for _, id := range ids {
		s, ok := f.states[id]
		if !ok {
			s = driver.Missing
		}
		out[id] = s
	}
	return out, nil
}

func (f *Fake) Start(_ context.Context, ids []string) error {
	return f.set(ids, driver.Running, "start:")
}

func (f *Fake) Stop(_ context.Context, ids []string) error {
	return f.set(ids, driver.Stopped, "stop:")
}

func (f *Fake) set(ids []string, s driver.State, prefix string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, id := range ids {
		f.states[id] = s
		f.calls = append(f.calls, prefix+id)
	}
	return nil
}

// Calls returns a copy of the recorded calls.
func (f *Fake) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

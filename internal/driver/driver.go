// Package driver defines how the guardian starts and stops machines on a provider.
package driver

import (
	"context"
	"fmt"
)

type State string

const (
	Running  State = "running"
	Stopped  State = "stopped"
	Changing State = "changing"
	Missing  State = "missing"
)

// Driver starts and stops machines. Implementations must never create,
// modify or delete anything else.
type Driver interface {
	States(ctx context.Context, ids []string) (map[string]State, error)
	Start(ctx context.Context, ids []string) error
	Stop(ctx context.Context, ids []string) error
}

// Guard refuses any machine that is not listed in the manifest.
type Guard struct {
	inner   Driver
	allowed map[string]bool
}

func NewGuard(inner Driver, ids []string) *Guard {
	allowed := make(map[string]bool, len(ids))
	for _, id := range ids {
		allowed[id] = true
	}
	return &Guard{inner: inner, allowed: allowed}
}

func (g *Guard) check(ids []string) error {
	for _, id := range ids {
		if !g.allowed[id] {
			return fmt.Errorf("refusing %q: not in the manifest", id)
		}
	}
	return nil
}

func (g *Guard) States(ctx context.Context, ids []string) (map[string]State, error) {
	if err := g.check(ids); err != nil {
		return nil, err
	}
	return g.inner.States(ctx, ids)
}

func (g *Guard) Start(ctx context.Context, ids []string) error {
	if err := g.check(ids); err != nil {
		return err
	}
	return g.inner.Start(ctx, ids)
}

func (g *Guard) Stop(ctx context.Context, ids []string) error {
	if err := g.check(ids); err != nil {
		return err
	}
	return g.inner.Stop(ctx, ids)
}

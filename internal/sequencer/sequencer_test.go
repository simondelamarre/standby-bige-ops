package sequencer

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/simondelamarre/standby-bige-ops/internal/driver"
	"github.com/simondelamarre/standby-bige-ops/internal/driver/drivertest"
	"github.com/simondelamarre/standby-bige-ops/internal/manifest"
)

func demo() *manifest.Manifest {
	return &manifest.Manifest{
		Version: 1, Stack: "demo", Provider: "docker",
		Waves: [][]manifest.Resource{
			{{ID: "db", Probe: &manifest.Probe{Kind: "tcp", Target: "db:6379"}}},
			{{ID: "app"}, {ID: "tap"}},
		},
		AppProbe: manifest.Probe{Kind: "http", Target: "http://app:9090/health"},
	}
}

func seq(d driver.Driver, probe func(context.Context, manifest.Probe) error) *Sequencer {
	return &Sequencer{Driver: d, Probe: probe, Poll: time.Millisecond, StateTimeout: time.Second, ProbeTimeout: 50 * time.Millisecond}
}

func TestWakeStartsWavesInOrderAndSleepReverses(t *testing.T) {
	fake := drivertest.New(map[string]driver.State{"db": driver.Stopped, "app": driver.Stopped, "tap": driver.Stopped})
	var probed []string
	s := seq(fake, func(_ context.Context, p manifest.Probe) error {
		probed = append(probed, p.Target)
		return nil
	})
	var steps []int
	if err := s.Wake(context.Background(), demo(), func(p Progress) { steps = append(steps, p.Step) }); err != nil {
		t.Fatal(err)
	}
	if err := s.Sleep(context.Background(), demo()); err != nil {
		t.Fatal(err)
	}
	want := []string{"start:db", "start:app", "start:tap", "stop:app", "stop:tap", "stop:db"}
	if got := fake.Calls(); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
	if !reflect.DeepEqual(probed, []string{"db:6379", "http://app:9090/health"}) {
		t.Fatalf("probes = %v", probed)
	}
	if !reflect.DeepEqual(steps, []int{0, 1, 2, 3}) {
		t.Fatalf("steps = %v", steps)
	}
}

func TestWakeStopsAtAFailingProbe(t *testing.T) {
	fake := drivertest.New(map[string]driver.State{"db": driver.Stopped, "app": driver.Stopped, "tap": driver.Stopped})
	s := seq(fake, func(context.Context, manifest.Probe) error { return errors.New("refused") })
	if err := s.Wake(context.Background(), demo(), func(Progress) {}); err == nil {
		t.Fatal("wake succeeded with a dead database")
	}
	if got := fake.Calls(); !reflect.DeepEqual(got, []string{"start:db"}) {
		t.Fatalf("the app started before its database answered: %v", got)
	}
}

func TestWakeRefusesAMissingMachine(t *testing.T) {
	fake := drivertest.New(map[string]driver.State{"app": driver.Stopped, "tap": driver.Stopped})
	s := seq(fake, func(context.Context, manifest.Probe) error { return nil })
	if err := s.Wake(context.Background(), demo(), func(Progress) {}); err == nil {
		t.Fatal("wake ignored a missing machine")
	}
}

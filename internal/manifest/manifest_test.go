package manifest

import "testing"

func valid() Manifest {
	return Manifest{
		Version: 1, Stack: "demo", Provider: "docker",
		Waves: [][]Resource{
			{{ID: "db", Probe: &Probe{Kind: "tcp", Target: "db:6379"}}},
			{{ID: "app"}, {ID: "tap"}},
		},
		AppProbe: Probe{Kind: "http", Target: "http://app:9090/health"},
	}
}

func TestValidate(t *testing.T) {
	m := valid()
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := m.IDs(); len(got) != 3 || got[0] != "db" || got[2] != "tap" {
		t.Fatalf("IDs() = %v", got)
	}
}

func TestValidateRejects(t *testing.T) {
	cases := map[string]func(*Manifest){
		"duplicate id":  func(m *Manifest) { m.Waves[1][0].ID = "db" },
		"path in id":    func(m *Manifest) { m.Waves[1][0].ID = "../etc" },
		"empty wave":    func(m *Manifest) { m.Waves = append(m.Waves, nil) },
		"no waves":      func(m *Manifest) { m.Waves = nil },
		"bad tcp probe": func(m *Manifest) { m.Waves[0][0].Probe.Target = "db" },
		"file url":      func(m *Manifest) { m.AppProbe = Probe{Kind: "http", Target: "file:///etc/passwd"} },
		"probe kind":    func(m *Manifest) { m.AppProbe.Kind = "icmp" },
		"version":       func(m *Manifest) { m.Version = 2 },
	}
	for name, mutate := range cases {
		m := valid()
		mutate(&m)
		if err := m.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

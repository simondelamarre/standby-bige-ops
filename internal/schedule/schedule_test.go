package schedule

import (
	"testing"
	"time"
)

func TestContains(t *testing.T) {
	w, err := Parse("Europe/Paris", []string{"mon", "tue", "wed", "thu", "fri"}, []string{"08:45-12:00", "14:00-18:00"})
	if err != nil {
		t.Fatal(err)
	}
	paris, _ := time.LoadLocation("Europe/Paris")
	cases := []struct {
		at   time.Time
		want bool
	}{
		{time.Date(2026, 11, 2, 8, 44, 0, 0, paris), false},  // Monday, before
		{time.Date(2026, 11, 2, 8, 45, 0, 0, paris), true},   // Monday, start is inclusive
		{time.Date(2026, 11, 2, 11, 59, 0, 0, paris), true},  // morning range
		{time.Date(2026, 11, 2, 12, 30, 0, 0, paris), false}, // lunch break
		{time.Date(2026, 11, 2, 14, 0, 0, 0, paris), true},   // afternoon range
		{time.Date(2026, 11, 2, 17, 59, 0, 0, paris), true},
		{time.Date(2026, 11, 2, 18, 0, 0, 0, paris), false},  // end is exclusive
		{time.Date(2026, 10, 31, 10, 0, 0, 0, paris), false}, // Saturday
		{time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC), true}, // 09:00 in Paris
	}
	for _, c := range cases {
		if got := w.Contains(c.at); got != c.want {
			t.Errorf("Contains(%s) = %v, want %v", c.at, got, c.want)
		}
	}
}

func TestParseRejects(t *testing.T) {
	bad := []struct {
		tz    string
		awake []string
		days  []string
	}{
		{"", []string{"08:00-18:00"}, []string{"mon"}},
		{"Europe/Paris", []string{"18:00-08:00"}, []string{"mon"}},
		{"Europe/Paris", []string{"8:00-18:00"}, []string{"mon"}},
		{"Europe/Paris", []string{"08:00-18:00"}, []string{"monday"}},
		{"Europe/Paris", []string{"08:00-18:00"}, nil},
		{"Mars/Olympus", []string{"08:00-18:00"}, []string{"mon"}},
		{"Europe/Paris", nil, []string{"mon"}},
	}
	for _, b := range bad {
		if _, err := Parse(b.tz, b.days, b.awake); err == nil {
			t.Errorf("Parse(%q, %v, %q) accepted", b.tz, b.days, b.awake)
		}
	}
}

// Package schedule decides whether a moment falls inside the awake hours.
package schedule

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	// Embedded zone data: the guardian image has no /usr/share/zoneinfo.
	_ "time/tzdata"
)

var dayNames = map[string]time.Weekday{
	"sun": time.Sunday, "mon": time.Monday, "tue": time.Tuesday, "wed": time.Wednesday,
	"thu": time.Thursday, "fri": time.Friday, "sat": time.Saturday,
}

// span is a range of minutes after midnight: from inclusive, to exclusive.
type span struct{ from, to int }

// Window holds the weekly awake hours, such as weekdays 08:00-12:00 and
// 14:00-18:00 in Europe/Paris.
type Window struct {
	loc   *time.Location
	days  [7]bool
	spans []span
}

// Parse builds a window from a time zone, day names and "HH:MM-HH:MM" ranges.
func Parse(timezone string, days []string, awake []string) (*Window, error) {
	if timezone == "" {
		return nil, errors.New("schedule: timezone is required")
	}
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		return nil, fmt.Errorf("schedule: %w", err)
	}
	if len(days) == 0 {
		return nil, errors.New("schedule: at least one day is required")
	}
	if len(awake) == 0 {
		return nil, errors.New("schedule: at least one awake range is required")
	}
	w := &Window{loc: loc}
	for _, d := range days {
		wd, ok := dayNames[strings.ToLower(strings.TrimSpace(d))]
		if !ok {
			return nil, fmt.Errorf("schedule: unknown day %q (use mon..sun)", d)
		}
		w.days[wd] = true
	}
	for _, r := range awake {
		s, err := parseRange(r)
		if err != nil {
			return nil, err
		}
		w.spans = append(w.spans, s)
	}
	return w, nil
}

func parseRange(r string) (span, error) {
	start, end, ok := strings.Cut(r, "-")
	if !ok {
		return span{}, fmt.Errorf("schedule: awake range must look like 08:00-12:00, got %q", r)
	}
	from, err := parseClock(start)
	if err != nil {
		return span{}, err
	}
	to, err := parseClock(end)
	if err != nil {
		return span{}, err
	}
	if from >= to {
		return span{}, fmt.Errorf("schedule: %q must start before it ends and not cross midnight", r)
	}
	return span{from, to}, nil
}

func parseClock(s string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "24:00" {
		return 24 * 60, nil
	}
	hh, mm, ok := strings.Cut(s, ":")
	h, errH := strconv.Atoi(hh)
	m, errM := strconv.Atoi(mm)
	if !ok || errH != nil || errM != nil || len(hh) != 2 || len(mm) != 2 || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, fmt.Errorf("schedule: invalid time %q (use HH:MM)", s)
	}
	return h*60 + m, nil
}

// Contains reports whether t falls inside one of the awake ranges.
func (w *Window) Contains(t time.Time) bool {
	lt := t.In(w.loc)
	if !w.days[lt.Weekday()] {
		return false
	}
	m := lt.Hour()*60 + lt.Minute()
	for _, s := range w.spans {
		if m >= s.from && m < s.to {
			return true
		}
	}
	return false
}

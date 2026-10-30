// Package store keeps the small history the guardian needs across restarts:
// recent resume durations and the counters behind the caps. Losing it is harmless.
package store

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/simondelamarre/standby-bige-ops/internal/config"
)

const keepResumes = 5

type Data struct {
	ResumeSeconds []float64 `json:"resume_seconds"`
	WakeDay       string    `json:"wake_day"`
	WakesToday    int       `json:"wakes_today"`
	Month         string    `json:"month"`
	AwakeSeconds  float64   `json:"awake_seconds"`
}

// Store is not safe for concurrent use; the controller serialises access.
type Store struct {
	path string
	Data
}

// Open loads the store, or starts empty when the file does not exist yet.
func Open(path string) (*Store, error) {
	s := &Store{path: path}
	b, err := config.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &s.Data); err != nil {
		return nil, err
	}
	return s, nil
}

// Save writes the store atomically, readable by its owner only.
func (s *Store) Save() error {
	b, err := json.MarshalIndent(s.Data, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".state-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.path)
}

func (s *Store) AddResume(seconds float64) {
	s.ResumeSeconds = append(s.ResumeSeconds, seconds)
	if n := len(s.ResumeSeconds); n > keepResumes {
		s.ResumeSeconds = s.ResumeSeconds[n-keepResumes:]
	}
}

// MedianResume is the median of the last resumes, if any.
func (s *Store) MedianResume() (float64, bool) {
	if len(s.ResumeSeconds) == 0 {
		return 0, false
	}
	v := append([]float64(nil), s.ResumeSeconds...)
	sort.Float64s(v)
	return v[len(v)/2], true
}

func (s *Store) CountWake(now time.Time) {
	day := now.UTC().Format("2006-01-02")
	if s.WakeDay != day {
		s.WakeDay, s.WakesToday = day, 0
	}
	s.WakesToday++
}

func (s *Store) WakesOn(now time.Time) int {
	if s.WakeDay != now.UTC().Format("2006-01-02") {
		return 0
	}
	return s.WakesToday
}

func (s *Store) AddAwake(now time.Time, d time.Duration) {
	month := now.UTC().Format("2006-01")
	if s.Month != month {
		s.Month, s.AwakeSeconds = month, 0
	}
	s.AwakeSeconds += d.Seconds()
}

func (s *Store) AwakeHours(now time.Time) float64 {
	if s.Month != now.UTC().Format("2006-01") {
		return 0
	}
	return s.AwakeSeconds / 3600
}

// Package scheduler applies screen on/off based on a screen schedule pushed
// from the server heartbeat. The schedule is updated concurrently by the
// heartbeat loop; the ticker applies it every 30 s.
//
// When a Commander is wired in (via WithCommander), the scheduler switches
// Chromium between the kiosk URL and a plain black data: HTML page.
package scheduler

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/marketing-signage/player/internal/api"
)

// blackScreenURL is loaded into Chromium when the schedule says the screen
// should be off. It renders a plain black page with no content.
const blackScreenURL = "data:text/html,<html><body style=\"margin:0;background:black\"></body></html>"

// Commander is satisfied by *supervisor.Supervisor. When set, the scheduler
// switches the Chromium URL for on/off.
type Commander interface {
	SwitchURL(string) error
}

// Scheduler holds the current screen schedule and drives the display on/off.
type Scheduler struct {
	log      *slog.Logger
	mu       sync.RWMutex
	schedule api.ScreenSchedule
	lastOn   *bool // last applied state; nil = unknown

	commander Commander
	kioskURL  string
}

// New returns a Scheduler. If log is nil, slog.Default() is used.
func New(log *slog.Logger) *Scheduler {
	if log == nil {
		log = slog.Default()
	}
	return &Scheduler{log: log.With("subsystem", "scheduler")}
}

// WithCommander wires a Chromium supervisor into the scheduler. When set,
// schedule transitions switch the Chromium URL to a black screen (off) or
// back to kioskURL (on).
func (s *Scheduler) WithCommander(c Commander, kioskURL string) {
	s.commander = c
	s.kioskURL = kioskURL
}

// Update is called by the heartbeat loop on every successful response.
// It is safe to call from any goroutine.
func (s *Scheduler) Update(sched api.ScreenSchedule) {
	s.mu.Lock()
	s.schedule = sched
	s.mu.Unlock()
}

// Run ticks every 30 s and applies the schedule. It satisfies runtime.Subsystem.
func (s *Scheduler) Run(ctx context.Context) error {
	s.apply()
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
			s.apply()
		}
	}
}

func (s *Scheduler) apply() {
	s.mu.RLock()
	sched := s.schedule
	s.mu.RUnlock()

	on := IsOn(sched, time.Now())
	if s.lastOn != nil && *s.lastOn == on {
		return
	}
	v := on
	s.lastOn = &v

	if s.commander != nil {
		url := s.kioskURL
		if !on {
			url = blackScreenURL
		}
		if err := s.commander.SwitchURL(url); err != nil {
			s.log.Warn("schedule URL switch failed", slog.Bool("want_on", on), slog.String("error", err.Error()))
			return
		}
	}

	s.log.Info("display toggled", slog.Bool("on", on))
}

// IsOn reports whether the screen should be on at the given moment.
// Exported for tests. If the schedule has no on/off times, always returns true.
func IsOn(sched api.ScreenSchedule, now time.Time) bool {
	if sched.On == "" || sched.Off == "" {
		return true
	}

	loc := time.UTC
	if sched.Timezone != "" {
		if l, err := time.LoadLocation(sched.Timezone); err == nil {
			loc = l
		}
	}
	now = now.In(loc)

	onH, onM, ok1 := parseHHMM(sched.On)
	offH, offM, ok2 := parseHHMM(sched.Off)
	if !ok1 || !ok2 {
		return true // unparseable schedule → safe default
	}

	cur := now.Hour()*60 + now.Minute()
	onMin := onH*60 + onM
	offMin := offH*60 + offM

	if onMin <= offMin {
		// Normal window: e.g. 07:00–23:00
		return cur >= onMin && cur < offMin
	}
	// Overnight window: e.g. 22:00–06:00
	return cur >= onMin || cur < offMin
}

func parseHHMM(s string) (h, m int, ok bool) {
	if len(s) < 5 || s[2] != ':' {
		return
	}
	h = int(s[0]-'0')*10 + int(s[1]-'0')
	m = int(s[3]-'0')*10 + int(s[4]-'0')
	ok = h >= 0 && h < 24 && m >= 0 && m < 60
	return
}

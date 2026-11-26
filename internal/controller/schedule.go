package controller

import (
	"fmt"
	"time"

	"github.com/mudler/agent-controller/api/v1alpha1"
	"github.com/mudler/agent-controller/internal/policy"
)

// StartingDeadline is how late a schedule tick may still start a run.
// Older missed ticks are skipped, like a CronJob with
// startingDeadlineSeconds 120.
const StartingDeadline = 120 * time.Second

// dueTickWindow bounds the search for missed ticks. Ticks before it are
// older than StartingDeadline anyway.
const dueTickWindow = 7 * 24 * time.Hour

func parseSchedule(s *v1alpha1.Schedule) (func(time.Time) time.Time, error) {
	sched, err := policy.CronParser.Parse(s.Cron)
	if err != nil {
		return nil, fmt.Errorf("parsing cron %q: %w", s.Cron, err)
	}
	loc, err := policy.LoadLocation(s.TimeZone)
	if err != nil {
		return nil, err
	}
	return func(t time.Time) time.Time { return sched.Next(t.In(loc)) }, nil
}

// NextTick returns the first tick strictly after the given time.
func NextTick(s *v1alpha1.Schedule, after time.Time) (time.Time, error) {
	next, err := parseSchedule(s)
	if err != nil {
		return time.Time{}, err
	}
	return next(after), nil
}

// DueTick returns the latest tick in (last, now]. due is false when there is
// no such tick or when it is older than StartingDeadline. tick is zero when
// no tick falls in the searched window.
func DueTick(s *v1alpha1.Schedule, last, now time.Time) (tick time.Time, due bool, err error) {
	next, err := parseSchedule(s)
	if err != nil {
		return time.Time{}, false, err
	}
	start := last
	if floor := now.Add(-dueTickWindow); start.Before(floor) {
		start = floor
	}
	for t := next(start); !t.After(now); t = next(t) {
		tick = t
	}
	if tick.IsZero() {
		return time.Time{}, false, nil
	}
	return tick, now.Sub(tick) <= StartingDeadline, nil
}

// ScheduledRunName returns the name of the run for a tick:
// <profile>-s<yyyymmddhhmm> in UTC. A retried tick gets the same name.
func ScheduledRunName(profile string, tick time.Time) string {
	return profile + "-s" + tick.UTC().Format("200601021504")
}

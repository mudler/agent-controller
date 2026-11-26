package controller

import (
	"strings"
	"testing"
	"time"

	"github.com/mudler/agent-controller/api/v1alpha1"
)

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return ts
}

func TestNextTickTimeZones(t *testing.T) {
	tests := []struct {
		name  string
		sched v1alpha1.Schedule
		after string
		want  string
	}{
		{name: "utc default", sched: v1alpha1.Schedule{Cron: "30 1,13 * * *"}, after: "2026-11-26T02:00:00Z", want: "2026-11-26T13:30:00Z"},
		{name: "explicit utc", sched: v1alpha1.Schedule{Cron: "30 1 * * *", TimeZone: "Etc/UTC"}, after: "2026-11-26T02:00:00Z", want: "2026-11-27T01:30:00Z"},
		{name: "rome winter", sched: v1alpha1.Schedule{Cron: "0 9 * * *", TimeZone: "Europe/Rome"}, after: "2026-01-10T00:00:00Z", want: "2026-01-10T08:00:00Z"},
		{name: "rome summer", sched: v1alpha1.Schedule{Cron: "0 9 * * *", TimeZone: "Europe/Rome"}, after: "2026-07-10T00:00:00Z", want: "2026-07-10T07:00:00Z"},
		// 2026-03-29 02:00 Europe/Rome jumps to 03:00.
		{name: "rome across spring forward", sched: v1alpha1.Schedule{Cron: "0 9 * * *", TimeZone: "Europe/Rome"}, after: "2026-03-28T09:00:00Z", want: "2026-03-29T07:00:00Z"},
		// 2026-10-25 03:00 Europe/Rome falls back to 02:00.
		{name: "rome across fall back", sched: v1alpha1.Schedule{Cron: "0 9 * * *", TimeZone: "Europe/Rome"}, after: "2026-10-24T08:00:00Z", want: "2026-10-25T08:00:00Z"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NextTick(&tt.sched, mustTime(t, tt.after))
			if err != nil {
				t.Fatal(err)
			}
			if want := mustTime(t, tt.want); !got.Equal(want) {
				t.Errorf("NextTick() = %s, want %s", got.UTC().Format(time.RFC3339), tt.want)
			}
		})
	}
}

func TestNextTickSpringForwardGap(t *testing.T) {
	// 02:30 does not exist in Europe/Rome on 2026-03-29. The tick must not
	// land on the day before or repeat.
	s := &v1alpha1.Schedule{Cron: "30 2 * * *", TimeZone: "Europe/Rome"}
	got, err := NextTick(s, mustTime(t, "2026-03-28T02:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	if !got.After(mustTime(t, "2026-03-28T02:00:00Z")) {
		t.Errorf("NextTick() = %s, want a time after the start", got)
	}
	again, err := NextTick(s, got)
	if err != nil {
		t.Fatal(err)
	}
	if !again.After(got) {
		t.Errorf("second tick %s is not after the first %s", again, got)
	}
}

func TestNextTickErrors(t *testing.T) {
	if _, err := NextTick(&v1alpha1.Schedule{Cron: "bad"}, time.Now()); err == nil {
		t.Error("bad cron: want error")
	}
	if _, err := NextTick(&v1alpha1.Schedule{Cron: "* * * * *", TimeZone: "Mars/Base"}, time.Now()); err == nil {
		t.Error("bad time zone: want error")
	}
}

func TestDueTick(t *testing.T) {
	every5 := &v1alpha1.Schedule{Cron: "*/5 * * * *"}
	tests := []struct {
		name     string
		last     string
		now      string
		wantTick string
		wantDue  bool
	}{
		{name: "tick just passed", last: "2026-11-26T10:00:00Z", now: "2026-11-26T10:05:30Z", wantTick: "2026-11-26T10:05:00Z", wantDue: true},
		{name: "tick exactly now", last: "2026-11-26T10:00:00Z", now: "2026-11-26T10:05:00Z", wantTick: "2026-11-26T10:05:00Z", wantDue: true},
		{name: "tick at deadline", last: "2026-11-26T10:00:00Z", now: "2026-11-26T10:07:00Z", wantTick: "2026-11-26T10:05:00Z", wantDue: true},
		{name: "tick missed", last: "2026-11-26T10:00:00Z", now: "2026-11-26T10:07:01Z", wantTick: "2026-11-26T10:05:00Z", wantDue: false},
		{name: "many missed, latest due", last: "2026-11-26T08:00:00Z", now: "2026-11-26T10:10:30Z", wantTick: "2026-11-26T10:10:00Z", wantDue: true},
		{name: "last is the tick", last: "2026-11-26T10:05:00Z", now: "2026-11-26T10:06:00Z", wantDue: false},
		{name: "very old last", last: "2020-01-01T00:00:00Z", now: "2026-11-26T10:05:10Z", wantTick: "2026-11-26T10:05:00Z", wantDue: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tick, due, err := DueTick(every5, mustTime(t, tt.last), mustTime(t, tt.now))
			if err != nil {
				t.Fatal(err)
			}
			if due != tt.wantDue {
				t.Errorf("due = %v, want %v", due, tt.wantDue)
			}
			if tt.wantTick == "" {
				if !tick.IsZero() {
					t.Errorf("tick = %s, want zero", tick)
				}
				return
			}
			if want := mustTime(t, tt.wantTick); !tick.Equal(want) {
				t.Errorf("tick = %s, want %s", tick.UTC().Format(time.RFC3339), tt.wantTick)
			}
		})
	}
}

func TestScheduledRunName(t *testing.T) {
	rome, err := time.LoadLocation("Europe/Rome")
	if err != nil {
		t.Fatal(err)
	}
	tick := time.Date(2026, 11, 26, 14, 30, 0, 0, rome)
	if got, want := ScheduledRunName("example", tick), "example-s202611261330"; got != want {
		t.Errorf("ScheduledRunName() = %q, want %q", got, want)
	}
	long := strings.Repeat("a", 40)
	if got := ScheduledRunName(long, tick); len(got) > 57 {
		t.Errorf("name %q has %d characters, want at most 57", got, len(got))
	}
}

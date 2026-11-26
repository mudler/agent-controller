package admission

import (
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mudler/agent-controller/api/v1alpha1"
)

var t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func run(name, profile string, minute int, phase v1alpha1.Phase, memory bool) Run {
	return Run{
		Name:       name,
		Profile:    profile,
		Created:    t0.Add(time.Duration(minute) * time.Minute),
		Phase:      phase,
		UsesMemory: memory,
		Trigger:    v1alpha1.TriggerManual,
	}
}

func ok(maxParallel int32) ProfileState {
	return ProfileState{Exists: true, Valid: true, Validated: true, MaxParallel: maxParallel}
}

func TestDecide(t *testing.T) {
	tests := []struct {
		name       string
		snap       Snapshot
		wantAdmit  []string
		wantReject []string
		wantPos    map[string]int32
	}{
		{
			name: "three memory-off runs with three slots",
			snap: Snapshot{
				GlobalSlots: 6,
				Runs: []Run{
					run("a", "p", 1, v1alpha1.PhaseQueued, false),
					run("b", "p", 2, v1alpha1.PhaseQueued, false),
					run("c", "p", 3, v1alpha1.PhaseQueued, false),
				},
				Profiles: map[string]ProfileState{"p": ok(3)},
			},
			wantAdmit: []string{"a", "b", "c"},
			wantPos:   map[string]int32{"a": 0, "b": 0, "c": 0},
		},
		{
			name: "global slots exhausted",
			snap: Snapshot{
				GlobalSlots: 2,
				Runs: []Run{
					run("busy", "q", 0, v1alpha1.PhaseRunning, false),
					run("a", "p", 1, v1alpha1.PhaseQueued, false),
					run("b", "p", 2, v1alpha1.PhaseQueued, false),
				},
				Profiles: map[string]ProfileState{"p": ok(4), "q": ok(4)},
			},
			wantAdmit: []string{"a"},
			wantPos:   map[string]int32{"a": 0, "b": 1},
		},
		{
			name: "profile slots exhausted",
			snap: Snapshot{
				GlobalSlots: 6,
				Runs: []Run{
					run("busy", "p", 0, v1alpha1.PhaseStarting, false),
					run("a", "p", 1, v1alpha1.PhaseQueued, false),
					run("b", "q", 2, v1alpha1.PhaseQueued, false),
				},
				Profiles: map[string]ProfileState{"p": ok(1), "q": ok(1)},
			},
			wantAdmit: []string{"b"},
			wantPos:   map[string]int32{"a": 1, "b": 0},
		},
		{
			name: "memory runs serialise",
			snap: Snapshot{
				GlobalSlots: 6,
				Runs: []Run{
					run("m1", "p", 1, v1alpha1.PhaseQueued, true),
					run("m2", "p", 2, v1alpha1.PhaseQueued, true),
				},
				Profiles: map[string]ProfileState{"p": ok(4)},
			},
			wantAdmit: []string{"m1"},
			wantPos:   map[string]int32{"m1": 0, "m2": 1},
		},
		{
			name: "memory lock held",
			snap: Snapshot{
				GlobalSlots:      6,
				Runs:             []Run{run("m2", "p", 2, v1alpha1.PhaseQueued, true)},
				Profiles:         map[string]ProfileState{"p": ok(4)},
				MemoryLockHolder: map[string]string{"p": "m1"},
			},
			wantAdmit: []string{},
			wantPos:   map[string]int32{"m2": 1},
		},
		{
			name: "memory-off run passes a waiting memory run",
			snap: Snapshot{
				GlobalSlots: 6,
				Runs: []Run{
					run("m1", "p", 1, v1alpha1.PhaseQueued, true),
					run("off", "p", 2, v1alpha1.PhaseQueued, false),
					run("m2", "p", 3, v1alpha1.PhaseQueued, true),
				},
				Profiles:         map[string]ProfileState{"p": ok(4)},
				MemoryLockHolder: map[string]string{"p": "m0"},
			},
			wantAdmit: []string{"off"},
			wantPos:   map[string]int32{"m1": 1, "off": 0, "m2": 2},
		},
		{
			name: "memory lock of another profile does not block",
			snap: Snapshot{
				GlobalSlots:      6,
				Runs:             []Run{run("m", "p", 1, v1alpha1.PhaseQueued, true)},
				Profiles:         map[string]ProfileState{"p": ok(1), "q": ok(1)},
				MemoryLockHolder: map[string]string{"q": "other"},
			},
			wantAdmit: []string{"m"},
			wantPos:   map[string]int32{"m": 0},
		},
		{
			name: "memory run waiting for a profile slot still blocks later memory runs",
			snap: Snapshot{
				GlobalSlots: 6,
				Runs: []Run{
					run("busy", "p", 0, v1alpha1.PhaseRunning, false),
					run("m1", "p", 1, v1alpha1.PhaseQueued, true),
				},
				Profiles: map[string]ProfileState{"p": ok(1)},
			},
			wantAdmit: []string{},
			wantPos:   map[string]int32{"m1": 1},
		},
		{
			name: "creation order with name tie break",
			snap: Snapshot{
				GlobalSlots: 1,
				Runs: []Run{
					run("z", "p", 1, v1alpha1.PhaseQueued, false),
					run("b", "p", 1, v1alpha1.PhaseQueued, false),
					run("a", "p", 5, v1alpha1.PhaseQueued, false),
				},
				Profiles: map[string]ProfileState{"p": ok(4)},
			},
			wantAdmit: []string{"b"},
			wantPos:   map[string]int32{"b": 0, "z": 1, "a": 2},
		},
		{
			name: "rejections",
			snap: Snapshot{
				GlobalSlots: 6,
				Runs: []Run{
					run("missing", "nope", 1, v1alpha1.PhaseQueued, false),
					run("invalid", "bad", 2, v1alpha1.PhaseQueued, false),
					run("unvalidated", "new", 3, v1alpha1.PhaseQueued, false),
					run("fine", "p", 4, "", false),
				},
				Profiles: map[string]ProfileState{
					"bad": {Exists: true, Valid: false, Validated: true, MaxParallel: 1, Reason: "egress.mode: no"},
					"new": {Exists: true, Valid: true, Validated: false, MaxParallel: 1},
					"p":   ok(1),
				},
			},
			wantAdmit:  []string{"fine"},
			wantReject: []string{"invalid", "missing", "unvalidated"},
			wantPos:    map[string]int32{"fine": 0},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := Decide(tt.snap)
			if !slices.Equal(d.Admit, tt.wantAdmit) {
				t.Errorf("Admit = %v, want %v", d.Admit, tt.wantAdmit)
			}
			gotReject := slices.Sorted(maps.Keys(d.Reject))
			if !slices.Equal(gotReject, tt.wantReject) && (len(gotReject) != 0 || len(tt.wantReject) != 0) {
				t.Errorf("Reject = %v, want %v", d.Reject, tt.wantReject)
			}
			if !maps.Equal(d.QueuePosition, tt.wantPos) {
				t.Errorf("QueuePosition = %v, want %v", d.QueuePosition, tt.wantPos)
			}
		})
	}
}

func TestDecideRejectReasons(t *testing.T) {
	d := Decide(Snapshot{
		GlobalSlots: 1,
		Runs: []Run{
			run("missing", "nope", 1, v1alpha1.PhaseQueued, false),
			run("invalid", "bad", 2, v1alpha1.PhaseQueued, false),
			run("unvalidated", "new", 3, v1alpha1.PhaseQueued, false),
		},
		Profiles: map[string]ProfileState{
			"bad": {Exists: true, Reason: "egress.mode: no"},
			"new": {Exists: true, Valid: true},
		},
	})
	want := map[string]string{
		"missing":     "not found",
		"invalid":     "egress.mode: no",
		"unvalidated": "not validated",
	}
	for name, substr := range want {
		if !strings.Contains(d.Reject[name], substr) {
			t.Errorf("Reject[%s] = %q, want it to contain %q", name, d.Reject[name], substr)
		}
	}
}

func TestShouldTick(t *testing.T) {
	sched := func(name string, phase v1alpha1.Phase) Run {
		r := run(name, "p", 0, phase, false)
		r.Trigger = v1alpha1.TriggerSchedule
		return r
	}
	tests := []struct {
		name string
		runs []Run
		want bool
	}{
		{name: "no runs", want: true},
		{name: "scheduled queued", runs: []Run{sched("a", v1alpha1.PhaseQueued)}, want: false},
		{name: "scheduled no phase yet", runs: []Run{sched("a", "")}, want: false},
		{name: "scheduled starting", runs: []Run{sched("a", v1alpha1.PhaseStarting)}, want: false},
		{name: "scheduled running", runs: []Run{sched("a", v1alpha1.PhaseRunning)}, want: true},
		{name: "manual queued", runs: []Run{run("a", "p", 0, v1alpha1.PhaseQueued, false)}, want: true},
		{name: "other profile queued", runs: []Run{func() Run { r := sched("a", v1alpha1.PhaseQueued); r.Profile = "q"; return r }()}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ShouldTick("p", tt.runs); got != tt.want {
				t.Errorf("ShouldTick() = %v, want %v", got, tt.want)
			}
		})
	}
}

// Package admission decides which queued runs start. It is a pure function
// of a snapshot, so the controller can rebuild it from the API at any time.
package admission

import (
	"cmp"
	"slices"
	"time"

	"github.com/mudler/agent-controller/api/v1alpha1"
)

// Run is the admission view of one non-final AgentRun.
type Run struct {
	Name       string
	Profile    string
	Created    time.Time
	Phase      v1alpha1.Phase
	UsesMemory bool
	Trigger    v1alpha1.Trigger
}

// ProfileState is the admission view of a profile.
type ProfileState struct {
	Exists      bool
	Valid       bool
	Validated   bool
	MaxParallel int32
	// Reason explains why the profile is invalid.
	Reason string
}

// Snapshot is the state admission decides on.
type Snapshot struct {
	GlobalSlots int32
	// Runs holds all non-final runs.
	Runs     []Run
	Profiles map[string]ProfileState
	// MemoryLockHolder maps a profile to the run that holds memory-<profile>.
	MemoryLockHolder map[string]string
}

// Decision is the result of one admission pass.
type Decision struct {
	// Admit lists the runs to start, in order.
	Admit []string
	// Reject maps a run to the reason it cannot run.
	Reject map[string]string
	// QueuePosition is 1-based among runs still queued, 0 for admitted runs.
	QueuePosition map[string]int32
}

func active(p v1alpha1.Phase) bool {
	return p == v1alpha1.PhaseStarting || p == v1alpha1.PhaseRunning
}

func queued(p v1alpha1.Phase) bool {
	return p == v1alpha1.PhaseQueued || p == ""
}

// Decide admits queued runs in creation order (ties by name). A run starts
// when its profile exists, is valid and validated, a global slot and a
// profile slot are free, and, for a memory run, nobody holds the profile's
// memory lock. A waiting memory run blocks only later memory runs of the
// same profile.
func Decide(s Snapshot) Decision {
	d := Decision{Admit: []string{}, Reject: map[string]string{}, QueuePosition: map[string]int32{}}

	var global int32
	perProfile := map[string]int32{}
	memoryTaken := map[string]bool{}
	for profile, holder := range s.MemoryLockHolder {
		if holder != "" {
			memoryTaken[profile] = true
		}
	}
	waiting := []Run{}
	for _, r := range s.Runs {
		switch {
		case active(r.Phase):
			global++
			perProfile[r.Profile]++
		case queued(r.Phase):
			waiting = append(waiting, r)
		}
	}
	slices.SortStableFunc(waiting, func(a, b Run) int {
		if c := a.Created.Compare(b.Created); c != 0 {
			return c
		}
		return cmp.Compare(a.Name, b.Name)
	})

	var position int32
	for _, r := range waiting {
		prof, ok := s.Profiles[r.Profile]
		if reason := rejectReason(r.Profile, prof, ok); reason != "" {
			d.Reject[r.Name] = reason
			continue
		}
		canStart := global < s.GlobalSlots &&
			perProfile[r.Profile] < prof.MaxParallel &&
			(!r.UsesMemory || !memoryTaken[r.Profile])
		if r.UsesMemory {
			// Admitted or waiting, this run is ahead of later memory runs.
			memoryTaken[r.Profile] = true
		}
		if !canStart {
			position++
			d.QueuePosition[r.Name] = position
			continue
		}
		global++
		perProfile[r.Profile]++
		d.Admit = append(d.Admit, r.Name)
		d.QueuePosition[r.Name] = 0
	}
	return d
}

func rejectReason(name string, p ProfileState, ok bool) string {
	switch {
	case !ok || !p.Exists:
		return "profile " + name + " not found"
	case !p.Valid:
		if p.Reason != "" {
			return "profile " + name + " is invalid: " + p.Reason
		}
		return "profile " + name + " is invalid"
	case !p.Validated:
		return "profile " + name + " is not validated by the owner"
	default:
		return ""
	}
}

// ShouldTick reports whether a schedule tick may create a run: false when a
// scheduled run of the profile is still Queued or Starting.
func ShouldTick(profile string, runs []Run) bool {
	for _, r := range runs {
		if r.Profile != profile || r.Trigger != v1alpha1.TriggerSchedule {
			continue
		}
		if queued(r.Phase) || r.Phase == v1alpha1.PhaseStarting {
			return false
		}
	}
	return true
}

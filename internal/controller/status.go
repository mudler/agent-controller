package controller

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/mudler/agent-controller/api/v1alpha1"
)

// Launcher progress steps, in order.
const (
	StepAdmitted        = "admitted"
	StepSandboxReady    = "sandbox-ready"
	StepGatePassed      = "gate-passed"
	StepCredentialsSent = "credentials-sent"
	StepHarnessStarted  = "harness-started"
	StepCleanupDone     = "cleanup-done"
)

// Steps lists the launcher steps that ParseSteps accepts.
var Steps = []string{StepAdmitted, StepSandboxReady, StepGatePassed, StepCredentialsSent, StepHarnessStarted, StepCleanupDone}

// HarnessTimeoutExit is the harness exit code for a session that hit its
// time budget.
const HarnessTimeoutExit = 124

// MessageNoResult is the run message when the launcher wrote no outcome.
const MessageNoResult = "launcher reported no result"

// MessageNoCapacity is the run message when the Job deadline passed before
// the harness started.
const MessageNoCapacity = "no capacity"

// Termination is the outcome the launcher writes to its termination log.
type Termination struct {
	HarnessExit *int32 `json:"harnessExit"`
	Error       string `json:"error"`
}

// ParseTermination reads the launcher outcome from the terminated state of
// the launcher container. It returns false when there is no valid outcome.
func ParseTermination(pod *corev1.Pod) (*Termination, bool) {
	if pod == nil {
		return nil, false
	}
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.Name != LauncherContainer || cs.State.Terminated == nil {
			continue
		}
		msg := strings.TrimSpace(cs.State.Terminated.Message)
		if msg == "" {
			return nil, false
		}
		term := &Termination{}
		if err := json.Unmarshal([]byte(msg), term); err != nil {
			return nil, false
		}
		return term, true
	}
	return nil, false
}

// ParseSteps reads launcher step lines from a log. A step line is a JSON
// object with exactly the keys "step" and "at". Unknown step names are
// ignored, the first occurrence of a step wins and the order is kept.
func ParseSteps(log []byte) []v1alpha1.Step {
	steps := []v1alpha1.Step{}
	seen := map[string]bool{}
	sc := bufio.NewScanner(bytes.NewReader(log))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(line, &fields); err != nil || len(fields) != 2 {
			continue
		}
		var name, at string
		if json.Unmarshal(fields["step"], &name) != nil || json.Unmarshal(fields["at"], &at) != nil {
			continue
		}
		if !slices.Contains(Steps, name) || seen[name] {
			continue
		}
		ts, err := time.Parse(time.RFC3339, at)
		if err != nil {
			continue
		}
		seen[name] = true
		steps = append(steps, v1alpha1.Step{Name: name, At: metav1.NewTime(ts)})
	}
	return steps
}

// HasStep reports whether the steps contain the named step.
func HasStep(steps []v1alpha1.Step, name string) bool {
	return slices.ContainsFunc(steps, func(s v1alpha1.Step) bool { return s.Name == name })
}

func jobCondition(job *batchv1.Job, t batchv1.JobConditionType) *batchv1.JobCondition {
	for i := range job.Status.Conditions {
		c := &job.Status.Conditions[i]
		if c.Type == t && c.Status == corev1.ConditionTrue {
			return c
		}
	}
	return nil
}

// FinalPhase maps a finished launcher Job and its outcome to the final run
// phase, message and harness exit code. done is false while the Job runs.
func FinalPhase(job *batchv1.Job, term *Termination, steps []v1alpha1.Step) (phase v1alpha1.Phase, message string, exitCode *int32, done bool) {
	complete := jobCondition(job, batchv1.JobComplete)
	failed := jobCondition(job, batchv1.JobFailed)
	if complete == nil && failed == nil {
		return "", "", nil, false
	}
	if term != nil {
		exitCode = term.HarnessExit
	}

	switch {
	case exitCode != nil && *exitCode == HarnessTimeoutExit:
		return v1alpha1.PhaseTimedOut, "harness session reached its time budget", exitCode, true
	case failed != nil && failed.Reason == batchv1.JobReasonDeadlineExceeded:
		if !HasStep(steps, StepHarnessStarted) {
			return v1alpha1.PhaseFailed, MessageNoCapacity, exitCode, true
		}
		return v1alpha1.PhaseTimedOut, "launcher Job reached its deadline", exitCode, true
	case term == nil:
		return v1alpha1.PhaseFailed, MessageNoResult, nil, true
	case complete != nil && exitCode != nil && *exitCode == 0 && term.Error == "":
		return v1alpha1.PhaseSucceeded, "harness session ended with exit code 0", exitCode, true
	case term.Error != "":
		return v1alpha1.PhaseFailed, term.Error, exitCode, true
	case exitCode != nil:
		return v1alpha1.PhaseFailed, fmt.Sprintf("harness exited with code %d", *exitCode), exitCode, true
	default:
		return v1alpha1.PhaseFailed, "launcher failed before the harness ran", nil, true
	}
}

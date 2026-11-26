package controller

import (
	"slices"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"

	"github.com/mudler/agent-controller/api/v1alpha1"
)

func podWithMessage(container, msg string) *corev1.Pod {
	return &corev1.Pod{Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
		Name:  container,
		State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Message: msg}},
	}}}}
}

func TestParseTermination(t *testing.T) {
	tests := []struct {
		name     string
		pod      *corev1.Pod
		wantOK   bool
		wantExit *int32
		wantErr  string
	}{
		{name: "nil pod"},
		{name: "running", pod: &corev1.Pod{Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{Name: "launcher"}}}}},
		{name: "success", pod: podWithMessage("launcher", `{"harnessExit": 0, "error": ""}`), wantOK: true, wantExit: ptr.To(int32(0))},
		{name: "error", pod: podWithMessage("launcher", `{"harnessExit": null, "error": "check: egress.hosts[2]"}`), wantOK: true, wantErr: "check: egress.hosts[2]"},
		{name: "malformed", pod: podWithMessage("launcher", `Traceback (most recent call last)`)},
		{name: "extra key ignored", pod: podWithMessage("launcher", `{"harnessExit": 0, "error": "", "x": 1}`), wantOK: true, wantExit: ptr.To(int32(0))},
		{name: "empty", pod: podWithMessage("launcher", ``)},
		{name: "other container", pod: podWithMessage("sidecar", `{"harnessExit": 0, "error": ""}`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			term, ok := ParseTermination(tt.pod)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if !ptrEqual(term.HarnessExit, tt.wantExit) || term.Error != tt.wantErr {
				t.Errorf("term = %+v", term)
			}
		})
	}
}

func ptrEqual(a, b *int32) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func TestParseSteps(t *testing.T) {
	log := []byte(`starting launcher
{"step": "admitted", "at": "2026-11-26T10:00:00Z"}
{"step": "sandbox-ready", "at": "2026-11-26T10:00:30Z"}
{"step": "unknown", "at": "2026-11-26T10:00:31Z"}
{"step": "gate-passed", "at": "2026-11-26T10:00:40Z", "extra": 1}
{"step": "gate-passed", "at": "2026-11-26T10:00:41Z"}
{"step": "credentials-sent", "at": "not a time"}
{"step": "harness-started", "at": "2026-11-26T10:01:00Z"}
{"step": "admitted", "at": "2026-11-26T11:00:00Z"}
{"type": "assistant", "text": "{\"step\": \"cleanup-done\"}"}
  {"step": "cleanup-done", "at": "2026-11-26T10:30:00Z"}
`)
	steps := ParseSteps(log)
	names := []string{}
	for _, s := range steps {
		names = append(names, s.Name)
	}
	want := []string{"admitted", "sandbox-ready", "gate-passed", "harness-started", "cleanup-done"}
	if !slices.Equal(names, want) {
		t.Fatalf("steps = %v, want %v", names, want)
	}
	if got := steps[0].At.UTC().Format("15:04"); got != "10:00" {
		t.Errorf("spoofed duplicate admitted replaced the first one: %s", got)
	}
	if got := steps[2].At.UTC().Format("15:04:05"); got != "10:00:41" {
		t.Errorf("gate-passed at %s, want the line with exactly two keys", got)
	}
}

func job(conds ...batchv1.JobCondition) *batchv1.Job {
	return &batchv1.Job{Status: batchv1.JobStatus{Conditions: conds}}
}

var (
	complete = batchv1.JobCondition{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}
	failed   = batchv1.JobCondition{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: batchv1.JobReasonBackoffLimitExceeded}
	deadline = batchv1.JobCondition{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: batchv1.JobReasonDeadlineExceeded}
)

func TestFinalPhase(t *testing.T) {
	started := []v1alpha1.Step{{Name: StepAdmitted}, {Name: StepHarnessStarted}}
	notStarted := []v1alpha1.Step{{Name: StepAdmitted}}
	tests := []struct {
		name     string
		job      *batchv1.Job
		term     *Termination
		steps    []v1alpha1.Step
		want     v1alpha1.Phase
		wantMsg  string
		wantExit *int32
		wantDone bool
	}{
		{name: "active", job: job(), want: "", wantDone: false},
		{name: "not true condition", job: job(batchv1.JobCondition{Type: batchv1.JobComplete, Status: corev1.ConditionFalse}), wantDone: false},
		{name: "succeeded", job: job(complete), term: &Termination{HarnessExit: ptr.To(int32(0))}, steps: started, want: v1alpha1.PhaseSucceeded, wantExit: ptr.To(int32(0)), wantDone: true},
		{name: "harness timeout exit", job: job(failed), term: &Termination{HarnessExit: ptr.To(int32(124))}, steps: started, want: v1alpha1.PhaseTimedOut, wantExit: ptr.To(int32(124)), wantDone: true},
		{name: "harness timeout exit on complete job", job: job(complete), term: &Termination{HarnessExit: ptr.To(int32(124))}, steps: started, want: v1alpha1.PhaseTimedOut, wantExit: ptr.To(int32(124)), wantDone: true},
		{name: "deadline before harness", job: job(deadline), steps: notStarted, want: v1alpha1.PhaseFailed, wantMsg: MessageNoCapacity, wantDone: true},
		{name: "deadline after harness", job: job(deadline), steps: started, want: v1alpha1.PhaseTimedOut, wantDone: true},
		{name: "launcher error", job: job(failed), term: &Termination{Error: "check: egress.hosts[2]"}, want: v1alpha1.PhaseFailed, wantMsg: "check: egress.hosts[2]", wantDone: true},
		{name: "harness nonzero", job: job(failed), term: &Termination{HarnessExit: ptr.To(int32(3))}, steps: started, want: v1alpha1.PhaseFailed, wantMsg: "harness exited with code 3", wantExit: ptr.To(int32(3)), wantDone: true},
		{name: "harness nonzero on complete job", job: job(complete), term: &Termination{HarnessExit: ptr.To(int32(1))}, want: v1alpha1.PhaseFailed, wantExit: ptr.To(int32(1)), wantDone: true},
		{name: "exit zero but cleanup error", job: job(failed), term: &Termination{HarnessExit: ptr.To(int32(0)), Error: "cleanup: sandbox"}, want: v1alpha1.PhaseFailed, wantMsg: "cleanup: sandbox", wantExit: ptr.To(int32(0)), wantDone: true},
		{name: "malformed message on failure", job: job(failed), term: nil, want: v1alpha1.PhaseFailed, wantMsg: MessageNoResult, wantDone: true},
		{name: "malformed message on complete", job: job(complete), term: nil, want: v1alpha1.PhaseFailed, wantMsg: MessageNoResult, wantDone: true},
		{name: "no exit and no error", job: job(failed), term: &Termination{}, want: v1alpha1.PhaseFailed, wantDone: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			phase, msg, exit, done := FinalPhase(tt.job, tt.term, tt.steps)
			if done != tt.wantDone || phase != tt.want {
				t.Fatalf("FinalPhase() = %q, done %v; want %q, done %v", phase, done, tt.want, tt.wantDone)
			}
			if tt.wantMsg != "" && msg != tt.wantMsg {
				t.Errorf("message = %q, want %q", msg, tt.wantMsg)
			}
			if done && msg == "" {
				t.Errorf("final phase without a message")
			}
			if !ptrEqual(exit, tt.wantExit) {
				t.Errorf("exit = %v, want %v", exit, tt.wantExit)
			}
		})
	}
}

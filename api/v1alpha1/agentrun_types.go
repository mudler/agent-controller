package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Trigger records what started a run.
// +kubebuilder:validation:Enum=schedule;manual
type Trigger string

const (
	TriggerSchedule Trigger = "schedule"
	TriggerManual   Trigger = "manual"
)

// Phase is the lifecycle phase of a run.
// +kubebuilder:validation:Enum=Queued;Starting;Running;Succeeded;Failed;Cancelled;TimedOut;Rejected
type Phase string

const (
	PhaseQueued    Phase = "Queued"
	PhaseStarting  Phase = "Starting"
	PhaseRunning   Phase = "Running"
	PhaseSucceeded Phase = "Succeeded"
	PhaseFailed    Phase = "Failed"
	PhaseCancelled Phase = "Cancelled"
	PhaseTimedOut  Phase = "TimedOut"
	PhaseRejected  Phase = "Rejected"
)

// Final reports whether the phase is terminal.
func (p Phase) Final() bool {
	switch p {
	case PhaseSucceeded, PhaseFailed, PhaseCancelled, PhaseTimedOut, PhaseRejected:
		return true
	default:
		return false
	}
}

// MaxTaskLength is the maximum length of AgentRunSpec.Task.
const MaxTaskLength = 4000

// AgentRunSpec requests one run of a profile.
// +kubebuilder:validation:XValidation:rule="self.profile == oldSelf.profile && self.trigger == oldSelf.trigger && self.requestedBy == oldSelf.requestedBy",message="profile, trigger and requestedBy are immutable"
// +kubebuilder:validation:XValidation:rule="(has(self.task) ? self.task : '') == (has(oldSelf.task) ? oldSelf.task : '')",message="task is immutable"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.cancel) || !oldSelf.cancel || (has(self.cancel) && self.cancel)",message="cancel cannot be undone"
type AgentRunSpec struct {
	// +kubebuilder:validation:MinLength=1
	Profile string `json:"profile"`
	// Task is appended to the profile prompt.
	// +kubebuilder:validation:MaxLength=4000
	// +optional
	Task string `json:"task,omitempty"`
	// Memory false turns memory off for this run. True cannot override a
	// profile with memory disabled.
	// +optional
	Memory *bool `json:"memory,omitempty"`
	// +optional
	Cancel  bool    `json:"cancel,omitempty"`
	Trigger Trigger `json:"trigger"`
	// RequestedBy is the name of the token that created the run.
	// +kubebuilder:validation:MinLength=1
	RequestedBy string `json:"requestedBy"`
}

// Step is one launcher progress mark.
type Step struct {
	Name string      `json:"name"`
	At   metav1.Time `json:"at"`
}

// AgentRunStatus is written by the controller.
type AgentRunStatus struct {
	// +optional
	Phase Phase `json:"phase,omitempty"`
	// QueuePosition is 1-based among queued runs, 0 otherwise.
	// +optional
	QueuePosition int32 `json:"queuePosition,omitempty"`
	// +optional
	ProfileVersion int64 `json:"profileVersion,omitempty"`
	// +optional
	JobName string `json:"jobName,omitempty"`
	// +optional
	PodName string `json:"podName,omitempty"`
	// +optional
	StartTime *metav1.Time `json:"startTime,omitempty"`
	// +optional
	CompletionTime *metav1.Time `json:"completionTime,omitempty"`
	// ExitCode is the harness exit code.
	// +optional
	ExitCode *int32 `json:"exitCode,omitempty"`
	// +optional
	Message string `json:"message,omitempty"`
	// +optional
	Steps []Step `json:"steps,omitempty"`
}

// AgentRun is one run of an AgentProfile.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:validation:XValidation:rule="self.metadata.name.size() <= 57",message="the run name must have at most 57 characters"
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Profile",type=string,JSONPath=`.spec.profile`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type AgentRun struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AgentRunSpec   `json:"spec"`
	Status AgentRunStatus `json:"status,omitempty"`
}

// AgentRunList is a list of AgentRun.
//
// +kubebuilder:object:root=true
type AgentRunList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AgentRun `json:"items"`
}

func init() {
	SchemeBuilder.Register(&AgentRun{}, &AgentRunList{})
}

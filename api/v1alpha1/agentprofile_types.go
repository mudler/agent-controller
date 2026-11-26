package v1alpha1

import (
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// ConditionValid is the condition type that reports policy fit.
const ConditionValid = "Valid"

// RepoRef names a GitHub repository and branch.
type RepoRef struct {
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`
	Slug string `json:"slug"`
	// +kubebuilder:validation:MinLength=1
	Branch string `json:"branch"`
}

// EgressMode selects how a run reaches the network.
// +kubebuilder:validation:Enum=allowlist;all
type EgressMode string

const (
	EgressAllowlist EgressMode = "allowlist"
	EgressAll       EgressMode = "all"
)

// ProfileEgress sets the egress of the runs of a profile.
type ProfileEgress struct {
	Mode EgressMode `json:"mode"`
	// ExtraHosts adds host:443 authorities in allowlist mode. Each must be in
	// the policy.
	// +kubebuilder:validation:items:Pattern=`^[a-z0-9.-]+:443$`
	// +optional
	ExtraHosts []string `json:"extraHosts,omitempty"`
}

// Budget limits one session.
type Budget struct {
	// +kubebuilder:validation:Minimum=1
	SessionSeconds int32 `json:"sessionSeconds"`
	// +kubebuilder:validation:Minimum=1
	Iterations int32 `json:"iterations"`
}

// ProfileResources sizes the sandbox.
type ProfileResources struct {
	CPU       resource.Quantity `json:"cpu"`
	Memory    resource.Quantity `json:"memory"`
	Workspace resource.Quantity `json:"workspace"`
	Tmp       resource.Quantity `json:"tmp"`
}

// Schedule starts runs on a cron schedule.
type Schedule struct {
	// Cron is a standard five-field cron expression.
	// +kubebuilder:validation:MinLength=1
	Cron string `json:"cron"`
	// TimeZone is an IANA time zone name. The default is UTC.
	// +optional
	TimeZone string `json:"timeZone,omitempty"`
	// +optional
	Suspend bool `json:"suspend"`
}

// MemoryMode turns the per-profile memory volume on or off.
// +kubebuilder:validation:Enum=enabled;disabled
type MemoryMode string

const (
	MemoryEnabled  MemoryMode = "enabled"
	MemoryDisabled MemoryMode = "disabled"
)

// AgentProfileSpec describes what a run of the profile does.
type AgentProfileSpec struct {
	// Harness names a HarnessProfile.
	// +kubebuilder:validation:MinLength=1
	Harness string `json:"harness"`
	// Repos holds exactly one repository.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=1
	Repos []RepoRef `json:"repos"`
	// GitHub names a Login of kind github-token that allows every repository.
	// +kubebuilder:validation:MinLength=1
	GitHub string `json:"github"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=32768
	Prompt    string           `json:"prompt"`
	Egress    ProfileEgress    `json:"egress"`
	Budget    Budget           `json:"budget"`
	Resources ProfileResources `json:"resources"`
	// +optional
	Schedule *Schedule `json:"schedule,omitempty"`
	// +kubebuilder:validation:Minimum=1
	MaxParallel int32      `json:"maxParallel"`
	Memory      MemoryMode `json:"memory"`
	// Validated is set only by the owner. Unvalidated profiles do not run.
	// +optional
	Validated bool `json:"validated"`
}

// ValidationBinding records the objects the owner validated a profile
// against. A profile counts as validated only while they are unchanged.
type ValidationBinding struct {
	HarnessUID        types.UID `json:"harnessUID"`
	HarnessGeneration int64     `json:"harnessGeneration"`
	GitHubLoginUID    types.UID `json:"githubLoginUID"`
}

// AgentProfileStatus is written by the controller, except validatedFor,
// which the server writes when the owner validates the profile.
type AgentProfileStatus struct {
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// Version is the generation of the spec.
	// +optional
	Version int64 `json:"version,omitempty"`
	// +optional
	LastChangedBy string `json:"lastChangedBy,omitempty"`
	// +optional
	LastScheduleTime *metav1.Time `json:"lastScheduleTime,omitempty"`
	// +optional
	NextScheduleTime *metav1.Time `json:"nextScheduleTime,omitempty"`
	// ValidatedFor binds spec.validated to the harness profile and GitHub
	// login that the owner validated.
	// +optional
	ValidatedFor *ValidationBinding `json:"validatedFor,omitempty"`
}

// AgentProfile is a runnable agent configuration.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:validation:XValidation:rule="self.metadata.name.size() <= 40 && self.metadata.name.matches('^[a-z]([-a-z0-9]*[a-z0-9])?$')",message="the name must be a DNS-1035 label of at most 40 characters"
// +kubebuilder:printcolumn:name="Harness",type=string,JSONPath=`.spec.harness`
// +kubebuilder:printcolumn:name="Validated",type=boolean,JSONPath=`.spec.validated`
// +kubebuilder:printcolumn:name="Valid",type=string,JSONPath=`.status.conditions[?(@.type=="Valid")].status`
// +kubebuilder:printcolumn:name="Version",type=integer,JSONPath=`.status.version`
type AgentProfile struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AgentProfileSpec   `json:"spec"`
	Status AgentProfileStatus `json:"status,omitempty"`
}

// AgentProfileList is a list of AgentProfile.
//
// +kubebuilder:object:root=true
type AgentProfileList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AgentProfile `json:"items"`
}

func init() {
	SchemeBuilder.Register(&AgentProfile{}, &AgentProfileList{})
}

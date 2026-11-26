package v1alpha1

import (
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// HarnessKind names a harness implementation in the runner image.
// +kubebuilder:validation:Enum=nib;claude-code
type HarnessKind string

const (
	HarnessNib        HarnessKind = "nib"
	HarnessClaudeCode HarnessKind = "claude-code"
)

// PolicyName is the only accepted name of an AgentPolicy.
const PolicyName = "default"

// AgentPolicySpec sets the outer limits for profiles, harness profiles and runs.
type AgentPolicySpec struct {
	// Harnesses lists the harness kinds that HarnessProfiles may use.
	// +listType=set
	Harnesses []HarnessKind `json:"harnesses"`
	Egress    PolicyEgress  `json:"egress"`
	Limits    PolicyLimits  `json:"limits"`
	// ModelEndpoints lists the endpoints that HarnessProfiles may use.
	// +kubebuilder:validation:items:Pattern=`^https://`
	// +listType=set
	ModelEndpoints []string `json:"modelEndpoints"`
}

// PolicyEgress is the egress ceiling.
type PolicyEgress struct {
	// Hosts lists the exact host:443 authorities that runs may reach in
	// allowlist mode.
	// +kubebuilder:validation:items:Pattern=`^[a-z0-9.-]+:443$`
	// +listType=set
	Hosts []string `json:"hosts"`
	// AllowAll permits profiles with egress mode "all".
	AllowAll bool `json:"allowAll"`
}

// PolicyLimits holds the ceilings for every profile.
type PolicyLimits struct {
	// +kubebuilder:validation:Minimum=1
	SessionSeconds int32             `json:"sessionSeconds"`
	CPU            resource.Quantity `json:"cpu"`
	Memory         resource.Quantity `json:"memory"`
	Workspace      resource.Quantity `json:"workspace"`
	// +kubebuilder:validation:Minimum=1
	MaxParallelPerProfile int32 `json:"maxParallelPerProfile"`
	// +kubebuilder:validation:Minimum=1
	GlobalSlots int32 `json:"globalSlots"`
}

// AgentPolicy is the Git-owned policy. Only one object, named "default", exists.
//
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:validation:XValidation:rule="self.metadata.name == 'default'",message="the AgentPolicy must be named default"
type AgentPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec AgentPolicySpec `json:"spec"`
}

// AgentPolicyList is a list of AgentPolicy.
//
// +kubebuilder:object:root=true
type AgentPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AgentPolicy `json:"items"`
}

func init() {
	SchemeBuilder.Register(&AgentPolicy{}, &AgentPolicyList{})
}

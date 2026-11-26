package v1alpha1

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// HarnessProfileSpec selects a harness, its model and its model login.
type HarnessProfileSpec struct {
	Harness HarnessKind `json:"harness"`
	// Version must match a harness version installed in the runner image.
	// +kubebuilder:validation:MinLength=1
	Version string `json:"version"`
	// +kubebuilder:validation:MinLength=1
	Model string `json:"model"`
	// +kubebuilder:validation:Pattern=`^https://`
	Endpoint string `json:"endpoint"`
	// Login names a Login of kind model-api-key or claude-credentials.
	// +kubebuilder:validation:MinLength=1
	Login string `json:"login"`
	// Config holds harness settings. The schema depends on the harness kind.
	// +optional
	Config *apiextensionsv1.JSON `json:"config,omitempty"`
}

// HarnessProfileStatus reports whether the harness profile fits the policy.
type HarnessProfileStatus struct {
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// HarnessProfile is a reusable harness configuration.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Harness",type=string,JSONPath=`.spec.harness`
// +kubebuilder:printcolumn:name="Model",type=string,JSONPath=`.spec.model`
// +kubebuilder:printcolumn:name="Valid",type=string,JSONPath=`.status.conditions[?(@.type=="Valid")].status`
type HarnessProfile struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   HarnessProfileSpec   `json:"spec"`
	Status HarnessProfileStatus `json:"status,omitempty"`
}

// HarnessProfileList is a list of HarnessProfile.
//
// +kubebuilder:object:root=true
type HarnessProfileList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []HarnessProfile `json:"items"`
}

func init() {
	SchemeBuilder.Register(&HarnessProfile{}, &HarnessProfileList{})
}

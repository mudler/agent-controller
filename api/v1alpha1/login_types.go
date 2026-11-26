package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// LoginKind is the kind of credential a Login holds.
// +kubebuilder:validation:Enum=github-token;model-api-key;claude-credentials
type LoginKind string

const (
	LoginGitHubToken       LoginKind = "github-token"
	LoginModelAPIKey       LoginKind = "model-api-key"
	LoginClaudeCredentials LoginKind = "claude-credentials"
)

// SecretKey returns the data key of the Secret that holds the value.
func (k LoginKind) SecretKey() string {
	switch k {
	case LoginGitHubToken:
		return "token"
	case LoginModelAPIKey:
		return "api-key"
	case LoginClaudeCredentials:
		return "credentials.json"
	default:
		return ""
	}
}

// LoginSpec is the metadata of a credential. The value is in a Secret with
// the same name in the logins namespace.
// +kubebuilder:validation:XValidation:rule="self.kind == 'github-token' || ((!has(self.repos) || size(self.repos) == 0) && (!has(self.egressAll) || !self.egressAll))",message="repos and egressAll apply only to github-token logins"
type LoginSpec struct {
	Kind LoginKind `json:"kind"`
	// Repos lists the repositories a github-token may serve, as
	// "owner/name" or "owner/*".
	// +kubebuilder:validation:items:Pattern=`^[A-Za-z0-9_.-]+/([A-Za-z0-9_.-]+|\*)$`
	// +optional
	Repos []string `json:"repos,omitempty"`
	// EgressAll allows profiles with egress mode "all" to use this token.
	// +optional
	EgressAll bool `json:"egressAll,omitempty"`
	// +kubebuilder:validation:MaxLength=512
	// +optional
	Description string `json:"description,omitempty"`
}

// LoginStatus records the last rotation.
type LoginStatus struct {
	// +optional
	RotatedAt *metav1.Time `json:"rotatedAt,omitempty"`
	// +optional
	RotatedBy string `json:"rotatedBy,omitempty"`
}

// Login is the write-only metadata of a credential.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Kind",type=string,JSONPath=`.spec.kind`
// +kubebuilder:printcolumn:name="Rotated",type=date,JSONPath=`.status.rotatedAt`
type Login struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   LoginSpec   `json:"spec"`
	Status LoginStatus `json:"status,omitempty"`
}

// LoginList is a list of Login.
//
// +kubebuilder:object:root=true
type LoginList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Login `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Login{}, &LoginList{})
}

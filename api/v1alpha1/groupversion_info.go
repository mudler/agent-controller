// Package v1alpha1 contains the API types of the agents.mudler.dev group.
//
// +kubebuilder:object:generate=true
// +groupName=agents.mudler.dev
package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

var (
	// GroupVersion is the group and version of these types.
	GroupVersion = schema.GroupVersion{Group: "agents.mudler.dev", Version: "v1alpha1"}

	// SchemeBuilder registers the types with a runtime scheme.
	SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}

	// AddToScheme adds the types of this group to a scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)

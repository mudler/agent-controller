// Package controller holds the reconcilers that admit, run and clean up
// AgentRuns, and the builders for the resolved run and the launcher Job.
package controller

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/mudler/agent-controller/api/v1alpha1"
)

const (
	// LabelRun marks every object that belongs to one run.
	LabelRun = "agents.mudler.dev/run"
	// LabelComponent names the role of a run object, for example "launcher".
	LabelComponent = "agents.mudler.dev/component"
	// ComponentLauncher is the LabelComponent value of the launcher Job Pod.
	ComponentLauncher = "launcher"
	// FinalizerCleanup keeps an AgentRun until its resources are gone.
	FinalizerCleanup = "agents.mudler.dev/cleanup"
	// AnnotationLastChangedBy holds the token name of the last profile writer.
	AnnotationLastChangedBy = "agents.mudler.dev/last-changed-by"
	// AnnotationCleanupEmptySince records when a cleanup pass first found
	// no run objects. The locks are released on a later empty pass.
	AnnotationCleanupEmptySince = "agents.mudler.dev/cleanup-empty-since"
	// LauncherContainer is the container the controller patches in the Job.
	LauncherContainer = "launcher"
)

// runOwnerRef returns a controller owner reference to the run.
func runOwnerRef(run *v1alpha1.AgentRun) metav1.OwnerReference {
	return *metav1.NewControllerRef(run, v1alpha1.GroupVersion.WithKind("AgentRun"))
}

// MemoryLockName returns the name of the memory lock Lease of a profile.
func MemoryLockName(profile string) string {
	return "memory-" + profile
}

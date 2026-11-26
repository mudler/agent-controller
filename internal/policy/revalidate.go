package policy

import (
	"k8s.io/apimachinery/pkg/api/equality"

	"github.com/mudler/agent-controller/api/v1alpha1"
)

// NeedsRevalidation reports whether a profile update must reset validated:
// a change to repos, github, egress or harness.
func NeedsRevalidation(old, updated *v1alpha1.AgentProfileSpec) bool {
	return !equality.Semantic.DeepEqual(old.Repos, updated.Repos) ||
		old.GitHub != updated.GitHub ||
		!equality.Semantic.DeepEqual(old.Egress, updated.Egress) ||
		old.Harness != updated.Harness
}

// HarnessChangeResets reports whether a harness profile update must reset
// validated on every profile that references it: a change to harness,
// version, endpoint, login or config. A model change alone does not.
func HarnessChangeResets(old, updated *v1alpha1.HarnessProfileSpec) bool {
	return HarnessApprovalHash(old) != HarnessApprovalHash(updated)
}

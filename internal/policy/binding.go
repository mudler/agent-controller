package policy

import (
	"github.com/mudler/agent-controller/api/v1alpha1"
)

// BindingFor returns the validation binding of a harness profile and a
// GitHub login.
func BindingFor(h *v1alpha1.HarnessProfile, github *v1alpha1.Login) *v1alpha1.ValidationBinding {
	return &v1alpha1.ValidationBinding{
		HarnessUID:        h.UID,
		HarnessGeneration: h.Generation,
		GitHubLoginUID:    github.UID,
	}
}

// ValidationCurrent reports whether a profile is validated and the harness
// profile and GitHub login it references are still the objects, at the
// same harness generation, that the owner validated it against. A deleted
// and recreated object has a new UID, so it does not match.
func ValidationCurrent(env Env, p *v1alpha1.AgentProfile) bool {
	b := p.Status.ValidatedFor
	if !p.Spec.Validated || b == nil {
		return false
	}
	h, ok := env.Harnesses[p.Spec.Harness]
	if !ok || h.UID != b.HarnessUID || h.Generation != b.HarnessGeneration {
		return false
	}
	gh, ok := env.Logins[p.Spec.GitHub]
	return ok && gh.UID == b.GitHubLoginUID
}

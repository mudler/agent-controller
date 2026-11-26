package policy

import (
	"testing"

	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/mudler/agent-controller/api/v1alpha1"
)

func TestNeedsRevalidation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(p *v1alpha1.AgentProfileSpec)
		want   bool
	}{
		{name: "no change", mutate: func(*v1alpha1.AgentProfileSpec) {}, want: false},
		{name: "repo", mutate: func(p *v1alpha1.AgentProfileSpec) { p.Repos[0].Slug = "example-org/other" }, want: true},
		{name: "branch", mutate: func(p *v1alpha1.AgentProfileSpec) { p.Repos[0].Branch = "dev" }, want: true},
		{name: "github", mutate: func(p *v1alpha1.AgentProfileSpec) { p.GitHub = "gh-all" }, want: true},
		{name: "egress mode", mutate: func(p *v1alpha1.AgentProfileSpec) { p.Egress.Mode = v1alpha1.EgressAll }, want: true},
		{name: "egress hosts", mutate: func(p *v1alpha1.AgentProfileSpec) { p.Egress.ExtraHosts = nil }, want: true},
		{name: "harness", mutate: func(p *v1alpha1.AgentProfileSpec) { p.Harness = "other" }, want: true},
		{name: "prompt", mutate: func(p *v1alpha1.AgentProfileSpec) { p.Prompt = "other" }, want: false},
		{name: "budget", mutate: func(p *v1alpha1.AgentProfileSpec) { p.Budget.Iterations = 1 }, want: false},
		{name: "resources", mutate: func(p *v1alpha1.AgentProfileSpec) { p.Resources.CPU = resource.MustParse("1") }, want: false},
		{name: "schedule", mutate: func(p *v1alpha1.AgentProfileSpec) { p.Schedule = nil }, want: false},
		{name: "maxParallel", mutate: func(p *v1alpha1.AgentProfileSpec) { p.MaxParallel = 1 }, want: false},
		{name: "memory", mutate: func(p *v1alpha1.AgentProfileSpec) { p.Memory = v1alpha1.MemoryDisabled }, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			old := testProfile()
			updated := testProfile()
			tt.mutate(updated)
			if got := NeedsRevalidation(old, updated); got != tt.want {
				t.Errorf("NeedsRevalidation() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNeedsRevalidationNilVersusEmptyHosts(t *testing.T) {
	old := testProfile()
	old.Egress.ExtraHosts = nil
	updated := testProfile()
	updated.Egress.ExtraHosts = []string{}
	if NeedsRevalidation(old, updated) {
		t.Errorf("nil and empty extraHosts should be equal")
	}
}

func TestHarnessChangeResets(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(h *v1alpha1.HarnessProfileSpec)
		want   bool
	}{
		{name: "no change", mutate: func(*v1alpha1.HarnessProfileSpec) {}, want: false},
		{name: "harness", mutate: func(h *v1alpha1.HarnessProfileSpec) { h.Harness = v1alpha1.HarnessClaudeCode }, want: true},
		{name: "version", mutate: func(h *v1alpha1.HarnessProfileSpec) { h.Version = "v2" }, want: true},
		{name: "endpoint", mutate: func(h *v1alpha1.HarnessProfileSpec) { h.Endpoint = "https://api.example.com" }, want: true},
		{name: "login", mutate: func(h *v1alpha1.HarnessProfileSpec) { h.Login = "other" }, want: true},
		{name: "model", mutate: func(h *v1alpha1.HarnessProfileSpec) { h.Model = "other" }, want: false},
		{name: "config", mutate: func(h *v1alpha1.HarnessProfileSpec) { h.Config = nil }, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			old := testHarness().Spec
			updated := testHarness().Spec
			tt.mutate(&updated)
			if got := HarnessChangeResets(&old, &updated); got != tt.want {
				t.Errorf("HarnessChangeResets() = %v, want %v", got, tt.want)
			}
		})
	}
}

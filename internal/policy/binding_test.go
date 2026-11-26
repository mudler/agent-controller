package policy

import (
	"testing"

	"github.com/mudler/agent-controller/api/v1alpha1"
)

func TestValidationCurrent(t *testing.T) {
	newEnv := func() Env {
		env := testEnv()
		env.Harnesses["nib-default"].UID = "h-1"
		env.Harnesses["nib-default"].Generation = 3
		env.Logins["gh-example"].UID = "gh-1"
		return env
	}
	bound := func(env Env) *v1alpha1.AgentProfile {
		p := &v1alpha1.AgentProfile{Spec: *testProfile()}
		p.Spec.Validated = true
		p.Status.ValidatedFor = BindingFor(env.Harnesses["nib-default"], env.Logins["gh-example"])
		return p
	}
	tests := []struct {
		name   string
		mutate func(env *Env, p *v1alpha1.AgentProfile)
		want   bool
	}{
		{name: "current", mutate: func(*Env, *v1alpha1.AgentProfile) {}, want: true},
		{name: "not validated", mutate: func(_ *Env, p *v1alpha1.AgentProfile) { p.Spec.Validated = false }},
		{name: "no binding", mutate: func(_ *Env, p *v1alpha1.AgentProfile) { p.Status.ValidatedFor = nil }},
		{name: "harness recreated", mutate: func(env *Env, _ *v1alpha1.AgentProfile) { env.Harnesses["nib-default"].UID = "h-2" }},
		{name: "harness changed", mutate: func(env *Env, _ *v1alpha1.AgentProfile) { env.Harnesses["nib-default"].Generation = 4 }},
		{name: "harness deleted", mutate: func(env *Env, _ *v1alpha1.AgentProfile) { delete(env.Harnesses, "nib-default") }},
		{name: "login recreated", mutate: func(env *Env, _ *v1alpha1.AgentProfile) { env.Logins["gh-example"].UID = "gh-2" }},
		{name: "login deleted", mutate: func(env *Env, _ *v1alpha1.AgentProfile) { delete(env.Logins, "gh-example") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newEnv()
			p := bound(env)
			tt.mutate(&env, p)
			if got := ValidationCurrent(env, p); got != tt.want {
				t.Errorf("ValidationCurrent() = %v, want %v", got, tt.want)
			}
		})
	}
}

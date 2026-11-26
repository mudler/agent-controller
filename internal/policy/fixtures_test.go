package policy

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/mudler/agent-controller/api/v1alpha1"
)

func testPolicy() *v1alpha1.AgentPolicy {
	return &v1alpha1.AgentPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "default"},
		Spec: v1alpha1.AgentPolicySpec{
			Harnesses: []v1alpha1.HarnessKind{v1alpha1.HarnessNib, v1alpha1.HarnessClaudeCode},
			Egress: v1alpha1.PolicyEgress{
				Hosts:    []string{"api.github.com:443", "github.com:443", "models.example.com:443", "huggingface.co:443"},
				AllowAll: true,
			},
			Limits: v1alpha1.PolicyLimits{
				SessionSeconds:        21600,
				CPU:                   resource.MustParse("8"),
				Memory:                resource.MustParse("32Gi"),
				Workspace:             resource.MustParse("50Gi"),
				MaxParallelPerProfile: 4,
				GlobalSlots:           6,
			},
			ModelEndpoints: []string{"https://models.example.com/v1", "https://api.example.com"},
		},
	}
}

func login(name string, kind v1alpha1.LoginKind, repos ...string) *v1alpha1.Login {
	return &v1alpha1.Login{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       v1alpha1.LoginSpec{Kind: kind, Repos: repos},
	}
}

func testHarness() *v1alpha1.HarnessProfile {
	return &v1alpha1.HarnessProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "nib-default"},
		Spec: v1alpha1.HarnessProfileSpec{
			Harness:  v1alpha1.HarnessNib,
			Version:  "v0.14.2",
			Model:    "default",
			Endpoint: "https://models.example.com/v1",
			Login:    "model-example",
			Config:   &apiextensionsv1.JSON{Raw: []byte(`{"tools":["bash","read"],"contextTokens":32768}`)},
		},
	}
}

func testEnv() Env {
	ghEgress := login("gh-all", v1alpha1.LoginGitHubToken, "example-org/*")
	ghEgress.Spec.EgressAll = true
	return Env{
		Policy: testPolicy(),
		Logins: map[string]*v1alpha1.Login{
			"gh-example":    login("gh-example", v1alpha1.LoginGitHubToken, "example-org/example-repo"),
			"gh-all":        ghEgress,
			"model-example": login("model-example", v1alpha1.LoginModelAPIKey),
			"claude-file":   login("claude-file", v1alpha1.LoginClaudeCredentials),
		},
		Harnesses: map[string]*v1alpha1.HarnessProfile{"nib-default": testHarness()},
		HarnessVersions: map[v1alpha1.HarnessKind][]string{
			v1alpha1.HarnessNib:        {"v0.14.2"},
			v1alpha1.HarnessClaudeCode: {"2.1.326"},
		},
	}
}

func testProfile() *v1alpha1.AgentProfileSpec {
	return &v1alpha1.AgentProfileSpec{
		Harness: "nib-default",
		Repos:   []v1alpha1.RepoRef{{Slug: "example-org/example-repo", Branch: "main"}},
		GitHub:  "gh-example",
		Prompt:  "Do the work.",
		Egress: v1alpha1.ProfileEgress{
			Mode:       v1alpha1.EgressAllowlist,
			ExtraHosts: []string{"github.com:443", "api.github.com:443"},
		},
		Budget: v1alpha1.Budget{SessionSeconds: 3000, Iterations: 100},
		Resources: v1alpha1.ProfileResources{
			CPU:       resource.MustParse("2"),
			Memory:    resource.MustParse("4Gi"),
			Workspace: resource.MustParse("8Gi"),
			Tmp:       resource.MustParse("1Gi"),
		},
		Schedule:    &v1alpha1.Schedule{Cron: "30 1 * * *", TimeZone: "Etc/UTC"},
		MaxParallel: 2,
		Memory:      v1alpha1.MemoryEnabled,
	}
}

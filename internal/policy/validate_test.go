package policy

import (
	"slices"
	"strings"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/utils/ptr"

	"github.com/mudler/agent-controller/api/v1alpha1"
)

// findField returns the error for field, or nil.
func findField(errs Errors, field string) *FieldError {
	for i := range errs {
		if errs[i].Field == field {
			return &errs[i]
		}
	}
	return nil
}

func TestValidateHarness(t *testing.T) {
	tests := []struct {
		name        string
		mutate      func(env *Env, h *v1alpha1.HarnessProfileSpec)
		field       string
		reason      string
		wantAllowed bool
	}{
		{name: "valid", mutate: func(*Env, *v1alpha1.HarnessProfileSpec) {}},
		{
			name: "kind not in policy",
			mutate: func(env *Env, _ *v1alpha1.HarnessProfileSpec) {
				env.Policy.Spec.Harnesses = []v1alpha1.HarnessKind{v1alpha1.HarnessClaudeCode}
			},
			field:       "harness",
			reason:      "not in policy",
			wantAllowed: true,
		},
		{
			name:        "version not installed",
			mutate:      func(_ *Env, h *v1alpha1.HarnessProfileSpec) { h.Version = "v9" },
			field:       "version",
			reason:      "not installed",
			wantAllowed: true,
		},
		{
			name:        "endpoint not in policy",
			mutate:      func(_ *Env, h *v1alpha1.HarnessProfileSpec) { h.Endpoint = "https://evil.example.net/v1" },
			field:       "endpoint",
			reason:      "not in policy",
			wantAllowed: true,
		},
		{
			name:   "login missing",
			mutate: func(_ *Env, h *v1alpha1.HarnessProfileSpec) { h.Login = "nope" },
			field:  "login",
			reason: "not found",
		},
		{
			name:        "login wrong kind",
			mutate:      func(_ *Env, h *v1alpha1.HarnessProfileSpec) { h.Login = "gh-example" },
			field:       "login",
			reason:      "cannot serve",
			wantAllowed: true,
		},
		{
			name:   "claude credentials on nib",
			mutate: func(_ *Env, h *v1alpha1.HarnessProfileSpec) { h.Login = "claude-file" },
			field:  "login",
			reason: "cannot serve",
		},
		{
			name: "bad config",
			mutate: func(_ *Env, h *v1alpha1.HarnessProfileSpec) {
				h.Config = &apiextensionsv1.JSON{Raw: []byte(`{"maxTurns":3}`)}
			},
			field:  "config.maxTurns",
			reason: "unknown key",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := testEnv()
			h := testHarness().Spec
			tt.mutate(&env, &h)
			errs := ValidateHarness(env, &h)
			checkErr(t, errs, tt.field, tt.reason, tt.wantAllowed)
		})
	}
}

func TestValidateHarnessClaudeCredentials(t *testing.T) {
	env := testEnv()
	h := v1alpha1.HarnessProfileSpec{
		Harness:  v1alpha1.HarnessClaudeCode,
		Version:  "2.1.326",
		Model:    "claude-model",
		Endpoint: "https://api.example.com",
		Login:    "claude-file",
		Config:   &apiextensionsv1.JSON{Raw: []byte(`{"tools":["Bash","Read","Bash(git *)"],"maxTurns":50}`)},
	}
	if errs := ValidateHarness(env, &h); errs != nil {
		t.Fatalf("ValidateHarness() = %v, want nil", errs)
	}
}

func TestValidateHarnessNoPolicy(t *testing.T) {
	env := testEnv()
	env.Policy = nil
	h := testHarness().Spec
	checkErr(t, ValidateHarness(env, &h), "policy", "no AgentPolicy", false)
}

func checkErr(t *testing.T, errs Errors, field, reason string, wantAllowed bool) {
	t.Helper()
	if field == "" {
		if errs != nil {
			t.Fatalf("errors = %v, want none", errs)
		}
		return
	}
	fe := findField(errs, field)
	if fe == nil {
		t.Fatalf("errors = %v, want one for field %q", errs, field)
	}
	if !strings.Contains(fe.Reason, reason) {
		t.Errorf("reason = %q, want it to contain %q", fe.Reason, reason)
	}
	if wantAllowed && len(fe.Allowed) == 0 {
		t.Errorf("allowed is empty for %q", field)
	}
}

func TestValidateProfile(t *testing.T) {
	tests := []struct {
		name        string
		mutate      func(env *Env, p *v1alpha1.AgentProfileSpec)
		field       string
		reason      string
		wantAllowed bool
	}{
		{name: "valid", mutate: func(*Env, *v1alpha1.AgentProfileSpec) {}},
		{
			name:   "harness missing",
			mutate: func(_ *Env, p *v1alpha1.AgentProfileSpec) { p.Harness = "nope" },
			field:  "harness",
			reason: "not found",
		},
		{
			name:   "harness invalid",
			mutate: func(env *Env, _ *v1alpha1.AgentProfileSpec) { env.Harnesses["nib-default"].Spec.Version = "v0" },
			field:  "harness",
			reason: "harness profile is invalid",
		},
		{
			name: "two repos",
			mutate: func(_ *Env, p *v1alpha1.AgentProfileSpec) {
				p.Repos = append(p.Repos, v1alpha1.RepoRef{Slug: "example-org/other", Branch: "main"})
			},
			field:  "repos",
			reason: "exactly one",
		},
		{
			name:   "no repos",
			mutate: func(_ *Env, p *v1alpha1.AgentProfileSpec) { p.Repos = nil },
			field:  "repos",
			reason: "exactly one",
		},
		{
			name:   "github login missing",
			mutate: func(_ *Env, p *v1alpha1.AgentProfileSpec) { p.GitHub = "nope" },
			field:  "github",
			reason: "not found",
		},
		{
			name:        "github login wrong kind",
			mutate:      func(_ *Env, p *v1alpha1.AgentProfileSpec) { p.GitHub = "model-example" },
			field:       "github",
			reason:      "not github-token",
			wantAllowed: true,
		},
		{
			name:        "repo not allowed by login",
			mutate:      func(_ *Env, p *v1alpha1.AgentProfileSpec) { p.Repos[0].Slug = "other-org/repo" },
			field:       "repos[0].slug",
			reason:      "not allowed by login",
			wantAllowed: true,
		},
		{
			name: "egress all denied by policy",
			mutate: func(env *Env, p *v1alpha1.AgentProfileSpec) {
				env.Policy.Spec.Egress.AllowAll = false
				p.GitHub = "gh-all"
				p.Egress = v1alpha1.ProfileEgress{Mode: v1alpha1.EgressAll}
			},
			field:  "egress.mode",
			reason: "policy does not allow",
		},
		{
			name: "egress all denied by login",
			mutate: func(_ *Env, p *v1alpha1.AgentProfileSpec) {
				p.Egress = v1alpha1.ProfileEgress{Mode: v1alpha1.EgressAll}
			},
			field:  "egress.mode",
			reason: "does not allow egress all",
		},
		{
			name: "extra hosts in all mode",
			mutate: func(_ *Env, p *v1alpha1.AgentProfileSpec) {
				p.GitHub = "gh-all"
				p.Egress = v1alpha1.ProfileEgress{Mode: v1alpha1.EgressAll, ExtraHosts: []string{"github.com:443"}}
			},
			field:  "egress.extraHosts",
			reason: "only allowed in allowlist mode",
		},
		{
			name:        "extra host not in policy",
			mutate:      func(_ *Env, p *v1alpha1.AgentProfileSpec) { p.Egress.ExtraHosts = []string{"evil.example.net:443"} },
			field:       "egress.extraHosts[0]",
			reason:      "not in policy",
			wantAllowed: true,
		},
		{
			name:   "extra host bad format",
			mutate: func(_ *Env, p *v1alpha1.AgentProfileSpec) { p.Egress.ExtraHosts = []string{"github.com"} },
			field:  "egress.extraHosts[0]",
			reason: "host:443",
		},
		{
			name: "endpoint host not in policy hosts",
			mutate: func(env *Env, _ *v1alpha1.AgentProfileSpec) {
				env.Policy.Spec.Egress.Hosts = []string{"api.github.com:443", "github.com:443"}
			},
			field:       "harness",
			reason:      "endpoint host models.example.com:443 not in policy",
			wantAllowed: true,
		},
		{
			name:   "session over ceiling",
			mutate: func(_ *Env, p *v1alpha1.AgentProfileSpec) { p.Budget.SessionSeconds = 21601 },
			field:  "budget.sessionSeconds",
			reason: "between 1 and 21600",
		},
		{
			name:   "session zero",
			mutate: func(_ *Env, p *v1alpha1.AgentProfileSpec) { p.Budget.SessionSeconds = 0 },
			field:  "budget.sessionSeconds",
			reason: "between 1",
		},
		{
			name:   "iterations zero",
			mutate: func(_ *Env, p *v1alpha1.AgentProfileSpec) { p.Budget.Iterations = 0 },
			field:  "budget.iterations",
			reason: "at least 1",
		},
		{
			name:   "cpu over ceiling",
			mutate: func(_ *Env, p *v1alpha1.AgentProfileSpec) { p.Resources.CPU = resource.MustParse("9") },
			field:  "resources.cpu",
			reason: "exceeds policy limit 8",
		},
		{
			name:   "memory over ceiling",
			mutate: func(_ *Env, p *v1alpha1.AgentProfileSpec) { p.Resources.Memory = resource.MustParse("33Gi") },
			field:  "resources.memory",
			reason: "exceeds policy limit",
		},
		{
			name:   "workspace over ceiling",
			mutate: func(_ *Env, p *v1alpha1.AgentProfileSpec) { p.Resources.Workspace = resource.MustParse("51Gi") },
			field:  "resources.workspace",
			reason: "exceeds policy limit",
		},
		{
			name:   "tmp over workspace ceiling",
			mutate: func(_ *Env, p *v1alpha1.AgentProfileSpec) { p.Resources.Tmp = resource.MustParse("60Gi") },
			field:  "resources.tmp",
			reason: "exceeds policy limit 50Gi",
		},
		{
			name:   "cpu zero",
			mutate: func(_ *Env, p *v1alpha1.AgentProfileSpec) { p.Resources.CPU = resource.MustParse("0") },
			field:  "resources.cpu",
			reason: "greater than zero",
		},
		{
			name:   "max parallel over ceiling",
			mutate: func(_ *Env, p *v1alpha1.AgentProfileSpec) { p.MaxParallel = 5 },
			field:  "maxParallel",
			reason: "between 1 and 4",
		},
		{
			name:   "max parallel zero",
			mutate: func(_ *Env, p *v1alpha1.AgentProfileSpec) { p.MaxParallel = 0 },
			field:  "maxParallel",
			reason: "between 1 and 4",
		},
		{
			name:   "bad cron",
			mutate: func(_ *Env, p *v1alpha1.AgentProfileSpec) { p.Schedule.Cron = "61 * * * *" },
			field:  "schedule.cron",
			reason: "invalid cron",
		},
		{
			name:   "six-field cron",
			mutate: func(_ *Env, p *v1alpha1.AgentProfileSpec) { p.Schedule.Cron = "0 30 1 * * *" },
			field:  "schedule.cron",
			reason: "invalid cron",
		},
		{
			name:   "bad time zone",
			mutate: func(_ *Env, p *v1alpha1.AgentProfileSpec) { p.Schedule.TimeZone = "Mars/Olympus" },
			field:  "schedule.timeZone",
			reason: "unknown time zone",
		},
		{
			name:   "empty prompt",
			mutate: func(_ *Env, p *v1alpha1.AgentProfileSpec) { p.Prompt = " " },
			field:  "prompt",
			reason: "must not be empty",
		},
		{
			name:   "long prompt",
			mutate: func(_ *Env, p *v1alpha1.AgentProfileSpec) { p.Prompt = strings.Repeat("x", MaxPromptLength+1) },
			field:  "prompt",
			reason: "at most",
		},
		{
			name:   "bad memory mode",
			mutate: func(_ *Env, p *v1alpha1.AgentProfileSpec) { p.Memory = "sometimes" },
			field:  "memory",
			reason: "enabled or disabled",
		},
		{
			name: "egress all accepted",
			mutate: func(_ *Env, p *v1alpha1.AgentProfileSpec) {
				p.GitHub = "gh-all"
				p.Egress = v1alpha1.ProfileEgress{Mode: v1alpha1.EgressAll}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := testEnv()
			p := testProfile()
			tt.mutate(&env, p)
			checkErr(t, ValidateProfile(env, p), tt.field, tt.reason, tt.wantAllowed)
		})
	}
}

func TestValidateRun(t *testing.T) {
	disabled := testProfile()
	disabled.Memory = v1alpha1.MemoryDisabled
	tests := []struct {
		name    string
		profile *v1alpha1.AgentProfileSpec
		run     v1alpha1.AgentRunSpec
		field   string
		reason  string
	}{
		{name: "valid", profile: testProfile(), run: v1alpha1.AgentRunSpec{Profile: "p", Task: "x"}},
		{
			name:    "task too long",
			profile: testProfile(),
			run:     v1alpha1.AgentRunSpec{Profile: "p", Task: strings.Repeat("x", 4001)},
			field:   "task",
			reason:  "at most 4000",
		},
		{name: "task at limit", profile: testProfile(), run: v1alpha1.AgentRunSpec{Profile: "p", Task: strings.Repeat("x", 4000)}},
		{
			name:    "memory on disabled profile",
			profile: disabled,
			run:     v1alpha1.AgentRunSpec{Profile: "p", Memory: ptr.To(true)},
			field:   "memory",
			reason:  "disabled",
		},
		{name: "memory off on disabled profile", profile: disabled, run: v1alpha1.AgentRunSpec{Profile: "p", Memory: ptr.To(false)}},
		{name: "no profile", profile: testProfile(), run: v1alpha1.AgentRunSpec{}, field: "profile", reason: "must not be empty"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checkErr(t, ValidateRun(testEnv(), tt.profile, &tt.run), tt.field, tt.reason, false)
		})
	}
}

func TestValidateHarnessConfig(t *testing.T) {
	tests := []struct {
		name   string
		kind   v1alpha1.HarnessKind
		raw    string
		field  string
		reason string
	}{
		{name: "nil", kind: v1alpha1.HarnessNib, raw: ""},
		{name: "nib valid", kind: v1alpha1.HarnessNib, raw: `{"tools":["bash","repo_map"],"contextTokens":1024}`},
		{name: "nib unknown tool", kind: v1alpha1.HarnessNib, raw: `{"tools":["bash","curl"]}`, field: "config.tools[1]", reason: "unknown nib tool"},
		{name: "nib tools not list", kind: v1alpha1.HarnessNib, raw: `{"tools":"bash"}`, field: "config.tools", reason: "list of strings"},
		{name: "nib context low", kind: v1alpha1.HarnessNib, raw: `{"contextTokens":1023}`, field: "config.contextTokens", reason: "between"},
		{name: "nib context high", kind: v1alpha1.HarnessNib, raw: `{"contextTokens":2000001}`, field: "config.contextTokens", reason: "between"},
		{name: "nib context float", kind: v1alpha1.HarnessNib, raw: `{"contextTokens":1.5}`, field: "config.contextTokens", reason: "integer"},
		{name: "nib unknown key", kind: v1alpha1.HarnessNib, raw: `{"maxTurns":3}`, field: "config.maxTurns", reason: "unknown key"},
		{name: "not an object", kind: v1alpha1.HarnessNib, raw: `[1]`, field: "config", reason: "JSON object"},
		{name: "claude valid", kind: v1alpha1.HarnessClaudeCode, raw: `{"tools":["Read","Bash(go test *)"],"maxTurns":10000,"contextTokens":200000}`},
		{name: "claude empty tools", kind: v1alpha1.HarnessClaudeCode, raw: `{"tools":[]}`, field: "config.tools", reason: "must not be empty"},
		{name: "claude bad tool", kind: v1alpha1.HarnessClaudeCode, raw: `{"tools":["rm -rf"]}`, field: "config.tools[0]", reason: "tool name"},
		{name: "claude max turns zero", kind: v1alpha1.HarnessClaudeCode, raw: `{"maxTurns":0}`, field: "config.maxTurns", reason: "between 1 and 10000"},
		{name: "claude unknown key", kind: v1alpha1.HarnessClaudeCode, raw: `{"env":{}}`, field: "config.env", reason: "unknown key"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var raw *apiextensionsv1.JSON
			if tt.raw != "" {
				raw = &apiextensionsv1.JSON{Raw: []byte(tt.raw)}
			}
			checkErr(t, ValidateHarnessConfig(tt.kind, raw), tt.field, tt.reason, false)
		})
	}
}

func TestRunHosts(t *testing.T) {
	h := testHarness().Spec
	p := testProfile()
	p.Egress.ExtraHosts = []string{"github.com:443", "models.example.com:443", "api.github.com:443", "github.com:443"}
	got := RunHosts(p, &h)
	want := []string{"api.github.com:443", "github.com:443", "models.example.com:443"}
	if !slices.Equal(got, want) {
		t.Errorf("RunHosts() = %v, want %v", got, want)
	}

	p.Egress = v1alpha1.ProfileEgress{Mode: v1alpha1.EgressAll}
	if got := RunHosts(p, &h); got == nil || len(got) != 0 {
		t.Errorf("RunHosts(all) = %#v, want empty non-nil", got)
	}
}

func TestEndpointHost(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{in: "https://models.example.com/v1", want: "models.example.com:443"},
		{in: "https://API.example.com:443", want: "api.example.com:443"},
		{in: "http://models.example.com", wantErr: true},
		{in: "https://models.example.com:8443/v1", wantErr: true},
		{in: "::", wantErr: true},
	}
	for _, tt := range tests {
		got, err := EndpointHost(tt.in)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("EndpointHost(%q) = %q, %v; want %q, err %v", tt.in, got, err, tt.want, tt.wantErr)
		}
	}
}

func TestRepoAllowed(t *testing.T) {
	l := login("gh", v1alpha1.LoginGitHubToken, "example-org/one", "other-org/*")
	tests := map[string]bool{
		"example-org/one":   true,
		"example-org/two":   false,
		"other-org/any":     true,
		"other-orgx/any":    false,
		"Example-Org/one":   false,
		"no-slash":          false,
		"example-org/one/x": false,
	}
	for slug, want := range tests {
		if got := RepoAllowed(l, slug); got != want {
			t.Errorf("RepoAllowed(%q) = %v, want %v", slug, got, want)
		}
	}
}

func TestErrorsFormat(t *testing.T) {
	var errs Errors
	if errs.Err() != nil {
		t.Errorf("empty Errors.Err() should be nil")
	}
	errs = Errors{{Field: "a", Reason: "x"}, {Field: "b", Reason: "y"}}
	if got := errs.Error(); got != "a: x; b: y" {
		t.Errorf("Error() = %q", got)
	}
}

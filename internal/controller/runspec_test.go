package controller

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"github.com/mudler/agent-controller/api/v1alpha1"
)

func exampleConfig() Config {
	return Config{
		Namespace:           "agents",
		LoginsNamespace:     "agent-logins",
		MemoryClaimTemplate: "%s-memory",
		ProxyIdleSeconds:    600,
		HarnessVersions:     map[v1alpha1.HarnessKind][]string{v1alpha1.HarnessNib: {"v0.14.2"}},
		RetainPerProfile:    200,
	}
}

func exampleRun() *v1alpha1.AgentRun {
	return &v1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "example-x7k2p", Namespace: "agents", UID: "uid-run"},
		Spec: v1alpha1.AgentRunSpec{
			Profile:     "example",
			Task:        "Do X.",
			Trigger:     v1alpha1.TriggerManual,
			RequestedBy: "planner",
		},
	}
}

func exampleProfile() *v1alpha1.AgentProfile {
	return &v1alpha1.AgentProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "example", Namespace: "agents", Generation: 7},
		Spec: v1alpha1.AgentProfileSpec{
			Harness: "nib-default",
			Repos:   []v1alpha1.RepoRef{{Slug: "example-org/example-repo", Branch: "main"}},
			GitHub:  "gh-example",
			Prompt:  "Profile prompt.",
			Egress: v1alpha1.ProfileEgress{
				Mode:       v1alpha1.EgressAllowlist,
				ExtraHosts: []string{"github.com:443", "api.github.com:443", "github.com:443"},
			},
			Budget: v1alpha1.Budget{SessionSeconds: 3000, Iterations: 100},
			Resources: v1alpha1.ProfileResources{
				CPU:       resource.MustParse("2"),
				Memory:    resource.MustParse("4Gi"),
				Workspace: resource.MustParse("8Gi"),
				Tmp:       resource.MustParse("1Gi"),
			},
			MaxParallel: 1,
			Memory:      v1alpha1.MemoryEnabled,
			Validated:   true,
		},
	}
}

func exampleHarness() *v1alpha1.HarnessProfile {
	return &v1alpha1.HarnessProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "nib-default", Namespace: "agents"},
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

func exampleLogins() (gh, model *v1alpha1.Login) {
	gh = &v1alpha1.Login{
		ObjectMeta: metav1.ObjectMeta{Name: "gh-example"},
		Spec:       v1alpha1.LoginSpec{Kind: v1alpha1.LoginGitHubToken, Repos: []string{"example-org/*"}},
	}
	model = &v1alpha1.Login{
		ObjectMeta: metav1.ObjectMeta{Name: "model-example"},
		Spec:       v1alpha1.LoginSpec{Kind: v1alpha1.LoginModelAPIKey},
	}
	return gh, model
}

func resolveExample(t *testing.T, mutate func(run *v1alpha1.AgentRun, prof *v1alpha1.AgentProfile)) *RunSpec {
	t.Helper()
	run, prof := exampleRun(), exampleProfile()
	if mutate != nil {
		mutate(run, prof)
	}
	gh, model := exampleLogins()
	spec, err := Resolve(exampleConfig(), run, prof, exampleHarness(), gh, model)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	return spec
}

func TestRunSpecGolden(t *testing.T) {
	spec := resolveExample(t, nil)
	got, err := spec.JSON()
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join("testdata", "runspec.golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, bytes.TrimRight(want, "\n")) {
		t.Errorf("resolved run differs from golden file\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestResolveMemoryOff(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(run *v1alpha1.AgentRun, prof *v1alpha1.AgentProfile)
	}{
		{name: "run memory false", mutate: func(run *v1alpha1.AgentRun, _ *v1alpha1.AgentProfile) { run.Spec.Memory = ptr.To(false) }},
		{name: "profile disabled", mutate: func(_ *v1alpha1.AgentRun, prof *v1alpha1.AgentProfile) { prof.Spec.Memory = v1alpha1.MemoryDisabled }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := resolveExample(t, tt.mutate)
			if spec.Memory != (RunSpecMemory{}) {
				t.Errorf("Memory = %+v, want all empty", spec.Memory)
			}
		})
	}
}

func TestResolveLauncherLock(t *testing.T) {
	cfg := exampleConfig()
	cfg.LauncherLockTemplate = "legacy-%s"
	gh, model := exampleLogins()
	spec, err := Resolve(cfg, exampleRun(), exampleProfile(), exampleHarness(), gh, model)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Memory.LauncherLock != "legacy-example" {
		t.Errorf("LauncherLock = %q, want legacy-example", spec.Memory.LauncherLock)
	}
}

func TestResolveEgressAllHasNoHosts(t *testing.T) {
	spec := resolveExample(t, func(_ *v1alpha1.AgentRun, prof *v1alpha1.AgentProfile) {
		prof.Spec.Egress = v1alpha1.ProfileEgress{Mode: v1alpha1.EgressAll}
	})
	b, err := spec.JSON()
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Egress struct {
			Mode  string    `json:"mode"`
			Hosts *[]string `json:"hosts"`
		} `json:"egress"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.Egress.Mode != "all" || out.Egress.Hosts == nil || len(*out.Egress.Hosts) != 0 {
		t.Errorf("egress = %+v, want mode all and an empty hosts list", out.Egress)
	}
}

func TestResolveHostsSortedAndDeduplicated(t *testing.T) {
	spec := resolveExample(t, nil)
	if !slices.IsSorted(spec.Egress.Hosts) {
		t.Errorf("hosts %v are not sorted", spec.Egress.Hosts)
	}
	if len(slices.Compact(slices.Clone(spec.Egress.Hosts))) != len(spec.Egress.Hosts) {
		t.Errorf("hosts %v have duplicates", spec.Egress.Hosts)
	}
}

func TestResolveNilConfigIsEmptyObject(t *testing.T) {
	run, prof := exampleRun(), exampleProfile()
	h := exampleHarness()
	h.Spec.Config = nil
	gh, model := exampleLogins()
	spec, err := Resolve(exampleConfig(), run, prof, h, gh, model)
	if err != nil {
		t.Fatal(err)
	}
	if string(spec.Harness.Config) != "{}" {
		t.Errorf("config = %s, want {}", spec.Harness.Config)
	}
}

func TestResolveRejectsWrongLogins(t *testing.T) {
	gh, model := exampleLogins()
	if _, err := Resolve(exampleConfig(), exampleRun(), exampleProfile(), exampleHarness(), model, model); err == nil {
		t.Error("model login as github: want error")
	}
	other := model.DeepCopy()
	other.Name = "other"
	if _, err := Resolve(exampleConfig(), exampleRun(), exampleProfile(), exampleHarness(), gh, other); err == nil {
		t.Error("model login not the harness login: want error")
	}
}

func TestRunSpecConfigMap(t *testing.T) {
	run := exampleRun()
	cm := resolveExample(t, nil).ConfigMap(run)
	if cm.Name != "example-x7k2p-spec" || cm.Namespace != "agents" {
		t.Errorf("ConfigMap name = %s/%s", cm.Namespace, cm.Name)
	}
	if cm.Labels[LabelRun] != run.Name {
		t.Errorf("labels = %v", cm.Labels)
	}
	if len(cm.OwnerReferences) != 1 || cm.OwnerReferences[0].UID != run.UID || !ptr.Deref(cm.OwnerReferences[0].Controller, false) {
		t.Errorf("owner references = %+v", cm.OwnerReferences)
	}
	if !json.Valid([]byte(cm.Data[RunSpecKey])) {
		t.Errorf("data[%s] is not valid JSON", RunSpecKey)
	}
}

func TestComposePrompt(t *testing.T) {
	if got := ComposePrompt("P.", ""); got != "P." {
		t.Errorf("no task: %q", got)
	}
	if got, want := ComposePrompt("P.", "T."), "P.\n\nTask for this run:\nT."; got != want {
		t.Errorf("with task: %q, want %q", got, want)
	}
}

package v1alpha1_test

import (
	"context"
	"os"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mudler/agent-controller/api/v1alpha1"
	"github.com/mudler/agent-controller/internal/testenv"
)

func TestMain(m *testing.M) {
	code := m.Run()
	testenv.Stop()
	os.Exit(code)
}

func newNamespace(t *testing.T, c client.Client) string {
	t.Helper()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "crd-"}}
	if err := c.Create(context.Background(), ns); err != nil {
		t.Fatalf("creating namespace: %v", err)
	}
	return ns.Name
}

func validPolicy(ns string) *v1alpha1.AgentPolicy {
	return &v1alpha1.AgentPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: ns},
		Spec: v1alpha1.AgentPolicySpec{
			Harnesses: []v1alpha1.HarnessKind{v1alpha1.HarnessNib},
			Egress:    v1alpha1.PolicyEgress{Hosts: []string{"github.com:443"}},
			Limits: v1alpha1.PolicyLimits{
				SessionSeconds:        3600,
				CPU:                   resource.MustParse("8"),
				Memory:                resource.MustParse("32Gi"),
				Workspace:             resource.MustParse("50Gi"),
				MaxParallelPerProfile: 4,
				GlobalSlots:           6,
			},
			ModelEndpoints: []string{"https://models.example.com/v1"},
		},
	}
}

func validProfile(ns, name string) *v1alpha1.AgentProfile {
	return &v1alpha1.AgentProfile{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: v1alpha1.AgentProfileSpec{
			Harness: "nib-default",
			Repos:   []v1alpha1.RepoRef{{Slug: "example-org/example-repo", Branch: "main"}},
			GitHub:  "gh-example",
			Prompt:  "Do the work.",
			Egress:  v1alpha1.ProfileEgress{Mode: v1alpha1.EgressAllowlist},
			Budget:  v1alpha1.Budget{SessionSeconds: 600, Iterations: 10},
			Resources: v1alpha1.ProfileResources{
				CPU:       resource.MustParse("1"),
				Memory:    resource.MustParse("1Gi"),
				Workspace: resource.MustParse("1Gi"),
				Tmp:       resource.MustParse("1Gi"),
			},
			MaxParallel: 1,
			Memory:      v1alpha1.MemoryEnabled,
		},
	}
}

func validRun(ns string) *v1alpha1.AgentRun {
	return &v1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{GenerateName: "example-", Namespace: ns},
		Spec: v1alpha1.AgentRunSpec{
			Profile:     "example",
			Task:        "Fix it.",
			Trigger:     v1alpha1.TriggerManual,
			RequestedBy: "planner",
		},
	}
}

func TestCRDsAcceptValidObjects(t *testing.T) {
	c := testenv.Client(t)
	ctx := context.Background()
	ns := newNamespace(t, c)

	objs := []client.Object{
		validPolicy(ns),
		&v1alpha1.Login{
			ObjectMeta: metav1.ObjectMeta{Name: "gh-example", Namespace: ns},
			Spec: v1alpha1.LoginSpec{
				Kind:  v1alpha1.LoginGitHubToken,
				Repos: []string{"example-org/example-repo", "example-org/*"},
			},
		},
		&v1alpha1.HarnessProfile{
			ObjectMeta: metav1.ObjectMeta{Name: "nib-default", Namespace: ns},
			Spec: v1alpha1.HarnessProfileSpec{
				Harness:  v1alpha1.HarnessNib,
				Version:  "v0.14.2",
				Model:    "default",
				Endpoint: "https://models.example.com/v1",
				Login:    "model-example",
				Config:   &apiextensionsv1.JSON{Raw: []byte(`{"tools":["bash"],"contextTokens":32768}`)},
			},
		},
		validProfile(ns, "example"),
		validRun(ns),
	}
	for _, o := range objs {
		if err := c.Create(ctx, o); err != nil {
			t.Fatalf("creating %T: %v", o, err)
		}
	}

	h := &v1alpha1.HarnessProfile{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: "nib-default"}, h); err != nil {
		t.Fatalf("getting harness profile: %v", err)
	}
	if !strings.Contains(string(h.Spec.Config.Raw), "contextTokens") {
		t.Errorf("config was pruned: %s", h.Spec.Config.Raw)
	}
}

func TestCRDsRejectInvalidObjects(t *testing.T) {
	c := testenv.Client(t)
	ctx := context.Background()
	ns := newNamespace(t, c)

	badPolicyName := validPolicy(ns)
	badPolicyName.Name = "other"

	badHost := validPolicy(ns)
	badHost.Spec.Egress.Hosts = []string{"github.com:80"}

	longProfile := validProfile(ns, strings.Repeat("a", 41))
	upperProfile := validProfile(ns, "Example")
	twoRepos := validProfile(ns, "two-repos")
	twoRepos.Spec.Repos = append(twoRepos.Spec.Repos, v1alpha1.RepoRef{Slug: "a/b", Branch: "main"})

	longTask := validRun(ns)
	longTask.Spec.Task = strings.Repeat("x", v1alpha1.MaxTaskLength+1)

	badLogin := &v1alpha1.Login{
		ObjectMeta: metav1.ObjectMeta{Name: "model", Namespace: ns},
		Spec:       v1alpha1.LoginSpec{Kind: v1alpha1.LoginModelAPIKey, Repos: []string{"a/b"}},
	}

	tests := []struct {
		name string
		obj  client.Object
		want string
	}{
		{name: "policy name", obj: badPolicyName, want: "must be named default"},
		{name: "policy host port", obj: badHost, want: "spec.egress.hosts"},
		{name: "profile name too long", obj: longProfile, want: "DNS-1035"},
		{name: "profile name uppercase", obj: upperProfile, want: ""},
		{name: "profile two repos", obj: twoRepos, want: "spec.repos"},
		{name: "run task too long", obj: longTask, want: "spec.task"},
		{name: "login repos on model key", obj: badLogin, want: "only to github-token"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := c.Create(ctx, tt.obj)
			if err == nil {
				t.Fatalf("create succeeded, want rejection")
			}
			if tt.want != "" && !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}

func TestAgentRunSpecRules(t *testing.T) {
	c := testenv.Client(t)
	ctx := context.Background()
	ns := newNamespace(t, c)

	run := validRun(ns)
	if err := c.Create(ctx, run); err != nil {
		t.Fatalf("creating run: %v", err)
	}

	changeTask := run.DeepCopy()
	changeTask.Spec.Task = "Other task."
	if err := c.Update(ctx, changeTask); err == nil || !strings.Contains(err.Error(), "task is immutable") {
		t.Errorf("task update error = %v, want immutable", err)
	}

	changeProfile := run.DeepCopy()
	changeProfile.Spec.Profile = "other"
	if err := c.Update(ctx, changeProfile); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Errorf("profile update error = %v, want immutable", err)
	}

	cancel := run.DeepCopy()
	cancel.Spec.Cancel = true
	if err := c.Update(ctx, cancel); err != nil {
		t.Fatalf("setting cancel: %v", err)
	}
	uncancel := cancel.DeepCopy()
	uncancel.Spec.Cancel = false
	if err := c.Update(ctx, uncancel); err == nil || !strings.Contains(err.Error(), "cannot be undone") {
		t.Errorf("uncancel error = %v, want rejection", err)
	}

	withMemory := validRun(ns)
	withMemory.Spec.Memory = ptr.To(false)
	if err := c.Create(ctx, withMemory); err != nil {
		t.Fatalf("creating memory-off run: %v", err)
	}
}

func TestPhaseFinal(t *testing.T) {
	final := map[v1alpha1.Phase]bool{
		v1alpha1.PhaseQueued: false, v1alpha1.PhaseStarting: false, v1alpha1.PhaseRunning: false,
		v1alpha1.PhaseSucceeded: true, v1alpha1.PhaseFailed: true, v1alpha1.PhaseCancelled: true,
		v1alpha1.PhaseTimedOut: true, v1alpha1.PhaseRejected: true, "": false,
	}
	for p, want := range final {
		if got := p.Final(); got != want {
			t.Errorf("%q.Final() = %v, want %v", p, got, want)
		}
	}
}

func TestLoginKindSecretKey(t *testing.T) {
	want := map[v1alpha1.LoginKind]string{
		v1alpha1.LoginGitHubToken:       "token",
		v1alpha1.LoginModelAPIKey:       "api-key",
		v1alpha1.LoginClaudeCredentials: "credentials.json",
	}
	for k, w := range want {
		if got := k.SecretKey(); got != w {
			t.Errorf("%s.SecretKey() = %q, want %q", k, got, w)
		}
	}
}

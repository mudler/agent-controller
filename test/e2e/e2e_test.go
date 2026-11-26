// Package e2e runs the controller and the server in one process against
// envtest and drives them the way an agent and the owner would.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	batchv1 "k8s.io/api/batch/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mudler/agent-controller/api/v1alpha1"
	"github.com/mudler/agent-controller/internal/controller"
	"github.com/mudler/agent-controller/internal/server"
	"github.com/mudler/agent-controller/internal/testenv"
)

func TestMain(m *testing.M) {
	code := m.Run()
	testenv.Stop()
	os.Exit(code)
}

const (
	ghValue    = "e2e-github-value-91b2"
	modelValue = "e2e-model-value-55d0"
	rotated    = "e2e-rotated-value-c3e1"
)

// logs serves launcher logs to both the controller and the server.
type logs struct {
	mu   sync.Mutex
	text map[string]string
}

func (l *logs) set(pod, text string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.text[pod] = text
}

func (l *logs) get(pod string) ([]byte, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	t, ok := l.text[pod]
	if !ok {
		return nil, errors.New("no log")
	}
	return []byte(t), nil
}

func (l *logs) Tail(_ context.Context, _, pod, _ string, _ int64) ([]byte, error) { return l.get(pod) }

func (l *logs) Since(_ context.Context, _, pod, _ string, _ *metav1.Time, _ int64) ([]byte, error) {
	return l.get(pod)
}

func (l *logs) Stream(_ context.Context, w io.Writer, _, pod string, _ bool, _ int64) error {
	b, err := l.get(pod)
	if err != nil {
		return err
	}
	_, err = w.Write(b)
	return err
}

type tokens map[string]*server.Identity

func (t tokens) Lookup(_ context.Context, bearer string) (*server.Identity, error) {
	if id, ok := t[bearer]; ok {
		return id, nil
	}
	return nil, server.ErrUnauthorized
}

type env struct {
	t      *testing.T
	c      client.Client
	ns     string
	logins string
	srv    *httptest.Server
	logs   *logs
	mu     sync.Mutex
	bodies []string
}

func (e *env) record(s string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.bodies = append(e.bodies, s)
}

func eventually(t *testing.T, timeout time.Duration, fn func() error) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		err := fn()
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("condition not met after %s: %v", timeout, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func namespace(t *testing.T, c client.Client, prefix string) string {
	t.Helper()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: prefix}}
	if err := c.Create(context.Background(), ns); err != nil {
		t.Fatal(err)
	}
	return ns.Name
}

func setup(t *testing.T) *env {
	t.Helper()
	c := testenv.Client(t)
	e := &env{t: t, c: c, logs: &logs{text: map[string]string{}}}
	e.ns = namespace(t, c, "e2e-")
	e.logins = namespace(t, c, "e2e-logins-")
	history := namespace(t, c, "e2e-history-")

	pol := &v1alpha1.AgentPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: v1alpha1.PolicyName, Namespace: e.ns},
		Spec: v1alpha1.AgentPolicySpec{
			Harnesses: []v1alpha1.HarnessKind{v1alpha1.HarnessNib, v1alpha1.HarnessClaudeCode},
			Egress:    v1alpha1.PolicyEgress{Hosts: []string{"api.github.com:443", "github.com:443", "models.example.com:443"}},
			Limits: v1alpha1.PolicyLimits{
				SessionSeconds: 21600, CPU: resource.MustParse("8"), Memory: resource.MustParse("32Gi"),
				Workspace: resource.MustParse("50Gi"), MaxParallelPerProfile: 4, GlobalSlots: 6,
			},
			ModelEndpoints: []string{"https://models.example.com/v1"},
		},
	}
	if err := c.Create(context.Background(), pol); err != nil {
		t.Fatal(err)
	}

	versions := map[v1alpha1.HarnessKind][]string{v1alpha1.HarnessNib: {"v0.14.2"}}
	root, err := testenv.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	cfg := controller.Config{
		Namespace:           e.ns,
		LoginsNamespace:     e.logins,
		MemoryClaimTemplate: "%s-memory",
		ProxyIdleSeconds:    600,
		JobTemplatePath:     filepath.Join(root, "internal", "controller", "testdata", "job-template.yaml"),
		HarnessVersions:     versions,
		RetainPerProfile:    200,
	}
	mgr, err := controller.NewManager(testenv.Config(t), cfg, controller.ManagerOptions{
		MetricsBindAddress: "0", HealthProbeBindAddress: "0", Logs: e.logs,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- mgr.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("manager: %v", err)
		}
	})

	store := &server.Store{
		C: c, Namespace: e.ns, LoginsNamespace: e.logins, HistoryNamespace: history, HarnessVersions: versions,
	}
	all := map[server.Scope]bool{}
	for _, s := range server.AllScopes {
		all[s] = true
	}
	toks := tokens{
		"owner-token": {Name: "owner", Scopes: all},
		"agent-token": {Name: "planner", Scopes: map[server.Scope]bool{server.ScopeRunsRead: true, server.ScopeRunsWrite: true, server.ScopeProfilesWrite: true}},
	}
	e.srv = httptest.NewServer(server.NewHandler(store, toks, e.logs))
	t.Cleanup(e.srv.Close)
	t.Cleanup(e.checkNoSecrets)
	return e
}

func (e *env) checkNoSecrets() {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, b := range e.bodies {
		for _, v := range []string{ghValue, modelValue, rotated} {
			if strings.Contains(b, v) {
				e.t.Errorf("a response contains a login value: %s", b)
			}
		}
	}
}

func (e *env) rest(method, path string, body any, want int) string {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			e.t.Fatal(err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, e.srv.URL+path, rd)
	if err != nil {
		e.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer owner-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		e.t.Fatal(err)
	}
	e.record(string(out))
	if resp.StatusCode != want {
		e.t.Fatalf("%s %s = %d, want %d: %s", method, path, resp.StatusCode, want, out)
	}
	return string(out)
}

type bearer struct {
	token string
}

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

func (e *env) mcp(token string) *mcp.ClientSession {
	e.t.Helper()
	c := mcp.NewClient(&mcp.Implementation{Name: "e2e-agent", Version: "v0"}, nil)
	sess, err := c.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint:             e.srv.URL + "/mcp",
		HTTPClient:           &http.Client{Transport: bearer{token}},
		DisableStandaloneSSE: true,
		MaxRetries:           -1,
	}, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { _ = sess.Close() })
	return sess
}

func (e *env) call(sess *mcp.ClientSession, tool string, args any) string {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		e.t.Fatalf("%s: %v", tool, err)
	}
	raw, _ := json.Marshal(res)
	e.record(string(raw))
	var text strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			text.WriteString(tc.Text)
		}
	}
	if res.IsError {
		e.t.Fatalf("%s failed: %s", tool, text.String())
	}
	return text.String()
}

func asMap(t *testing.T, v any) map[string]any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]any{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func decode[T any](t *testing.T, s string) T {
	t.Helper()
	var v T
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("decoding %s: %v", s, err)
	}
	return v
}

func (e *env) run(name string) *v1alpha1.AgentRun {
	e.t.Helper()
	r := &v1alpha1.AgentRun{}
	if err := e.c.Get(context.Background(), client.ObjectKey{Namespace: e.ns, Name: name}, r); err != nil {
		e.t.Fatal(err)
	}
	return r
}

func (e *env) waitPhase(name string, phase v1alpha1.Phase) {
	e.t.Helper()
	eventually(e.t, 30*time.Second, func() error {
		if p := e.run(name).Status.Phase; p != phase {
			return fmt.Errorf("phase %q, want %q", p, phase)
		}
		return nil
	})
}

// playLauncher acts as the Job controller and the launcher for one run.
func (e *env) playLauncher(run string) {
	e.t.Helper()
	ctx := context.Background()
	eventually(e.t, 30*time.Second, func() error {
		return e.c.Get(ctx, client.ObjectKey{Namespace: e.ns, Name: run}, &batchv1.Job{})
	})
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: run + "-launcher", Namespace: e.ns,
			Labels: map[string]string{controller.LabelRun: run, controller.LabelComponent: controller.ComponentLauncher},
		},
		Spec: corev1.PodSpec{RestartPolicy: corev1.RestartPolicyNever, Containers: []corev1.Container{{Name: "launcher", Image: "example.com/agent-runner:current"}}},
	}
	if err := e.c.Create(ctx, pod); err != nil {
		e.t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	steps := []string{}
	for _, s := range controller.Steps[:5] {
		steps = append(steps, fmt.Sprintf(`{"step": %q, "at": %q}`, s, now))
	}
	e.logs.set(pod.Name, strings.Join(steps, "\n")+"\n")
	e.waitPhase(run, v1alpha1.PhaseRunning)

	e.logs.set(pod.Name, strings.Join(steps, "\n")+"\n"+`{"type":"result","text":"done"}`+"\n"+fmt.Sprintf(`{"step": "cleanup-done", "at": %q}`, now)+"\n")
	pod.Status.Phase = corev1.PodSucceeded
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: "launcher", Image: "example.com/agent-runner:current",
		State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Message: `{"harnessExit": 0, "error": ""}`}},
	}}
	if err := e.c.Status().Update(ctx, pod); err != nil {
		e.t.Fatal(err)
	}
	job := &batchv1.Job{}
	if err := e.c.Get(ctx, client.ObjectKey{Namespace: e.ns, Name: run}, job); err != nil {
		e.t.Fatal(err)
	}
	t0 := metav1.Now()
	start := metav1.NewTime(t0.Add(-time.Minute))
	job.Status.StartTime = &start
	job.Status.CompletionTime = &t0
	job.Status.Succeeded = 1
	job.Status.Conditions = []batchv1.JobCondition{
		{Type: batchv1.JobSuccessCriteriaMet, Status: corev1.ConditionTrue, LastTransitionTime: t0},
		{Type: batchv1.JobComplete, Status: corev1.ConditionTrue, LastTransitionTime: t0},
	}
	if err := e.c.Status().Update(ctx, job); err != nil {
		e.t.Fatal(err)
	}
}

func TestAgentWorkflowOverMCP(t *testing.T) {
	e := setup(t)
	ctx := context.Background()

	// The owner uploads the logins and rotates one.
	e.rest(http.MethodPut, "/v1/logins/gh-e2e", map[string]any{"kind": "github-token", "repos": []string{"example-org/*"}, "value": ghValue}, 200)
	e.rest(http.MethodPut, "/v1/logins/model-e2e", map[string]any{"kind": "model-api-key", "value": modelValue}, 200)
	if list := decode[[]server.LoginView](t, e.rest(http.MethodGet, "/v1/logins", nil, 200)); len(list) != 2 {
		t.Fatalf("logins = %+v", list)
	}
	time.Sleep(1100 * time.Millisecond)
	rot := decode[server.LoginView](t, e.rest(http.MethodPut, "/v1/logins/model-e2e", map[string]any{"kind": "model-api-key", "value": rotated}, 200))
	if rot.RotatedAt == nil || rot.RotatedBy != "owner" {
		t.Errorf("rotation = %+v", rot)
	}
	secret := &corev1.Secret{}
	if err := e.c.Get(ctx, client.ObjectKey{Namespace: e.logins, Name: "model-e2e"}, secret); err != nil {
		t.Fatal(err)
	}
	if string(secret.Data["api-key"]) != rotated {
		t.Errorf("rotated value not stored")
	}

	// The agent creates a harness profile and a profile through MCP only.
	agent := e.mcp("agent-token")
	e.call(agent, "create_harness", map[string]any{"name": "nib-e2e", "spec": map[string]any{
		"harness": "nib", "version": "v0.14.2", "model": "default", "endpoint": "https://models.example.com/v1",
		"login": "model-e2e", "config": map[string]any{"tools": []string{"bash", "read"}},
	}})
	spec := v1alpha1.AgentProfileSpec{
		Harness: "nib-e2e",
		Repos:   []v1alpha1.RepoRef{{Slug: "example-org/example-repo", Branch: "main"}},
		GitHub:  "gh-e2e",
		Prompt:  "Look after the repository.",
		Egress:  v1alpha1.ProfileEgress{Mode: v1alpha1.EgressAllowlist, ExtraHosts: []string{"api.github.com:443", "github.com:443"}},
		Budget:  v1alpha1.Budget{SessionSeconds: 1800, Iterations: 50},
		Resources: v1alpha1.ProfileResources{
			CPU: resource.MustParse("1"), Memory: resource.MustParse("2Gi"), Workspace: resource.MustParse("4Gi"), Tmp: resource.MustParse("1Gi"),
		},
		MaxParallel: 2,
		Memory:      v1alpha1.MemoryEnabled,
	}
	e.call(agent, "create_profile", map[string]any{"name": "e2e", "spec": asMap(t, spec)})

	// The owner validates it through REST.
	e.rest(http.MethodPost, "/v1/profiles/e2e/validate", nil, 200)

	// The agent starts a run with a task and follows it.
	run := decode[server.RunView](t, e.call(agent, "start_run", map[string]any{"profile": "e2e", "task": "Fix the flaky test."}))
	e.waitPhase(run.Name, v1alpha1.PhaseStarting)
	cm := &corev1.ConfigMap{}
	if err := e.c.Get(ctx, client.ObjectKey{Namespace: e.ns, Name: run.Name + "-spec"}, cm); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cm.Data["run.json"], `Task for this run:\nFix the flaky test.`) {
		t.Errorf("resolved prompt = %s", cm.Data["run.json"])
	}
	e.playLauncher(run.Name)
	final := decode[server.RunView](t, e.call(agent, "wait_run", map[string]any{"run": run.Name, "timeoutSeconds": 60}))
	if final.Status.Phase != v1alpha1.PhaseSucceeded || final.Status.ExitCode == nil || *final.Status.ExitCode != 0 {
		t.Fatalf("final run = %+v", final.Status)
	}
	logsOut := decode[map[string]string](t, e.call(agent, "get_run_logs", map[string]any{"run": run.Name}))
	if !strings.Contains(logsOut["text"], `"type":"result"`) {
		t.Errorf("logs = %q", logsOut["text"])
	}

	// A second run is cancelled and leaves nothing behind.
	second := decode[server.RunView](t, e.call(agent, "start_run", map[string]any{"profile": "e2e"}))
	e.waitPhase(second.Name, v1alpha1.PhaseStarting)
	e.call(agent, "cancel_run", map[string]any{"name": second.Name})
	done := decode[server.RunView](t, e.call(agent, "wait_run", map[string]any{"run": second.Name, "timeoutSeconds": 60}))
	if done.Status.Phase != v1alpha1.PhaseCancelled {
		t.Fatalf("cancelled run phase = %s", done.Status.Phase)
	}
	eventually(t, 30*time.Second, func() error {
		sel := client.MatchingLabels{controller.LabelRun: second.Name}
		for _, list := range []client.ObjectList{&corev1.ConfigMapList{}, &corev1.PodList{}, &corev1.ServiceList{}, &coordinationv1.LeaseList{}, &batchv1.JobList{}} {
			if err := e.c.List(ctx, list, client.InNamespace(e.ns), sel); err != nil {
				return err
			}
			if n := countItems(list); n > 0 {
				return fmt.Errorf("%T still has %d run objects", list, n)
			}
		}
		lease := &coordinationv1.Lease{}
		if err := e.c.Get(ctx, client.ObjectKey{Namespace: e.ns, Name: controller.MemoryLockName("e2e")}, lease); err == nil {
			return errors.New("memory lock still exists")
		}
		if r := e.run(second.Name); len(r.Finalizers) > 0 {
			return fmt.Errorf("finalizers %v", r.Finalizers)
		}
		return nil
	})
}

func countItems(list client.ObjectList) int {
	switch l := list.(type) {
	case *corev1.ConfigMapList:
		return len(l.Items)
	case *corev1.PodList:
		return len(l.Items)
	case *corev1.ServiceList:
		return len(l.Items)
	case *coordinationv1.LeaseList:
		return len(l.Items)
	case *batchv1.JobList:
		n := 0
		for _, j := range l.Items {
			if j.DeletionTimestamp.IsZero() {
				n++
			}
		}
		return n
	}
	return 0
}

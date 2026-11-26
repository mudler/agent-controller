package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mudler/agent-controller/api/v1alpha1"
	"github.com/mudler/agent-controller/internal/testenv"
)

func TestMain(m *testing.M) {
	code := m.Run()
	testenv.Stop()
	os.Exit(code)
}

// plantedSecret is a login value that must never appear in a response.
const plantedSecret = "planted-secret-value-7f3a9c"

func allScopes() map[Scope]bool {
	out := map[Scope]bool{}
	for _, s := range AllScopes {
		out[s] = true
	}
	return out
}

func testTokens() staticTokens {
	return staticTokens{
		"owner-token":  {Name: "owner", Scopes: allScopes()},
		"agent-token":  {Name: "planner", Scopes: map[Scope]bool{ScopeRunsRead: true, ScopeRunsWrite: true, ScopeProfilesWrite: true}},
		"reader-token": {Name: "reader", Scopes: map[Scope]bool{ScopeRunsRead: true}},
		"none-token":   {Name: "nobody", Scopes: map[Scope]bool{}},
	}
}

// fakeStreamer serves fixed log text and records calls.
type fakeStreamer struct {
	mu    sync.Mutex
	text  string
	calls []string
}

func (f *fakeStreamer) Stream(_ context.Context, w io.Writer, namespace, pod string, follow bool, tail int64) error {
	f.mu.Lock()
	f.calls = append(f.calls, fmt.Sprintf("%s/%s follow=%v tail=%d", namespace, pod, follow, tail))
	text := f.text
	f.mu.Unlock()
	for _, line := range strings.SplitAfter(text, "\n") {
		if _, err := io.WriteString(w, line); err != nil {
			return err
		}
	}
	return nil
}

type testServer struct {
	t       *testing.T
	c       client.Client
	ns      string
	logins  string
	history string
	store   *Store
	logs    *fakeStreamer
	srv     *httptest.Server
	// bodies records every response body, to scan for secrets.
	mu     sync.Mutex
	bodies []string
}

func newNamespace(t *testing.T, c client.Client, prefix string) string {
	t.Helper()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: prefix}}
	if err := c.Create(context.Background(), ns); err != nil {
		t.Fatalf("creating namespace: %v", err)
	}
	return ns.Name
}

func testPolicy(ns string) *v1alpha1.AgentPolicy {
	return &v1alpha1.AgentPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: v1alpha1.PolicyName, Namespace: ns},
		Spec: v1alpha1.AgentPolicySpec{
			Harnesses: []v1alpha1.HarnessKind{v1alpha1.HarnessNib, v1alpha1.HarnessClaudeCode},
			Egress: v1alpha1.PolicyEgress{
				Hosts: []string{"api.github.com:443", "github.com:443", "models.example.com:443", "api.example.com:443"},
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

func testHarnessSpec() v1alpha1.HarnessProfileSpec {
	return v1alpha1.HarnessProfileSpec{
		Harness:  v1alpha1.HarnessNib,
		Version:  "v0.14.2",
		Model:    "default",
		Endpoint: "https://models.example.com/v1",
		Login:    "model-example",
		Config:   &apiextensionsv1.JSON{Raw: []byte(`{"tools":["bash","read"],"contextTokens":32768}`)},
	}
}

func testProfileSpec() v1alpha1.AgentProfileSpec {
	return v1alpha1.AgentProfileSpec{
		Harness: "nib-default",
		Repos:   []v1alpha1.RepoRef{{Slug: "example-org/example-repo", Branch: "main"}},
		GitHub:  "gh-example",
		Prompt:  "Do the work.",
		Egress:  v1alpha1.ProfileEgress{Mode: v1alpha1.EgressAllowlist, ExtraHosts: []string{"api.github.com:443", "github.com:443"}},
		Budget:  v1alpha1.Budget{SessionSeconds: 3000, Iterations: 100},
		Resources: v1alpha1.ProfileResources{
			CPU:       resource.MustParse("2"),
			Memory:    resource.MustParse("4Gi"),
			Workspace: resource.MustParse("8Gi"),
			Tmp:       resource.MustParse("1Gi"),
		},
		Schedule:    &v1alpha1.Schedule{Cron: "30 1 * * *", Suspend: true},
		MaxParallel: 1,
		Memory:      v1alpha1.MemoryEnabled,
	}
}

// newTestServer starts the HTTP server against envtest with a policy,
// two logins (with Secrets holding plantedSecret) and a harness profile.
func newTestServer(t *testing.T) *testServer {
	t.Helper()
	c := testenv.Client(t)
	ctx := context.Background()
	ts := &testServer{t: t, c: c, logs: &fakeStreamer{text: "line one\nline two\n"}}
	ts.ns = newNamespace(t, c, "srv-")
	ts.logins = newNamespace(t, c, "logins-")
	ts.history = newNamespace(t, c, "history-")

	objs := []client.Object{
		testPolicy(ts.ns),
		&v1alpha1.Login{
			ObjectMeta: metav1.ObjectMeta{Name: "gh-example", Namespace: ts.ns},
			Spec:       v1alpha1.LoginSpec{Kind: v1alpha1.LoginGitHubToken, Repos: []string{"example-org/*"}},
		},
		&v1alpha1.Login{
			ObjectMeta: metav1.ObjectMeta{Name: "model-example", Namespace: ts.ns},
			Spec:       v1alpha1.LoginSpec{Kind: v1alpha1.LoginModelAPIKey},
		},
		&v1alpha1.HarnessProfile{ObjectMeta: metav1.ObjectMeta{Name: "nib-default", Namespace: ts.ns}, Spec: testHarnessSpec()},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "gh-example", Namespace: ts.logins},
			Data:       map[string][]byte{"token": []byte(plantedSecret)},
		},
	}
	for _, o := range objs {
		if err := c.Create(ctx, o); err != nil {
			t.Fatalf("creating %T: %v", o, err)
		}
	}

	ts.store = &Store{
		C:                c,
		Namespace:        ts.ns,
		LoginsNamespace:  ts.logins,
		HistoryNamespace: ts.history,
		HarnessVersions:  map[v1alpha1.HarnessKind][]string{v1alpha1.HarnessNib: {"v0.14.2"}, v1alpha1.HarnessClaudeCode: {"2.1.326"}},
		CreateRunWait:    time.Second,
	}
	ts.srv = httptest.NewServer(NewHandler(ts.store, testTokens(), ts.logs))
	t.Cleanup(ts.srv.Close)
	t.Cleanup(ts.checkNoSecret)
	return ts
}

func (ts *testServer) record(body string) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.bodies = append(ts.bodies, body)
}

// checkNoSecret fails the test when any recorded body holds a login value.
func (ts *testServer) checkNoSecret() {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	for _, b := range ts.bodies {
		if strings.Contains(b, plantedSecret) || strings.Contains(b, "rotated-value-") {
			ts.t.Errorf("a response contains a login value: %s", b)
		}
	}
}

// do sends a request and returns the status and body.
func (ts *testServer) do(method, path, token string, body any) (int, string) {
	ts.t.Helper()
	var rd io.Reader
	if body != nil {
		switch b := body.(type) {
		case string:
			rd = strings.NewReader(b)
		default:
			data, err := json.Marshal(b)
			if err != nil {
				ts.t.Fatal(err)
			}
			rd = bytes.NewReader(data)
		}
	}
	req, err := http.NewRequest(method, ts.srv.URL+path, rd)
	if err != nil {
		ts.t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		ts.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		ts.t.Fatal(err)
	}
	ts.record(string(data))
	return resp.StatusCode, string(data)
}

// mustDo is do with an expected status.
func (ts *testServer) mustDo(method, path, token string, body any, want int) string {
	ts.t.Helper()
	status, out := ts.do(method, path, token, body)
	if status != want {
		ts.t.Fatalf("%s %s = %d, want %d: %s", method, path, status, want, out)
	}
	return out
}

func decodeInto[T any](t *testing.T, body string) T {
	t.Helper()
	var v T
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatalf("decoding %s: %v", body, err)
	}
	return v
}

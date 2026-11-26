package server

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mudler/agent-controller/api/v1alpha1"
	"github.com/mudler/agent-controller/internal/policy"
)

// bearerTransport adds a bearer token to every request.
type bearerTransport struct {
	token string
	base  http.RoundTripper
}

func (b *bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return b.base.RoundTrip(r)
}

// mcpSession connects an MCP client to the test server with a token.
func (ts *testServer) mcpSession(token string) *mcp.ClientSession {
	ts.t.Helper()
	c := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "v0"}, nil)
	transport := &mcp.StreamableClientTransport{
		Endpoint:             ts.srv.URL + "/mcp",
		HTTPClient:           &http.Client{Transport: &bearerTransport{token: token, base: http.DefaultTransport}},
		DisableStandaloneSSE: true,
		MaxRetries:           -1,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sess, err := c.Connect(ctx, transport, nil)
	if err != nil {
		ts.t.Fatalf("connecting MCP client: %v", err)
	}
	ts.t.Cleanup(func() { _ = sess.Close() })
	return sess
}

// call invokes a tool and returns its text and error flag.
func (ts *testServer) call(sess *mcp.ClientSession, name string, args any) (string, bool) {
	ts.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		ts.t.Fatalf("calling %s: %v", name, err)
	}
	raw, err := json.Marshal(res)
	if err != nil {
		ts.t.Fatal(err)
	}
	ts.record(string(raw))
	var text strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			text.WriteString(tc.Text)
		}
	}
	return text.String(), res.IsError
}

// mustCall calls a tool that must succeed.
func (ts *testServer) mustCall(sess *mcp.ClientSession, name string, args any) string {
	ts.t.Helper()
	out, isErr := ts.call(sess, name, args)
	if isErr {
		ts.t.Fatalf("%s failed: %s", name, out)
	}
	return out
}

func specMap(t *testing.T, spec any) map[string]any {
	t.Helper()
	b, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]any{}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestMCPToolList(t *testing.T) {
	ts := newTestServer(t)
	sess := ts.mcpSession("reader-token")
	res, err := sess.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	want := []string{
		"cancel_run", "create_harness", "create_profile", "delete_harness", "delete_profile", "get_harness",
		"get_policy", "get_profile", "get_run", "get_run_logs", "list_harnesses", "list_logins", "list_profiles",
		"list_runs", "profile_history", "start_run", "suspend_profile", "update_harness", "update_profile", "wait_run",
	}
	if !slices.Equal(names, want) {
		t.Errorf("tools = %v, want %v", names, want)
	}
}

func TestMCPUnauthenticated(t *testing.T) {
	ts := newTestServer(t)
	for _, token := range []string{"", "bad-token"} {
		req, err := http.NewRequest(http.MethodPost, ts.srv.URL+"/mcp", strings.NewReader(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("token %q: status %d, want 401", token, resp.StatusCode)
		}
	}
}

func TestMCPToolsHappyPathAndScopes(t *testing.T) {
	ts := newTestServer(t)
	agent := ts.mcpSession("agent-token")
	reader := ts.mcpSession("reader-token")
	none := ts.mcpSession("none-token")

	// Harness and profile lifecycle through MCP only.
	h := testHarnessSpec()
	out := ts.mustCall(agent, "create_harness", map[string]any{"name": "nib-two", "spec": specMap(t, h)})
	if !strings.Contains(out, `"name":"nib-two"`) {
		t.Errorf("create_harness = %s", out)
	}
	h.Model = "other"
	ts.mustCall(agent, "update_harness", map[string]any{"name": "nib-two", "spec": specMap(t, h)})
	ts.mustCall(reader, "get_harness", map[string]any{"name": "nib-two"})
	if out := ts.mustCall(reader, "list_harnesses", map[string]any{}); !strings.Contains(out, "nib-two") {
		t.Errorf("list_harnesses = %s", out)
	}

	spec := testProfileSpec()
	ts.mustCall(agent, "create_profile", map[string]any{"name": "mcp-prof", "spec": specMap(t, spec)})
	spec.Prompt = "Changed."
	ts.mustCall(agent, "update_profile", map[string]any{"name": "mcp-prof", "spec": specMap(t, spec)})
	out = ts.mustCall(agent, "suspend_profile", map[string]any{"name": "mcp-prof", "suspend": false})
	if !strings.Contains(out, `"suspend":false`) {
		t.Errorf("suspend_profile = %s", out)
	}
	ts.mustCall(reader, "get_profile", map[string]any{"name": "mcp-prof"})
	hist := ts.mustCall(reader, "profile_history", map[string]any{"name": "mcp-prof"})
	if entries := decodeInto[[]HistoryEntry](t, hist); len(entries) != 2 {
		t.Errorf("history entries = %d, want 2", len(entries))
	}
	list := decodeInto[[]profileSummary](t, ts.mustCall(reader, "list_profiles", map[string]any{}))
	if len(list) != 1 || list[0].Name != "mcp-prof" || list[0].Validated || list[0].Memory != v1alpha1.MemoryEnabled {
		t.Errorf("list_profiles = %+v", list)
	}
	if out := ts.mustCall(reader, "get_policy", map[string]any{}); !strings.Contains(out, "modelEndpoints") {
		t.Errorf("get_policy = %s", out)
	}

	// Runs.
	run := decodeInto[RunView](t, ts.mustCall(agent, "start_run", map[string]any{"profile": "mcp-prof", "task": "Do X.", "memory": false}))
	if run.Spec.RequestedBy != "planner" || run.Spec.Task != "Do X." {
		t.Errorf("start_run = %+v", run.Spec)
	}
	runs := decodeInto[[]RunView](t, ts.mustCall(reader, "list_runs", map[string]any{"profile": "mcp-prof", "limit": 5}))
	if len(runs) != 1 {
		t.Errorf("list_runs = %d", len(runs))
	}
	ts.mustCall(reader, "get_run", map[string]any{"name": run.Name})
	out, isErr := ts.call(reader, "get_run_logs", map[string]any{"run": run.Name})
	if !isErr || !strings.Contains(out, "no launcher pod") {
		t.Errorf("logs without pod = %s", out)
	}
	cancelled := decodeInto[RunView](t, ts.mustCall(agent, "cancel_run", map[string]any{"name": run.Name}))
	if !cancelled.Spec.Cancel {
		t.Error("cancel_run did not set cancel")
	}

	// Scope denials, with the REST error body.
	denied := []struct {
		sess *mcp.ClientSession
		tool string
		args map[string]any
		want Scope
	}{
		{none, "get_policy", map[string]any{}, ScopeRunsRead},
		{none, "list_profiles", map[string]any{}, ScopeRunsRead},
		{none, "get_profile", map[string]any{"name": "mcp-prof"}, ScopeRunsRead},
		{reader, "create_profile", map[string]any{"name": "x", "spec": specMap(t, spec)}, ScopeProfilesWrite},
		{reader, "update_profile", map[string]any{"name": "mcp-prof", "spec": specMap(t, spec)}, ScopeProfilesWrite},
		{reader, "suspend_profile", map[string]any{"name": "mcp-prof", "suspend": true}, ScopeProfilesWrite},
		{reader, "delete_profile", map[string]any{"name": "mcp-prof"}, ScopeProfilesWrite},
		{none, "profile_history", map[string]any{"name": "mcp-prof"}, ScopeRunsRead},
		{none, "list_harnesses", map[string]any{}, ScopeRunsRead},
		{none, "get_harness", map[string]any{"name": "nib-two"}, ScopeRunsRead},
		{reader, "create_harness", map[string]any{"name": "x", "spec": specMap(t, h)}, ScopeProfilesWrite},
		{reader, "update_harness", map[string]any{"name": "nib-two", "spec": specMap(t, h)}, ScopeProfilesWrite},
		{reader, "delete_harness", map[string]any{"name": "nib-two"}, ScopeProfilesWrite},
		{none, "list_logins", map[string]any{}, ScopeRunsRead},
		{reader, "start_run", map[string]any{"profile": "mcp-prof"}, ScopeRunsWrite},
		{none, "list_runs", map[string]any{}, ScopeRunsRead},
		{none, "get_run", map[string]any{"name": run.Name}, ScopeRunsRead},
		{reader, "cancel_run", map[string]any{"name": run.Name}, ScopeRunsWrite},
		{none, "get_run_logs", map[string]any{"run": run.Name}, ScopeRunsRead},
		{none, "wait_run", map[string]any{"run": run.Name, "timeoutSeconds": 1}, ScopeRunsRead},
	}
	for _, d := range denied {
		out, isErr := ts.call(d.sess, d.tool, d.args)
		want := `{"error":{"field":"scope","reason":"missing ` + string(d.want) + `"}}`
		if !isErr || out != want {
			t.Errorf("%s denied: %v %s, want %s", d.tool, isErr, out, want)
		}
	}

	// Policy violation keeps the REST error shape.
	bad := testProfileSpec()
	bad.Egress.ExtraHosts = []string{"evil.example.net:443"}
	out, isErr = ts.call(agent, "create_profile", map[string]any{"name": "bad", "spec": specMap(t, bad)})
	var body struct {
		Error policy.FieldError `json:"error"`
	}
	if !isErr || json.Unmarshal([]byte(out), &body) != nil || body.Error.Field != "egress.extraHosts[0]" || len(body.Error.Allowed) == 0 {
		t.Errorf("policy violation = %s", out)
	}
	out, isErr = ts.call(agent, "create_profile", map[string]any{"name": "bad", "spec": map[string]any{"unknown": 1}})
	if !isErr || !strings.Contains(out, `"field":"spec"`) {
		t.Errorf("unknown spec field = %s", out)
	}

	ts.mustCall(agent, "delete_profile", map[string]any{"name": "mcp-prof"})
	ts.mustCall(agent, "delete_harness", map[string]any{"name": "nib-two"})
}

func TestMCPListLoginNamesAndKinds(t *testing.T) {
	ts := newTestServer(t)
	sess := ts.mcpSession("reader-token")
	out := ts.mustCall(sess, "list_logins", map[string]any{})
	var items []map[string]any
	if err := json.Unmarshal([]byte(out), &items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("logins = %s", out)
	}
	for _, item := range items {
		if len(item) != 2 || item["name"] == nil || item["kind"] == nil {
			t.Errorf("login item = %v, want only name and kind", item)
		}
	}
}

func TestMCPWaitRunAndLogs(t *testing.T) {
	ts := newTestServer(t)
	agent := ts.mcpSession("agent-token")
	ts.mustCall(agent, "create_profile", map[string]any{"name": "w", "spec": specMap(t, testProfileSpec())})
	run := decodeInto[RunView](t, ts.mustCall(agent, "start_run", map[string]any{"profile": "w"}))

	// Timeout: the run stays without a phase.
	start := time.Now()
	got := decodeInto[RunView](t, ts.mustCall(agent, "wait_run", map[string]any{"run": run.Name, "timeoutSeconds": 1}))
	if got.Status.Phase.Final() || time.Since(start) < time.Second {
		t.Errorf("wait_run timeout returned %s after %s", got.Status.Phase, time.Since(start))
	}
	if out, isErr := ts.call(agent, "wait_run", map[string]any{"run": run.Name, "timeoutSeconds": 601}); !isErr || !strings.Contains(out, "timeoutSeconds") {
		t.Errorf("bad timeout = %s", out)
	}

	// Final phase: the run finishes while wait_run waits.
	go func() {
		time.Sleep(1500 * time.Millisecond)
		r := &v1alpha1.AgentRun{}
		if err := ts.c.Get(context.Background(), client.ObjectKey{Namespace: ts.ns, Name: run.Name}, r); err != nil {
			return
		}
		r.Status.Phase = v1alpha1.PhaseSucceeded
		r.Status.PodName = "launcher-pod"
		_ = ts.c.Status().Update(context.Background(), r)
	}()
	start = time.Now()
	got = decodeInto[RunView](t, ts.mustCall(agent, "wait_run", map[string]any{"run": run.Name, "timeoutSeconds": 30}))
	if got.Status.Phase != v1alpha1.PhaseSucceeded || time.Since(start) > 10*time.Second {
		t.Errorf("wait_run = %s after %s", got.Status.Phase, time.Since(start))
	}

	logs := decodeInto[map[string]string](t, ts.mustCall(agent, "get_run_logs", map[string]any{"run": run.Name, "tail": 9999}))
	if logs["text"] != "line one\nline two\n" {
		t.Errorf("logs = %q", logs["text"])
	}
	if got := ts.logs.calls[len(ts.logs.calls)-1]; !strings.HasSuffix(got, "follow=false tail=5000") {
		t.Errorf("stream call = %s", got)
	}
}

func TestMCPNeverReturnsSecretValues(t *testing.T) {
	ts := newTestServer(t)
	// The owner uploads values through REST; then every read tool runs.
	ts.mustDo(http.MethodPut, "/v1/logins/model-example", "owner-token", map[string]any{"kind": "model-api-key", "value": "rotated-value-mcp"}, 200)
	owner := ts.mcpSession("owner-token")
	for _, tool := range []string{"get_policy", "list_profiles", "list_harnesses", "list_logins", "list_runs"} {
		ts.mustCall(owner, tool, map[string]any{})
	}
	ts.mustCall(owner, "get_harness", map[string]any{"name": "nib-default"})
	// checkNoSecret runs at cleanup over every recorded response.
}

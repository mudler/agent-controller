package server

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mudler/agent-controller/api/v1alpha1"
	"github.com/mudler/agent-controller/internal/controller"
	"github.com/mudler/agent-controller/internal/policy"
)

func (ts *testServer) createProfile(name string, spec v1alpha1.AgentProfileSpec) ProfileView {
	ts.t.Helper()
	body := ts.mustDo(http.MethodPost, "/v1/profiles", "agent-token", map[string]any{"name": name, "spec": spec}, http.StatusCreated)
	return decodeInto[ProfileView](ts.t, body)
}

func TestRESTScopes(t *testing.T) {
	ts := newTestServer(t)
	ts.createProfile("example", testProfileSpec())
	ts.mustDo(http.MethodPost, "/v1/runs", "agent-token", map[string]any{"profile": "example"}, http.StatusCreated)
	run := ts.mustDo(http.MethodGet, "/v1/runs", "reader-token", nil, http.StatusOK)
	runName := decodeInto[[]RunView](t, run)[0].Name

	spec := testProfileSpec()
	h := testHarnessSpec()
	routes := []struct {
		method, path string
		body         any
		allowed      string
		denied       string
		scope        Scope
		wantStatus   int
	}{
		{http.MethodGet, "/v1/policy", nil, "reader-token", "none-token", ScopeRunsRead, 200},
		{http.MethodGet, "/v1/profiles", nil, "reader-token", "none-token", ScopeRunsRead, 200},
		{http.MethodGet, "/v1/profiles/example", nil, "reader-token", "none-token", ScopeRunsRead, 200},
		{http.MethodGet, "/v1/profiles/example/history", nil, "reader-token", "none-token", ScopeRunsRead, 200},
		{http.MethodPost, "/v1/profiles", map[string]any{"name": "second", "spec": spec}, "agent-token", "reader-token", ScopeProfilesWrite, 201},
		{http.MethodPut, "/v1/profiles/second", map[string]any{"spec": spec}, "agent-token", "reader-token", ScopeProfilesWrite, 200},
		{http.MethodPost, "/v1/profiles/second/validate", nil, "owner-token", "agent-token", ScopeProfilesValidate, 200},
		{http.MethodDelete, "/v1/profiles/second", nil, "agent-token", "reader-token", ScopeProfilesWrite, 200},
		{http.MethodGet, "/v1/harnesses", nil, "reader-token", "none-token", ScopeRunsRead, 200},
		{http.MethodGet, "/v1/harnesses/nib-default", nil, "reader-token", "none-token", ScopeRunsRead, 200},
		{http.MethodPost, "/v1/harnesses", map[string]any{"name": "nib-two", "spec": h}, "agent-token", "reader-token", ScopeProfilesWrite, 201},
		{http.MethodPut, "/v1/harnesses/nib-two", map[string]any{"spec": h}, "agent-token", "reader-token", ScopeProfilesWrite, 200},
		{http.MethodDelete, "/v1/harnesses/nib-two", nil, "agent-token", "reader-token", ScopeProfilesWrite, 200},
		{http.MethodGet, "/v1/logins", nil, "reader-token", "none-token", ScopeRunsRead, 200},
		{http.MethodPut, "/v1/logins/new-key", map[string]any{"kind": "model-api-key", "value": "rotated-value-1"}, "owner-token", "agent-token", ScopeLoginsWrite, 200},
		{http.MethodDelete, "/v1/logins/new-key", nil, "owner-token", "agent-token", ScopeLoginsWrite, 200},
		{http.MethodGet, "/v1/runs", nil, "reader-token", "none-token", ScopeRunsRead, 200},
		{http.MethodPost, "/v1/runs", map[string]any{"profile": "example"}, "agent-token", "reader-token", ScopeRunsWrite, 201},
		{http.MethodGet, "/v1/runs/" + runName, nil, "reader-token", "none-token", ScopeRunsRead, 200},
		{http.MethodGet, "/v1/runs/" + runName + "/logs", nil, "reader-token", "none-token", ScopeRunsRead, 409},
		{http.MethodPost, "/v1/runs/" + runName + "/cancel", nil, "agent-token", "reader-token", ScopeRunsWrite, 200},
	}
	for _, rt := range routes {
		t.Run(rt.method+" "+rt.path, func(t *testing.T) {
			status, body := ts.do(rt.method, rt.path, rt.denied, rt.body)
			want := fmt.Sprintf(`{"error":{"field":"scope","reason":"missing %s"}}`, rt.scope)
			if status != http.StatusForbidden || body != want {
				t.Errorf("denied: %d %s, want 403 %s", status, body, want)
			}
			if status, _ := ts.do(rt.method, rt.path, "", rt.body); status != http.StatusUnauthorized {
				t.Errorf("no token: %d, want 401", status)
			}
			ts.mustDo(rt.method, rt.path, rt.allowed, rt.body, rt.wantStatus)
		})
	}
}

func TestRESTPolicyViolationShape(t *testing.T) {
	ts := newTestServer(t)
	spec := testProfileSpec()
	spec.Egress.ExtraHosts = []string{"evil.example.net:443"}
	status, body := ts.do(http.MethodPost, "/v1/profiles", "agent-token", map[string]any{"name": "bad", "spec": spec})
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d: %s", status, body)
	}
	out := decodeInto[struct {
		Error policy.FieldError `json:"error"`
	}](t, body)
	if out.Error.Field != "egress.extraHosts[0]" || out.Error.Reason != "not in policy" || len(out.Error.Allowed) == 0 {
		t.Errorf("error = %+v", out.Error)
	}

	status, body = ts.do(http.MethodPost, "/v1/profiles", "agent-token", `{"name":"x","spec":{"nope":1}}`)
	if status != http.StatusUnprocessableEntity || !strings.Contains(body, `"field":"body"`) {
		t.Errorf("unknown field: %d %s", status, body)
	}
	status, body = ts.do(http.MethodGet, "/v1/profiles/missing", "reader-token", nil)
	if status != http.StatusNotFound || !strings.Contains(body, `"reason":"not found"`) {
		t.Errorf("missing profile: %d %s", status, body)
	}
	status, _ = ts.do(http.MethodPost, "/v1/profiles", "agent-token", map[string]any{"name": "Bad_Name", "spec": testProfileSpec()})
	if status != http.StatusUnprocessableEntity {
		t.Errorf("bad name: %d", status)
	}
}

func TestRESTValidatedRules(t *testing.T) {
	ts := newTestServer(t)
	spec := testProfileSpec()
	spec.Validated = true
	status, body := ts.do(http.MethodPost, "/v1/profiles", "agent-token", map[string]any{"name": "p", "spec": spec})
	if status != http.StatusForbidden || !strings.Contains(body, `"field":"validated"`) {
		t.Fatalf("create with validated: %d %s", status, body)
	}

	created := ts.createProfile("p", testProfileSpec())
	if created.Spec.Validated {
		t.Fatal("new profile is validated")
	}
	if status, body := ts.do(http.MethodPut, "/v1/profiles/p", "agent-token", map[string]any{"spec": spec}); status != http.StatusForbidden {
		t.Fatalf("agent sets validated: %d %s", status, body)
	}
	v := decodeInto[ProfileView](t, ts.mustDo(http.MethodPost, "/v1/profiles/p/validate", "owner-token", nil, 200))
	if !v.Spec.Validated {
		t.Fatal("validate did not set validated")
	}

	// A prompt change keeps validated, even when the body omits it.
	keep := testProfileSpec()
	keep.Prompt = "Different prompt."
	v = decodeInto[ProfileView](t, ts.mustDo(http.MethodPut, "/v1/profiles/p", "agent-token", map[string]any{"spec": keep}, 200))
	if !v.Spec.Validated {
		t.Error("prompt change reset validated")
	}

	// A repository change resets it.
	moved := keep
	moved.Repos = []v1alpha1.RepoRef{{Slug: "example-org/other-repo", Branch: "main"}}
	v = decodeInto[ProfileView](t, ts.mustDo(http.MethodPut, "/v1/profiles/p", "agent-token", map[string]any{"spec": moved}, 200))
	if v.Spec.Validated {
		t.Error("repository change kept validated")
	}

	// A harness endpoint change resets validated on the profiles using it.
	ts.mustDo(http.MethodPost, "/v1/profiles/p/validate", "owner-token", nil, 200)
	h := testHarnessSpec()
	h.Endpoint = "https://api.example.com"
	ts.mustDo(http.MethodPut, "/v1/harnesses/nib-default", "agent-token", map[string]any{"spec": h}, 200)
	v = decodeInto[ProfileView](t, ts.mustDo(http.MethodGet, "/v1/profiles/p", "reader-token", nil, 200))
	if v.Spec.Validated {
		t.Error("harness endpoint change kept validated")
	}

	p := &v1alpha1.AgentProfile{}
	if err := ts.c.Get(context.Background(), client.ObjectKey{Namespace: ts.ns, Name: "p"}, p); err != nil {
		t.Fatal(err)
	}
	if p.Annotations[controller.AnnotationLastChangedBy] != "planner" {
		t.Errorf("last-changed-by = %q", p.Annotations[controller.AnnotationLastChangedBy])
	}
}

func TestRESTHistoryKeepsTwenty(t *testing.T) {
	ts := newTestServer(t)
	ts.createProfile("h", testProfileSpec())
	for i := range 25 {
		spec := testProfileSpec()
		spec.Prompt = fmt.Sprintf("Prompt %d.", i)
		ts.mustDo(http.MethodPut, "/v1/profiles/h", "agent-token", map[string]any{"spec": spec}, 200)
	}
	hist := decodeInto[[]HistoryEntry](t, ts.mustDo(http.MethodGet, "/v1/profiles/h/history", "reader-token", nil, 200))
	if len(hist) != HistoryLimit {
		t.Fatalf("history = %d entries, want %d", len(hist), HistoryLimit)
	}
	if hist[0].Spec.Prompt != "Prompt 23." || hist[0].ChangedBy != "planner" || hist[0].Version <= hist[1].Version {
		t.Errorf("newest entry = %+v", hist[0])
	}

	// History lives in the history namespace, not the runs namespace.
	cms := &corev1.ConfigMapList{}
	if err := ts.c.List(context.Background(), cms, client.InNamespace(ts.ns)); err != nil {
		t.Fatal(err)
	}
	for _, cm := range cms.Items {
		if strings.HasSuffix(cm.Name, "-history") {
			t.Errorf("history ConfigMap %s in the runs namespace", cm.Name)
		}
	}
	ts.mustDo(http.MethodDelete, "/v1/profiles/h", "agent-token", nil, 200)
	err := ts.c.Get(context.Background(), client.ObjectKey{Namespace: ts.history, Name: "h-history"}, &corev1.ConfigMap{})
	if !apierrors.IsNotFound(err) {
		t.Errorf("history after delete: %v", err)
	}
}

func TestRESTBusyProfileQueues(t *testing.T) {
	ts := newTestServer(t)
	ts.createProfile("busy", testProfileSpec())
	ts.store.CreateRunWait = 5 * time.Second

	// Play the controller: mark the new run Queued.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(50 * time.Millisecond):
			}
			list := &v1alpha1.AgentRunList{}
			if err := ts.c.List(context.Background(), list, client.InNamespace(ts.ns)); err != nil {
				continue
			}
			for i := range list.Items {
				r := &list.Items[i]
				if r.Status.Phase == "" {
					r.Status.Phase = v1alpha1.PhaseQueued
					r.Status.QueuePosition = 2
					_ = ts.c.Status().Update(context.Background(), r)
				}
			}
		}
	}()

	body := ts.mustDo(http.MethodPost, "/v1/runs", "agent-token",
		map[string]any{"profile": "busy", "task": "Fix it.", "memory": false, "requestedBy": "someone-else"}, http.StatusCreated)
	run := decodeInto[RunView](t, body)
	if run.Status.Phase != v1alpha1.PhaseQueued || run.Status.QueuePosition != 2 {
		t.Errorf("status = %+v, want Queued at position 2", run.Status)
	}
	if run.Spec.RequestedBy != "planner" || run.Spec.Task != "Fix it." || run.Spec.Trigger != v1alpha1.TriggerManual {
		t.Errorf("spec = %+v", run.Spec)
	}
	if !strings.HasPrefix(run.Name, "busy-") {
		t.Errorf("name = %s", run.Name)
	}

	status, body := ts.do(http.MethodPost, "/v1/runs", "agent-token", map[string]any{"profile": "busy", "task": strings.Repeat("x", 4001)})
	if status != http.StatusUnprocessableEntity || !strings.Contains(body, `"field":"task"`) {
		t.Errorf("long task: %d %s", status, body)
	}
}

func TestRESTRunsListAndLogs(t *testing.T) {
	ts := newTestServer(t)
	ts.createProfile("a", testProfileSpec())
	ts.createProfile("b", testProfileSpec())
	for _, p := range []string{"a", "a", "b"} {
		ts.mustDo(http.MethodPost, "/v1/runs", "agent-token", map[string]any{"profile": p}, http.StatusCreated)
	}
	all := decodeInto[[]RunView](t, ts.mustDo(http.MethodGet, "/v1/runs", "reader-token", nil, 200))
	if len(all) != 3 {
		t.Fatalf("runs = %d", len(all))
	}
	onlyA := decodeInto[[]RunView](t, ts.mustDo(http.MethodGet, "/v1/runs?profile=a&limit=1", "reader-token", nil, 200))
	if len(onlyA) != 1 || onlyA[0].Spec.Profile != "a" {
		t.Errorf("filtered = %+v", onlyA)
	}
	if status, _ := ts.do(http.MethodGet, "/v1/runs?limit=x", "reader-token", nil); status != 422 {
		t.Errorf("bad limit: %d", status)
	}

	run := &v1alpha1.AgentRun{}
	if err := ts.c.Get(context.Background(), client.ObjectKey{Namespace: ts.ns, Name: all[0].Name}, run); err != nil {
		t.Fatal(err)
	}
	run.Status.Phase = v1alpha1.PhaseRunning
	run.Status.PodName = "launcher-pod"
	if err := ts.c.Status().Update(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	logs := ts.mustDo(http.MethodGet, "/v1/runs/"+run.Name+"/logs?follow=true&tail=9000", "reader-token", nil, 200)
	if logs != "line one\nline two\n" {
		t.Errorf("logs = %q", logs)
	}
	if got := ts.logs.calls[len(ts.logs.calls)-1]; got != ts.ns+"/launcher-pod follow=true tail=5000" {
		t.Errorf("stream call = %s", got)
	}
	ts.mustDo(http.MethodGet, "/v1/runs/"+run.Name+"/logs", "reader-token", nil, 200)
	if got := ts.logs.calls[len(ts.logs.calls)-1]; got != ts.ns+"/launcher-pod follow=false tail=200" {
		t.Errorf("default stream call = %s", got)
	}

	cancelled := decodeInto[RunView](t, ts.mustDo(http.MethodPost, "/v1/runs/"+run.Name+"/cancel", "agent-token", nil, 200))
	if !cancelled.Spec.Cancel {
		t.Error("cancel not set")
	}
}

func TestRESTLoginRoutes(t *testing.T) {
	ts := newTestServer(t)
	ctx := context.Background()

	status, body := ts.do(http.MethodPut, "/v1/logins/new-gh", "owner-token", map[string]any{"kind": "github-token", "repos": []string{"example-org/*"}})
	if status != 422 || !strings.Contains(body, `"field":"value"`) {
		t.Fatalf("create without value: %d %s", status, body)
	}
	body = ts.mustDo(http.MethodPut, "/v1/logins/new-gh", "owner-token",
		map[string]any{"kind": "github-token", "repos": []string{"example-org/*"}, "egressAll": true, "description": "bot", "value": "rotated-value-a"}, 200)
	view := decodeInto[LoginView](t, body)
	if view.RotatedAt == nil || view.RotatedBy != "owner" || view.Kind != v1alpha1.LoginGitHubToken {
		t.Errorf("view = %+v", view)
	}

	secret := &corev1.Secret{}
	if err := ts.c.Get(ctx, client.ObjectKey{Namespace: ts.logins, Name: "new-gh"}, secret); err != nil {
		t.Fatal(err)
	}
	if string(secret.Data["token"]) != "rotated-value-a" || len(secret.Data) != 1 {
		t.Errorf("secret data = %v", secret.Data)
	}

	// Metadata-only update keeps the value and rotatedAt.
	ts.mustDo(http.MethodPut, "/v1/logins/new-gh", "owner-token", map[string]any{"kind": "github-token", "repos": []string{"example-org/one"}}, 200)
	if err := ts.c.Get(ctx, client.ObjectKey{Namespace: ts.logins, Name: "new-gh"}, secret); err != nil {
		t.Fatal(err)
	}
	if string(secret.Data["token"]) != "rotated-value-a" {
		t.Error("metadata update changed the value")
	}

	// Rotation with a file value, base64-encoded, for a claude credentials login.
	file := base64.StdEncoding.EncodeToString([]byte(`{"token":"rotated-value-file"}`))
	ts.mustDo(http.MethodPut, "/v1/logins/claude", "owner-token", map[string]any{"kind": "claude-credentials", "valueBase64": file}, 200)
	if err := ts.c.Get(ctx, client.ObjectKey{Namespace: ts.logins, Name: "claude"}, secret); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(secret.Data["credentials.json"]), "rotated-value-file") {
		t.Errorf("credentials key = %v", secret.Data)
	}
	if status, _ := ts.do(http.MethodPut, "/v1/logins/claude", "owner-token", map[string]any{"kind": "claude-credentials", "value": "a", "valueBase64": file}); status != 422 {
		t.Errorf("both values: %d", status)
	}
	if status, _ := ts.do(http.MethodPut, "/v1/logins/k", "owner-token", map[string]any{"kind": "model-api-key", "repos": []string{"a/b"}, "value": "x"}); status != 422 {
		t.Errorf("repos on model key: %d", status)
	}

	list := ts.mustDo(http.MethodGet, "/v1/logins", "reader-token", nil, 200)
	items := decodeInto[[]map[string]any](t, list)
	for _, item := range items {
		for k := range item {
			switch k {
			case "name", "kind", "repos", "egressAll", "description", "rotatedAt", "rotatedBy":
			default:
				t.Errorf("login list has key %q", k)
			}
		}
	}

	ts.mustDo(http.MethodDelete, "/v1/logins/new-gh", "owner-token", nil, 200)
	if err := ts.c.Get(ctx, client.ObjectKey{Namespace: ts.logins, Name: "new-gh"}, &corev1.Secret{}); !apierrors.IsNotFound(err) {
		t.Errorf("secret after delete: %v", err)
	}
	if err := ts.c.Get(ctx, client.ObjectKey{Namespace: ts.ns, Name: "new-gh"}, &v1alpha1.Login{}); !apierrors.IsNotFound(err) {
		t.Errorf("login after delete: %v", err)
	}
}

func TestRESTHealthzAndUnknownRoute(t *testing.T) {
	ts := newTestServer(t)
	ts.mustDo(http.MethodGet, "/healthz", "", nil, 200)
	ts.mustDo(http.MethodGet, "/v1/nope", "reader-token", nil, 404)
}

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mudler/agent-controller/api/v1alpha1"
)

const (
	// mcpTokenLifetime is the expiration reported to the SDK middleware.
	// Tokens have no expiry of their own; revocation is a tokens.json edit.
	mcpTokenLifetime = time.Hour
	maxWaitSeconds   = 600
	waitPoll         = time.Second
)

// NewMCPHandler returns the Streamable HTTP MCP handler. It is stateless and
// authenticates each request with the same bearer tokens as REST. There
// are no tools for profiles:validate or logins:write.
func NewMCPHandler(s *Store, tokens TokenStore, logs LogStreamer) http.Handler {
	server := mcp.NewServer(&mcp.Implementation{Name: "agent-controller", Version: "v1alpha1"}, nil)
	t := &mcpTools{s: s, logs: logs}
	t.register(server)

	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true})
	verify := func(ctx context.Context, token string, _ *http.Request) (*auth.TokenInfo, error) {
		id, err := tokens.Lookup(ctx, token)
		if err != nil {
			return nil, errors.Join(auth.ErrInvalidToken, err)
		}
		scopes := make([]string, 0, len(id.Scopes))
		for sc, ok := range id.Scopes {
			if ok {
				scopes = append(scopes, string(sc))
			}
		}
		return &auth.TokenInfo{Scopes: scopes, UserID: id.Name, Expiration: time.Now().Add(mcpTokenLifetime)}, nil
	}
	return auth.RequireBearerToken(verify, nil)(h)
}

type mcpTools struct {
	s    *Store
	logs LogStreamer
}

// identityOf rebuilds the caller identity from the request token info.
func identityOf(req *mcp.CallToolRequest) *Identity {
	if req == nil || req.Extra == nil || req.Extra.TokenInfo == nil {
		return nil
	}
	ti := req.Extra.TokenInfo
	id := &Identity{Name: ti.UserID, Scopes: map[Scope]bool{}}
	for _, sc := range ti.Scopes {
		id.Scopes[Scope(sc)] = true
	}
	return id
}

// result turns a value or an error into a tool result. Errors use the same
// JSON body as REST.
func result(v any, err error) (*mcp.CallToolResult, any, error) {
	if err != nil {
		_, body := ErrorResponse(err)
		return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: string(body)}}}, nil, nil
	}
	b, mErr := json.Marshal(v)
	if mErr != nil {
		return result(nil, mErr)
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}, nil, nil
}

// decodeSpec converts a JSON object argument into a CRD spec, rejecting
// unknown fields.
func decodeSpec(raw map[string]any, out any) error {
	b, err := json.Marshal(raw)
	if err != nil {
		return newAPIError(http.StatusUnprocessableEntity, "spec", "invalid object")
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return newAPIError(http.StatusUnprocessableEntity, "spec", "invalid spec: "+err.Error())
	}
	return nil
}

// Tool inputs.
type (
	emptyInput struct{}
	nameInput  struct {
		Name string `json:"name" jsonschema:"object name"`
	}
	specInput struct {
		Name string         `json:"name" jsonschema:"object name"`
		Spec map[string]any `json:"spec" jsonschema:"the full spec object, in the same shape as get_profile or get_harness return it"`
	}
	suspendInput struct {
		Name    string `json:"name" jsonschema:"profile name"`
		Suspend bool   `json:"suspend" jsonschema:"true suspends the schedule, false resumes it"`
	}
	startRunInput struct {
		Profile string `json:"profile" jsonschema:"profile to run"`
		Task    string `json:"task,omitempty" jsonschema:"optional task appended to the profile prompt, at most 4000 characters"`
		Memory  *bool  `json:"memory,omitempty" jsonschema:"false runs without the profile memory"`
	}
	listRunsInput struct {
		Profile string `json:"profile,omitempty" jsonschema:"only runs of this profile"`
		Phase   string `json:"phase,omitempty" jsonschema:"only runs in this phase"`
		Limit   int    `json:"limit,omitempty" jsonschema:"maximum runs, default 50, at most 500"`
	}
	runLogsInput struct {
		Run  string `json:"run" jsonschema:"run name"`
		Tail int64  `json:"tail,omitempty" jsonschema:"last lines, default 200, at most 5000"`
	}
	waitRunInput struct {
		Run            string `json:"run" jsonschema:"run name"`
		TimeoutSeconds int    `json:"timeoutSeconds" jsonschema:"how long to wait, 1 to 600 seconds"`
	}
)

// profileSummary is one entry of list_profiles.
type profileSummary struct {
	Name      string              `json:"name"`
	Valid     bool                `json:"valid"`
	Validated bool                `json:"validated"`
	Version   int64               `json:"version"`
	Schedule  *v1alpha1.Schedule  `json:"schedule"`
	Memory    v1alpha1.MemoryMode `json:"memory"`
}

// loginSummary is one entry of list_logins: name and kind only.
type loginSummary struct {
	Name string             `json:"name"`
	Kind v1alpha1.LoginKind `json:"kind"`
}

func tool(name, desc string) *mcp.Tool {
	return &mcp.Tool{Name: name, Description: desc}
}

func (t *mcpTools) register(srv *mcp.Server) {
	s := t.s
	mcp.AddTool(srv, tool("get_policy", "Return the AgentPolicy limits that every profile must fit."),
		func(ctx context.Context, req *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, any, error) {
			return result(s.GetPolicy(ctx, identityOf(req)))
		})
	mcp.AddTool(srv, tool("list_profiles", "List agent profiles with validity, validation, version, schedule and memory."),
		func(ctx context.Context, req *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, any, error) {
			profiles, err := s.ListProfiles(ctx, identityOf(req))
			if err != nil {
				return result(nil, err)
			}
			out := make([]profileSummary, 0, len(profiles))
			for _, p := range profiles {
				out = append(out, profileSummary{
					Name:      p.Name,
					Valid:     isValid(p.Status.Conditions),
					Validated: p.Spec.Validated,
					Version:   p.Status.Version,
					Schedule:  p.Spec.Schedule,
					Memory:    p.Spec.Memory,
				})
			}
			return result(out, nil)
		})
	mcp.AddTool(srv, tool("get_profile", "Return one agent profile: name, spec and status."),
		func(ctx context.Context, req *mcp.CallToolRequest, in nameInput) (*mcp.CallToolResult, any, error) {
			return result(s.GetProfile(ctx, identityOf(req), in.Name))
		})
	mcp.AddTool(srv, tool("create_profile", "Create an agent profile. New profiles need the owner's validation before they run."),
		func(ctx context.Context, req *mcp.CallToolRequest, in specInput) (*mcp.CallToolResult, any, error) {
			id := identityOf(req)
			if err := Require(id, ScopeProfilesWrite); err != nil {
				return result(nil, err)
			}
			var spec v1alpha1.AgentProfileSpec
			if err := decodeSpec(in.Spec, &spec); err != nil {
				return result(nil, err)
			}
			return result(s.CreateProfile(ctx, id, in.Name, spec))
		})
	mcp.AddTool(srv, tool("update_profile", "Replace the spec of an agent profile. Changing repos, github, egress or harness clears validation."),
		func(ctx context.Context, req *mcp.CallToolRequest, in specInput) (*mcp.CallToolResult, any, error) {
			id := identityOf(req)
			if err := Require(id, ScopeProfilesWrite); err != nil {
				return result(nil, err)
			}
			var spec v1alpha1.AgentProfileSpec
			if err := decodeSpec(in.Spec, &spec); err != nil {
				return result(nil, err)
			}
			return result(s.UpdateProfile(ctx, id, in.Name, spec))
		})
	mcp.AddTool(srv, tool("suspend_profile", "Suspend or resume the schedule of an agent profile."),
		func(ctx context.Context, req *mcp.CallToolRequest, in suspendInput) (*mcp.CallToolResult, any, error) {
			return result(s.SuspendProfile(ctx, identityOf(req), in.Name, in.Suspend))
		})
	mcp.AddTool(srv, tool("delete_profile", "Delete an agent profile. Queued runs are rejected; running runs continue."),
		func(ctx context.Context, req *mcp.CallToolRequest, in nameInput) (*mcp.CallToolResult, any, error) {
			if err := s.DeleteProfile(ctx, identityOf(req), in.Name); err != nil {
				return result(nil, err)
			}
			return result(map[string]string{"deleted": in.Name}, nil)
		})
	mcp.AddTool(srv, tool("profile_history", "Return the previous versions of an agent profile, newest first."),
		func(ctx context.Context, req *mcp.CallToolRequest, in nameInput) (*mcp.CallToolResult, any, error) {
			return result(s.ProfileHistory(ctx, identityOf(req), in.Name))
		})
	mcp.AddTool(srv, tool("list_harnesses", "List harness profiles."),
		func(ctx context.Context, req *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, any, error) {
			return result(s.ListHarnesses(ctx, identityOf(req)))
		})
	mcp.AddTool(srv, tool("get_harness", "Return one harness profile: name, spec and status."),
		func(ctx context.Context, req *mcp.CallToolRequest, in nameInput) (*mcp.CallToolResult, any, error) {
			return result(s.GetHarness(ctx, identityOf(req), in.Name))
		})
	mcp.AddTool(srv, tool("create_harness", "Create a harness profile (harness kind, version, model, endpoint, login, config)."),
		func(ctx context.Context, req *mcp.CallToolRequest, in specInput) (*mcp.CallToolResult, any, error) {
			id := identityOf(req)
			if err := Require(id, ScopeProfilesWrite); err != nil {
				return result(nil, err)
			}
			var spec v1alpha1.HarnessProfileSpec
			if err := decodeSpec(in.Spec, &spec); err != nil {
				return result(nil, err)
			}
			return result(s.CreateHarness(ctx, id, in.Name, spec))
		})
	mcp.AddTool(srv, tool("update_harness", "Replace the spec of a harness profile. Changing harness, version, endpoint or login clears validation of the profiles that use it."),
		func(ctx context.Context, req *mcp.CallToolRequest, in specInput) (*mcp.CallToolResult, any, error) {
			id := identityOf(req)
			if err := Require(id, ScopeProfilesWrite); err != nil {
				return result(nil, err)
			}
			var spec v1alpha1.HarnessProfileSpec
			if err := decodeSpec(in.Spec, &spec); err != nil {
				return result(nil, err)
			}
			return result(s.UpdateHarness(ctx, id, in.Name, spec))
		})
	mcp.AddTool(srv, tool("delete_harness", "Delete a harness profile."),
		func(ctx context.Context, req *mcp.CallToolRequest, in nameInput) (*mcp.CallToolResult, any, error) {
			if err := s.DeleteHarness(ctx, identityOf(req), in.Name); err != nil {
				return result(nil, err)
			}
			return result(map[string]string{"deleted": in.Name}, nil)
		})
	mcp.AddTool(srv, tool("list_logins", "List login names and kinds. Values are never returned."),
		func(ctx context.Context, req *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, any, error) {
			logins, err := s.ListLogins(ctx, identityOf(req))
			if err != nil {
				return result(nil, err)
			}
			out := make([]loginSummary, 0, len(logins))
			for _, l := range logins {
				out = append(out, loginSummary{Name: l.Name, Kind: l.Kind})
			}
			return result(out, nil)
		})
	mcp.AddTool(srv, tool("start_run", "Start a run of a profile with an optional task. A busy profile returns the run Queued with its queue position."),
		func(ctx context.Context, req *mcp.CallToolRequest, in startRunInput) (*mcp.CallToolResult, any, error) {
			return result(s.CreateRun(ctx, identityOf(req), RunInput(in)))
		})
	mcp.AddTool(srv, tool("list_runs", "List runs, newest first."),
		func(ctx context.Context, req *mcp.CallToolRequest, in listRunsInput) (*mcp.CallToolResult, any, error) {
			return result(s.ListRuns(ctx, identityOf(req), RunFilter(in)))
		})
	mcp.AddTool(srv, tool("get_run", "Return one run: name, spec and status."),
		func(ctx context.Context, req *mcp.CallToolRequest, in nameInput) (*mcp.CallToolResult, any, error) {
			return result(s.GetRun(ctx, identityOf(req), in.Name))
		})
	mcp.AddTool(srv, tool("cancel_run", "Cancel a run."),
		func(ctx context.Context, req *mcp.CallToolRequest, in nameInput) (*mcp.CallToolResult, any, error) {
			return result(s.CancelRun(ctx, identityOf(req), in.Name))
		})
	mcp.AddTool(srv, tool("get_run_logs", "Return the last lines of the launcher log of a run."),
		func(ctx context.Context, req *mcp.CallToolRequest, in runLogsInput) (*mcp.CallToolResult, any, error) {
			return result(t.runLogs(ctx, identityOf(req), in))
		})
	mcp.AddTool(srv, tool("wait_run", "Wait until a run reaches a final phase or the timeout ends, then return the run."),
		func(ctx context.Context, req *mcp.CallToolRequest, in waitRunInput) (*mcp.CallToolResult, any, error) {
			return result(t.waitRun(ctx, identityOf(req), in))
		})
}

func isValid(conds []metav1.Condition) bool {
	for _, c := range conds {
		if c.Type == v1alpha1.ConditionValid {
			return c.Status == metav1.ConditionTrue
		}
	}
	return false
}

func (t *mcpTools) runLogs(ctx context.Context, id *Identity, in runLogsInput) (map[string]string, error) {
	run, err := t.s.GetRun(ctx, id, in.Run)
	if err != nil {
		return nil, err
	}
	tail := in.Tail
	if tail <= 0 {
		tail = defaultLogTail
	}
	tail = min(tail, maxLogTail)
	if run.Status.PodName == "" {
		return nil, errNoPod
	}
	var buf bytes.Buffer
	if err := t.logs.Stream(ctx, &buf, t.s.Namespace, run.Status.PodName, false, tail); err != nil {
		return nil, newAPIError(http.StatusBadGateway, "run", "reading the log failed")
	}
	return map[string]string{"text": buf.String()}, nil
}

func (t *mcpTools) waitRun(ctx context.Context, id *Identity, in waitRunInput) (*RunView, error) {
	if in.TimeoutSeconds < 1 || in.TimeoutSeconds > maxWaitSeconds {
		return nil, newAPIError(http.StatusUnprocessableEntity, "timeoutSeconds", "must be between 1 and 600")
	}
	deadline := time.Now().Add(time.Duration(in.TimeoutSeconds) * time.Second)
	for {
		run, err := t.s.GetRun(ctx, id, in.Run)
		if err != nil {
			return nil, err
		}
		if run.Status.Phase.Final() || !time.Now().Before(deadline) {
			return run, nil
		}
		select {
		case <-ctx.Done():
			return run, nil
		case <-time.After(min(waitPoll, time.Until(deadline))):
		}
	}
}

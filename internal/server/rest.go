package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/mudler/agent-controller/api/v1alpha1"
)

// maxBodyBytes bounds a request body.
const maxBodyBytes = 1 << 20

// NewHandler returns the HTTP handler of the server: the REST API under
// /v1, the MCP server at /mcp, the Logins page at /logins and an
// unauthenticated /healthz.
func NewHandler(s *Store, tokens TokenStore, logs LogStreamer) http.Handler {
	api := &restAPI{s: s, logs: logs}
	v1 := http.NewServeMux()
	v1.HandleFunc("GET /v1/policy", api.getPolicy)
	v1.HandleFunc("GET /v1/profiles", api.listProfiles)
	v1.HandleFunc("POST /v1/profiles", api.createProfile)
	v1.HandleFunc("GET /v1/profiles/{name}", api.getProfile)
	v1.HandleFunc("PUT /v1/profiles/{name}", api.updateProfile)
	v1.HandleFunc("DELETE /v1/profiles/{name}", api.deleteProfile)
	v1.HandleFunc("POST /v1/profiles/{name}/validate", api.validateProfile)
	v1.HandleFunc("GET /v1/profiles/{name}/history", api.profileHistory)
	v1.HandleFunc("GET /v1/harnesses", api.listHarnesses)
	v1.HandleFunc("POST /v1/harnesses", api.createHarness)
	v1.HandleFunc("GET /v1/harnesses/{name}", api.getHarness)
	v1.HandleFunc("PUT /v1/harnesses/{name}", api.updateHarness)
	v1.HandleFunc("DELETE /v1/harnesses/{name}", api.deleteHarness)
	v1.HandleFunc("GET /v1/logins", api.listLogins)
	v1.HandleFunc("PUT /v1/logins/{name}", api.putLogin)
	v1.HandleFunc("DELETE /v1/logins/{name}", api.deleteLogin)
	v1.HandleFunc("GET /v1/runs", api.listRuns)
	v1.HandleFunc("POST /v1/runs", api.createRun)
	v1.HandleFunc("GET /v1/runs/{name}", api.getRun)
	v1.HandleFunc("POST /v1/runs/{name}/cancel", api.cancelRun)
	v1.HandleFunc("GET /v1/runs/{name}/logs", api.runLogs)
	v1.HandleFunc("/v1/", func(w http.ResponseWriter, _ *http.Request) {
		WriteError(w, newAPIError(http.StatusNotFound, "path", "no such route"))
	})

	mux := http.NewServeMux()
	mux.Handle("/v1/", Middleware(tokens, v1))
	mux.Handle("/mcp", NewMCPHandler(s, tokens, logs))
	mountLoginsPage(mux)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	return mux
}

type restAPI struct {
	s    *Store
	logs LogStreamer
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		WriteError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(b)
}

// decode reads a JSON body. strict rejects unknown fields.
func decode(r *http.Request, v any, strict bool) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, maxBodyBytes))
	if strict {
		dec.DisallowUnknownFields()
	}
	if err := dec.Decode(v); err != nil {
		return newAPIError(http.StatusUnprocessableEntity, "body", "invalid JSON: "+err.Error())
	}
	return nil
}

func identity(r *http.Request) *Identity {
	id, _ := IdentityFrom(r.Context())
	return id
}

func (a *restAPI) respond(w http.ResponseWriter, status int, v any, err error) {
	if err != nil {
		WriteError(w, err)
		return
	}
	writeJSON(w, status, v)
}

func (a *restAPI) getPolicy(w http.ResponseWriter, r *http.Request) {
	v, err := a.s.GetPolicy(r.Context(), identity(r))
	a.respond(w, http.StatusOK, v, err)
}

type profileBody struct {
	Name string                    `json:"name"`
	Spec v1alpha1.AgentProfileSpec `json:"spec"`
}

type harnessBody struct {
	Name string                      `json:"name"`
	Spec v1alpha1.HarnessProfileSpec `json:"spec"`
}

func nameMatches(body, path string) error {
	if body != "" && body != path {
		return newAPIError(http.StatusUnprocessableEntity, "name", "does not match the path")
	}
	return nil
}

func (a *restAPI) listProfiles(w http.ResponseWriter, r *http.Request) {
	v, err := a.s.ListProfiles(r.Context(), identity(r))
	a.respond(w, http.StatusOK, v, err)
}

func (a *restAPI) createProfile(w http.ResponseWriter, r *http.Request) {
	if err := Require(identity(r), ScopeProfilesWrite); err != nil {
		WriteError(w, err)
		return
	}
	var body profileBody
	if err := decode(r, &body, true); err != nil {
		WriteError(w, err)
		return
	}
	v, err := a.s.CreateProfile(r.Context(), identity(r), body.Name, body.Spec)
	a.respond(w, http.StatusCreated, v, err)
}

func (a *restAPI) getProfile(w http.ResponseWriter, r *http.Request) {
	v, err := a.s.GetProfile(r.Context(), identity(r), r.PathValue("name"))
	a.respond(w, http.StatusOK, v, err)
}

func (a *restAPI) updateProfile(w http.ResponseWriter, r *http.Request) {
	if err := Require(identity(r), ScopeProfilesWrite); err != nil {
		WriteError(w, err)
		return
	}
	var body profileBody
	if err := decode(r, &body, true); err != nil {
		WriteError(w, err)
		return
	}
	if err := nameMatches(body.Name, r.PathValue("name")); err != nil {
		WriteError(w, err)
		return
	}
	v, err := a.s.UpdateProfile(r.Context(), identity(r), r.PathValue("name"), body.Spec)
	a.respond(w, http.StatusOK, v, err)
}

func (a *restAPI) deleteProfile(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	err := a.s.DeleteProfile(r.Context(), identity(r), name)
	a.respond(w, http.StatusOK, map[string]string{"deleted": name}, err)
}

func (a *restAPI) validateProfile(w http.ResponseWriter, r *http.Request) {
	v, err := a.s.ValidateProfile(r.Context(), identity(r), r.PathValue("name"))
	a.respond(w, http.StatusOK, v, err)
}

func (a *restAPI) profileHistory(w http.ResponseWriter, r *http.Request) {
	v, err := a.s.ProfileHistory(r.Context(), identity(r), r.PathValue("name"))
	a.respond(w, http.StatusOK, v, err)
}

func (a *restAPI) listHarnesses(w http.ResponseWriter, r *http.Request) {
	v, err := a.s.ListHarnesses(r.Context(), identity(r))
	a.respond(w, http.StatusOK, v, err)
}

func (a *restAPI) createHarness(w http.ResponseWriter, r *http.Request) {
	if err := Require(identity(r), ScopeProfilesWrite); err != nil {
		WriteError(w, err)
		return
	}
	var body harnessBody
	if err := decode(r, &body, true); err != nil {
		WriteError(w, err)
		return
	}
	v, err := a.s.CreateHarness(r.Context(), identity(r), body.Name, body.Spec)
	a.respond(w, http.StatusCreated, v, err)
}

func (a *restAPI) getHarness(w http.ResponseWriter, r *http.Request) {
	v, err := a.s.GetHarness(r.Context(), identity(r), r.PathValue("name"))
	a.respond(w, http.StatusOK, v, err)
}

func (a *restAPI) updateHarness(w http.ResponseWriter, r *http.Request) {
	if err := Require(identity(r), ScopeProfilesWrite); err != nil {
		WriteError(w, err)
		return
	}
	var body harnessBody
	if err := decode(r, &body, true); err != nil {
		WriteError(w, err)
		return
	}
	if err := nameMatches(body.Name, r.PathValue("name")); err != nil {
		WriteError(w, err)
		return
	}
	v, err := a.s.UpdateHarness(r.Context(), identity(r), r.PathValue("name"), body.Spec)
	a.respond(w, http.StatusOK, v, err)
}

func (a *restAPI) deleteHarness(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	err := a.s.DeleteHarness(r.Context(), identity(r), name)
	a.respond(w, http.StatusOK, map[string]string{"deleted": name}, err)
}

func (a *restAPI) listLogins(w http.ResponseWriter, r *http.Request) {
	v, err := a.s.ListLogins(r.Context(), identity(r))
	a.respond(w, http.StatusOK, v, err)
}

func (a *restAPI) putLogin(w http.ResponseWriter, r *http.Request) {
	if err := Require(identity(r), ScopeLoginsWrite); err != nil {
		WriteError(w, err)
		return
	}
	var body LoginInput
	if err := decode(r, &body, true); err != nil {
		WriteError(w, err)
		return
	}
	v, err := a.s.PutLogin(r.Context(), identity(r), r.PathValue("name"), body)
	a.respond(w, http.StatusOK, v, err)
}

func (a *restAPI) deleteLogin(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	err := a.s.DeleteLogin(r.Context(), identity(r), name)
	a.respond(w, http.StatusOK, map[string]string{"deleted": name}, err)
}

func (a *restAPI) listRuns(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := RunFilter{Profile: q.Get("profile"), Phase: q.Get("phase")}
	if l := q.Get("limit"); l != "" {
		n, err := strconv.Atoi(l)
		if err != nil || n < 1 {
			WriteError(w, newAPIError(http.StatusUnprocessableEntity, "limit", "must be a positive integer"))
			return
		}
		f.Limit = n
	}
	v, err := a.s.ListRuns(r.Context(), identity(r), f)
	a.respond(w, http.StatusOK, v, err)
}

func (a *restAPI) createRun(w http.ResponseWriter, r *http.Request) {
	if err := Require(identity(r), ScopeRunsWrite); err != nil {
		WriteError(w, err)
		return
	}
	// Lenient decode: a client-sent requestedBy is ignored.
	var body RunInput
	if err := decode(r, &body, false); err != nil {
		WriteError(w, err)
		return
	}
	v, err := a.s.CreateRun(r.Context(), identity(r), body)
	a.respond(w, http.StatusCreated, v, err)
}

func (a *restAPI) getRun(w http.ResponseWriter, r *http.Request) {
	v, err := a.s.GetRun(r.Context(), identity(r), r.PathValue("name"))
	a.respond(w, http.StatusOK, v, err)
}

func (a *restAPI) cancelRun(w http.ResponseWriter, r *http.Request) {
	v, err := a.s.CancelRun(r.Context(), identity(r), r.PathValue("name"))
	a.respond(w, http.StatusOK, v, err)
}

// parseTail reads a tail value: default 200, at most 5000.
func parseTail(s string) (int64, error) {
	if s == "" {
		return defaultLogTail, nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return 0, newAPIError(http.StatusUnprocessableEntity, "tail", "must be a non-negative integer")
	}
	return min(n, maxLogTail), nil
}

var errNoPod = newAPIError(http.StatusConflict, "run", "the run has no launcher pod yet")

func (a *restAPI) runLogs(w http.ResponseWriter, r *http.Request) {
	run, err := a.s.GetRun(r.Context(), identity(r), r.PathValue("name"))
	if err != nil {
		WriteError(w, err)
		return
	}
	tail, err := parseTail(r.URL.Query().Get("tail"))
	if err != nil {
		WriteError(w, err)
		return
	}
	if run.Status.PodName == "" {
		WriteError(w, errNoPod)
		return
	}
	follow := r.URL.Query().Get("follow") == "true"
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if err := a.logs.Stream(r.Context(), newFlushWriter(w), a.s.Namespace, run.Status.PodName, follow, tail); err != nil && !errors.Is(err, r.Context().Err()) {
		_, _ = io.WriteString(w, "\nlog stream ended: "+err.Error()+"\n")
	}
}

package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"

	"github.com/mudler/agent-controller/internal/policy"
)

func TestErrorResponse(t *testing.T) {
	gr := schema.GroupResource{Group: "agents.mudler.dev", Resource: "agentprofiles"}
	tests := []struct {
		name   string
		err    error
		status int
		field  string
		reason string
	}{
		{name: "api error", err: newAPIError(403, "scope", "missing runs:write"), status: 403, field: "scope", reason: "missing runs:write"},
		{
			name:   "policy errors",
			err:    fmt.Errorf("wrapped: %w", policy.Errors{{Field: "egress.extraHosts[0]", Reason: "not in policy", Allowed: []string{"github.com:443"}}}),
			status: 422, field: "egress.extraHosts[0]", reason: "not in policy",
		},
		{name: "not found", err: fmt.Errorf("getting: %w", apierrors.NewNotFound(gr, "x")), status: 404, field: "name", reason: "not found"},
		{name: "exists", err: apierrors.NewAlreadyExists(gr, "x"), status: 409, reason: "already exists"},
		{name: "conflict", err: apierrors.NewConflict(gr, "x", errors.New("changed")), status: 409},
		{
			name:   "invalid",
			err:    apierrors.NewInvalid(schema.GroupKind{Kind: "AgentProfile"}, "x", field.ErrorList{field.Invalid(field.NewPath("spec", "repos"), 2, "too many")}),
			status: 422, field: "spec.repos",
		},
		{name: "internal", err: errors.New("secret detail"), status: 500, reason: "internal error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, body := ErrorResponse(tt.err)
			if status != tt.status {
				t.Errorf("status = %d, want %d", status, tt.status)
			}
			var out struct {
				Error policy.FieldError `json:"error"`
			}
			if err := json.Unmarshal(body, &out); err != nil {
				t.Fatalf("body %s: %v", body, err)
			}
			if tt.field != "" && out.Error.Field != tt.field {
				t.Errorf("field = %q, want %q", out.Error.Field, tt.field)
			}
			if tt.reason != "" && out.Error.Reason != tt.reason {
				t.Errorf("reason = %q, want %q", out.Error.Reason, tt.reason)
			}
			if strings.Contains(string(body), "secret detail") {
				t.Errorf("internal error leaked: %s", body)
			}
		})
	}
}

func TestErrorAllowedList(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteError(rec, policy.Errors{{Field: "egress.extraHosts[0]", Reason: "not in policy", Allowed: []string{"a:443", "b:443"}}})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d", rec.Code)
	}
	want := `{"error":{"field":"egress.extraHosts[0]","reason":"not in policy","allowed":["a:443","b:443"]}`
	if !strings.HasPrefix(rec.Body.String(), want) {
		t.Errorf("body = %s, want prefix %s", rec.Body, want)
	}
}

// Package server serves the REST API, the MCP server and the Logins page.
// Both APIs share the Store, so scopes and validation are the same.
package server

import (
	"encoding/json"
	"errors"
	"net/http"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/mudler/agent-controller/internal/policy"
)

// APIError is an error with an HTTP status and a field error body.
type APIError struct {
	Status int
	Err    policy.FieldError
}

// Error implements error.
func (e *APIError) Error() string {
	return e.Err.Error()
}

func newAPIError(status int, field, reason string, allowed ...string) *APIError {
	return &APIError{Status: status, Err: policy.FieldError{Field: field, Reason: reason, Allowed: allowed}}
}

// errorBody is the JSON shape of every error response.
type errorBody struct {
	Error  policy.FieldError   `json:"error"`
	Errors []policy.FieldError `json:"errors,omitempty"`
}

// ErrorResponse maps an error to an HTTP status and JSON body. Kubernetes
// errors are translated; unknown errors become a generic 500 so internal
// details do not leak.
func ErrorResponse(err error) (int, []byte) {
	status, body := classify(err)
	b, mErr := json.Marshal(body)
	if mErr != nil {
		return http.StatusInternalServerError, []byte(`{"error":{"field":"","reason":"internal error"}}`)
	}
	return status, b
}

func classify(err error) (int, errorBody) {
	var apiErr *APIError
	var fieldErrs policy.Errors
	var fieldErr policy.FieldError
	switch {
	case errors.As(err, &apiErr):
		return apiErr.Status, errorBody{Error: apiErr.Err}
	case errors.As(err, &fieldErrs) && len(fieldErrs) > 0:
		return http.StatusUnprocessableEntity, errorBody{Error: fieldErrs[0], Errors: fieldErrs}
	case errors.As(err, &fieldErr):
		return http.StatusUnprocessableEntity, errorBody{Error: fieldErr}
	case apierrors.IsNotFound(err):
		return http.StatusNotFound, errorBody{Error: policy.FieldError{Field: "name", Reason: "not found"}}
	case apierrors.IsAlreadyExists(err):
		return http.StatusConflict, errorBody{Error: policy.FieldError{Field: "name", Reason: "already exists"}}
	case apierrors.IsConflict(err):
		return http.StatusConflict, errorBody{Error: policy.FieldError{Field: "", Reason: "the object changed; retry"}}
	case apierrors.IsInvalid(err):
		return http.StatusUnprocessableEntity, errorBody{Error: invalidField(err)}
	default:
		return http.StatusInternalServerError, errorBody{Error: policy.FieldError{Field: "", Reason: "internal error"}}
	}
}

// invalidField turns an API server validation error into a field error.
func invalidField(err error) policy.FieldError {
	var status apierrors.APIStatus
	if errors.As(err, &status) {
		if d := status.Status().Details; d != nil && len(d.Causes) > 0 {
			return policy.FieldError{Field: d.Causes[0].Field, Reason: d.Causes[0].Message}
		}
	}
	return policy.FieldError{Reason: "invalid object"}
}

// WriteError writes the JSON error response for err.
func WriteError(w http.ResponseWriter, err error) {
	status, body := ErrorResponse(err)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

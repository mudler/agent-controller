// Package policy validates harness profiles, agent profiles and runs against
// the AgentPolicy and the Login objects.
package policy

import (
	"strings"
)

// FieldError is one validation failure. The JSON shape is the body of the
// REST error format, so a caller can correct the named field.
type FieldError struct {
	Field   string   `json:"field"`
	Reason  string   `json:"reason"`
	Allowed []string `json:"allowed,omitempty"`
}

// Error implements error.
func (e FieldError) Error() string {
	return e.Field + ": " + e.Reason
}

// Errors is a list of field errors. A nil Errors means valid.
type Errors []FieldError

// Error implements error.
func (e Errors) Error() string {
	parts := make([]string, 0, len(e))
	for _, fe := range e {
		parts = append(parts, fe.Error())
	}
	return strings.Join(parts, "; ")
}

// Err returns e as an error, or nil when e is empty.
func (e Errors) Err() error {
	if len(e) == 0 {
		return nil
	}
	return e
}

func (e *Errors) add(field, reason string, allowed ...string) {
	*e = append(*e, FieldError{Field: field, Reason: reason, Allowed: allowed})
}

func (e *Errors) addPrefixed(prefix string, errs Errors) {
	for _, fe := range errs {
		switch {
		case fe.Field == "":
			fe.Field = prefix
		case strings.HasPrefix(fe.Field, "["):
			fe.Field = prefix + fe.Field
		default:
			fe.Field = prefix + "." + fe.Field
		}
		*e = append(*e, fe)
	}
}

func (e Errors) orNil() Errors {
	if len(e) == 0 {
		return nil
	}
	return e
}

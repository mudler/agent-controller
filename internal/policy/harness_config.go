package policy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	"github.com/mudler/agent-controller/api/v1alpha1"
)

// NibTools lists the tool names that nib accepts.
var NibTools = []string{"bash", "read", "write", "edit", "glob", "grep", "tree", "index", "repo_map"}

var claudeToolPattern = regexp.MustCompile(`^([A-Za-z]+|Bash\([^()\n]+\))$`)

const (
	minContextTokens = 1024
	maxContextTokens = 2000000
	maxTurnsLimit    = 10000
)

// ValidateHarnessConfig checks the harness-specific config. Unknown keys are
// errors. A nil config is valid.
func ValidateHarnessConfig(kind v1alpha1.HarnessKind, raw *apiextensionsv1.JSON) Errors {
	var errs Errors
	if raw == nil || len(bytes.TrimSpace(raw.Raw)) == 0 || string(bytes.TrimSpace(raw.Raw)) == "null" {
		return nil
	}
	cfg := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw.Raw, &cfg); err != nil {
		errs.add("config", "must be a JSON object")
		return errs
	}

	var known map[string]func(json.RawMessage) Errors
	switch kind {
	case v1alpha1.HarnessNib:
		known = map[string]func(json.RawMessage) Errors{
			"tools":         nibTools,
			"contextTokens": intRange(minContextTokens, maxContextTokens),
		}
	case v1alpha1.HarnessClaudeCode:
		known = map[string]func(json.RawMessage) Errors{
			"tools":         claudeTools,
			"maxTurns":      intRange(1, maxTurnsLimit),
			"contextTokens": intRange(minContextTokens, maxContextTokens),
		}
	default:
		errs.add("config", fmt.Sprintf("no config schema for harness %q", kind))
		return errs
	}

	allowedKeys := slices.Sorted(maps.Keys(known))
	for _, key := range slices.Sorted(maps.Keys(cfg)) {
		check, ok := known[key]
		if !ok {
			errs.add("config."+key, "unknown key", allowedKeys...)
			continue
		}
		errs.addPrefixed("config."+key, check(cfg[key]))
	}
	return errs.orNil()
}

// valueErr reports an error on the value itself. addPrefixed replaces the
// empty field with the key path.
func valueErr(reason string, allowed ...string) Errors {
	return Errors{{Field: "", Reason: reason, Allowed: allowed}}
}

func intRange(lo, hi int64) func(json.RawMessage) Errors {
	return func(raw json.RawMessage) Errors {
		var n int64
		if err := json.Unmarshal(raw, &n); err != nil {
			return valueErr("must be an integer")
		}
		if n < lo || n > hi {
			return valueErr(fmt.Sprintf("must be between %d and %d", lo, hi))
		}
		return nil
	}
}

func stringList(raw json.RawMessage) ([]string, Errors) {
	var list []string
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, valueErr("must be a list of strings")
	}
	return list, nil
}

func nibTools(raw json.RawMessage) Errors {
	list, errs := stringList(raw)
	if errs != nil {
		return errs
	}
	for i, tool := range list {
		if !slices.Contains(NibTools, tool) {
			errs = append(errs, FieldError{Field: fmt.Sprintf("[%d]", i), Reason: "unknown nib tool", Allowed: NibTools})
		}
	}
	return errs
}

func claudeTools(raw json.RawMessage) Errors {
	list, errs := stringList(raw)
	if errs != nil {
		return errs
	}
	if len(list) == 0 {
		return valueErr("must not be empty")
	}
	for i, tool := range list {
		if !claudeToolPattern.MatchString(tool) {
			errs = append(errs, FieldError{Field: fmt.Sprintf("[%d]", i), Reason: "must be a tool name or Bash(<pattern>)"})
		}
	}
	return errs
}

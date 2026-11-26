package policy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	"github.com/mudler/agent-controller/api/v1alpha1"
)

// canonicalConfig returns the config as JSON with sorted keys and no
// insignificant space. A missing or null config is "null". Invalid JSON is
// kept as raw bytes, so it still changes the hash.
func canonicalConfig(raw *apiextensionsv1.JSON) []byte {
	if raw == nil || len(bytes.TrimSpace(raw.Raw)) == 0 {
		return []byte("null")
	}
	var v any
	if err := json.Unmarshal(raw.Raw, &v); err != nil {
		return raw.Raw
	}
	out, err := json.Marshal(v)
	if err != nil {
		return raw.Raw
	}
	return out
}

// HarnessApprovalHash is a stable hash of the harness fields that the owner
// approves: kind, version, endpoint, login and config. The model is left
// out, so a model change keeps approval.
func HarnessApprovalHash(h *v1alpha1.HarnessProfileSpec) string {
	doc := struct {
		Harness  v1alpha1.HarnessKind `json:"harness"`
		Version  string               `json:"version"`
		Endpoint string               `json:"endpoint"`
		Login    string               `json:"login"`
		Config   json.RawMessage      `json:"config"`
	}{h.Harness, h.Version, h.Endpoint, h.Login, canonicalConfig(h.Config)}
	// Marshalling strings and valid canonical JSON does not fail; on the
	// impossible error the hash covers the fields without the config.
	b, err := json.Marshal(doc)
	if err != nil {
		doc.Config = json.RawMessage("null")
		b, _ = json.Marshal(doc)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// BindingFor returns the validation binding of a harness profile and a
// GitHub login.
func BindingFor(h *v1alpha1.HarnessProfile, github *v1alpha1.Login) *v1alpha1.ValidationBinding {
	return &v1alpha1.ValidationBinding{
		HarnessUID:     h.UID,
		HarnessHash:    HarnessApprovalHash(&h.Spec),
		GitHubLoginUID: github.UID,
	}
}

// ValidationCurrent reports whether a profile is validated and the harness
// profile and GitHub login it references are still the objects, with the
// same approval-relevant harness fields, that the owner validated it
// against. A deleted
// and recreated object has a new UID, so it does not match.
func ValidationCurrent(env Env, p *v1alpha1.AgentProfile) bool {
	b := p.Status.ValidatedFor
	if !p.Spec.Validated || b == nil {
		return false
	}
	h, ok := env.Harnesses[p.Spec.Harness]
	if !ok || h.UID != b.HarnessUID || HarnessApprovalHash(&h.Spec) != b.HarnessHash {
		return false
	}
	gh, ok := env.Logins[p.Spec.GitHub]
	return ok && gh.UID == b.GitHubLoginUID
}

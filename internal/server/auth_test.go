package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func tokensSecret(t *testing.T, entries map[string]TokenEntry) *corev1.Secret {
	t.Helper()
	data, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: DefaultTokensSecret, Namespace: "agents"},
		Data:       map[string][]byte{TokensKey: data},
	}
}

// countingReader counts Get calls.
type countingReader struct {
	client.Reader
	gets atomic.Int32
}

func (c *countingReader) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	c.gets.Add(1)
	return c.Reader.Get(ctx, key, obj, opts...)
}

func TestTokenStoreLookup(t *testing.T) {
	secret := tokensSecret(t, map[string]TokenEntry{
		"planner": {SHA256: HashToken("planner-token"), Scopes: []string{"runs:read", "runs:write"}},
		"owner":   {SHA256: strings.ToUpper(HashToken("owner-token")), Scopes: []string{"logins:write"}},
	})
	c := fake.NewClientBuilder().WithObjects(secret).Build()
	store := NewSecretTokenStore(c, "agents", DefaultTokensSecret, time.Minute)

	id, err := store.Lookup(context.Background(), "planner-token")
	if err != nil {
		t.Fatal(err)
	}
	if id.Name != "planner" || !id.Scopes[ScopeRunsWrite] || id.Scopes[ScopeLoginsWrite] {
		t.Errorf("identity = %+v", id)
	}
	if id, err := store.Lookup(context.Background(), "owner-token"); err != nil || id.Name != "owner" {
		t.Errorf("uppercase hash: %v %v", id, err)
	}
	for _, bad := range []string{"", "nope", HashToken("planner-token")} {
		if _, err := store.Lookup(context.Background(), bad); err != ErrUnauthorized {
			t.Errorf("Lookup(%q) error = %v, want ErrUnauthorized", bad, err)
		}
	}
}

func TestTokenStoreReloadsAfterTTL(t *testing.T) {
	secret := tokensSecret(t, map[string]TokenEntry{"a": {SHA256: HashToken("a-token"), Scopes: []string{"runs:read"}}})
	c := fake.NewClientBuilder().WithObjects(secret).Build()
	reader := &countingReader{Reader: c}
	store := NewSecretTokenStore(reader, "agents", DefaultTokensSecret, 30*time.Second).(*secretTokenStore)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	ctx := context.Background()

	if _, err := store.Lookup(ctx, "a-token"); err != nil {
		t.Fatal(err)
	}
	// A new token is not seen before the TTL ends.
	updated := tokensSecret(t, map[string]TokenEntry{"b": {SHA256: HashToken("b-token"), Scopes: []string{"runs:read"}}})
	if err := c.Update(ctx, updated); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Lookup(ctx, "b-token"); err != ErrUnauthorized {
		t.Errorf("before TTL: error = %v, want ErrUnauthorized", err)
	}
	if got := reader.gets.Load(); got != 1 {
		t.Errorf("gets = %d, want 1", got)
	}
	now = now.Add(31 * time.Second)
	if _, err := store.Lookup(ctx, "b-token"); err != nil {
		t.Errorf("after TTL: %v", err)
	}
	if _, err := store.Lookup(ctx, "a-token"); err != ErrUnauthorized {
		t.Errorf("removed token still accepted: %v", err)
	}
}

func TestTokenStoreMissingSecret(t *testing.T) {
	store := NewSecretTokenStore(fake.NewClientBuilder().Build(), "agents", DefaultTokensSecret, time.Minute)
	if _, err := store.Lookup(context.Background(), "x"); err == nil || err == ErrUnauthorized {
		t.Errorf("error = %v, want a load error", err)
	}
}

type staticTokens map[string]*Identity

func (s staticTokens) Lookup(_ context.Context, bearer string) (*Identity, error) {
	if id, ok := s[bearer]; ok {
		return id, nil
	}
	return nil, ErrUnauthorized
}

func TestAuthMiddleware(t *testing.T) {
	tokens := staticTokens{"good": {Name: "planner", Scopes: map[Scope]bool{ScopeRunsRead: true}}}
	h := Middleware(tokens, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := IdentityFrom(r.Context())
		if !ok {
			t.Error("no identity in context")
			return
		}
		if err := Require(id, ScopeRunsWrite); err != nil {
			WriteError(w, err)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))

	tests := []struct {
		name   string
		header string
		status int
		body   string
	}{
		{name: "no header", header: "", status: 401, body: `"field":"authorization"`},
		{name: "unknown token", header: "Bearer bad", status: 401, body: "invalid bearer token"},
		{name: "malformed", header: "Basic good", status: 401},
		{name: "empty bearer", header: "Bearer ", status: 401},
		{name: "missing scope", header: "Bearer good", status: 403, body: `{"error":{"field":"scope","reason":"missing runs:write"}}`},
		{name: "case insensitive scheme", header: "bearer good", status: 403},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/v1/runs", nil)
			if tt.header != "" {
				req.Header.Set("Authorization", tt.header)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tt.status, rec.Body)
			}
			if tt.body != "" && !strings.Contains(rec.Body.String(), tt.body) {
				t.Errorf("body = %s, want it to contain %s", rec.Body, tt.body)
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q", ct)
			}
		})
	}
}

func TestHashToken(t *testing.T) {
	if got := HashToken("abc"); got != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Errorf("HashToken(abc) = %s", got)
	}
}

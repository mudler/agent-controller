package server

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Scope is a permission of an API token.
type Scope string

// Token scopes.
const (
	ScopeRunsRead         Scope = "runs:read"
	ScopeRunsWrite        Scope = "runs:write"
	ScopeProfilesWrite    Scope = "profiles:write"
	ScopeProfilesValidate Scope = "profiles:validate"
	ScopeLoginsWrite      Scope = "logins:write"
)

// AllScopes lists every scope.
var AllScopes = []Scope{ScopeRunsRead, ScopeRunsWrite, ScopeProfilesWrite, ScopeProfilesValidate, ScopeLoginsWrite}

// TokensKey is the data key of the tokens Secret.
const TokensKey = "tokens.json"

// DefaultTokensSecret is the default name of the tokens Secret.
const DefaultTokensSecret = "agent-controller-tokens"

// Identity is the caller behind a token.
type Identity struct {
	Name   string
	Scopes map[Scope]bool
}

// TokenStore resolves bearer tokens.
type TokenStore interface {
	Lookup(ctx context.Context, bearer string) (*Identity, error)
}

// ErrUnauthorized means the bearer token is missing or unknown.
var ErrUnauthorized = errors.New("missing or invalid bearer token")

// TokenEntry is one entry of tokens.json.
type TokenEntry struct {
	SHA256 string   `json:"sha256"`
	Scopes []string `json:"scopes"`
}

// HashToken returns the hex SHA-256 of a token.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

type secretTokenStore struct {
	reader    client.Reader
	key       client.ObjectKey
	ttl       time.Duration
	now       func() time.Time
	mu        sync.Mutex
	loadedAt  time.Time
	entries   map[string]TokenEntry
	loadError error
}

// NewSecretTokenStore returns a TokenStore that reads tokens.json from one
// Secret and reloads it at most every ttl. Use an uncached reader, so the
// server needs only "get" on that Secret.
func NewSecretTokenStore(c client.Reader, namespace, secretName string, ttl time.Duration) TokenStore {
	return &secretTokenStore{reader: c, key: client.ObjectKey{Namespace: namespace, Name: secretName}, ttl: ttl, now: time.Now}
}

func (s *secretTokenStore) load(ctx context.Context) (map[string]TokenEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.entries != nil && s.now().Sub(s.loadedAt) < s.ttl {
		return s.entries, nil
	}
	secret := &corev1.Secret{}
	if err := s.reader.Get(ctx, s.key, secret); err != nil {
		return nil, fmt.Errorf("reading tokens secret: %w", err)
	}
	entries := map[string]TokenEntry{}
	if err := json.Unmarshal(secret.Data[TokensKey], &entries); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", TokensKey, err)
	}
	s.entries = entries
	s.loadedAt = s.now()
	return entries, nil
}

// Lookup compares the token hash with every entry in constant time.
func (s *secretTokenStore) Lookup(ctx context.Context, bearer string) (*Identity, error) {
	if bearer == "" {
		return nil, ErrUnauthorized
	}
	entries, err := s.load(ctx)
	if err != nil {
		return nil, err
	}
	hash := []byte(HashToken(bearer))
	var found *Identity
	for name, e := range entries {
		if subtle.ConstantTimeCompare(hash, []byte(strings.ToLower(e.SHA256))) == 1 {
			id := &Identity{Name: name, Scopes: map[Scope]bool{}}
			for _, sc := range e.Scopes {
				id.Scopes[Scope(sc)] = true
			}
			found = id
		}
	}
	if found == nil {
		return nil, ErrUnauthorized
	}
	return found, nil
}

type identityKey struct{}

// WithIdentity returns a context that carries the caller identity.
func WithIdentity(ctx context.Context, id *Identity) context.Context {
	return context.WithValue(ctx, identityKey{}, id)
}

// IdentityFrom returns the caller identity of a request context.
func IdentityFrom(ctx context.Context) (*Identity, bool) {
	id, ok := ctx.Value(identityKey{}).(*Identity)
	return id, ok && id != nil
}

// BearerToken extracts the token of an "Authorization: Bearer" header.
func BearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	scheme, token, ok := strings.Cut(h, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return strings.TrimSpace(token)
}

// Middleware authenticates the bearer token and stores the identity in the
// request context. It answers 401 with a JSON error otherwise.
func Middleware(store TokenStore, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := store.Lookup(r.Context(), BearerToken(r))
		if err != nil {
			if !errors.Is(err, ErrUnauthorized) {
				err = newAPIError(http.StatusServiceUnavailable, "authorization", "token store unavailable")
			} else {
				err = newAPIError(http.StatusUnauthorized, "authorization", ErrUnauthorized.Error())
			}
			w.Header().Set("WWW-Authenticate", `Bearer realm="agent-controller"`)
			WriteError(w, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(WithIdentity(r.Context(), id)))
	})
}

// Require returns a 403 error when the identity lacks the scope.
func Require(id *Identity, s Scope) error {
	if id == nil || !id.Scopes[s] {
		return newAPIError(http.StatusForbidden, "scope", "missing "+string(s))
	}
	return nil
}

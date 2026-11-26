package server

import (
	"cmp"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mudler/agent-controller/api/v1alpha1"
	"github.com/mudler/agent-controller/internal/controller"
	"github.com/mudler/agent-controller/internal/policy"
)

const (
	// maxProfileName keeps scheduled and manual run names within limits.
	maxProfileName = 40
	// defaultRunLimit and maxRunLimit bound GET /v1/runs.
	defaultRunLimit = 50
	maxRunLimit     = 500
	// createRunWait is how long run creation waits for the controller.
	createRunWait = 5 * time.Second
	// maxLoginValue bounds an uploaded login value.
	maxLoginValue = 256 * 1024
)

var loginRepoPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/([A-Za-z0-9_.-]+|\*)$`)

// Store holds the handlers shared by REST and MCP. Every method checks the
// scope of the caller first.
type Store struct {
	// C reads and writes the API. It should not be cached, so the server
	// needs no list or watch beyond what it serves.
	C                client.Client
	Namespace        string
	LoginsNamespace  string
	HistoryNamespace string
	HarnessVersions  map[v1alpha1.HarnessKind][]string
	// CreateRunWait overrides how long CreateRun waits for a phase.
	CreateRunWait time.Duration
	now           func() time.Time
}

func (s *Store) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// ProfileView is the API shape of an AgentProfile.
type ProfileView struct {
	Name   string                      `json:"name"`
	Spec   v1alpha1.AgentProfileSpec   `json:"spec"`
	Status v1alpha1.AgentProfileStatus `json:"status"`
}

// HarnessView is the API shape of a HarnessProfile.
type HarnessView struct {
	Name   string                        `json:"name"`
	Spec   v1alpha1.HarnessProfileSpec   `json:"spec"`
	Status v1alpha1.HarnessProfileStatus `json:"status"`
}

// RunView is the API shape of an AgentRun.
type RunView struct {
	Name   string                  `json:"name"`
	Spec   v1alpha1.AgentRunSpec   `json:"spec"`
	Status v1alpha1.AgentRunStatus `json:"status"`
}

// LoginView is the API shape of a Login. It never holds a value.
type LoginView struct {
	Name        string             `json:"name"`
	Kind        v1alpha1.LoginKind `json:"kind"`
	Repos       []string           `json:"repos"`
	EgressAll   bool               `json:"egressAll"`
	Description string             `json:"description"`
	RotatedAt   *metav1.Time       `json:"rotatedAt"`
	RotatedBy   string             `json:"rotatedBy"`
}

// LoginInput is the body of PUT /v1/logins/{name}.
type LoginInput struct {
	Kind        v1alpha1.LoginKind `json:"kind"`
	Repos       []string           `json:"repos,omitempty"`
	EgressAll   bool               `json:"egressAll,omitempty"`
	Description string             `json:"description,omitempty"`
	Value       *string            `json:"value,omitempty"`
	ValueBase64 *string            `json:"valueBase64,omitempty"`
}

// RunInput is the body of POST /v1/runs.
type RunInput struct {
	Profile string `json:"profile"`
	Task    string `json:"task,omitempty"`
	Memory  *bool  `json:"memory,omitempty"`
}

// RunFilter selects runs for ListRuns.
type RunFilter struct {
	Profile string
	Phase   string
	Limit   int
}

func (s *Store) key(name string) client.ObjectKey {
	return client.ObjectKey{Namespace: s.Namespace, Name: name}
}

func (s *Store) env(ctx context.Context) (policy.Env, error) {
	env, err := controller.LoadEnv(ctx, s.C, s.Namespace, s.HarnessVersions)
	if err != nil {
		return env, fmt.Errorf("loading policy: %w", err)
	}
	return env, nil
}

func checkName(name string, maxLen int) error {
	errs := validation.IsDNS1035Label(name)
	if len(errs) == 0 && len(name) <= maxLen {
		return nil
	}
	return newAPIError(http.StatusUnprocessableEntity, "name", fmt.Sprintf("must be a DNS-1035 label of at most %d characters", maxLen))
}

func checkSubdomain(name string) error {
	if errs := validation.IsDNS1123Subdomain(name); len(errs) > 0 {
		return newAPIError(http.StatusUnprocessableEntity, "name", "must be a DNS-1123 subdomain")
	}
	return nil
}

// GetPolicy returns the policy spec.
func (s *Store) GetPolicy(ctx context.Context, id *Identity) (*v1alpha1.AgentPolicySpec, error) {
	if err := Require(id, ScopeRunsRead); err != nil {
		return nil, err
	}
	pol := &v1alpha1.AgentPolicy{}
	if err := s.C.Get(ctx, s.key(v1alpha1.PolicyName), pol); err != nil {
		return nil, fmt.Errorf("getting policy: %w", err)
	}
	return &pol.Spec, nil
}

func profileView(p *v1alpha1.AgentProfile) *ProfileView {
	return &ProfileView{Name: p.Name, Spec: p.Spec, Status: p.Status}
}

// ListProfiles returns all profiles, sorted by name.
func (s *Store) ListProfiles(ctx context.Context, id *Identity) ([]ProfileView, error) {
	if err := Require(id, ScopeRunsRead); err != nil {
		return nil, err
	}
	list := &v1alpha1.AgentProfileList{}
	if err := s.C.List(ctx, list, client.InNamespace(s.Namespace)); err != nil {
		return nil, fmt.Errorf("listing profiles: %w", err)
	}
	out := make([]ProfileView, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, *profileView(&list.Items[i]))
	}
	slices.SortFunc(out, func(a, b ProfileView) int { return cmp.Compare(a.Name, b.Name) })
	return out, nil
}

// GetProfile returns one profile.
func (s *Store) GetProfile(ctx context.Context, id *Identity, name string) (*ProfileView, error) {
	if err := Require(id, ScopeRunsRead); err != nil {
		return nil, err
	}
	p := &v1alpha1.AgentProfile{}
	if err := s.C.Get(ctx, s.key(name), p); err != nil {
		return nil, fmt.Errorf("getting profile: %w", err)
	}
	return profileView(p), nil
}

var errValidatedByOwner = newAPIError(http.StatusForbidden, "validated", "only POST /v1/profiles/{name}/validate sets validated")

// CreateProfile creates a profile. New profiles are never validated.
func (s *Store) CreateProfile(ctx context.Context, id *Identity, name string, spec v1alpha1.AgentProfileSpec) (*ProfileView, error) {
	if err := Require(id, ScopeProfilesWrite); err != nil {
		return nil, err
	}
	if err := checkName(name, maxProfileName); err != nil {
		return nil, err
	}
	if spec.Validated {
		return nil, errValidatedByOwner
	}
	env, err := s.env(ctx)
	if err != nil {
		return nil, err
	}
	if errs := policy.ValidateProfile(env, &spec); errs != nil {
		return nil, errs
	}
	p := &v1alpha1.AgentProfile{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   s.Namespace,
			Annotations: map[string]string{controller.AnnotationLastChangedBy: id.Name},
		},
		Spec: spec,
	}
	if err := s.C.Create(ctx, p); err != nil {
		return nil, fmt.Errorf("creating profile: %w", err)
	}
	return profileView(p), nil
}

// UpdateProfile replaces a profile spec. It keeps validated unless repos,
// github, egress or harness change, and a profiles:write token cannot set
// it.
func (s *Store) UpdateProfile(ctx context.Context, id *Identity, name string, spec v1alpha1.AgentProfileSpec) (*ProfileView, error) {
	if err := Require(id, ScopeProfilesWrite); err != nil {
		return nil, err
	}
	return s.changeProfile(ctx, id, name, func(p *v1alpha1.AgentProfile) error {
		if spec.Validated && !p.Spec.Validated {
			return errValidatedByOwner
		}
		spec.Validated = p.Spec.Validated && !policy.NeedsRevalidation(&p.Spec, &spec)
		p.Spec = spec
		return nil
	})
}

// SuspendProfile suspends or resumes the schedule of a profile.
func (s *Store) SuspendProfile(ctx context.Context, id *Identity, name string, suspend bool) (*ProfileView, error) {
	if err := Require(id, ScopeProfilesWrite); err != nil {
		return nil, err
	}
	return s.changeProfile(ctx, id, name, func(p *v1alpha1.AgentProfile) error {
		if p.Spec.Schedule == nil {
			return newAPIError(http.StatusUnprocessableEntity, "schedule", "profile has no schedule")
		}
		p.Spec.Schedule.Suspend = suspend
		return nil
	})
}

// ValidateProfile marks a profile as validated by the owner. It first
// records which harness profile (UID and generation) and GitHub login (UID)
// the owner validated, so a later replacement of either one does not keep
// the validation.
func (s *Store) ValidateProfile(ctx context.Context, id *Identity, name string) (*ProfileView, error) {
	if err := Require(id, ScopeProfilesValidate); err != nil {
		return nil, err
	}
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		p := &v1alpha1.AgentProfile{}
		if err := s.C.Get(ctx, s.key(name), p); err != nil {
			return fmt.Errorf("getting profile: %w", err)
		}
		h := &v1alpha1.HarnessProfile{}
		if err := s.C.Get(ctx, s.key(p.Spec.Harness), h); err != nil {
			if apierrors.IsNotFound(err) {
				return newAPIError(http.StatusUnprocessableEntity, "harness", "harness profile not found")
			}
			return fmt.Errorf("getting harness profile: %w", err)
		}
		gh := &v1alpha1.Login{}
		if err := s.C.Get(ctx, s.key(p.Spec.GitHub), gh); err != nil {
			if apierrors.IsNotFound(err) {
				return newAPIError(http.StatusUnprocessableEntity, "github", "login not found")
			}
			return fmt.Errorf("getting github login: %w", err)
		}
		p.Status.ValidatedFor = policy.BindingFor(h, gh)
		return s.C.Status().Update(ctx, p)
	})
	if err != nil {
		return nil, err
	}
	return s.changeProfile(ctx, id, name, func(p *v1alpha1.AgentProfile) error {
		p.Spec.Validated = true
		return nil
	})
}

// changeProfile applies mutate, checks the policy, records the previous
// spec in the history and updates the profile.
func (s *Store) changeProfile(ctx context.Context, id *Identity, name string, mutate func(*v1alpha1.AgentProfile) error) (*ProfileView, error) {
	env, err := s.env(ctx)
	if err != nil {
		return nil, err
	}
	var out *v1alpha1.AgentProfile
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		p := &v1alpha1.AgentProfile{}
		if err := s.C.Get(ctx, s.key(name), p); err != nil {
			return fmt.Errorf("getting profile: %w", err)
		}
		previous := p.DeepCopy()
		if err := mutate(p); err != nil {
			return err
		}
		if errs := policy.ValidateProfile(env, &p.Spec); errs != nil {
			return errs
		}
		if err := appendHistory(ctx, s.C, s.HistoryNamespace, previous, previous.Annotations[controller.AnnotationLastChangedBy]); err != nil {
			return err
		}
		if p.Annotations == nil {
			p.Annotations = map[string]string{}
		}
		p.Annotations[controller.AnnotationLastChangedBy] = id.Name
		if err := s.C.Update(ctx, p); err != nil {
			return err
		}
		out = p
		return nil
	})
	if err != nil {
		return nil, err
	}
	return profileView(out), nil
}

// DeleteProfile deletes a profile and its history. Queued runs of the
// profile are rejected by the controller; running runs continue.
func (s *Store) DeleteProfile(ctx context.Context, id *Identity, name string) error {
	if err := Require(id, ScopeProfilesWrite); err != nil {
		return err
	}
	p := &v1alpha1.AgentProfile{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: s.Namespace}}
	if err := s.C.Delete(ctx, p); err != nil {
		return fmt.Errorf("deleting profile: %w", err)
	}
	return deleteHistory(ctx, s.C, s.HistoryNamespace, name)
}

// ProfileHistory returns the previous versions of a profile, newest first.
func (s *Store) ProfileHistory(ctx context.Context, id *Identity, name string) ([]HistoryEntry, error) {
	if err := Require(id, ScopeRunsRead); err != nil {
		return nil, err
	}
	if err := s.C.Get(ctx, s.key(name), &v1alpha1.AgentProfile{}); err != nil {
		return nil, fmt.Errorf("getting profile: %w", err)
	}
	return readHistory(ctx, s.C, s.HistoryNamespace, name)
}

func harnessView(h *v1alpha1.HarnessProfile) *HarnessView {
	return &HarnessView{Name: h.Name, Spec: h.Spec, Status: h.Status}
}

// ListHarnesses returns all harness profiles, sorted by name.
func (s *Store) ListHarnesses(ctx context.Context, id *Identity) ([]HarnessView, error) {
	if err := Require(id, ScopeRunsRead); err != nil {
		return nil, err
	}
	list := &v1alpha1.HarnessProfileList{}
	if err := s.C.List(ctx, list, client.InNamespace(s.Namespace)); err != nil {
		return nil, fmt.Errorf("listing harness profiles: %w", err)
	}
	out := make([]HarnessView, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, *harnessView(&list.Items[i]))
	}
	slices.SortFunc(out, func(a, b HarnessView) int { return cmp.Compare(a.Name, b.Name) })
	return out, nil
}

// GetHarness returns one harness profile.
func (s *Store) GetHarness(ctx context.Context, id *Identity, name string) (*HarnessView, error) {
	if err := Require(id, ScopeRunsRead); err != nil {
		return nil, err
	}
	h := &v1alpha1.HarnessProfile{}
	if err := s.C.Get(ctx, s.key(name), h); err != nil {
		return nil, fmt.Errorf("getting harness profile: %w", err)
	}
	return harnessView(h), nil
}

// CreateHarness creates a harness profile.
func (s *Store) CreateHarness(ctx context.Context, id *Identity, name string, spec v1alpha1.HarnessProfileSpec) (*HarnessView, error) {
	if err := Require(id, ScopeProfilesWrite); err != nil {
		return nil, err
	}
	if err := checkSubdomain(name); err != nil {
		return nil, err
	}
	env, err := s.env(ctx)
	if err != nil {
		return nil, err
	}
	if errs := policy.ValidateHarness(env, &spec); errs != nil {
		return nil, errs
	}
	// Profiles that name this harness profile were validated against
	// another object, or against none.
	if err := s.resetProfilesUsing(ctx, id, name); err != nil {
		return nil, err
	}
	h := &v1alpha1.HarnessProfile{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: s.Namespace}, Spec: spec}
	if err := s.C.Create(ctx, h); err != nil {
		return nil, fmt.Errorf("creating harness profile: %w", err)
	}
	return harnessView(h), nil
}

// UpdateHarness replaces a harness profile spec. A change to the harness,
// version, endpoint, login or config resets validated on every profile that uses
// it.
func (s *Store) UpdateHarness(ctx context.Context, id *Identity, name string, spec v1alpha1.HarnessProfileSpec) (*HarnessView, error) {
	if err := Require(id, ScopeProfilesWrite); err != nil {
		return nil, err
	}
	env, err := s.env(ctx)
	if err != nil {
		return nil, err
	}
	if errs := policy.ValidateHarness(env, &spec); errs != nil {
		return nil, errs
	}
	current := &v1alpha1.HarnessProfile{}
	if err := s.C.Get(ctx, s.key(name), current); err != nil {
		return nil, fmt.Errorf("getting harness profile: %w", err)
	}
	// Reset before the change, so no profile is validated against the new
	// harness settings even for a moment.
	if policy.HarnessChangeResets(&current.Spec, &spec) {
		if err := s.resetProfilesUsing(ctx, id, name); err != nil {
			return nil, err
		}
	}
	var out *v1alpha1.HarnessProfile
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		h := &v1alpha1.HarnessProfile{}
		if err := s.C.Get(ctx, s.key(name), h); err != nil {
			return fmt.Errorf("getting harness profile: %w", err)
		}
		if h.UID != current.UID {
			return newAPIError(http.StatusConflict, "name", "the harness profile was replaced; retry")
		}
		if policy.HarnessChangeResets(&current.Spec, &h.Spec) {
			return newAPIError(http.StatusConflict, "spec", "the harness profile changed; retry")
		}
		h.Spec = spec
		if err := s.C.Update(ctx, h); err != nil {
			return err
		}
		out = h
		return nil
	})
	if err != nil {
		return nil, err
	}
	return harnessView(out), nil
}

func (s *Store) resetProfilesUsing(ctx context.Context, id *Identity, harness string) error {
	list := &v1alpha1.AgentProfileList{}
	if err := s.C.List(ctx, list, client.InNamespace(s.Namespace)); err != nil {
		return fmt.Errorf("listing profiles: %w", err)
	}
	var errs []error
	for _, p := range list.Items {
		if p.Spec.Harness != harness || !p.Spec.Validated {
			continue
		}
		err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			cur := &v1alpha1.AgentProfile{}
			if err := s.C.Get(ctx, s.key(p.Name), cur); err != nil {
				return err
			}
			if err := appendHistory(ctx, s.C, s.HistoryNamespace, cur.DeepCopy(), cur.Annotations[controller.AnnotationLastChangedBy]); err != nil {
				return err
			}
			cur.Spec.Validated = false
			if cur.Annotations == nil {
				cur.Annotations = map[string]string{}
			}
			cur.Annotations[controller.AnnotationLastChangedBy] = id.Name
			return s.C.Update(ctx, cur)
		})
		if err != nil {
			errs = append(errs, fmt.Errorf("resetting validated on %s: %w", p.Name, err))
		}
	}
	return errors.Join(errs...)
}

// DeleteHarness deletes a harness profile. Profiles that use it become
// invalid.
func (s *Store) DeleteHarness(ctx context.Context, id *Identity, name string) error {
	if err := Require(id, ScopeProfilesWrite); err != nil {
		return err
	}
	if err := s.resetProfilesUsing(ctx, id, name); err != nil {
		return err
	}
	h := &v1alpha1.HarnessProfile{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: s.Namespace}}
	if err := s.C.Delete(ctx, h); err != nil {
		return fmt.Errorf("deleting harness profile: %w", err)
	}
	return nil
}

func loginView(l *v1alpha1.Login) LoginView {
	repos := l.Spec.Repos
	if repos == nil {
		repos = []string{}
	}
	return LoginView{
		Name:        l.Name,
		Kind:        l.Spec.Kind,
		Repos:       repos,
		EgressAll:   l.Spec.EgressAll,
		Description: l.Spec.Description,
		RotatedAt:   l.Status.RotatedAt,
		RotatedBy:   l.Status.RotatedBy,
	}
}

// ListLogins returns login metadata, sorted by name. It never reads a
// Secret.
func (s *Store) ListLogins(ctx context.Context, id *Identity) ([]LoginView, error) {
	if err := Require(id, ScopeRunsRead); err != nil {
		return nil, err
	}
	list := &v1alpha1.LoginList{}
	if err := s.C.List(ctx, list, client.InNamespace(s.Namespace)); err != nil {
		return nil, fmt.Errorf("listing logins: %w", err)
	}
	out := make([]LoginView, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, loginView(&list.Items[i]))
	}
	slices.SortFunc(out, func(a, b LoginView) int { return cmp.Compare(a.Name, b.Name) })
	return out, nil
}

func (in *LoginInput) value() ([]byte, bool, error) {
	switch {
	case in.Value != nil && in.ValueBase64 != nil:
		return nil, false, newAPIError(http.StatusUnprocessableEntity, "value", "set value or valueBase64, not both")
	case in.Value != nil:
		return []byte(*in.Value), true, nil
	case in.ValueBase64 != nil:
		b, err := base64.StdEncoding.DecodeString(*in.ValueBase64)
		if err != nil {
			return nil, false, newAPIError(http.StatusUnprocessableEntity, "valueBase64", "must be standard base64")
		}
		return b, true, nil
	default:
		return nil, false, nil
	}
}

func (in *LoginInput) validate() error {
	var errs policy.Errors
	switch in.Kind {
	case v1alpha1.LoginGitHubToken, v1alpha1.LoginModelAPIKey, v1alpha1.LoginClaudeCredentials:
	default:
		errs = append(errs, policy.FieldError{Field: "kind", Reason: "unknown login kind",
			Allowed: []string{string(v1alpha1.LoginGitHubToken), string(v1alpha1.LoginModelAPIKey), string(v1alpha1.LoginClaudeCredentials)}})
	}
	if in.Kind != v1alpha1.LoginGitHubToken && (len(in.Repos) > 0 || in.EgressAll) {
		errs = append(errs, policy.FieldError{Field: "repos", Reason: "repos and egressAll apply only to github-token logins"})
	}
	for i, r := range in.Repos {
		if !loginRepoPattern.MatchString(r) {
			errs = append(errs, policy.FieldError{Field: fmt.Sprintf("repos[%d]", i), Reason: "must be owner/name or owner/*"})
		}
	}
	if len(in.Description) > 512 {
		errs = append(errs, policy.FieldError{Field: "description", Reason: "must have at most 512 characters"})
	}
	return errs.Err()
}

// PutLogin creates or updates a Login and, when the input has a value,
// writes its Secret. The Secret is written without reading it, and the
// value is never returned.
func (s *Store) PutLogin(ctx context.Context, id *Identity, name string, in LoginInput) (*LoginView, error) {
	if err := Require(id, ScopeLoginsWrite); err != nil {
		return nil, err
	}
	if err := checkSubdomain(name); err != nil {
		return nil, err
	}
	if err := in.validate(); err != nil {
		return nil, err
	}
	value, hasValue, err := in.value()
	if err != nil {
		return nil, err
	}
	if hasValue && (len(value) == 0 || len(value) > maxLoginValue) {
		return nil, newAPIError(http.StatusUnprocessableEntity, "value", fmt.Sprintf("must have 1 to %d bytes", maxLoginValue))
	}

	login := &v1alpha1.Login{}
	err = s.C.Get(ctx, s.key(name), login)
	exists := err == nil
	if err != nil && !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("getting login: %w", err)
	}
	if !hasValue && (!exists || login.Spec.Kind != in.Kind) {
		return nil, newAPIError(http.StatusUnprocessableEntity, "value", "a value is required for a new login or a kind change")
	}

	if hasValue {
		if err := s.writeSecret(ctx, name, in.Kind, value); err != nil {
			return nil, err
		}
	}

	spec := v1alpha1.LoginSpec{Kind: in.Kind, Repos: in.Repos, EgressAll: in.EgressAll, Description: in.Description}
	if exists {
		login.Spec = spec
		if err := s.C.Update(ctx, login); err != nil {
			return nil, fmt.Errorf("updating login: %w", err)
		}
	} else {
		login = &v1alpha1.Login{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: s.Namespace}, Spec: spec}
		if err := s.C.Create(ctx, login); err != nil {
			return nil, fmt.Errorf("creating login: %w", err)
		}
	}
	if hasValue {
		now := metav1.NewTime(s.clock())
		login.Status = v1alpha1.LoginStatus{RotatedAt: &now, RotatedBy: id.Name}
		if err := s.C.Status().Update(ctx, login); err != nil {
			return nil, fmt.Errorf("recording rotation: %w", err)
		}
	}
	v := loginView(login)
	return &v, nil
}

// writeSecret replaces the login Secret without reading it: an update with
// no resourceVersion, then a create when it does not exist.
func (s *Store) writeSecret(ctx context.Context, name string, kind v1alpha1.LoginKind, value []byte) error {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: s.LoginsNamespace,
			Labels:    map[string]string{"agents.mudler.dev/login": name},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{kind.SecretKey(): value},
	}
	err := s.C.Update(ctx, secret)
	if apierrors.IsNotFound(err) {
		err = s.C.Create(ctx, secret)
	}
	if err != nil {
		// Never wrap the API error: it may echo the object.
		return errors.New("writing login secret failed")
	}
	return nil
}

// DeleteLogin deletes a Login and its Secret.
func (s *Store) DeleteLogin(ctx context.Context, id *Identity, name string) error {
	if err := Require(id, ScopeLoginsWrite); err != nil {
		return err
	}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: s.LoginsNamespace}}
	if err := s.C.Delete(ctx, secret); err != nil && !apierrors.IsNotFound(err) {
		return errors.New("deleting login secret failed")
	}
	login := &v1alpha1.Login{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: s.Namespace}}
	if err := s.C.Delete(ctx, login); err != nil {
		return fmt.Errorf("deleting login: %w", err)
	}
	return nil
}

func runView(r *v1alpha1.AgentRun) *RunView {
	return &RunView{Name: r.Name, Spec: r.Spec, Status: r.Status}
}

// ListRuns returns runs, newest first.
func (s *Store) ListRuns(ctx context.Context, id *Identity, f RunFilter) ([]RunView, error) {
	if err := Require(id, ScopeRunsRead); err != nil {
		return nil, err
	}
	limit := f.Limit
	switch {
	case limit <= 0:
		limit = defaultRunLimit
	case limit > maxRunLimit:
		limit = maxRunLimit
	}
	opts := []client.ListOption{client.InNamespace(s.Namespace)}
	if f.Profile != "" {
		opts = append(opts, client.MatchingLabels{controller.LabelProfile: f.Profile})
	}
	list := &v1alpha1.AgentRunList{}
	if err := s.C.List(ctx, list, opts...); err != nil {
		return nil, fmt.Errorf("listing runs: %w", err)
	}
	items := list.Items
	slices.SortFunc(items, func(a, b v1alpha1.AgentRun) int {
		return cmp.Or(b.CreationTimestamp.Compare(a.CreationTimestamp.Time), cmp.Compare(b.Name, a.Name))
	})
	out := []RunView{}
	for i := range items {
		r := &items[i]
		if f.Profile != "" && r.Spec.Profile != f.Profile {
			continue
		}
		if f.Phase != "" && string(r.Status.Phase) != f.Phase {
			continue
		}
		out = append(out, *runView(r))
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

// GetRun returns one run.
func (s *Store) GetRun(ctx context.Context, id *Identity, name string) (*RunView, error) {
	if err := Require(id, ScopeRunsRead); err != nil {
		return nil, err
	}
	run := &v1alpha1.AgentRun{}
	if err := s.C.Get(ctx, s.key(name), run); err != nil {
		return nil, fmt.Errorf("getting run: %w", err)
	}
	return runView(run), nil
}

// CreateRun starts a run of a profile. requestedBy is the token name. A busy
// profile is not an error: the run comes back Queued. It waits briefly for
// the controller to set a phase.
func (s *Store) CreateRun(ctx context.Context, id *Identity, in RunInput) (*RunView, error) {
	if err := Require(id, ScopeRunsWrite); err != nil {
		return nil, err
	}
	if err := checkName(in.Profile, maxProfileName); err != nil {
		return nil, newAPIError(http.StatusUnprocessableEntity, "profile", "must name a profile")
	}
	prof := &v1alpha1.AgentProfile{}
	var profSpec *v1alpha1.AgentProfileSpec
	if err := s.C.Get(ctx, s.key(in.Profile), prof); err == nil {
		profSpec = &prof.Spec
	} else if !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("getting profile: %w", err)
	}
	spec := v1alpha1.AgentRunSpec{
		Profile:     in.Profile,
		Task:        in.Task,
		Memory:      in.Memory,
		Trigger:     v1alpha1.TriggerManual,
		RequestedBy: id.Name,
	}
	if errs := policy.ValidateRun(policy.Env{}, profSpec, &spec); errs != nil {
		return nil, errs
	}
	run := &v1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: in.Profile + "-",
			Namespace:    s.Namespace,
			Labels:       map[string]string{controller.LabelProfile: in.Profile},
		},
		Spec: spec,
	}
	if err := s.C.Create(ctx, run); err != nil {
		return nil, fmt.Errorf("creating run: %w", err)
	}

	wait := cmp.Or(s.CreateRunWait, createRunWait)
	deadline := time.Now().Add(wait)
	for run.Status.Phase == "" && time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return runView(run), nil
		case <-time.After(100 * time.Millisecond):
		}
		cur := &v1alpha1.AgentRun{}
		if err := s.C.Get(ctx, client.ObjectKeyFromObject(run), cur); err == nil {
			run = cur
		}
	}
	return runView(run), nil
}

// CancelRun sets cancel on a run. A finished run is returned unchanged.
func (s *Store) CancelRun(ctx context.Context, id *Identity, name string) (*RunView, error) {
	if err := Require(id, ScopeRunsWrite); err != nil {
		return nil, err
	}
	var out *v1alpha1.AgentRun
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		run := &v1alpha1.AgentRun{}
		if err := s.C.Get(ctx, s.key(name), run); err != nil {
			return fmt.Errorf("getting run: %w", err)
		}
		out = run
		if run.Spec.Cancel || run.Status.Phase.Final() {
			return nil
		}
		run.Spec.Cancel = true
		return s.C.Update(ctx, run)
	})
	if err != nil {
		return nil, err
	}
	return runView(out), nil
}

package controller

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/mudler/agent-controller/api/v1alpha1"
	"github.com/mudler/agent-controller/internal/policy"
)

// RunSpecKey is the ConfigMap data key that holds the resolved run.
const RunSpecKey = "run.json"

// Config holds the controller settings from flags.
type Config struct {
	Namespace       string
	LoginsNamespace string
	// MemoryClaimTemplate is a printf template for the memory PVC name.
	MemoryClaimTemplate string
	// MemoryStorageClass is the storage class of new memory PVCs. Empty
	// selects the cluster default.
	MemoryStorageClass string
	// LauncherLockTemplate is a printf template for an extra Lease that the
	// launcher takes. Empty means none.
	LauncherLockTemplate string
	ProxyIdleSeconds     int32
	JobTemplatePath      string
	HarnessVersions      map[v1alpha1.HarnessKind][]string
	// RetainPerProfile is how many finished runs to keep per profile.
	RetainPerProfile int
}

// RunSpec is the resolved run that the launcher reads from RUN_SPEC. The
// field set and JSON names are a contract with the launcher.
type RunSpec struct {
	APIVersion     string         `json:"apiVersion"`
	Kind           string         `json:"kind"`
	Run            string         `json:"run"`
	Namespace      string         `json:"namespace"`
	Profile        string         `json:"profile"`
	ProfileVersion int64          `json:"profileVersion"`
	Trigger        string         `json:"trigger"`
	RequestedBy    string         `json:"requestedBy"`
	Repo           RunSpecRepo    `json:"repo"`
	Prompt         string         `json:"prompt"`
	Harness        RunSpecHarness `json:"harness"`
	Logins         RunSpecLogins  `json:"logins"`
	Egress         RunSpecEgress  `json:"egress"`
	Budget         RunSpecBudget  `json:"budget"`
	Resources      RunSpecRes     `json:"resources"`
	Memory         RunSpecMemory  `json:"memory"`
	Proxy          RunSpecProxy   `json:"proxy"`
}

// RunSpecRepo is the repository of a run.
type RunSpecRepo struct {
	Slug     string `json:"slug"`
	Branch   string `json:"branch"`
	Checkout string `json:"checkout"`
}

// RunSpecHarness is the resolved harness profile.
type RunSpecHarness struct {
	Name     string          `json:"name"`
	Kind     string          `json:"kind"`
	Version  string          `json:"version"`
	Model    string          `json:"model"`
	Endpoint string          `json:"endpoint"`
	Config   json.RawMessage `json:"config"`
}

// RunSpecLogin names a Login. It never holds a value.
type RunSpecLogin struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
}

// RunSpecLogins holds the logins of a run.
type RunSpecLogins struct {
	GitHub RunSpecLogin `json:"github"`
	Model  RunSpecLogin `json:"model"`
}

// RunSpecEgress is the egress of a run.
type RunSpecEgress struct {
	Mode  string   `json:"mode"`
	Hosts []string `json:"hosts"`
}

// RunSpecBudget is the budget of a run.
type RunSpecBudget struct {
	SessionSeconds int32 `json:"sessionSeconds"`
	Iterations     int32 `json:"iterations"`
}

// RunSpecRes is the sandbox size of a run.
type RunSpecRes struct {
	CPU       string `json:"cpu"`
	Memory    string `json:"memory"`
	Workspace string `json:"workspace"`
	Tmp       string `json:"tmp"`
}

// RunSpecMemory is the memory volume and locks of a run.
type RunSpecMemory struct {
	Enabled      bool   `json:"enabled"`
	Claim        string `json:"claim"`
	Lock         string `json:"lock"`
	LauncherLock string `json:"launcherLock"`
}

// RunSpecProxy configures the run proxy.
type RunSpecProxy struct {
	IdleSeconds int32 `json:"idleSeconds"`
}

// ComposePrompt appends the run task to the profile prompt.
func ComposePrompt(profilePrompt, task string) string {
	if task == "" {
		return profilePrompt
	}
	return profilePrompt + "\n\nTask for this run:\n" + task
}

// UsesMemory reports whether a run mounts the profile memory.
func UsesMemory(prof *v1alpha1.AgentProfileSpec, run *v1alpha1.AgentRunSpec) bool {
	return prof.Memory == v1alpha1.MemoryEnabled && (run.Memory == nil || *run.Memory)
}

// MemoryClaimName returns the memory PVC name of a profile.
func (c Config) MemoryClaimName(profile string) string {
	return fmt.Sprintf(c.MemoryClaimTemplate, profile)
}

// Resolve builds the resolved run from the run, its profile, its harness
// profile and the two logins.
func Resolve(
	cfg Config,
	run *v1alpha1.AgentRun,
	prof *v1alpha1.AgentProfile,
	h *v1alpha1.HarnessProfile,
	githubLogin, modelLogin *v1alpha1.Login,
) (*RunSpec, error) {
	if len(prof.Spec.Repos) != 1 {
		return nil, errors.New("profile must have exactly one repository")
	}
	if githubLogin == nil || githubLogin.Spec.Kind != v1alpha1.LoginGitHubToken {
		return nil, errors.New("github login missing or not a github-token")
	}
	if modelLogin == nil || modelLogin.Name != h.Spec.Login {
		return nil, errors.New("model login missing or not the harness login")
	}
	repo := prof.Spec.Repos[0]
	_, name, _ := strings.Cut(repo.Slug, "/")

	config := json.RawMessage(`{}`)
	if h.Spec.Config != nil && len(h.Spec.Config.Raw) > 0 {
		config = json.RawMessage(h.Spec.Config.Raw)
	}

	mem := RunSpecMemory{}
	if UsesMemory(&prof.Spec, &run.Spec) {
		mem = RunSpecMemory{
			Enabled: true,
			Claim:   cfg.MemoryClaimName(prof.Name),
			Lock:    MemoryLockName(prof.Name),
		}
		if cfg.LauncherLockTemplate != "" {
			mem.LauncherLock = fmt.Sprintf(cfg.LauncherLockTemplate, prof.Name)
		}
	}

	res := prof.Spec.Resources
	return &RunSpec{
		APIVersion:     v1alpha1.GroupVersion.String(),
		Kind:           "ResolvedRun",
		Run:            run.Name,
		Namespace:      run.Namespace,
		Profile:        prof.Name,
		ProfileVersion: prof.Generation,
		Trigger:        string(run.Spec.Trigger),
		RequestedBy:    run.Spec.RequestedBy,
		Repo:           RunSpecRepo{Slug: repo.Slug, Branch: repo.Branch, Checkout: "/workspace/" + name},
		Prompt:         ComposePrompt(prof.Spec.Prompt, run.Spec.Task),
		Harness: RunSpecHarness{
			Name:     h.Name,
			Kind:     string(h.Spec.Harness),
			Version:  h.Spec.Version,
			Model:    h.Spec.Model,
			Endpoint: h.Spec.Endpoint,
			Config:   config,
		},
		Logins: RunSpecLogins{
			GitHub: RunSpecLogin{Name: githubLogin.Name, Kind: string(githubLogin.Spec.Kind)},
			Model:  RunSpecLogin{Name: modelLogin.Name, Kind: string(modelLogin.Spec.Kind)},
		},
		Egress: RunSpecEgress{Mode: string(prof.Spec.Egress.Mode), Hosts: policy.RunHosts(&prof.Spec, &h.Spec)},
		Budget: RunSpecBudget{SessionSeconds: prof.Spec.Budget.SessionSeconds, Iterations: prof.Spec.Budget.Iterations},
		Resources: RunSpecRes{
			CPU:       res.CPU.String(),
			Memory:    res.Memory.String(),
			Workspace: res.Workspace.String(),
			Tmp:       res.Tmp.String(),
		},
		Memory: mem,
		Proxy:  RunSpecProxy{IdleSeconds: cfg.ProxyIdleSeconds},
	}, nil
}

// JSON returns the indented JSON form of the resolved run.
func (r *RunSpec) JSON() ([]byte, error) {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encoding resolved run: %w", err)
	}
	return b, nil
}

// ConfigMapName returns the name of the resolved run ConfigMap.
func ConfigMapName(run string) string {
	return run + "-spec"
}

// ConfigMap returns the ConfigMap <run>-spec that holds the resolved run,
// owned by the AgentRun.
func (r *RunSpec) ConfigMap(owner *v1alpha1.AgentRun) *corev1.ConfigMap {
	// Marshalling plain strings, numbers and a valid RawMessage cannot fail.
	data, err := r.JSON()
	if err != nil {
		panic(err)
	}
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:            ConfigMapName(owner.Name),
			Namespace:       owner.Namespace,
			Labels:          map[string]string{LabelRun: owner.Name},
			OwnerReferences: []metav1.OwnerReference{runOwnerRef(owner)},
		},
		Data: map[string]string{RunSpecKey: string(data)},
	}
}

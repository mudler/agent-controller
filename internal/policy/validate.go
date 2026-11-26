package policy

import (
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/mudler/agent-controller/api/v1alpha1"
)

// MaxPromptLength is the maximum length of a profile prompt.
const MaxPromptLength = 32768

var (
	slugPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	hostPattern = regexp.MustCompile(`^[a-z0-9.-]+:443$`)

	// CronParser parses the standard five-field cron format.
	CronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
)

// Env holds everything a check needs.
type Env struct {
	Policy          *v1alpha1.AgentPolicy
	Logins          map[string]*v1alpha1.Login
	Harnesses       map[string]*v1alpha1.HarnessProfile
	HarnessVersions map[v1alpha1.HarnessKind][]string
}

func harnessKinds(p *v1alpha1.AgentPolicy) []string {
	out := make([]string, 0, len(p.Spec.Harnesses))
	for _, h := range p.Spec.Harnesses {
		out = append(out, string(h))
	}
	return out
}

// ValidateHarness checks a harness profile against the policy and logins.
func ValidateHarness(env Env, h *v1alpha1.HarnessProfileSpec) Errors {
	var errs Errors
	if env.Policy == nil {
		errs.add("policy", "no AgentPolicy named default")
		return errs
	}
	pol := env.Policy.Spec

	if !slices.Contains(pol.Harnesses, h.Harness) {
		errs.add("harness", "not in policy", harnessKinds(env.Policy)...)
	}
	versions := env.HarnessVersions[h.Harness]
	if !slices.Contains(versions, h.Version) {
		errs.add("version", "not installed in the runner image", versions...)
	}
	if strings.TrimSpace(h.Model) == "" {
		errs.add("model", "must not be empty")
	}
	if !slices.Contains(pol.ModelEndpoints, h.Endpoint) {
		errs.add("endpoint", "not in policy", pol.ModelEndpoints...)
	} else if _, err := EndpointHost(h.Endpoint); err != nil {
		errs.add("endpoint", err.Error())
	}

	login, ok := env.Logins[h.Login]
	switch {
	case h.Login == "":
		errs.add("login", "must not be empty")
	case !ok:
		errs.add("login", "login not found")
	case login.Spec.Kind == v1alpha1.LoginModelAPIKey:
	case login.Spec.Kind == v1alpha1.LoginClaudeCredentials && h.Harness == v1alpha1.HarnessClaudeCode:
	default:
		allowed := []string{string(v1alpha1.LoginModelAPIKey)}
		if h.Harness == v1alpha1.HarnessClaudeCode {
			allowed = append(allowed, string(v1alpha1.LoginClaudeCredentials))
		}
		errs.add("login", fmt.Sprintf("login kind %s cannot serve harness %s", login.Spec.Kind, h.Harness), allowed...)
	}

	errs = append(errs, ValidateHarnessConfig(h.Harness, h.Config)...)
	return errs.orNil()
}

// ValidateProfile checks a profile, including its harness profile, against
// the policy and logins.
func ValidateProfile(env Env, p *v1alpha1.AgentProfileSpec) Errors {
	var errs Errors
	if env.Policy == nil {
		errs.add("policy", "no AgentPolicy named default")
		return errs
	}
	pol := env.Policy.Spec

	harness, ok := env.Harnesses[p.Harness]
	switch {
	case p.Harness == "":
		errs.add("harness", "must not be empty")
	case !ok:
		errs.add("harness", "harness profile not found")
	default:
		if herrs := ValidateHarness(env, &harness.Spec); herrs != nil {
			errs.add("harness", "harness profile is invalid: "+herrs.Error())
		}
	}

	if strings.TrimSpace(p.Prompt) == "" {
		errs.add("prompt", "must not be empty")
	} else if len(p.Prompt) > MaxPromptLength {
		errs.add("prompt", fmt.Sprintf("must have at most %d characters", MaxPromptLength))
	}

	errs = append(errs, validateRepos(env, p)...)
	errs = append(errs, validateEgress(env, p, harness)...)
	errs = append(errs, validateBudget(pol, p)...)
	errs = append(errs, validateResources(pol, p)...)

	if p.MaxParallel < 1 || p.MaxParallel > pol.Limits.MaxParallelPerProfile {
		errs.add("maxParallel", fmt.Sprintf("must be between 1 and %d", pol.Limits.MaxParallelPerProfile))
	}
	if p.Memory != v1alpha1.MemoryEnabled && p.Memory != v1alpha1.MemoryDisabled {
		errs.add("memory", "must be enabled or disabled", string(v1alpha1.MemoryEnabled), string(v1alpha1.MemoryDisabled))
	}
	if p.Schedule != nil {
		errs = append(errs, validateSchedule(p.Schedule)...)
	}
	return errs.orNil()
}

func validateRepos(env Env, p *v1alpha1.AgentProfileSpec) Errors {
	var errs Errors
	if len(p.Repos) != 1 {
		errs.add("repos", "must have exactly one repository")
	}
	for i, r := range p.Repos {
		if !ValidSlug(r.Slug) {
			errs.add(fmt.Sprintf("repos[%d].slug", i), "must be owner/name")
		}
		if strings.TrimSpace(r.Branch) == "" {
			errs.add(fmt.Sprintf("repos[%d].branch", i), "must not be empty")
		}
	}

	login, ok := env.Logins[p.GitHub]
	switch {
	case p.GitHub == "":
		errs.add("github", "must not be empty")
	case !ok:
		errs.add("github", "login not found")
	case login.Spec.Kind != v1alpha1.LoginGitHubToken:
		errs.add("github", fmt.Sprintf("login kind %s is not github-token", login.Spec.Kind), string(v1alpha1.LoginGitHubToken))
	default:
		for i, r := range p.Repos {
			if !RepoAllowed(login, r.Slug) {
				errs.add(fmt.Sprintf("repos[%d].slug", i), "not allowed by login "+login.Name, login.Spec.Repos...)
			}
		}
	}
	return errs
}

func validateEgress(env Env, p *v1alpha1.AgentProfileSpec, harness *v1alpha1.HarnessProfile) Errors {
	var errs Errors
	pol := env.Policy.Spec
	switch p.Egress.Mode {
	case v1alpha1.EgressAll:
		if !pol.Egress.AllowAll {
			errs.add("egress.mode", "policy does not allow egress all", string(v1alpha1.EgressAllowlist))
		}
		if login, ok := env.Logins[p.GitHub]; ok && !login.Spec.EgressAll {
			errs.add("egress.mode", "login "+login.Name+" does not allow egress all", string(v1alpha1.EgressAllowlist))
		}
		if len(p.Egress.ExtraHosts) > 0 {
			errs.add("egress.extraHosts", "only allowed in allowlist mode")
		}
	case v1alpha1.EgressAllowlist:
		for i, host := range p.Egress.ExtraHosts {
			field := fmt.Sprintf("egress.extraHosts[%d]", i)
			if !hostPattern.MatchString(host) {
				errs.add(field, "must be host:443")
			} else if !slices.Contains(pol.Egress.Hosts, host) {
				errs.add(field, "not in policy", pol.Egress.Hosts...)
			}
		}
		if harness != nil {
			if host, err := EndpointHost(harness.Spec.Endpoint); err == nil && !slices.Contains(pol.Egress.Hosts, host) {
				errs.add("harness", "endpoint host "+host+" not in policy egress hosts", pol.Egress.Hosts...)
			}
		}
	default:
		errs.add("egress.mode", "must be allowlist or all", string(v1alpha1.EgressAllowlist), string(v1alpha1.EgressAll))
	}
	return errs
}

func validateBudget(pol v1alpha1.AgentPolicySpec, p *v1alpha1.AgentProfileSpec) Errors {
	var errs Errors
	if p.Budget.SessionSeconds < 1 || p.Budget.SessionSeconds > pol.Limits.SessionSeconds {
		errs.add("budget.sessionSeconds", fmt.Sprintf("must be between 1 and %d", pol.Limits.SessionSeconds))
	}
	if p.Budget.Iterations < 1 {
		errs.add("budget.iterations", "must be at least 1")
	}
	return errs
}

func validateResources(pol v1alpha1.AgentPolicySpec, p *v1alpha1.AgentProfileSpec) Errors {
	var errs Errors
	check := func(field string, q, limit resource.Quantity) {
		switch {
		case q.Sign() <= 0:
			errs.add(field, "must be greater than zero")
		case q.Cmp(limit) > 0:
			errs.add(field, "exceeds policy limit "+limit.String())
		}
	}
	check("resources.cpu", p.Resources.CPU, pol.Limits.CPU)
	check("resources.memory", p.Resources.Memory, pol.Limits.Memory)
	check("resources.workspace", p.Resources.Workspace, pol.Limits.Workspace)
	check("resources.tmp", p.Resources.Tmp, pol.Limits.Workspace)
	return errs
}

func validateSchedule(s *v1alpha1.Schedule) Errors {
	var errs Errors
	if _, err := CronParser.Parse(s.Cron); err != nil {
		errs.add("schedule.cron", "invalid cron expression: "+err.Error())
	}
	if _, err := LoadLocation(s.TimeZone); err != nil {
		errs.add("schedule.timeZone", "unknown time zone")
	}
	return errs
}

// LoadLocation loads an IANA time zone. An empty name is UTC.
func LoadLocation(name string) (*time.Location, error) {
	if name == "" {
		return time.UTC, nil
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("loading time zone: %w", err)
	}
	return loc, nil
}

// ValidateRun checks the run-level fields against the profile. Profile
// validity is checked by the controller at admission.
func ValidateRun(env Env, p *v1alpha1.AgentProfileSpec, r *v1alpha1.AgentRunSpec) Errors {
	_ = env
	var errs Errors
	if r.Profile == "" {
		errs.add("profile", "must not be empty")
	}
	if len(r.Task) > v1alpha1.MaxTaskLength {
		errs.add("task", fmt.Sprintf("must have at most %d characters", v1alpha1.MaxTaskLength))
	}
	if p != nil && r.Memory != nil && *r.Memory && p.Memory == v1alpha1.MemoryDisabled {
		errs.add("memory", "memory is disabled for profile "+r.Profile, "false")
	}
	return errs.orNil()
}

// ValidSlug reports whether slug is "owner/name" with neither part "." or
// "..".
func ValidSlug(slug string) bool {
	if !slugPattern.MatchString(slug) {
		return false
	}
	owner, name, _ := strings.Cut(slug, "/")
	dots := func(s string) bool { return s == "." || s == ".." }
	return !dots(owner) && !dots(name)
}

// EndpointHost returns the host:443 authority of an https endpoint.
func EndpointHost(endpoint string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" {
		return "", fmt.Errorf("must be an https URL")
	}
	if port := u.Port(); port != "" && port != strconv.Itoa(443) {
		return "", fmt.Errorf("must use port 443")
	}
	return strings.ToLower(u.Hostname()) + ":443", nil
}

// RunHosts returns the egress hosts of a run in allowlist mode: the harness
// endpoint host plus the profile's extra hosts, sorted and deduplicated. In
// mode "all" it returns an empty list.
func RunHosts(p *v1alpha1.AgentProfileSpec, h *v1alpha1.HarnessProfileSpec) []string {
	hosts := []string{}
	if p.Egress.Mode == v1alpha1.EgressAll {
		return hosts
	}
	if host, err := EndpointHost(h.Endpoint); err == nil {
		hosts = append(hosts, host)
	}
	hosts = append(hosts, p.Egress.ExtraHosts...)
	slices.Sort(hosts)
	return slices.Compact(hosts)
}

// RepoAllowed reports whether a github-token login may serve the repository.
// A pattern matches exactly or as "owner/*".
func RepoAllowed(login *v1alpha1.Login, slug string) bool {
	owner, _, ok := strings.Cut(slug, "/")
	if !ok {
		return false
	}
	for _, pattern := range login.Spec.Repos {
		if pattern == slug || pattern == owner+"/*" {
			return true
		}
	}
	return false
}

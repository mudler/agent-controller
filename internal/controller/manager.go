package controller

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"
	"strings"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/utils/clock"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/mudler/agent-controller/api/v1alpha1"
	"github.com/mudler/agent-controller/internal/scheme"
)

// ManagerOptions holds the manager settings from flags.
type ManagerOptions struct {
	MetricsBindAddress     string
	HealthProbeBindAddress string
	LeaderElect            bool
	// Logs overrides the launcher log reader. Nil reads from the API server.
	Logs LogReader
}

// ParseControllerFlags parses the flags of the controller role.
func ParseControllerFlags(args []string) (Config, ManagerOptions, error) {
	var cfg Config
	var opts ManagerOptions
	var versions string
	var retain int

	fs := flag.NewFlagSet("controller", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&cfg.Namespace, "namespace", "agents", "namespace of the agent objects and runs")
	fs.StringVar(&cfg.LoginsNamespace, "logins-namespace", "agent-logins", "namespace of the login Secrets")
	fs.StringVar(&cfg.JobTemplatePath, "job-template", "/etc/agent-controller/job-template.yaml", "launcher Job template file")
	fs.StringVar(&cfg.MemoryClaimTemplate, "memory-claim-template", "%s-memory", "printf template of the memory PVC name")
	fs.StringVar(&cfg.MemoryStorageClass, "memory-storage-class", "", "storage class of new memory PVCs (empty: cluster default)")
	fs.StringVar(&cfg.LauncherLockTemplate, "launcher-lock-template", "", "printf template of an extra Lease the launcher takes (empty: none)")
	fs.StringVar(&versions, "harness-versions", "", "installed harness versions, as kind=version[,kind=version]")
	var idle int
	fs.IntVar(&idle, "proxy-idle-seconds", 600, "proxy idle timeout of a run")
	fs.IntVar(&retain, "retain-per-profile", DefaultRetainPerProfile, "finished runs kept per profile")
	fs.StringVar(&opts.MetricsBindAddress, "metrics-bind-address", ":8080", "metrics address (0 disables)")
	fs.StringVar(&opts.HealthProbeBindAddress, "health-probe-bind-address", ":8081", "health probe address")
	fs.BoolVar(&opts.LeaderElect, "leader-elect", false, "enable leader election")
	if err := fs.Parse(args); err != nil {
		return cfg, opts, fmt.Errorf("parsing controller flags: %w", err)
	}

	hv, err := ParseHarnessVersions(versions)
	if err != nil {
		return cfg, opts, err
	}
	cfg.HarnessVersions = hv
	if idle < 1 {
		return cfg, opts, errors.New("--proxy-idle-seconds must be positive")
	}
	cfg.ProxyIdleSeconds = int32(min(idle, 1<<30))
	if retain < 1 {
		return cfg, opts, errors.New("--retain-per-profile must be positive")
	}
	cfg.RetainPerProfile = retain
	if strings.Count(cfg.MemoryClaimTemplate, "%s") != 1 {
		return cfg, opts, errors.New("--memory-claim-template must contain one %s")
	}
	if cfg.LauncherLockTemplate != "" && strings.Count(cfg.LauncherLockTemplate, "%s") != 1 {
		return cfg, opts, errors.New("--launcher-lock-template must contain one %s")
	}
	return cfg, opts, nil
}

// ParseHarnessVersions parses "kind=version[,kind=version]". A kind may
// repeat to allow several versions.
func ParseHarnessVersions(s string) (map[v1alpha1.HarnessKind][]string, error) {
	out := map[v1alpha1.HarnessKind][]string{}
	if strings.TrimSpace(s) == "" {
		return nil, errors.New("--harness-versions must name at least one harness")
	}
	for _, part := range strings.Split(s, ",") {
		kind, version, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok || version == "" {
			return nil, fmt.Errorf("--harness-versions: %q is not kind=version", part)
		}
		k := v1alpha1.HarnessKind(kind)
		if k != v1alpha1.HarnessNib && k != v1alpha1.HarnessClaudeCode {
			return nil, fmt.Errorf("--harness-versions: unknown harness kind %q", kind)
		}
		if !slices.Contains(out[k], version) {
			out[k] = append(out[k], version)
		}
	}
	return out, nil
}

// NewManager builds the controller manager with the profile, harness, queue
// and run reconcilers. The cache is restricted to cfg.Namespace.
func NewManager(restCfg *rest.Config, cfg Config, opts ManagerOptions) (ctrl.Manager, error) {
	tmpl, err := LoadJobTemplate(cfg.JobTemplatePath)
	if err != nil {
		return nil, err
	}
	mgr, err := ctrl.NewManager(restCfg, ctrl.Options{
		Scheme:                  scheme.New(),
		Cache:                   cache.Options{DefaultNamespaces: map[string]cache.Config{cfg.Namespace: {}}},
		Metrics:                 metricsserver.Options{BindAddress: opts.MetricsBindAddress},
		HealthProbeBindAddress:  opts.HealthProbeBindAddress,
		LeaderElection:          opts.LeaderElect,
		LeaderElectionID:        "agent-controller.agents.mudler.dev",
		LeaderElectionNamespace: cfg.Namespace,
	})
	if err != nil {
		return nil, fmt.Errorf("creating manager: %w", err)
	}
	logs := opts.Logs
	if logs == nil {
		cs, err := kubernetes.NewForConfig(restCfg)
		if err != nil {
			return nil, fmt.Errorf("creating clientset: %w", err)
		}
		logs = NewKubeLogReader(cs)
	}
	clk := clock.RealClock{}
	setups := []interface{ SetupWithManager(ctrl.Manager) error }{
		&ProfileReconciler{Client: mgr.GetClient(), Reader: mgr.GetAPIReader(), Cfg: cfg, Clock: clk},
		&HarnessReconciler{Client: mgr.GetClient(), Cfg: cfg},
		&QueueReconciler{Client: mgr.GetClient(), Reader: mgr.GetAPIReader(), Cfg: cfg, JobTemplate: tmpl},
		&RunReconciler{Client: mgr.GetClient(), Logs: logs, Cfg: cfg, Clock: clk},
	}
	for _, s := range setups {
		if err := s.SetupWithManager(mgr); err != nil {
			return nil, fmt.Errorf("registering reconciler: %w", err)
		}
	}
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return nil, fmt.Errorf("adding health check: %w", err)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return nil, fmt.Errorf("adding ready check: %w", err)
	}
	return mgr, nil
}

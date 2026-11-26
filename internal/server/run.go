package server

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"time"

	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mudler/agent-controller/internal/controller"
	"github.com/mudler/agent-controller/internal/scheme"
)

const (
	// tokenReload is how often the tokens Secret is read again at most.
	tokenReload    = 30 * time.Second
	shutdownPeriod = 15 * time.Second
)

// ServerConfig holds the server settings from flags.
type ServerConfig struct {
	Namespace        string
	LoginsNamespace  string
	HistoryNamespace string
	Listen           string
	TokensSecret     string
	HarnessVersions  string
}

// ParseServerFlags parses the flags of the server role.
func ParseServerFlags(args []string) (ServerConfig, error) {
	var cfg ServerConfig
	fs := flag.NewFlagSet("server", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&cfg.Namespace, "namespace", "agents", "namespace of the agent objects and runs")
	fs.StringVar(&cfg.LoginsNamespace, "logins-namespace", "agent-logins", "namespace of the login Secrets")
	fs.StringVar(&cfg.HistoryNamespace, "history-namespace", DefaultHistoryNamespace, "namespace of the profile history ConfigMaps")
	fs.StringVar(&cfg.Listen, "listen", ":8080", "HTTP listen address")
	fs.StringVar(&cfg.TokensSecret, "tokens-secret", DefaultTokensSecret, "Secret in the agents namespace that holds tokens.json")
	fs.StringVar(&cfg.HarnessVersions, "harness-versions", "", "installed harness versions, as kind=version[,kind=version]")
	if err := fs.Parse(args); err != nil {
		return cfg, fmt.Errorf("parsing server flags: %w", err)
	}
	if _, err := controller.ParseHarnessVersions(cfg.HarnessVersions); err != nil {
		return cfg, err
	}
	if cfg.HistoryNamespace == cfg.Namespace {
		return cfg, errors.New("--history-namespace must differ from --namespace")
	}
	if cfg.LoginsNamespace == cfg.Namespace {
		return cfg, errors.New("--logins-namespace must differ from --namespace")
	}
	return cfg, nil
}

// RunServer serves the API until ctx ends, then shuts down gracefully.
func RunServer(ctx context.Context, args []string) error {
	cfg, err := ParseServerFlags(args)
	if err != nil {
		return err
	}
	versions, err := controller.ParseHarnessVersions(cfg.HarnessVersions)
	if err != nil {
		return err
	}
	restCfg, err := ctrl.GetConfig()
	if err != nil {
		return fmt.Errorf("loading kubeconfig: %w", err)
	}
	c, err := client.New(restCfg, client.Options{Scheme: scheme.New()})
	if err != nil {
		return fmt.Errorf("creating client: %w", err)
	}
	cs, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return fmt.Errorf("creating clientset: %w", err)
	}

	store := &Store{
		C:                c,
		Namespace:        cfg.Namespace,
		LoginsNamespace:  cfg.LoginsNamespace,
		HistoryNamespace: cfg.HistoryNamespace,
		HarnessVersions:  versions,
	}
	tokens := NewSecretTokenStore(c, cfg.Namespace, cfg.TokensSecret, tokenReload)
	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           NewHandler(store, tokens, NewKubeLogStreamer(cs)),
		ReadHeaderTimeout: 10 * time.Second,
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	select {
	case err := <-errc:
		return fmt.Errorf("serving: %w", err)
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownPeriod)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutting down: %w", err)
	}
	return nil
}

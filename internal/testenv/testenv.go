// Package testenv starts one shared envtest API server per test binary.
package testenv

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/mudler/agent-controller/api/v1alpha1"
)

var (
	once     sync.Once
	env      *envtest.Environment
	restCfg  *rest.Config
	startErr error

	// Scheme holds the core types and the agents.mudler.dev types.
	Scheme = newScheme()
)

func newScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		panic(err)
	}
	if err := v1alpha1.AddToScheme(s); err != nil {
		panic(err)
	}
	return s
}

// RepoRoot returns the directory that holds go.mod.
func RepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("getting the working directory: %w", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found")
		}
		dir = parent
	}
}

// Config starts the shared API server on first use and returns its config.
// It fails the test when the server cannot start, for example when
// KUBEBUILDER_ASSETS is not set.
func Config(t testing.TB) *rest.Config {
	t.Helper()
	once.Do(func() {
		root, err := RepoRoot()
		if err != nil {
			startErr = err
			return
		}
		env = &envtest.Environment{
			CRDDirectoryPaths:     []string{filepath.Join(root, "config", "crd", "bases")},
			ErrorIfCRDPathMissing: true,
		}
		restCfg, startErr = env.Start()
	})
	if startErr != nil {
		t.Fatalf("starting envtest (is KUBEBUILDER_ASSETS set?): %v", startErr)
	}
	return restCfg
}

// Client returns a client for the shared API server.
func Client(t testing.TB) client.Client {
	t.Helper()
	c, err := client.New(Config(t), client.Options{Scheme: Scheme})
	if err != nil {
		t.Fatalf("creating client: %v", err)
	}
	return c
}

// Stop stops the shared API server if it was started. Call it from TestMain.
func Stop() {
	if env != nil {
		_ = env.Stop()
	}
}

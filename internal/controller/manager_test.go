package controller

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mudler/agent-controller/api/v1alpha1"
	"github.com/mudler/agent-controller/internal/testenv"
)

func TestParseControllerFlags(t *testing.T) {
	cfg, opts, err := ParseControllerFlags([]string{"--harness-versions=nib=v0.14.2,claude-code=2.1.326,nib=v0.15.0"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Namespace != "agents" || cfg.LoginsNamespace != "agent-logins" || cfg.MemoryClaimTemplate != "%s-memory" ||
		cfg.ProxyIdleSeconds != 600 || cfg.RetainPerProfile != 200 || cfg.LauncherLockTemplate != "" {
		t.Errorf("defaults = %+v", cfg)
	}
	if !slices.Equal(cfg.HarnessVersions[v1alpha1.HarnessNib], []string{"v0.14.2", "v0.15.0"}) ||
		!slices.Equal(cfg.HarnessVersions[v1alpha1.HarnessClaudeCode], []string{"2.1.326"}) {
		t.Errorf("harness versions = %v", cfg.HarnessVersions)
	}
	if opts.LeaderElect || opts.HealthProbeBindAddress != ":8081" {
		t.Errorf("options = %+v", opts)
	}
}

func TestParseControllerFlagsErrors(t *testing.T) {
	tests := map[string][]string{
		"no harness versions":  nil,
		"bad harness versions": {"--harness-versions=nib"},
		"unknown harness":      {"--harness-versions=other=1"},
		"empty version":        {"--harness-versions=nib="},
		"bad claim template":   {"--harness-versions=nib=1", "--memory-claim-template=memory"},
		"bad lock template":    {"--harness-versions=nib=1", "--launcher-lock-template=lock"},
		"zero retention":       {"--harness-versions=nib=1", "--retain-per-profile=0"},
		"unknown flag":         {"--harness-versions=nib=1", "--nope"},
		"negative proxy idle":  {"--harness-versions=nib=1", "--proxy-idle-seconds=-1"},
	}
	for name, args := range tests {
		t.Run(name, func(t *testing.T) {
			if _, _, err := ParseControllerFlags(args); err == nil {
				t.Errorf("ParseControllerFlags(%v): want error", args)
			}
		})
	}
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}

func TestNewManagerStartsReady(t *testing.T) {
	c := testenv.Client(t)
	ns := newNamespace(t, c)
	cfg := testConfig(ns)
	cfg.JobTemplatePath = filepath.Join("testdata", "job-template.yaml")
	probe := freeAddr(t)
	mgr, err := NewManager(testenv.Config(t), cfg, ManagerOptions{MetricsBindAddress: "0", HealthProbeBindAddress: probe})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- mgr.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("manager: %v", err)
		}
	})
	eventually(t, 15*time.Second, func() error {
		resp, err := http.Get("http://" + probe + "/readyz")
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("readyz = %d", resp.StatusCode)
		}
		return nil
	})
}

func TestNewManagerBadTemplate(t *testing.T) {
	cfg := testConfig("x")
	cfg.JobTemplatePath = filepath.Join("testdata", "missing.yaml")
	if _, err := NewManager(nil, cfg, ManagerOptions{}); err == nil || !strings.Contains(err.Error(), "job template") {
		t.Errorf("NewManager() error = %v, want job template error", err)
	}
}

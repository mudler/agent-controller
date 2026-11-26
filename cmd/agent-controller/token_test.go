package main

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/mudler/agent-controller/internal/server"
)

func TestTokenNew(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"token", "new", "--name", "planner", "--scopes", "runs:read,runs:write,runs:read"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("output = %q", stdout.String())
	}
	m := regexp.MustCompile(`^token: ([A-Za-z0-9_-]{43})$`).FindStringSubmatch(lines[0])
	if m == nil {
		t.Fatalf("token line = %q", lines[0])
	}
	entryJSON, ok := strings.CutPrefix(lines[1], server.TokensKey+": ")
	if !ok {
		t.Fatalf("entry line = %q", lines[1])
	}
	entries := map[string]server.TokenEntry{}
	if err := json.Unmarshal([]byte(entryJSON), &entries); err != nil {
		t.Fatal(err)
	}
	e := entries["planner"]
	if e.SHA256 != server.HashToken(m[1]) || strings.Join(e.Scopes, ",") != "runs:read,runs:write" {
		t.Errorf("entry = %+v", e)
	}
	if strings.Contains(entryJSON, m[1]) {
		t.Error("entry contains the token itself")
	}
}

func TestTokenNewRejects(t *testing.T) {
	tests := map[string][]string{
		"no subcommand": {"token"},
		"unknown scope": {"token", "new", "--name", "a", "--scopes", "runs:read,admin"},
		"no name":       {"token", "new", "--scopes", "runs:read"},
		"no scopes":     {"token", "new", "--name", "a"},
		"unknown flag":  {"token", "new", "--name", "a", "--scopes", "runs:read", "--x"},
		"other command": {"token", "list"},
	}
	for name, args := range tests {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run(args, &stdout, &stderr); code != 2 || stdout.Len() != 0 {
				t.Errorf("exit %d, stdout %q", code, stdout.String())
			}
		})
	}
}

func TestServerFlags(t *testing.T) {
	cfg, err := server.ParseServerFlags([]string{"--harness-versions=nib=v1"})
	if err != nil {
		t.Fatal(err)
	}
	want := server.ServerConfig{
		Namespace: "agents", LoginsNamespace: "agent-logins", HistoryNamespace: "agent-controller-history",
		Listen: ":8080", TokensSecret: "agent-controller-tokens", HarnessVersions: "nib=v1",
	}
	if cfg != want {
		t.Errorf("defaults = %+v", cfg)
	}
	for _, args := range [][]string{
		nil,
		{"--harness-versions=nib=v1", "--history-namespace=agents"},
		{"--harness-versions=nib=v1", "--logins-namespace=agents"},
	} {
		if _, err := server.ParseServerFlags(args); err == nil {
			t.Errorf("ParseServerFlags(%v): want error", args)
		}
	}
}

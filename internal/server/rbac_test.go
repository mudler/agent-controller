package server

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	"sigs.k8s.io/yaml"

	"github.com/mudler/agent-controller/internal/testenv"
)

func loadRoles(t *testing.T, path string) []rbacv1.Role {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	roles := []rbacv1.Role{}
	for _, doc := range bytes.Split(data, []byte("\n---")) {
		if len(bytes.TrimSpace(doc)) == 0 {
			continue
		}
		var r rbacv1.Role
		if err := yaml.Unmarshal(doc, &r); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if r.Kind == "Role" || r.Kind == "ClusterRole" {
			roles = append(roles, r)
		}
	}
	return roles
}

func coreRule(r rbacv1.PolicyRule, resource string) bool {
	return slices.Contains(r.APIGroups, "") && slices.Contains(r.Resources, resource)
}

func TestGeneratedRBACHasNoSecretReads(t *testing.T) {
	root, err := testenv.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(root, "config", "rbac", "*", "role.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("role files = %v, want controller and server", files)
	}
	for _, f := range files {
		component := filepath.Base(filepath.Dir(f))
		roles := loadRoles(t, f)
		if len(roles) == 0 {
			t.Fatalf("%s has no roles", f)
		}
		for _, role := range roles {
			if role.Kind != "Role" {
				t.Errorf("%s: %s is a %s, want namespaced Roles only", f, role.Name, role.Kind)
			}
			for _, rule := range role.Rules {
				if coreRule(rule, "secrets") {
					if component == "controller" {
						t.Errorf("controller role %s/%s grants %v on secrets", role.Namespace, role.Name, rule.Verbs)
						continue
					}
					reads := slices.ContainsFunc(rule.Verbs, func(v string) bool { return v == "get" || v == "list" || v == "watch" || v == "*" })
					if reads && (!slices.Equal(rule.ResourceNames, []string{DefaultTokensSecret}) || slices.Contains(rule.Verbs, "list") || slices.Contains(rule.Verbs, "watch")) {
						t.Errorf("server role %s/%s reads secrets beyond the tokens Secret: %+v", role.Namespace, role.Name, rule)
					}
				}
				if component == "server" && coreRule(rule, "configmaps") && role.Namespace != DefaultHistoryNamespace {
					t.Errorf("server role %s/%s has configmap access outside the history namespace: %v", role.Namespace, role.Name, rule.Verbs)
				}
			}
		}
	}
}

package controller

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	"sigs.k8s.io/yaml"

	"github.com/mudler/agent-controller/internal/testenv"
)

// TestControllerRBACVerbs pins the controller Role to the verbs the code
// uses. Repository deployments mirror this list.
func TestControllerRBACVerbs(t *testing.T) {
	root, err := testenv.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "config", "rbac", "controller", "role.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var role rbacv1.Role
	if err := yaml.Unmarshal(data, &role); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, r := range role.Rules {
		for _, g := range r.APIGroups {
			for _, res := range r.Resources {
				verbs := slices.Clone(r.Verbs)
				slices.Sort(verbs)
				got[g+"/"+res] = strings.Join(verbs, ",")
			}
		}
	}
	want := map[string]string{
		"agents.mudler.dev/agentprofiles":          "get,list,watch",
		"agents.mudler.dev/agentprofiles/status":   "update",
		"agents.mudler.dev/harnessprofiles":        "get,list,watch",
		"agents.mudler.dev/harnessprofiles/status": "update",
		"agents.mudler.dev/logins":                 "get,list,watch",
		"agents.mudler.dev/agentpolicies":          "get,list,watch",
		"agents.mudler.dev/agentruns":              "create,delete,get,list,update,watch",
		"agents.mudler.dev/agentruns/status":       "update",
		"agents.mudler.dev/agentruns/finalizers":   "update",
		"batch/jobs":                               "create,delete,get,list,watch",
		"/configmaps":                              "create,delete,list",
		"/pods":                                    "delete,list,watch",
		"/pods/log":                                "get",
		"/services":                                "delete,list",
		"/persistentvolumeclaims":                  "create,get",
		"/events":                                  "create,patch",
		"networking.k8s.io/networkpolicies":        "delete,list",
		"agents.x-k8s.io/sandboxes":                "delete,list",
		"coordination.k8s.io/leases":               "create,delete,get,list,update,watch",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: verbs %q, want %q", k, got[k], v)
		}
	}
	for k, v := range got {
		if _, ok := want[k]; !ok {
			t.Errorf("unexpected rule %s: %s", k, v)
		}
	}
}

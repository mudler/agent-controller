package controller

import (
	"context"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/mudler/agent-controller/internal/testenv"
)

// TestCleanupListsThroughReader checks that Cleanup lists with the uncached
// reader. The controller Role has list but no watch on Services,
// NetworkPolicies and Sandboxes, so a cached list would fail in a cluster.
func TestCleanupListsThroughReader(t *testing.T) {
	real := testenv.Client(t)
	ns := newNamespace(t, real)
	ww, err := client.NewWithWatch(testenv.Config(t), client.Options{Scheme: testenv.Scheme})
	if err != nil {
		t.Fatal(err)
	}
	noList := interceptor.NewClient(ww, interceptor.Funcs{
		List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
			return apierrors.NewForbidden(schema.GroupResource{Resource: "services"}, "", nil)
		},
	})
	createRunObjects(t, real, ns, "r")
	done, err := Cleanup(context.Background(), noList, real, ns, "r", CleanupOptions{})
	if err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if done {
		t.Fatal("first pass reported done with objects present")
	}
}

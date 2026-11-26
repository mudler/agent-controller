package controller

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/mudler/agent-controller/api/v1alpha1"
	"github.com/mudler/agent-controller/internal/testenv"
)

// TestQueueSnapshotListsRunsFirst checks the read order of a snapshot. Runs
// must be read before the policy and profiles: a run is created after its
// profile, so every run in the snapshot then has its profile in it too.
func TestQueueSnapshotListsRunsFirst(t *testing.T) {
	c := testenv.Client(t)
	ns := newNamespace(t, c)
	newFixtures(ns).create(t, c)

	ww, err := client.NewWithWatch(testenv.Config(t), client.Options{Scheme: testenv.Scheme})
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var order []string
	note := func(kind string) {
		mu.Lock()
		defer mu.Unlock()
		order = append(order, kind)
	}
	reader := interceptor.NewClient(ww, interceptor.Funcs{
		List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			note(fmt.Sprintf("%T", list))
			return cl.List(ctx, list, opts...)
		},
		Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			note(fmt.Sprintf("%T", obj))
			return cl.Get(ctx, key, obj, opts...)
		},
	})
	r := &QueueReconciler{Client: c, Reader: reader, Cfg: testConfig(ns)}
	if _, _, err := r.snapshot(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(order) == 0 || order[0] != "*v1alpha1.AgentRunList" {
		t.Errorf("snapshot read order = %v, want AgentRunList first", order)
	}
}

// TestQueueAdmitRechecksProfile checks that admit reads the profile again
// and refuses a run whose profile is no longer validated.
func TestQueueAdmitRechecksProfile(t *testing.T) {
	c := testenv.Client(t)
	ctx := context.Background()
	ns := newNamespace(t, c)
	newFixtures(ns).create(t, c)
	prof := newTestProfile(ns, "recheck")
	mustCreate(t, c, prof)
	run := newRun(ns, "recheck-1", "recheck", nil)
	mustCreate(t, c, run)

	// The owner validation goes stale after the snapshot was taken.
	if err := c.Get(ctx, client.ObjectKeyFromObject(prof), prof); err != nil {
		t.Fatal(err)
	}
	prof.Spec.Validated = false
	if err := c.Update(ctx, prof); err != nil {
		t.Fatal(err)
	}

	tmpl, err := LoadJobTemplate(filepath.Join("testdata", "job-template.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	r := &QueueReconciler{Client: c, Reader: c, Cfg: testConfig(ns), JobTemplate: tmpl}
	err = r.admit(ctx, run)
	if !errors.Is(err, errNotAdmissible) {
		t.Errorf("admit() error = %v, want errNotAdmissible", err)
	}
	if jobExists(t, c, ns, run.Name) {
		t.Error("admit created a Job for an unvalidated profile")
	}
	if got := getRun(t, c, ns, run.Name).Status.Phase; got == v1alpha1.PhaseStarting {
		t.Errorf("phase = %s", got)
	}
}

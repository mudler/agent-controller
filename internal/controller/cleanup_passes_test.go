package controller

import (
	"context"
	"fmt"
	"testing"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clocktesting "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/mudler/agent-controller/api/v1alpha1"
	"github.com/mudler/agent-controller/internal/testenv"
)

// TestRunLocksNeedTwoEmptyCleanupPasses checks that one empty cleanup pass
// is not enough to release the locks: a terminating launcher may still
// create a Sandbox between the lists of one pass.
func TestRunLocksNeedTwoEmptyCleanupPasses(t *testing.T) {
	c := testenv.Client(t)
	ctx := context.Background()
	ns := newNamespace(t, c)
	run := newRun(ns, "twice", "example", nil)
	controllerutil.AddFinalizer(run, FinalizerCleanup)
	mustCreate(t, c, run)
	run.Status.Phase = v1alpha1.PhaseCancelled
	if err := c.Status().Update(ctx, run); err != nil {
		t.Fatal(err)
	}
	mustCreate(t, c, &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{Name: MemoryLockName("example"), Namespace: ns, Labels: map[string]string{LabelRun: "twice"}},
		Spec:       coordinationv1.LeaseSpec{HolderIdentity: ptr.To("twice")},
	})

	clk := clocktesting.NewFakeClock(time.Now())
	r := &RunReconciler{Client: c, Reader: c, Logs: newFakeLogs(), Cfg: testConfig(ns), Clock: clk}
	req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(run)}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	if !leaseExists(t, c, ns, MemoryLockName("example")) {
		t.Fatal("lock released after one empty cleanup pass")
	}
	// A second pass right away is not enough either.
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	if !leaseExists(t, c, ns, MemoryLockName("example")) {
		t.Fatal("lock released by an immediate second pass")
	}
	clk.Step(cleanupRetry + time.Second)
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	if leaseExists(t, c, ns, MemoryLockName("example")) {
		t.Fatal("lock still held after two empty cleanup passes")
	}
	if got := getRun(t, c, ns, "twice"); controllerutil.ContainsFinalizer(got, FinalizerCleanup) {
		t.Error("finalizer kept after release")
	}
}

// TestCleanupListsPodsFirst checks that Cleanup lists Pods before Sandboxes,
// so a Sandbox that a terminating launcher creates is seen by this pass or
// by the launcher Pod check.
func TestCleanupListsPodsFirst(t *testing.T) {
	c := testenv.Client(t)
	ns := newNamespace(t, c)
	ww, err := client.NewWithWatch(testenv.Config(t), client.Options{Scheme: testenv.Scheme})
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	reader := interceptor.NewClient(ww, interceptor.Funcs{
		List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			order = append(order, fmt.Sprintf("%T", list))
			return cl.List(ctx, list, opts...)
		},
	})
	if _, err := Cleanup(context.Background(), c, reader, ns, "r", CleanupOptions{StopLauncher: true}); err != nil {
		t.Fatal(err)
	}
	if len(order) == 0 || order[0] != "*v1.PodList" {
		t.Errorf("list order = %v, want PodList first", order)
	}
}

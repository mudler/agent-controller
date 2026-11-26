package controller

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mudler/agent-controller/api/v1alpha1"
	"github.com/mudler/agent-controller/internal/testenv"
)

func newQueueReconciler(t *testing.T, mgr ctrl.Manager, cfg Config) *QueueReconciler {
	t.Helper()
	tmpl, err := LoadJobTemplate(filepath.Join("testdata", "job-template.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return &QueueReconciler{Client: mgr.GetClient(), Reader: mgr.GetAPIReader(), Cfg: cfg, JobTemplate: tmpl}
}

func startQueue(t *testing.T, ns string, cfg Config) {
	t.Helper()
	startManager(t, ns, func(mgr ctrl.Manager) error {
		return newQueueReconciler(t, mgr, cfg).SetupWithManager(mgr)
	})
}

func newRun(ns, name, profile string, memory *bool) *v1alpha1.AgentRun {
	return &v1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: v1alpha1.AgentRunSpec{
			Profile:     profile,
			Memory:      memory,
			Trigger:     v1alpha1.TriggerManual,
			RequestedBy: "tester",
		},
	}
}

func getRun(t *testing.T, c client.Client, ns, name string) *v1alpha1.AgentRun {
	t.Helper()
	run := &v1alpha1.AgentRun{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: name}, run); err != nil {
		t.Fatalf("getting run %s: %v", name, err)
	}
	return run
}

func waitPhase(t *testing.T, c client.Client, ns, name string, want v1alpha1.Phase) *v1alpha1.AgentRun {
	t.Helper()
	var run *v1alpha1.AgentRun
	eventually(t, 20*time.Second, func() error {
		run = getRun(t, c, ns, name)
		if run.Status.Phase != want {
			return fmt.Errorf("run %s phase = %q (%s), want %q", name, run.Status.Phase, run.Status.Message, want)
		}
		return nil
	})
	return run
}

func jobExists(t *testing.T, c client.Client, ns, name string) bool {
	t.Helper()
	err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: name}, &batchv1.Job{})
	return err == nil
}

// finishRun marks a run final and removes its memory lock, as the run
// reconciler would.
func finishRun(t *testing.T, c client.Client, ns, name string) {
	t.Helper()
	ctx := context.Background()
	run := getRun(t, c, ns, name)
	run.Status.Phase = v1alpha1.PhaseSucceeded
	run.Status.CompletionTime = ptr.To(metav1.Now())
	if err := c.Status().Update(ctx, run); err != nil {
		t.Fatal(err)
	}
	lease := &coordinationv1.Lease{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: MemoryLockName(run.Spec.Profile)}, lease); err == nil {
		if ptr.Deref(lease.Spec.HolderIdentity, "") == name {
			if err := c.Delete(ctx, lease); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestQueueParallelMemoryOffRuns(t *testing.T) {
	c := testenv.Client(t)
	ns := newNamespace(t, c)
	newFixtures(ns).create(t, c)
	prof := newTestProfile(ns, "par")
	prof.Spec.MaxParallel = 3
	prof.Spec.Memory = v1alpha1.MemoryDisabled
	mustCreate(t, c, prof)
	for i := range 3 {
		mustCreate(t, c, newRun(ns, fmt.Sprintf("par-%d", i), "par", nil))
	}
	startQueue(t, ns, testConfig(ns))

	for i := range 3 {
		name := fmt.Sprintf("par-%d", i)
		run := waitPhase(t, c, ns, name, v1alpha1.PhaseStarting)
		if !jobExists(t, c, ns, name) {
			t.Errorf("no Job for %s", name)
		}
		if run.Status.StartTime == nil || run.Status.ProfileVersion != getProfileGeneration(t, c, ns, "par") {
			t.Errorf("status = %+v", run.Status)
		}
		if !strings.Contains(strings.Join(run.Finalizers, ","), FinalizerCleanup) {
			t.Errorf("finalizers = %v", run.Finalizers)
		}
		cm := &corev1.ConfigMap{}
		if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: name + "-spec"}, cm); err != nil {
			t.Errorf("resolved run ConfigMap: %v", err)
		} else if !strings.Contains(cm.Data[RunSpecKey], `"enabled": false`) {
			t.Errorf("resolved run has memory enabled: %s", cm.Data[RunSpecKey])
		}
	}
	leases := &coordinationv1.LeaseList{}
	if err := c.List(context.Background(), leases, client.InNamespace(ns)); err != nil {
		t.Fatal(err)
	}
	if len(leases.Items) != 0 {
		t.Errorf("memory-off runs created %d leases", len(leases.Items))
	}
}

func getProfileGeneration(t *testing.T, c client.Client, ns, name string) int64 {
	t.Helper()
	p := &v1alpha1.AgentProfile{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: name}, p); err != nil {
		t.Fatal(err)
	}
	return p.Generation
}

func TestQueueMemoryRunsSerialise(t *testing.T) {
	c := testenv.Client(t)
	ctx := context.Background()
	ns := newNamespace(t, c)
	newFixtures(ns).create(t, c)
	prof := newTestProfile(ns, "mem")
	prof.Spec.MaxParallel = 4
	mustCreate(t, c, prof)
	mustCreate(t, c, newRun(ns, "mem-1", "mem", nil))
	time.Sleep(1100 * time.Millisecond) // distinct creation timestamps
	mustCreate(t, c, newRun(ns, "mem-2", "mem", nil))
	time.Sleep(1100 * time.Millisecond)
	mustCreate(t, c, newRun(ns, "mem-off", "mem", ptr.To(false)))
	startQueue(t, ns, testConfig(ns))

	waitPhase(t, c, ns, "mem-1", v1alpha1.PhaseStarting)
	waitPhase(t, c, ns, "mem-off", v1alpha1.PhaseStarting)
	second := waitPhase(t, c, ns, "mem-2", v1alpha1.PhaseQueued)
	if second.Status.QueuePosition != 1 {
		t.Errorf("queuePosition = %d, want 1", second.Status.QueuePosition)
	}

	lease := &coordinationv1.Lease{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: "memory-mem"}, lease); err != nil {
		t.Fatalf("memory lock: %v", err)
	}
	if ptr.Deref(lease.Spec.HolderIdentity, "") != "mem-1" || lease.Labels[LabelRun] != "mem-1" {
		t.Errorf("lease = %+v", lease)
	}
	if len(lease.OwnerReferences) != 1 || lease.OwnerReferences[0].Name != "mem-1" {
		t.Errorf("lease owner = %+v", lease.OwnerReferences)
	}
	pvc := &corev1.PersistentVolumeClaim{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: "mem-memory"}, pvc); err != nil {
		t.Errorf("memory claim: %v", err)
	} else if len(pvc.OwnerReferences) != 0 {
		t.Errorf("memory claim is owned: %+v", pvc.OwnerReferences)
	}

	// Final phase alone does not free the lock; the Lease must be gone.
	run := getRun(t, c, ns, "mem-1")
	run.Status.Phase = v1alpha1.PhaseSucceeded
	if err := c.Status().Update(ctx, run); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Second)
	if p := getRun(t, c, ns, "mem-2").Status.Phase; p != v1alpha1.PhaseQueued {
		t.Fatalf("mem-2 phase = %s while the lock is held", p)
	}
	finishRun(t, c, ns, "mem-1")
	waitPhase(t, c, ns, "mem-2", v1alpha1.PhaseStarting)
}

func TestQueueGlobalSlots(t *testing.T) {
	c := testenv.Client(t)
	ns := newNamespace(t, c)
	f := newFixtures(ns)
	f.Policy.Spec.Limits.GlobalSlots = 2
	f.create(t, c)
	a := newTestProfile(ns, "a")
	a.Spec.MaxParallel = 4
	a.Spec.Memory = v1alpha1.MemoryDisabled
	b := newTestProfile(ns, "b")
	b.Spec.Memory = v1alpha1.MemoryDisabled
	mustCreate(t, c, a, b)
	mustCreate(t, c, newRun(ns, "a-1", "a", nil))
	time.Sleep(1100 * time.Millisecond)
	mustCreate(t, c, newRun(ns, "a-2", "a", nil))
	time.Sleep(1100 * time.Millisecond)
	mustCreate(t, c, newRun(ns, "b-1", "b", nil))
	startQueue(t, ns, testConfig(ns))

	waitPhase(t, c, ns, "a-1", v1alpha1.PhaseStarting)
	waitPhase(t, c, ns, "a-2", v1alpha1.PhaseStarting)
	waitPhase(t, c, ns, "b-1", v1alpha1.PhaseQueued)
	finishRun(t, c, ns, "a-1")
	waitPhase(t, c, ns, "b-1", v1alpha1.PhaseStarting)
}

func TestQueueRejectsAndCancels(t *testing.T) {
	c := testenv.Client(t)
	ns := newNamespace(t, c)
	newFixtures(ns).create(t, c)
	unvalidated := newTestProfile(ns, "fresh")
	unvalidated.Spec.Validated = false
	invalid := newTestProfile(ns, "wide")
	invalid.Spec.Egress.ExtraHosts = []string{"evil.example.net:443"}
	mustCreate(t, c, unvalidated, invalid)
	mustCreate(t, c,
		newRun(ns, "fresh-1", "fresh", nil),
		newRun(ns, "wide-1", "wide", nil),
		newRun(ns, "ghost-1", "ghost", nil),
	)
	cancelled := newRun(ns, "fresh-2", "fresh", nil)
	cancelled.Spec.Cancel = true
	mustCreate(t, c, cancelled)
	startQueue(t, ns, testConfig(ns))

	for name, msg := range map[string]string{
		"fresh-1": "not validated",
		"wide-1":  "egress.extraHosts[0]",
		"ghost-1": "not found",
	} {
		run := waitPhase(t, c, ns, name, v1alpha1.PhaseRejected)
		if !strings.Contains(run.Status.Message, msg) || run.Status.CompletionTime == nil {
			t.Errorf("%s: status = %+v, want message containing %q", name, run.Status, msg)
		}
	}
	waitPhase(t, c, ns, "fresh-2", v1alpha1.PhaseCancelled)
}

func TestQueueRetention(t *testing.T) {
	c := testenv.Client(t)
	ctx := context.Background()
	ns := newNamespace(t, c)
	newFixtures(ns).create(t, c)
	mustCreate(t, c, newTestProfile(ns, "ret"))
	base := time.Now().Add(-time.Hour)
	for i := range 5 {
		run := newRun(ns, fmt.Sprintf("ret-%d", i), "ret", nil)
		mustCreate(t, c, run)
		run.Status.Phase = v1alpha1.PhaseSucceeded
		run.Status.CompletionTime = &metav1.Time{Time: base.Add(time.Duration(i) * time.Minute)}
		if err := c.Status().Update(ctx, run); err != nil {
			t.Fatal(err)
		}
	}
	cfg := testConfig(ns)
	cfg.RetainPerProfile = 3
	startQueue(t, ns, cfg)

	eventually(t, 20*time.Second, func() error {
		runs := listRuns(t, c, ns)
		if len(runs) != 3 {
			return fmt.Errorf("runs = %d, want 3", len(runs))
		}
		for _, r := range runs {
			if r.Name == "ret-0" || r.Name == "ret-1" {
				return fmt.Errorf("old run %s kept", r.Name)
			}
		}
		return nil
	})
}

func TestQueueRejectsStaleValidation(t *testing.T) {
	c := testenv.Client(t)
	ctx := context.Background()
	ns := newNamespace(t, c)
	f := newFixtures(ns)
	f.create(t, c)
	mustCreate(t, c, newTestProfile(ns, "stale"))

	// Delete and recreate the harness profile with the same name: the
	// profile keeps spec.validated, but the owner validated another object.
	if err := c.Delete(ctx, f.Harness); err != nil {
		t.Fatal(err)
	}
	again := exampleHarness()
	again.Namespace = ns
	mustCreate(t, c, again)

	mustCreate(t, c, newRun(ns, "stale-1", "stale", nil))
	startQueue(t, ns, testConfig(ns))
	run := waitPhase(t, c, ns, "stale-1", v1alpha1.PhaseRejected)
	if !strings.Contains(run.Status.Message, "not validated") {
		t.Errorf("message = %q", run.Status.Message)
	}
	if jobExists(t, c, ns, "stale-1") {
		t.Error("a Job was created for a stale validation")
	}
}

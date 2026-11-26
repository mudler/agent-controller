package controller

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clocktesting "k8s.io/utils/clock/testing"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mudler/agent-controller/api/v1alpha1"
	"github.com/mudler/agent-controller/internal/testenv"
)

func profileValid(t *testing.T, c client.Client, ns, name string) (metav1.ConditionStatus, string) {
	t.Helper()
	p := &v1alpha1.AgentProfile{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: name}, p); err != nil {
		t.Fatalf("getting profile: %v", err)
	}
	cond := meta.FindStatusCondition(p.Status.Conditions, v1alpha1.ConditionValid)
	if cond == nil {
		return metav1.ConditionUnknown, ""
	}
	return cond.Status, cond.Message
}

func waitValid(t *testing.T, c client.Client, ns, name string, want metav1.ConditionStatus, msg string) {
	t.Helper()
	eventually(t, 15*time.Second, func() error {
		got, m := profileValid(t, c, ns, name)
		if got != want || !strings.Contains(m, msg) {
			return fmt.Errorf("Valid = %s (%q), want %s containing %q", got, m, want, msg)
		}
		return nil
	})
}

func setupProfileReconcilers(ns string) func(mgr ctrl.Manager) error {
	return func(mgr ctrl.Manager) error {
		cfg := testConfig(ns)
		pr := &ProfileReconciler{Client: mgr.GetClient(), Reader: mgr.GetAPIReader(), Cfg: cfg, Clock: clocktesting.NewFakePassiveClock(time.Now())}
		if err := pr.SetupWithManager(mgr); err != nil {
			return err
		}
		return (&HarnessReconciler{Client: mgr.GetClient(), Cfg: cfg}).SetupWithManager(mgr)
	}
}

func TestProfileValidityFollowsPolicy(t *testing.T) {
	c := testenv.Client(t)
	ctx := context.Background()
	ns := newNamespace(t, c)
	f := newFixtures(ns)
	f.create(t, c)
	prof := newTestProfile(ns, "egress-all")
	prof.Spec.Egress = v1alpha1.ProfileEgress{Mode: v1alpha1.EgressAll}
	prof.Annotations = map[string]string{AnnotationLastChangedBy: "planner"}
	mustCreate(t, c, prof)

	startManager(t, ns, setupProfileReconcilers(ns))
	waitValid(t, c, ns, prof.Name, metav1.ConditionTrue, "")

	got := &v1alpha1.AgentProfile{}
	if err := c.Get(ctx, client.ObjectKeyFromObject(prof), got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Version != got.Generation || got.Status.LastChangedBy != "planner" {
		t.Errorf("status = %+v, want version %d and lastChangedBy planner", got.Status, got.Generation)
	}

	f.Policy.Spec.Egress.AllowAll = false
	if err := c.Update(ctx, f.Policy); err != nil {
		t.Fatal(err)
	}
	waitValid(t, c, ns, prof.Name, metav1.ConditionFalse, "egress.mode")

	h := &v1alpha1.HarnessProfile{}
	eventually(t, 10*time.Second, func() error {
		if err := c.Get(ctx, client.ObjectKeyFromObject(f.Harness), h); err != nil {
			return err
		}
		if !meta.IsStatusConditionTrue(h.Status.Conditions, v1alpha1.ConditionValid) {
			return fmt.Errorf("harness not Valid: %+v", h.Status.Conditions)
		}
		return nil
	})
}

func TestProfileInvalidAfterLoginDeletion(t *testing.T) {
	c := testenv.Client(t)
	ctx := context.Background()
	ns := newNamespace(t, c)
	f := newFixtures(ns)
	f.create(t, c)
	prof := newTestProfile(ns, "example")
	mustCreate(t, c, prof)

	startManager(t, ns, setupProfileReconcilers(ns))
	waitValid(t, c, ns, prof.Name, metav1.ConditionTrue, "")

	if err := c.Delete(ctx, f.GitHub); err != nil {
		t.Fatal(err)
	}
	waitValid(t, c, ns, prof.Name, metav1.ConditionFalse, "github: login not found")

	if err := c.Delete(ctx, f.Model); err != nil {
		t.Fatal(err)
	}
	waitValid(t, c, ns, prof.Name, metav1.ConditionFalse, "harness profile is invalid")
}

func listRuns(t *testing.T, c client.Client, ns string) []v1alpha1.AgentRun {
	t.Helper()
	list := &v1alpha1.AgentRunList{}
	if err := c.List(context.Background(), list, client.InNamespace(ns)); err != nil {
		t.Fatal(err)
	}
	return list.Items
}

func TestProfileScheduleCreatesOneRunPerTick(t *testing.T) {
	c := testenv.Client(t)
	ctx := context.Background()
	ns := newNamespace(t, c)
	newFixtures(ns).create(t, c)
	prof := newTestProfile(ns, "sched")
	prof.Spec.Schedule = &v1alpha1.Schedule{Cron: "*/5 * * * *"}
	mustCreate(t, c, prof)
	if err := c.Get(ctx, client.ObjectKeyFromObject(prof), prof); err != nil {
		t.Fatal(err)
	}

	firstTick, err := NextTick(prof.Spec.Schedule, prof.CreationTimestamp.Time)
	if err != nil {
		t.Fatal(err)
	}
	clk := clocktesting.NewFakePassiveClock(firstTick.Add(10 * time.Second))
	r := &ProfileReconciler{Client: c, Reader: c, Cfg: testConfig(ns), Clock: clk}
	req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(prof)}

	res, err := r.Reconcile(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if res.RequeueAfter <= 0 || res.RequeueAfter > 5*time.Minute+time.Second {
		t.Errorf("RequeueAfter = %s, want until the next tick", res.RequeueAfter)
	}
	runs := listRuns(t, c, ns)
	if len(runs) != 1 {
		t.Fatalf("runs = %d, want 1", len(runs))
	}
	first := runs[0]
	if first.Name != ScheduledRunName("sched", firstTick) || first.Spec.Trigger != v1alpha1.TriggerSchedule || first.Spec.RequestedBy != RequestedBySchedule {
		t.Errorf("scheduled run = %s %+v", first.Name, first.Spec)
	}

	// Same tick again: no second run.
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	if n := len(listRuns(t, c, ns)); n != 1 {
		t.Fatalf("runs after a repeated reconcile = %d, want 1", n)
	}
	if err := c.Get(ctx, req.NamespacedName, prof); err != nil {
		t.Fatal(err)
	}
	if prof.Status.LastScheduleTime == nil || !prof.Status.LastScheduleTime.Time.Equal(firstTick) {
		t.Errorf("lastScheduleTime = %v, want %s", prof.Status.LastScheduleTime, firstTick)
	}
	if prof.Status.NextScheduleTime == nil || !prof.Status.NextScheduleTime.Time.Equal(firstTick.Add(5*time.Minute)) {
		t.Errorf("nextScheduleTime = %v", prof.Status.NextScheduleTime)
	}

	// Next tick while the first scheduled run is still queued: skipped.
	clk.SetTime(firstTick.Add(5*time.Minute + 10*time.Second))
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	if n := len(listRuns(t, c, ns)); n != 1 {
		t.Fatalf("runs while a scheduled run is queued = %d, want 1", n)
	}

	// Once the first run is running, the next tick creates a run.
	first.Status.Phase = v1alpha1.PhaseRunning
	if err := c.Status().Update(ctx, &first); err != nil {
		t.Fatal(err)
	}
	clk.SetTime(firstTick.Add(10*time.Minute + 10*time.Second))
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	if n := len(listRuns(t, c, ns)); n != 2 {
		t.Fatalf("runs after the next tick = %d, want 2", n)
	}
}

func TestProfileScheduleSkipsSuspendedAndUnvalidated(t *testing.T) {
	c := testenv.Client(t)
	ctx := context.Background()
	ns := newNamespace(t, c)
	newFixtures(ns).create(t, c)

	suspended := newTestProfile(ns, "suspended")
	suspended.Spec.Schedule = &v1alpha1.Schedule{Cron: "* * * * *", Suspend: true}
	unvalidated := newTestProfile(ns, "unvalidated")
	unvalidated.Spec.Schedule = &v1alpha1.Schedule{Cron: "* * * * *"}
	unvalidated.Spec.Validated = false
	mustCreate(t, c, suspended, unvalidated)

	r := &ProfileReconciler{Client: c, Reader: c, Cfg: testConfig(ns), Clock: clocktesting.NewFakePassiveClock(time.Now().Add(2 * time.Minute))}
	for _, p := range []*v1alpha1.AgentProfile{suspended, unvalidated} {
		if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(p)}); err != nil {
			t.Fatal(err)
		}
		got := &v1alpha1.AgentProfile{}
		if err := c.Get(ctx, client.ObjectKeyFromObject(p), got); err != nil {
			t.Fatal(err)
		}
		if got.Status.NextScheduleTime != nil {
			t.Errorf("%s: nextScheduleTime = %v, want nil", p.Name, got.Status.NextScheduleTime)
		}
	}
	if n := len(listRuns(t, c, ns)); n != 0 {
		t.Errorf("runs = %d, want 0", n)
	}
}

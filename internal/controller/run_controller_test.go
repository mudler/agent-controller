package controller

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/clock"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mudler/agent-controller/api/v1alpha1"
	"github.com/mudler/agent-controller/internal/testenv"
)

// fakeLogs serves launcher logs from memory.
type fakeLogs struct {
	mu   sync.Mutex
	logs map[string]string
}

func newFakeLogs() *fakeLogs { return &fakeLogs{logs: map[string]string{}} }

func (f *fakeLogs) set(pod, log string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.logs[pod] = log
}

func (f *fakeLogs) get(pod string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	log, ok := f.logs[pod]
	if !ok {
		return nil, fmt.Errorf("pod %s has no log", pod)
	}
	return []byte(log), nil
}

func (f *fakeLogs) Tail(_ context.Context, _, pod, _ string, _ int64) ([]byte, error) {
	return f.get(pod)
}

func (f *fakeLogs) Since(_ context.Context, _, pod, _ string, _ *metav1.Time, _ int64) ([]byte, error) {
	return f.get(pod)
}

// startController runs the profile, harness, queue and run reconcilers.
func startController(t *testing.T, ns string, cfg Config, logs LogReader) {
	t.Helper()
	startManager(t, ns, func(mgr ctrl.Manager) error {
		clk := clock.RealClock{}
		if err := (&ProfileReconciler{Client: mgr.GetClient(), Reader: mgr.GetAPIReader(), Cfg: cfg, Clock: clk}).SetupWithManager(mgr); err != nil {
			return err
		}
		if err := (&HarnessReconciler{Client: mgr.GetClient(), Cfg: cfg}).SetupWithManager(mgr); err != nil {
			return err
		}
		if err := newQueueReconciler(t, mgr, cfg).SetupWithManager(mgr); err != nil {
			return err
		}
		return (&RunReconciler{Client: mgr.GetClient(), Reader: mgr.GetAPIReader(), Logs: logs, Cfg: cfg, Clock: clk}).SetupWithManager(mgr)
	})
}

func stepLine(name string) string {
	return fmt.Sprintf(`{"step": %q, "at": %q}`, name, time.Now().UTC().Format(time.RFC3339))
}

// startLauncherPod creates the launcher Pod of a run, as the Job controller
// would.
func startLauncherPod(t *testing.T, c client.Client, ns, run string) *corev1.Pod {
	t.Helper()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      run + "-launcher",
			Namespace: ns,
			Labels:    map[string]string{LabelRun: run, LabelComponent: ComponentLauncher},
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers:    []corev1.Container{{Name: LauncherContainer, Image: "example.com/agent-runner:current"}},
		},
	}
	mustCreate(t, c, pod)
	return pod
}

// terminateLauncher writes the launcher termination message.
func terminateLauncher(t *testing.T, c client.Client, pod *corev1.Pod, msg string) {
	t.Helper()
	pod.Status.Phase = corev1.PodFailed
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name:  LauncherContainer,
		Image: "example.com/agent-runner:current",
		State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1, Message: msg}},
	}}
	if err := c.Status().Update(context.Background(), pod); err != nil {
		t.Fatalf("updating pod status: %v", err)
	}
}

// finishJob sets the terminal Job conditions that the Job controller would.
func finishJob(t *testing.T, c client.Client, ns, name string, succeeded bool, reason string) {
	t.Helper()
	ctx := context.Background()
	job := &batchv1.Job{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, job); err != nil {
		t.Fatalf("getting job: %v", err)
	}
	now := metav1.Now()
	start := metav1.NewTime(now.Add(-time.Minute))
	job.Status.StartTime = &start
	if succeeded {
		job.Status.Succeeded = 1
		job.Status.CompletionTime = &now
		job.Status.Conditions = []batchv1.JobCondition{
			{Type: batchv1.JobSuccessCriteriaMet, Status: corev1.ConditionTrue, LastTransitionTime: now},
			{Type: batchv1.JobComplete, Status: corev1.ConditionTrue, LastTransitionTime: now},
		}
	} else {
		job.Status.Failed = 1
		job.Status.Conditions = []batchv1.JobCondition{
			{Type: batchv1.JobFailureTarget, Status: corev1.ConditionTrue, Reason: reason, LastTransitionTime: now},
			{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: reason, LastTransitionTime: now},
		}
	}
	if err := c.Status().Update(ctx, job); err != nil {
		t.Fatalf("updating job status: %v", err)
	}
}

func setupRunTest(t *testing.T, memory v1alpha1.MemoryMode, maxParallel int32) (client.Client, string, *fakeLogs) {
	t.Helper()
	c := testenv.Client(t)
	ns := newNamespace(t, c)
	newFixtures(ns).create(t, c)
	prof := newTestProfile(ns, "example")
	prof.Spec.Memory = memory
	prof.Spec.MaxParallel = maxParallel
	mustCreate(t, c, prof)
	return c, ns, newFakeLogs()
}

func TestRunSucceedsWithSteps(t *testing.T) {
	c, ns, logs := setupRunTest(t, v1alpha1.MemoryEnabled, 1)
	startController(t, ns, testConfig(ns), logs)
	mustCreate(t, c, newRun(ns, "ok", "example", nil))
	waitPhase(t, c, ns, "ok", v1alpha1.PhaseStarting)

	pod := startLauncherPod(t, c, ns, "ok")
	logs.set(pod.Name, strings.Join([]string{stepLine(StepAdmitted), stepLine(StepSandboxReady), stepLine(StepGatePassed)}, "\n"))
	// The pod event triggers a step read; the run is still Starting.
	eventually(t, 20*time.Second, func() error {
		run := getRun(t, c, ns, "ok")
		if !HasStep(run.Status.Steps, StepGatePassed) || run.Status.PodName != pod.Name {
			return fmt.Errorf("status = %+v", run.Status)
		}
		return nil
	})
	if p := getRun(t, c, ns, "ok").Status.Phase; p != v1alpha1.PhaseStarting {
		t.Fatalf("phase = %s before harness-started", p)
	}

	logs.set(pod.Name, strings.Join([]string{stepLine(StepAdmitted), stepLine(StepSandboxReady), stepLine(StepGatePassed),
		stepLine(StepCredentialsSent), stepLine(StepHarnessStarted)}, "\n"))
	pod.Labels["poke"] = "1"
	if err := c.Update(context.Background(), pod); err != nil {
		t.Fatal(err)
	}
	waitPhase(t, c, ns, "ok", v1alpha1.PhaseRunning)

	logs.set(pod.Name, strings.Join([]string{stepLine(StepHarnessStarted), `{"type":"result"}`, stepLine(StepCleanupDone)}, "\n"))
	terminateLauncher(t, c, pod, `{"harnessExit": 0, "error": ""}`)
	finishJob(t, c, ns, "ok", true, "")

	run := waitPhase(t, c, ns, "ok", v1alpha1.PhaseSucceeded)
	if ptr.Deref(run.Status.ExitCode, -1) != 0 || run.Status.CompletionTime == nil {
		t.Errorf("status = %+v", run.Status)
	}
	if !HasStep(run.Status.Steps, StepCleanupDone) || len(run.Status.Steps) != 6 {
		t.Errorf("steps = %+v", run.Status.Steps)
	}
	waitCleanedUp(t, c, ns, "ok")
}

// waitCleanedUp waits until the run has no finalizer, no memory lock and no
// run-labeled objects other than the launcher Job and Pod.
func waitCleanedUp(t *testing.T, c client.Client, ns, run string) {
	t.Helper()
	ctx := context.Background()
	eventually(t, 20*time.Second, func() error {
		r := &v1alpha1.AgentRun{}
		err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: run}, r)
		if err == nil && len(r.Finalizers) > 0 {
			return fmt.Errorf("finalizers = %v", r.Finalizers)
		}
		if err != nil && !apierrors.IsNotFound(err) {
			return err
		}
		sel := client.MatchingLabels{LabelRun: run}
		for _, list := range []client.ObjectList{&corev1.ConfigMapList{}, &corev1.ServiceList{}, &networkingv1.NetworkPolicyList{}, &coordinationv1.LeaseList{}} {
			if err := c.List(ctx, list, client.InNamespace(ns), sel); err != nil {
				return err
			}
			if n := lenList(list); n > 0 {
				return fmt.Errorf("%T has %d run objects", list, n)
			}
		}
		pods := &corev1.PodList{}
		if err := c.List(ctx, pods, client.InNamespace(ns), sel); err != nil {
			return err
		}
		for _, p := range pods.Items {
			if p.Labels[LabelComponent] != ComponentLauncher {
				return fmt.Errorf("pod %s remains", p.Name)
			}
		}
		lease := &coordinationv1.Lease{}
		if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: MemoryLockName("example")}, lease); err == nil {
			return fmt.Errorf("memory lock still held by %s", ptr.Deref(lease.Spec.HolderIdentity, ""))
		}
		return nil
	})
}

func lenList(list client.ObjectList) int {
	switch l := list.(type) {
	case *corev1.ConfigMapList:
		return len(l.Items)
	case *corev1.ServiceList:
		return len(l.Items)
	case *networkingv1.NetworkPolicyList:
		return len(l.Items)
	case *coordinationv1.LeaseList:
		return len(l.Items)
	}
	return 0
}

func TestRunDeadlineBeforeHarnessIsNoCapacity(t *testing.T) {
	c, ns, logs := setupRunTest(t, v1alpha1.MemoryDisabled, 1)
	startController(t, ns, testConfig(ns), logs)
	mustCreate(t, c, newRun(ns, "slow", "example", nil))
	waitPhase(t, c, ns, "slow", v1alpha1.PhaseStarting)
	pod := startLauncherPod(t, c, ns, "slow")
	logs.set(pod.Name, stepLine(StepAdmitted))
	finishJob(t, c, ns, "slow", false, batchv1.JobReasonDeadlineExceeded)

	run := waitPhase(t, c, ns, "slow", v1alpha1.PhaseFailed)
	if run.Status.Message != MessageNoCapacity {
		t.Errorf("message = %q, want %q", run.Status.Message, MessageNoCapacity)
	}
}

// createRunObjects creates the objects the launcher would make for a run.
func createRunObjects(t *testing.T, c client.Client, ns, run string) {
	t.Helper()
	labels := map[string]string{LabelRun: run}
	mustCreate(t, c,
		&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: run + "-proxy", Namespace: ns, Labels: labels},
			Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 3128}}},
		},
		&networkingv1.NetworkPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: run + "-deny", Namespace: ns, Labels: labels},
			Spec:       networkingv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{MatchLabels: labels}},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: run + "-proxy", Namespace: ns, Labels: map[string]string{LabelRun: run, LabelComponent: "proxy"}},
			Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "proxy", Image: "example.com/proxy"}}},
		},
	)
}

func TestRunCancelLeavesNoRunObjects(t *testing.T) {
	c, ns, logs := setupRunTest(t, v1alpha1.MemoryEnabled, 1)
	startController(t, ns, testConfig(ns), logs)
	mustCreate(t, c, newRun(ns, "stop", "example", nil))
	waitPhase(t, c, ns, "stop", v1alpha1.PhaseStarting)
	createRunObjects(t, c, ns, "stop")
	pod := startLauncherPod(t, c, ns, "stop")
	logs.set(pod.Name, stepLine(StepHarnessStarted))
	waitPhase(t, c, ns, "stop", v1alpha1.PhaseRunning)

	run := getRun(t, c, ns, "stop")
	run.Spec.Cancel = true
	if err := c.Update(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	waitPhase(t, c, ns, "stop", v1alpha1.PhaseCancelled)
	waitCleanedUp(t, c, ns, "stop")
	eventually(t, 10*time.Second, func() error {
		if jobExists(t, c, ns, "stop") {
			return fmt.Errorf("job still exists")
		}
		return nil
	})
}

func TestRunDeletionRemovesJobAndObjects(t *testing.T) {
	c, ns, logs := setupRunTest(t, v1alpha1.MemoryEnabled, 1)
	startController(t, ns, testConfig(ns), logs)
	mustCreate(t, c, newRun(ns, "gone", "example", nil))
	waitPhase(t, c, ns, "gone", v1alpha1.PhaseStarting)
	createRunObjects(t, c, ns, "gone")

	if err := c.Delete(context.Background(), getRun(t, c, ns, "gone")); err != nil {
		t.Fatal(err)
	}
	waitCleanedUp(t, c, ns, "gone")
	eventually(t, 10*time.Second, func() error {
		if jobExists(t, c, ns, "gone") {
			return fmt.Errorf("job still exists")
		}
		err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "gone"}, &v1alpha1.AgentRun{})
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("run still exists: %v", err)
		}
		return nil
	})
}

func TestRunSurvivesControllerRestart(t *testing.T) {
	c, ns, logs := setupRunTest(t, v1alpha1.MemoryDisabled, 1)
	cfg := testConfig(ns)

	// First controller: admit the run, then stop.
	stopFirst := startControllerStoppable(t, ns, cfg, logs)
	mustCreate(t, c, newRun(ns, "long", "example", nil))
	waitPhase(t, c, ns, "long", v1alpha1.PhaseStarting)
	pod := startLauncherPod(t, c, ns, "long")
	logs.set(pod.Name, stepLine(StepHarnessStarted))
	waitPhase(t, c, ns, "long", v1alpha1.PhaseRunning)
	stopFirst()

	// While no controller runs, a second run arrives; maxParallel is 1.
	mustCreate(t, c, newRun(ns, "next", "example", nil))

	startController(t, ns, cfg, logs)
	waitPhase(t, c, ns, "next", v1alpha1.PhaseQueued)
	time.Sleep(2 * time.Second)
	if p := getRun(t, c, ns, "next").Status.Phase; p != v1alpha1.PhaseQueued {
		t.Fatalf("next phase = %s; the restarted controller double-counted slots", p)
	}

	terminateLauncher(t, c, pod, `{"harnessExit": 0, "error": ""}`)
	finishJob(t, c, ns, "long", true, "")
	waitPhase(t, c, ns, "long", v1alpha1.PhaseSucceeded)
	waitPhase(t, c, ns, "next", v1alpha1.PhaseStarting)
}

// startControllerStoppable runs the queue and run reconcilers and returns
// a function that stops them.
func startControllerStoppable(t *testing.T, ns string, cfg Config, logs LogReader) func() {
	t.Helper()
	return startManagerStoppable(t, ns, func(mgr ctrl.Manager) error {
		if err := newQueueReconciler(t, mgr, cfg).SetupWithManager(mgr); err != nil {
			return err
		}
		return (&RunReconciler{Client: mgr.GetClient(), Reader: mgr.GetAPIReader(), Logs: logs, Cfg: cfg, Clock: clock.RealClock{}}).SetupWithManager(mgr)
	})
}

func TestRunPolicyChangeRejectsQueuedKeepsRunning(t *testing.T) {
	c, ns, logs := setupRunTest(t, v1alpha1.MemoryDisabled, 1)
	startController(t, ns, testConfig(ns), logs)
	mustCreate(t, c, newRun(ns, "busy", "example", nil))
	waitPhase(t, c, ns, "busy", v1alpha1.PhaseStarting)
	pod := startLauncherPod(t, c, ns, "busy")
	logs.set(pod.Name, stepLine(StepHarnessStarted))
	waitPhase(t, c, ns, "busy", v1alpha1.PhaseRunning)
	mustCreate(t, c, newRun(ns, "waiting", "example", nil))
	waitPhase(t, c, ns, "waiting", v1alpha1.PhaseQueued)

	pol := &v1alpha1.AgentPolicy{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: v1alpha1.PolicyName}, pol); err != nil {
		t.Fatal(err)
	}
	pol.Spec.Egress.Hosts = []string{"models.example.com:443"}
	if err := c.Update(context.Background(), pol); err != nil {
		t.Fatal(err)
	}
	run := waitPhase(t, c, ns, "waiting", v1alpha1.PhaseRejected)
	if !strings.Contains(run.Status.Message, "egress.extraHosts") {
		t.Errorf("message = %q", run.Status.Message)
	}
	waitValid(t, c, ns, "example", metav1.ConditionFalse, "egress.extraHosts")
	if p := getRun(t, c, ns, "busy").Status.Phase; p != v1alpha1.PhaseRunning {
		t.Errorf("running run phase = %s, want Running", p)
	}
}

const holdFinalizer = "example.com/hold"

func leaseExists(t *testing.T, c client.Client, ns, name string) bool {
	t.Helper()
	err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: name}, &coordinationv1.Lease{})
	if err != nil && !apierrors.IsNotFound(err) {
		t.Fatal(err)
	}
	return err == nil
}

func releaseHold(t *testing.T, c client.Client, ns, pod string) {
	t.Helper()
	p := &corev1.Pod{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: pod}, p); err != nil {
		t.Fatal(err)
	}
	p.Finalizers = nil
	if err := c.Update(context.Background(), p); err != nil {
		t.Fatal(err)
	}
}

// assertLocksHeld checks for a while that both locks stay.
func assertLocksHeld(t *testing.T, c client.Client, ns string, locks ...string) {
	t.Helper()
	for range 20 {
		for _, l := range locks {
			if !leaseExists(t, c, ns, l) {
				t.Fatalf("lock %s released while run objects remain", l)
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestRunLocksHeldUntilSandboxGone(t *testing.T) {
	c, ns, logs := setupRunTest(t, v1alpha1.MemoryEnabled, 1)
	startController(t, ns, testConfig(ns), logs)
	mustCreate(t, c, newRun(ns, "slowgone", "example", nil))
	waitPhase(t, c, ns, "slowgone", v1alpha1.PhaseStarting)

	// The launcher takes its own lock and starts a proxy that is slow to go.
	mustCreate(t, c,
		&coordinationv1.Lease{
			ObjectMeta: metav1.ObjectMeta{Name: "launcher-lock", Namespace: ns, Labels: map[string]string{LabelRun: "slowgone"}},
			Spec:       coordinationv1.LeaseSpec{HolderIdentity: ptr.To("slowgone")},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: "slowgone-proxy", Namespace: ns, Finalizers: []string{holdFinalizer},
				Labels: map[string]string{LabelRun: "slowgone", LabelComponent: "proxy"},
			},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "proxy", Image: "example.com/proxy"}}},
		},
	)
	pod := startLauncherPod(t, c, ns, "slowgone")
	logs.set(pod.Name, stepLine(StepHarnessStarted))
	waitPhase(t, c, ns, "slowgone", v1alpha1.PhaseRunning)
	terminateLauncher(t, c, pod, `{"harnessExit": 0, "error": ""}`)
	finishJob(t, c, ns, "slowgone", true, "")
	waitPhase(t, c, ns, "slowgone", v1alpha1.PhaseSucceeded)

	assertLocksHeld(t, c, ns, MemoryLockName("example"), "launcher-lock")
	releaseHold(t, c, ns, "slowgone-proxy")
	waitCleanedUp(t, c, ns, "slowgone")
	if leaseExists(t, c, ns, "launcher-lock") {
		t.Error("launcher lock not released after cleanup")
	}
}

func TestRunCancelWaitsForLauncherPod(t *testing.T) {
	c, ns, logs := setupRunTest(t, v1alpha1.MemoryEnabled, 1)
	startController(t, ns, testConfig(ns), logs)
	mustCreate(t, c, newRun(ns, "stopping", "example", nil))
	waitPhase(t, c, ns, "stopping", v1alpha1.PhaseStarting)
	pod := startLauncherPod(t, c, ns, "stopping")
	pod.Finalizers = []string{holdFinalizer}
	if err := c.Update(context.Background(), pod); err != nil {
		t.Fatal(err)
	}
	logs.set(pod.Name, stepLine(StepHarnessStarted))
	waitPhase(t, c, ns, "stopping", v1alpha1.PhaseRunning)

	run := getRun(t, c, ns, "stopping")
	run.Spec.Cancel = true
	if err := c.Update(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	waitPhase(t, c, ns, "stopping", v1alpha1.PhaseCancelled)
	assertLocksHeld(t, c, ns, MemoryLockName("example"))

	// The controller asked the launcher Pod to stop.
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(pod), pod); err != nil || pod.DeletionTimestamp.IsZero() {
		t.Fatalf("launcher pod not being deleted: %v", err)
	}
	releaseHold(t, c, ns, pod.Name)
	waitCleanedUp(t, c, ns, "stopping")
}

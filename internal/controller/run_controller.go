package controller

import (
	"context"
	"fmt"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/clock"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/mudler/agent-controller/api/v1alpha1"
)

const (
	// stepPollInterval is how often a starting run reads launcher steps.
	stepPollInterval = 15 * time.Second
	// cleanupRetry is how soon a run whose cleanup is not done retries.
	cleanupRetry = 2 * time.Second
	// jobGracePeriod is how long a new run may miss its Job in the cache.
	jobGracePeriod = 30 * time.Second
	// stepLogLimit bounds the log read while the run is starting.
	stepLogLimit = 1 << 20
	// stepTailLines is how many log lines are read after the Job ends.
	stepTailLines = 200
)

// RunReconciler mirrors the launcher Job into AgentRun status, handles
// cancel and deletion, cleans up run objects and releases the memory lock.
// It keeps no state of its own: everything comes from AgentRun status,
// Jobs, Pods and Leases.
type RunReconciler struct {
	client.Client
	// Reader lists run objects and locks without the cache.
	Reader client.Reader
	Logs   LogReader
	Cfg    Config
	Clock  clock.PassiveClock
}

// Reconcile updates one run.
func (r *RunReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	run := &v1alpha1.AgentRun{}
	if err := r.Get(ctx, req.NamespacedName, run); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !run.DeletionTimestamp.IsZero() {
		if err := r.deleteJob(ctx, run, metav1.DeletePropagationForeground); err != nil {
			return ctrl.Result{}, err
		}
		return r.finalize(ctx, run)
	}

	switch {
	case run.Status.Phase.Final():
		return r.finalize(ctx, run)
	case run.Status.Phase == v1alpha1.PhaseStarting || run.Status.Phase == v1alpha1.PhaseRunning:
		return r.track(ctx, run)
	default:
		return ctrl.Result{}, nil
	}
}

// track follows a started run until its Job ends or it is cancelled.
func (r *RunReconciler) track(ctx context.Context, run *v1alpha1.AgentRun) (ctrl.Result, error) {
	status := run.Status.DeepCopy()
	if run.Spec.Cancel {
		if err := r.deleteJob(ctx, run, metav1.DeletePropagationForeground); err != nil {
			return ctrl.Result{}, err
		}
		r.complete(status, v1alpha1.PhaseCancelled, "cancelled", nil)
		return r.writeStatus(ctx, run, status, ctrl.Result{RequeueAfter: time.Millisecond})
	}

	job := &batchv1.Job{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: run.Namespace, Name: jobName(run)}, job); err != nil {
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("getting launcher job: %w", err)
		}
		if run.Status.StartTime != nil && r.Clock.Since(run.Status.StartTime.Time) < jobGracePeriod {
			return ctrl.Result{RequeueAfter: cleanupRetry}, nil
		}
		r.complete(status, v1alpha1.PhaseFailed, "launcher Job not found", nil)
		return r.writeStatus(ctx, run, status, ctrl.Result{RequeueAfter: time.Millisecond})
	}
	status.JobName = job.Name

	pod, err := r.launcherPod(ctx, run)
	if err != nil {
		return ctrl.Result{}, err
	}
	if pod != nil {
		status.PodName = pod.Name
	}

	_, _, _, finished := FinalPhase(job, nil, nil)
	switch {
	case finished:
		var term *Termination
		if pod != nil {
			term, _ = ParseTermination(pod)
			if log, err := r.Logs.Tail(ctx, run.Namespace, pod.Name, LauncherContainer, stepTailLines); err == nil {
				status.Steps = mergeSteps(status.Steps, ParseSteps(log))
			} else {
				ctrl.LoggerFrom(ctx).Info("reading launcher log tail failed", "error", err.Error())
			}
		}
		phase, msg, exit, _ := FinalPhase(job, term, status.Steps)
		r.complete(status, phase, msg, exit)
		return r.writeStatus(ctx, run, status, ctrl.Result{RequeueAfter: time.Millisecond})
	case pod != nil && run.Status.Phase == v1alpha1.PhaseStarting:
		// Before the harness starts, the log holds only launcher lines.
		if log, err := r.Logs.Since(ctx, run.Namespace, pod.Name, LauncherContainer, nil, stepLogLimit); err == nil {
			status.Steps = mergeSteps(status.Steps, ParseSteps(log))
		} else {
			ctrl.LoggerFrom(ctx).Info("reading launcher log failed", "error", err.Error())
		}
		if HasStep(status.Steps, StepHarnessStarted) {
			status.Phase = v1alpha1.PhaseRunning
			status.Message = "harness running"
		}
	}
	return r.writeStatus(ctx, run, status, ctrl.Result{RequeueAfter: stepPollInterval})
}

func (r *RunReconciler) complete(status *v1alpha1.AgentRunStatus, phase v1alpha1.Phase, msg string, exit *int32) {
	now := metav1.NewTime(r.Clock.Now())
	status.Phase = phase
	status.Message = msg
	status.ExitCode = exit
	status.CompletionTime = &now
}

func (r *RunReconciler) writeStatus(ctx context.Context, run *v1alpha1.AgentRun, status *v1alpha1.AgentRunStatus, res ctrl.Result) (ctrl.Result, error) {
	if equality.Semantic.DeepEqual(&run.Status, status) {
		return res, nil
	}
	run.Status = *status
	if err := r.Status().Update(ctx, run); err != nil {
		if apierrors.IsConflict(err) || apierrors.IsNotFound(err) {
			return ctrl.Result{RequeueAfter: time.Second}, nil
		}
		return ctrl.Result{}, fmt.Errorf("updating run status: %w", err)
	}
	return res, nil
}

// finalize cleans up after a final phase or a deletion, then releases the
// locks and removes the finalizer. Locks go only after a cleanup pass finds
// no run objects left, so no sandbox outlives its memory lock. For a
// cancelled or deleted run the launcher Pod is stopped first.
func (r *RunReconciler) finalize(ctx context.Context, run *v1alpha1.AgentRun) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(run, FinalizerCleanup) {
		return ctrl.Result{}, nil
	}
	stop := run.Spec.Cancel || !run.DeletionTimestamp.IsZero() || run.Status.Phase == v1alpha1.PhaseCancelled
	done, err := Cleanup(ctx, r.Client, r.Reader, run.Namespace, run.Name, CleanupOptions{StopLauncher: stop})
	if err != nil {
		return ctrl.Result{}, err
	}
	if !done {
		return ctrl.Result{RequeueAfter: cleanupRetry}, nil
	}
	if err := ReleaseLocks(ctx, r.Client, r.Reader, run.Namespace, run.Name); err != nil {
		return ctrl.Result{}, err
	}
	controllerutil.RemoveFinalizer(run, FinalizerCleanup)
	if err := r.Update(ctx, run); err != nil {
		if apierrors.IsConflict(err) {
			return ctrl.Result{RequeueAfter: time.Second}, nil
		}
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	return ctrl.Result{}, nil
}

func (r *RunReconciler) deleteJob(ctx context.Context, run *v1alpha1.AgentRun, propagation metav1.DeletionPropagation) error {
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Namespace: run.Namespace, Name: jobName(run)}}
	err := r.Delete(ctx, job, client.PropagationPolicy(propagation))
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting launcher job: %w", err)
	}
	return nil
}

func jobName(run *v1alpha1.AgentRun) string {
	if run.Status.JobName != "" {
		return run.Status.JobName
	}
	return run.Name
}

// launcherPod returns the newest launcher Pod of the run, or nil.
func (r *RunReconciler) launcherPod(ctx context.Context, run *v1alpha1.AgentRun) (*corev1.Pod, error) {
	pods := &corev1.PodList{}
	err := r.List(ctx, pods, client.InNamespace(run.Namespace),
		client.MatchingLabels{LabelRun: run.Name, LabelComponent: ComponentLauncher})
	if err != nil {
		return nil, fmt.Errorf("listing launcher pods: %w", err)
	}
	var newest *corev1.Pod
	for i := range pods.Items {
		p := &pods.Items[i]
		if newest == nil || p.CreationTimestamp.After(newest.CreationTimestamp.Time) {
			newest = p
		}
	}
	return newest, nil
}

// mergeSteps keeps existing steps and appends new step names.
func mergeSteps(existing, parsed []v1alpha1.Step) []v1alpha1.Step {
	out := append([]v1alpha1.Step{}, existing...)
	for _, s := range parsed {
		if !HasStep(out, s.Name) {
			out = append(out, s)
		}
	}
	return out
}

// SetupWithManager registers the reconciler. It watches AgentRuns, their
// Jobs and their run-labeled Pods.
func (r *RunReconciler) SetupWithManager(mgr ctrl.Manager) error {
	podToRun := handler.EnqueueRequestsFromMapFunc(func(_ context.Context, obj client.Object) []reconcile.Request {
		run := obj.GetLabels()[LabelRun]
		if run == "" {
			return nil
		}
		return []reconcile.Request{{NamespacedName: client.ObjectKey{Namespace: obj.GetNamespace(), Name: run}}}
	})
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.AgentRun{}).
		Owns(&batchv1.Job{}).
		Watches(&corev1.Pod{}, podToRun).
		Named("agentrun").
		Complete(r)
}

package controller

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/mudler/agent-controller/api/v1alpha1"
	"github.com/mudler/agent-controller/internal/admission"
	"github.com/mudler/agent-controller/internal/policy"
)

// QueueKey is the name of the single queue reconcile request.
const QueueKey = "queue"

// DefaultRetainPerProfile is how many finished runs are kept per profile.
const DefaultRetainPerProfile = 200

// queueResync bounds how long the queue waits without any event.
const queueResync = 30 * time.Second

// memoryClaimSize is the size of a new memory PVC.
var memoryClaimSize = resource.MustParse("1Gi")

var errLockBusy = errors.New("memory lock held by another run")

// QueueReconciler admits queued runs. It runs as one singleton reconcile
// that reads everything through the uncached reader, so a run admitted in
// the previous pass is never counted twice.
type QueueReconciler struct {
	client.Client
	Reader      client.Reader
	Cfg         Config
	JobTemplate *batchv1.Job
}

// Reconcile runs one admission pass.
func (r *QueueReconciler) Reconcile(ctx context.Context, _ ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	snap, runs, err := r.snapshot(ctx)
	if err != nil {
		return ctrl.Result{}, err
	}

	// A queued run with cancel set never starts.
	for name, run := range runs {
		if isQueued(run) && run.Spec.Cancel {
			if err := r.finish(ctx, run, v1alpha1.PhaseCancelled, "cancelled before it started"); err != nil {
				return ctrl.Result{}, err
			}
			delete(runs, name)
		}
	}
	snap.Runs = slices.DeleteFunc(snap.Runs, func(a admission.Run) bool { _, ok := runs[a.Name]; return !ok })

	d := admission.Decide(snap)
	var errs []error
	for name, reason := range d.Reject {
		if err := r.finish(ctx, runs[name], v1alpha1.PhaseRejected, reason); err != nil {
			errs = append(errs, err)
		}
	}
	for _, name := range d.Admit {
		err := r.admit(ctx, runs[name])
		switch {
		case errors.Is(err, errLockBusy):
			log.Info("memory lock busy; run stays queued", "run", name)
		case err != nil:
			errs = append(errs, fmt.Errorf("admitting %s: %w", name, err))
		}
	}
	for name, pos := range d.QueuePosition {
		run := runs[name]
		if pos == 0 || (run.Status.Phase == v1alpha1.PhaseQueued && run.Status.QueuePosition == pos) {
			continue
		}
		run.Status.Phase = v1alpha1.PhaseQueued
		run.Status.QueuePosition = pos
		if err := r.Status().Update(ctx, run); err != nil && !apierrors.IsConflict(err) && !apierrors.IsNotFound(err) {
			errs = append(errs, fmt.Errorf("updating queue position of %s: %w", name, err))
		}
	}
	if err := r.enforceRetention(ctx); err != nil {
		errs = append(errs, err)
	}
	if err := errors.Join(errs...); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: queueResync}, nil
}

func isQueued(run *v1alpha1.AgentRun) bool {
	return run.Status.Phase == "" || run.Status.Phase == v1alpha1.PhaseQueued
}

// snapshot reads runs, profiles and memory locks into an admission
// snapshot. The map holds every non-final run that is not being deleted.
func (r *QueueReconciler) snapshot(ctx context.Context) (admission.Snapshot, map[string]*v1alpha1.AgentRun, error) {
	ns := r.Cfg.Namespace
	snap := admission.Snapshot{Profiles: map[string]admission.ProfileState{}, MemoryLockHolder: map[string]string{}}
	runs := map[string]*v1alpha1.AgentRun{}

	env, err := LoadEnv(ctx, r.Reader, ns, r.Cfg.HarnessVersions)
	if err != nil {
		return snap, nil, err
	}
	if env.Policy != nil {
		snap.GlobalSlots = env.Policy.Spec.Limits.GlobalSlots
	}

	profiles := &v1alpha1.AgentProfileList{}
	if err := r.Reader.List(ctx, profiles, client.InNamespace(ns)); err != nil {
		return snap, nil, fmt.Errorf("listing profiles: %w", err)
	}
	specs := map[string]*v1alpha1.AgentProfileSpec{}
	for i := range profiles.Items {
		p := &profiles.Items[i]
		if !p.DeletionTimestamp.IsZero() {
			continue
		}
		specs[p.Name] = &p.Spec
		errs := policy.ValidateProfile(env, &p.Spec)
		state := admission.ProfileState{Exists: true, Valid: errs == nil, Validated: policy.ValidationCurrent(env, p), MaxParallel: p.Spec.MaxParallel}
		if errs != nil {
			state.Reason = errs.Error()
		}
		snap.Profiles[p.Name] = state
	}

	list := &v1alpha1.AgentRunList{}
	if err := r.Reader.List(ctx, list, client.InNamespace(ns)); err != nil {
		return snap, nil, fmt.Errorf("listing runs: %w", err)
	}
	allRuns := map[string]*v1alpha1.AgentRun{}
	for i := range list.Items {
		run := &list.Items[i]
		allRuns[run.Name] = run
		if run.Status.Phase.Final() || (!run.DeletionTimestamp.IsZero() && isQueued(run)) {
			continue
		}
		runs[run.Name] = run
		usesMemory := false
		if spec, ok := specs[run.Spec.Profile]; ok {
			usesMemory = UsesMemory(spec, &run.Spec)
		}
		snap.Runs = append(snap.Runs, admission.Run{
			Name:       run.Name,
			Profile:    run.Spec.Profile,
			Created:    run.CreationTimestamp.Time,
			Phase:      run.Status.Phase,
			UsesMemory: usesMemory,
			Trigger:    run.Spec.Trigger,
		})
	}

	leases := &coordinationv1.LeaseList{}
	if err := r.Reader.List(ctx, leases, client.InNamespace(ns)); err != nil {
		return snap, nil, fmt.Errorf("listing leases: %w", err)
	}
	for i := range leases.Items {
		lease := &leases.Items[i]
		profile, ok := strings.CutPrefix(lease.Name, "memory-")
		if !ok {
			continue
		}
		holder := ptr.Deref(lease.Spec.HolderIdentity, "")
		holderRun, exists := allRuns[holder]
		switch {
		case !exists:
			// The holder run is gone; its lock is stale.
			if err := deleteLease(ctx, r.Client, lease); err != nil {
				return snap, nil, err
			}
		case isQueued(holderRun) && holderRun.DeletionTimestamp.IsZero():
			// A run that took the lock but was not marked Starting before
			// a restart; admitting it again reuses the lock.
		default:
			snap.MemoryLockHolder[profile] = holder
		}
	}
	return snap, runs, nil
}

// admit starts one run: memory claim and lock, finalizer, resolved run
// ConfigMap, launcher Job, then status Starting. Every create that finds
// the object already there counts as done, so admit can repeat after a
// restart.
func (r *QueueReconciler) admit(ctx context.Context, run *v1alpha1.AgentRun) error {
	ns := run.Namespace
	prof := &v1alpha1.AgentProfile{}
	if err := r.Reader.Get(ctx, client.ObjectKey{Namespace: ns, Name: run.Spec.Profile}, prof); err != nil {
		return fmt.Errorf("getting profile: %w", err)
	}
	h := &v1alpha1.HarnessProfile{}
	if err := r.Reader.Get(ctx, client.ObjectKey{Namespace: ns, Name: prof.Spec.Harness}, h); err != nil {
		return fmt.Errorf("getting harness profile: %w", err)
	}
	gh := &v1alpha1.Login{}
	if err := r.Reader.Get(ctx, client.ObjectKey{Namespace: ns, Name: prof.Spec.GitHub}, gh); err != nil {
		return fmt.Errorf("getting github login: %w", err)
	}
	model := &v1alpha1.Login{}
	if err := r.Reader.Get(ctx, client.ObjectKey{Namespace: ns, Name: h.Spec.Login}, model); err != nil {
		return fmt.Errorf("getting model login: %w", err)
	}
	spec, err := Resolve(r.Cfg, run, prof, h, gh, model)
	if err != nil {
		return fmt.Errorf("resolving run: %w", err)
	}

	if spec.Memory.Enabled {
		if err := r.ensureMemoryClaim(ctx, ns, spec.Memory.Claim); err != nil {
			return err
		}
		if err := r.takeMemoryLock(ctx, run, spec.Memory.Lock); err != nil {
			return err
		}
	}

	if controllerutil.AddFinalizer(run, FinalizerCleanup) {
		if err := r.Update(ctx, run); err != nil {
			return fmt.Errorf("adding finalizer: %w", err)
		}
	}
	if err := r.Create(ctx, spec.ConfigMap(run)); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating resolved run: %w", err)
	}
	job := BuildJob(r.JobTemplate, r.Cfg, run, spec)
	if err := r.Create(ctx, job); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating launcher job: %w", err)
	}

	now := metav1.Now()
	run.Status.Phase = v1alpha1.PhaseStarting
	run.Status.QueuePosition = 0
	run.Status.ProfileVersion = prof.Generation
	run.Status.JobName = job.Name
	run.Status.StartTime = &now
	run.Status.Message = "launcher Job created"
	if err := r.Status().Update(ctx, run); err != nil {
		return fmt.Errorf("setting run Starting: %w", err)
	}
	return nil
}

func (r *QueueReconciler) ensureMemoryClaim(ctx context.Context, ns, name string) error {
	pvc := &corev1.PersistentVolumeClaim{}
	err := r.Reader.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, pvc)
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("getting memory claim: %w", err)
	}
	pvc = &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: memoryClaimSize},
			},
		},
	}
	if r.Cfg.MemoryStorageClass != "" {
		pvc.Spec.StorageClassName = ptr.To(r.Cfg.MemoryStorageClass)
	}
	if err := r.Create(ctx, pvc); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating memory claim: %w", err)
	}
	return nil
}

func (r *QueueReconciler) takeMemoryLock(ctx context.Context, run *v1alpha1.AgentRun, name string) error {
	now := metav1.NewMicroTime(time.Now())
	lease := &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{
			Name:            name,
			Namespace:       run.Namespace,
			Labels:          map[string]string{LabelRun: run.Name},
			OwnerReferences: []metav1.OwnerReference{runOwnerRef(run)},
		},
		Spec: coordinationv1.LeaseSpec{HolderIdentity: ptr.To(run.Name), AcquireTime: &now},
	}
	err := r.Create(ctx, lease)
	if err == nil {
		return nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating memory lock: %w", err)
	}
	existing := &coordinationv1.Lease{}
	if err := r.Reader.Get(ctx, client.ObjectKeyFromObject(lease), existing); err != nil {
		return fmt.Errorf("getting memory lock: %w", err)
	}
	if ptr.Deref(existing.Spec.HolderIdentity, "") != run.Name {
		return errLockBusy
	}
	return nil
}

// finish sets a final phase on a run that never started.
func (r *QueueReconciler) finish(ctx context.Context, run *v1alpha1.AgentRun, phase v1alpha1.Phase, message string) error {
	now := metav1.Now()
	run.Status.Phase = phase
	run.Status.Message = message
	run.Status.QueuePosition = 0
	run.Status.CompletionTime = &now
	if err := r.Status().Update(ctx, run); err != nil && !apierrors.IsConflict(err) && !apierrors.IsNotFound(err) {
		return fmt.Errorf("setting %s on %s: %w", phase, run.Name, err)
	}
	return nil
}

// enforceRetention keeps the newest finished runs of each profile and
// deletes older ones.
func (r *QueueReconciler) enforceRetention(ctx context.Context) error {
	keep := r.Cfg.RetainPerProfile
	if keep <= 0 {
		keep = DefaultRetainPerProfile
	}
	list := &v1alpha1.AgentRunList{}
	if err := r.Reader.List(ctx, list, client.InNamespace(r.Cfg.Namespace)); err != nil {
		return fmt.Errorf("listing runs: %w", err)
	}
	finished := map[string][]*v1alpha1.AgentRun{}
	for i := range list.Items {
		run := &list.Items[i]
		if run.Status.Phase.Final() && run.DeletionTimestamp.IsZero() {
			finished[run.Spec.Profile] = append(finished[run.Spec.Profile], run)
		}
	}
	var errs []error
	for _, runs := range finished {
		if len(runs) <= keep {
			continue
		}
		slices.SortFunc(runs, func(a, b *v1alpha1.AgentRun) int {
			return cmp.Or(completedAt(b).Compare(completedAt(a)), cmp.Compare(b.Name, a.Name))
		})
		for _, run := range runs[keep:] {
			if err := r.Delete(ctx, run); err != nil && !apierrors.IsNotFound(err) {
				errs = append(errs, fmt.Errorf("deleting old run %s: %w", run.Name, err))
			}
		}
	}
	return errors.Join(errs...)
}

func completedAt(run *v1alpha1.AgentRun) time.Time {
	if run.Status.CompletionTime != nil {
		return run.Status.CompletionTime.Time
	}
	return run.CreationTimestamp.Time
}

func deleteLease(ctx context.Context, c client.Client, lease *coordinationv1.Lease) error {
	err := c.Delete(ctx, lease, client.Preconditions{UID: ptr.To(lease.UID)})
	if err != nil && !apierrors.IsNotFound(err) && !apierrors.IsConflict(err) {
		return fmt.Errorf("deleting lease %s: %w", lease.Name, err)
	}
	return nil
}

// SetupWithManager registers the queue. Every AgentRun, AgentProfile,
// HarnessProfile, Login, AgentPolicy and Lease event maps to the one queue
// request.
func (r *QueueReconciler) SetupWithManager(mgr ctrl.Manager) error {
	key := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: r.Cfg.Namespace, Name: QueueKey}}
	toQueue := handler.EnqueueRequestsFromMapFunc(func(context.Context, client.Object) []reconcile.Request {
		return []reconcile.Request{key}
	})
	return ctrl.NewControllerManagedBy(mgr).
		Named("queue").
		Watches(&v1alpha1.AgentRun{}, toQueue).
		Watches(&v1alpha1.AgentProfile{}, toQueue).
		Watches(&v1alpha1.HarnessProfile{}, toQueue).
		Watches(&v1alpha1.Login{}, toQueue).
		Watches(&v1alpha1.AgentPolicy{}, toQueue).
		Watches(&coordinationv1.Lease{}, toQueue).
		WithOptions(controller.Options{MaxConcurrentReconciles: 1}).
		Complete(r)
}

package controller

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/clock"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/mudler/agent-controller/api/v1alpha1"
	"github.com/mudler/agent-controller/internal/admission"
	"github.com/mudler/agent-controller/internal/policy"
)

// Condition reasons.
const (
	ReasonPolicyFit       = "PolicyFit"
	ReasonPolicyViolation = "PolicyViolation"
	// RequestedBySchedule is the requestedBy of scheduled runs.
	RequestedBySchedule = "schedule"
)

// ProfileReconciler keeps the Valid condition, version and schedule of each
// AgentProfile, and creates scheduled runs.
type ProfileReconciler struct {
	client.Client
	// Reader reads runs without the cache, so a just-created scheduled run
	// is always seen.
	Reader client.Reader
	Cfg    Config
	Clock  clock.PassiveClock
}

// Reconcile updates one profile.
func (r *ProfileReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	prof := &v1alpha1.AgentProfile{}
	if err := r.Get(ctx, req.NamespacedName, prof); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !prof.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}
	env, err := LoadEnv(ctx, r.Client, prof.Namespace, r.Cfg.HarnessVersions)
	if err != nil {
		return ctrl.Result{}, err
	}

	status := prof.Status.DeepCopy()
	errs := policy.ValidateProfile(env, &prof.Spec)
	setValid(&status.Conditions, prof.Generation, errs)
	status.Version = prof.Generation
	status.LastChangedBy = prof.Annotations[AnnotationLastChangedBy]

	result, err := r.schedule(ctx, prof, status, errs == nil && policy.ValidationCurrent(env, prof))
	if err != nil {
		return ctrl.Result{}, err
	}

	if !equality.Semantic.DeepEqual(&prof.Status, status) {
		prof.Status = *status
		if err := r.Status().Update(ctx, prof); err != nil {
			if apierrors.IsConflict(err) {
				return ctrl.Result{RequeueAfter: time.Second}, nil
			}
			return ctrl.Result{}, fmt.Errorf("updating profile status: %w", err)
		}
	}
	return result, nil
}

func setValid(conds *[]metav1.Condition, generation int64, errs policy.Errors) {
	c := metav1.Condition{
		Type:               v1alpha1.ConditionValid,
		Status:             metav1.ConditionTrue,
		Reason:             ReasonPolicyFit,
		Message:            "fits the policy",
		ObservedGeneration: generation,
	}
	if errs != nil {
		c.Status = metav1.ConditionFalse
		c.Reason = ReasonPolicyViolation
		c.Message = errs.Error()
	}
	meta.SetStatusCondition(conds, c)
}

// schedule creates the run of a due tick and computes the next tick.
func (r *ProfileReconciler) schedule(
	ctx context.Context,
	prof *v1alpha1.AgentProfile,
	status *v1alpha1.AgentProfileStatus,
	runnable bool,
) (ctrl.Result, error) {
	s := prof.Spec.Schedule
	if s == nil || s.Suspend || !runnable {
		status.NextScheduleTime = nil
		return ctrl.Result{}, nil
	}
	now := r.Clock.Now()
	last := prof.CreationTimestamp.Time
	if status.LastScheduleTime != nil {
		last = status.LastScheduleTime.Time
	}

	tick, due, err := DueTick(s, last, now)
	if err != nil {
		// The profile is valid, so the schedule parses; this is unexpected.
		return ctrl.Result{}, fmt.Errorf("computing schedule: %w", err)
	}
	if due {
		if err := r.tick(ctx, prof, tick); err != nil {
			return ctrl.Result{}, err
		}
	}
	if !tick.IsZero() {
		status.LastScheduleTime = &metav1.Time{Time: tick}
	}

	next, err := NextTick(s, now)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("computing next tick: %w", err)
	}
	status.NextScheduleTime = &metav1.Time{Time: next}
	return ctrl.Result{RequeueAfter: next.Sub(now) + time.Second}, nil
}

func (r *ProfileReconciler) tick(ctx context.Context, prof *v1alpha1.AgentProfile, tick time.Time) error {
	runs, err := listActiveRuns(ctx, r.Reader, prof.Namespace)
	if err != nil {
		return err
	}
	if !admission.ShouldTick(prof.Name, runs) {
		ctrl.LoggerFrom(ctx).Info("skipping schedule tick; an earlier scheduled run is still waiting", "tick", tick)
		return nil
	}
	run := &v1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ScheduledRunName(prof.Name, tick),
			Namespace: prof.Namespace,
			Labels:    map[string]string{LabelProfile: prof.Name},
		},
		Spec: v1alpha1.AgentRunSpec{
			Profile:     prof.Name,
			Trigger:     v1alpha1.TriggerSchedule,
			RequestedBy: RequestedBySchedule,
		},
	}
	if err := r.Create(ctx, run); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating scheduled run: %w", err)
	}
	return nil
}

// listActiveRuns returns the admission view of all non-final runs.
func listActiveRuns(ctx context.Context, rd client.Reader, namespace string) ([]admission.Run, error) {
	list := &v1alpha1.AgentRunList{}
	if err := rd.List(ctx, list, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("listing runs: %w", err)
	}
	runs := make([]admission.Run, 0, len(list.Items))
	for i := range list.Items {
		run := &list.Items[i]
		if run.Status.Phase.Final() {
			continue
		}
		runs = append(runs, admission.Run{
			Name:    run.Name,
			Profile: run.Spec.Profile,
			Created: run.CreationTimestamp.Time,
			Phase:   run.Status.Phase,
			Trigger: run.Spec.Trigger,
		})
	}
	return runs, nil
}

// SetupWithManager registers the reconciler. Policy changes enqueue every
// profile; Login and HarnessProfile changes enqueue the profiles that use
// them.
func (r *ProfileReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.AgentProfile{}, builder.WithPredicates(predicate.Or(
			predicate.GenerationChangedPredicate{},
			predicate.AnnotationChangedPredicate{},
		))).
		Watches(&v1alpha1.AgentPolicy{}, handler.EnqueueRequestsFromMapFunc(r.allProfiles)).
		Watches(&v1alpha1.Login{}, handler.EnqueueRequestsFromMapFunc(r.profilesUsingLogin)).
		Watches(&v1alpha1.HarnessProfile{}, handler.EnqueueRequestsFromMapFunc(r.profilesUsingHarness)).
		Named("agentprofile").
		Complete(r)
}

func (r *ProfileReconciler) profiles(ctx context.Context, namespace string, keep func(*v1alpha1.AgentProfile) bool) []reconcile.Request {
	list := &v1alpha1.AgentProfileList{}
	if err := r.List(ctx, list, client.InNamespace(namespace)); err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "listing profiles")
		return nil
	}
	reqs := []reconcile.Request{}
	for i := range list.Items {
		if keep(&list.Items[i]) {
			reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&list.Items[i])})
		}
	}
	return reqs
}

func (r *ProfileReconciler) allProfiles(ctx context.Context, obj client.Object) []reconcile.Request {
	return r.profiles(ctx, obj.GetNamespace(), func(*v1alpha1.AgentProfile) bool { return true })
}

func (r *ProfileReconciler) profilesUsingHarness(ctx context.Context, obj client.Object) []reconcile.Request {
	return r.profiles(ctx, obj.GetNamespace(), func(p *v1alpha1.AgentProfile) bool { return p.Spec.Harness == obj.GetName() })
}

func (r *ProfileReconciler) profilesUsingLogin(ctx context.Context, obj client.Object) []reconcile.Request {
	harnesses := &v1alpha1.HarnessProfileList{}
	if err := r.List(ctx, harnesses, client.InNamespace(obj.GetNamespace())); err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "listing harness profiles")
		return nil
	}
	usesLogin := map[string]bool{}
	for _, h := range harnesses.Items {
		if h.Spec.Login == obj.GetName() {
			usesLogin[h.Name] = true
		}
	}
	return r.profiles(ctx, obj.GetNamespace(), func(p *v1alpha1.AgentProfile) bool {
		return p.Spec.GitHub == obj.GetName() || usesLogin[p.Spec.Harness]
	})
}

// HarnessReconciler keeps the Valid condition of each HarnessProfile.
type HarnessReconciler struct {
	client.Client
	Cfg Config
}

// Reconcile updates one harness profile.
func (r *HarnessReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	h := &v1alpha1.HarnessProfile{}
	if err := r.Get(ctx, req.NamespacedName, h); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	env, err := LoadEnv(ctx, r.Client, h.Namespace, r.Cfg.HarnessVersions)
	if err != nil {
		return ctrl.Result{}, err
	}
	status := h.Status.DeepCopy()
	setValid(&status.Conditions, h.Generation, policy.ValidateHarness(env, &h.Spec))
	if equality.Semantic.DeepEqual(&h.Status, status) {
		return ctrl.Result{}, nil
	}
	h.Status = *status
	if err := r.Status().Update(ctx, h); err != nil {
		if apierrors.IsConflict(err) {
			return ctrl.Result{RequeueAfter: time.Second}, nil
		}
		return ctrl.Result{}, fmt.Errorf("updating harness status: %w", err)
	}
	return ctrl.Result{}, nil
}

// SetupWithManager registers the reconciler. Policy and Login changes
// enqueue every harness profile.
func (r *HarnessReconciler) SetupWithManager(mgr ctrl.Manager) error {
	all := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
		list := &v1alpha1.HarnessProfileList{}
		if err := r.List(ctx, list, client.InNamespace(obj.GetNamespace())); err != nil {
			ctrl.LoggerFrom(ctx).Error(err, "listing harness profiles")
			return nil
		}
		reqs := make([]reconcile.Request, 0, len(list.Items))
		for i := range list.Items {
			reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&list.Items[i])})
		}
		return reqs
	})
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.HarnessProfile{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&v1alpha1.AgentPolicy{}, all).
		Watches(&v1alpha1.Login{}, all).
		Named("harnessprofile").
		Complete(r)
}

package controller

import (
	"context"
	"errors"
	"fmt"

	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// SandboxListGVK is the list kind of the Agent Sandbox objects that the
// launcher creates.
var SandboxListGVK = schema.GroupVersionKind{Group: "agents.x-k8s.io", Version: "v1beta1", Kind: "SandboxList"}

// CleanupOptions controls one cleanup pass.
type CleanupOptions struct {
	// StopLauncher deletes the launcher Pod too and waits until it is gone.
	// Use it for cancelled and deleted runs. Otherwise the launcher Pod is
	// kept for its log, and the pass waits only while it still runs.
	StopLauncher bool
}

// Cleanup deletes the run-labeled Sandboxes, Services, NetworkPolicies,
// ConfigMaps and non-launcher Pods of a run. It lists through the uncached
// reader r, Pods first. It never deletes Leases: locks are released by
// ReleaseLocks only after two passes in separate reconciles report done. done is true when a pass finds none of
// these objects and no running launcher Pod. A missing Sandbox CRD is not
// an error.
func Cleanup(ctx context.Context, c client.Client, r client.Reader, namespace, run string, opts CleanupOptions) (done bool, err error) {
	runSel := client.MatchingLabels{LabelRun: run}

	sandboxes := &unstructured.UnstructuredList{}
	sandboxes.SetGroupVersionKind(SandboxListGVK)

	remaining := 0
	var errs []error
	// Pods first: a launcher still running may create more objects, and a
	// pass that sees it is not done.
	pods := &corev1.PodList{}
	if err := r.List(ctx, pods, client.InNamespace(namespace), runSel); err != nil {
		errs = append(errs, fmt.Errorf("listing pods: %w", err))
	}
	for i := range pods.Items {
		pod := &pods.Items[i]
		launcher := pod.Labels[LabelComponent] == ComponentLauncher
		if launcher && !opts.StopLauncher {
			if !podFinished(pod) {
				remaining++
			}
			continue
		}
		remaining++
		errs = append(errs, deleteObject(ctx, c, pod))
	}

	for _, list := range []client.ObjectList{sandboxes, &corev1.ServiceList{}, &networkingv1.NetworkPolicyList{}, &corev1.ConfigMapList{}} {
		if err := r.List(ctx, list, client.InNamespace(namespace), runSel); err != nil {
			if meta.IsNoMatchError(err) || apierrors.IsNotFound(err) {
				continue
			}
			errs = append(errs, fmt.Errorf("listing %T: %w", list, err))
			continue
		}
		items, err := meta.ExtractList(list)
		if err != nil {
			errs = append(errs, fmt.Errorf("extracting %T: %w", list, err))
			continue
		}
		for _, item := range items {
			obj, ok := item.(client.Object)
			if !ok {
				continue
			}
			remaining++
			errs = append(errs, deleteObject(ctx, c, obj))
		}
	}

	if err := errors.Join(errs...); err != nil {
		return false, err
	}
	return remaining == 0, nil
}

func podFinished(pod *corev1.Pod) bool {
	return pod.DeletionTimestamp.IsZero() &&
		(pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed)
}

func deleteObject(ctx context.Context, c client.Client, obj client.Object) error {
	if !obj.GetDeletionTimestamp().IsZero() {
		return nil
	}
	err := c.Delete(ctx, obj, client.PropagationPolicy("Background"), client.Preconditions{UID: ptr.To(obj.GetUID())})
	if err != nil && !apierrors.IsNotFound(err) && !apierrors.IsConflict(err) {
		return fmt.Errorf("deleting %s: %w", obj.GetName(), err)
	}
	return nil
}

// ReleaseLocks deletes the run-labeled Leases of a run: the memory lock it
// holds and any lock the launcher took. Call it only after Cleanup reports
// done, so no sandbox can still write the memory volume.
func ReleaseLocks(ctx context.Context, c client.Client, r client.Reader, namespace, run string) error {
	leases := &coordinationv1.LeaseList{}
	if err := r.List(ctx, leases, client.InNamespace(namespace), client.MatchingLabels{LabelRun: run}); err != nil {
		return fmt.Errorf("listing locks: %w", err)
	}
	var errs []error
	for i := range leases.Items {
		lease := &leases.Items[i]
		if holder := ptr.Deref(lease.Spec.HolderIdentity, ""); holder != "" && holder != run {
			continue
		}
		errs = append(errs, deleteLease(ctx, c, lease))
	}
	return errors.Join(errs...)
}

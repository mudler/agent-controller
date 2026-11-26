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
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// SandboxListGVK is the list kind of the Agent Sandbox objects that the
// launcher creates.
var SandboxListGVK = schema.GroupVersionKind{Group: "agents.x-k8s.io", Version: "v1beta1", Kind: "SandboxList"}

// Cleanup deletes the run-labeled Sandboxes, Services, NetworkPolicies,
// ConfigMaps, Leases and non-launcher Pods of a run. The launcher Job and
// its Pod stay, so the log remains readable. done is true when a fresh list
// finds none of them. A missing Sandbox CRD is not an error.
func Cleanup(ctx context.Context, c client.Client, namespace, run string) (done bool, err error) {
	runSel := client.MatchingLabels{LabelRun: run}
	notLauncher, err := labels.NewRequirement(LabelComponent, selection.NotEquals, []string{ComponentLauncher})
	if err != nil {
		return false, fmt.Errorf("building selector: %w", err)
	}
	podSel := client.MatchingLabelsSelector{Selector: labels.SelectorFromSet(labels.Set{LabelRun: run}).Add(*notLauncher)}

	sandboxes := &unstructured.UnstructuredList{}
	sandboxes.SetGroupVersionKind(SandboxListGVK)

	lists := []struct {
		list client.ObjectList
		sel  client.ListOption
	}{
		{list: sandboxes, sel: runSel},
		{list: &corev1.ServiceList{}, sel: runSel},
		{list: &networkingv1.NetworkPolicyList{}, sel: runSel},
		{list: &corev1.ConfigMapList{}, sel: runSel},
		{list: &coordinationv1.LeaseList{}, sel: runSel},
		{list: &corev1.PodList{}, sel: podSel},
	}

	remaining := 0
	var errs []error
	for _, l := range lists {
		if err := c.List(ctx, l.list, client.InNamespace(namespace), l.sel); err != nil {
			if meta.IsNoMatchError(err) || apierrors.IsNotFound(err) {
				continue
			}
			errs = append(errs, fmt.Errorf("listing %T: %w", l.list, err))
			continue
		}
		items, err := meta.ExtractList(l.list)
		if err != nil {
			errs = append(errs, fmt.Errorf("extracting %T: %w", l.list, err))
			continue
		}
		for _, item := range items {
			obj, ok := item.(client.Object)
			if !ok {
				continue
			}
			remaining++
			if !obj.GetDeletionTimestamp().IsZero() {
				continue
			}
			err := c.Delete(ctx, obj, client.PropagationPolicy("Background"), client.Preconditions{UID: ptr.To(obj.GetUID())})
			if err != nil && !apierrors.IsNotFound(err) && !apierrors.IsConflict(err) {
				errs = append(errs, fmt.Errorf("deleting %s: %w", obj.GetName(), err))
			}
		}
	}
	if err := errors.Join(errs...); err != nil {
		return false, err
	}
	if remaining > 0 {
		// Deletions were issued; the next call confirms they are gone.
		return false, nil
	}
	return true, nil
}

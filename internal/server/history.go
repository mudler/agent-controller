package server

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mudler/agent-controller/api/v1alpha1"
	"github.com/mudler/agent-controller/internal/controller"
)

// HistoryKey is the data key of a history ConfigMap.
const HistoryKey = "history.json"

// HistoryLimit is how many previous versions are kept.
const HistoryLimit = 20

// DefaultHistoryNamespace is the default namespace of history ConfigMaps.
// It is separate from the runs namespace, so the server needs no ConfigMap
// write access where the launcher reads its resolved runs.
const DefaultHistoryNamespace = "agent-controller-history"

// HistoryEntry is one previous version of a profile spec.
type HistoryEntry struct {
	Version   int64                     `json:"version"`
	Spec      v1alpha1.AgentProfileSpec `json:"spec"`
	ChangedBy string                    `json:"changedBy"`
	At        string                    `json:"at"`
}

func historyName(profile string) string {
	return profile + "-history"
}

// readHistory returns the history of a profile, newest first.
func readHistory(ctx context.Context, r client.Reader, namespace, profile string) ([]HistoryEntry, error) {
	cm := &corev1.ConfigMap{}
	err := r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: historyName(profile)}, cm)
	if apierrors.IsNotFound(err) {
		return []HistoryEntry{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading history: %w", err)
	}
	entries := []HistoryEntry{}
	if err := json.Unmarshal([]byte(cm.Data[HistoryKey]), &entries); err != nil {
		return nil, fmt.Errorf("parsing history: %w", err)
	}
	return entries, nil
}

// appendHistory stores the current spec of prof as a previous version,
// before the caller updates it. It keeps HistoryLimit entries.
func appendHistory(ctx context.Context, c client.Client, namespace string, prof *v1alpha1.AgentProfile, changedBy string) error {
	entries, err := readHistory(ctx, c, namespace, prof.Name)
	if err != nil {
		return err
	}
	entry := HistoryEntry{
		Version:   prof.Generation,
		Spec:      prof.Spec,
		ChangedBy: changedBy,
		At:        time.Now().UTC().Format(time.RFC3339),
	}
	entries = append([]HistoryEntry{entry}, entries...)
	if len(entries) > HistoryLimit {
		entries = entries[:HistoryLimit]
	}
	data, err := json.Marshal(entries)
	if err != nil {
		return fmt.Errorf("encoding history: %w", err)
	}
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      historyName(prof.Name),
			Namespace: namespace,
			Labels:    map[string]string{controller.LabelProfile: prof.Name},
		},
		Data: map[string]string{HistoryKey: string(data)},
	}
	existing := &corev1.ConfigMap{}
	err = c.Get(ctx, client.ObjectKeyFromObject(cm), existing)
	switch {
	case apierrors.IsNotFound(err):
		if err := c.Create(ctx, cm); err != nil {
			return fmt.Errorf("creating history: %w", err)
		}
	case err != nil:
		return fmt.Errorf("reading history: %w", err)
	default:
		existing.Data = cm.Data
		existing.Labels = cm.Labels
		if err := c.Update(ctx, existing); err != nil {
			return fmt.Errorf("updating history: %w", err)
		}
	}
	return nil
}

func deleteHistory(ctx context.Context, c client.Client, namespace, profile string) error {
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: historyName(profile), Namespace: namespace}}
	if err := c.Delete(ctx, cm); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting history: %w", err)
	}
	return nil
}

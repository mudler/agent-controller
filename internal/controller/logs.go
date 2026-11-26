package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// LogReader reads launcher Pod logs.
type LogReader interface {
	// Tail returns the last lines of a container log.
	Tail(ctx context.Context, namespace, pod, container string, lines int64) ([]byte, error)
	// Since returns the log since a time (all of it when since is nil), up
	// to limitBytes.
	Since(ctx context.Context, namespace, pod, container string, since *metav1.Time, limitBytes int64) ([]byte, error)
}

type kubeLogReader struct {
	cs kubernetes.Interface
}

// NewKubeLogReader returns a LogReader that reads logs from the API server.
func NewKubeLogReader(cs kubernetes.Interface) LogReader {
	return &kubeLogReader{cs: cs}
}

func (k *kubeLogReader) Tail(ctx context.Context, namespace, pod, container string, lines int64) ([]byte, error) {
	b, err := k.cs.CoreV1().Pods(namespace).GetLogs(pod, &corev1.PodLogOptions{Container: container, TailLines: &lines}).DoRaw(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading log tail: %w", err)
	}
	return b, nil
}

func (k *kubeLogReader) Since(ctx context.Context, namespace, pod, container string, since *metav1.Time, limitBytes int64) ([]byte, error) {
	opts := &corev1.PodLogOptions{Container: container, SinceTime: since, LimitBytes: &limitBytes}
	b, err := k.cs.CoreV1().Pods(namespace).GetLogs(pod, opts).DoRaw(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading log: %w", err)
	}
	return b, nil
}

package server

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/mudler/agent-controller/internal/controller"
)

const (
	defaultLogTail = 200
	maxLogTail     = 5000
)

// LogStreamer writes the launcher log of a Pod to w.
type LogStreamer interface {
	Stream(ctx context.Context, w io.Writer, namespace, pod string, follow bool, tail int64) error
}

type kubeLogStreamer struct {
	cs kubernetes.Interface
}

// NewKubeLogStreamer returns a LogStreamer that reads the launcher container
// log from the API server.
func NewKubeLogStreamer(cs kubernetes.Interface) LogStreamer {
	return &kubeLogStreamer{cs: cs}
}

func (k *kubeLogStreamer) Stream(ctx context.Context, w io.Writer, namespace, pod string, follow bool, tail int64) error {
	opts := &corev1.PodLogOptions{Container: controller.LauncherContainer, Follow: follow, TailLines: &tail}
	rc, err := k.cs.CoreV1().Pods(namespace).GetLogs(pod, opts).Stream(ctx)
	if err != nil {
		return fmt.Errorf("opening log stream: %w", err)
	}
	defer rc.Close()
	return copyLines(w, rc)
}

// copyLines copies r to w one line at a time.
func copyLines(w io.Writer, r io.Reader) error {
	br := bufio.NewReader(r)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			if _, werr := w.Write(line); werr != nil {
				return fmt.Errorf("writing log: %w", werr)
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("reading log: %w", err)
		}
	}
}

// flushWriter flushes the response after every write, so a follower sees
// each line as it arrives.
type flushWriter struct {
	w http.ResponseWriter
	f http.Flusher
}

func newFlushWriter(w http.ResponseWriter) io.Writer {
	f, _ := w.(http.Flusher)
	return &flushWriter{w: w, f: f}
}

func (fw *flushWriter) Write(p []byte) (int, error) {
	n, err := fw.w.Write(p)
	if fw.f != nil {
		fw.f.Flush()
	}
	return n, err
}

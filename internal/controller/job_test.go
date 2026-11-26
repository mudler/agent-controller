package controller

import (
	"os"
	"path/filepath"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/utils/ptr"
)

func loadTestJobTemplate(t *testing.T) *batchv1.Job {
	t.Helper()
	tmpl, err := LoadJobTemplate(filepath.Join("testdata", "job-template.yaml"))
	if err != nil {
		t.Fatalf("LoadJobTemplate() error = %v", err)
	}
	return tmpl
}

func envMap(c *corev1.Container) map[string]corev1.EnvVar {
	out := map[string]corev1.EnvVar{}
	for _, e := range c.Env {
		out[e.Name] = e
	}
	return out
}

func TestBuildJob(t *testing.T) {
	tmpl := loadTestJobTemplate(t)
	orig := tmpl.DeepCopy()
	run := exampleRun()
	spec := resolveExample(t, nil)

	job := BuildJob(tmpl, exampleConfig(), run, spec)

	if !equality.Semantic.DeepEqual(tmpl, orig) {
		t.Fatal("BuildJob mutated the template")
	}
	if job.Name != run.Name || job.Namespace != run.Namespace {
		t.Errorf("name = %s/%s", job.Namespace, job.Name)
	}
	for _, labels := range []map[string]string{job.Labels, job.Spec.Template.Labels} {
		if labels[LabelRun] != run.Name || labels[LabelComponent] != ComponentLauncher {
			t.Errorf("labels = %v", labels)
		}
		if labels["app.kubernetes.io/name"] != "agent-launcher" {
			t.Errorf("template label lost: %v", labels)
		}
	}
	if got := ptr.Deref(job.Spec.ActiveDeadlineSeconds, 0); got != 3600 {
		t.Errorf("activeDeadlineSeconds = %d, want 3600", got)
	}
	if got := ptr.Deref(job.Spec.BackoffLimit, -1); got != 0 {
		t.Errorf("backoffLimit = %d, want 0", got)
	}
	if got := ptr.Deref(job.Spec.TTLSecondsAfterFinished, 0); got != 86400 {
		t.Errorf("ttlSecondsAfterFinished = %d, want the template value", got)
	}
	if len(job.OwnerReferences) != 1 || job.OwnerReferences[0].Name != run.Name || !ptr.Deref(job.OwnerReferences[0].Controller, false) {
		t.Errorf("owner references = %+v", job.OwnerReferences)
	}

	pod := job.Spec.Template.Spec
	if pod.ServiceAccountName != "agent-launcher" || pod.NodeSelector["kubernetes.io/os"] != "linux" {
		t.Errorf("pod spec fields lost: %+v", pod)
	}
	if pod.SecurityContext == nil || !ptr.Deref(pod.SecurityContext.RunAsNonRoot, false) {
		t.Errorf("pod security context lost")
	}
	if len(pod.Volumes) != 1 || pod.Volumes[0].Name != "settings" {
		t.Errorf("volumes = %+v", pod.Volumes)
	}
	c := pod.Containers[0]
	if c.SecurityContext == nil || !ptr.Deref(c.SecurityContext.ReadOnlyRootFilesystem, false) {
		t.Errorf("container security context lost")
	}
	env := envMap(&c)
	if env["RUN_SPEC"].Value != "example-x7k2p-spec" {
		t.Errorf("RUN_SPEC = %q", env["RUN_SPEC"].Value)
	}
	if env["AGENT"].Value != "example" {
		t.Errorf("AGENT = %q, want the profile (replaced)", env["AGENT"].Value)
	}
	if env["LOG_FORMAT"].Value != "json" {
		t.Errorf("template env lost")
	}
	if fr := env["POD_NAME"].ValueFrom; fr == nil || fr.FieldRef == nil || fr.FieldRef.FieldPath != "metadata.name" {
		t.Errorf("POD_NAME = %+v", env["POD_NAME"])
	}
	agentCount := 0
	for _, e := range c.Env {
		if e.Name == "AGENT" {
			agentCount++
		}
	}
	if agentCount != 1 {
		t.Errorf("AGENT appears %d times", agentCount)
	}
}

func TestLoadJobTemplateErrors(t *testing.T) {
	dir := t.TempDir()
	noLauncher := filepath.Join(dir, "no-launcher.yaml")
	if err := os.WriteFile(noLauncher, []byte(`apiVersion: batch/v1
kind: Job
metadata: {name: x}
spec:
  template:
    spec:
      containers: [{name: other, image: example.com/x}]
`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadJobTemplate(noLauncher); err == nil {
		t.Error("template without launcher: want error")
	}
	if _, err := LoadJobTemplate(filepath.Join(dir, "missing.yaml")); err == nil {
		t.Error("missing file: want error")
	}
}

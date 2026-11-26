package controller

import (
	"errors"
	"fmt"
	"os"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/yaml"

	"github.com/mudler/agent-controller/api/v1alpha1"
)

// JobDeadlinePadding is added to the session budget for the launcher Job
// deadline, to cover sandbox start and cleanup.
const JobDeadlinePadding = 600

// LoadJobTemplate reads the launcher Job template. It must have a container
// named "launcher".
func LoadJobTemplate(path string) (*batchv1.Job, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading job template: %w", err)
	}
	job := &batchv1.Job{}
	if err := yaml.UnmarshalStrict(data, job); err != nil {
		return nil, fmt.Errorf("parsing job template: %w", err)
	}
	if launcherIndex(&job.Spec.Template.Spec) < 0 {
		return nil, errors.New("job template has no container named " + LauncherContainer)
	}
	return job, nil
}

func launcherIndex(spec *corev1.PodSpec) int {
	for i := range spec.Containers {
		if spec.Containers[i].Name == LauncherContainer {
			return i
		}
	}
	return -1
}

// BuildJob returns the launcher Job of a run. It keeps everything from the
// template except the name, namespace, labels, owner reference, deadline,
// backoff limit and the run environment variables.
func BuildJob(tmpl *batchv1.Job, _ Config, run *v1alpha1.AgentRun, spec *RunSpec) *batchv1.Job {
	job := tmpl.DeepCopy()
	job.ObjectMeta = metav1.ObjectMeta{
		Name:            run.Name,
		Namespace:       run.Namespace,
		Labels:          withRunLabels(tmpl.Labels, run.Name),
		Annotations:     tmpl.Annotations,
		OwnerReferences: []metav1.OwnerReference{runOwnerRef(run)},
	}
	job.Status = batchv1.JobStatus{}
	job.Spec.Selector = nil
	job.Spec.ManualSelector = nil
	job.Spec.ActiveDeadlineSeconds = ptr.To(int64(spec.Budget.SessionSeconds) + JobDeadlinePadding)
	job.Spec.BackoffLimit = ptr.To(int32(0))
	job.Spec.Template.Labels = withRunLabels(tmpl.Spec.Template.Labels, run.Name)

	pod := &job.Spec.Template.Spec
	pod.RestartPolicy = corev1.RestartPolicyNever
	if i := launcherIndex(pod); i >= 0 {
		c := &pod.Containers[i]
		c.Env = setEnv(c.Env, corev1.EnvVar{Name: "RUN_SPEC", Value: ConfigMapName(run.Name)})
		c.Env = setEnv(c.Env, corev1.EnvVar{Name: "AGENT", Value: run.Spec.Profile})
		c.Env = setEnv(c.Env, corev1.EnvVar{
			Name: "POD_NAME",
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{APIVersion: "v1", FieldPath: "metadata.name"},
			},
		})
		c.TerminationMessagePolicy = corev1.TerminationMessageReadFile
	}
	return job
}

func withRunLabels(base map[string]string, run string) map[string]string {
	out := make(map[string]string, len(base)+2)
	for k, v := range base {
		out[k] = v
	}
	out[LabelRun] = run
	out[LabelComponent] = ComponentLauncher
	return out
}

// setEnv adds or replaces the variable with the same name.
func setEnv(env []corev1.EnvVar, v corev1.EnvVar) []corev1.EnvVar {
	for i := range env {
		if env[i].Name == v.Name {
			env[i] = v
			return env
		}
	}
	return append(env, v)
}

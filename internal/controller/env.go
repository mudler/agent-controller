package controller

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mudler/agent-controller/api/v1alpha1"
	"github.com/mudler/agent-controller/internal/policy"
)

// LabelProfile marks a run with its profile name, for listing.
const LabelProfile = "agents.mudler.dev/profile"

// LoadEnv reads the policy, logins and harness profiles of a namespace. A
// missing policy leaves Env.Policy nil, which makes every check fail.
func LoadEnv(ctx context.Context, r client.Reader, namespace string, versions map[v1alpha1.HarnessKind][]string) (policy.Env, error) {
	env := policy.Env{
		Logins:          map[string]*v1alpha1.Login{},
		Harnesses:       map[string]*v1alpha1.HarnessProfile{},
		HarnessVersions: versions,
	}

	pol := &v1alpha1.AgentPolicy{}
	err := r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: v1alpha1.PolicyName}, pol)
	switch {
	case apierrors.IsNotFound(err):
	case err != nil:
		return env, fmt.Errorf("getting policy: %w", err)
	default:
		env.Policy = pol
	}

	logins := &v1alpha1.LoginList{}
	if err := r.List(ctx, logins, client.InNamespace(namespace)); err != nil {
		return env, fmt.Errorf("listing logins: %w", err)
	}
	for i := range logins.Items {
		env.Logins[logins.Items[i].Name] = &logins.Items[i]
	}

	harnesses := &v1alpha1.HarnessProfileList{}
	if err := r.List(ctx, harnesses, client.InNamespace(namespace)); err != nil {
		return env, fmt.Errorf("listing harness profiles: %w", err)
	}
	for i := range harnesses.Items {
		env.Harnesses[harnesses.Items[i].Name] = &harnesses.Items[i]
	}
	return env, nil
}

package controller

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/mudler/agent-controller/api/v1alpha1"
	"github.com/mudler/agent-controller/internal/testenv"
)

func TestMain(m *testing.M) {
	code := m.Run()
	testenv.Stop()
	os.Exit(code)
}

func newNamespace(t *testing.T, c client.Client) string {
	t.Helper()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "ctrl-"}}
	if err := c.Create(context.Background(), ns); err != nil {
		t.Fatalf("creating namespace: %v", err)
	}
	return ns.Name
}

// eventually polls fn until it returns nil or the timeout passes.
func eventually(t *testing.T, timeout time.Duration, fn func() error) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		err := fn()
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("condition not met after %s: %v", timeout, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func mustCreate(t *testing.T, c client.Client, objs ...client.Object) {
	t.Helper()
	for _, o := range objs {
		if err := c.Create(context.Background(), o); err != nil {
			t.Fatalf("creating %T %s: %v", o, o.GetName(), err)
		}
	}
}

type fixtures struct {
	Policy  *v1alpha1.AgentPolicy
	GitHub  *v1alpha1.Login
	Model   *v1alpha1.Login
	Harness *v1alpha1.HarnessProfile
}

// newFixtures returns a policy, two logins and a harness profile that fit
// together, in namespace ns.
func newFixtures(ns string) fixtures {
	gh, model := exampleLogins()
	gh.Namespace, model.Namespace = ns, ns
	gh.Spec.EgressAll = true
	h := exampleHarness()
	h.Namespace = ns
	return fixtures{
		Policy: &v1alpha1.AgentPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: v1alpha1.PolicyName, Namespace: ns},
			Spec: v1alpha1.AgentPolicySpec{
				Harnesses: []v1alpha1.HarnessKind{v1alpha1.HarnessNib},
				Egress: v1alpha1.PolicyEgress{
					Hosts:    []string{"api.github.com:443", "github.com:443", "models.example.com:443"},
					AllowAll: true,
				},
				Limits: v1alpha1.PolicyLimits{
					SessionSeconds:        21600,
					CPU:                   resource.MustParse("8"),
					Memory:                resource.MustParse("32Gi"),
					Workspace:             resource.MustParse("50Gi"),
					MaxParallelPerProfile: 4,
					GlobalSlots:           6,
				},
				ModelEndpoints: []string{"https://models.example.com/v1"},
			},
		},
		GitHub:  gh,
		Model:   model,
		Harness: h,
	}
}

func (f fixtures) create(t *testing.T, c client.Client) {
	t.Helper()
	mustCreate(t, c, f.Policy, f.GitHub, f.Model, f.Harness)
}

// newTestProfile returns a valid, validated profile in namespace ns.
func newTestProfile(ns, name string) *v1alpha1.AgentProfile {
	p := exampleProfile()
	p.ObjectMeta = metav1.ObjectMeta{Name: name, Namespace: ns}
	return p
}

func testConfig(ns string) Config {
	cfg := exampleConfig()
	cfg.Namespace = ns
	return cfg
}

// startManager runs a manager scoped to ns until the test ends.
func startManager(t *testing.T, ns string, setup func(mgr ctrl.Manager) error) {
	t.Helper()
	startManagerStoppable(t, ns, setup)
}

// startManagerStoppable runs a manager scoped to ns and returns a function
// that stops it. The test cleanup also stops it.
func startManagerStoppable(t *testing.T, ns string, setup func(mgr ctrl.Manager) error) func() {
	t.Helper()
	mgr, err := ctrl.NewManager(testenv.Config(t), ctrl.Options{
		Scheme:                 testenv.Scheme,
		Cache:                  cache.Options{DefaultNamespaces: map[string]cache.Config{ns: {}}},
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
		Controller:             config.Controller{SkipNameValidation: ptrTrue()},
	})
	if err != nil {
		t.Fatalf("creating manager: %v", err)
	}
	if err := setup(mgr); err != nil {
		t.Fatalf("setting up reconcilers: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := mgr.Start(ctx); err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("manager stopped: %v", err)
		}
	}()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			wg.Wait()
		})
	}
	t.Cleanup(stop)
	return stop
}

func ptrTrue() *bool {
	b := true
	return &b
}

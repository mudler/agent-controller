package controller

// Controller Role in the agents namespace. The verbs are exactly the ones
// the code uses; TestControllerRBACVerbs pins them. It has no access to
// Secrets. The controller writes only the status of profiles, harness
// profiles and runs; it updates runs themselves only for the cleanup
// finalizer. Cleanup lists through the uncached reader, so Services,
// NetworkPolicies, Sandboxes and ConfigMaps need list but not watch.
//
// +kubebuilder:rbac:groups=agents.mudler.dev,namespace=agents,resources=agentprofiles;harnessprofiles;logins;agentpolicies,verbs=get;list;watch
// +kubebuilder:rbac:groups=agents.mudler.dev,namespace=agents,resources=agentprofiles/status;harnessprofiles/status;agentruns/status,verbs=update
// +kubebuilder:rbac:groups=agents.mudler.dev,namespace=agents,resources=agentruns,verbs=get;list;watch;create;update;delete
// +kubebuilder:rbac:groups=agents.mudler.dev,namespace=agents,resources=agentruns/finalizers,verbs=update
// +kubebuilder:rbac:groups=batch,namespace=agents,resources=jobs,verbs=create;get;list;watch;delete
// +kubebuilder:rbac:groups="",namespace=agents,resources=configmaps,verbs=create;list;delete
// +kubebuilder:rbac:groups="",namespace=agents,resources=pods,verbs=list;watch;delete
// +kubebuilder:rbac:groups="",namespace=agents,resources=pods/log,verbs=get
// +kubebuilder:rbac:groups="",namespace=agents,resources=services,verbs=list;delete
// +kubebuilder:rbac:groups=networking.k8s.io,namespace=agents,resources=networkpolicies,verbs=list;delete
// +kubebuilder:rbac:groups=agents.x-k8s.io,namespace=agents,resources=sandboxes,verbs=list;delete
// +kubebuilder:rbac:groups=coordination.k8s.io,namespace=agents,resources=leases,verbs=create;get;list;watch;update;delete
// +kubebuilder:rbac:groups="",namespace=agents,resources=persistentvolumeclaims,verbs=create;get
// +kubebuilder:rbac:groups="",namespace=agents,resources=events,verbs=create;patch

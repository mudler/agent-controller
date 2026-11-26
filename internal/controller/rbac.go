package controller

// Controller Role in the agents namespace. It has no access to Secrets.
//
// +kubebuilder:rbac:groups=agents.mudler.dev,namespace=agents,resources=agentprofiles;harnessprofiles;agentruns;logins,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=agents.mudler.dev,namespace=agents,resources=agentprofiles/status;harnessprofiles/status;agentruns/status;logins/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=agents.mudler.dev,namespace=agents,resources=agentprofiles/finalizers;harnessprofiles/finalizers;agentruns/finalizers;logins/finalizers,verbs=update
// +kubebuilder:rbac:groups=agents.mudler.dev,namespace=agents,resources=agentruns,verbs=create;delete
// +kubebuilder:rbac:groups=agents.mudler.dev,namespace=agents,resources=agentpolicies,verbs=get;list;watch
// +kubebuilder:rbac:groups=batch,namespace=agents,resources=jobs,verbs=create;get;list;watch;delete
// +kubebuilder:rbac:groups="",namespace=agents,resources=configmaps,verbs=create;get;list;watch;delete
// +kubebuilder:rbac:groups="",namespace=agents,resources=pods,verbs=get;list;watch;delete
// +kubebuilder:rbac:groups="",namespace=agents,resources=pods/log,verbs=get
// +kubebuilder:rbac:groups="",namespace=agents,resources=services,verbs=list;delete
// +kubebuilder:rbac:groups=networking.k8s.io,namespace=agents,resources=networkpolicies,verbs=list;delete
// +kubebuilder:rbac:groups=agents.x-k8s.io,namespace=agents,resources=sandboxes,verbs=list;delete
// +kubebuilder:rbac:groups=coordination.k8s.io,namespace=agents,resources=leases,verbs=create;get;list;watch;update;delete
// +kubebuilder:rbac:groups="",namespace=agents,resources=persistentvolumeclaims,verbs=create;get
// +kubebuilder:rbac:groups="",namespace=agents,resources=events,verbs=create;patch

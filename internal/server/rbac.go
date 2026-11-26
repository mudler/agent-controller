package server

// Server Role in the agents namespace. The server reads and writes the
// agent CRDs, reads Pods and their logs for the logs routes, and reads only
// the tokens Secret. It has no ConfigMap access here, so it cannot touch the
// resolved run ConfigMaps that the launcher reads.
//
// +kubebuilder:rbac:groups=agents.mudler.dev,namespace=agents,resources=agentprofiles;harnessprofiles;agentruns;logins,verbs=get;list;create;update;patch;delete
// +kubebuilder:rbac:groups=agents.mudler.dev,namespace=agents,resources=logins/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=agents.mudler.dev,namespace=agents,resources=agentpolicies,verbs=get;list
// +kubebuilder:rbac:groups="",namespace=agents,resources=pods,verbs=get;list
// +kubebuilder:rbac:groups="",namespace=agents,resources=pods/log,verbs=get
// +kubebuilder:rbac:groups="",namespace=agents,resources=secrets,resourceNames=agent-controller-tokens,verbs=get

// Server Role in the history namespace: profile history ConfigMaps.
//
// +kubebuilder:rbac:groups="",namespace=agent-controller-history,resources=configmaps,verbs=get;list;create;update;delete

// Server Role in the logins namespace: write-only login Secrets.
//
// +kubebuilder:rbac:groups="",namespace=agent-logins,resources=secrets,verbs=create;update;delete

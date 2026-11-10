package argocd

import "time"

// The types below are the subset of ArgoCD's Application resource that argo-center reads.
// Field names match the JSON served by the ArgoCD REST API.

type Application struct {
	Metadata ObjectMeta `json:"metadata"`
	Spec     AppSpec    `json:"spec"`
	Status   AppStatus  `json:"status"`
}

type ObjectMeta struct {
	Name            string `json:"name"`
	Namespace       string `json:"namespace"`
	ResourceVersion string `json:"resourceVersion"`
}

type AppSpec struct {
	Project string      `json:"project"`
	Source  *AppSource  `json:"source"`
	Sources []AppSource `json:"sources"`
	// SyncPolicy.Automated is non-nil when auto-sync is on.
	SyncPolicy *struct {
		Automated *struct{} `json:"automated"`
	} `json:"syncPolicy"`
}

type AppSource struct {
	RepoURL        string `json:"repoURL"`
	Path           string `json:"path"`
	TargetRevision string `json:"targetRevision"`
}

type AppStatus struct {
	Sync           SyncStatus        `json:"sync"`
	Health         HealthStatus      `json:"health"`
	OperationState *OperationState   `json:"operationState"`
	ReconciledAt   *time.Time        `json:"reconciledAt"`
	History        []RevisionHistory `json:"history"`
	Resources      []ResourceStatus  `json:"resources"`
}

type SyncStatus struct {
	// Status is Synced, OutOfSync or Unknown.
	Status string `json:"status"`
	// Revision is the resolved Git revision ArgoCD compared the live state against.
	Revision string `json:"revision"`
}

type HealthStatus struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

type OperationState struct {
	// Phase is Running, Terminating, Failed, Error or Succeeded.
	Phase     string `json:"phase"`
	Message   string `json:"message"`
	Operation struct {
		Sync *struct {
			DryRun   bool   `json:"dryRun"`
			Revision string `json:"revision"`
		} `json:"sync"`
	} `json:"operation"`
	StartedAt  *time.Time `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt"`
}

type RevisionHistory struct {
	Revision   string     `json:"revision"`
	DeployedAt *time.Time `json:"deployedAt"`
}

type ResourceStatus struct {
	Group     string `json:"group"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Status    string `json:"status"`
}

type ApplicationList struct {
	Metadata struct {
		ResourceVersion string `json:"resourceVersion"`
	} `json:"metadata"`
	Items []Application `json:"items"`
}

// WatchEvent is one message of the /api/v1/stream/applications stream.
type WatchEvent struct {
	// Type is ADDED, MODIFIED or DELETED.
	Type        string      `json:"type"`
	Application Application `json:"application"`
}

// ManagedResource is one item of /api/v1/applications/{name}/managed-resources.
// The *State fields hold JSON-encoded objects, or "null" when the object does not exist
// (TargetState "null": not in Git; LiveState "null": not yet created).
type ManagedResource struct {
	Group               string `json:"group"`
	Kind                string `json:"kind"`
	Namespace           string `json:"namespace"`
	Name                string `json:"name"`
	TargetState         string `json:"targetState"`
	LiveState           string `json:"liveState"`
	NormalizedLiveState string `json:"normalizedLiveState"`
	PredictedLiveState  string `json:"predictedLiveState"`
	Hook                bool   `json:"hook"`
	Modified            bool   `json:"modified"`
}

// SyncRequest is the body of POST /api/v1/applications/{name}/sync.
type SyncRequest struct {
	AppNamespace string `json:"appNamespace,omitempty"`
	// Revision pins the sync to a commit; empty syncs the app's target revision.
	Revision string `json:"revision,omitempty"`
	Prune    bool   `json:"prune"`
	DryRun   bool   `json:"dryRun"`
	// Resources limits the sync to these resources; empty syncs all of them.
	Resources   []SyncResource `json:"resources,omitempty"`
	SyncOptions *SyncOptions   `json:"syncOptions,omitempty"`
	// Infos are shown with the operation in ArgoCD's UI.
	Infos []Info `json:"infos,omitempty"`
}

// PatchRequest is the body of PATCH /api/v1/applications/{name}.
type PatchRequest struct {
	Name         string `json:"name"`
	AppNamespace string `json:"appNamespace,omitempty"`
	// Patch is a JSON document, sent as a string; PatchType is "merge" or "json".
	Patch     string `json:"patch"`
	PatchType string `json:"patchType"`
}

type SyncResource struct {
	Group     string `json:"group"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

type SyncOptions struct {
	Items []string `json:"items"`
}

type Info struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

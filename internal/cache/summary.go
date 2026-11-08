package cache

import (
	"net/url"
	"time"

	"github.com/snapp-incubator/argo-center/internal/argocd"
)

// AppSummary is what the panel shows for one app on one cluster.
// It is comparable, so unchanged updates can be detected with ==.
type AppSummary struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Project   string `json:"project"`

	// MultiSource is set for apps with spec.sources; RepoURL, Path and TargetRevision are then empty.
	MultiSource bool `json:"multiSource,omitempty"`
	// AutoSync is set when ArgoCD syncs the app by itself; such apps cannot be synced to a pinned revision.
	AutoSync bool `json:"autoSync,omitempty"`

	RepoURL        string `json:"repoURL"`
	Path           string `json:"path"`
	TargetRevision string `json:"targetRevision"`

	// SyncStatus is Synced, OutOfSync or Unknown.
	SyncStatus string `json:"syncStatus"`
	// Revision is the Git revision ArgoCD compares against (what a sync would apply).
	Revision string `json:"revision"`
	// DeployedRevision is the revision of the last successful sync.
	DeployedRevision string `json:"deployedRevision"`
	// OutOfSyncResources counts managed resources whose live state differs from Git.
	OutOfSyncResources int `json:"outOfSyncResources"`

	HealthStatus  string `json:"healthStatus"`
	HealthMessage string `json:"healthMessage,omitempty"`

	OperationPhase      string    `json:"operationPhase,omitempty"`
	OperationMessage    string    `json:"operationMessage,omitempty"`
	OperationDryRun     bool      `json:"operationDryRun,omitempty"`
	OperationRevision   string    `json:"operationRevision,omitempty"`
	OperationStartedAt  time.Time `json:"operationStartedAt,omitzero"`
	OperationFinishedAt time.Time `json:"operationFinishedAt,omitzero"`
	LastDeployedAt      time.Time `json:"lastDeployedAt,omitzero"`
	ReconciledAt        time.Time `json:"reconciledAt,omitzero"`

	// ArgoURL deep-links to the app in that instance's own UI.
	ArgoURL string `json:"argoURL"`
}

// Summarize reduces an application to what the panel shows. argoBaseURL is the instance URL.
func Summarize(app argocd.Application, argoBaseURL string) AppSummary {
	s := AppSummary{
		Name:          app.Metadata.Name,
		Namespace:     app.Metadata.Namespace,
		Project:       app.Spec.Project,
		SyncStatus:    app.Status.Sync.Status,
		Revision:      app.Status.Sync.Revision,
		HealthStatus:  app.Status.Health.Status,
		HealthMessage: app.Status.Health.Message,
		ReconciledAt:  deref(app.Status.ReconciledAt),
		ArgoURL:       argoBaseURL + "/applications/" + url.PathEscape(app.Metadata.Name),
	}
	if app.Metadata.Namespace != "" {
		s.ArgoURL = argoBaseURL + "/applications/" + url.PathEscape(app.Metadata.Namespace) + "/" + url.PathEscape(app.Metadata.Name)
	}
	if src := app.Spec.Source; src != nil {
		s.RepoURL, s.Path, s.TargetRevision = src.RepoURL, src.Path, src.TargetRevision
	}
	s.MultiSource = app.Spec.Source == nil && len(app.Spec.Sources) > 0
	s.AutoSync = app.Spec.SyncPolicy != nil && app.Spec.SyncPolicy.Automated != nil
	if n := len(app.Status.History); n > 0 {
		last := app.Status.History[n-1]
		s.DeployedRevision = last.Revision
		s.LastDeployedAt = deref(last.DeployedAt)
	}
	for _, r := range app.Status.Resources {
		if r.Status == "OutOfSync" {
			s.OutOfSyncResources++
		}
	}
	if op := app.Status.OperationState; op != nil {
		s.OperationPhase = op.Phase
		s.OperationMessage = op.Message
		s.OperationStartedAt = deref(op.StartedAt)
		s.OperationFinishedAt = deref(op.FinishedAt)
		if sync := op.Operation.Sync; sync != nil {
			s.OperationDryRun, s.OperationRevision = sync.DryRun, sync.Revision
		}
	}
	return s
}

// sameIgnoringReconcile reports whether two summaries differ only in ReconciledAt,
// which ArgoCD bumps on every periodic reconcile and is not worth an event.
func sameIgnoringReconcile(a, b AppSummary) bool {
	a.ReconciledAt = b.ReconciledAt
	return a == b
}

func deref(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return t.UTC()
}

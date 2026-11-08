package rollout

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/snapp-incubator/argo-center/internal/argocd"
	"github.com/snapp-incubator/argo-center/internal/audit"
	"github.com/snapp-incubator/argo-center/internal/cache"
)

// Engine runs rollouts. Each running rollout has one goroutine working through its steps; every
// state change is saved and announced as a rollout-updated event.
type Engine struct {
	store *repo
	cache *cache.Cache
	audit *audit.Store

	// HealthTimeout bounds the wait for Healthy after each cluster's sync.
	HealthTimeout time.Duration
	// PollInterval is how often a step checks the app while waiting.
	PollInterval time.Duration
	// DegradedGrace is how long an app may stay Degraded after its sync before the step fails;
	// short Degraded blips happen while pods restart.
	DegradedGrace time.Duration
	// RefreshTimeout bounds the refresh at the start of each step.
	RefreshTimeout time.Duration

	mu      sync.Mutex // serializes load-modify-save and guards running
	running map[int64]context.CancelFunc
	root    context.Context
}

func New(db *sql.DB, c *cache.Cache, a *audit.Store, healthTimeout time.Duration) (*Engine, error) {
	s, err := newRepo(db)
	if err != nil {
		return nil, err
	}
	return &Engine{
		store: s, cache: c, audit: a,
		HealthTimeout:  healthTimeout,
		PollInterval:   5 * time.Second,
		DegradedGrace:  2 * time.Minute,
		RefreshTimeout: 3 * time.Minute,
		running:        map[int64]context.CancelFunc{},
		root:           context.Background(),
	}, nil
}

// Start marks rollouts left running by a previous process as interrupted, and makes ctx the parent
// of every rollout started from now on.
func (e *Engine) Start(ctx context.Context) error {
	e.root = ctx
	running, err := e.store.list(ctx, "", true, 1000)
	if err != nil {
		return err
	}
	for _, r := range running {
		if r.State != StateRunning {
			continue
		}
		e.mutate(ctx, r.ID, func(r *Rollout) error {
			r.State, r.Message = StateInterrupted, "argo-center restarted during the rollout; resume to continue"
			for i := range r.Steps {
				if s := &r.Steps[i]; isInProgress(s.State) {
					s.State, s.Message = StepInterrupted, "interrupted by a restart of argo-center"
				}
			}
			return nil
		})
	}
	return nil
}

func isInProgress(state string) bool {
	return state == StepRefreshing || state == StepSyncing || state == StepWaiting
}

func isDone(state string) bool {
	return state == StepSucceeded || state == StepSkipped
}

type CreateRequest struct {
	App     string       `json:"app"`
	Steps   []CreateStep `json:"steps"`
	Options Options      `json:"options"`
}

type CreateStep struct {
	Cluster string `json:"cluster"`
	// Revision is the commit the user reviewed for this cluster; empty uses ArgoCD's current one.
	Revision string `json:"revision"`
}

// Create plans and starts a rollout. Steps run in the given order.
func (e *Engine) Create(ctx context.Context, user string, req CreateRequest) (*Rollout, error) {
	if len(req.Steps) == 0 {
		return nil, fmt.Errorf("%w: choose at least one cluster", ErrInvalid)
	}
	r := &Rollout{
		App: req.App, Options: req.Options, CreatedBy: user, ActingUser: user,
		CreatedAt: time.Now().UTC(), State: StateRunning,
	}
	seen := map[string]bool{}
	for _, cs := range req.Steps {
		if seen[cs.Cluster] {
			return nil, fmt.Errorf("%w: cluster %s listed twice", ErrInvalid, cs.Cluster)
		}
		seen[cs.Cluster] = true
		_, summary, known, found := e.cache.Lookup(cs.Cluster, req.App)
		if !known || !found {
			return nil, fmt.Errorf("%w: %s is not deployed on %s", ErrInvalid, req.App, cs.Cluster)
		}
		rev := cs.Revision
		if summary.AutoSync || summary.MultiSource {
			rev = ""
		} else if rev == "" {
			rev = summary.Revision
		}
		r.Steps = append(r.Steps, Step{Cluster: cs.Cluster, Revision: rev, State: StepPending})
	}

	e.mu.Lock()
	active, err := e.store.list(ctx, req.App, true, 1)
	if err == nil && len(active) > 0 {
		err = fmt.Errorf("%w: rollout #%d for %s is %s; abort it first", ErrConflict, active[0].ID, req.App, active[0].State)
	}
	if err == nil {
		err = e.store.insert(ctx, r)
	}
	e.mu.Unlock()
	if err != nil {
		return nil, err
	}

	clusters := make([]string, len(r.Steps))
	for i, s := range r.Steps {
		clusters[i] = s.Cluster
	}
	e.record(r, user, audit.ActionRolloutStart, "", "", r.optionStrings("order "+strings.Join(clusters, " → ")), nil)
	e.publish(r)
	e.launch(r.ID)
	return r, nil
}

var ErrInvalid = errors.New("invalid rollout")

// Abort stops a rollout. With terminate, a sync it started that is still running is terminated too;
// otherwise that sync finishes on its own.
func (e *Engine) Abort(ctx context.Context, user string, id int64, terminate bool) (*Rollout, error) {
	var syncing *Step
	r, err := e.mutate(ctx, id, func(r *Rollout) error {
		if !r.Active() {
			return fmt.Errorf("%w: rollout is already %s", ErrConflict, r.State)
		}
		for i := range r.Steps {
			s := &r.Steps[i]
			if s.State == StepSyncing {
				step := *s
				syncing = &step
			}
			if !isDone(s.State) && s.State != StepFailed {
				s.State = StepAborted
				if s.FinishedAt.IsZero() && !s.StartedAt.IsZero() {
					s.FinishedAt = time.Now().UTC()
				}
			}
		}
		r.finish(StateAborted, "aborted by "+user)
		return nil
	})
	if err != nil {
		return nil, err
	}
	e.stop(id)

	var opts []string
	var termErr error
	if terminate && syncing != nil {
		opts = append(opts, "terminated the sync on "+syncing.Cluster)
		if src, summary, _, found := e.cache.Lookup(syncing.Cluster, r.App); found {
			termErr = src.Client.TerminateOperation(context.WithoutCancel(ctx), r.App, summary.Namespace)
		}
	}
	e.record(r, user, audit.ActionRolloutAbort, "", "", opts, termErr)
	if termErr != nil {
		return r, fmt.Errorf("rollout aborted, but terminating the sync on %s failed: %w", syncing.Cluster, termErr)
	}
	return r, nil
}

// Resume retries a failed or interrupted rollout from its first unfinished step.
func (e *Engine) Resume(ctx context.Context, user string, id int64) (*Rollout, error) {
	r, err := e.mutate(ctx, id, func(r *Rollout) error {
		if r.State != StateFailed && r.State != StateInterrupted {
			return fmt.Errorf("%w: only failed or interrupted rollouts can be resumed (this one is %s)", ErrConflict, r.State)
		}
		for i := range r.Steps {
			if s := &r.Steps[i]; !isDone(s.State) {
				s.State, s.Message, s.StartedAt, s.FinishedAt = StepPending, "", time.Time{}, time.Time{}
			}
		}
		r.State, r.Message, r.ActingUser, r.FinishedAt = StateRunning, "", user, time.Time{}
		return nil
	})
	if err != nil {
		return nil, err
	}
	e.record(r, user, audit.ActionRolloutResume, "", "", nil, nil)
	e.launch(id)
	return r, nil
}

// Confirm continues a paused rollout with the new revision for the paused cluster.
func (e *Engine) Confirm(ctx context.Context, user string, id int64) (*Rollout, error) {
	var cluster, rev string
	r, err := e.mutate(ctx, id, func(r *Rollout) error {
		if r.State != StatePaused {
			return fmt.Errorf("%w: rollout is %s, not paused", ErrConflict, r.State)
		}
		for i := range r.Steps {
			if s := &r.Steps[i]; s.State == StepPaused {
				cluster, rev = s.Cluster, s.NewRevision
				s.Revision, s.NewRevision = s.NewRevision, ""
				s.State, s.Message, s.StartedAt = StepPending, "", time.Time{}
			}
		}
		r.State, r.Message, r.ActingUser = StateRunning, "", user
		return nil
	})
	if err != nil {
		return nil, err
	}
	e.record(r, user, audit.ActionRolloutConfirm, cluster, rev, nil, nil)
	e.launch(id)
	return r, nil
}

func (e *Engine) Get(ctx context.Context, id int64) (*Rollout, error) {
	return e.store.get(ctx, id)
}

// List returns an app's rollouts, newest first; with activeOnly, only those still needing attention.
func (e *Engine) List(ctx context.Context, app string, activeOnly bool, limit int) ([]*Rollout, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	return e.store.list(ctx, app, activeOnly, limit)
}

// Busy reports the running rollout of app that includes cluster, if any. Single-cluster syncs are
// refused meanwhile, so the two do not race.
func (e *Engine) Busy(ctx context.Context, app, cluster string) (*Rollout, bool) {
	active, err := e.store.list(ctx, app, true, 1)
	if err != nil || len(active) == 0 || active[0].State != StateRunning {
		return nil, false
	}
	for _, s := range active[0].Steps {
		if s.Cluster == cluster {
			return active[0], true
		}
	}
	return nil, false
}

// mutate loads a rollout, applies fn, saves and announces it. fn returning an error leaves it unchanged.
func (e *Engine) mutate(ctx context.Context, id int64, fn func(*Rollout) error) (*Rollout, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	r, err := e.store.get(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := fn(r); err != nil {
		return nil, err
	}
	if err := e.store.save(context.WithoutCancel(ctx), r); err != nil {
		return nil, err
	}
	e.publish(r)
	return r, nil
}

func (e *Engine) publish(r *Rollout) {
	e.cache.Publish(cache.Event{Type: "rollout-updated", App: r.App})
}

func (e *Engine) record(r *Rollout, user, action, cluster, revision string, opts []string, err error) {
	entry := audit.Entry{User: user, Action: action, App: r.App, Cluster: cluster, Revision: revision, Result: audit.ResultOK,
		Options: append([]string{fmt.Sprintf("rollout #%d", r.ID)}, opts...)}
	if err != nil {
		entry.Result, entry.Message = audit.ResultError, err.Error()
	}
	e.audit.Record(context.Background(), entry)
}

func (r *Rollout) optionStrings(extra ...string) []string {
	opts := extra
	if r.Options.ApplyOutOfSyncOnly {
		opts = append(opts, "apply out-of-sync only")
	}
	if r.Options.Prune {
		opts = append(opts, "prune")
	}
	return opts
}

func (e *Engine) launch(id int64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.running[id]; ok {
		return
	}
	ctx, cancel := context.WithCancel(e.root)
	e.running[id] = cancel
	go func() {
		defer func() {
			e.mu.Lock()
			delete(e.running, id)
			e.mu.Unlock()
			cancel()
		}()
		e.run(ctx, id)
	}()
}

func (e *Engine) stop(id int64) {
	e.mu.Lock()
	cancel := e.running[id]
	e.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// errStopped means the rollout left the running state (aborted) while a step was working on it.
var errStopped = errors.New("rollout no longer running")

// update changes step i of a running rollout. It returns errStopped once the rollout was aborted,
// so the worker stops without overwriting the user's decision.
func (e *Engine) update(ctx context.Context, id int64, i int, fn func(r *Rollout, s *Step)) (*Rollout, error) {
	r, err := e.mutate(context.WithoutCancel(ctx), id, func(r *Rollout) error {
		if r.State != StateRunning {
			return errStopped
		}
		before := r.Steps[i]
		fn(r, &r.Steps[i])
		if r.Steps[i] == before {
			return errUnchanged
		}
		return nil
	})
	if errors.Is(err, errUnchanged) {
		err = nil
	}
	return r, err
}

// errUnchanged skips saving and announcing an update that changed nothing (the same wait message).
var errUnchanged = errors.New("unchanged")

func (e *Engine) run(ctx context.Context, id int64) {
	r, err := e.store.get(ctx, id)
	if err != nil || r.State != StateRunning {
		return
	}
	log := slog.With("rollout", id, "app", r.App)
	log.Info("rollout running")
	for i := range r.Steps {
		if isDone(r.Steps[i].State) {
			continue
		}
		msg, outcome := e.runStep(ctx, r, i)
		if ctx.Err() != nil || errors.Is(outcome, errStopped) {
			log.Info("rollout stopped")
			return
		}
		switch {
		case outcome == nil:
			continue
		case errors.Is(outcome, errPaused):
			e.mutate(context.WithoutCancel(ctx), id, func(r *Rollout) error {
				if r.State != StateRunning {
					return errStopped
				}
				r.State, r.Message = StatePaused, msg
				return nil
			})
		default:
			e.mutate(context.WithoutCancel(ctx), id, func(r *Rollout) error {
				if r.State != StateRunning {
					return errStopped
				}
				r.finish(StateFailed, fmt.Sprintf("%s: %s", r.Steps[i].Cluster, msg))
				return nil
			})
		}
		log.Info("rollout halted", "step", r.Steps[i].Cluster, "reason", msg)
		return
	}
	e.mutate(context.WithoutCancel(ctx), id, func(r *Rollout) error {
		if r.State != StateRunning {
			return errStopped
		}
		r.finish(StateSucceeded, "")
		return nil
	})
	log.Info("rollout succeeded")
}

var errPaused = errors.New("paused")

// runStep syncs one cluster and waits for it. The error is nil on success, errPaused when Git moved,
// errStopped when the rollout was aborted, or a failure; the message explains it to the user.
func (e *Engine) runStep(ctx context.Context, r *Rollout, i int) (string, error) {
	step := r.Steps[i]
	fail := func(msg string) (string, error) {
		_, err := e.update(ctx, r.ID, i, func(_ *Rollout, s *Step) {
			s.State, s.Message, s.FinishedAt = StepFailed, msg, time.Now().UTC()
		})
		if errors.Is(err, errStopped) {
			return "", errStopped
		}
		return msg, errors.New(msg)
	}

	src, summary, known, found := e.cache.Lookup(step.Cluster, r.App)
	if !known || !found {
		return fail(fmt.Sprintf("%s is no longer deployed on this cluster", r.App))
	}
	if _, err := e.update(ctx, r.ID, i, func(_ *Rollout, s *Step) {
		s.State, s.Message, s.StartedAt = StepRefreshing, "Comparing with Git", time.Now().UTC()
	}); err != nil {
		return "", errStopped
	}

	rctx, cancel := context.WithTimeout(ctx, e.RefreshTimeout)
	app, err := src.Client.RefreshApplication(rctx, r.App, summary.Namespace, false)
	cancel()
	if ctx.Err() != nil {
		return "", errStopped
	}
	if err != nil {
		return fail("refresh failed: " + err.Error())
	}
	e.cache.Put(step.Cluster, *app)
	now := cache.Summarize(*app, src.URL)
	pinned := step.Revision != ""

	if pinned && now.Revision != step.Revision {
		msg := fmt.Sprintf("Git moved from %s to %s since the rollout was planned", short(step.Revision), short(now.Revision))
		if _, err := e.update(ctx, r.ID, i, func(_ *Rollout, s *Step) {
			s.State, s.NewRevision, s.Message = StepPaused, now.Revision, msg
		}); err != nil {
			return "", errStopped
		}
		return fmt.Sprintf("%s: %s. Review the new diff, then confirm or abort.", step.Cluster, msg), errPaused
	}
	if busy(now) {
		return fail("another operation is already running on this cluster")
	}

	if now.SyncStatus == "Synced" {
		// Nothing to apply; still make sure the app is healthy before moving on.
		if now.HealthStatus == "Healthy" {
			_, err := e.update(ctx, r.ID, i, func(_ *Rollout, s *Step) {
				s.State, s.Message, s.FinishedAt = StepSkipped, "already synced and healthy", time.Now().UTC()
			})
			if err != nil {
				return "", errStopped
			}
			return "", nil
		}
		return e.waitHealthy(ctx, r, i, src, now.Namespace, time.Time{}, fail)
	}

	req := argocd.SyncRequest{
		AppNamespace: now.Namespace,
		Revision:     step.Revision,
		Prune:        r.Options.Prune,
		Infos:        []argocd.Info{{Name: "Requested by", Value: fmt.Sprintf("%s via argo-center rollout #%d", r.ActingUser, r.ID)}},
	}
	if r.Options.ApplyOutOfSyncOnly {
		req.SyncOptions = &argocd.SyncOptions{Items: []string{"ApplyOutOfSyncOnly=true"}}
	}
	if _, err := e.update(ctx, r.ID, i, func(_ *Rollout, s *Step) {
		s.State, s.Message = StepSyncing, "Sync requested"
	}); err != nil {
		return "", errStopped
	}
	prevOp := now.OperationStartedAt
	err = src.Client.Sync(ctx, r.App, req)
	e.record(r, r.ActingUser, audit.ActionSync, step.Cluster, step.Revision, r.optionStrings(), err)
	if err != nil {
		return fail("sync refused: " + err.Error())
	}
	return e.waitSynced(ctx, r, i, src, now.Namespace, prevOp, fail)
}

// waitSynced waits for the sync operation started after prevOp to finish, then for Healthy.
func (e *Engine) waitSynced(ctx context.Context, r *Rollout, i int, src cache.Source, ns string, prevOp time.Time,
	fail func(string) (string, error)) (string, error) {
	deadline := time.Now().Add(e.HealthTimeout)
	for {
		if err := sleep(ctx, e.PollInterval); err != nil {
			return "", errStopped
		}
		app, err := src.Client.GetApplication(ctx, r.App, ns)
		if ctx.Err() != nil {
			return "", errStopped
		}
		if err != nil {
			if time.Now().After(deadline) {
				return fail("could not read the app from ArgoCD: " + err.Error())
			}
			continue // transient; try again
		}
		s := cache.Summarize(*app, src.URL)
		ours := !s.OperationStartedAt.Equal(prevOp)
		switch {
		case !ours || busy(s):
			if _, err := e.update(ctx, r.ID, i, func(_ *Rollout, st *Step) {
				st.Message = cmp.Or(s.OperationMessage, "Syncing")
			}); err != nil {
				return "", errStopped
			}
			if time.Now().After(deadline) {
				return fail(fmt.Sprintf("sync did not finish within %s", e.HealthTimeout))
			}
		case s.OperationPhase == "Succeeded":
			return e.waitHealthy(ctx, r, i, src, ns, s.OperationFinishedAt, fail)
		default:
			return fail(fmt.Sprintf("sync %s: %s", strings.ToLower(s.OperationPhase), s.OperationMessage))
		}
	}
}

// waitHealthy waits until a comparison made after since reports the app Healthy.
func (e *Engine) waitHealthy(ctx context.Context, r *Rollout, i int, src cache.Source, ns string, since time.Time,
	fail func(string) (string, error)) (string, error) {
	deadline := time.Now().Add(e.HealthTimeout)
	var degradedSince time.Time
	for {
		app, err := src.Client.GetApplication(ctx, r.App, ns)
		if ctx.Err() != nil {
			return "", errStopped
		}
		if err == nil {
			s := cache.Summarize(*app, src.URL)
			fresh := s.ReconciledAt.After(since) || s.ReconciledAt.Equal(since)
			health := s.HealthStatus
			if fresh && health == "Healthy" {
				_, err := e.update(ctx, r.ID, i, func(_ *Rollout, st *Step) {
					st.State, st.Message, st.FinishedAt = StepSucceeded, "synced and healthy", time.Now().UTC()
				})
				if err != nil {
					return "", errStopped
				}
				return "", nil
			}
			if fresh && health == "Degraded" {
				if degradedSince.IsZero() {
					degradedSince = time.Now()
				} else if time.Since(degradedSince) > e.DegradedGrace {
					return fail("app is Degraded: " + cmp.Or(s.HealthMessage, "see ArgoCD for details"))
				}
			} else {
				degradedSince = time.Time{}
			}
			if _, err := e.update(ctx, r.ID, i, func(_ *Rollout, st *Step) {
				st.State, st.Message = StepWaiting, "Waiting for Healthy (now "+cmp.Or(health, "Unknown")+")"
			}); err != nil {
				return "", errStopped
			}
			if time.Now().After(deadline) {
				return fail(fmt.Sprintf("not Healthy after %s (health: %s)", e.HealthTimeout, cmp.Or(health, "Unknown")))
			}
		} else if time.Now().After(deadline) {
			return fail("could not read the app from ArgoCD: " + err.Error())
		}
		if err := sleep(ctx, e.PollInterval); err != nil {
			return "", errStopped
		}
	}
}

func busy(s cache.AppSummary) bool {
	return s.OperationPhase == "Running" || s.OperationPhase == "Terminating"
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func short(rev string) string {
	if len(rev) > 7 {
		return rev[:7]
	}
	return rev
}

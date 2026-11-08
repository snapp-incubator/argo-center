// Package rollout syncs one app on several clusters in order, waiting for each to become healthy
// before moving on. Rollouts run on the server, survive closed tabs, and are stored in SQLite.
package rollout

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const (
	StateRunning = "running"
	// StatePaused: Git moved since the rollout was planned; waiting for the user to confirm the new revision.
	StatePaused = "paused"
	// StateFailed: a step failed; the user can resume (retry from that step) or abort.
	StateFailed = "failed"
	// StateInterrupted: argo-center restarted mid-rollout; the user can resume or abort.
	StateInterrupted = "interrupted"
	StateAborted     = "aborted"
	StateSucceeded   = "succeeded"
)

const (
	StepPending     = "pending"
	StepRefreshing  = "refreshing"
	StepSyncing     = "syncing"
	StepWaiting     = "waiting" // sync done, waiting for Healthy
	StepSucceeded   = "succeeded"
	StepSkipped     = "skipped" // already synced at the pinned revision and healthy
	StepPaused      = "paused"
	StepFailed      = "failed"
	StepAborted     = "aborted"
	StepInterrupted = "interrupted"
)

type Options struct {
	Prune              bool `json:"prune"`
	ApplyOutOfSyncOnly bool `json:"applyOutOfSyncOnly"`
}

type Step struct {
	Cluster string `json:"cluster"`
	// Revision is the commit this cluster is synced to, captured when the rollout was planned.
	// Empty for apps that cannot be pinned (auto-sync or multi-source).
	Revision string `json:"revision"`
	// NewRevision is the commit ArgoCD now compares against, set while the step is paused.
	NewRevision string    `json:"newRevision,omitempty"`
	State       string    `json:"state"`
	Message     string    `json:"message,omitempty"`
	StartedAt   time.Time `json:"startedAt,omitzero"`
	FinishedAt  time.Time `json:"finishedAt,omitzero"`
}

type Rollout struct {
	ID        int64   `json:"id"`
	App       string  `json:"app"`
	Options   Options `json:"options"`
	CreatedBy string  `json:"createdBy"`
	// ActingUser started, resumed or confirmed the rollout last; syncs are attributed to them.
	ActingUser string    `json:"actingUser"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
	FinishedAt time.Time `json:"finishedAt,omitzero"`
	State      string    `json:"state"`
	Message    string    `json:"message,omitempty"`
	Steps      []Step    `json:"steps"`
}

// Active reports whether the rollout still needs attention: it blocks starting another one for the app.
func (r *Rollout) Active() bool {
	return r.State != StateSucceeded && r.State != StateAborted
}

func (r *Rollout) finish(state, msg string) {
	r.State, r.Message, r.FinishedAt = state, msg, time.Now().UTC()
}

var (
	ErrNotFound = errors.New("rollout not found")
	// ErrConflict is returned for actions that do not fit the rollout's state.
	ErrConflict = errors.New("conflict")
)

type repo struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS rollouts (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	app        TEXT NOT NULL,
	state      TEXT NOT NULL,
	created_at TEXT NOT NULL,
	data       TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS rollouts_app ON rollouts (app, id);
`

func newRepo(db *sql.DB) (*repo, error) {
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("create rollouts table: %w", err)
	}
	return &repo{db: db}, nil
}

func (s *repo) insert(ctx context.Context, r *Rollout) error {
	data, _ := json.Marshal(r)
	res, err := s.db.ExecContext(ctx, `INSERT INTO rollouts (app, state, created_at, data) VALUES (?, ?, ?, ?)`,
		r.App, r.State, r.CreatedAt.Format(time.RFC3339Nano), string(data))
	if err != nil {
		return err
	}
	r.ID, err = res.LastInsertId()
	if err != nil {
		return err
	}
	return s.save(ctx, r) // store the ID inside data too
}

func (s *repo) save(ctx context.Context, r *Rollout) error {
	r.UpdatedAt = time.Now().UTC()
	data, _ := json.Marshal(r)
	_, err := s.db.ExecContext(ctx, `UPDATE rollouts SET state = ?, data = ? WHERE id = ?`, r.State, string(data), r.ID)
	return err
}

func (s *repo) get(ctx context.Context, id int64) (*Rollout, error) {
	var data string
	err := s.db.QueryRowContext(ctx, `SELECT data FROM rollouts WHERE id = ?`, id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var r Rollout
	return &r, json.Unmarshal([]byte(data), &r)
}

var activeStates = []any{StateRunning, StatePaused, StateFailed, StateInterrupted}

// list returns rollouts newest first. An empty app matches all apps.
func (s *repo) list(ctx context.Context, app string, activeOnly bool, limit int) ([]*Rollout, error) {
	q := `SELECT data FROM rollouts WHERE (? = '' OR app = ?)`
	args := []any{app, app}
	if activeOnly {
		q += ` AND state IN (?, ?, ?, ?)`
		args = append(args, activeStates...)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Rollout{}
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var r Rollout
		if err := json.Unmarshal([]byte(data), &r); err != nil {
			return nil, err
		}
		out = append(out, &r)
	}
	return out, rows.Err()
}

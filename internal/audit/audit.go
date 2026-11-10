// Package audit records every action argo-center takes on an ArgoCD instance, in SQLite and in the log.
package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

const (
	ActionSync        = "sync"
	ActionDryRun      = "dry-run"
	ActionTerminate   = "terminate"
	ActionHardRefresh = "refresh-hard"
	// ActionTargetRevision is a change of the revision an app tracks; Entry.Revision holds the new one.
	ActionTargetRevision = "target-revision"

	ActionRolloutStart   = "rollout-start"
	ActionRolloutAbort   = "rollout-abort"
	ActionRolloutResume  = "rollout-resume"
	ActionRolloutConfirm = "rollout-confirm"

	ResultOK    = "ok"
	ResultError = "error"
)

type Entry struct {
	ID       int64     `json:"id"`
	Time     time.Time `json:"time"`
	User     string    `json:"user"`
	Action   string    `json:"action"`
	App      string    `json:"app"`
	Cluster  string    `json:"cluster"`
	Revision string    `json:"revision,omitempty"`
	// Options are short descriptions of the choices made, e.g. "prune" or "resource Deployment/api".
	Options []string `json:"options"`
	Result  string   `json:"result"`
	Message string   `json:"message,omitempty"`
}

type Store struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS audit (
	id       INTEGER PRIMARY KEY AUTOINCREMENT,
	ts       TEXT NOT NULL,
	user     TEXT NOT NULL,
	action   TEXT NOT NULL,
	app      TEXT NOT NULL,
	cluster  TEXT NOT NULL,
	revision TEXT NOT NULL DEFAULT '',
	options  TEXT NOT NULL DEFAULT '[]',
	result   TEXT NOT NULL,
	message  TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS audit_app ON audit (app, id);
`

// New creates the audit table in db if needed.
func New(db *sql.DB) (*Store, error) {
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("create audit table: %w", err)
	}
	return &Store{db: db}, nil
}

// Record stores e, setting its time if unset, and logs it. A storage failure is logged and returned;
// callers should not fail the user's action because of it, since the action already happened.
func (s *Store) Record(ctx context.Context, e Entry) error {
	if e.Time.IsZero() {
		e.Time = time.Now().UTC()
	}
	if e.Options == nil {
		e.Options = []string{}
	}
	slog.Info("audit", "user", e.User, "action", e.Action, "app", e.App, "cluster", e.Cluster,
		"revision", e.Revision, "options", e.Options, "result", e.Result, "message", e.Message)

	opts, _ := json.Marshal(e.Options)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO audit (ts, user, action, app, cluster, revision, options, result, message) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.Time.Format(time.RFC3339Nano), e.User, e.Action, e.App, e.Cluster, e.Revision, string(opts), e.Result, e.Message)
	if err != nil {
		slog.Error("write audit entry", "err", err)
		return fmt.Errorf("write audit entry: %w", err)
	}
	return nil
}

type Filter struct {
	App, Cluster, User, Action string
	// Before, when set, returns only entries older than this entry ID (paging).
	Before int64
	// Limit defaults to 50 and is capped at 500.
	Limit int
}

// List returns matching entries, newest first.
func (s *Store) List(ctx context.Context, f Filter) ([]Entry, error) {
	var where []string
	var args []any
	for _, c := range []struct{ col, v string }{{"app", f.App}, {"cluster", f.Cluster}, {"user", f.User}, {"action", f.Action}} {
		if c.v != "" {
			where = append(where, c.col+" = ?")
			args = append(args, c.v)
		}
	}
	if f.Before > 0 {
		where = append(where, "id < ?")
		args = append(args, f.Before)
	}
	q := `SELECT id, ts, user, action, app, cluster, revision, options, result, message FROM audit`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	q += " ORDER BY id DESC LIMIT ?"
	args = append(args, min(limit, 500))

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Entry{}
	for rows.Next() {
		var e Entry
		var ts, opts string
		if err := rows.Scan(&e.ID, &ts, &e.User, &e.Action, &e.App, &e.Cluster, &e.Revision, &opts, &e.Result, &e.Message); err != nil {
			return nil, err
		}
		e.Time, _ = time.Parse(time.RFC3339Nano, ts)
		if err := json.Unmarshal([]byte(opts), &e.Options); err != nil {
			e.Options = []string{opts}
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Users returns everyone who appears in the audit log, sorted.
func (s *Store) Users(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT user FROM audit ORDER BY user`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

package rollout

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/snapp-incubator/argo-center/internal/argocd"
	"github.com/snapp-incubator/argo-center/internal/audit"
	"github.com/snapp-incubator/argo-center/internal/cache"
	"github.com/snapp-incubator/argo-center/internal/store"
)

// fakeCluster simulates one ArgoCD instance with one app named "api".
type fakeCluster struct {
	mu sync.Mutex
	// behavior after a sync: "ok" (Succeeded, Healthy), "fail" (operation Failed),
	// "degraded" (Succeeded, then Degraded), "progressing" (Succeeded, never Healthy).
	behavior string
	sync     string // Synced or OutOfSync
	revision string // what ArgoCD compares against
	health   string
	// moveTo, if set, becomes the revision on the next refresh (a commit landed).
	moveTo string

	clock      time.Time
	opPhase    string
	opStarted  time.Time
	opFinished time.Time
	reconciled time.Time
	pendingOp  bool

	syncs      []argocd.SyncRequest
	terminated int
}

func (f *fakeCluster) tick() time.Time {
	f.clock = f.clock.Add(time.Second)
	return f.clock
}

func (f *fakeCluster) appJSON() string {
	op := ""
	if f.opPhase != "" {
		op = fmt.Sprintf(`,"operationState":{"phase":%q,"message":"m","startedAt":%q,"finishedAt":%q,"operation":{"sync":{"revision":%q}}}`,
			f.opPhase, f.opStarted.Format(time.RFC3339), f.opFinished.Format(time.RFC3339), f.revision)
	}
	return fmt.Sprintf(`{"metadata":{"name":"api","namespace":"argocd"},"spec":{"source":{"repoURL":"r","path":"p","targetRevision":"HEAD"}},
"status":{"sync":{"status":%q,"revision":%q},"health":{"status":%q},"reconciledAt":%q%s}}`,
		f.sync, f.revision, f.health, f.reconciled.Format(time.RFC3339), op)
}

// advance moves a requested sync forward by one step each time the app is read.
func (f *fakeCluster) advance() {
	if !f.pendingOp {
		return
	}
	f.pendingOp = false
	f.opFinished = f.tick()
	switch f.behavior {
	case "fail":
		f.opPhase = "Failed"
	default:
		f.opPhase, f.sync = "Succeeded", "Synced"
		f.health = map[string]string{"ok": "Healthy", "degraded": "Degraded", "progressing": "Progressing"}[f.behavior]
	}
	f.reconciled = f.tick()
}

func (f *fakeCluster) serve(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case r.URL.Path == "/api/v1/applications":
			fmt.Fprint(w, `{"metadata":{"resourceVersion":"1"},"items":[`+f.appJSON()+`]}`)
		case r.URL.Path == "/api/v1/stream/applications":
			f.mu.Unlock()
			<-r.Context().Done()
			f.mu.Lock()
		case r.URL.Path == "/api/v1/applications/api/sync":
			var req argocd.SyncRequest
			json.NewDecoder(r.Body).Decode(&req)
			f.syncs = append(f.syncs, req)
			f.opPhase, f.opStarted, f.pendingOp = "Running", f.tick(), true
			fmt.Fprint(w, f.appJSON())
		case r.URL.Path == "/api/v1/applications/api/operation" && r.Method == http.MethodDelete:
			f.terminated++
			w.Write([]byte("{}"))
		case r.URL.Path == "/api/v1/applications/api":
			if r.URL.Query().Get("refresh") != "" {
				if f.moveTo != "" {
					f.revision, f.moveTo, f.sync = f.moveTo, "", "OutOfSync"
				}
				f.reconciled = f.tick()
			} else {
				f.advance()
			}
			fmt.Fprint(w, f.appJSON())
		default:
			http.NotFound(w, r)
		}
	}))
}

type harness struct {
	engine   *Engine
	clusters map[string]*fakeCluster
	audit    *audit.Store
}

func newHarness(t *testing.T, clusters ...*fakeCluster) *harness {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	auditLog, err := audit.New(db)
	if err != nil {
		t.Fatal(err)
	}

	h := &harness{clusters: map[string]*fakeCluster{}, audit: auditLog}
	var sources []cache.Source
	for i, f := range clusters {
		key := fmt.Sprintf("c%d", i+1)
		f.clock = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Hour)
		f.reconciled = f.clock
		srv := f.serve(t)
		t.Cleanup(srv.Close)
		client, _ := argocd.New(argocd.Options{BaseURL: srv.URL, Token: "t"})
		sources = append(sources, cache.Source{Key: key, URL: srv.URL, Client: client})
		h.clusters[key] = f
	}
	c := cache.New(sources, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go c.Run(ctx)
	waitFor(t, func() bool {
		for _, cl := range c.Clusters() {
			if cl.State != cache.StateOK {
				return false
			}
		}
		return true
	})

	h.engine, err = New(db, c, auditLog, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	h.engine.PollInterval = 5 * time.Millisecond
	h.engine.DegradedGrace = 50 * time.Millisecond
	if err := h.engine.Start(ctx); err != nil {
		t.Fatal(err)
	}
	return h
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (h *harness) waitState(t *testing.T, id int64, want string) *Rollout {
	t.Helper()
	var r *Rollout
	waitFor(t, func() bool {
		r, _ = h.engine.Get(context.Background(), id)
		return r != nil && r.State == want
	})
	return r
}

func steps(r *Rollout) string {
	var out []string
	for _, s := range r.Steps {
		out = append(out, s.Cluster+"="+s.State)
	}
	return strings.Join(out, " ")
}

func outOfSync(behavior string) *fakeCluster {
	return &fakeCluster{behavior: behavior, sync: "OutOfSync", revision: "r2", health: "Healthy"}
}

func create(t *testing.T, h *harness, clusters ...string) *Rollout {
	t.Helper()
	var req CreateRequest
	req.App, req.Options = "api", Options{ApplyOutOfSyncOnly: true}
	for _, c := range clusters {
		req.Steps = append(req.Steps, CreateStep{Cluster: c})
	}
	r, err := h.engine.Create(context.Background(), "ali", req)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRolloutInOrder(t *testing.T) {
	synced := &fakeCluster{behavior: "ok", sync: "Synced", revision: "r2", health: "Healthy"}
	h := newHarness(t, synced, outOfSync("ok"), outOfSync("ok"))
	r := create(t, h, "c1", "c2", "c3")

	r = h.waitState(t, r.ID, StateSucceeded)
	if got := steps(r); got != "c1=skipped c2=succeeded c3=succeeded" {
		t.Errorf("steps: %s", got)
	}
	if len(synced.syncs) != 0 {
		t.Error("synced cluster was synced again")
	}
	for _, key := range []string{"c2", "c3"} {
		reqs := h.clusters[key].syncs
		if len(reqs) != 1 || reqs[0].Revision != "r2" || reqs[0].SyncOptions == nil ||
			!strings.Contains(reqs[0].Infos[0].Value, "ali via argo-center rollout #") {
			t.Errorf("%s sync requests: %+v", key, reqs)
		}
	}
	// c3 must not start before c2 is healthy: c2's sync came first in time on separate fakes,
	// so check the recorded audit order instead.
	entries, _ := h.audit.List(context.Background(), audit.Filter{App: "api"})
	var actions []string
	for _, e := range entries {
		actions = append([]string{e.Action + ":" + e.Cluster}, actions...)
	}
	if got := strings.Join(actions, " "); got != "rollout-start: sync:c2 sync:c3" {
		t.Errorf("audit: %s", got)
	}
}

func TestRolloutStopsOnFailureAndResumes(t *testing.T) {
	h := newHarness(t, outOfSync("ok"), outOfSync("fail"), outOfSync("ok"))
	r := create(t, h, "c1", "c2", "c3")

	r = h.waitState(t, r.ID, StateFailed)
	if got := steps(r); got != "c1=succeeded c2=failed c3=pending" {
		t.Errorf("steps: %s", got)
	}
	if !strings.Contains(r.Message, "c2: sync failed") {
		t.Errorf("message: %q", r.Message)
	}

	h.clusters["c2"].mu.Lock()
	h.clusters["c2"].behavior, h.clusters["c2"].opPhase = "ok", "Failed"
	h.clusters["c2"].mu.Unlock()
	if _, err := h.engine.Resume(context.Background(), "sara", r.ID); err != nil {
		t.Fatal(err)
	}
	r = h.waitState(t, r.ID, StateSucceeded)
	if got := steps(r); got != "c1=succeeded c2=succeeded c3=succeeded" {
		t.Errorf("after resume: %s", got)
	}
	if len(h.clusters["c1"].syncs) != 1 {
		t.Error("resume re-synced a finished cluster")
	}
	if v := h.clusters["c3"].syncs[0].Infos[0].Value; !strings.HasPrefix(v, "sara ") {
		t.Errorf("sync after resume attributed to %q", v)
	}
}

func TestRolloutPausesWhenGitMoves(t *testing.T) {
	moved := outOfSync("ok")
	moved.moveTo = "r3"
	h := newHarness(t, outOfSync("ok"), moved)
	r := create(t, h, "c1", "c2")

	r = h.waitState(t, r.ID, StatePaused)
	if s := r.Steps[1]; s.State != StepPaused || s.NewRevision != "r3" || s.Revision != "r2" {
		t.Fatalf("paused step: %+v", s)
	}
	if _, err := h.engine.Resume(context.Background(), "ali", r.ID); !errors.Is(err, ErrConflict) {
		t.Errorf("resume of a paused rollout: %v", err)
	}
	if _, err := h.engine.Confirm(context.Background(), "ali", r.ID); err != nil {
		t.Fatal(err)
	}
	h.waitState(t, r.ID, StateSucceeded)
	if got := moved.syncs[0].Revision; got != "r3" {
		t.Errorf("confirmed step synced %q, want r3", got)
	}
}

func TestRolloutFailsWhenDegraded(t *testing.T) {
	h := newHarness(t, outOfSync("degraded"))
	r := h.waitState(t, create(t, h, "c1").ID, StateFailed)
	if !strings.Contains(r.Steps[0].Message, "Degraded") {
		t.Errorf("step: %+v", r.Steps[0])
	}
}

func TestAbortWithTerminate(t *testing.T) {
	slow := outOfSync("progressing")
	h := newHarness(t, slow, outOfSync("ok"))
	r := create(t, h, "c1", "c2")
	waitFor(t, func() bool {
		r, _ = h.engine.Get(context.Background(), r.ID)
		return r.Steps[0].State == StepWaiting
	})

	// A second rollout for the same app is refused while this one is active.
	if _, err := h.engine.Create(context.Background(), "ali", CreateRequest{App: "api", Steps: []CreateStep{{Cluster: "c2"}}}); !errors.Is(err, ErrConflict) {
		t.Errorf("second rollout: %v", err)
	}
	if _, busy := h.engine.Busy(context.Background(), "api", "c2"); !busy {
		t.Error("Busy should report the running rollout")
	}

	r, err := h.engine.Abort(context.Background(), "sara", r.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if r.State != StateAborted || steps(r) != "c1=aborted c2=aborted" {
		t.Errorf("after abort: %s %s", r.State, steps(r))
	}
	// Not syncing anymore (waiting for health), so nothing to terminate.
	if slow.terminated != 0 {
		t.Errorf("terminated %d operations", slow.terminated)
	}
	time.Sleep(50 * time.Millisecond)
	if r, _ = h.engine.Get(context.Background(), r.ID); r.State != StateAborted {
		t.Errorf("worker overwrote the abort: %s", r.State)
	}
	if len(h.clusters["c2"].syncs) != 0 {
		t.Error("aborted rollout went on to the next cluster")
	}
}

func TestStartMarksRunningAsInterrupted(t *testing.T) {
	h := newHarness(t, outOfSync("progressing"))
	r := create(t, h, "c1")
	waitFor(t, func() bool {
		r, _ = h.engine.Get(context.Background(), r.ID)
		return r.Steps[0].State == StepWaiting
	})
	h.engine.stop(r.ID)

	// A new engine on the same database, as after a restart.
	e2, _ := New(h.engine.store.db, h.engine.cache, h.audit, time.Second)
	if err := e2.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	r, _ = e2.Get(context.Background(), r.ID)
	if r.State != StateInterrupted || r.Steps[0].State != StepInterrupted {
		t.Errorf("after restart: %s %s", r.State, steps(r))
	}
}

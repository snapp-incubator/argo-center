package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/snapp-incubator/argo-center/internal/argocd"
	"github.com/snapp-incubator/argo-center/internal/audit"
	"github.com/snapp-incubator/argo-center/internal/auth"
	"github.com/snapp-incubator/argo-center/internal/cache"
	"github.com/snapp-incubator/argo-center/internal/rollout"
	"github.com/snapp-incubator/argo-center/internal/store"
)

func TestRoutes(t *testing.T) {
	static := fstest.MapFS{
		"index.html":    {Data: []byte("<html>app</html>")},
		"assets/app.js": {Data: []byte("console.log(1)")},
	}
	srv, _ := newServer(t, cache.New(nil, time.Hour), static)

	cases := []struct {
		path, wantBody, wantCache string
		wantStatus                int
	}{
		{"/api/clusters", "[]", "", 200},
		{"/api/apps", "[]", "", 200},
		{"/api/apps/missing", "not found on any cluster", "", 404},
		{"/api/nope", "not found", "", 404},
		{"/api/rollouts?app=x", "[]", "", 200},
		{"/api/rollouts/7", "rollout not found", "", 404},
		{"/", "<html>app</html>", "no-cache", 200},
		{"/apps/api-gateway", "<html>app</html>", "no-cache", 200},
		{"/assets/app.js", "console.log(1)", "immutable", 200},
		{"/healthz", "ok", "", 200},
	}
	for _, tc := range cases {
		resp, err := http.Get(srv.URL + tc.path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != tc.wantStatus || !strings.Contains(string(body), tc.wantBody) ||
			!strings.Contains(resp.Header.Get("Cache-Control"), tc.wantCache) {
			t.Errorf("%s: got %d %q (Cache-Control %q)", tc.path, resp.StatusCode, body, resp.Header.Get("Cache-Control"))
		}
	}
}

func TestDiffAndRefresh(t *testing.T) {
	const app = `{"metadata":{"name":"api","namespace":"argocd"},"status":{"sync":{"status":"OutOfSync","revision":"abc123"},"reconciledAt":"2026-10-31T10:00:00Z"}}`
	cm := func(v string) string {
		return strconv.Quote(`{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"c"},"data":{"LOG_LEVEL":"` + v + `"}}`)
	}
	var refreshed, synced, patched atomic.Value
	argo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/applications":
			fmt.Fprint(w, `{"metadata":{"resourceVersion":"1"},"items":[`+app+`]}`)
		case "/api/v1/stream/applications":
			<-r.Context().Done()
		case "/api/v1/applications/api/managed-resources":
			fmt.Fprintf(w, `{"items":[{"kind":"ConfigMap","name":"c","modified":true,"targetState":%s,"liveState":%s,"normalizedLiveState":%s,"predictedLiveState":%s}]}`,
				cm("warn"), cm("info"), cm("info"), cm("warn"))
		case "/api/v1/applications/api/sync":
			b, _ := io.ReadAll(r.Body)
			synced.Store(b)
			fmt.Fprint(w, app)
		case "/api/v1/applications/api/operation":
			w.WriteHeader(http.StatusConflict)
			fmt.Fprint(w, `{"message":"no operation in progress"}`)
		case "/api/v1/applications/api":
			if r.Method == http.MethodPatch {
				b, _ := io.ReadAll(r.Body)
				patched.Store(b)
				fmt.Fprint(w, strings.Replace(app, `"status"`, `"spec":{"source":{"targetRevision":"release-1"}},"status"`, 1))
				return
			}
			refreshed.Store(r.URL.Query().Get("refresh") + " " + r.URL.Query().Get("appNamespace"))
			fmt.Fprint(w, app)
		default:
			http.NotFound(w, r)
		}
	}))
	defer argo.Close()

	client, err := argocd.New(argocd.Options{BaseURL: argo.URL, Token: "t"})
	if err != nil {
		t.Fatal(err)
	}
	c := cache.New([]cache.Source{{Key: "cluster-a", URL: argo.URL, Client: client}}, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	srv, auditLog := newServer(t, c, fstest.MapFS{})
	for deadline := time.Now().Add(3 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if _, found := c.App("api"); found {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("cache never loaded")
		}
	}

	resp, err := http.Get(srv.URL + "/api/apps/api/diff?cluster=cluster-a")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Revision  string
		Resources []struct {
			Kind, Name, Status, UnifiedDiff string
			Changes                         []struct{ Path, Op string }
		}
	}
	json.NewDecoder(resp.Body).Decode(&got)
	resp.Body.Close()
	if resp.StatusCode != 200 || got.Revision != "abc123" || len(got.Resources) != 1 {
		t.Fatalf("diff: %d %+v", resp.StatusCode, got)
	}
	r := got.Resources[0]
	if r.Status != "modified" || len(r.Changes) != 1 || r.Changes[0].Path != "data.LOG_LEVEL" ||
		!strings.Contains(r.UnifiedDiff, "-  LOG_LEVEL: info\n+  LOG_LEVEL: warn\n") {
		t.Errorf("resource = %+v", r)
	}

	for path, want := range map[string]int{
		"/api/apps/api/diff?cluster=nope":        400,
		"/api/apps/other/diff?cluster=cluster-a": 404,
	} {
		resp, _ := http.Get(srv.URL + path)
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("%s: status %d, want %d", path, resp.StatusCode, want)
		}
	}

	if code := post(t, "POST", srv.URL+"/api/apps/api/refresh?cluster=cluster-a&hard=true", ""); code != 204 || refreshed.Load() != "hard argocd" {
		t.Errorf("refresh: status %d, ArgoCD saw %v", code, refreshed.Load())
	}

	if code := post(t, "POST", srv.URL+"/api/apps/api/sync?cluster=cluster-a",
		`{"revision":"abc123","prune":true,"applyOutOfSyncOnly":true,"resources":[{"kind":"ConfigMap","name":"c"}]}`); code != 202 {
		t.Errorf("sync: status %d", code)
	}
	var sync argocd.SyncRequest
	json.Unmarshal(synced.Load().([]byte), &sync)
	if sync.Revision != "abc123" || !sync.Prune || sync.DryRun || sync.AppNamespace != "argocd" || len(sync.Resources) != 1 ||
		sync.SyncOptions == nil || sync.SyncOptions.Items[0] != "ApplyOutOfSyncOnly=true" || sync.Infos[0].Value != "local via argo-center" {
		t.Errorf("ArgoCD got sync request %+v", sync)
	}

	if code := post(t, "DELETE", srv.URL+"/api/apps/api/operation?cluster=cluster-a", ""); code != 409 {
		t.Errorf("terminate with ArgoCD error: status %d, want 409", code)
	}

	for body, want := range map[string]int{
		`{"targetRevision":""}`:              400,
		`{"targetRevision":"two words"}`:     400,
		`{"targetRevision":" release-1 \n"}`: 204,
	} {
		if code := post(t, "PUT", srv.URL+"/api/apps/api/target-revision?cluster=cluster-a", body); code != want {
			t.Errorf("target revision %s: status %d, want %d", body, code, want)
		}
	}
	var patch argocd.PatchRequest
	json.Unmarshal(patched.Load().([]byte), &patch)
	if patch.Name != "api" || patch.AppNamespace != "argocd" || patch.PatchType != "merge" ||
		patch.Patch != `{"spec":{"source":{"targetRevision":"release-1"}}}` {
		t.Errorf("ArgoCD got patch request %+v", patch)
	}
	if row, _ := c.App("api"); row.Cells["cluster-a"].TargetRevision != "release-1" {
		t.Errorf("cache has target revision %q after the change", row.Cells["cluster-a"].TargetRevision)
	}

	entries, _ := auditLog.List(context.Background(), audit.Filter{App: "api"})
	var actions []string
	for _, e := range entries {
		actions = append(actions, e.Action+":"+e.Result)
	}
	if want := "target-revision:ok terminate:error sync:ok refresh-hard:ok"; strings.Join(actions, " ") != want {
		t.Errorf("audit = %v, want %s", actions, want)
	}
	if e := entries[0]; e.Revision != "release-1" || len(e.Options) != 1 || e.Options[0] != "was HEAD" {
		t.Errorf("target revision audit entry = %+v", e)
	}
}

func openAudit(t *testing.T) (*audit.Store, *sql.DB) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s, err := audit.New(db)
	if err != nil {
		t.Fatal(err)
	}
	return s, db
}

func newServer(t *testing.T, c *cache.Cache, static fs.FS) (*httptest.Server, *audit.Store) {
	t.Helper()
	auditLog, db := openAudit(t)
	engine, err := rollout.New(db, c, auditLog, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(c, static, auth.New(auth.Config{Disabled: true}), auditLog, engine))
	t.Cleanup(srv.Close)
	return srv, auditLog
}

func post(t *testing.T, method, url, body string) int {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	req.Header.Set("X-Requested-With", "argo-center")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

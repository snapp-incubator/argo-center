package cache

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/snapp-incubator/argo-center/internal/argocd"
)

func appJSON(name, sync, health, rev string) string {
	return fmt.Sprintf(`{"metadata":{"name":%q,"namespace":"argocd"},"spec":{"project":"platform","source":{"repoURL":"https://git.example.com/x/deploy.git","path":"apps/%s","targetRevision":"main"}},
"status":{"sync":{"status":%q,"revision":%q},"health":{"status":%q},"history":[{"revision":"old1"},{"revision":"old2"}],
"resources":[{"kind":"Deployment","name":"a","status":"OutOfSync"},{"kind":"Service","name":"b","status":"Synced"}]}}`,
		name, name, sync, rev, health)
}

// fakeArgo serves a fixed list and then the given watch messages, holding the stream open afterwards.
func fakeArgo(t *testing.T, list string, watch []string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"error":"invalid session","code":16,"message":"invalid session: token is expired"}`)
			return
		}
		switch r.URL.Path {
		case "/api/v1/applications":
			fmt.Fprint(w, list)
		case "/api/v1/stream/applications":
			if got := r.URL.Query().Get("resourceVersion"); got != "42" {
				t.Errorf("watch resourceVersion = %q, want 42", got)
			}
			for _, m := range watch {
				fmt.Fprintln(w, m)
				w.(http.Flusher).Flush()
			}
			<-r.Context().Done()
		default:
			http.NotFound(w, r)
		}
	}))
}

func newSource(t *testing.T, key, url, token string) Source {
	t.Helper()
	cl, err := argocd.New(argocd.Options{BaseURL: url, Token: token})
	if err != nil {
		t.Fatal(err)
	}
	return Source{Key: key, DisplayName: key, URL: url, Client: cl}
}

func eventually(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestListAndWatch(t *testing.T) {
	list := `{"metadata":{"resourceVersion":"42"},"items":[` +
		appJSON("api-gateway", "OutOfSync", "Healthy", "new") + "," +
		appJSON("legacy", "Synced", "Healthy", "r1") + `]}`
	watch := []string{
		`{"result":{"type":"MODIFIED","application":` + appJSON("api-gateway", "Synced", "Progressing", "new") + `}}`,
		`{"result":{"type":"DELETED","application":{"metadata":{"name":"legacy"}}}}`,
		`{"result":{"type":"ADDED","application":` + appJSON("only-here", "Synced", "Healthy", "r9") + `}}`,
	}
	srvA := fakeArgo(t, list, watch)
	defer srvA.Close()
	srvB := fakeArgo(t, `{"metadata":{"resourceVersion":"42"},"items":[`+appJSON("api-gateway", "Synced", "Healthy", "new")+`]}`, nil)
	defer srvB.Close()

	c := New([]Source{newSource(t, "cluster-a", srvA.URL, "tok"), newSource(t, "cluster-b", srvB.URL, "tok")}, time.Hour)
	events, unsubscribe := c.Subscribe()
	defer unsubscribe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)

	eventually(t, func() bool {
		apps := c.Apps()
		return len(apps) == 2 && apps[0].Name == "api-gateway" && apps[1].Name == "only-here" &&
			apps[0].Cells["cluster-a"].HealthStatus == "Progressing" && len(apps[0].Cells) == 2
	})

	row, ok := c.App("api-gateway")
	if !ok {
		t.Fatal("api-gateway not found")
	}
	got := row.Cells["cluster-a"]
	if got.SyncStatus != "Synced" || got.Revision != "new" || got.DeployedRevision != "old2" ||
		got.OutOfSyncResources != 1 || got.Path != "apps/api-gateway" ||
		got.ArgoURL != srvA.URL+"/applications/argocd/api-gateway" {
		t.Errorf("unexpected summary: %+v", got)
	}
	if _, ok := c.App("legacy"); ok {
		t.Error("deleted app still present")
	}
	if row, _ := c.App("only-here"); len(row.Cells) != 1 {
		t.Errorf("only-here should exist on one cluster, got %v", row.Cells)
	}
	for _, cl := range c.Clusters() {
		if cl.State != StateOK {
			t.Errorf("cluster %s state %s: %s", cl.Key, cl.State, cl.Error)
		}
	}

	seen := map[string]bool{}
	for len(events) > 0 {
		e := <-events
		seen[e.Type+"/"+e.Cluster+"/"+e.App] = true
	}
	for _, want := range []string{"app-changed/cluster-a/api-gateway", "app-deleted/cluster-a/legacy", "app-changed/cluster-a/only-here", "cluster-changed/cluster-b/"} {
		if !seen[want] {
			t.Errorf("missing event %s (got %v)", want, seen)
		}
	}
}

func TestBadTokenMarksClusterError(t *testing.T) {
	srv := fakeArgo(t, `{}`, nil)
	defer srv.Close()

	c := New([]Source{newSource(t, "cluster-a", srv.URL, "wrong")}, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)

	eventually(t, func() bool { return c.Clusters()[0].State == StateError })
	if msg := c.Clusters()[0].Error; !strings.Contains(msg, "token rejected") || !strings.Contains(msg, "token is expired") {
		t.Errorf("error message = %q", msg)
	}
}

func TestReconcileOnlyChangeIsSilent(t *testing.T) {
	a := AppSummary{Name: "x", ReconciledAt: time.Unix(1, 0)}
	b := a
	b.ReconciledAt = time.Unix(2, 0)
	if !sameIgnoringReconcile(a, b) {
		t.Error("reconciledAt-only change should be ignored")
	}
	b.HealthStatus = "Degraded"
	if sameIgnoringReconcile(a, b) {
		t.Error("health change should be detected")
	}
}

// cuttingArgo cuts every watch the way Envoy does, per the given script: "event" sends one change
// (resourceVersion 50) and closes, "504" answers with a gateway timeout, "hold" keeps the stream open.
func cuttingArgo(t *testing.T, script []string) (srv *httptest.Server, lists *atomic.Int32, watchRVs func() []string) {
	t.Helper()
	lists = &atomic.Int32{}
	var mu sync.Mutex
	var rvs []string
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/applications":
			lists.Add(1)
			fmt.Fprint(w, `{"metadata":{"resourceVersion":"42"},"items":[`+appJSON("api", "Synced", "Healthy", "r1")+`]}`)
		case "/api/v1/stream/applications":
			mu.Lock()
			n := len(rvs)
			rvs = append(rvs, r.URL.Query().Get("resourceVersion"))
			mu.Unlock()
			step := "hold"
			if n < len(script) {
				step = script[n]
			}
			switch step {
			case "event":
				app := strings.Replace(appJSON("api", "OutOfSync", "Healthy", "r2"), `"namespace":"argocd"`, `"namespace":"argocd","resourceVersion":"50"`, 1)
				fmt.Fprintln(w, `{"result":{"type":"MODIFIED","application":`+app+`}}`)
			case "504":
				w.WriteHeader(http.StatusGatewayTimeout)
				fmt.Fprint(w, "upstream request timeout")
			default:
				w.(http.Flusher).Flush()
				<-r.Context().Done()
			}
		}
	}))
	return srv, lists, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(rvs)
	}
}

func TestWatchResumesAfterProxyCuts(t *testing.T) {
	srv, lists, watchRVs := cuttingArgo(t, []string{"event", "504"})
	defer srv.Close()
	c := New([]Source{newSource(t, "cluster-a", srv.URL, "tok")}, time.Hour)
	events, unsubscribe := c.Subscribe()
	defer unsubscribe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)

	eventually(t, func() bool { return len(watchRVs()) == 3 })
	if got := watchRVs(); !slices.Equal(got, []string{"42", "50", "50"}) {
		t.Errorf("watch resourceVersions = %v, want resume from the last event", got)
	}
	if lists.Load() != 1 {
		t.Errorf("listed %d times, want 1 (no re-list after a cut)", lists.Load())
	}
	if st := c.Clusters()[0]; st.State != StateOK {
		t.Errorf("cluster state = %s (%s), want ok", st.State, st.Error)
	}
	if row, _ := c.App("api"); row.Cells["cluster-a"].SyncStatus != "OutOfSync" {
		t.Errorf("event before the cut not applied: %+v", row)
	}
	for len(events) > 0 {
		if e := <-events; e.Type == "cluster-changed" && c.Clusters()[0].State == StateError {
			t.Error("cluster flapped to error")
		}
	}
}

func TestWatchFailsWhenStreamsKeepBreaking(t *testing.T) {
	srv, _, _ := cuttingArgo(t, []string{"504", "504", "504"})
	defer srv.Close()
	c := New([]Source{newSource(t, "cluster-a", srv.URL, "tok")}, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	eventually(t, func() bool { return c.Clusters()[0].State == StateError })
	if err := c.Clusters()[0].Error; !strings.Contains(err, "keeps breaking") {
		t.Errorf("error = %q", err)
	}
}

func TestNewerVersion(t *testing.T) {
	for _, tc := range [][3]string{{"42", "50", "50"}, {"50", "42", "50"}, {"", "7", "7"}, {"9", "", "9"}} {
		if got := newerVersion(tc[0], tc[1]); got != tc[2] {
			t.Errorf("newerVersion(%q, %q) = %q", tc[0], tc[1], got)
		}
	}
}

// Package api serves argo-center's JSON API, the SSE event stream and the embedded frontend.
package api

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/snapp-incubator/argo-center/internal/argocd"
	"github.com/snapp-incubator/argo-center/internal/audit"
	"github.com/snapp-incubator/argo-center/internal/auth"
	"github.com/snapp-incubator/argo-center/internal/cache"
	"github.com/snapp-incubator/argo-center/internal/diff"
	"github.com/snapp-incubator/argo-center/internal/rollout"
)

type Server struct {
	rollouts *rollout.Engine
	cache    *cache.Cache
	auth     *auth.Authenticator
	audit    *audit.Store
	mux      *http.ServeMux
	handler  http.Handler
}

// New returns the HTTP handler. static is the built frontend (index.html at its root).
func New(c *cache.Cache, static fs.FS, authn *auth.Authenticator, auditLog *audit.Store, rollouts *rollout.Engine) *Server {
	s := &Server{cache: c, auth: authn, audit: auditLog, rollouts: rollouts, mux: http.NewServeMux()}
	s.mux.HandleFunc("POST /api/rollouts", s.rolloutCreate)
	s.mux.HandleFunc("GET /api/rollouts", s.rolloutList)
	s.mux.HandleFunc("GET /api/rollouts/{id}", s.rolloutGet)
	s.mux.HandleFunc("POST /api/rollouts/{id}/{action}", s.rolloutAction)
	s.handler = authn.Middleware(s.mux)
	authn.Register(s.mux)
	s.mux.HandleFunc("GET /api/me", authn.Me)
	s.mux.HandleFunc("GET /api/audit", s.auditList)
	s.mux.HandleFunc("GET /api/audit/users", s.auditUsers)
	s.mux.HandleFunc("POST /api/apps/{name}/sync", s.sync)
	s.mux.HandleFunc("DELETE /api/apps/{name}/operation", s.terminate)
	s.mux.HandleFunc("GET /api/clusters", s.clusters)
	s.mux.HandleFunc("GET /api/apps", s.apps)
	s.mux.HandleFunc("GET /api/apps/{name}", s.app)
	s.mux.HandleFunc("GET /api/apps/{name}/diff", s.diff)
	s.mux.HandleFunc("POST /api/apps/{name}/refresh", s.refresh)
	s.mux.HandleFunc("GET /api/events", s.events)
	s.mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not found")
	})
	s.mux.HandleFunc("GET /healthz", ok)
	s.mux.HandleFunc("GET /readyz", ok)
	s.mux.Handle("/", spa(static))
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.handler.ServeHTTP(w, r)
}

func (s *Server) clusters(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.cache.Clusters())
}

func (s *Server) apps(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.cache.Apps())
}

func (s *Server) app(w http.ResponseWriter, r *http.Request) {
	row, found := s.cache.App(r.PathValue("name"))
	if !found {
		writeError(w, http.StatusNotFound, "application not found on any cluster")
		return
	}
	writeJSON(w, http.StatusOK, row)
}

// target resolves the ?cluster= query and the {name} path value, writing an error response if either is unknown.
func (s *Server) target(w http.ResponseWriter, r *http.Request) (cache.Source, cache.AppSummary, bool) {
	cluster := r.URL.Query().Get("cluster")
	src, summary, knownCluster, found := s.cache.Lookup(cluster, r.PathValue("name"))
	switch {
	case !knownCluster:
		writeError(w, http.StatusBadRequest, fmt.Sprintf("unknown cluster %q", cluster))
	case !found:
		writeError(w, http.StatusNotFound, fmt.Sprintf("application not found on %s", cluster))
	}
	return src, summary, knownCluster && found
}

type diffResponse struct {
	Cluster string `json:"cluster"`
	// Revision and ReconciledAt describe the comparison the diff comes from.
	Revision     string          `json:"revision"`
	ReconciledAt time.Time       `json:"reconciledAt,omitzero"`
	Resources    []diff.Resource `json:"resources"`
}

func (s *Server) diff(w http.ResponseWriter, r *http.Request) {
	src, summary, ok := s.target(w, r)
	if !ok {
		return
	}
	items, err := src.Client.ManagedResources(r.Context(), summary.Name, summary.Namespace)
	if err != nil {
		upstreamError(w, src.Key, err)
		return
	}
	resources, err := diff.Compute(items)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "compute diff: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, diffResponse{
		Cluster:      src.Key,
		Revision:     summary.Revision,
		ReconciledAt: summary.ReconciledAt,
		Resources:    resources,
	})
}

// refreshTimeout bounds a refresh. ArgoCD answers once the comparison is done; a hard refresh
// regenerates manifests, which can take a while for large Helm charts.
const refreshTimeout = 3 * time.Minute

func (s *Server) refresh(w http.ResponseWriter, r *http.Request) {
	src, summary, ok := s.target(w, r)
	if !ok {
		return
	}
	hard := r.URL.Query().Get("hard") == "true"
	ctx, cancel := context.WithTimeout(r.Context(), refreshTimeout)
	defer cancel()
	start := time.Now()
	app, err := src.Client.RefreshApplication(ctx, summary.Name, summary.Namespace, hard)
	if errors.Is(err, context.DeadlineExceeded) {
		err = fmt.Errorf("refresh did not finish within %s", refreshTimeout)
	}
	if hard {
		// A hard refresh is audited: it regenerates manifests, which loads the repo server.
		s.record(r, audit.Entry{Action: audit.ActionHardRefresh, App: summary.Name, Cluster: src.Key}, err)
	}
	if err != nil {
		upstreamError(w, src.Key, err)
		return
	}
	// The watch delivers this too, but maybe after the client has refetched; store it now so the
	// client sees the new comparison (and its reconcile time) right away.
	s.cache.Put(src.Key, *app)
	slog.Info("refreshed", "app", summary.Name, "cluster", src.Key, "hard", hard, "took", time.Since(start).Round(time.Millisecond).String())
	w.WriteHeader(http.StatusNoContent)
}

type syncBody struct {
	// Revision is the commit whose diff the user reviewed. The sync applies exactly that commit,
	// even if a newer one has landed since.
	Revision           string                `json:"revision"`
	Prune              bool                  `json:"prune"`
	DryRun             bool                  `json:"dryRun"`
	ApplyOutOfSyncOnly bool                  `json:"applyOutOfSyncOnly"`
	Resources          []argocd.SyncResource `json:"resources"`
}

func (s *Server) sync(w http.ResponseWriter, r *http.Request) {
	src, summary, ok := s.target(w, r)
	if !ok {
		return
	}
	if ro, busy := s.rollouts.Busy(r.Context(), summary.Name, src.Key); busy {
		writeError(w, http.StatusConflict, fmt.Sprintf("rollout #%d is running on %s; abort it or wait for it to finish", ro.ID, src.Key))
		return
	}
	var body syncBody
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	user := auth.UserFrom(r.Context())
	req := argocd.SyncRequest{
		AppNamespace: summary.Namespace,
		Prune:        body.Prune,
		DryRun:       body.DryRun,
		Resources:    body.Resources,
		Infos:        []argocd.Info{{Name: "Requested by", Value: user.Name + " via argo-center"}},
	}
	var opts []string
	switch {
	case summary.AutoSync:
		// ArgoCD refuses a pinned revision while auto-sync is on.
		opts = append(opts, "not pinned: auto-sync is on")
	case summary.MultiSource:
		opts = append(opts, "not pinned: multi-source app")
	default:
		req.Revision = cmp.Or(body.Revision, summary.Revision)
	}
	if body.ApplyOutOfSyncOnly {
		req.SyncOptions = &argocd.SyncOptions{Items: []string{"ApplyOutOfSyncOnly=true"}}
		opts = append(opts, "apply out-of-sync only")
	}
	if body.Prune {
		opts = append(opts, "prune")
	}
	for _, res := range body.Resources {
		opts = append(opts, "resource "+res.Kind+"/"+res.Name)
	}

	action := audit.ActionSync
	if body.DryRun {
		action = audit.ActionDryRun
	}
	err := src.Client.Sync(r.Context(), summary.Name, req)
	s.record(r, audit.Entry{Action: action, App: summary.Name, Cluster: src.Key, Revision: req.Revision, Options: opts}, err)
	if err != nil {
		upstreamError(w, src.Key, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"revision": req.Revision})
}

func (s *Server) terminate(w http.ResponseWriter, r *http.Request) {
	src, summary, ok := s.target(w, r)
	if !ok {
		return
	}
	err := src.Client.TerminateOperation(r.Context(), summary.Name, summary.Namespace)
	s.record(r, audit.Entry{Action: audit.ActionTerminate, App: summary.Name, Cluster: src.Key}, err)
	if err != nil {
		upstreamError(w, src.Key, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) auditList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	before, _ := strconv.ParseInt(q.Get("before"), 10, 64)
	entries, err := s.audit.List(r.Context(), audit.Filter{
		App: q.Get("app"), Cluster: q.Get("cluster"), User: q.Get("user"), Action: q.Get("action"),
		Before: before, Limit: limit,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, entries)
}

func (s *Server) auditUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.audit.Users(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, users)
}

func (s *Server) rolloutCreate(w http.ResponseWriter, r *http.Request) {
	var req rollout.CreateRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	ro, err := s.rollouts.Create(r.Context(), auth.UserFrom(r.Context()).Name, req)
	if err != nil {
		rolloutError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, ro)
}

func (s *Server) rolloutList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	list, err := s.rollouts.List(r.Context(), q.Get("app"), q.Get("active") == "true", limit)
	if err != nil {
		rolloutError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) rolloutGet(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusNotFound, "rollout not found")
		return
	}
	ro, err := s.rollouts.Get(r.Context(), id)
	if err != nil {
		rolloutError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ro)
}

func (s *Server) rolloutAction(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusNotFound, "rollout not found")
		return
	}
	user := auth.UserFrom(r.Context()).Name
	var ro *rollout.Rollout
	switch r.PathValue("action") {
	case "abort":
		var body struct {
			Terminate bool `json:"terminate"`
		}
		json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body)
		ro, err = s.rollouts.Abort(r.Context(), user, id, body.Terminate)
	case "resume":
		ro, err = s.rollouts.Resume(r.Context(), user, id)
	case "confirm":
		ro, err = s.rollouts.Confirm(r.Context(), user, id)
	default:
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		rolloutError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ro)
}

func rolloutError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, rollout.ErrNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, rollout.ErrInvalid):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, rollout.ErrConflict):
		writeError(w, http.StatusConflict, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}

// record writes an audit entry for the request's user. err is the action's outcome.
func (s *Server) record(r *http.Request, e audit.Entry, err error) {
	e.User = auth.UserFrom(r.Context()).Name
	e.Result = audit.ResultOK
	if err != nil {
		e.Result, e.Message = audit.ResultError, err.Error()
	}
	// Recorded even if the client went away: the action itself was already sent to ArgoCD.
	s.audit.Record(context.WithoutCancel(r.Context()), e)
}

// upstreamError reports an ArgoCD failure. ArgoCD refusing the request in its current state
// (e.g. "another operation is already in progress") becomes 409; token and network problems 502.
func upstreamError(w http.ResponseWriter, cluster string, err error) {
	status := http.StatusBadGateway
	var apiErr *argocd.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.StatusCode {
		case http.StatusNotFound:
			status = http.StatusNotFound
		case http.StatusBadRequest, http.StatusConflict, http.StatusPreconditionFailed:
			status = http.StatusConflict
		}
	}
	writeError(w, status, fmt.Sprintf("%s: %v", cluster, err))
}

// sseHeartbeat keeps the stream alive through the OpenShift router, whose default idle timeout is 30s.
const sseHeartbeat = 15 * time.Second

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	flusher, canFlush := w.(http.Flusher)
	if !canFlush {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	events, unsubscribe := s.cache.Subscribe()
	defer unsubscribe()

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()

	ticker := time.NewTicker(sseHeartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			fmt.Fprint(w, ": ping\n\n")
		case e := <-events:
			data, _ := json.Marshal(e)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.Type, data)
		}
		flusher.Flush()
	}
}

func ok(w http.ResponseWriter, r *http.Request) {
	w.Write([]byte("ok\n"))
}

// spa serves files from static and falls back to index.html for client-side routes.
func spa(static fs.FS) http.Handler {
	files := http.FileServerFS(static)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name != "" && name != "index.html" {
			if info, err := fs.Stat(static, name); err == nil && !info.IsDir() {
				if strings.HasPrefix(name, "assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				files.ServeHTTP(w, r)
				return
			}
		}
		index, err := fs.ReadFile(static, "index.html")
		if err != nil {
			http.Error(w, "frontend not built: run `make web`", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Write(index)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Warn("write response", "err", err)
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

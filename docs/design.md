# argo-center — Design

One panel to see and sync ArgoCD applications across many clusters, without switching between ArgoCD UIs.

## 1. Goals / non-goals

**Goals**
- See every app's sync/health status across all clusters at a glance.
- For one app, see a brief diff per cluster (what would change), with full diff on demand.
- Sync an app on one cluster, or roll it out to several clusters in order, from one screen.
- Know who synced what, when (ArgoCD sees a shared service token, so we keep our own audit log).

**Non-goals (v1)**
- Creating/editing apps, rollback, resource actions, pod logs/exec. Use ArgoCD UI for those (we deep-link to it).
- Per-user permissions. Everyone who can log in has full access.

## 2. Clusters

Each cluster runs its own ArgoCD. The clusters are listed in the config, and that order is also the default rollout order.

An app is identified by **the same name on every instance**. **Every app on any instance is listed**, including apps that exist on only one cluster; a cluster where the app is missing shows "—", not an error.

The panel is built around apps with a **single Git source** (`spec.source`), so one revision per app per cluster. Multi-source apps are listed, but their syncs are not pinned to a commit (7.3).

## 3. Architecture

```
            ┌──────────── argo-center (1 pod, in a cluster that reaches all) ────────────┐
Browser ──► │  React SPA (embedded)                                                      │
  OIDC      │  Go HTTP API ── auth (your OIDC provider) ── session cookie                 │
  session   │      │                                                                     │
            │      ├─ cache  ◄── watch stream per instance ──┐                           │
            │      ├─ diff   ◄── managed-resources (on demand)│                           │
            │      ├─ rollout engine ── sync / wait ──────────┤──► N × ArgoCD REST API    │
            │      └─ audit (SQLite on PVC)                   │    (Bearer token each)    │
            └──────────────────────────────────────────────────┘                          │
```

- **Single Go binary** serves the API and the built React app. The React app is compiled (`npm run build`) to static files, which are embedded into the Go binary with `embed.FS` at build time. The Go server serves `/api/*` and `/auth/*`, and falls back to `index.html` for every other path (SPA routing). One image, one origin: no CORS, and the session cookie needs no cross-site settings.
  - Local dev: Vite dev server (hot reload) proxies `/api` and `/auth` to the Go server.
  - Alternative if ever needed: nginx container for static files + Go container for the API, split by path on the Route. Not planned.
- **Single replica.** In-memory cache + SQLite keep it simple; no HA needed for a team tool.
- The browser never talks to ArgoCD and never sees ArgoCD tokens.

## 4. Auth

### 4.1 Users → argo-center
- OIDC login against your provider (`coreos/go-oidc` + `x/oauth2`, auth-code flow).
- Session: HMAC-signed (not encrypted: it holds only identity), `HttpOnly`, `Secure`, `SameSite=Lax` cookie holding `{sub, username, email, expiry}`, valid 12h. Login uses PKCE + nonce.
- The OIDC provider is discovered on first login, so argo-center starts even while the provider is down.
- `insecureNoAuth: true` (instead of `oidc`) turns login off for local development; actions are audited as user `local` and the header shows a "No login" badge. One of the two is required.
- Optional `allowedGroups` check at login. No roles beyond that.
- CSRF: `SameSite=Lax` + mutating requests must send `X-Requested-With: argo-center`.

### 4.2 argo-center → ArgoCD
- **One token per instance**, from config/env. Same mechanism works for both OAuth-backed and user/pass instances, because both support **local accounts with `apiKey`**:
  ```yaml
  # argocd-cm
  accounts.argo-center: apiKey
  # argocd-rbac-cm
  p, role:argo-center, applications, get,     */*, allow
  p, role:argo-center, applications, sync,    */*, allow
  p, role:argo-center, applications, action/*, */*, allow   # not used in v1
  g, argo-center, role:argo-center
  ```
  `argocd account generate-token --account argo-center` on each instance.
- Token validity is checked by every list/watch (re-listed every 5 min); a 401/403 marks that cluster as **unavailable** in the UI instead of failing the whole panel.
- Per-instance TLS: optional `caFile`, or `insecureSkipVerify` as last resort.

## 5. Configuration

`config.yaml` (ConfigMap) + secrets via env. See [config.example.yaml](../config.example.yaml) for every option.

## 6. ArgoCD API usage

| Purpose | Call |
|---|---|
| Live status of all apps | `GET /api/v1/stream/applications` (long-lived watch, NDJSON) |
| Initial / periodic full list | `GET /api/v1/applications` |
| Refresh | `GET /api/v1/applications/{name}?refresh=normal` (or `hard`); ArgoCD answers once the comparison is done |
| Diff | `GET /api/v1/applications/{name}/managed-resources` |
| Sync | `POST /api/v1/applications/{name}/sync` |
| Cancel running sync | `DELETE /api/v1/applications/{name}/operation` |

Full objects are fetched: ArgoCD v3's `?fields=` filter only honors the exact paths its own UI requests and silently drops the rest.

**Fields we keep per app per cluster:** `metadata.name`, `spec.project`, `spec.source.repoURL/path/targetRevision`, `status.sync.status`, `status.sync.revision`, `status.health.status`, `status.operationState.phase/message/startedAt/finishedAt`, `status.reconciledAt`, count of out-of-sync resources from `status.resources`.

The ArgoCD client is a **thin hand-written REST client** with our own structs for the fields above — not the `argo-cd` Go module (huge dependency tree, version-coupled).

## 7. Backend components

### 7.1 Cache
- Per instance: one goroutine doing list → watch, reconnect with backoff, full re-list every 5 min to heal drift.
- Holds `map[cluster]map[app]AppSummary`; publishes change events to an in-process bus.
- Per instance connection state: `ok | degraded (watch reconnecting) | unavailable (auth/network)`, plus `lastUpdate`.

### 7.2 Diff
- Fetch `managed-resources`, keep resources with `modified=true` (plus missing / extra-to-prune); skip hooks.
- Compare `normalizedLiveState` vs `predictedLiveState` (falls back to `targetState`) — same as ArgoCD UI's diff.
- Strip noise before diffing: `metadata.managedFields`, `resourceVersion`, `uid`, `generation`, `creationTimestamp`, `status`, `kubectl.kubernetes.io/last-applied-configuration`.
- Output per resource:
  - `changes`: structural JSON diff → `{path, op: changed|added|removed}`, e.g. `spec.template.spec.containers[app].image`, `data.LOG_LEVEL` (the "brief diff"). Lists whose items have unique `name`s are matched by name. Capped at 25 per resource (`moreChanges` counts the rest).
  - `unifiedDiff`: YAML unified diff, hunks only, capped at 256 KB (`truncated`).
  - `status`: `modified | missing (will be created) | extra (will be pruned if prune=on)`.
- Not cached. Uses ArgoCD's latest comparison (the controller re-compares every ~3 min); the UI shows its age and the compared revision, and offers Refresh / Hard refresh. The frontend refetches a diff only when the app's revision or `reconciledAt` changes. The sync dialog (milestone 3) refreshes first so nobody syncs on stale state.

### 7.3 Sync
Single-cluster sync request body:
```json
{
  "revision": "<pinned sha>",
  "prune": false,
  "dryRun": false,
  "resources": [{"group":"apps","kind":"Deployment","namespace":"x","name":"y"}],
  "syncOptions": {"items": ["ApplyOutOfSyncOnly=true"]}
}
```
- `revision` is **pinned to the revision whose diff the user just looked at** (`status.sync.revision`). If a new commit lands between "view diff" and "sync", the sync still applies what was reviewed.
- Not pinned (and the dialog says so) for apps with auto-sync on (ArgoCD refuses a revision other than the target there) and multi-source apps.
- `infos: [{name: "Requested by", value: "<user> via argo-center"}]` is attached, so ArgoCD's own UI shows who asked for the sync.
- `resources` is sent only when the user deselected some; otherwise the whole app is synced.
- ArgoCD refusing the request (400/409/412, e.g. operation already running) → argo-center answers 409 with ArgoCD's message; token/network problems → 502. While an operation runs, the card shows its progress and a Terminate button (with confirmation) instead of Sync.

### 7.4 Rollout engine ("Sync all")
A rollout = one app × ordered list of clusters × options (prune, apply out-of-sync only). Runs **server-side**, so it survives closing the tab and every teammate sees it.

```
for each cluster in order (skipping steps already done):
    refresh (normal)
    if Git moved (revision != pinned[cluster])   → step paused, rollout paused: Confirm (use new commit) or Abort
    if an operation is already running          → step failed
    if Synced: Healthy → step skipped; else wait for Healthy as below
    sync(pinned[cluster], options), audited as the acting user
    wait for that operation: Failed/Error       → step failed, rollout stops
    then wait until a comparison made after the sync reports Healthy:
        Degraded for more than 2 min / not Healthy within healthTimeout (10 min) → step failed, rollout stops
next
```
- `pinned[cluster]` is captured per cluster when the rollout is created, from the commits shown in the dialog (which refreshes every cluster first). Not pinned for auto-sync / multi-source apps.
- States: `running`, `paused` (Git moved), `failed`, `interrupted` (argo-center restarted mid-rollout), `aborted`, `succeeded`. The first four are *active*: one active rollout per app, so a failed one must be resumed or aborted before a new one.
- **Abort**: stops now; the sync running at that moment finishes unless "also terminate" is ticked. **Resume** (failed/interrupted): retries from the first unfinished step, re-checking what is already synced. **Confirm** (paused): continues with the new commit for the paused cluster.
- Single-cluster sync on a cluster of a *running* rollout is refused (409).
- Syncs carry `Requested by <user> via argo-center rollout #N`; the acting user is whoever started, resumed or confirmed last.
- Stored as JSON rows in SQLite (`rollouts` table, same DB as the audit log); every change emits a `rollout-updated` SSE event.

### 7.5 Audit
SQLite table, one row per action: `ts, user, action (sync|dry-run|terminate|refresh-hard|rollout-*), app, cluster, revision, options, result, message`. Also logged as structured JSON to stdout.

## 8. HTTP API (backend → SPA)

```
GET    /auth/login | /auth/callback | POST /auth/logout
GET    /api/me

GET    /api/clusters                       → [{key, displayName, url, state, error, lastUpdate}]
GET    /api/apps?q=&project=&outOfSync=    → [{name, project, cells: {cluster: AppSummary|null}}]
GET    /api/apps/{name}                    → {name, cells, activeRollout?}
POST   /api/apps/{name}/refresh?cluster=X&hard=true   → 204 once ArgoCD finished comparing (3 min timeout)
GET    /api/apps/{name}/diff?cluster=X     → {cluster, revision, reconciledAt, resources: [{group,kind,namespace,name,status,changes,moreChanges,unifiedDiff,truncated}]}
POST   /api/apps/{name}/sync               {cluster, revision, prune, dryRun, applyOutOfSyncOnly, resources?}
DELETE /api/apps/{name}/operation?cluster=X

POST   /api/rollouts                       {app, steps: [{cluster, revision}] (ordered), options: {prune, applyOutOfSyncOnly}}
GET    /api/rollouts?app=&active=
GET    /api/rollouts/{id}
POST   /api/rollouts/{id}/(abort|resume|confirm)   abort body: {terminate: bool}

GET    /api/audit?app=&cluster=&user=&action=&before=&limit=
GET    /api/audit/users
GET    /api/events                         SSE: app-changed, cluster-state, rollout-updated
```

Each cell (`AppSummary`) also carries `argocdURL` for a deep link to that app in the native UI.

## 9. Frontend

**Stack:** React + TypeScript + Vite, TanStack Query (server state), React Router, Mantine (dense tables, modals, notifications); unified diffs are rendered by a small in-house component. SSE events invalidate the matching queries.

### 9.1 Overview (`/`)
- Grid: rows = every app found on any instance, columns = clusters (config order). Column header shows cluster connection state.
- Cell: sync badge (Synced / OutOfSync / Unknown), health dot, short revision; `a1b2c3 → d4e5f6` when out of sync; spinner while an operation runs; "—" if app absent.
- Filters: name search, project, "out of sync anywhere", "unhealthy anywhere". Sorted: out-of-sync first.
- Row click → app detail.

### 9.2 App detail (`/apps/:name`)
- Header: app name, project, repo/path, actions **Refresh all**, **Sync all out-of-sync…**
- One card per cluster:
  - status, health, current → target revision (commit link), last sync time + result
  - brief diff: `N resources differ · compared with <rev> <age>` + list of `Kind/name — changed paths` (first 10, then "Show all")
  - expand → full unified diff per resource
  - **Sync** (opens sync dialog), **Dry run**, **Open in ArgoCD ↗**
  - live operation progress / error message
- Active rollout banner at top with per-cluster step status and Abort/Confirm/Resume.

### 9.3 Sync dialog (single cluster)
- Opening it refreshes the app on that cluster first, then shows the resources that differ (all selected; not-in-Git ones only selectable with Prune) and the pinned revision.
- **Dry run** runs in the dialog and shows its result there; **Sync** closes it and the card shows progress. The card itself only has **Sync…** (no separate Dry run button), so every action starts from a fresh comparison.
- Options: Prune, Apply out-of-sync only (default on), select resources.

### 9.4 Rollout dialog ("Sync all…")
- Refreshes every cluster on open, then lists them in config order with ↑/↓ to reorder and checkboxes (pre-checked = not Synced), each with `live → pinned` commit.
- Options: prune, apply out-of-sync only. Start → rollout banner on the app page.
- Banner: per-cluster step state and message; Resume / Continue with the new commit / Abort (with optional terminate). Finished rollouts stay visible for 6 hours or until dismissed.

### 9.5 Audit (`/audit`)
- Table of the whole audit log, newest first, 50 per page ("Load older entries", `before=<id>` cursor).
- Filters: app, cluster, user (`GET /api/audit/users`), action. Kept in the URL so a view can be shared; the app page's "Recent activity" links to `/audit?app=<name>`.

### 9.6 Deep links
- Overview filters live in the URL: `/?q=<search>&project=<p>&view=attention`.
- Each overview cell links to `/apps/<name>#<cluster>`, which scrolls to that cluster's card.
- Revisions (cluster cards, rollout steps, audit log) link to the commit (`<repo>/-/commit/<sha>` on GitLab, `<repo>/commit/<sha>` on GitHub), and cards with live ≠ target have a "compare ↗" link to `<repo>/-/compare/<live>...<target>`. Only for full commit SHAs, so Helm chart versions and branch names are left as plain text.
- Apps with an active rollout carry a "rollout <state>" badge on the overview.

## 10. Deployment
- Image: `Dockerfile`, multi-stage (node → go → distroless static), single binary, ~17 MB. Runs as any non-root UID, read-only root filesystem.
- Helm chart `deploy/helm/argo-center`. See `deploy/README.md`.
- K8s: Deployment (replicas: 1, strategy: Recreate), PVC (1 Gi, kept on uninstall) for SQLite, Service, optional Ingress or Contour HTTPProxy, ServiceAccount without API token.
- Proxy timeouts: `/api/events` (SSE) needs a long response timeout; everything else 5m (hard refresh can take minutes). A 30s timeout would cut live updates every 30s.
- Secret: `ARGOCD_TOKEN_<KEY>` per cluster, `OIDC_CLIENT_SECRET`, `SESSION_KEY`. ConfigMap: `config.yaml`, rendered from values.
- Health: `/healthz` (process), `/readyz` (config loaded; does *not* depend on all ArgoCD instances being up).
- Deployed in one cluster that can reach every ArgoCD API.

## 11. Repo layout

```
cmd/argo-center/        main.go
internal/config/        load + validate config.yaml / env
internal/argocd/        REST client + types
internal/cache/         list/watch per instance, in-memory state, event bus
internal/diff/          normalize, changed paths, unified diff
internal/rollout/       rollout engine
internal/audit/         SQLite store
internal/auth/          OIDC + session
internal/api/           HTTP handlers, SSE
web/                    React app (embedded into the binary)
deploy/                 Helm chart + deploy guide
docs/                   this file
```

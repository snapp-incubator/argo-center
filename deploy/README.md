# Deploying argo-center

argo-center runs as one pod in one cluster that can reach the API of every ArgoCD instance. It needs
an image, the Helm chart in `deploy/helm/argo-center`, an ArgoCD API token per instance, and (for
login) an OIDC client.

## 1. Image

Each `vX.Y.Z` tag builds and pushes `ghcr.io/snapp-incubator/argo-center:X.Y.Z` and `:latest`
(`.github/workflows/release.yml`). The tag must match the chart's `appVersion`; bump both together.

To build it yourself:

```sh
make image IMAGE=registry.example.com/argo-center TAG=dev
```

The Dockerfile builds the frontend (node), then a static Go binary with the frontend embedded, into
a distroless image (~17 MB, no shell). It runs as non-root with a read-only root filesystem.

## 2. ArgoCD tokens

On each instance, create the `argo-center` local account and a token (see the main README, "Tokens").

## 3. OIDC client

Create a confidential client on your OIDC provider:

- Redirect URL: `https://<host>/auth/callback`
- Post-logout redirect URL: `https://<host>/auth/signed-out`
- If you use `allowedGroups`, the ID token must include the groups claim.

## 4. Chart

Put your settings in a `values.yaml`:

```yaml
ingress:
  enabled: true
  kind: Ingress                 # or HTTPProxy for Contour
  className: nginx
  host: argo-center.example.com
  tlsSecret: argo-center-tls

config:
  clusters:                     # column order in the UI and default rollout order
    - key: staging
      url: https://argocd.staging.example.com
    - key: prod
      url: https://argocd.prod.example.com
  oidc:
    issuer: https://sso.example.com/realms/example
    clientID: argo-center
    scopes: [openid, profile, email, groups]
    allowedGroups: [platform-team]
```

Keep the secrets in a separate file that you do not commit, or encrypt it (for example with SOPS):

```yaml
argo_center:                    # or argo_center.existingSecret: <name>
  argocdTokens:
    staging: <token>
    prod: <token>
  oidcClientSecret: <client secret>
  sessionKey: <openssl rand -base64 32>
```

Then install from a clone of this repo:

```sh
helm upgrade --install argo-center deploy/helm/argo-center \
  --namespace argo-center --create-namespace \
  -f values.yaml -f secrets.yaml
```

The chart refuses to render if a required value is missing, so nothing half-configured gets deployed.

To try it without login, set `config.insecureNoAuth: true`, leave the ingress off and use
`kubectl -n argo-center port-forward svc/argo-center 8080:80`. Anyone who can reach the service can
then sync, so keep that to a short trial.

**OpenShift:** set `podSecurityContext.fsGroup: null`. The restricted SCC assigns the UID and GID
itself and rejects a fixed value.

**Proxy timeouts:** live updates use a long-lived stream on `/api/events`, with a heartbeat every 15
seconds, and a hard refresh can wait minutes for an answer. Your proxy must allow both. The
`HTTPProxy` template already sets these timeouts. For `Ingress`, set them with
`ingress.annotations`, for example on ingress-nginx:

```yaml
ingress:
  annotations:
    nginx.ingress.kubernetes.io/proxy-read-timeout: "3600"
    nginx.ingress.kubernetes.io/proxy-buffering: "off"
```

What the chart creates:

| Object | Notes |
|---|---|
| Deployment | 1 replica, `Recreate` (rollouts run in-process; SQLite on a RWO volume). Restarts on config/secret change. |
| PersistentVolumeClaim | 1Gi, `/data/argo-center.db`: audit log and rollouts. Kept if the chart is deleted. |
| ConfigMap / Secret | `config.yaml`; tokens as `ARGOCD_TOKEN_<KEY>`, `OIDC_CLIENT_SECRET`, `SESSION_KEY`. |
| Service | port 80 → 8080. |
| Ingress or HTTPProxy | Only with `ingress.enabled: true`. |
| ServiceAccount | no Kubernetes API token mounted; argo-center only talks to ArgoCD. |

## 5. Check

- `https://<host>/healthz` → `ok`
- The overview shows every cluster as connected, and the "Live" dot stays green. If it flips to
  "Reconnecting…" every 30 seconds, the proxy timeout on `/api/events` is too short.
- After login, your name appears in the header, and your first sync shows up under Recent activity.

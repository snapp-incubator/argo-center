import { useEffect, useState } from 'react';
import { keepPreviousData, useInfiniteQuery, useQuery, useQueryClient } from '@tanstack/react-query';

// Mirrors internal/cache types.

export type ClusterStateName = 'connecting' | 'ok' | 'error';

export interface ClusterState {
  key: string;
  displayName: string;
  url: string;
  state: ClusterStateName;
  error?: string;
  lastUpdate?: string;
  appCount: number;
}

export interface AppSummary {
  name: string;
  namespace: string;
  project: string;
  multiSource?: boolean;
  autoSync?: boolean;
  repoURL: string;
  path: string;
  targetRevision: string;
  syncStatus: 'Synced' | 'OutOfSync' | 'Unknown' | '';
  revision: string;
  deployedRevision: string;
  outOfSyncResources: number;
  healthStatus: string;
  healthMessage?: string;
  operationPhase?: string;
  operationMessage?: string;
  operationDryRun?: boolean;
  operationRevision?: string;
  operationStartedAt?: string;
  operationFinishedAt?: string;
  lastDeployedAt?: string;
  reconciledAt?: string;
  argoURL: string;
}

export interface AppRow {
  name: string;
  cells: Record<string, AppSummary>;
}

// Mirrors internal/diff types.

export type DiffStatus = 'modified' | 'missing' | 'extra';

export interface DiffChange {
  path: string;
  op: 'changed' | 'added' | 'removed';
}

export interface DiffResource {
  group: string;
  kind: string;
  namespace: string;
  name: string;
  status: DiffStatus;
  changes: DiffChange[];
  moreChanges?: number;
  /** Unified diff of live YAML (-) against YAML after sync (+), hunks only. */
  unifiedDiff: string;
  truncated?: boolean;
}

export interface DiffResponse {
  cluster: string;
  revision: string;
  reconciledAt?: string;
  resources: DiffResource[];
}

export class NotFoundError extends Error {}

export interface Me {
  sub: string;
  name: string;
  email?: string;
  authEnabled: boolean;
}

export interface AuditEntry {
  id: number;
  time: string;
  user: string;
  action:
    | 'sync'
    | 'dry-run'
    | 'terminate'
    | 'refresh-hard'
    | 'target-revision'
    | 'rollout-start'
    | 'rollout-abort'
    | 'rollout-resume'
    | 'rollout-confirm';
  app: string;
  cluster: string;
  revision?: string;
  options: string[];
  result: 'ok' | 'error';
  message?: string;
}

export interface SyncResource {
  group: string;
  kind: string;
  namespace: string;
  name: string;
}

export interface SyncOptions {
  /** The commit whose diff was reviewed; the sync applies exactly this one. */
  revision: string;
  prune: boolean;
  dryRun: boolean;
  applyOutOfSyncOnly: boolean;
  /** Empty syncs every resource. */
  resources?: SyncResource[];
}

// Mirrors internal/rollout types.

export type RolloutState = 'running' | 'paused' | 'failed' | 'interrupted' | 'aborted' | 'succeeded';
export type StepState =
  | 'pending'
  | 'refreshing'
  | 'syncing'
  | 'waiting'
  | 'succeeded'
  | 'skipped'
  | 'paused'
  | 'failed'
  | 'aborted'
  | 'interrupted';

export interface RolloutStep {
  cluster: string;
  /** Pinned commit; empty for apps that cannot be pinned. */
  revision: string;
  newRevision?: string;
  state: StepState;
  message?: string;
  startedAt?: string;
  finishedAt?: string;
}

export interface Rollout {
  id: number;
  app: string;
  options: { prune: boolean; applyOutOfSyncOnly: boolean };
  createdBy: string;
  actingUser: string;
  createdAt: string;
  updatedAt: string;
  finishedAt?: string;
  state: RolloutState;
  message?: string;
  steps: RolloutStep[];
}

export const isActiveRollout = (r: Rollout) => r.state !== 'succeeded' && r.state !== 'aborted';

export function login() {
  window.location.assign(`/auth/login?return=${encodeURIComponent(location.pathname + location.search)}`);
}

async function request<T>(method: string, path: string, payload?: unknown): Promise<T> {
  const headers: Record<string, string> = { Accept: 'application/json' };
  if (method !== 'GET') headers['X-Requested-With'] = 'argo-center';
  if (payload !== undefined) headers['Content-Type'] = 'application/json';
  const res = await fetch(path, { method, headers, body: payload === undefined ? undefined : JSON.stringify(payload) });
  if (res.status === 401) {
    login();
    throw new Error('Login required');
  }
  const body = res.status === 204 ? undefined : await res.json().catch(() => undefined);
  if (res.status === 404 && method === 'GET') throw new NotFoundError(body?.error ?? path);
  if (!res.ok) throw new Error(body?.error ?? `${res.status} ${res.statusText}`);
  return body as T;
}

const getJSON = <T>(path: string) => request<T>('GET', path);

export const useMe = () => useQuery({ queryKey: ['me'], queryFn: () => getJSON<Me>('/api/me') });

export const useAudit = (app: string) =>
  useQuery({
    queryKey: ['audit', app],
    queryFn: () => getJSON<AuditEntry[]>(`/api/audit?app=${encodeURIComponent(app)}&limit=20`),
  });

export interface AuditFilter {
  app?: string;
  cluster?: string;
  user?: string;
  action?: string;
}

const auditPage = 50;

/** The whole audit log, newest first, loaded in pages of 50 ("load more" fetches the next page). */
export const useAuditLog = (filter: AuditFilter) =>
  useInfiniteQuery({
    queryKey: ['audit-log', filter],
    queryFn: ({ pageParam }) => {
      const q = new URLSearchParams({ limit: String(auditPage) });
      for (const [k, v] of Object.entries(filter)) if (v) q.set(k, v);
      if (pageParam) q.set('before', String(pageParam));
      return getJSON<AuditEntry[]>(`/api/audit?${q}`);
    },
    initialPageParam: 0,
    getNextPageParam: (last) => (last.length === auditPage ? last[last.length - 1].id : undefined),
  });

export const useAuditUsers = () =>
  useQuery({ queryKey: ['audit-users'], queryFn: () => getJSON<string[]>('/api/audit/users') });

/** Active rollouts of all apps, keyed by app, for the overview. */
export const useActiveRollouts = () =>
  useQuery({
    queryKey: ['rollouts', '*active'],
    queryFn: async () => {
      const list = await getJSON<Rollout[]>('/api/rollouts?active=true&limit=100');
      return new Map(list.map((r) => [r.app, r]));
    },
  });

const appPath = (name: string, cluster: string, action: string) =>
  `/api/apps/${encodeURIComponent(name)}/${action}?cluster=${encodeURIComponent(cluster)}`;

export const syncApp = (name: string, cluster: string, opts: SyncOptions) =>
  request<{ revision: string }>('POST', appPath(name, cluster, 'sync'), opts);

export const terminateOperation = (name: string, cluster: string) =>
  request<void>('DELETE', appPath(name, cluster, 'operation'));

/** Changes the revision the app tracks on one cluster (a branch, tag or commit). Does not sync. */
export const setTargetRevision = (name: string, cluster: string, targetRevision: string) =>
  request<void>('PUT', appPath(name, cluster, 'target-revision'), { targetRevision });

/** The app's latest rollout, active or not. */
export const useLatestRollout = (app: string) =>
  useQuery({
    queryKey: ['rollouts', app],
    queryFn: async () => (await getJSON<Rollout[]>(`/api/rollouts?app=${encodeURIComponent(app)}&limit=1`))[0] ?? null,
  });

export const createRollout = (req: {
  app: string;
  steps: { cluster: string; revision: string }[];
  options: { prune: boolean; applyOutOfSyncOnly: boolean };
}) => request<Rollout>('POST', '/api/rollouts', req);

export const rolloutAction = (id: number, action: 'abort' | 'resume' | 'confirm', body?: { terminate: boolean }) =>
  request<Rollout>('POST', `/api/rollouts/${id}/${action}`, body ?? {});

export const useClusters = () =>
  useQuery({ queryKey: ['clusters'], queryFn: () => getJSON<ClusterState[]>('/api/clusters') });

export const useApps = () => useQuery({ queryKey: ['apps'], queryFn: () => getJSON<AppRow[]>('/api/apps') });

export const useApp = (name: string) =>
  useQuery({
    queryKey: ['apps', name],
    queryFn: () => getJSON<AppRow>(`/api/apps/${encodeURIComponent(name)}`),
    retry: (count, err) => !(err instanceof NotFoundError) && count < 3,
  });

/**
 * Fetches the diff of one app on one cluster. The key includes the revision and reconcile time, so
 * the diff is fetched again whenever ArgoCD finishes a new comparison, and only then: the generic
 * live-update invalidation skips diff queries, since a diff can be megabytes.
 */
export const useDiff = (name: string, summary: AppSummary | undefined, cluster: string, enabled: boolean) =>
  useQuery({
    queryKey: ['diff', name, cluster, summary?.revision, summary?.reconciledAt],
    queryFn: () =>
      getJSON<DiffResponse>(`/api/apps/${encodeURIComponent(name)}/diff?cluster=${encodeURIComponent(cluster)}`),
    enabled: enabled && summary !== undefined,
    placeholderData: keepPreviousData,
    gcTime: 60_000,
  });

export interface RefreshState {
  pending: boolean;
  hard?: boolean;
  error?: string;
}

/**
 * Refreshes an app on given clusters, tracking progress per cluster. ArgoCD answers once the
 * comparison has finished, after which the app and its diffs are fetched again.
 */
export function useRefresh(name: string) {
  const queryClient = useQueryClient();
  const [state, setState] = useState<Record<string, RefreshState>>({});

  const refresh = (clusters: string[], hard = false) => {
    for (const cluster of clusters) {
      setState((s) => ({ ...s, [cluster]: { pending: true, hard } }));
      const q = new URLSearchParams({ cluster, ...(hard && { hard: 'true' }) });
      request<void>('POST', `/api/apps/${encodeURIComponent(name)}/refresh?${q}`)
        // Wait for the app (and its new reconcile time) before reporting done, so whoever shows the
        // diff next fetches the new comparison rather than the previous one.
        .then(() => queryClient.invalidateQueries({ queryKey: ['apps', name] }))
        .then(
          () => setState((s) => ({ ...s, [cluster]: { pending: false } })),
          (err: Error) => setState((s) => ({ ...s, [cluster]: { pending: false, error: err.message } })),
        );
    }
  };

  return { state, refresh };
}

/**
 * Subscribes to the server's event stream and refetches active queries when anything changes.
 * Refetches are throttled to one per second, since ArgoCD can emit bursts of events.
 * Returns whether the stream is currently connected.
 */
export function useLiveUpdates(): boolean {
  const queryClient = useQueryClient();
  const [connected, setConnected] = useState(false);

  useEffect(() => {
    const source = new EventSource('/api/events');
    let timer: number | undefined;
    const schedule = () => {
      if (timer !== undefined) return;
      timer = window.setTimeout(() => {
        timer = undefined;
        queryClient.invalidateQueries({ predicate: (q) => q.queryKey[0] !== 'diff' });
      }, 1000);
    };

    // Refetch on (re)connect too, so events missed while disconnected are not lost.
    source.onopen = () => {
      setConnected(true);
      schedule();
    };
    source.onerror = () => setConnected(false);
    for (const type of ['app-changed', 'app-deleted', 'cluster-changed', 'rollout-updated']) {
      source.addEventListener(type, schedule);
    }
    return () => {
      source.close();
      window.clearTimeout(timer);
    };
  }, [queryClient]);

  return connected;
}

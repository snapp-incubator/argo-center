import { useEffect, useState } from 'react';
import { Alert, Badge, Button, Checkbox, Code, Group, Loader, Modal, ScrollArea, Stack, Text } from '@mantine/core';
import { useQueryClient } from '@tanstack/react-query';
import {
  syncApp,
  useDiff,
  type AppSummary,
  type ClusterState,
  type DiffResource,
  type RefreshState,
  type SyncOptions,
} from '../api';
import { shortRev, timeAgo } from '../format';
import { isSyncing } from './Status';

const resourceKey = (r: DiffResource) => `${r.group}/${r.kind}/${r.namespace}/${r.name}`;

interface Props {
  app: string;
  cluster: ClusterState;
  summary: AppSummary;
  opened: boolean;
  onClose: () => void;
  refreshState?: RefreshState;
  onRefresh: (hard: boolean) => void;
}

/**
 * Syncs one app on one cluster. It refreshes first so the diff is current, then pins the sync to
 * the revision of that diff: if a newer commit lands meanwhile, the sync still applies what was shown.
 */
export function SyncDialog({ app, cluster, summary, opened, onClose, refreshState, onRefresh }: Props) {
  const queryClient = useQueryClient();
  const [prune, setPrune] = useState(false);
  const [outOfSyncOnly, setOutOfSyncOnly] = useState(true);
  const [deselected, setDeselected] = useState<ReadonlySet<string>>(new Set());
  const [submitting, setSubmitting] = useState<'sync' | 'dry-run' | null>(null);
  const [error, setError] = useState<string>();
  // The operation start time seen when a dry run was requested; a different one means the dry run's own result.
  const [dryRunAfter, setDryRunAfter] = useState<string | null>(null);

  useEffect(() => {
    if (!opened) return;
    setPrune(false);
    setOutOfSyncOnly(true);
    setDeselected(new Set());
    setError(undefined);
    setDryRunAfter(null);
    onRefresh(false);
    // Only on open: onRefresh is a new function on every render.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [opened]);

  const refreshing = refreshState?.pending ?? false;
  const diff = useDiff(app, summary, cluster.key, opened && !refreshing);
  const busy = isSyncing(summary);
  const pinned = !summary.autoSync && !summary.multiSource;

  const resources = diff.data?.resources ?? [];
  const candidates = resources.filter((r) => r.status !== 'extra' || prune);
  const chosen = candidates.filter((r) => !deselected.has(resourceKey(r)));
  const partial = chosen.length < candidates.length;
  const extras = resources.filter((r) => r.status === 'extra');
  const ready = !refreshing && diff.isSuccess && !diff.isPlaceholderData;

  const submit = async (dryRun: boolean) => {
    if (!diff.data) return;
    const opts: SyncOptions = {
      revision: diff.data.revision,
      prune,
      dryRun,
      applyOutOfSyncOnly: outOfSyncOnly,
      resources: partial
        ? chosen.map(({ group, kind, namespace, name }) => ({ group, kind, namespace, name }))
        : undefined,
    };
    const previousOp = summary.operationStartedAt ?? '';
    setSubmitting(dryRun ? 'dry-run' : 'sync');
    setError(undefined);
    try {
      await syncApp(app, cluster.key, opts);
      queryClient.invalidateQueries({ queryKey: ['apps', app] });
      queryClient.invalidateQueries({ queryKey: ['audit', app] });
      if (dryRun) setDryRunAfter(previousOp);
      else onClose();
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setSubmitting(null);
    }
  };

  const toggle = (key: string) =>
    setDeselected((s) => {
      const next = new Set(s);
      if (!next.delete(key)) next.add(key);
      return next;
    });

  return (
    <Modal
      opened={opened}
      onClose={onClose}
      size="xl"
      title={
        <Text fw={700}>
          Sync {app} on {cluster.displayName}
        </Text>
      }
    >
      <Stack gap="md">
        {refreshState?.error && (
          <Alert color="red" variant="light" title="Refresh failed">
            {refreshState.error}. The diff below may be out of date.
          </Alert>
        )}

        {!ready && !diff.isError && !refreshState?.error ? (
          <Group gap="xs">
            <Loader size="xs" />
            <Text size="sm" c="dimmed">
              {refreshing ? 'Comparing with Git…' : 'Loading diff…'}
            </Text>
          </Group>
        ) : diff.isError ? (
          <Alert color="red" variant="light" title="Could not load diff">
            {diff.error.message}
          </Alert>
        ) : (
          diff.data && (
            <>
              <Text size="sm">
                {pinned ? (
                  <>
                    Applies <Code>{shortRev(diff.data.revision)}</Code>
                  </>
                ) : (
                  <>
                    Applies the app's target revision <Code>{summary.targetRevision || 'HEAD'}</Code>
                  </>
                )}{' '}
                <Text span c="dimmed" size="sm">
                  · compared {timeAgo(diff.data.reconciledAt)}
                  {summary.targetRevision && pinned && ` · tracking ${summary.targetRevision}`}
                </Text>
              </Text>
              {!pinned && (
                <Alert color="yellow" variant="light">
                  {summary.autoSync
                    ? 'Auto-sync is on for this app, so ArgoCD does not allow pinning a commit. '
                    : 'This app has several sources, so the commit cannot be pinned. '}
                  If a new commit lands before the sync runs, it is applied too.
                </Alert>
              )}

              {resources.length === 0 ? (
                <Text size="sm" c="dimmed">
                  No resource differs. A sync re-applies the manifests without changes.
                </Text>
              ) : (
                <div>
                  <Group justify="space-between" mb={6}>
                    <Text size="sm" fw={600}>
                      {chosen.length} of {resources.length} resources selected
                    </Text>
                  </Group>
                  <ScrollArea.Autosize mah={300} type="auto" scrollbars="y">
                    <Stack gap={4}>
                      {resources.map((r) => {
                        const key = resourceKey(r);
                        const isExtra = r.status === 'extra';
                        const disabled = isExtra && !prune;
                        return (
                          <Checkbox
                            key={key}
                            size="sm"
                            disabled={disabled}
                            checked={!disabled && !deselected.has(key)}
                            onChange={() => toggle(key)}
                            styles={{
                              root: { width: '100%' },
                              body: { minWidth: 0 },
                              labelWrapper: { minWidth: 0, flex: 1 },
                            }}
                            description={
                              r.changes.length > 0 && (
                                <Text
                                  size="xs"
                                  c="dimmed"
                                  ff="monospace"
                                  truncate
                                  title={r.changes.map((c) => c.path).join('\n')}
                                >
                                  {r.changes.map((c) => c.path).join(', ')}
                                </Text>
                              )
                            }
                            label={
                              <Group gap={6}>
                                <Text size="sm" span>
                                  {r.kind}/{r.name}
                                </Text>
                                {r.namespace && (
                                  <Text size="xs" c="dimmed" span>
                                    {r.namespace}
                                  </Text>
                                )}
                                {r.status === 'missing' && (
                                  <Badge size="xs" variant="light" color="green">
                                    new
                                  </Badge>
                                )}
                                {isExtra && (
                                  <Badge size="xs" variant="light" color="red">
                                    {prune ? 'will be deleted' : 'not in Git · kept unless Prune'}
                                  </Badge>
                                )}
                              </Group>
                            }
                          />
                        );
                      })}
                    </Stack>
                  </ScrollArea.Autosize>
                </div>
              )}

              <Stack gap={6}>
                <Checkbox
                  checked={prune}
                  onChange={(e) => setPrune(e.currentTarget.checked)}
                  label="Prune: delete resources that are no longer in Git"
                  description={
                    extras.length > 0
                      ? `${extras.length} resource${extras.length === 1 ? '' : 's'} not in Git ${prune ? 'will be deleted' : 'will be kept'}.`
                      : undefined
                  }
                />
                <Checkbox
                  checked={outOfSyncOnly}
                  onChange={(e) => setOutOfSyncOnly(e.currentTarget.checked)}
                  label="Apply out-of-sync resources only"
                  description="Faster, and leaves resources that already match Git untouched."
                />
              </Stack>
            </>
          )
        )}

        {dryRunAfter !== null && <DryRunResult summary={summary} previousOp={dryRunAfter} />}

        {busy && dryRunAfter === null && (
          <Alert color="blue" variant="light">
            An operation is running on this cluster. Wait for it to finish or terminate it first.
          </Alert>
        )}
        {error && (
          <Alert color="red" variant="light" title="ArgoCD refused the request">
            {error}
          </Alert>
        )}

        <Group justify="flex-end" gap="sm">
          <Button variant="default" onClick={onClose}>
            Cancel
          </Button>
          <Button
            variant="default"
            loading={submitting === 'dry-run'}
            disabled={!ready || busy || submitting !== null || (resources.length > 0 && chosen.length === 0)}
            onClick={() => submit(true)}
          >
            Dry run
          </Button>
          <Button
            loading={submitting === 'sync'}
            disabled={!ready || busy || submitting !== null || (resources.length > 0 && chosen.length === 0)}
            onClick={() => submit(false)}
          >
            Sync {cluster.displayName}
          </Button>
        </Group>
      </Stack>
    </Modal>
  );
}

function DryRunResult({ summary, previousOp }: { summary: AppSummary; previousOp: string }) {
  if (isSyncing(summary) || !summary.operationDryRun || (summary.operationStartedAt ?? '') === previousOp) {
    return (
      <Group gap="xs">
        <Loader size="xs" />
        <Text size="sm" c="dimmed">
          Dry run running…
        </Text>
      </Group>
    );
  }
  const ok = summary.operationPhase === 'Succeeded';
  return (
    <Alert
      color={ok ? 'green' : 'red'}
      variant="light"
      title={ok ? 'Dry run passed' : `Dry run ${summary.operationPhase?.toLowerCase()}`}
    >
      <Text size="sm" style={{ whiteSpace: 'pre-wrap' }}>
        {summary.operationMessage || (ok ? 'The cluster accepted every manifest.' : '')}
      </Text>
    </Alert>
  );
}

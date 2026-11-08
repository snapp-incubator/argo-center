import { useEffect, useState } from 'react';
import { ActionIcon, Alert, Button, Checkbox, Code, Group, Loader, Modal, Stack, Text } from '@mantine/core';
import { useQueryClient } from '@tanstack/react-query';
import { createRollout, type AppSummary, type ClusterState, type RefreshState } from '../api';
import { shortRev } from '../format';
import { HealthBadge, SyncBadge } from './Status';

interface Props {
  app: string;
  /** Clusters in config order; only those with the app are offered. */
  clusters: ClusterState[];
  cells: Record<string, AppSummary>;
  opened: boolean;
  onClose: () => void;
  refreshing: Record<string, RefreshState>;
  onRefresh: (clusters: string[]) => void;
}

/**
 * Plans a rollout: which clusters, in which order, and with which options. Each cluster is pinned to
 * the commit shown here, after a refresh, so the rollout applies what was reviewed.
 */
export function RolloutDialog({ app, clusters, cells, opened, onClose, refreshing, onRefresh }: Props) {
  const queryClient = useQueryClient();
  const present = clusters.filter((c) => cells[c.key]);
  const [order, setOrder] = useState<string[]>([]);
  const [included, setIncluded] = useState<ReadonlySet<string>>(new Set());
  const [prune, setPrune] = useState(false);
  const [outOfSyncOnly, setOutOfSyncOnly] = useState(true);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string>();

  useEffect(() => {
    if (!opened) return;
    setOrder(present.map((c) => c.key));
    setIncluded(new Set(present.filter((c) => cells[c.key].syncStatus !== 'Synced').map((c) => c.key)));
    setPrune(false);
    setOutOfSyncOnly(true);
    setError(undefined);
    onRefresh(present.map((c) => c.key));
    // Only on open.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [opened]);

  const anyRefreshing = order.some((k) => refreshing[k]?.pending);
  const chosen = order.filter((k) => included.has(k));
  const byKey = Object.fromEntries(clusters.map((c) => [c.key, c]));

  const move = (i: number, delta: number) =>
    setOrder((o) => {
      const next = [...o];
      [next[i], next[i + delta]] = [next[i + delta], next[i]];
      return next;
    });
  const toggle = (key: string) =>
    setIncluded((s) => {
      const next = new Set(s);
      if (!next.delete(key)) next.add(key);
      return next;
    });

  const start = async () => {
    setSubmitting(true);
    setError(undefined);
    try {
      await createRollout({
        app,
        steps: chosen.map((k) => ({ cluster: k, revision: cells[k].revision })),
        options: { prune, applyOutOfSyncOnly: outOfSyncOnly },
      });
      queryClient.invalidateQueries({ queryKey: ['rollouts', app] });
      queryClient.invalidateQueries({ queryKey: ['audit', app] });
      onClose();
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Modal opened={opened} onClose={onClose} size="xl" title={<Text fw={700}>Sync {app} on several clusters</Text>}>
      <Stack gap="md">
        <Text size="sm" c="dimmed">
          Clusters are synced one at a time, in this order. Each must report Healthy before the next one starts; a
          failure stops the rollout. Each cluster is pinned to the commit shown, so a commit landing meanwhile pauses
          the rollout instead of being applied unreviewed.
        </Text>

        <Stack gap={4}>
          {order.map((key, i) => {
            const c = byKey[key];
            const s = cells[key];
            const state = refreshing[key];
            const pinned = !s.autoSync && !s.multiSource;
            return (
              <Group
                key={key}
                gap="sm"
                wrap="nowrap"
                p={6}
                style={{
                  border: '1px solid var(--mantine-color-default-border)',
                  borderRadius: 'var(--mantine-radius-sm)',
                  opacity: included.has(key) ? 1 : 0.6,
                }}
              >
                <Checkbox
                  checked={included.has(key)}
                  onChange={() => toggle(key)}
                  aria-label={`Include ${c.displayName}`}
                />
                <Text size="sm" c="dimmed" w={18} ta="right">
                  {included.has(key) ? chosen.indexOf(key) + 1 : '–'}
                </Text>
                <Text size="sm" fw={600} w={150} truncate>
                  {c.displayName}
                </Text>
                <SyncBadge summary={s} />
                <HealthBadge summary={s} />
                <Text size="xs" c="dimmed" style={{ flex: 1 }}>
                  {state?.pending ? (
                    <Group gap={6} component="span">
                      <Loader size={10} /> comparing with Git…
                    </Group>
                  ) : state?.error ? (
                    <Text span c="red" size="xs">
                      refresh failed: {state.error}
                    </Text>
                  ) : pinned ? (
                    <>
                      <Code>{shortRev(s.deployedRevision)}</Code> → <Code>{shortRev(s.revision)}</Code>
                      {s.outOfSyncResources > 0 && ` · ${s.outOfSyncResources} out of sync`}
                    </>
                  ) : (
                    `not pinned (${s.autoSync ? 'auto-sync' : 'multi-source'})`
                  )}
                </Text>
                <Group gap={2} wrap="nowrap">
                  <ActionIcon
                    variant="subtle"
                    color="gray"
                    disabled={i === 0}
                    onClick={() => move(i, -1)}
                    aria-label={`Move ${c.displayName} up`}
                  >
                    ↑
                  </ActionIcon>
                  <ActionIcon
                    variant="subtle"
                    color="gray"
                    disabled={i === order.length - 1}
                    onClick={() => move(i, 1)}
                    aria-label={`Move ${c.displayName} down`}
                  >
                    ↓
                  </ActionIcon>
                </Group>
              </Group>
            );
          })}
        </Stack>

        <Stack gap={6}>
          <Checkbox
            checked={prune}
            onChange={(e) => setPrune(e.currentTarget.checked)}
            label="Prune: delete resources that are no longer in Git"
          />
          <Checkbox
            checked={outOfSyncOnly}
            onChange={(e) => setOutOfSyncOnly(e.currentTarget.checked)}
            label="Apply out-of-sync resources only"
          />
        </Stack>

        {error && (
          <Alert color="red" variant="light" title="Could not start the rollout">
            {error}
          </Alert>
        )}

        <Group justify="flex-end" gap="sm">
          <Button variant="default" onClick={onClose}>
            Cancel
          </Button>
          <Button loading={submitting} disabled={anyRefreshing || chosen.length === 0} onClick={start}>
            {chosen.length === 1 ? 'Start rollout on 1 cluster' : `Start rollout on ${chosen.length} clusters`}
          </Button>
        </Group>
      </Stack>
    </Modal>
  );
}

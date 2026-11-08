import { useMemo } from 'react';
import { Alert, Button, Group, Loader, Paper, Select, Stack, Text, Title } from '@mantine/core';
import { useSearchParams } from 'react-router';
import { useApps, useAuditLog, useAuditUsers, useClusters, type AuditFilter } from '../api';
import { actionLabel, AuditTable } from '../components/AuditTable';
import { usePageTitle } from '../usePageTitle';

const filterKeys = ['app', 'cluster', 'user', 'action'] as const;

/** The audit log of every action taken through argo-center. Filters live in the URL, so a view can be shared. */
export function Audit() {
  usePageTitle('Audit log');
  const [params, setParams] = useSearchParams();
  const filter: AuditFilter = Object.fromEntries(filterKeys.map((k) => [k, params.get(k) ?? undefined]));

  const clusters = useClusters();
  const apps = useApps();
  const users = useAuditUsers();
  const log = useAuditLog(filter);

  const repoByApp = useMemo(
    () => new Map((apps.data ?? []).map((r) => [r.name, Object.values(r.cells)[0]?.repoURL])),
    [apps.data],
  );
  const entries = log.data?.pages.flat() ?? [];

  const set = (key: (typeof filterKeys)[number], value: string | null) =>
    setParams(
      (p) => {
        const next = new URLSearchParams(p);
        if (value) next.set(key, value);
        else next.delete(key);
        return next;
      },
      { replace: true },
    );
  const anyFilter = filterKeys.some((k) => params.get(k));

  return (
    <Stack gap="lg">
      <Group justify="space-between" align="flex-end">
        <div>
          <Title order={2}>Audit log</Title>
          <Text c="dimmed" size="sm">
            Every sync, dry run, terminate, hard refresh and rollout action taken through argo-center.
          </Text>
        </div>
        <Group gap="sm">
          <Select
            placeholder="All apps"
            aria-label="Filter by app"
            data={(apps.data ?? []).map((r) => r.name)}
            value={filter.app ?? null}
            onChange={(v) => set('app', v)}
            searchable
            clearable
            w={220}
          />
          <Select
            placeholder="All clusters"
            aria-label="Filter by cluster"
            data={(clusters.data ?? []).map((c) => ({ value: c.key, label: c.displayName }))}
            value={filter.cluster ?? null}
            onChange={(v) => set('cluster', v)}
            clearable
            w={170}
          />
          <Select
            placeholder="All users"
            aria-label="Filter by user"
            data={users.data ?? []}
            value={filter.user ?? null}
            onChange={(v) => set('user', v)}
            searchable
            clearable
            w={160}
          />
          <Select
            placeholder="All actions"
            aria-label="Filter by action"
            data={Object.entries(actionLabel).map(([value, label]) => ({ value, label }))}
            value={filter.action ?? null}
            onChange={(v) => set('action', v)}
            clearable
            w={170}
          />
        </Group>
      </Group>

      <Paper withBorder p="md">
        {log.isPending || clusters.isPending ? (
          <Loader />
        ) : log.isError ? (
          <Alert color="red" variant="light" title="Could not load the audit log">
            {log.error.message}
          </Alert>
        ) : entries.length === 0 ? (
          <Text c="dimmed" ta="center" py="lg">
            {anyFilter ? 'No entries match these filters.' : 'Nothing has been done through argo-center yet.'}
          </Text>
        ) : (
          <Stack gap="sm">
            <AuditTable entries={entries} clusters={clusters.data ?? []} showApp repoOf={(a) => repoByApp.get(a)} />
            {log.hasNextPage && (
              <Group justify="center">
                <Button
                  variant="default"
                  size="xs"
                  loading={log.isFetchingNextPage}
                  onClick={() => log.fetchNextPage()}
                >
                  Load older entries
                </Button>
              </Group>
            )}
          </Stack>
        )}
      </Paper>
    </Stack>
  );
}

import { useMemo } from 'react';
import {
  Alert,
  Anchor,
  Group,
  Loader,
  Paper,
  SegmentedControl,
  Select,
  SimpleGrid,
  Stack,
  Table,
  Text,
  TextInput,
  Title,
  Tooltip,
  UnstyledButton,
} from '@mantine/core';
import { Link, useSearchParams } from 'react-router';
import { useActiveRollouts, useApps, useClusters, type AppRow, type AppSummary, type Rollout } from '../api';
import { SearchIcon } from '../components/icons';
import { ClusterStatus, HealthBadge, isSyncing, Pill, SyncBadge, type Tone } from '../components/Status';
import { shortRev } from '../format';
import { usePageTitle } from '../usePageTitle';
import classes from './Overview.module.css';

const isOutOfSync = (s: AppSummary) => s.syncStatus === 'OutOfSync';
const isUnhealthy = (s: AppSummary) => !!s.healthStatus && s.healthStatus !== 'Healthy';

function attention(row: AppRow) {
  const cells = Object.values(row.cells);
  return {
    outOfSync: cells.some(isOutOfSync),
    syncing: cells.some(isSyncing),
    unhealthy: cells.some(isUnhealthy),
  };
}

export function Overview() {
  usePageTitle('Applications');
  const clusters = useClusters();
  const apps = useApps();
  const rollouts = useActiveRollouts();
  // Filters live in the URL (?q=&project=&view=attention), so a filtered view can be shared.
  const [params, setParams] = useSearchParams();
  const query = params.get('q') ?? '';
  const filter = params.get('view') === 'attention' ? 'attention' : 'all';
  const project = params.get('project');
  const setParam = (key: string, value: string | null) =>
    setParams(
      (p) => {
        const next = new URLSearchParams(p);
        if (value) next.set(key, value);
        else next.delete(key);
        return next;
      },
      { replace: true },
    );

  const rows = useMemo(() => {
    const q = query.trim().toLowerCase();
    return (apps.data ?? [])
      .map((row) => ({ row, ...attention(row), projects: new Set(Object.values(row.cells).map((c) => c.project)) }))
      .filter((r) => r.row.name.toLowerCase().includes(q))
      .filter((r) => !project || r.projects.has(project))
      .filter((r) => filter === 'all' || r.outOfSync || r.syncing || r.unhealthy)
      .sort(
        (a, b) =>
          Number(b.outOfSync) - Number(a.outOfSync) ||
          Number(b.syncing) - Number(a.syncing) ||
          Number(b.unhealthy) - Number(a.unhealthy) ||
          a.row.name.localeCompare(b.row.name),
      );
  }, [apps.data, query, filter, project]);

  const totals = useMemo(() => {
    const all = (apps.data ?? []).map(attention);
    return {
      outOfSync: all.filter((a) => a.outOfSync).length,
      syncing: all.filter((a) => a.syncing).length,
      unhealthy: all.filter((a) => a.unhealthy).length,
    };
  }, [apps.data]);

  const projects = useMemo(
    () =>
      [...new Set((apps.data ?? []).flatMap((r) => Object.values(r.cells).map((c) => c.project)))]
        .filter(Boolean)
        .sort(),
    [apps.data],
  );

  if (clusters.isPending || apps.isPending) return <Loader m="xl" />;
  if (clusters.error || apps.error) {
    return (
      <Alert color="red" title="Could not load applications">
        {String(clusters.error ?? apps.error)}
      </Alert>
    );
  }

  const connected = clusters.data.filter((c) => c.state === 'ok').length;

  return (
    <Stack gap="lg">
      <Group justify="space-between" align="flex-end">
        <div>
          <Title order={2}>Applications</Title>
          <Text c="dimmed">
            {apps.data.length} apps across {clusters.data.length} ArgoCD instances, live via watch streams.
          </Text>
        </div>
        <Group gap="sm">
          <TextInput
            type="search"
            placeholder="Search apps"
            aria-label="Search applications"
            leftSection={<SearchIcon />}
            value={query}
            onChange={(e) => setParam('q', e.currentTarget.value)}
            size="md"
            w={240}
          />
          <Select
            placeholder="All projects"
            aria-label="Filter by project"
            data={projects}
            value={project}
            onChange={(v) => setParam('project', v)}
            clearable
            searchable
            size="md"
            w={180}
          />
          <SegmentedControl
            className={classes.filter}
            size="md"
            color="brand"
            value={filter}
            onChange={(v) => setParam('view', v === 'attention' ? v : null)}
            data={[
              { label: 'All', value: 'all' },
              { label: 'Needs attention', value: 'attention' },
            ]}
          />
        </Group>
      </Group>

      <SimpleGrid cols={{ base: 2, sm: 4 }} spacing="sm">
        <Stat label="Out of sync somewhere" value={plural(totals.outOfSync, 'app')} tone="orange" />
        <Stat label="Syncing now" value={plural(totals.syncing, 'app')} tone="blue" />
        <Stat label="Unhealthy somewhere" value={plural(totals.unhealthy, 'app')} tone="red" />
        <Stat label="Instances connected" value={`${connected} of ${clusters.data.length}`} />
      </SimpleGrid>

      <Paper withBorder className={classes.tableCard}>
        <Table.ScrollContainer minWidth={230 + clusters.data.length * 170} type="native">
          <Table className={classes.table} highlightOnHover>
            <Table.Thead>
              <Table.Tr>
                <Table.Th w={230}>
                  <span className={classes.colLabel}>Application</span>
                </Table.Th>
                {clusters.data.map((c) => (
                  <Table.Th key={c.key}>
                    <Stack gap={3}>
                      <span className={classes.clusterName}>{c.displayName}</span>
                      <ClusterStatus cluster={c} />
                    </Stack>
                  </Table.Th>
                ))}
              </Table.Tr>
            </Table.Thead>
            <Table.Tbody>
              {rows.map(({ row }) => (
                <Table.Tr key={row.name}>
                  <Table.Td>
                    <Group gap={6} wrap="nowrap">
                      <Anchor component={Link} to={`/apps/${encodeURIComponent(row.name)}`} className={classes.appName}>
                        {row.name}
                      </Anchor>
                      <RolloutMark rollout={rollouts.data?.get(row.name)} />
                    </Group>
                    <Text size="xs" c="dimmed">
                      project: {[...new Set(Object.values(row.cells).map((c) => c.project))].join(', ')}
                    </Text>
                  </Table.Td>
                  {clusters.data.map((c) => (
                    <Table.Td key={c.key}>
                      <Cell app={row.name} cluster={c.key} summary={row.cells[c.key]} />
                    </Table.Td>
                  ))}
                </Table.Tr>
              ))}
              {rows.length === 0 && (
                <Table.Tr>
                  <Table.Td colSpan={clusters.data.length + 1}>
                    <Text c="dimmed" ta="center" py="lg">
                      No applications match.
                    </Text>
                  </Table.Td>
                </Table.Tr>
              )}
            </Table.Tbody>
          </Table>
        </Table.ScrollContainer>
      </Paper>
      <Text size="xs" c="dimmed">
        Sorted with apps that need attention first. Click a cell to see that cluster's diff and sync it.
      </Text>
    </Stack>
  );
}

const plural = (n: number, word: string) => `${n} ${word}${n === 1 ? '' : 's'}`;

function Stat({ label, value, tone }: { label: string; value: string; tone?: Tone }) {
  return (
    <Paper withBorder className={classes.stat}>
      <span className={classes.statLabel}>{label}</span>
      <span className={classes.statValue} data-tone={tone}>
        {value}
      </span>
    </Paper>
  );
}

const rolloutTone: Record<string, Tone> = {
  running: 'blue',
  paused: 'yellow',
  failed: 'red',
  interrupted: 'orange',
};

/** Marks apps with an active rollout; the app page has its progress and controls. */
function RolloutMark({ rollout }: { rollout?: Rollout }) {
  if (!rollout) return null;
  return (
    <Tooltip label={`Rollout #${rollout.id} by ${rollout.createdBy}: ${rollout.state}`}>
      <span>
        <Pill size="xs" tone={rolloutTone[rollout.state] ?? 'gray'}>
          rollout {rollout.state}
        </Pill>
      </span>
    </Tooltip>
  );
}

/** One app on one cluster; links to that cluster's card on the app page. */
function Cell({ app, cluster, summary }: { app: string; cluster: string; summary?: AppSummary }) {
  if (!summary) {
    return <span className={classes.absent}>— not deployed</span>;
  }
  const pending = isOutOfSync(summary) && summary.deployedRevision && summary.deployedRevision !== summary.revision;
  return (
    <UnstyledButton
      component={Link}
      to={`/apps/${encodeURIComponent(app)}#${cluster}`}
      aria-label={`${app} on ${cluster}`}
      className={classes.cell}
    >
      <Group gap={4}>
        <SyncBadge summary={summary} />
        <HealthBadge summary={summary} />
      </Group>
      <span className={classes.rev}>
        {pending ? (
          <>
            {shortRev(summary.deployedRevision)} → <strong>{shortRev(summary.revision)}</strong>
          </>
        ) : (
          shortRev(summary.revision)
        )}
      </span>
    </UnstyledButton>
  );
}

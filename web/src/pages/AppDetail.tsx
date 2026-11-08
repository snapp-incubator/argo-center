import { useEffect, useState, type ReactNode } from 'react';
import {
  ActionIcon,
  Alert,
  Anchor,
  Button,
  Group,
  Loader,
  Menu,
  Paper,
  Popover,
  Stack,
  Text,
  Title,
  Tooltip,
} from '@mantine/core';
import { Link, useLocation, useParams } from 'react-router';
import { useQueryClient } from '@tanstack/react-query';
import {
  NotFoundError,
  isActiveRollout,
  terminateOperation,
  useApp,
  useClusters,
  useLatestRollout,
  useRefresh,
  type AppSummary,
  type ClusterState,
  type RefreshState,
} from '../api';
import { ClusterChanges } from '../components/ClusterChanges';
import { RecentActivity } from '../components/RecentActivity';
import { ClusterStatus, HealthBadge, isSyncing, SyncBadge } from '../components/Status';
import { SyncDialog } from '../components/SyncDialog';
import { RolloutBanner } from '../components/RolloutBanner';
import { RolloutDialog } from '../components/RolloutDialog';
import { compareURL, shortRev, timeAgo } from '../format';
import { Rev } from '../components/Rev';
import { ChevronDownIcon, ExternalIcon, RefreshIcon } from '../components/icons';
import classes from './AppDetail.module.css';
import { usePageTitle } from '../usePageTitle';

export function AppDetail() {
  const { name = '' } = useParams();
  const clusters = useClusters();
  const app = useApp(name);
  usePageTitle(name);
  const { hash } = useLocation();
  const { state: refreshing, refresh } = useRefresh(name);
  const latestRollout = useLatestRollout(name);
  const [rolloutOpen, setRolloutOpen] = useState(false);

  const crumbs = (
    <nav aria-label="Breadcrumb" className={classes.crumbs}>
      <Link to="/">Applications</Link> / <Text span inherit c="var(--mantine-color-text)">{name}</Text>
    </nav>
  );

  if (clusters.isPending || app.isPending) return <Loader m="xl" />;
  if (app.error instanceof NotFoundError) {
    return (
      <Stack>
        {crumbs}
        <Alert color="gray" title="Not found">
          No ArgoCD instance has an application named “{name}”.
        </Alert>
      </Stack>
    );
  }
  if (clusters.error || app.error) {
    return (
      <Alert color="red" title="Could not load application">
        {String(clusters.error ?? app.error)}
      </Alert>
    );
  }

  const first = Object.values(app.data.cells)[0];
  const present = clusters.data.filter((c) => app.data.cells[c.key]).map((c) => c.key);
  const anyRefreshing = present.some((k) => refreshing[k]?.pending);
  const rolloutActive = latestRollout.data ? isActiveRollout(latestRollout.data) : false;
  const rolloutRunningOn = (cluster: string) =>
    latestRollout.data?.state === 'running' && latestRollout.data.steps.some((s) => s.cluster === cluster)
      ? latestRollout.data.id
      : undefined;

  return (
    <Stack gap="md">
      {crumbs}
      <Group justify="space-between" align="flex-start" gap="md">
        <Stack gap={6} style={{ minWidth: 0 }}>
          <Title order={1}>{name}</Title>
          <div className={classes.meta}>
            <Meta label="project">{first.project}</Meta>
            <Meta label="repo">{first.repoURL}</Meta>
            <Meta label="path" mono>
              {first.path}
            </Meta>
            <Meta label="tracking" mono>
              {first.targetRevision}
            </Meta>
          </div>
        </Stack>
        <Group gap="sm">
          <RefreshButton
            label="Refresh all"
            state={{ pending: anyRefreshing }}
            onRefresh={(hard) => refresh(present, hard)}
          />
          <Tooltip label="An active rollout must finish or be aborted first" disabled={!rolloutActive}>
            <Button onClick={() => setRolloutOpen(true)} disabled={rolloutActive}>
              Sync all…
            </Button>
          </Tooltip>
        </Group>
      </Group>
      <RolloutDialog
        app={name}
        clusters={clusters.data}
        cells={app.data.cells}
        opened={rolloutOpen}
        onClose={() => setRolloutOpen(false)}
        refreshing={refreshing}
        onRefresh={(keys) => refresh(keys)}
      />

      {latestRollout.data && (
        <RolloutBanner rollout={latestRollout.data} clusters={clusters.data} repo={first.repoURL} />
      )}

      {clusters.data.map((c) => (
        <ClusterCard
          key={c.key}
          app={name}
          cluster={c}
          summary={app.data.cells[c.key]}
          refreshState={refreshing[c.key]}
          onRefresh={(hard) => refresh([c.key], hard)}
          rolloutID={rolloutRunningOn(c.key)}
        />
      ))}

      <RecentActivity app={name} repo={first.repoURL} clusters={clusters.data} />
      <ScrollToHash hash={hash} />
    </Stack>
  );
}

function Meta({ label, mono, children }: { label: string; mono?: boolean; children: ReactNode }) {
  return (
    <span>
      {label} <strong data-mono={mono || undefined}>{children || '—'}</strong>
    </span>
  );
}

/** A refresh button with a menu for a hard refresh, which also regenerates manifests. */
function RefreshButton({
  label,
  state,
  onRefresh,
}: {
  label: string;
  state?: RefreshState;
  onRefresh: (hard: boolean) => void;
}) {
  return (
    <Group gap={0} wrap="nowrap">
      <Button
        variant="default"
        loading={state?.pending}
        leftSection={<RefreshIcon />}
        onClick={() => onRefresh(false)}
        style={{ borderTopRightRadius: 0, borderBottomRightRadius: 0 }}
      >
        {label}
      </Button>
      <Menu position="bottom-end" withinPortal>
        <Menu.Target>
          <ActionIcon
            variant="default"
            size={36}
            disabled={state?.pending}
            aria-label={`More ${label.toLowerCase()} options`}
            style={{ borderTopLeftRadius: 0, borderBottomLeftRadius: 0, borderLeft: 0 }}
          >
            <ChevronDownIcon />
          </ActionIcon>
        </Menu.Target>
        <Menu.Dropdown>
          <Menu.Item onClick={() => onRefresh(true)}>
            <Text size="sm">Hard refresh</Text>
            <Text size="xs" c="dimmed">
              Regenerates manifests from Git, skipping ArgoCD's cache
            </Text>
          </Menu.Item>
        </Menu.Dropdown>
      </Menu>
    </Group>
  );
}

function ClusterCard({
  app,
  cluster,
  summary,
  refreshState,
  onRefresh,
  rolloutID,
}: {
  app: string;
  cluster: ClusterState;
  summary?: AppSummary;
  refreshState?: RefreshState;
  onRefresh: (hard: boolean) => void;
  /** Set while a running rollout includes this cluster; single-cluster sync is blocked meanwhile. */
  rolloutID?: number;
}) {
  const [syncOpen, setSyncOpen] = useState(false);
  const failed = summary?.operationPhase === 'Failed' || summary?.operationPhase === 'Error';
  const running = summary !== undefined && isSyncing(summary);

  const compare = summary && compareURL(summary.repoURL, summary.deployedRevision, summary.revision);
  return (
    <Paper
      withBorder
      className={classes.card}
      component="section"
      aria-label={cluster.displayName}
      id={cluster.key}
    >
      <div className={classes.cardHead} data-divided={summary ? true : undefined}>
        <Group gap="sm">
          <Title order={2} fz={16}>
            {cluster.displayName}
          </Title>
          {summary && <SyncBadge summary={summary} />}
          {summary && <HealthBadge summary={summary} always />}
          {cluster.state !== 'ok' && <ClusterStatus cluster={cluster} />}
          {!summary && (
            <Text size="sm" c="dimmed">
              Not deployed on this cluster.
            </Text>
          )}
        </Group>
        {summary && (
          <Group gap="sm">
            <Anchor href={summary.argoURL} target="_blank" rel="noreferrer" className={classes.external}>
              Open in ArgoCD
              <ExternalIcon />
            </Anchor>
            <RefreshButton label="Refresh" state={refreshState} onRefresh={onRefresh} />
            {running ? (
              <TerminateButton app={app} cluster={cluster.key} />
            ) : (
              <Tooltip label={`Rollout #${rolloutID} is running on this cluster`} disabled={rolloutID === undefined}>
                <Button
                  onClick={() => setSyncOpen(true)}
                  disabled={cluster.state === 'error' || rolloutID !== undefined}
                >
                  {rolloutID !== undefined ? 'In rollout' : 'Sync…'}
                </Button>
              </Tooltip>
            )}
          </Group>
        )}
      </div>
      {summary && (
        <SyncDialog
          app={app}
          cluster={cluster}
          summary={summary}
          opened={syncOpen}
          onClose={() => setSyncOpen(false)}
          refreshState={refreshState}
          onRefresh={onRefresh}
        />
      )}

      {summary && (
        <div className={classes.cardBody}>
          <dl className={classes.facts}>
            <dt>Live</dt>
            <dd data-mono>
              <Rev repo={summary.repoURL} rev={summary.deployedRevision} size="xs" />
            </dd>
            <dt>Target</dt>
            <dd data-mono>
              <Rev repo={summary.repoURL} rev={summary.revision} size="xs" />
              {compare && (
                <Anchor
                  href={compare}
                  target="_blank"
                  rel="noreferrer"
                  size="xs"
                  ml={8}
                  ff="var(--mantine-font-family)"
                  title="Commits between the live and the target revision"
                >
                  compare ↗
                </Anchor>
              )}
            </dd>
            <dt>Out of sync</dt>
            <dd>
              {summary.outOfSyncResources === 1 ? '1 resource' : `${summary.outOfSyncResources} resources`}
            </dd>
            <dt>Last deployed</dt>
            <dd>{timeAgo(summary.lastDeployedAt)}</dd>
            <dt>Last operation</dt>
            <dd>
              {summary.operationPhase
                ? `${summary.operationDryRun ? 'Dry run ' : ''}${summary.operationPhase} · ${timeAgo(summary.operationFinishedAt || summary.operationStartedAt)}`
                : '—'}
            </dd>
          </dl>
          <div className={classes.changes}>
            {refreshState?.error && (
              <Alert color="red" variant="light" title="Refresh failed">
                {refreshState.error}
              </Alert>
            )}
            {running && (
              <Alert
                color="blue"
                variant="light"
                icon={<Loader size={16} />}
                title={summary.operationDryRun ? 'Dry run running' : 'Sync running'}
              >
                <Text size="sm" style={{ whiteSpace: 'pre-wrap' }}>
                  {summary.operationRevision && `Applying ${shortRev(summary.operationRevision)}. `}
                  {summary.operationMessage}
                </Text>
              </Alert>
            )}
            {failed && summary.operationMessage && (
              <Alert
                color="red"
                variant="light"
                title={`Last ${summary.operationDryRun ? 'dry run' : 'sync'} ${summary.operationPhase?.toLowerCase()}`}
              >
                <Text size="sm" style={{ whiteSpace: 'pre-wrap' }}>
                  {summary.operationMessage}
                </Text>
              </Alert>
            )}
            <ClusterChanges app={app} cluster={cluster.key} summary={summary} />
          </div>
        </div>
      )}
    </Paper>
  );
}

/** Stops the running operation, after a confirmation. */
function TerminateButton({ app, cluster }: { app: string; cluster: string }) {
  const queryClient = useQueryClient();
  const [opened, setOpened] = useState(false);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string>();

  const terminate = async () => {
    setPending(true);
    setError(undefined);
    try {
      await terminateOperation(app, cluster);
      setOpened(false);
      queryClient.invalidateQueries({ queryKey: ['apps', app] });
      queryClient.invalidateQueries({ queryKey: ['audit', app] });
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setPending(false);
    }
  };

  return (
    <Popover opened={opened} onChange={setOpened} position="bottom-end" withArrow shadow="md" width={300}>
      <Popover.Target>
        <Button color="red" variant="light" onClick={() => setOpened((o) => !o)}>
          Terminate
        </Button>
      </Popover.Target>
      <Popover.Dropdown>
        <Stack gap="xs">
          <Text size="sm">Stop the running operation? Resources already applied stay as they are.</Text>
          {error && (
            <Text size="sm" c="red">
              {error}
            </Text>
          )}
          <Group justify="flex-end" gap="xs">
            <Button size="xs" variant="default" onClick={() => setOpened(false)}>
              Keep running
            </Button>
            <Button size="xs" color="red" loading={pending} onClick={terminate}>
              Terminate
            </Button>
          </Group>
        </Stack>
      </Popover.Dropdown>
    </Popover>
  );
}

/** Scrolls to the cluster card named in the URL hash (#cluster-a), once it has rendered. */
function ScrollToHash({ hash }: { hash: string }) {
  useEffect(() => {
    const id = decodeURIComponent(hash.slice(1));
    if (id) document.getElementById(id)?.scrollIntoView({ block: 'start' });
  }, [hash]);
  return null;
}

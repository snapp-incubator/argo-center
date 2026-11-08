import type { ReactNode } from 'react';
import { Tooltip } from '@mantine/core';
import type { AppSummary, ClusterState } from '../api';
import { timeAgo } from '../format';
import { AlertCircleIcon, ArcIcon, CheckIcon, HeartIcon, PauseIcon, QuestionIcon, SpinnerIcon, WarningIcon } from './icons';
import classes from './Status.module.css';

export type Tone = 'green' | 'orange' | 'blue' | 'red' | 'yellow' | 'gray';

/** A rounded status label with an optional leading icon. */
export function Pill({
  tone = 'gray',
  icon,
  size,
  children,
}: {
  tone?: Tone;
  icon?: ReactNode;
  size?: 'xs';
  children: ReactNode;
}) {
  return (
    <span className={classes.pill} data-tone={tone} data-size={size}>
      {icon}
      {children}
    </span>
  );
}

export const isSyncing = (s: AppSummary) => s.operationPhase === 'Running' || s.operationPhase === 'Terminating';

export function SyncBadge({ summary }: { summary: AppSummary }) {
  if (isSyncing(summary)) {
    return (
      <Pill tone="blue" icon={<SpinnerIcon />}>
        Syncing
      </Pill>
    );
  }
  switch (summary.syncStatus) {
    case 'Synced':
      return (
        <Pill tone="green" icon={<CheckIcon />}>
          Synced
        </Pill>
      );
    case 'OutOfSync':
      return (
        <Pill tone="orange" icon={<AlertCircleIcon />}>
          Out of sync
        </Pill>
      );
    default:
      return (
        <Pill tone="gray" icon={<QuestionIcon />}>
          Unknown
        </Pill>
      );
  }
}

const health: Record<string, { tone: Tone; icon: ReactNode }> = {
  Progressing: { tone: 'blue', icon: <ArcIcon /> },
  Degraded: { tone: 'red', icon: <WarningIcon /> },
  Suspended: { tone: 'gray', icon: <PauseIcon /> },
  Missing: { tone: 'gray', icon: <QuestionIcon /> },
};

/** Health status. Hidden for Healthy apps unless always is set, to keep the overview quiet. */
export function HealthBadge({ summary, always = false }: { summary: AppSummary; always?: boolean }) {
  const status = summary.healthStatus || 'Unknown';
  if (status === 'Healthy') {
    return always ? (
      <span className={classes.healthy}>
        <HeartIcon />
        Healthy
      </span>
    ) : null;
  }
  const look = health[status] ?? { tone: 'gray' as Tone, icon: <QuestionIcon /> };
  const badge = (
    <Pill tone={look.tone} icon={look.icon}>
      {status}
    </Pill>
  );
  return summary.healthMessage ? (
    <Tooltip label={summary.healthMessage} multiline maw={420}>
      {badge}
    </Tooltip>
  ) : (
    badge
  );
}

export function ClusterStatus({ cluster }: { cluster: ClusterState }) {
  const label = { ok: 'Connected', connecting: 'Connecting…', error: 'Unreachable' }[cluster.state];
  const detail =
    cluster.state === 'error'
      ? `${cluster.error} · showing data from ${timeAgo(cluster.lastUpdate)}`
      : `${cluster.appCount} apps · updated ${timeAgo(cluster.lastUpdate)}`;

  return (
    <Tooltip label={detail} multiline maw={360}>
      <span className={classes.clusterStatus} data-state={cluster.state}>
        <span aria-hidden className={classes.dot} />
        {label}
      </span>
    </Tooltip>
  );
}

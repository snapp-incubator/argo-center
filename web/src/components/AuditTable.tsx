import { Anchor, Table, Text, Tooltip } from '@mantine/core';
import { Link } from 'react-router';
import type { AuditEntry, ClusterState } from '../api';
import { timeAgo } from '../format';
import { Rev } from './Rev';
import { Pill } from './Status';

export const actionLabel: Record<AuditEntry['action'], string> = {
  sync: 'Sync',
  'dry-run': 'Dry run',
  terminate: 'Terminate',
  'refresh-hard': 'Hard refresh',
  'rollout-start': 'Rollout started',
  'rollout-abort': 'Rollout aborted',
  'rollout-resume': 'Rollout resumed',
  'rollout-confirm': 'Rollout confirmed',
};

interface Props {
  entries: AuditEntry[];
  clusters: ClusterState[];
  /** Adds an App column linking to the app page (the audit page lists every app). */
  showApp?: boolean;
  /** Repo per app, to link revisions to their commits. */
  repoOf?: (app: string) => string | undefined;
}

/** Audit log entries, newest first. "Accepted" means ArgoCD accepted the request. */
export function AuditTable({ entries, clusters, showApp, repoOf }: Props) {
  const clusterName = (key: string) => clusters.find((c) => c.key === key)?.displayName ?? key;
  return (
    <Table.ScrollContainer minWidth={showApp ? 860 : 640}>
      <Table className="ac-table" verticalSpacing={8} fz="sm">
        <Table.Thead>
          <Table.Tr>
            <Table.Th>When</Table.Th>
            <Table.Th>Who</Table.Th>
            <Table.Th>Action</Table.Th>
            {showApp && <Table.Th>App</Table.Th>}
            <Table.Th>Cluster</Table.Th>
            <Table.Th>Revision</Table.Th>
            <Table.Th>Options</Table.Th>
            <Table.Th>Result</Table.Th>
          </Table.Tr>
        </Table.Thead>
        <Table.Tbody>
          {entries.map((e) => (
            <Table.Tr key={e.id}>
              <Table.Td>
                <Tooltip label={new Date(e.time).toLocaleString()}>
                  <span>{timeAgo(e.time)}</span>
                </Tooltip>
              </Table.Td>
              <Table.Td>{e.user}</Table.Td>
              <Table.Td>{actionLabel[e.action] ?? e.action}</Table.Td>
              {showApp && (
                <Table.Td>
                  <Anchor component={Link} to={`/apps/${encodeURIComponent(e.app)}`} size="sm">
                    {e.app}
                  </Anchor>
                </Table.Td>
              )}
              <Table.Td>{e.cluster ? clusterName(e.cluster) : '—'}</Table.Td>
              <Table.Td>{e.revision ? <Rev repo={repoOf?.(e.app)} rev={e.revision} /> : '—'}</Table.Td>
              <Table.Td>
                <Text size="xs" c="dimmed">
                  {e.options.join(' · ') || '—'}
                </Text>
              </Table.Td>
              <Table.Td>
                {e.result === 'ok' ? (
                  <Pill size="xs" tone="green">
                    Accepted
                  </Pill>
                ) : (
                  <Tooltip label={e.message} multiline maw={420}>
                    <span>
                      <Pill size="xs" tone="red">
                        Failed
                      </Pill>
                    </span>
                  </Tooltip>
                )}
              </Table.Td>
            </Table.Tr>
          ))}
        </Table.Tbody>
      </Table>
    </Table.ScrollContainer>
  );
}

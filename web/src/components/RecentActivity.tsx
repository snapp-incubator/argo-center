import { Alert, Anchor, Group, Paper, Text, Title } from '@mantine/core';
import { Link } from 'react-router';
import { useAudit, type ClusterState } from '../api';
import { AuditTable } from './AuditTable';

/** The latest actions taken on an app through argo-center, from the audit log. */
export function RecentActivity({ app, repo, clusters }: { app: string; repo?: string; clusters: ClusterState[] }) {
  const audit = useAudit(app);
  return (
    <Paper withBorder p="md" component="section" aria-label="Recent activity">
      <Group justify="space-between" mb="sm">
        <Title order={4}>Recent activity</Title>
        <Anchor component={Link} to={`/audit?app=${encodeURIComponent(app)}`} size="sm">
          Full audit log
        </Anchor>
      </Group>
      {audit.isError ? (
        <Alert color="red" variant="light">
          {audit.error.message}
        </Alert>
      ) : !audit.data ? null : audit.data.length === 0 ? (
        <Text size="sm" c="dimmed">
          Nothing done on this app through argo-center yet.
        </Text>
      ) : (
        <AuditTable entries={audit.data} clusters={clusters} repoOf={() => repo} />
      )}
    </Paper>
  );
}

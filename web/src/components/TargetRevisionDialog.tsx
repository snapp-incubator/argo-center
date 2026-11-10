import { useEffect, useState } from 'react';
import { Alert, Button, Checkbox, Code, Group, Modal, Stack, Text, TextInput } from '@mantine/core';
import { useQueryClient } from '@tanstack/react-query';
import { setTargetRevision, type AppSummary, type ClusterState } from '../api';
import { trackedRev } from '../format';

interface Props {
  app: string;
  /** Clusters in config order; only those with the app are offered. */
  clusters: ClusterState[];
  cells: Record<string, AppSummary>;
  opened: boolean;
  onClose: () => void;
  /** Called with the clusters that were changed, to compare them with the new revision. */
  onChanged: (clusters: string[]) => void;
}

/**
 * Changes the revision an app tracks (a branch, tag or commit instead of HEAD) on chosen clusters.
 * Nothing is synced here: the clusters are compared with the new revision and synced as usual.
 */
export function TargetRevisionDialog({ app, clusters, cells, opened, onClose, onChanged }: Props) {
  const queryClient = useQueryClient();
  const present = clusters.filter((c) => cells[c.key]);
  const editable = present.filter((c) => !cells[c.key].multiSource);
  const [revision, setRevision] = useState('');
  const [included, setIncluded] = useState<ReadonlySet<string>>(new Set());
  const [submitting, setSubmitting] = useState(false);
  const [errors, setErrors] = useState<Record<string, string>>({});

  useEffect(() => {
    if (!opened) return;
    const current = new Set(editable.map((c) => trackedRev(cells[c.key])));
    // Start from the current revision when all clusters agree, so a small edit is easy.
    setRevision(current.size === 1 ? [...current][0] : '');
    setIncluded(new Set(editable.map((c) => c.key)));
    setErrors({});
    // Only on open.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [opened]);

  const rev = revision.trim();
  const invalid = /\s/.test(rev) ? 'A revision cannot contain spaces.' : undefined;
  // Clusters that already track this revision are left alone.
  const chosen = editable.filter((c) => included.has(c.key) && trackedRev(cells[c.key]) !== rev);
  const autoSynced = chosen.filter((c) => cells[c.key].autoSync);

  const toggle = (key: string) =>
    setIncluded((s) => {
      const next = new Set(s);
      if (!next.delete(key)) next.add(key);
      return next;
    });

  const apply = async () => {
    setSubmitting(true);
    setErrors({});
    const results = await Promise.allSettled(chosen.map((c) => setTargetRevision(app, c.key, rev)));
    const failed: Record<string, string> = {};
    const changed: string[] = [];
    results.forEach((res, i) => {
      if (res.status === 'rejected') failed[chosen[i].key] = (res.reason as Error).message;
      else changed.push(chosen[i].key);
    });
    queryClient.invalidateQueries({ queryKey: ['apps', app] });
    queryClient.invalidateQueries({ queryKey: ['audit', app] });
    if (changed.length > 0) onChanged(changed);
    setSubmitting(false);
    setErrors(failed);
    if (Object.keys(failed).length === 0) onClose();
  };

  return (
    <Modal opened={opened} onClose={onClose} size="lg" title={<Text fw={700}>Change the revision {app} tracks</Text>}>
      <Stack gap="md">
        <Text size="sm" c="dimmed">
          Sets the app's target revision in ArgoCD. Nothing is synced: each cluster is compared with the new revision,
          and you review and sync the difference as usual. Use <Code>HEAD</Code> to go back to the default branch.
        </Text>

        <TextInput
          label="Target revision"
          description="A branch, tag or commit (or a chart version for Helm repositories)"
          placeholder="HEAD"
          value={revision}
          onChange={(e) => setRevision(e.currentTarget.value)}
          error={invalid}
          data-autofocus
          styles={{ input: { fontFamily: 'var(--mantine-font-family-monospace)' } }}
        />

        <Stack gap={6}>
          {present.map((c) => {
            const s = cells[c.key];
            return (
              <Checkbox
                key={c.key}
                checked={!s.multiSource && included.has(c.key)}
                disabled={s.multiSource}
                onChange={() => toggle(c.key)}
                label={
                  <Group gap={8}>
                    <Text size="sm" span>
                      {c.displayName}
                    </Text>
                    <Text size="xs" c="dimmed" span>
                      {s.multiSource ? (
                        'several sources: change it in ArgoCD'
                      ) : (
                        <>
                          tracks <Code>{trackedRev(s)}</Code>
                          {s.autoSync && ' · auto-sync'}
                        </>
                      )}
                    </Text>
                  </Group>
                }
                error={errors[c.key]}
              />
            );
          })}
        </Stack>

        {autoSynced.length > 0 && (
          <Alert color="yellow" variant="light">
            Auto-sync is on for {autoSynced.map((c) => c.displayName).join(', ')}: ArgoCD deploys the new revision
            there by itself, without a review.
          </Alert>
        )}
        {Object.keys(errors).length > 0 && (
          <Alert color="red" variant="light" title="Some clusters were not changed">
            ArgoCD refused the change. The token of that instance may lack the <Code>applications, update</Code> right.
          </Alert>
        )}

        <Group justify="flex-end" gap="sm">
          <Button variant="default" onClick={onClose}>
            Cancel
          </Button>
          <Button loading={submitting} disabled={!rev || !!invalid || chosen.length === 0} onClick={apply}>
            {chosen.length === 1 ? 'Change on 1 cluster' : `Change on ${chosen.length} clusters`}
          </Button>
        </Group>
      </Stack>
    </Modal>
  );
}

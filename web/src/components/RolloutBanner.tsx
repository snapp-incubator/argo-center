import { useState } from 'react';
import {
  ActionIcon,
  Alert,
  Button,
  Checkbox,
  Group,
  Loader,
  Paper,
  Popover,
  SimpleGrid,
  Stack,
  Text,
} from '@mantine/core';
import { useQueryClient } from '@tanstack/react-query';
import { isActiveRollout, rolloutAction, type ClusterState, type Rollout, type RolloutStep } from '../api';
import { timeAgo } from '../format';
import { Rev } from './Rev';
import { Pill, type Tone } from './Status';

const stateColor: Record<Rollout['state'], Tone> = {
  running: 'blue',
  paused: 'yellow',
  failed: 'red',
  interrupted: 'orange',
  aborted: 'gray',
  succeeded: 'green',
};

const stepLook: Record<RolloutStep['state'], { color: Tone; label: string }> = {
  pending: { color: 'gray', label: 'Pending' },
  refreshing: { color: 'blue', label: 'Comparing' },
  syncing: { color: 'blue', label: 'Syncing' },
  waiting: { color: 'blue', label: 'Waiting for Healthy' },
  succeeded: { color: 'green', label: 'Done' },
  skipped: { color: 'green', label: 'Already synced' },
  paused: { color: 'yellow', label: 'Paused' },
  failed: { color: 'red', label: 'Failed' },
  aborted: { color: 'gray', label: 'Aborted' },
  interrupted: { color: 'orange', label: 'Interrupted' },
};

const busySteps = new Set(['refreshing', 'syncing', 'waiting']);

/** Progress and controls of the app's latest rollout. Finished rollouts can be dismissed. */
export function RolloutBanner({
  rollout,
  clusters,
  repo,
}: {
  rollout: Rollout;
  clusters: ClusterState[];
  repo?: string;
}) {
  const queryClient = useQueryClient();
  const [dismissed, setDismissed] = useState<number>();
  const [pending, setPending] = useState<string>();
  const [error, setError] = useState<string>();
  const active = isActiveRollout(rollout);
  // Finished rollouts stay visible for a while so their outcome can be seen, then drop off.
  const stale = !active && rollout.finishedAt && Date.now() - new Date(rollout.finishedAt).getTime() > 6 * 3600_000;
  if (stale || (!active && dismissed === rollout.id)) return null;

  const name = (key: string) => clusters.find((c) => c.key === key)?.displayName ?? key;
  const act = async (action: 'abort' | 'resume' | 'confirm', terminate = false) => {
    setPending(action);
    setError(undefined);
    try {
      await rolloutAction(rollout.id, action, { terminate });
      queryClient.invalidateQueries({ queryKey: ['rollouts', rollout.app] });
      queryClient.invalidateQueries({ queryKey: ['audit', rollout.app] });
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setPending(undefined);
    }
  };
  const opts = [
    rollout.options.applyOutOfSyncOnly && 'apply out-of-sync only',
    rollout.options.prune && 'prune',
  ].filter(Boolean);

  return (
    <Paper
      withBorder
      p="md"
      component="section"
      aria-label={`Rollout #${rollout.id}`}
      style={{ borderColor: active ? `var(--mantine-color-${stateColor[rollout.state]}-filled)` : undefined }}
    >
      <Group justify="space-between" align="flex-start" mb="sm">
        <div>
          <Group gap="sm">
            <Text fw={700}>Rollout #{rollout.id}</Text>
            <Pill tone={stateColor[rollout.state]}>{rollout.state}</Pill>
          </Group>
          <Text size="xs" c="dimmed" mt={2}>
            started by {rollout.createdBy} {timeAgo(rollout.createdAt)}
            {rollout.actingUser !== rollout.createdBy && ` · continued by ${rollout.actingUser}`}
            {opts.length > 0 && ` · ${opts.join(' · ')}`}
            {rollout.finishedAt && ` · finished ${timeAgo(rollout.finishedAt)}`}
          </Text>
        </div>
        <Group gap="xs">
          {rollout.state === 'paused' && (
            <Button size="xs" loading={pending === 'confirm'} onClick={() => act('confirm')}>
              Continue with the new commit
            </Button>
          )}
          {(rollout.state === 'failed' || rollout.state === 'interrupted') && (
            <Button size="xs" loading={pending === 'resume'} onClick={() => act('resume')}>
              Resume
            </Button>
          )}
          {active && (
            <AbortButton
              syncing={rollout.steps.some((s) => s.state === 'syncing')}
              loading={pending === 'abort'}
              onAbort={(terminate) => act('abort', terminate)}
            />
          )}
          {!active && (
            <ActionIcon variant="subtle" color="gray" onClick={() => setDismissed(rollout.id)} aria-label="Dismiss">
              ✕
            </ActionIcon>
          )}
        </Group>
      </Group>

      <SimpleGrid cols={{ base: 1, sm: Math.min(rollout.steps.length, 5) }} spacing="xs">
        {rollout.steps.map((s, i) => {
          const look = stepLook[s.state];
          return (
            <Paper
              key={s.cluster}
              withBorder
              radius="sm"
              p="xs"
              bg={s.state === 'pending' ? undefined : 'var(--mantine-color-default-hover)'}
            >
              <Group gap={6} wrap="nowrap">
                <Text size="xs" c="dimmed">
                  {i + 1}
                </Text>
                <Text size="sm" fw={600} truncate>
                  {name(s.cluster)}
                </Text>
              </Group>
              <Group gap={6} mt={4} wrap="nowrap">
                {busySteps.has(s.state) && <Loader size={12} />}
                <Pill size="xs" tone={look.color}>
                  {look.label}
                </Pill>
                {s.revision && <Rev repo={repo} rev={s.revision} size="xs" />}
              </Group>
              {s.message && (
                <Text size="xs" c="dimmed" mt={4} lineClamp={3} title={s.message}>
                  {s.message}
                </Text>
              )}
            </Paper>
          );
        })}
      </SimpleGrid>

      {(rollout.state === 'failed' || rollout.state === 'paused' || rollout.state === 'interrupted') &&
        rollout.message && (
          <Alert mt="sm" variant="light" color={stateColor[rollout.state]}>
            {rollout.message}
            {rollout.state === 'paused' && (
              <Text size="sm" mt={4}>
                The cluster card below shows the diff for the new commit.
              </Text>
            )}
          </Alert>
        )}
      {error && (
        <Text size="sm" c="red" mt="sm">
          {error}
        </Text>
      )}
    </Paper>
  );
}

function AbortButton({
  syncing,
  loading,
  onAbort,
}: {
  syncing: boolean;
  loading: boolean;
  onAbort: (terminate: boolean) => void;
}) {
  const [opened, setOpened] = useState(false);
  const [terminate, setTerminate] = useState(false);
  return (
    <Popover opened={opened} onChange={setOpened} position="bottom-end" withArrow shadow="md" width={320}>
      <Popover.Target>
        <Button size="xs" color="red" variant="light" loading={loading} onClick={() => setOpened((o) => !o)}>
          Abort
        </Button>
      </Popover.Target>
      <Popover.Dropdown>
        <Stack gap="xs">
          <Text size="sm">Stop the rollout? Clusters not reached yet are left as they are.</Text>
          {syncing && (
            <Checkbox
              size="sm"
              checked={terminate}
              onChange={(e) => setTerminate(e.currentTarget.checked)}
              label="Also terminate the sync that is running now"
              description="Otherwise that sync finishes on its own."
            />
          )}
          <Group justify="flex-end" gap="xs">
            <Button size="xs" variant="default" onClick={() => setOpened(false)}>
              Keep going
            </Button>
            <Button
              size="xs"
              color="red"
              onClick={() => {
                setOpened(false);
                onAbort(syncing && terminate);
              }}
            >
              Abort rollout
            </Button>
          </Group>
        </Stack>
      </Popover.Dropdown>
    </Popover>
  );
}

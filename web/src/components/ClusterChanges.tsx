import { useState } from 'react';
import {
  Alert,
  Anchor,
  Code,
  Collapse,
  Group,
  Loader,
  Stack,
  Text,
  Tooltip,
  UnstyledButton,
} from '@mantine/core';
import { useDiff, type AppSummary, type DiffChange, type DiffResource } from '../api';
import { shortRev, timeAgo } from '../format';
import { DiffView } from './DiffView';
import { Pill } from './Status';
import classes from './ClusterChanges.module.css';

const listLimit = 10;
const chipLimit = 3;

const resourceKey = (r: DiffResource) => `${r.group}/${r.kind}/${r.namespace}/${r.name}`;

/** The brief diff of one app on one cluster: what a sync would change, with the full diff per resource on demand. */
export function ClusterChanges({ app, cluster, summary }: { app: string; cluster: string; summary: AppSummary }) {
  const outOfSync = summary.syncStatus === 'OutOfSync';
  const diff = useDiff(app, summary, cluster, outOfSync);
  const [open, setOpen] = useState<ReadonlySet<string>>(new Set());
  const [showAll, setShowAll] = useState(false);

  if (!outOfSync) {
    return summary.syncStatus === 'Synced' ? (
      <Text size="sm" c="dimmed">
        No differences. Live state matches <Code>{shortRev(summary.revision)}</Code>.
      </Text>
    ) : null;
  }
  if (diff.isPending) {
    return (
      <Group gap="xs">
        <Loader size="xs" />
        <Text size="sm" c="dimmed">
          Loading diff…
        </Text>
      </Group>
    );
  }
  if (diff.isError) {
    return (
      <Alert color="red" variant="light" title="Could not load diff">
        {diff.error.message}
      </Alert>
    );
  }

  const { resources, revision, reconciledAt } = diff.data;
  if (resources.length === 0) {
    return (
      <Text size="sm" c="dimmed">
        ArgoCD reports out of sync, but no resource differs. Try a refresh.
      </Text>
    );
  }

  const allOpen = resources.every((r) => open.has(resourceKey(r)));
  const toggle = (key: string) =>
    setOpen((s) => {
      const next = new Set(s);
      if (!next.delete(key)) next.add(key);
      return next;
    });
  const shown = showAll ? resources : resources.slice(0, listLimit);

  return (
    <Stack gap="xs">
      <Group justify="space-between" gap="xs">
        <Group gap="xs">
          <Text size="sm" fw={600}>
            {resources.length === 1 ? '1 resource differs' : `${resources.length} resources differ`}
          </Text>
          <Text size="xs" c="dimmed">
            compared with <Code>{shortRev(revision)}</Code> {timeAgo(reconciledAt)}
          </Text>
          {diff.isPlaceholderData && <Loader size={12} aria-label="Updating diff" />}
        </Group>
        <Anchor
          component="button"
          type="button"
          size="sm"
          onClick={() => {
            setOpen(allOpen ? new Set() : new Set(resources.map(resourceKey)));
            if (!allOpen) setShowAll(true);
          }}
        >
          {allOpen ? 'Hide full diffs' : 'Show full diffs'}
        </Anchor>
      </Group>

      <div className={classes.list}>
        {shown.map((r) => {
          const key = resourceKey(r);
          const expanded = open.has(key);
          return (
            <div key={key} className={classes.item}>
              <UnstyledButton className={classes.row} onClick={() => toggle(key)} aria-expanded={expanded}>
                <span className={classes.chevron} data-expanded={expanded || undefined} aria-hidden>
                  ›
                </span>
                <Text size="sm" fw={500} className={classes.name}>
                  {r.kind}/{r.name}
                </Text>
                {r.namespace && (
                  <Text size="xs" c="dimmed">
                    {r.namespace}
                  </Text>
                )}
                <StatusTag resource={r} />
                <Changes resource={r} />
              </UnstyledButton>
              <Collapse expanded={expanded} keepMounted={false}>
                <div className={classes.diff}>
                  <DiffView diff={r.unifiedDiff} truncated={r.truncated} />
                </div>
              </Collapse>
            </div>
          );
        })}
      </div>

      {resources.length > listLimit && (
        <Anchor component="button" type="button" size="sm" onClick={() => setShowAll((v) => !v)}>
          {showAll ? 'Show fewer' : `Show all ${resources.length} resources`}
        </Anchor>
      )}
    </Stack>
  );
}

function StatusTag({ resource }: { resource: DiffResource }) {
  switch (resource.status) {
    case 'missing':
      return (
        <Pill size="xs" tone="green">
          new · will be created
        </Pill>
      );
    case 'extra':
      return (
        <Tooltip label="In the cluster but not in Git. A sync deletes it only with Prune on.">
          <span>
            <Pill size="xs" tone="red">
              not in Git · prune
            </Pill>
          </span>
        </Tooltip>
      );
    default:
      return null;
  }
}

const opSymbol: Record<DiffChange['op'], string> = { changed: '~', added: '+', removed: '−' };
const opColor: Record<DiffChange['op'], string | undefined> = { changed: undefined, added: 'green', removed: 'red' };

/** Shortens long paths from the left, at a segment boundary, since the end of a path says the most. */
function shortPath(path: string, max = 64): string {
  if (path.length <= max) return path;
  const tail = path.slice(-max);
  const cut = tail.search(/[.[]/);
  return '…' + (cut > 0 ? tail.slice(cut) : tail);
}

function Changes({ resource }: { resource: DiffResource }) {
  if (resource.status !== 'modified') return null;
  if (resource.changes.length === 0) {
    return (
      <Text size="xs" c="dimmed">
        metadata only
      </Text>
    );
  }
  const shown = resource.changes.slice(0, chipLimit);
  const more = resource.changes.length - shown.length + (resource.moreChanges ?? 0);
  return (
    <Group gap={6} className={classes.changes}>
      {shown.map((c) => (
        <Tooltip key={c.path} label={`${c.op}: ${c.path}`} openDelay={300} multiline maw={560}>
          <Code c={opColor[c.op]} className={classes.chip}>
            {opSymbol[c.op]} {shortPath(c.path)}
          </Code>
        </Tooltip>
      ))}
      {more > 0 && (
        <Text size="xs" c="dimmed">
          +{more} more
        </Text>
      )}
    </Group>
  );
}

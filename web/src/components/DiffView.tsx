import { Box, Text } from '@mantine/core';
import classes from './DiffView.module.css';

/** Renders a unified diff (hunks only): "-" lines are live state, "+" lines are what a sync applies. */
export function DiffView({ diff, truncated }: { diff: string; truncated?: boolean }) {
  const lines = diff.endsWith('\n') ? diff.slice(0, -1).split('\n') : diff.split('\n');
  return (
    <Box className={classes.root}>
      <pre className={classes.pre}>
        {lines.map((line, i) => {
          const kind = line.startsWith('@@')
            ? classes.hunk
            : line.startsWith('+')
              ? classes.add
              : line.startsWith('-')
                ? classes.del
                : line.startsWith('\\')
                  ? classes.hunk
                  : undefined;
          return (
            <div key={i} className={kind}>
              {line || ' '}
            </div>
          );
        })}
      </pre>
      {truncated && (
        <Text size="xs" c="dimmed" px="sm" py={6}>
          Diff truncated. Open the app in ArgoCD to see all of it.
        </Text>
      )}
    </Box>
  );
}

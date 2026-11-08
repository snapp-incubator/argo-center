import { Anchor, Text } from '@mantine/core';
import { commitURL, shortRev } from '../format';

/** A short commit hash, linked to the commit in Git when the repo is known. */
export function Rev({ repo, rev, size = 'sm' }: { repo?: string; rev?: string; size?: string }) {
  const href = commitURL(repo, rev);
  if (!href) {
    return (
      <Text span size={size} ff="monospace">
        {shortRev(rev)}
      </Text>
    );
  }
  return (
    <Anchor href={href} target="_blank" rel="noreferrer" size={size} ff="monospace" title={rev}>
      {shortRev(rev)}
    </Anchor>
  );
}

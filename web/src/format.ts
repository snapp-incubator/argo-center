const relative = new Intl.RelativeTimeFormat('en', { numeric: 'auto' });

const units: [Intl.RelativeTimeFormatUnit, number][] = [
  ['year', 365 * 86400],
  ['month', 30 * 86400],
  ['day', 86400],
  ['hour', 3600],
  ['minute', 60],
  ['second', 1],
];

/** Formats an ISO timestamp as "3 hours ago". Empty input gives "never". */
export function timeAgo(iso?: string): string {
  if (!iso) return 'never';
  const seconds = (new Date(iso).getTime() - Date.now()) / 1000;
  for (const [unit, size] of units) {
    if (Math.abs(seconds) >= size || unit === 'second') {
      return relative.format(Math.round(seconds / size), unit);
    }
  }
  return '';
}

export const shortRev = (rev?: string) => (rev ? rev.slice(0, 7) : '—');

/** The revision an app tracks; ArgoCD reads an empty target revision as HEAD. */
export const trackedRev = (s: { targetRevision: string }) => s.targetRevision || 'HEAD';

/**
 * Web URL of a Git repository, from the forms ArgoCD uses: git@host:group/repo.git,
 * ssh://git@host/group/repo.git or https://host/group/repo(.git). Undefined if unrecognized.
 */
export function repoWebURL(repoURL?: string): string | undefined {
  if (!repoURL) return undefined;
  const scp = repoURL.match(/^[\w.-]+@([\w.-]+):(.+?)(\.git)?\/?$/);
  if (scp) return `https://${scp[1]}/${scp[2]}`;
  try {
    const u = new URL(repoURL);
    if (!['https:', 'http:', 'ssh:'].includes(u.protocol)) return undefined;
    const path = u.pathname.replace(/\.git\/?$/, '').replace(/\/$/, '');
    return `https://${u.hostname}${path}`;
  } catch {
    return undefined;
  }
}

const isFullSha = (rev?: string) => !!rev && /^[0-9a-f]{40}$/.test(rev);
// GitHub uses /commit/<sha>; GitLab uses /-/commit/<sha>.
const sep = (base: string) => (new URL(base).hostname === 'github.com' ? '' : '/-');

/** Link to a commit, if the repo and revision allow one. */
export function commitURL(repoURL?: string, rev?: string): string | undefined {
  const base = repoWebURL(repoURL);
  return base && isFullSha(rev) ? `${base}${sep(base)}/commit/${rev}` : undefined;
}

/** Link to the changes between two commits (what a sync would apply). */
export function compareURL(repoURL?: string, from?: string, to?: string): string | undefined {
  const base = repoWebURL(repoURL);
  return base && isFullSha(from) && isFullSha(to) && from !== to
    ? `${base}${sep(base)}/compare/${from}...${to}`
    : undefined;
}

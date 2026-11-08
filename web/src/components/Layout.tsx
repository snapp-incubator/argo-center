import { AppShell, Badge, Menu, Tooltip, UnstyledButton } from '@mantine/core';
import { Link, Outlet, useLocation } from 'react-router';
import { useLiveUpdates, useMe } from '../api';
import { ColorSchemeToggle } from './ColorSchemeToggle';
import { LogoIcon } from './icons';
import classes from './Layout.module.css';

export function Layout() {
  const live = useLiveUpdates();

  return (
    <AppShell header={{ height: 60 }}>
      <AppShell.Header className={classes.header} withBorder={false}>
        <div className={classes.inner}>
          <Link to="/" className={classes.brand}>
            <LogoIcon />
            argo-center
          </Link>
          <nav aria-label="Main" className={classes.nav}>
            <NavItem to="/" label="Applications" match={(p) => p === '/' || p.startsWith('/apps/')} />
            <NavItem to="/audit" label="Audit log" />
          </nav>
          <div className={classes.right}>
            <Tooltip label={live ? 'Receiving live updates' : 'Live updates disconnected, retrying'}>
              <span className={classes.live} data-off={live ? undefined : true}>
                <span aria-hidden className={classes.liveDot} />
                <span className={classes.liveLabel}>{live ? 'Live' : 'Reconnecting…'}</span>
              </span>
            </Tooltip>
            <UserMenu />
            <ColorSchemeToggle />
          </div>
        </div>
      </AppShell.Header>
      <AppShell.Main>
        <div className={classes.main}>
          <Outlet />
        </div>
      </AppShell.Main>
    </AppShell>
  );
}

const initials = (name: string) =>
  name
    .split(/[\s._@-]+/)
    .filter(Boolean)
    .slice(0, 2)
    .map((p) => p[0]!.toUpperCase())
    .join('') || '?';

function UserMenu() {
  const me = useMe();
  if (!me.data) return null;
  if (!me.data.authEnabled) {
    return (
      <Tooltip label="Login is disabled (insecureNoAuth). Actions are audited as user “local”.">
        <Badge color="orange" variant="light">
          No login
        </Badge>
      </Tooltip>
    );
  }
  // The server ends its session and names the next stop: the SSO's logout, which also ends the
  // single sign-on session, then /auth/signed-out. Going to / instead would log straight back in.
  const logout = async () => {
    const res = await fetch('/auth/logout', { method: 'POST' });
    const body = (await res.json().catch(() => undefined)) as { redirect?: string } | undefined;
    window.location.assign(body?.redirect ?? '/auth/signed-out');
  };
  return (
    <Menu position="bottom-end">
      <Menu.Target>
        <UnstyledButton className={classes.user} aria-label={`Signed in as ${me.data.name}`}>
          <span className={classes.userName}>{me.data.name}</span>
          <span aria-hidden className={classes.avatar}>
            {initials(me.data.name)}
          </span>
        </UnstyledButton>
      </Menu.Target>
      <Menu.Dropdown>
        {me.data.email && <Menu.Label>{me.data.email}</Menu.Label>}
        <Menu.Item onClick={logout}>Log out</Menu.Item>
      </Menu.Dropdown>
    </Menu>
  );
}

/** A header link, highlighted while its page is open. */
function NavItem({ to, label, match }: { to: string; label: string; match?: (path: string) => boolean }) {
  const { pathname } = useLocation();
  const active = match ? match(pathname) : pathname === to;
  return (
    <Link to={to} className={classes.navLink} aria-current={active ? 'page' : undefined}>
      {label}
    </Link>
  );
}

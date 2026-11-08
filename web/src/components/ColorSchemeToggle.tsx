import { ActionIcon, Tooltip, useComputedColorScheme, useMantineColorScheme } from '@mantine/core';

/** Toggles light/dark. Starts from the OS preference; the choice is remembered in localStorage by Mantine. */
export function ColorSchemeToggle() {
  const { setColorScheme } = useMantineColorScheme();
  const dark = useComputedColorScheme('light') === 'dark';
  const label = dark ? 'Switch to light mode' : 'Switch to dark mode';

  return (
    <Tooltip label={label}>
      <ActionIcon
        variant="default"
        size="lg"
        aria-label={label}
        onClick={() => setColorScheme(dark ? 'light' : 'dark')}
      >
        {dark ? <SunIcon /> : <MoonIcon />}
      </ActionIcon>
    </Tooltip>
  );
}

const iconProps = {
  width: 18,
  height: 18,
  viewBox: '0 0 24 24',
  fill: 'none',
  stroke: 'currentColor',
  strokeWidth: 2,
  strokeLinecap: 'round',
  strokeLinejoin: 'round',
  'aria-hidden': true,
} as const;

function SunIcon() {
  return (
    <svg {...iconProps}>
      <circle cx="12" cy="12" r="4" />
      <path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4" />
    </svg>
  );
}

function MoonIcon() {
  return (
    <svg {...iconProps}>
      <path d="M21 12.8A9 9 0 1 1 11.2 3a7 7 0 0 0 9.8 9.8z" />
    </svg>
  );
}

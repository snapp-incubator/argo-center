import type { SVGProps } from 'react';

type IconProps = SVGProps<SVGSVGElement> & { size?: number };

/** A stroke icon on a 24×24 grid, decorative unless given a title by the caller. */
function Icon({ size = 16, strokeWidth = 2, children, ...rest }: IconProps) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth={strokeWidth}
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden
      focusable="false"
      {...rest}
    >
      {children}
    </svg>
  );
}

export const LogoIcon = (p: IconProps) => (
  <Icon size={22} {...p}>
    <circle cx="12" cy="12" r="3" />
    <circle cx="12" cy="3.5" r="1.5" />
    <circle cx="20" cy="16.5" r="1.5" />
    <circle cx="4" cy="16.5" r="1.5" />
    <path d="M12 5v4" />
    <path d="m18.7 15.7-4.1-2.3" />
    <path d="m5.3 15.7 4.1-2.3" />
  </Icon>
);

export const CheckIcon = (p: IconProps) => (
  <Icon size={12} strokeWidth={3} {...p}>
    <path d="M20 6 9 17l-5-5" />
  </Icon>
);

export const AlertCircleIcon = (p: IconProps) => (
  <Icon size={12} strokeWidth={2.5} {...p}>
    <path d="M12 8v5" />
    <path d="M12 16.5h.01" />
    <circle cx="12" cy="12" r="9" />
  </Icon>
);

export const WarningIcon = (p: IconProps) => (
  <Icon size={12} strokeWidth={2.5} {...p}>
    <path d="M12 9v4" />
    <path d="M12 17h.01" />
    <path d="M10.3 3.9 1.8 18a2 2 0 0 0 1.7 3h17a2 2 0 0 0 1.7-3L13.7 3.9a2 2 0 0 0-3.4 0z" />
  </Icon>
);

export const SpinnerIcon = ({ className, ...p }: IconProps) => (
  <Icon size={12} strokeWidth={3} className={['ac-spin', className].filter(Boolean).join(' ')} {...p}>
    <path d="M21 12a9 9 0 1 1-6.2-8.6" />
  </Icon>
);

/** The spinner's arc, standing still: for a state (Progressing), not an activity. */
export const ArcIcon = (p: IconProps) => (
  <Icon size={12} strokeWidth={3} {...p}>
    <path d="M21 12a9 9 0 1 1-6.2-8.6" />
  </Icon>
);

export const HeartIcon = (p: IconProps) => (
  <Icon size={12} strokeWidth={2.5} {...p}>
    <path d="M19.5 12.6 12 20l-7.5-7.4A5 5 0 1 1 12 6a5 5 0 1 1 7.5 6.6z" />
  </Icon>
);

export const PauseIcon = (p: IconProps) => (
  <Icon size={12} strokeWidth={2.5} {...p}>
    <path d="M9 5v14" />
    <path d="M15 5v14" />
  </Icon>
);

export const QuestionIcon = (p: IconProps) => (
  <Icon size={12} strokeWidth={2.5} {...p}>
    <circle cx="12" cy="12" r="9" />
    <path d="M9.6 9.2a2.5 2.5 0 0 1 4.8.8c0 1.7-2.4 2.2-2.4 3.5" />
    <path d="M12 16.8h.01" />
  </Icon>
);

export const ExternalIcon = (p: IconProps) => (
  <Icon size={13} {...p}>
    <path d="M15 3h6v6" />
    <path d="M10 14 21 3" />
    <path d="M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6" />
  </Icon>
);

export const RefreshIcon = (p: IconProps) => (
  <Icon {...p}>
    <path d="M21 12a9 9 0 0 1-15 6.7L3 16" />
    <path d="M3 12a9 9 0 0 1 15-6.7L21 8" />
    <path d="M21 3v5h-5" />
    <path d="M3 21v-5h5" />
  </Icon>
);

export const SearchIcon = (p: IconProps) => (
  <Icon {...p}>
    <circle cx="11" cy="11" r="7" />
    <path d="m21 21-4.3-4.3" />
  </Icon>
);

export const ChevronDownIcon = (p: IconProps) => (
  <Icon size={14} strokeWidth={2.5} {...p}>
    <path d="m6 9 6 6 6-6" />
  </Icon>
);

import type { ReactNode } from 'react';

export type IconName =
  | "spark"
  | "send"
  | "arrow"
  | "lock"
  | "calendar"
  | "chat"
  | "check"
  | "clock"
  | "service"
  | "card"
  | "rules"
  | "x"
  | "refresh"
  | "chevron";

export function Icon({ name, size = 20 }: { name: IconName; size?: number }) {
  const paths: Record<IconName, ReactNode> = {
    spark: (
      <>
        <path d="m12 2 1.5 5.5L19 9l-5.5 1.5L12 16l-1.5-5.5L5 9l5.5-1.5Z" />
        <path d="m19 15 .7 2.3L22 18l-2.3.7L19 22l-.7-3.3L15 18l3.3-.7Z" />
      </>
    ),
    send: (
      <>
        <path d="m21 3-6.8 18-3.6-7.6L3 9.8Z" />
        <path d="m21 3-10.4 10.4" />
      </>
    ),
    arrow: <path d="m15 18-6-6 6-6" />,
    lock: (
      <>
        <rect x="5" y="10" width="14" height="11" rx="3" />
        <path d="M8 10V7a4 4 0 0 1 8 0v3" />
      </>
    ),
    calendar: (
      <>
        <rect x="3" y="5" width="18" height="16" rx="3" />
        <path d="M7 3v4M17 3v4M3 10h18" />
      </>
    ),
    chat: <path d="M21 15a4 4 0 0 1-4 4H8l-5 3V7a4 4 0 0 1 4-4h10a4 4 0 0 1 4 4Z" />,
    check: <path d="m5 12 4 4L19 6" />,
    clock: (
      <>
        <circle cx="12" cy="12" r="9" />
        <path d="M12 7v5l3 2" />
      </>
    ),
    service: (
      <>
        <path d="M4 7h16v12H4zM8 3v4M16 3v4M8 12h8M8 15h5" />
      </>
    ),
    card: (
      <>
        <rect x="3" y="5" width="18" height="14" rx="3" />
        <path d="M3 10h18M7 15h4" />
      </>
    ),
    rules: (
      <>
        <path d="M9 6h11M9 12h11M9 18h11" />
        <path d="m3 6 1 1 2-2M3 12l1 1 2-2M3 18l1 1 2-2" />
      </>
    ),
    x: (
      <>
        <path d="m6 6 12 12M18 6 6 18" />
      </>
    ),
    refresh: (
      <>
        <path d="M20 7v5h-5M4 17v-5h5" />
        <path d="M18.5 10A7 7 0 0 0 6 6.5L4 12M5.5 14A7 7 0 0 0 18 17.5l2-5.5" />
      </>
    ),
    chevron: <path d="m9 18 6-6-6-6" />,
  };
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.9"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden
    >
      {paths[name]}
    </svg>
  );
}


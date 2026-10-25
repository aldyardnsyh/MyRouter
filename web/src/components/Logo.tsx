// Logo variants from PRD §3. Icon-only for sidebar/favicon, horizontal for
// login cards. Uses currentColor so it adapts to light/dark.
export function LogoMark({ size = 28, mono = false }: { size?: number; mono?: boolean }) {
  return (
    <svg width={size} height={size} viewBox="0 0 48 48" fill="none" aria-hidden="true">
      <rect width="48" height="48" rx="12" fill={mono ? 'currentColor' : '#4F46E5'} />
      <path
        d="M10 24h10c5 0 5-12 12-12h4M20 24h16M20 24c5 0 5 12 12 12h4"
        stroke="#fff"
        strokeWidth="3"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
      <circle cx="38" cy="12" r="3" fill="#fff" />
      <circle cx="38" cy="24" r="3" fill="#fff" />
      <circle cx="38" cy="36" r="3" fill="#fff" />
    </svg>
  );
}

export function LogoWordmark({ size = 28 }: { size?: number }) {
  return (
    <span className="flex items-center gap-2">
      <LogoMark size={size} />
      <span className="text-[15px] tracking-[-0.01em] text-text" aria-label="MyRouter">
        <span className="font-semibold">My</span>
        <span className="font-bold">Router</span>
      </span>
    </span>
  );
}

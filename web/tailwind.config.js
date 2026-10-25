// Tailwind config derived from docs/design/tokens.json (A6 contract).
// Colors are exposed as CSS variables (see src/index.css) so light/dark soft
// are switched by the `data-theme` attribute without duplicating utilities.
// Values mirror the design tokens. They live here so the web build has no
// dependency on local-only design documents.
const tokens = {
  typography: { fontFamily: { sans: 'Plus Jakarta Sans', mono: 'JetBrains Mono' } },
  radius: { control: '8px', card: '12px', pill: '9999px' },
  shadow: { overlay: '0 8px 24px rgba(0,0,0,0.08)' },
  layout: { sidebarWidth: '232px', topbarHeight: '56px', contentMaxWidth: '1200px' },
};

/** @type {import('tailwindcss').Config} */
module.exports = {
  darkMode: ['class', '[data-theme="dark"]'],
  content: ['./index.html', './src/**/*.{ts,tsx}'],
  theme: {
    extend: {
      colors: {
        bg: 'rgb(var(--c-bg) / <alpha-value>)',
        surface: 'rgb(var(--c-surface) / <alpha-value>)',
        'surface-2': 'rgb(var(--c-surface-2) / <alpha-value>)',
        border: 'rgb(var(--c-border) / <alpha-value>)',
        text: 'rgb(var(--c-text) / <alpha-value>)',
        muted: 'rgb(var(--c-muted) / <alpha-value>)',
        accent: 'rgb(var(--c-accent) / <alpha-value>)',
        'accent-hover': 'rgb(var(--c-accent-hover) / <alpha-value>)',
        'accent-soft': 'rgb(var(--c-accent-soft) / <alpha-value>)',
        success: 'rgb(var(--c-success) / <alpha-value>)',
        'success-soft': 'rgb(var(--c-success-soft) / <alpha-value>)',
        warning: 'rgb(var(--c-warning) / <alpha-value>)',
        'warning-soft': 'rgb(var(--c-warning-soft) / <alpha-value>)',
        danger: 'rgb(var(--c-danger) / <alpha-value>)',
        'danger-soft': 'rgb(var(--c-danger-soft) / <alpha-value>)',
        info: 'rgb(var(--c-info) / <alpha-value>)',
        'info-soft': 'rgb(var(--c-info-soft) / <alpha-value>)',
      },
      fontFamily: {
        sans: [tokens.typography.fontFamily.sans, 'system-ui', 'sans-serif'],
        mono: [tokens.typography.fontFamily.mono, 'ui-monospace', 'monospace'],
      },
      fontSize: {
        display: ['28px', { lineHeight: '36px', letterSpacing: '-0.02em', fontWeight: '700' }],
        h2: ['20px', { lineHeight: '28px', letterSpacing: '-0.01em', fontWeight: '600' }],
        h3: ['16px', { lineHeight: '24px', fontWeight: '600' }],
        body: ['14px', { lineHeight: '20px', fontWeight: '400' }],
        small: ['12.5px', { lineHeight: '18px', fontWeight: '500' }],
        mono: ['13px', { lineHeight: '18px', fontWeight: '500' }],
      },
      borderRadius: {
        control: tokens.radius.control,
        card: tokens.radius.card,
        pill: tokens.radius.pill,
      },
      boxShadow: {
        overlay: tokens.shadow.overlay,
      },
      spacing: {
        sidebar: tokens.layout.sidebarWidth,
        topbar: tokens.layout.topbarHeight,
      },
      transitionDuration: { DEFAULT: '120ms' },
      maxWidth: { content: tokens.layout.contentMaxWidth },
      keyframes: {
        shimmer: { '100%': { transform: 'translateX(100%)' } },
        'fade-in': { from: { opacity: '0' }, to: { opacity: '1' } },
        'slide-in': { from: { transform: 'translateX(100%)' }, to: { transform: 'translateX(0)' } },
      },
      animation: {
        shimmer: 'shimmer 1.4s infinite',
        'fade-in': 'fade-in 120ms ease',
        'slide-in': 'slide-in 160ms ease',
      },
    },
  },
  plugins: [],
};

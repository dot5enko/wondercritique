# Frontend design system

State as of 2026-11-01 (Tailwind v4 rebuild — replaces the former dependency-free CSS system).

Tailwind-based design system in `apps/frontend/src/ui/`. Look: light-only, compact editor
density (13px UI base), rounded, teal accent, Linear-inspired. Icons come from
`lucide-react` — no hand-drawn SVGs anywhere in the codebase.

## Stack

- Tailwind CSS v4 (4.3.x) via `@tailwindcss/vite` plugin (registered in `vite.config.ts`)
- Fonts self-hosted: `@fontsource-variable/inter`, `@fontsource-variable/jetbrains-mono`
  (imported in `main.tsx`). Families: `'Inter Variable'`, `'JetBrains Mono Variable'`.
- `ui/styles.css` is the single CSS entry: `@import 'tailwindcss'` + `@theme` tokens +
  `@layer base` (body defaults, scrollbars, selection, reduced-motion). No other global CSS.

## Tokens (`@theme` in `ui/styles.css`)

| Group | Tokens |
|---|---|
| Fonts | `--font-sans` Inter Variable · `--font-mono` JetBrains Mono Variable |
| Type | `--text-base` = 13px (UI workhorse), `--text-2xs` = 11px; `text-xs` 12 / `text-sm` 14 stay default |
| Surfaces | `page #ebecee` (desktop margin) · `app #f6f7f8` (hover/base gray) · `sunken #f7f8f9` (sidebar) · `surface #ffffff` (cards/controls) |
| Lines | `line #e9eaec` · `line-strong #d5d8db` |
| Text | `ink #1b1f23` · `ink-2 #5d636b` · `ink-3 #9aa0a8` |
| Accent (teal, blue-leaning) | `accent #0889a0` · `accent-hover #067085` · `accent-ink #0a6274` · `accent-soft #e6f5f8` · `accent-muted #a7dde8` |
| Status | `danger`/`danger-soft` · `warning`/`warning-soft` · `success`/`success-soft` |
| Elevation | `shadow-pop` (hairline + soft drop for popovers) · `animate-pop` keyframe |
| Control heights | sm 24px · md 30px · lg 36px |

Dark theme: not implemented (light-only by design; tokens make a dark pass mechanical later).

## Files

| Path | Contents |
|---|---|
| `ui/styles.css` | Tailwind entry: `@theme` tokens + base layer |
| `ui/cx.ts` | `cx()` classnames joiner |
| `ui/Button.tsx` | `Button` (variants primary/secondary/soft/ghost/danger, sizes sm/md/lg, `icon`, `iconRight`, `loading`, `block`), `IconButton` (requires `label`), `Spinner`, `ButtonGroup` |
| `ui/Form.tsx` | `Field` (label/hint/error/optional; provides id + invalid via context, `useField`), `Input` (icon, trail, sizes, outline/filled), `Textarea` (`mono`), `Checkbox`, `RadioGroup`, `Switch`, `Segmented` |
| `ui/Select.tsx` | `Select` (custom listbox, keyboard nav ↑↓/Home/End/Enter/Esc, per-option icon/hint; outline/ghost variants), `Menu` + `Menu.Item`/`Menu.Label`/`Menu.Separator` (icons, shortcuts, danger items), `useDismiss` (outside click + Esc) |
| `ui/Display.tsx` | `Badge`, `Kbd`, `Shortcut`, `StatusDot`, `Avatar`, `Tooltip` (CSS-only, top/bottom/right), `Card` + `CardHeader/Body/Footer`, `Collapsible`, `Accordion` + `AccordionItem`, `Callout`, `Progress`, `Skeleton`, `EmptyState` |
| `ui/Sidebar.tsx` | `Sidebar` (header/body/footer), `SidebarSection`, `NavItem` (icon, label, active, trail) |
| `ui/index.ts` | barrel export |

## Conventions

- **Linear-style restraint**: monochrome gray chrome; hairline borders; flat surfaces —
  no drop shadows except popovers (`shadow-pop`), the sidebar active pill and switch knobs
  (`shadow-xs`); tight letter-spacing (`-0.011em` body). Color appears only in doses:
  primary buttons, focus rings, selected checks, progress, small badges.
- Component styling is Tailwind utilities only; colors always come from the semantic
  `@theme` tokens (`bg-app`, `border-line`, `text-ink-2`, `bg-accent-soft`…), never raw hex.
- Focus: UA outline reset globally in base; each control renders its own
  `focus-visible:ring-2 ring-accent/40` (text controls: `focus:border-accent` + soft ring).
- Expandable zones animate via `grid-rows-[0fr] ↔ [1fr]` (no JS measuring).
- Popovers: absolutely positioned under the trigger, `shadow-pop`, `animate-pop`
  (slight overshoot easing), closed by `useDismiss` (outside pointerdown + Escape).
- Motion: 100–150ms everywhere. Press = `active:scale-[0.98]` on buttons/triggers
  (`0.97` segmented, `0.90` checkbox/radio ticks, switch knob squish). Showcase panels
  enter with `animate-rise` (fade + 6px rise, keyed remount per section). Collapsibles
  animate `grid-rows-[0fr] ↔ [1fr]` plus content opacity. Reduced-motion disables all.
- Icon sizing inside buttons/nav is constrained with `[&_svg]:size-*`.

## App shell (showcase)

`App.tsx` renders a full-viewport frame inset 10px on every side (`p-2.5`), a rounded-xl
bordered container containing:

- `Sidebar` — nav over the element groups (Overview, Buttons, Input forms, Dropdowns,
  Cards, Expandable, Feedback — `SHOWCASE_NAV` in `showcase.tsx`) + a Workspace section;
  footer with StatusDot + Badge.
- Main column — scrollable `Showcase` panel (`showcase.tsx`) + bottom message composer:
  bordered rounded box with textarea, model `Select` (ghost variant), `Kbd` hint and
  primary Send button. Enter sends, Shift+Enter newline; `send` only clears the text —
  not wired to the backend.

# Design system — cinema-ticketing web

Captured from the only design source in the repository at the time of the sync:
`apps/web/src/app/globals.css` (Tailwind v4 theme layer plus the `@theme inline` mapping onto
the shadcn vocabulary). Every value below is taken from that file; nothing is invented.

The current palette is deliberately plain: it was built to validate behaviour (hold, payment,
ticket, door), not to look like a cinema. The three redesign directions in
`design/cinema-redesign/` propose a new palette and type personality on top of the same
spacing, radius and accessibility rules — see "Proposed direction" at the end.

## Colour — light (`prefers-color-scheme: light`)

| Token | Value | Usage |
| --- | --- | --- |
| `--color-bg` | `#f6f8fa` | page behind everything |
| `--color-surface` | `#ffffff` | cards, header, footer |
| `--color-surface-muted` | `#eaeff5` | secondary buttons, code, skeletons |
| `--color-text` | `#141c26` | body text (>= 16:1) |
| `--color-text-muted` | `#4b5561` | secondary text (>= 6.4:1) |
| `--color-border` | `#c6cfda` | hairlines |
| `--color-border-strong` | `#66727f` | control outlines |
| `--color-accent` | `#1b4fa8` | links and primary button background |
| `--color-accent-hover` | `#143d85` | primary hover |
| `--color-on-accent` | `#ffffff` | text on the accent |
| `--color-danger` | `#b3261e` | destructive button |
| `--color-danger-text` | `#b3261e` | destructive message text |
| `--color-neutral-bg` / `-fg` | `#e9edf2` / `#37414c` | neutral badge, alert |
| `--color-info-bg` / `-fg` | `#e7effd` / `#1b4fa8` | info badge, alert |
| `--color-success-bg` / `-fg` | `#e4f3e9` / `#0f5c28` | success badge, alert |
| `--color-warning-bg` / `-fg` | `#fdf3d7` / `#6f4300` | warning badge, alert |
| `--color-danger-bg` / `-fg` | `#fdecea` / `#b3261e` | error badge, alert |

Source: `apps/web/src/app/globals.css` (`:root`, `--color-*`).

## Colour — dark (`prefers-color-scheme: dark`)

| Token | Value |
| --- | --- |
| `--color-bg` | `#0d1117` |
| `--color-surface` | `#161b22` |
| `--color-surface-muted` | `#1f2630` |
| `--color-text` | `#eef2f7` |
| `--color-text-muted` | `#b0bac6` |
| `--color-border` | `#2c3540` |
| `--color-border-strong` | `#6d7987` |
| `--color-accent` | `#9dc1ff` |
| `--color-accent-hover` | `#c2d8ff` |
| `--color-on-accent` | `#0b1220` |
| `--color-danger` | `#c0322a` |
| `--color-danger-text` | `#ff9a92` |

Source: `apps/web/src/app/globals.css` (`@media (prefers-color-scheme: dark)`).
Note the inversion: in dark mode the accent is used as *text* and `--color-on-accent` is the
dark ink on the filled button.

## Typography

| Token | Value | Usage |
| --- | --- | --- |
| `--font-family-sans` | `system-ui, -apple-system, "Segoe UI", Roboto, Helvetica, Arial, sans-serif` | everything |
| `--font-family-mono` | `ui-monospace, "SF Mono", Menlo, Consolas, monospace` | codes, ticket ids |
| `--font-size-xs` | `0.75rem` | eyebrows, badges, metadata |
| `--font-size-sm` | `0.875rem` | hints, footer, secondary copy |
| `--font-size-base` | `1rem` | body, `h3` |
| `--font-size-lg` | `1.125rem` | `h2`, card titles |
| `--font-size-xl` | `1.375rem` | `h1` under 640px |
| `--font-size-2xl` | `1.75rem` | `h1` |
| `--font-size-3xl` | `2.125rem` | declared, unused today |
| `--line-height-tight` | `1.2` | headings |
| `--line-height-normal` | `1.5` | prose |
| `--font-weight-normal` / `-medium` / `-bold` | `400` / `500` / `600` | three weights only |

Source: `apps/web/src/app/globals.css` (`:root` + base element rules for `h1`–`h3`, `body`, `a`, `code`).
No display, serif or condensed family is declared anywhere: adding one is a design decision, not a sync.

## Spacing, radius, elevation, layout

| Group | Tokens |
| --- | --- |
| Spacing | `--space-1` `0.25rem` · `-2` `0.5rem` · `-3` `0.75rem` · `-4` `1rem` · `-5` `1.5rem` · `-6` `2rem` · `-7` `3rem` · `-8` `4rem` |
| Radius | `--radius-sm` `0.375rem` · `--radius-md` `0.5rem` (the controlled `--radius` used by shadcn) · `--radius-lg` `0.75rem` · `--radius-pill` `999px` |
| Elevation | `--shadow-1` `0 1px 2px rgb(16 24 32 / 8%)` · `--shadow-2` `0 4px 16px rgb(16 24 32 / 12%)` (dark mode: `40%` / `50%` black) |
| Layout | `--layout-max-width` `64rem` · `--layout-gutter` `var(--space-5)`, `var(--space-4)` under 640px · `--measure` `74ch` |
| Focus | `--focus-ring-color` = accent · `--focus-ring-width` `2px` · `--focus-ring-offset` `2px`, drawn on `:focus-visible` only |

Source: `apps/web/src/app/globals.css` (`:root`, `@media (max-width: 640px)`).

## Component rules

- **Button** (`.btn`): three variants — `--primary` (accent fill, on-accent text), `--secondary`
  (surface fill, `--color-border-strong` outline), `--danger`. Radius `--radius-sm`, padding
  `--space-2 --space-4`, weight bold, tight line height. A busy button keeps its width and sets
  `aria-busy`; disabled is announced by the platform, never by colour alone. Source: `.btn*`.
- **Card** (`.card`): surface fill, `--color-border` hairline, `--radius-lg`, padding `--space-5`,
  `--shadow-1`. Variants `--muted` (muted surface, no shadow) and `--danger`. Source: `.card*`.
- **Badge / status pill** (`.badge`): the label *is* the state; tone only reinforces it and the
  leading glyph is `aria-hidden`. Pairs must stay >= 5.6:1. Source: `.badge*`.
- **Field** (`.field`): label + control + hint + error, wired with `htmlFor`, `aria-describedby`
  and `aria-invalid`; the invalid control thickens its border instead of shifting the layout.
  Source: `.field*`.
- **Alert** (`.alert`): a tone, a 4px leading border, a glyph and a heading — four signals, one of
  which is colour. Source: `.alert*`.
- **Empty state** (`.empty-state`): dashed `--color-border-strong` outline on the muted surface.
- **Skeleton** (`.skeleton`): muted block with a 1.6s pulse, disabled under
  `prefers-reduced-motion`. Never announced by assistive technology.
- **Shell**: `.site-header` (surface, hairline, `--shadow-1`) with `.brand` on the left and a
  wrapping `.site-nav`; `.site-footer`; every page renders `<main id="main-content" class="foundation">`,
  capped at `--layout-max-width`, stacked with `--space-5` gaps.
- **Page header** (`.page-header`): eyebrow (uppercase, `0.08em` tracking, xs, bold) + `h1` +
  description capped at `--measure` + an action row that wraps.

Source: every rule named above lives in `apps/web/src/app/globals.css`; the React surface is
`apps/web/src/components/` and `apps/web/src/components/ui/` (shadcn vocabulary aliased onto the
same tokens by the `@theme inline` block).

## Constraints the redesign must keep

1. Self-contained styling: no external URL, CDN, remote font or web image (the app never reaches
   the network for styling, and `components.test.ts` pins "no hex outside `globals.css`").
2. Colour never carries a state alone: a status always spells itself out in words plus a glyph.
3. AA contrast in both schemes, visible `:focus-visible` ring, `prefers-reduced-motion` respected.
4. One spacing scale, one radius scale, three weights — no invented 13px margins.
5. Interface copy for the redesign is Castilian Spanish (`cartelera`, `butacas`, `entrada`, `taquilla`,
   `acceso`, amounts as `12,50 €`). The shipped app still carries English strings: translating them is
   part of implementing the chosen direction, not a separate feature.

## Proposed direction (not yet part of the system)

The three artboards under `design/cinema-redesign/` propose, per direction, a new colour
personality and a display face for headings. Until one is chosen and merged into `globals.css`,
`design/system.md` keeps describing the shipped system, and each artboard lists its own
deviations in its `tradeoff` and in the comment block at the top of the file.

# Frontend Specification — Base48 Member Portal

Source of truth for all frontend decisions. When adding or modifying UI, follow these rules.

## Architecture

- **CSS framework**: Tailwind CSS **v3.4.17**, Play CDN, vendored at `web/static/js/tailwind.js` and loaded in `layout.html`. `darkMode` is set to `'class'`.
- **Theme tokens + dark mode**: `web/static/css/theme.css` — the palette, and the dark-mode remap. Loaded **before** `admin.css`.
- **Shared component CSS**: `web/static/css/admin.css` — buttons, badges, modals, navigation, text utilities
- **Page-specific CSS**: Inline `<style>` blocks in templates — only for styles unique to that page
- **Navigation behaviour**: `web/static/js/nav.js` — menus, theme switch, active link. Vanilla, no dependencies.
- **Templates**: Go `html/template`, located in `web/templates/`

### Rule: No CSS duplication

If a style is used on more than one page, it belongs in `admin.css`. Page-specific `<style>` blocks must only contain styles unique to that template.

### Rule: no literal colours

Use a token from the palette below, never a hex. A literal hex is light-mode-only
and will be unreadable in dark mode. The two deliberate exceptions are documented
under Theming.

## Theming

Two themes, one palette. `theme.css` declares every colour as a custom property
on `:root`, then redeclares the same names on `html.dark`. Components consume the
token and are themed automatically — **no component needs a `.dark` rule**.

The theme lives in `localStorage` under `base48-theme` (`light` | `dark`, absent =
follow the OS). An inline script at the top of `<head>` applies it before the
first paint; `nav.js` owns everything after that, including the three-way switch
in the account menu and cross-tab sync.

### Tokens

| Token | Light | Dark | Use |
|---|---|---|---|
| `--surface` | `#ffffff` | `#171a21` | cards, nav, footer, modals |
| `--surface-raised` | `#ffffff` | `#1e222b` | dropdowns, popovers, inputs |
| `--surface-sunken` | `#f9fafb` | `#0f1115` | page ground, table headers |
| `--surface-muted` | `#f3f4f6` | `#222732` | chips, tracks, **hover fills** |
| `--line-soft` / `--line` / `--line-strong` | `#f3f4f6` / `#e5e7eb` / `#d1d5db` | `#21262f` / `#2a303b` / `#3a4150` | gridlines / borders / input borders |
| `--ink` / `--ink-2` / `--ink-3` / `--ink-4` | `#111827` / `#374151` / `#6b7280` / `#9ca3af` | `#e8eaee` / `#cbd1db` / `#99a1b0` / `#7f8797` | primary / secondary / meta / disabled |
| `--accent-{red,green,blue,indigo,amber,purple}` | 600-step | 300–400-step | text and icon accents |
| `--tint-{colour}-bg` / `-fg` | pastel + dark ink | hue wash + light ink | badges, alert panels |
| `--money-positive` / `--money-negative` | `#4caf50` / `#f44336` | `#4ade80` / `#f87171` | balances |

Hover fills go **up** a surface step in dark mode, never down: a row that darkens
under the cursor reads as disabled.

### How dark mode reaches Tailwind utilities

Most pages are utility-built, so `theme.css` re-points the ~80 colour utilities
actually in use (`.dark .bg-white { background-color: var(--surface) }`, and so
on) rather than asking every template to carry a `dark:` twin for each of ~1200
usages. Consequences worth knowing:

1. **Every override must keep its `.dark` prefix.** The Play CDN injects its
   stylesheet at runtime and may land after `theme.css`; the extra class is what
   guarantees the override wins regardless of load order.
2. **New markup gets dark mode for free** as long as it stays on the palette
   above. Reach for a `dark:` variant only for something genuinely one-off.
3. `theme.css` loads **before** `admin.css`, so components win the border-colour
   tie against the `.dark *` preflight reset.

### Deliberately not themed

`.barcode-card` and `.bar-guide` are printed on white paper. They set all their
own colours, carry no Tailwind colour utility inside, and must stay light in both
themes. Do not tokenise them.

### Charts

The sequential blue ramp (`.bar-seq-*`, `.bar-coh-*`) is **rebuilt, not
re-tinted**, for dark: light→saturated becomes dark→bright, so the loudest step
still means the largest value. The `ink-light` class is set in Go and means "this
cell is at the saturated end of the ramp"; because the dark ramp is reversed that
end is the bright one, so the same class lands on the right cells in both themes
and no Go change is needed.


## Color Palette

The hexes below are the **light** values. Badge and text colours are now
consumed through the tokens in Theming above (`--tint-*`, `--money-*`,
`--ink-*`); this table stays as the reference for what those tokens resolve
to in light mode. Button fills are solid accents with white labels and are
intentionally identical in both themes.

### Buttons (solid backgrounds)

| Token           | Hex       | Usage                                    |
|-----------------|-----------|------------------------------------------|
| blue-primary    | `#2196F3` | Primary actions (`.btn-primary`)         |
| gray-primary    | `#6b7280` | Secondary/view actions (`.btn-secondary`, `.btn-view`) |
| red-primary     | `#f44336` | Destructive actions (`.btn-danger`)      |
| gray-dark       | `#4b5563` | Hover states for gray buttons            |

### Badges (pastel backgrounds, dark text)

| Variant          | Background | Text      | Usage                               |
|------------------|------------|-----------|---------------------------------------|
| green            | `#dcfce7`  | `#166534` | Success, accepted, active_member      |
| red              | `#fee2e2`  | `#991b1b` | Danger, suspended, memberportal_admin |
| yellow           | `#fef3c7`  | `#92400e` | Warning, awaiting                     |
| blue             | `#dbeafe`  | `#1e40af` | Info, roles (default), council_member |
| orange           | `#ffedd5`  | `#9a3412` | in_debt role                          |
| gray             | `#f3f4f6`  | `#6b7280` | Rejected, exmember, disabled          |
| purple           | `#ede9fe`  | `#5b21b6` | Special/sync badges                   |
| indigo           | `#e0e7ff`  | `#3730a3` | Fallback for unknown roles            |

### Text utilities

| Token           | Hex       | Usage                                    |
|-----------------|-----------|------------------------------------------|
| text-negative   | `#f44336` | Negative balance (bold)                  |
| text-positive   | `#4CAF50` | Positive balance                         |
| text-muted      | `#999`    | Placeholder / empty text                 |

## Components

### Badges (`.badge`)

Pill-shaped, pastel-background badges. Used for status indicators, roles, tags.
All badges use the same base class; never use inline Tailwind for badge styling.

```css
.badge {
    display: inline-flex;
    align-items: center;
    padding: 2px 10px;
    border-radius: 9999px;   /* pill shape */
    font-size: 12px;
    font-weight: 500;
    white-space: nowrap;      /* NEVER wraps */
}
```

**Semantic variants** (status indicators):
- `.badge-success` — green pastel (enabled, active, OK)
- `.badge-danger` — red pastel (disabled, error, suspended)
- `.badge-warning` — yellow pastel (not linked, awaiting)

**State variants** (member states — used with `badge-{{ .State }}`):
- `.badge-accepted` — green
- `.badge-awaiting` — yellow
- `.badge-suspended` — red
- `.badge-rejected` — gray
- `.badge-exmember` — gray

**Role variants** (used with `badge-role badge-role-{{ .RoleName }}`):
- `.badge-role` — blue (fallback for unknown roles)
- `.badge-role-active_member` — green
- `.badge-role-memberportal_admin` — red
- `.badge-role-council_member` — blue
- `.badge-role-in_debt` — orange

**Generic color variants** (for misc use):
- `.badge-gray`, `.badge-yellow`, `.badge-red`, `.badge-blue`, `.badge-green`, `.badge-orange`, `.badge-purple`, `.badge-indigo`

**Usage patterns:**

State badge (auto-colored by state name):
```html
<span class="badge badge-{{ .DBUser.State }}">{{ .DBUser.State }}</span>
```

Role badges (auto-colored by role name, with fallback):
```html
<div class="badge-group">
    {{ range .Roles }}
    <span class="badge badge-role badge-role-{{ . }}">{{ . }}</span>
    {{ end }}
</div>
```

**Badge groups** — always wrap multiple badges in `.badge-group`:
```css
.badge-group {
    display: flex;
    flex-wrap: wrap;
    gap: 4px;
    align-items: center;
}
```

### Buttons (`.btn`)

```
.btn {
    padding: 6px 12px;
    border: none;
    border-radius: 4px;
    cursor: pointer;
    font-size: 12px;
    font-weight: 500;
    text-decoration: none;
    display: inline-block;
}
```

**Sizes:**
- Default: `padding: 6px 12px; font-size: 12px;`
- `.btn-sm`: `padding: 4px 8px; font-size: 11px;`

**Variants:**
- `.btn-primary` — blue (`#2196F3`), primary actions
- `.btn-secondary` — gray (`#6b7280`), cancel/clear actions
- `.btn-danger` — red (`#f44336`), destructive actions
- `.btn-view` — gray (`#6b7280`), "view" and other neutral row-level actions
- `.btn-quiet` — grey tint (`--tint-gray-*`), for a row action that *records* something rather than destroys it

**Rule: Action buttons in tables must be visually consistent.** All row-level action buttons (Zobrazit etc.) use `.btn .btn-sm .btn-view`.

**Rule: reserve `.btn-danger` for actually destructive actions.** A solid red button
outshouts every badge on its row. Something that merely records a fact — "Return
Keys" — belongs in `.btn-quiet`, which sits one notch *below* the `in_debt` and
`memberportal_admin` badges beside it rather than above them.

### Modals (`.modal`)

Standard pattern for popup dialogs:

```html
<div id="myModal" class="modal" style="display:none;">
    <div class="modal-content">
        <span class="close" onclick="closeMyModal()">&times;</span>
        <h2>Title</h2>
        <!-- content -->
    </div>
</div>
```

### Tabs (`.tabs`)

A horizontal strip of underlined links that switch between views of one list —
the bar sub-pages, and the user-list presets. Mark the current one with
`class="active"` plus `aria-current="page"`.

```html
<div class="tabs">
    <a href="?view=awaiting">Čekající <span class="text-muted">3</span></a>
    <a href="?view=debt" class="active" aria-current="page">Dlužníci <span class="text-muted">7</span></a>
</div>
```

(Named `.bar-tabs` until it grew a second caller; it was never bar-specific.)

### Tables

Admin list pages use custom `.some-table` classes with consistent structure:
- Green header (`#4CAF50`) for main admin tables (admin_users)
- Gray header (`#f9fafb`) for detail/sub-tables
- `border-collapse: collapse`, `1px solid #ddd` borders

### Text Utilities

- `.text-negative` — red, bold (negative balances)
- `.text-positive` — green (positive balances)
- `.text-muted` — gray `#999` (empty/placeholder text)
- `.text-link` — styled link within tables

### Navigation (`layout.html` + `.nav-*`)

The bar is: brand, primary links, and one account control on the right.

- **Account menu** — an avatar button (`.nav-avatar`, initials from the `initials`
  template func; tinted red for admins) opening `.nav-menu`: name, e-mail, role
  badges, profile/services links, the theme switch, and logout as the only
  destructive item. It replaced an always-visible name/e-mail/role block plus a
  bare "Logout" text link sitting flush against the nav.
- **Správa menu** — same component, opened by click rather than hover so it works
  on touch and from the keyboard.
- **Both menus** are driven by `data-menu` / `data-menu-button` / `data-menu-panel`
  and get, for free: `aria-expanded`, Escape-to-close with focus returned to the
  trigger, click-outside and focus-out closing, and Up/Down arrow roving. Adding
  a third menu means adding those three attributes and nothing else.
- **Active link** — `nav.js` sets `aria-current="page"` on the longest matching
  `href` inside any `[data-nav-links]` container, so `/admin/bar/cards` marks
  "Bar" and not every shorter prefix. The script is loaded synchronously right
  after `</nav>`, so the state is set before the first paint.
- **Mobile** — one panel holding identity, links, theme and logout. The old
  second nav row that repeated the user's name and e-mail is gone.
- **Theme switch** — `.theme-switch`, a three-way segmented control (Systém /
  Světlý / Tmavý) rendered by the `themeSwitch` template. It appears up to three
  times per page and `nav.js` keeps every copy's `aria-pressed` in sync. The
  panel deliberately stays open while switching, so the two themes can be
  compared in place.


## Guidelines

1. **Badges never wrap.** Always use `white-space: nowrap`.
2. **Multiple badges need a `.badge-group` wrapper** for consistent spacing.
3. **Table action buttons must be consistent.** Use the same variant for all row-level actions.
4. **Shared styles live in `admin.css`.** Don't redefine `.btn`, `.badge`, `.modal` in templates.
5. **Page-specific styles stay in templates.** Filter forms, custom tables, project-specific layouts.
6. **Tailwind is available** for layout and one-off styling. Prefer it for pages that don't need custom components (logs, settings, profile).
7. **Never write a literal colour.** Use a token; see Theming. New pages then work in both themes with no extra effort.
8. **Scanning tables stay dense.** `padding: 3px 8px`, `font-size: 13px`, and
   `white-space: nowrap` on every cell, so a member with three role badges is no
   taller than one with none. `main :has(> table)` in `admin.css` already gives
   the wrapper horizontal scroll, so nowrap costs nothing. `admin_users.html` is
   the reference.
9. **Filters: presets first.** Give the three or four lists people actually ask
   for as `.tabs`, each with a count, and collapse the full filter form into a
   `<details>` that only opens when the current filters are off-preset. Define
   the presets once in Go (see `userViews`) so the tabs and the filtering cannot
   disagree.
10. **Check both themes** before shipping a page. The fastest check is the account menu's theme switch — it does not reload.

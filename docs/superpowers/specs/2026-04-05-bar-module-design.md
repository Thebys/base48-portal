# Bar Module — Design Spec

**Date:** 2026-04-05
**Status:** Approved
**Scope:** Port ncgen barcode card generator + bar guides into Base48 member portal

## Background

Base48 hackerspace runs a self-service bar using RevBank (Perl-based prepaid tab system). Members scan barcode cards to identify themselves at the terminal. The ncgen project provides a standalone HTML barcode card generator and printable bar guides. The bar has proven commercially viable (~20k CZK turnover in a short period).

The portal already integrates with RevBank (account sync, transaction history, sales dashboard). This spec adds barcode card generation and bar operation guides as a native portal feature.

## Architecture

### Approach: Hybrid (client-side barcodes, server-side data)

- Go backend provides data (member lists, auth, routing) and serves HTML templates
- JsBarcode (CDN, inline script tag — same pattern as Tailwind) generates CODE128 barcodes in the browser
- Print-optimized CSS (`@media print`) for direct browser printing
- No new Go dependencies required

### Routing

All bar functionality unified under `/admin/bar/`:

| Route | Handler | Purpose |
|---|---|---|
| `GET /admin/bar` | `handleAdminBar` | Dashboard (migrated from `/admin/revbank`) |
| `GET /admin/bar/cards` | `handleAdminBarCards` | Member card generator |
| `GET /admin/bar/guides` | `handleAdminBarGuides` | Guide listing page |
| `GET /admin/bar/guides/buy` | `handleAdminBarGuideBuy` | "Jak nakupovat" printable guide |
| `GET /admin/bar/guides/deposit` | `handleAdminBarGuideDeposit` | "Jak dobíjet" printable guide |

API endpoint migration:

| Old | New |
|---|---|
| `POST /api/revbank/sync` | `POST /api/bar/sync` |

Navigation: "Bar" in admin menu replaces "RevBank". Sub-navigation tabs on all `/admin/bar/*` pages: **Dashboard** | **Cards** | **Guides**.

### Database

No schema changes required. Card generation uses existing `users` table (username, state) and `revbank_accounts` table.

## Feature: Member Card Generator (`/admin/bar/cards`)

### Card Design: Clean Base48

Credit card size (85.6 x 53.98 mm), white background, print-friendly:

- **Header:** "BASE48" (bold, letter-spaced) | "HACKERSPACE BRNO" (subtle right)
- **Separator:** 2px solid line (#1a1a2e)
- **Nick section:** "member id" label (small, gray) + username (large, bold, #1a1a2e)
- **Barcode:** CODE128 SVG, full width, encoding the username (no trailing newline — scanner adds Enter automatically)
- **Username text** below barcode (small, monospace)
- **Footer:** "REVBANK TERMINAL" left | "SCAN TO PAY" right
- **Corner marks:** decorative crop marks in corners
- Color palette: white background, #1a1a2e (dark navy) for text/borders
- Fonts: monospace system font (no external font dependency for print reliability)

### Page UI

- Tab navigation: Dashboard | **Cards** (active) | Guides
- Table of active members with columns: checkbox, username, display name, membership state
- Action buttons above table:
  - **"Tisk všech aktivních"** — generates cards for all active members
  - **"Tisk vybraných"** — generates cards for checked members only
- Clicking a row shows single card preview with print button
- Print view: 2-column grid on A4, `@media print` hides UI chrome, shows only cards with crop marks

### Data Flow

1. Go handler queries active members from DB (`users` where state = 'accepted')
2. Template renders member table + card generation area
3. JsBarcode generates CODE128 SVG barcodes client-side on button click
4. `window.print()` → browser prints card grid

## Feature: Bar Guides (`/admin/bar/guides`)

### Listing Page (`/admin/bar/guides`)

Simple page with two guide cards linking to the printable versions. Each shows title, description, and "Otevřít / Tisk" button.

### Guide: "Jak nakupovat" (`/admin/bar/guides/buy`)

A4 printable page, Czech language. Two-step flow:

1. **Pípni zboží** — scan product barcode (one or more)
2. **Pípni sebe** — identify yourself:
   - Kartička (scan member barcode) — marked as fastest
   - Klávesnice (type nickname + Enter)
   - Guest účet (scan "guest" barcode)

**Rescue section:** barcodes for `abort` (cancel) and `undo` (revert last transaction)

**Tip:** balance check by scanning card without prior command, deposit guide reference.

### Guide: "Jak dobíjet" (`/admin/bar/guides/deposit`)

A4 printable page, Czech language. Simplified flow using **sequential barcodes**:

1. **Storno / Reset** — scan `abort` barcode (safety reset)
2. **Vyber částku** — scan one of:
   - 100 Kč → barcode encodes `deposit\n100\ncash`
   - 200 Kč → barcode encodes `deposit\n200\ncash`
   - 500 Kč → barcode encodes `deposit\n500\ncash`
   - 1000 Kč → barcode encodes `deposit\n1000\ncash`
3. **Pípni sebe** — scan member card / type nickname / scan guest
4. **Vlož peníze do pokladny**

This reduces the deposit flow from 5+ steps (scan deposit → scan amount → scan cash → scan identity → insert money) to 3 steps (scan amount-combo → scan identity → insert money), with only 2 barcode scans.

### Sequential Barcode Encoding (Experimental)

**This feature requires physical validation with the bar's barcode scanner.** CODE128 supports encoding control characters including newline (0x0A), but scanner behavior with embedded newlines varies by model. Implementation should include a test barcode on the guides page so admins can verify before relying on it. Fallback: if sequential barcodes don't work with the scanner, deposit guide reverts to the original multi-step flow (separate barcodes for each command).

The string `deposit\n500\ncash` is sent by the scanner as:
- `deposit` + Enter keystroke
- `500` + Enter keystroke
- `cash` + Enter keystroke (scanner adds final Enter)

RevBank's readline processes each line sequentially from the input buffer:
1. "deposit" → activates deposit plugin
2. "500" → parsed as amount by deposit's amount callback
3. "cash" → confirms cash payment method

### Guide Design

- A4 portrait, print-optimized
- Large step numbers (Orbitron-style bold)
- Clear visual hierarchy with borders and arrows
- Barcodes rendered as SVG via JsBarcode (scalable for A0 plotter)
- `@media print` CSS for clean output
- `@media screen` adds subtle border for on-screen preview
- Design aligned with Base48 visual identity (same color palette as cards)

## Migration: RevBank → Bar

### URL Migration

- `/admin/revbank` → `/admin/bar` (existing dashboard moves)
- `/api/revbank/sync` → `/api/bar/sync` (API endpoint moves)
- Navigation label: "RevBank" → "Bar"

### Template Migration

- `admin_revbank.html` → `admin_bar.html` (renamed, tab navigation added)
- New templates: `admin_bar_cards.html`, `admin_bar_guides.html`, `admin_bar_guide_buy.html`, `admin_bar_guide_deposit.html`

### Handler Migration

- Existing RevBank handlers renamed/moved under bar namespace
- New handlers for cards and guides added
- RevBank API key auth middleware unchanged (just different route path)

### Backward Compatibility

Old URLs (`/admin/revbank`, `/api/revbank/sync`) get permanent redirects (301) to new paths. This ensures the kiosk sync script (`contrib/revbank-sync.sh`, line 199: `${PORTAL_URL}/api/revbank/sync`) keeps working immediately. The script itself will be updated to use `/api/bar/sync` as part of this work, but redirects stay as a safety net for any other consumers.

## Files Affected

### New Files
- `web/templates/admin_bar_cards.html`
- `web/templates/admin_bar_guides.html`
- `web/templates/admin_bar_guide_buy.html`
- `web/templates/admin_bar_guide_deposit.html`

### Modified Files
- `web/templates/admin_revbank.html` → rename to `admin_bar.html`, add tab navigation
- `web/templates/layout.html` — nav menu: "RevBank" → "Bar"
- `cmd/server/main.go` — route registration (new routes, old redirects)
- `internal/handler/` — new handler functions, rename existing revbank handlers
- `web/static/css/admin.css` — card print styles, guide styles
- `contrib/revbank-sync.sh` — update URL

### Static Assets
- JsBarcode loaded via CDN script tag (no local file needed)

## Out of Scope

- Product management UI in portal
- RevBank bidirectional sync
- PDF export (browser print is sufficient)
- Member self-service card generation (admin-only for now)
- Label printer integration
- Card design customization UI

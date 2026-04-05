# Bar Module Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Port ncgen barcode card generator and bar guides into the member portal, unifying all bar functionality under `/admin/bar/`.

**Architecture:** Hybrid approach — Go backend serves data and handles auth/routing, JsBarcode CDN generates CODE128 barcodes client-side. Existing RevBank dashboard migrates from `/admin/revbank` to `/admin/bar`, with new pages for card generation and printable guides. 301 redirects preserve backward compatibility.

**Tech Stack:** Go (chi router, html/template, sqlc), JsBarcode CDN (CODE128), Tailwind CSS, `@media print` CSS

**Spec:** `docs/superpowers/specs/2026-04-05-bar-module-design.md`

---

## File Structure

### New Files
| File | Responsibility |
|---|---|
| `web/templates/admin_bar.html` | Bar dashboard (migrated from `admin_revbank.html`, adds tab navigation) |
| `web/templates/admin_bar_cards.html` | Member card generator page |
| `web/templates/admin_bar_guides.html` | Guide listing page |
| `web/templates/admin_bar_guide_buy.html` | "Jak nakupovat" printable guide |
| `web/templates/admin_bar_guide_deposit.html` | "Jak dobíjet" printable guide |
| `internal/handler/bar.go` | All bar handler functions (migrated + new) |

### Modified Files
| File | Changes |
|---|---|
| `internal/db/queries.sql` | New query for accepted users with username |
| `cmd/server/main.go` | New routes under `/admin/bar/*`, redirects for old URLs |
| `web/templates/layout.html` | Nav menu: "RevBank" → "Bar" with link to `/admin/bar` |
| `web/static/css/admin.css` | Card print styles, tab navigation, guide print styles |
| `contrib/revbank-sync.sh` | Update API URL to `/api/bar/sync` |

### Deleted Files
| File | Reason |
|---|---|
| `web/templates/admin_revbank.html` | Replaced by `admin_bar.html` |
| `internal/handler/revbank.go` | Replaced by `bar.go` |

---

## Task 1: Add SQL query for accepted users with username

**Files:**
- Modify: `internal/db/queries.sql` (append new query)
- Regenerate: `internal/db/queries.sql.go` (via `make sqlc`)

- [ ] **Step 1: Add the new query to queries.sql**

Append to the end of `internal/db/queries.sql`:

```sql
-- name: ListAcceptedUsersWithUsername :many
SELECT id, username, realname, email, state
FROM users
WHERE state = 'accepted' AND username IS NOT NULL AND username != ''
ORDER BY username;
```

- [ ] **Step 2: Regenerate sqlc code**

Run: `make sqlc`
Expected: Clean generation, no errors. New function `ListAcceptedUsersWithUsername` appears in `internal/db/queries.sql.go`.

- [ ] **Step 3: Verify the generated code**

Run: `grep -n "ListAcceptedUsersWithUsername" internal/db/queries.sql.go`
Expected: Function definition and const query string found.

- [ ] **Step 4: Build to verify compilation**

Run: `make build`
Expected: Clean build, no errors.

- [ ] **Step 5: Commit**

```bash
git add internal/db/queries.sql internal/db/queries.sql.go
git commit -m "feat(bar): add query for accepted users with username"
```

---

## Task 2: Migrate handler from revbank.go to bar.go

Rename the file and update all function names and references. No logic changes yet.

**Files:**
- Create: `internal/handler/bar.go` (copy of `revbank.go` with renames)
- Delete: `internal/handler/revbank.go`

- [ ] **Step 1: Copy revbank.go to bar.go and rename functions**

Copy `internal/handler/revbank.go` to `internal/handler/bar.go`. In the new file, rename:

| Old | New |
|---|---|
| `RequireRevbankAPIKey` | `RequireBarAPIKey` |
| `RevbankSyncHandler` | `BarSyncHandler` |
| `AdminRevbankHandler` | `AdminBarHandler` |
| `isValidRevbankUsername` | `isValidBarUsername` |

Also update:
- The template reference: `h.render(w, "admin_revbank.html", data)` → `h.render(w, "admin_bar.html", data)`
- The `"Title"` in data map: `"RevBank"` → `"Bar"`
- Log prefixes: `[RevBank]` → `[Bar]`
- Subsystem in log entries: `"revbank"` → `"bar"`
- Comment text referencing RevBank → Bar where appropriate

Keep all settings keys unchanged (`revbank_last_sync`, `revbank_system_accounts`) — these are stored data, not code identifiers.

- [ ] **Step 2: Add new handler stubs for cards and guides**

Append to `internal/handler/bar.go`:

```go
// AdminBarCardsHandler renders the barcode card generator page.
func (h *Handler) AdminBarCardsHandler(w http.ResponseWriter, r *http.Request) {
	user := h.auth.GetUser(r)
	ctx := r.Context()

	adminDBUser, _ := h.queries.GetUserByKeycloakID(ctx, sql.NullString{
		String: user.ID,
		Valid:  true,
	})

	members, err := h.queries.ListAcceptedUsersWithUsername(ctx)
	if err != nil {
		log.Printf("[Bar] Failed to list members for cards: %v", err)
	}

	data := map[string]interface{}{
		"Title":   "Bar — Kartičky",
		"User":    user,
		"DBUser":  adminDBUser,
		"Members": members,
	}

	h.render(w, "admin_bar_cards.html", data)
}

// AdminBarGuidesHandler renders the guides listing page.
func (h *Handler) AdminBarGuidesHandler(w http.ResponseWriter, r *http.Request) {
	user := h.auth.GetUser(r)

	adminDBUser, _ := h.queries.GetUserByKeycloakID(r.Context(), sql.NullString{
		String: user.ID,
		Valid:  true,
	})

	data := map[string]interface{}{
		"Title":  "Bar — Návody",
		"User":   user,
		"DBUser": adminDBUser,
	}

	h.render(w, "admin_bar_guides.html", data)
}

// AdminBarGuideBuyHandler renders the "Jak nakupovat" printable guide.
func (h *Handler) AdminBarGuideBuyHandler(w http.ResponseWriter, r *http.Request) {
	user := h.auth.GetUser(r)

	adminDBUser, _ := h.queries.GetUserByKeycloakID(r.Context(), sql.NullString{
		String: user.ID,
		Valid:  true,
	})

	data := map[string]interface{}{
		"Title":  "Jak nakupovat",
		"User":   user,
		"DBUser": adminDBUser,
	}

	h.render(w, "admin_bar_guide_buy.html", data)
}

// AdminBarGuideDepositHandler renders the "Jak dobíjet" printable guide.
func (h *Handler) AdminBarGuideDepositHandler(w http.ResponseWriter, r *http.Request) {
	user := h.auth.GetUser(r)

	adminDBUser, _ := h.queries.GetUserByKeycloakID(r.Context(), sql.NullString{
		String: user.ID,
		Valid:  true,
	})

	data := map[string]interface{}{
		"Title":  "Jak dobíjet",
		"User":   user,
		"DBUser": adminDBUser,
	}

	h.render(w, "admin_bar_guide_deposit.html", data)
}
```

- [ ] **Step 3: Delete old revbank.go**

```bash
rm internal/handler/revbank.go
```

- [ ] **Step 4: Verify compilation fails as expected**

Run: `make build`
Expected: Build fails — `main.go` still references old handler names. This is expected, we fix it in Task 3.

---

## Task 3: Update routes and navigation

**Files:**
- Modify: `cmd/server/main.go` (routes)
- Modify: `web/templates/layout.html` (nav menu)

- [ ] **Step 1: Update routes in main.go**

In `cmd/server/main.go`, replace the RevBank route registrations.

Replace the API route block (lines ~118-120):
```go
r.Route("/api/revbank", func(r chi.Router) {
    r.Post("/sync", h.RequireRevbankAPIKey(h.RevbankSyncHandler))
})
```

With:
```go
// Bar API (RevBank kiosk sync)
r.Route("/api/bar", func(r chi.Router) {
    r.Post("/sync", h.RequireBarAPIKey(h.BarSyncHandler))
})
// Backward compat redirect
r.Post("/api/revbank/sync", func(w http.ResponseWriter, r *http.Request) {
    http.Redirect(w, r, "/api/bar/sync", http.StatusPermanentRedirect)
})
```

Replace the admin route (line ~134):
```go
r.Get("/admin/revbank", h.RequireAdmin(h.AdminRevbankHandler))
```

With:
```go
// Bar admin pages
r.Get("/admin/bar", h.RequireAdmin(h.AdminBarHandler))
r.Get("/admin/bar/cards", h.RequireAdmin(h.AdminBarCardsHandler))
r.Get("/admin/bar/guides", h.RequireAdmin(h.AdminBarGuidesHandler))
r.Get("/admin/bar/guides/buy", h.RequireAdmin(h.AdminBarGuideBuyHandler))
r.Get("/admin/bar/guides/deposit", h.RequireAdmin(h.AdminBarGuideDepositHandler))
// Backward compat redirect
r.Get("/admin/revbank", func(w http.ResponseWriter, r *http.Request) {
    http.Redirect(w, r, "/admin/bar", http.StatusMovedPermanently)
})
```

- [ ] **Step 2: Update navigation in layout.html**

In `web/templates/layout.html`, replace both occurrences of the RevBank link.

Desktop nav (around line 58):
```html
<a href="/admin/revbank" class="block px-4 py-2 text-sm text-gray-700 hover:bg-gray-100">RevBank</a>
```
→
```html
<a href="/admin/bar" class="block px-4 py-2 text-sm text-gray-700 hover:bg-gray-100">Bar</a>
```

Mobile nav (around line 157):
```html
<a href="/admin/revbank" class="block px-3 py-2 pl-6 text-base font-medium text-gray-500 hover:text-gray-900 hover:bg-gray-50">RevBank</a>
```
→
```html
<a href="/admin/bar" class="block px-3 py-2 pl-6 text-base font-medium text-gray-500 hover:text-gray-900 hover:bg-gray-50">Bar</a>
```

- [ ] **Step 3: Build to verify**

Run: `make build`
Expected: Build succeeds. Templates don't exist yet but Go compiles — template errors are runtime only.

- [ ] **Step 4: Commit**

```bash
git add cmd/server/main.go web/templates/layout.html internal/handler/bar.go
git rm internal/handler/revbank.go
git commit -m "feat(bar): migrate revbank routes and handlers to /admin/bar"
```

---

## Task 4: Migrate dashboard template

**Files:**
- Create: `web/templates/admin_bar.html` (based on `admin_revbank.html`)
- Delete: `web/templates/admin_revbank.html`
- Modify: `web/static/css/admin.css` (add tab navigation styles)

- [ ] **Step 1: Add tab navigation CSS to admin.css**

Append to `web/static/css/admin.css`:

```css
/* Bar tab navigation */
.bar-tabs {
    display: flex;
    gap: 0;
    border-bottom: 2px solid #e5e7eb;
    margin-bottom: 1.5rem;
}
.bar-tabs a {
    padding: 0.5rem 1.25rem;
    font-size: 0.875rem;
    font-weight: 500;
    color: #6b7280;
    text-decoration: none;
    border-bottom: 2px solid transparent;
    margin-bottom: -2px;
    transition: color 0.15s, border-color 0.15s;
}
.bar-tabs a:hover {
    color: #1f2937;
}
.bar-tabs a.active {
    color: #1a1a2e;
    border-bottom-color: #1a1a2e;
    font-weight: 600;
}
```

- [ ] **Step 2: Create admin_bar.html from admin_revbank.html**

Copy `web/templates/admin_revbank.html` to `web/templates/admin_bar.html`. Make these changes:

At the very top of `{{ define "content" }}`, before the existing header, add tab navigation:

```html
{{ define "content" }}
<div class="max-w-7xl mx-auto">
    <!-- Tab navigation -->
    <nav class="bar-tabs">
        <a href="/admin/bar" class="active">Dashboard</a>
        <a href="/admin/bar/cards">Kartičky</a>
        <a href="/admin/bar/guides">Návody</a>
    </nav>
```

Update the title from "RevBank" to "Bar" in the heading.

Close the wrapping `</div>` at the very end before `{{ end }}`.

Keep all existing dashboard content (stats cards, accounts table, transactions table) unchanged.

- [ ] **Step 3: Delete old template**

```bash
rm web/templates/admin_revbank.html
```

- [ ] **Step 4: Test locally**

Run: `make dev`
Navigate to `http://localhost:8080/admin/bar`
Expected: Dashboard renders with tab navigation at top. Old URL `/admin/revbank` redirects.

- [ ] **Step 5: Commit**

```bash
git add web/templates/admin_bar.html web/static/css/admin.css
git rm web/templates/admin_revbank.html
git commit -m "feat(bar): migrate dashboard template with tab navigation"
```

---

## Task 5: Build the card generator page

**Files:**
- Create: `web/templates/admin_bar_cards.html`
- Modify: `web/static/css/admin.css` (add card print styles)

- [ ] **Step 1: Add card and print styles to admin.css**

Append to `web/static/css/admin.css`:

```css
/* Barcode card — credit card size */
.barcode-card {
    width: 85.6mm;
    height: 53.98mm;
    background: #fff;
    border: 2px solid #1a1a2e;
    position: relative;
    overflow: hidden;
    display: flex;
    flex-direction: column;
    padding: 3mm;
    font-family: 'Courier New', Courier, monospace;
    flex-shrink: 0;
}
.barcode-card .card-corner {
    position: absolute;
    width: 4mm;
    height: 4mm;
}
.barcode-card .corner-tl { top: 1mm; left: 1mm; border-top: 1.5px solid #1a1a2e; border-left: 1.5px solid #1a1a2e; }
.barcode-card .corner-tr { top: 1mm; right: 1mm; border-top: 1.5px solid #1a1a2e; border-right: 1.5px solid #1a1a2e; }
.barcode-card .corner-bl { bottom: 1mm; left: 1mm; border-bottom: 1.5px solid #1a1a2e; border-left: 1.5px solid #1a1a2e; }
.barcode-card .corner-br { bottom: 1mm; right: 1mm; border-bottom: 1.5px solid #1a1a2e; border-right: 1.5px solid #1a1a2e; }
.barcode-card .card-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: 0.5mm;
}
.barcode-card .card-org {
    font-weight: 900;
    font-size: 7pt;
    letter-spacing: 3px;
    text-transform: uppercase;
    color: #1a1a2e;
}
.barcode-card .card-subtitle {
    font-size: 5pt;
    color: #666;
}
.barcode-card .card-separator {
    border: none;
    border-top: 2px solid #1a1a2e;
    margin: 0.5mm 0;
}
.barcode-card .card-separator-thin {
    border: none;
    border-top: 0.5px dashed #aaa;
    margin: 0.5mm 0;
}
.barcode-card .card-nick-label {
    font-size: 4.5pt;
    color: #888;
    letter-spacing: 3px;
    text-transform: uppercase;
    text-align: center;
}
.barcode-card .card-nick {
    font-weight: 700;
    font-size: 16pt;
    letter-spacing: 2px;
    color: #1a1a2e;
    text-align: center;
}
.barcode-card .card-barcode {
    flex: 1;
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    padding: 0.5mm 0;
}
.barcode-card .card-barcode svg {
    width: 74mm;
    max-height: 18mm;
}
.barcode-card .card-barcode-text {
    font-size: 6.5pt;
    letter-spacing: 2px;
    color: #1a1a2e;
    margin-top: 0.3mm;
}
.barcode-card .card-footer {
    display: flex;
    justify-content: space-between;
    align-items: flex-end;
    margin-top: auto;
    font-size: 4pt;
    color: #999;
    line-height: 1.4;
}

/* Card grid for printing */
.card-print-area {
    display: none;
}

@media print {
    /* Hide everything except cards when printing from cards page */
    body:has(.card-print-area) > *:not(.card-print-area) { display: none !important; }
    body:has(.card-print-area) .card-print-area {
        display: grid !important;
        grid-template-columns: repeat(2, 85.6mm);
        gap: 5mm;
        justify-content: center;
        padding: 5mm;
    }
    .barcode-card {
        border: 1px solid #1a1a2e;
        break-inside: avoid;
    }
}
```

- [ ] **Step 2: Create admin_bar_cards.html**

Create `web/templates/admin_bar_cards.html`. Note: `username` values come from Go's `html/template` which auto-escapes all output, so DOM construction from these values is safe.

```html
{{ define "content" }}
<div class="max-w-7xl mx-auto">
    <!-- Tab navigation -->
    <nav class="bar-tabs">
        <a href="/admin/bar">Dashboard</a>
        <a href="/admin/bar/cards" class="active">Kartičky</a>
        <a href="/admin/bar/guides">Návody</a>
    </nav>

    <div class="flex items-center justify-between mb-6">
        <div>
            <h1 class="text-2xl font-bold text-gray-900">Členské kartičky</h1>
            <p class="text-sm text-gray-500 mt-1">CODE128 barkódy pro RevBank terminál</p>
        </div>
        <div class="flex gap-2">
            <button onclick="generateAll()" class="btn btn-primary">Tisk všech aktivních</button>
            <button onclick="generateSelected()" class="btn btn-secondary">Tisk vybraných</button>
        </div>
    </div>

    <!-- Member table -->
    <div class="bg-white shadow rounded-lg overflow-hidden mb-6">
        <table class="min-w-full divide-y divide-gray-200">
            <thead class="bg-gray-50">
                <tr>
                    <th class="px-4 py-3 text-left">
                        <input type="checkbox" id="select-all" onchange="toggleAll(this.checked)" class="rounded border-gray-300">
                    </th>
                    <th class="px-4 py-3 text-left text-xs font-medium text-gray-500 uppercase tracking-wider">Username</th>
                    <th class="px-4 py-3 text-left text-xs font-medium text-gray-500 uppercase tracking-wider">Jméno</th>
                    <th class="px-4 py-3 text-left text-xs font-medium text-gray-500 uppercase tracking-wider">Náhled</th>
                </tr>
            </thead>
            <tbody class="divide-y divide-gray-200">
                {{ range .Members }}
                <tr class="member-row hover:bg-gray-50" data-username="{{ .Username.String }}">
                    <td class="px-4 py-3">
                        <input type="checkbox" class="member-check rounded border-gray-300" value="{{ .Username.String }}">
                    </td>
                    <td class="px-4 py-3 text-sm font-mono font-medium text-gray-900">{{ .Username.String }}</td>
                    <td class="px-4 py-3 text-sm text-gray-600">
                        {{ if .Realname.Valid }}{{ .Realname.String }}{{ else }}{{ .Email }}{{ end }}
                    </td>
                    <td class="px-4 py-3">
                        <button onclick="previewCard('{{ .Username.String }}')" class="btn btn-sm btn-view">Náhled</button>
                    </td>
                </tr>
                {{ end }}
            </tbody>
        </table>
    </div>

    <!-- Single card preview -->
    <div id="card-preview" class="hidden mb-6">
        <h2 class="text-lg font-semibold text-gray-900 mb-3">Náhled kartičky</h2>
        <div id="preview-container" class="flex justify-center"></div>
        <div class="text-center mt-3">
            <button onclick="printPreview()" class="btn btn-primary">Vytisknout tuto kartičku</button>
        </div>
    </div>
</div>

<!-- Print area (hidden on screen, shown on print) -->
<div class="card-print-area" id="print-area"></div>

<script src="https://cdn.jsdelivr.net/npm/jsbarcode@3.11.6/dist/JsBarcode.all.min.js"></script>
<script>
// Card HTML builder. Username values are pre-escaped by Go html/template.
function createCard(username) {
    var card = document.createElement('div');
    card.className = 'barcode-card';
    card.innerHTML =
        '<div class="card-corner corner-tl"></div>' +
        '<div class="card-corner corner-tr"></div>' +
        '<div class="card-corner corner-bl"></div>' +
        '<div class="card-corner corner-br"></div>' +
        '<div class="card-header">' +
            '<div class="card-org">BASE48</div>' +
            '<div class="card-subtitle">HACKERSPACE BRNO</div>' +
        '</div>' +
        '<hr class="card-separator">' +
        '<div style="text-align:center;margin:0.5mm 0;">' +
            '<div class="card-nick-label">member id</div>' +
            '<div class="card-nick"></div>' +
        '</div>' +
        '<hr class="card-separator-thin">' +
        '<div class="card-barcode">' +
            '<svg class="barcode-svg"></svg>' +
            '<div class="card-barcode-text"></div>' +
        '</div>' +
        '<hr class="card-separator-thin">' +
        '<div class="card-footer">' +
            '<div>REVBANK TERMINAL</div>' +
            '<div style="text-align:right;">SCAN TO PAY</div>' +
        '</div>';
    // Set text content safely (not innerHTML)
    card.querySelector('.card-nick').textContent = username;
    card.querySelector('.card-barcode-text').textContent = username;
    return card;
}

function renderBarcode(svgElement, username) {
    JsBarcode(svgElement, username, {
        format: 'CODE128',
        width: 3,
        height: 50,
        margin: 10,
        displayValue: false,
        background: '#ffffff',
        lineColor: '#1a1a2e',
    });
}

function renderCards(usernames, container) {
    container.replaceChildren();
    usernames.forEach(function(username) {
        var card = createCard(username);
        container.appendChild(card);
        renderBarcode(card.querySelector('.barcode-svg'), username);
    });
}

function previewCard(username) {
    var container = document.getElementById('preview-container');
    renderCards([username], container);
    document.getElementById('card-preview').classList.remove('hidden');
}

function printPreview() {
    var preview = document.getElementById('preview-container');
    var printArea = document.getElementById('print-area');
    printArea.replaceChildren();
    Array.from(preview.children).forEach(function(card) {
        printArea.appendChild(card.cloneNode(true));
    });
    window.print();
}

function getSelectedUsernames() {
    var checks = document.querySelectorAll('.member-check:checked');
    return Array.from(checks).map(function(c) { return c.value; });
}

function generateSelected() {
    var usernames = getSelectedUsernames();
    if (usernames.length === 0) { alert('Vyber alespoň jednoho člena.'); return; }
    var printArea = document.getElementById('print-area');
    renderCards(usernames, printArea);
    window.print();
}

function generateAll() {
    var rows = document.querySelectorAll('.member-row');
    var usernames = Array.from(rows).map(function(r) { return r.dataset.username; });
    var printArea = document.getElementById('print-area');
    renderCards(usernames, printArea);
    window.print();
}

function toggleAll(checked) {
    document.querySelectorAll('.member-check').forEach(function(c) { c.checked = checked; });
}
</script>
{{ end }}
```

- [ ] **Step 3: Build and test locally**

Run: `make dev`
Navigate to `http://localhost:8080/admin/bar/cards`
Expected: Page shows member table with checkboxes, preview and print buttons work, JsBarcode renders CODE128 SVGs.

- [ ] **Step 4: Commit**

```bash
git add web/templates/admin_bar_cards.html web/static/css/admin.css
git commit -m "feat(bar): add member card generator page"
```

---

## Task 6: Build the guides listing page

**Files:**
- Create: `web/templates/admin_bar_guides.html`

- [ ] **Step 1: Create admin_bar_guides.html**

Create `web/templates/admin_bar_guides.html`:

```html
{{ define "content" }}
<div class="max-w-7xl mx-auto">
    <!-- Tab navigation -->
    <nav class="bar-tabs">
        <a href="/admin/bar">Dashboard</a>
        <a href="/admin/bar/cards">Kartičky</a>
        <a href="/admin/bar/guides" class="active">Návody</a>
    </nav>

    <div class="mb-6">
        <h1 class="text-2xl font-bold text-gray-900">Návody pro bar</h1>
        <p class="text-sm text-gray-500 mt-1">Tisknutelné plakáty k baru s čárovými kódy</p>
    </div>

    <div class="grid grid-cols-1 md:grid-cols-2 gap-6">
        <!-- How to buy guide -->
        <div class="bg-white shadow rounded-lg overflow-hidden">
            <div class="px-6 py-5">
                <h2 class="text-lg font-semibold text-gray-900">Jak nakupovat</h2>
                <p class="text-sm text-gray-500 mt-2">
                    Dvoustránkový návod pro zákazníky baru. Dva kroky: pípni zboží, pípni sebe.
                    Obsahuje barkódy pro guest účet, abort a undo.
                </p>
                <div class="mt-4">
                    <a href="/admin/bar/guides/buy" target="_blank" class="btn btn-primary">Otevřít / Tisk</a>
                </div>
            </div>
        </div>

        <!-- How to deposit guide -->
        <div class="bg-white shadow rounded-lg overflow-hidden">
            <div class="px-6 py-5">
                <h2 class="text-lg font-semibold text-gray-900">Jak dobíjet účet</h2>
                <p class="text-sm text-gray-500 mt-2">
                    Návod na dobití barového účtu. Zjednodušený flow se sekvenčními barkódy —
                    jedno pípnutí pro částku + cash potvrzení, druhé pro identitu.
                </p>
                <p class="text-xs text-amber-600 mt-2">
                    &#x26A0; Sekvenční barkódy (deposit+částka+cash) jsou experimentální —
                    otestuj se scannerem u baru před nasazením.
                </p>
                <div class="mt-4">
                    <a href="/admin/bar/guides/deposit" target="_blank" class="btn btn-primary">Otevřít / Tisk</a>
                </div>
            </div>
        </div>
    </div>
</div>
{{ end }}
```

- [ ] **Step 2: Build and test**

Run: `make dev`
Navigate to `http://localhost:8080/admin/bar/guides`
Expected: Two guide cards with links to printable versions.

- [ ] **Step 3: Commit**

```bash
git add web/templates/admin_bar_guides.html
git commit -m "feat(bar): add guides listing page"
```

---

## Task 7: Build "Jak nakupovat" printable guide

**Files:**
- Create: `web/templates/admin_bar_guide_buy.html`
- Modify: `web/static/css/admin.css` (add guide print styles)

- [ ] **Step 1: Add guide print styles to admin.css**

Append to `web/static/css/admin.css`:

```css
/* Printable bar guides */
.bar-guide {
    width: 210mm;
    min-height: 297mm;
    margin: 0 auto;
    padding: 8mm;
    font-family: 'Courier New', Courier, monospace;
    background: #fff;
    color: #000;
}
.bar-guide h1 {
    font-weight: 900;
    font-size: 28pt;
    letter-spacing: 3px;
    text-transform: uppercase;
    text-align: center;
    margin-bottom: 1mm;
    border-bottom: 2px solid #000;
    padding-bottom: 2mm;
}
.bar-guide .guide-subtitle {
    text-align: center;
    font-size: 13pt;
    color: #444;
    margin-bottom: 5mm;
}
.bar-guide .guide-step {
    border: 2px solid #000;
    padding: 5mm 6mm;
    display: flex;
    align-items: center;
    gap: 6mm;
}
.bar-guide .guide-step-number {
    font-weight: 900;
    font-size: 48pt;
    min-width: 22mm;
    text-align: center;
    color: #000;
    flex-shrink: 0;
}
.bar-guide .guide-step-title {
    font-weight: 900;
    font-size: 20pt;
    letter-spacing: 2px;
    text-transform: uppercase;
}
.bar-guide .guide-step-desc {
    font-size: 13pt;
    color: #333;
    line-height: 1.5;
}
.bar-guide .guide-arrow {
    text-align: center;
    font-size: 20pt;
    font-weight: bold;
    line-height: 1;
    color: #000;
    padding: 1mm 0;
}
.bar-guide .guide-options {
    display: flex;
    gap: 4mm;
    justify-content: center;
    align-items: stretch;
}
.bar-guide .guide-option {
    border: 1.5px dashed #666;
    padding: 3mm;
    flex: 1;
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 1.5mm;
    text-align: center;
}
.bar-guide .guide-option-best {
    border-style: solid;
    border-color: #000;
    background: #f8f8f8;
}
.bar-guide .guide-option-label {
    font-weight: 700;
    font-size: 12pt;
    letter-spacing: 2px;
    text-transform: uppercase;
}
.bar-guide .guide-option-desc {
    font-size: 10pt;
    color: #444;
}
.bar-guide .guide-badge {
    font-weight: 700;
    font-size: 7pt;
    letter-spacing: 2px;
    text-transform: uppercase;
    background: #000;
    color: #fff;
    padding: 0.5mm 2mm;
    margin-bottom: 1mm;
}
.bar-guide .guide-rescue {
    border: 1.5px solid #000;
    padding: 3mm 6mm;
    margin-top: 3mm;
}
.bar-guide .guide-rescue-title {
    font-weight: 700;
    font-size: 13pt;
    letter-spacing: 2px;
    text-transform: uppercase;
    margin-bottom: 2mm;
}
.bar-guide .guide-rescue-item {
    display: flex;
    align-items: center;
    gap: 4mm;
    border: 1px dashed #666;
    padding: 2mm 4mm;
    margin-bottom: 2mm;
}
.bar-guide .guide-rescue-label {
    font-size: 11pt;
    width: 65mm;
    flex-shrink: 0;
}
.bar-guide .guide-rescue-item svg {
    height: 12mm;
    flex: 1;
}
.bar-guide .guide-bc-label {
    font-size: 9pt;
    letter-spacing: 1px;
    min-width: 12mm;
}
.bar-guide .guide-tip {
    border: 1px solid #999;
    padding: 3mm 4mm;
    font-size: 10pt;
    color: #333;
    line-height: 1.5;
    margin-top: 2mm;
}
.bar-guide .guide-option svg {
    height: 14mm;
    width: auto;
}

@media screen {
    .bar-guide {
        border: 1px solid #ccc;
        margin-top: 10px;
        margin-bottom: 10px;
    }
}

@media print {
    .bar-guide-screen-only { display: none !important; }
    .bar-guide { border: none; }
}
```

- [ ] **Step 2: Create admin_bar_guide_buy.html**

Create `web/templates/admin_bar_guide_buy.html`:

```html
{{ define "content" }}
<div class="bar-guide-screen-only max-w-7xl mx-auto mb-4">
    <nav class="bar-tabs">
        <a href="/admin/bar">Dashboard</a>
        <a href="/admin/bar/cards">Kartičky</a>
        <a href="/admin/bar/guides" class="active">Návody</a>
    </nav>
    <div class="flex items-center justify-between">
        <a href="/admin/bar/guides" class="text-sm text-gray-500 hover:text-gray-700">&larr; Zpět na návody</a>
        <button onclick="window.print()" class="btn btn-primary">Tisk (Ctrl+P)</button>
    </div>
</div>

<div class="bar-guide">
    <h1>Jak nakupovat</h1>
    <div class="guide-subtitle">Dva kroky. Dvě pípnutí.</div>

    <div style="display:flex;flex-direction:column;gap:0;">
        <!-- Step 1: scan product -->
        <div class="guide-step">
            <div class="guide-step-number">1</div>
            <div style="flex:1;display:flex;flex-direction:column;gap:1mm;">
                <div class="guide-step-title">Pípni zboží</div>
                <div class="guide-step-desc">
                    Naskenuj čárový kód produktu, jeden nebo více.<br>
                    Sleduj stav na displeji.
                </div>
            </div>
            <div style="font-size:48pt;flex-shrink:0;text-align:center;min-width:20mm;">&#x1F4E6;</div>
        </div>

        <div class="guide-arrow">&#x25BC;</div>

        <!-- Step 2: scan yourself -->
        <div class="guide-step" style="flex-direction:column;align-items:stretch;">
            <div style="display:flex;align-items:center;gap:6mm;margin-bottom:3mm;">
                <div class="guide-step-number">2</div>
                <div style="flex:1;">
                    <div class="guide-step-title">Pípni sebe</div>
                    <div class="guide-step-desc">Identifikuj se jedním z těchto způsobů:</div>
                </div>
            </div>
            <div class="guide-options">
                <div class="guide-option guide-option-best">
                    <div class="guide-badge">Nejrychlejší</div>
                    <div class="guide-option-label">Tvoje kartička</div>
                    <div class="guide-option-desc">Naskenuj svůj<br>členský barkód</div>
                    <div style="font-size:36pt;">&#x1F4B3;</div>
                    <div class="guide-option-desc">Nemáš? Řekni si o ni!</div>
                </div>
                <div class="guide-option">
                    <div class="guide-option-label">Klávesnice</div>
                    <div class="guide-option-desc">Napiš svoji<br>přezdívku + Enter</div>
                    <div style="font-size:16pt;border:2px solid #000;padding:2mm 4mm;margin:2mm 0;letter-spacing:2px;">tvuj_nick</div>
                </div>
                <div class="guide-option">
                    <div class="guide-option-label">Guest účet</div>
                    <div class="guide-option-desc">Pro návštěvníky<br>bez účtu</div>
                    <svg id="bc-guest"></svg>
                    <div class="guide-bc-label">guest</div>
                </div>
            </div>
        </div>
    </div>

    <!-- Rescue barcodes -->
    <div class="guide-rescue">
        <div class="guide-rescue-title">Něco se nepovedlo?</div>
        <div class="guide-rescue-item">
            <div class="guide-rescue-label">Zrušit aktuální příkaz</div>
            <svg id="bc-abort"></svg>
            <div class="guide-bc-label">abort</div>
        </div>
        <div class="guide-rescue-item">
            <div class="guide-rescue-label">Vrátit starou transakci (výběr)</div>
            <svg id="bc-undo"></svg>
            <div class="guide-bc-label">undo</div>
        </div>
    </div>

    <div class="guide-tip">
        <strong>TIP:</strong> Zůstatek zjistíš pípnutím karty nebo zadáním přezdívky bez předchozího příkazu.<br>
        Účet si dobij hotovostí &mdash; návod vedle.
    </div>
</div>

<script src="https://cdn.jsdelivr.net/npm/jsbarcode@3.11.6/dist/JsBarcode.all.min.js"></script>
<script>
var bcOpts = {
    format: 'CODE128', width: 2, margin: 5,
    displayValue: false, background: '#ffffff', lineColor: '#000000',
};
JsBarcode('#bc-guest', 'guest', Object.assign({}, bcOpts, {height: 40}));
JsBarcode('#bc-abort', 'abort', Object.assign({}, bcOpts, {height: 35}));
JsBarcode('#bc-undo', 'undo', Object.assign({}, bcOpts, {height: 35}));
</script>
{{ end }}
```

- [ ] **Step 3: Test locally**

Run: `make dev`
Navigate to `http://localhost:8080/admin/bar/guides/buy`
Expected: A4-formatted guide with barcodes for guest, abort, undo. Print preview shows clean output without portal UI chrome.

- [ ] **Step 4: Commit**

```bash
git add web/templates/admin_bar_guide_buy.html web/static/css/admin.css
git commit -m "feat(bar): add 'Jak nakupovat' printable guide"
```

---

## Task 8: Build "Jak dobíjet" printable guide with sequential barcodes

**Files:**
- Create: `web/templates/admin_bar_guide_deposit.html`

- [ ] **Step 1: Create admin_bar_guide_deposit.html**

Create `web/templates/admin_bar_guide_deposit.html`:

```html
{{ define "content" }}
<div class="bar-guide-screen-only max-w-7xl mx-auto mb-4">
    <nav class="bar-tabs">
        <a href="/admin/bar">Dashboard</a>
        <a href="/admin/bar/cards">Kartičky</a>
        <a href="/admin/bar/guides" class="active">Návody</a>
    </nav>
    <div class="flex items-center justify-between">
        <a href="/admin/bar/guides" class="text-sm text-gray-500 hover:text-gray-700">&larr; Zpět na návody</a>
        <button onclick="window.print()" class="btn btn-primary">Tisk (Ctrl+P)</button>
    </div>
</div>

<div class="bar-guide">
    <h1>Jak si dobít barový účet</h1>
    <div class="guide-subtitle">Dvě pípnutí. Vlož peníze.</div>

    <div style="display:flex;flex-direction:column;gap:0;">
        <!-- Step 0: reset -->
        <div class="guide-step" style="padding:3mm 6mm;">
            <div class="guide-step-number" style="font-size:28pt;">0</div>
            <div style="flex:1;">
                <div class="guide-step-title" style="font-size:14pt;">Storno / Reset</div>
                <div class="guide-step-desc" style="font-size:11pt;">Zruší aktivní/předchozí příkaz.</div>
            </div>
            <div style="display:flex;flex-direction:column;align-items:center;">
                <svg id="bc-abort"></svg>
                <div class="guide-bc-label">abort</div>
            </div>
        </div>

        <div class="guide-arrow">&#x25BC;</div>

        <!-- Step 1: amount (sequential barcode) -->
        <div class="guide-step" style="flex-direction:column;align-items:stretch;">
            <div style="display:flex;align-items:center;gap:6mm;margin-bottom:2mm;">
                <div class="guide-step-number">1</div>
                <div style="flex:1;">
                    <div class="guide-step-title">Vyber částku</div>
                    <div class="guide-step-desc">Naskenuj jednu z částek &mdash; automaticky spustí dobíjení.</div>
                </div>
            </div>
            <div style="display:flex;flex-direction:column;gap:1mm;">
                <div style="display:flex;align-items:center;border:1.5px solid #000;padding:1mm 4mm;">
                    <div style="font-weight:900;font-size:16pt;min-width:28mm;text-align:right;">100 Kč</div>
                    <svg id="bc-100" style="height:12mm;flex:1;"></svg>
                </div>
                <div style="display:flex;align-items:center;border:1.5px solid #000;padding:1mm 4mm;">
                    <div style="font-weight:900;font-size:16pt;min-width:28mm;text-align:right;">200 Kč</div>
                    <svg id="bc-200" style="height:12mm;flex:1;"></svg>
                </div>
                <div style="display:flex;align-items:center;border:1.5px solid #000;padding:1mm 4mm;">
                    <div style="font-weight:900;font-size:16pt;min-width:28mm;text-align:right;">500 Kč</div>
                    <svg id="bc-500" style="height:12mm;flex:1;"></svg>
                </div>
                <div style="display:flex;align-items:center;border:1.5px solid #000;padding:1mm 4mm;">
                    <div style="font-weight:900;font-size:16pt;min-width:28mm;text-align:right;">1000 Kč</div>
                    <svg id="bc-1000" style="height:12mm;flex:1;"></svg>
                </div>
            </div>
        </div>

        <div class="guide-arrow">&#x25BC;</div>

        <!-- Step 2: identity -->
        <div class="guide-step" style="flex-direction:column;align-items:stretch;border-width:2.5px;">
            <div style="display:flex;align-items:center;gap:6mm;margin-bottom:2mm;">
                <div class="guide-step-number">2</div>
                <div style="flex:1;">
                    <div class="guide-step-title">Pípni sebe</div>
                    <div class="guide-step-desc">Použij svoji kartičku, napiš přezdívku, nebo naskenuj guest.</div>
                </div>
            </div>
            <div class="guide-options">
                <div class="guide-option">
                    <div class="guide-option-label">Tvoje kartička</div>
                    <div class="guide-option-desc">Naskenuj svůj členský barkód</div>
                    <div style="font-size:28pt;margin:1mm 0;">&#x1F4B3;</div>
                </div>
                <div class="guide-option">
                    <div class="guide-option-label">Nebo napiš</div>
                    <div class="guide-option-desc">Přezdívku na klávesnici + Enter</div>
                    <div style="font-size:14pt;border:1px solid #000;padding:1mm 3mm;margin:1mm 0;letter-spacing:2px;">tvuj_nick</div>
                </div>
                <div class="guide-option">
                    <div class="guide-option-label">Guest účet</div>
                    <div class="guide-option-desc">Pro návštěvníky bez účtu</div>
                    <svg id="bc-guest"></svg>
                    <div class="guide-bc-label">guest</div>
                </div>
            </div>
        </div>

        <div class="guide-arrow">&#x25BC;</div>

        <!-- Step 3: insert money -->
        <div class="guide-step">
            <div class="guide-step-number">3</div>
            <div style="flex:1;">
                <div class="guide-step-title">Vlož peníze</div>
                <div class="guide-step-desc" style="font-size:16pt;font-weight:bold;margin-top:1mm;">
                    Nyní vlož hotovost do pokladny.
                </div>
            </div>
            <div style="font-size:48pt;flex-shrink:0;text-align:center;min-width:20mm;">&#x1F4B0;</div>
        </div>
    </div>

    <div class="guide-tip">
        <strong>TIP:</strong> Zůstatek si zkontroluj načtením svojí kartičky nebo zadáním přezdívky.<br>
        Pokud se něco pokazí, naskenuj <strong>abort</strong> a začni znovu.<br>
        <strong>&#x26A0; Sekvenční barkódy (částka = deposit+cash v jednom) jsou experimentální.</strong>
        Pokud nefungují se scannerem, použij klasický postup: deposit &rarr; částka &rarr; cash &rarr; identita.
    </div>
</div>

<script src="https://cdn.jsdelivr.net/npm/jsbarcode@3.11.6/dist/JsBarcode.all.min.js"></script>
<script>
var bcOpts = {
    format: 'CODE128', width: 2, margin: 5,
    displayValue: false, background: '#ffffff', lineColor: '#000000',
};
JsBarcode('#bc-abort', 'abort', Object.assign({}, bcOpts, {height: 40}));
JsBarcode('#bc-guest', 'guest', Object.assign({}, bcOpts, {height: 35}));

// Sequential barcodes: deposit + amount + cash encoded in one CODE128.
// The \n characters (0x0A) are sent by the scanner as Enter keystrokes,
// causing RevBank to process each line as a separate command.
JsBarcode('#bc-100', "deposit\n100\ncash", Object.assign({}, bcOpts, {height: 35}));
JsBarcode('#bc-200', "deposit\n200\ncash", Object.assign({}, bcOpts, {height: 35}));
JsBarcode('#bc-500', "deposit\n500\ncash", Object.assign({}, bcOpts, {height: 35}));
JsBarcode('#bc-1000', "deposit\n1000\ncash", Object.assign({}, bcOpts, {height: 35}));
</script>
{{ end }}
```

- [ ] **Step 2: Test locally**

Run: `make dev`
Navigate to `http://localhost:8080/admin/bar/guides/deposit`
Expected: A4-formatted deposit guide. Sequential barcodes render (they will be noticeably longer than simple barcodes due to encoded newline characters). Print preview clean.

- [ ] **Step 3: Commit**

```bash
git add web/templates/admin_bar_guide_deposit.html
git commit -m "feat(bar): add 'Jak dobíjet' printable guide with sequential barcodes"
```

---

## Task 9: Update sync script and final cleanup

**Files:**
- Modify: `contrib/revbank-sync.sh` (API URL)

- [ ] **Step 1: Update the sync script URL**

In `contrib/revbank-sync.sh`, line 199, replace:

```bash
"${PORTAL_URL}/api/revbank/sync")
```

With:

```bash
"${PORTAL_URL}/api/bar/sync")
```

- [ ] **Step 2: Build full project**

Run: `make build-all`
Expected: All binaries compile cleanly.

- [ ] **Step 3: Smoke test all routes**

Run: `make dev`

Verify these URLs:
- `http://localhost:8080/admin/bar` — dashboard with tabs
- `http://localhost:8080/admin/bar/cards` — card generator with member table
- `http://localhost:8080/admin/bar/guides` — guide listing
- `http://localhost:8080/admin/bar/guides/buy` — buy guide (printable)
- `http://localhost:8080/admin/bar/guides/deposit` — deposit guide (printable)
- `http://localhost:8080/admin/revbank` — redirects to `/admin/bar`

- [ ] **Step 4: Commit**

```bash
git add contrib/revbank-sync.sh
git commit -m "chore(bar): update sync script URL to /api/bar/sync"
```

---

## Summary

| Task | Description | Steps |
|---|---|---|
| 1 | SQL query for accepted users | 5 |
| 2 | Migrate handler revbank.go → bar.go | 4 |
| 3 | Update routes and navigation | 4 |
| 4 | Migrate dashboard template | 5 |
| 5 | Card generator page | 4 |
| 6 | Guides listing page | 3 |
| 7 | "Jak nakupovat" guide | 4 |
| 8 | "Jak dobíjet" guide (sequential barcodes) | 3 |
| 9 | Sync script update + smoke test | 4 |
| **Total** | | **36 steps** |

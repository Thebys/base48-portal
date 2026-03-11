# RevBank Integration Spec

## Context

Base48 runs a [RevBank](https://github.com/vega-d/revbank) (fork with CZK fixes)
kiosk for bar/snack purchases. It's a Perl CLI app on a dedicated machine on the
local hackerspace network. The member portal runs on a VPS on the internet.

**Network constraint**: Kiosk can reach the internet (push), but the portal cannot
reach the kiosk (NAT, local WiFi). All sync is kiosk-initiated.

**Identity mapping**: RevBank username = Keycloak username (lowercase).

## Goals

### P0 - Core
1. **Member profile**: collapsible box showing bar balance + recent purchases
2. **Member dashboard**: small widget/tile with current bar balance
3. **Admin view**: simple overview of RevBank data (accounts, balances, recent sales)

### P1 - Product Management
4. **View/edit products** in portal admin UI
5. **Bidirectional product sync** (portal edits names/prices, kiosk pushes new barcodes)

### P2 - Future
6. **Dedicated bar VS** (e.g. 555XXXX prefix) for bank deposits into bar balance
7. **Storno from portal** (out of scope for now)

## Architecture

```
┌──────────────────┐                        ┌──────────────────────┐
│  RevBank Kiosk   │                        │  Member Portal (VPS) │
│  (local network) │                        │                      │
│                  │   POST /api/revbank/   │                      │
│  cron script     │ ─────────────────────► │  Sync endpoint       │
│  (every 1-2 min) │   Bearer: API_KEY      │  (API key auth)      │
│                  │                        │                      │
│                  │   GET  /api/revbank/   │                      │
│                  │ ◄───────────────────── │  Products endpoint   │
│                  │   products             │                      │
└──────────────────┘                        └──────────────────────┘
```

Sync is **idempotent**. The kiosk is source of truth for balances and transactions.
The portal is source of truth for product metadata (names, prices, visibility).

## RevBank Data (source files on kiosk)

**`~/.revbank/accounts`** - one line per account:
```
thebys              +25.00 2026-03-10_22:36:06 +@2026-03-10_20:19:34
```
Fields: `name  balance  last_update  zero_crossing_timestamp`

System/hidden accounts start with `+` `-` `*` (e.g. `+sales/products`, `-cash`).

**`~/.revbank/log`** - append-only, each transaction is a block of lines.

**`~/.revbank/products`** - tab/space separated:
```
8594014630053  20  bramborky
```

## Data Model

### New portal tables

```sql
CREATE TABLE revbank_accounts (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    username TEXT NOT NULL UNIQUE,
    user_id INTEGER REFERENCES users(id),
    balance_cents INTEGER NOT NULL DEFAULT 0,
    last_transaction_at TIMESTAMP,
    synced_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE revbank_transactions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    transaction_id TEXT NOT NULL UNIQUE,
    username TEXT NOT NULL,
    user_id INTEGER REFERENCES users(id),
    amount_cents INTEGER NOT NULL,
    description TEXT NOT NULL,
    counter_account TEXT,
    created_at TIMESTAMP NOT NULL,
    synced_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE revbank_products (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    barcode TEXT NOT NULL UNIQUE,
    price_cents INTEGER NOT NULL,
    name TEXT NOT NULL,
    visible BOOLEAN NOT NULL DEFAULT TRUE,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
```

**Separate tables from `payments`** - bar tab credit is not membership fees.
Different source of truth, different semantics, clean separation.

## API

### Authentication

`RequireAPIKey()` middleware. Checks `Authorization: Bearer <key>` against
`REVBANK_API_KEY` env var. Constant-time comparison.

### POST /api/revbank/sync

Full state push from kiosk. Idempotent.

```json
{
  "accounts": [
    {"username": "thebys", "balance_cents": 2500, "last_transaction_at": "2026-03-10T22:36:06Z"}
  ],
  "transactions": [
    {
      "id": "2026-03-10_22:36:06_thebys_0",
      "username": "thebys",
      "amount_cents": -2000,
      "description": "bramborky",
      "counter_account": "+sales/products",
      "created_at": "2026-03-10T22:36:06Z"
    }
  ]
}
```

Response: `{"accounts_synced": 42, "transactions_synced": 5, "users_matched": 38}`

Logic:
1. Upsert accounts by username, resolve `user_id` via `users.username`
2. Upsert transactions by `transaction_id`
3. Skip system accounts (`+`, `-`, `*` prefixed)

### GET /api/revbank/products (P1)

Returns visible products for kiosk to pull. Supports `If-Modified-Since`.

### POST /api/revbank/products (P1)

Kiosk pushes new barcodes. Only inserts new ones, never overwrites portal edits.

## Portal UI

### Member profile (`/profile`)

Collapsible box "Bar / Kiosk":
- Current balance (color-coded: green positive, red negative)
- Last 10 purchases (date, item, amount)
- "Data synced from the hackerspace kiosk"

### Member dashboard tile

Small widget showing bar balance at a glance (like existing finance tile pattern).

### Admin (`/admin/revbank`)

Simple data overview:
- **Accounts table**: username (linked to user if matched), balance, last activity
- **Recent transactions**: flat list, most recent first
- **Sync status**: last sync timestamp
- Later (P1): product management table

## Kiosk Sync Script

**Source**: [`contrib/revbank-sync.sh`](../contrib/revbank-sync.sh) in this repository.

Bash script (no Perl dependency) that parses RevBank data files and POSTs them
to the portal API. Designed to run via cron on the kiosk machine.

**Requirements**: bash, awk, jq, curl

**How it works**:
1. Parses `~/.revbank/accounts` → account balances (skips system accounts)
2. Parses `~/.revbank/log` → CHECKOUT lines for user-facing transactions
3. Builds JSON payload, POSTs to `POST /api/revbank/sync`
4. Tracks position in `~/.revbank/.sync_cursor` for incremental log sync

**Deployment on kiosk**:
```bash
# Copy script to kiosk
scp contrib/revbank-sync.sh kiosk:/usr/local/bin/revbank-sync.sh
chmod +x /usr/local/bin/revbank-sync.sh

# Add cron job (as the user that owns ~/.revbank/)
crontab -e
# * * * * * REVBANK_PORTAL_URL=https://members.base48.cz REVBANK_API_KEY=<key> /usr/local/bin/revbank-sync.sh
```

**Local testing**:
```bash
# Create test data dir or point to real data
export REVBANK_DATA_DIR=/tmp/revbank-test/.revbank
export REVBANK_PORTAL_URL=http://localhost:4848
export REVBANK_API_KEY=test-key
bash contrib/revbank-sync.sh
```

## Security

- API key: shared secret, env var on both sides, constant-time comparison
- HTTPS only (portal behind TLS)
- Kiosk pushes data, portal never modifies RevBank state
- No secrets in RevBank data (just usernames + balances)
- Input validation on all incoming data

## Implementation Order

### Phase 1 (P0)
1. Migration: `revbank_accounts`, `revbank_transactions`
2. Config: `REVBANK_API_KEY`
3. Middleware: `RequireAPIKey()`
4. Handler + queries: `POST /api/revbank/sync`
5. Query: `GetUserByUsername` (new, needed for user_id resolution)
6. UI: member profile collapsible box + dashboard tile
7. UI: admin revbank overview page
8. Kiosk: sync script + cron

### Phase 2 (P1)
1. Migration: `revbank_products`
2. Handlers: `GET/POST /api/revbank/products`
3. UI: admin product editor
4. Kiosk: product sync in script

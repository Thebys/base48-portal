#!/bin/bash
# revbank-sync.sh — Syncs RevBank kiosk data to the member portal.
#
# Parses ~/.revbank/accounts and log, POSTs JSON to portal API.
# Designed to run via cron every 1-2 minutes.
#
# Requirements: bash, awk, jq, curl
# Configuration: via environment variables (see below)
#
# Usage:
#   REVBANK_PORTAL_URL=https://members.base48.cz REVBANK_API_KEY=secret ./revbank-sync.sh
#
# Or via cron (as root, since kiosk data is owned by kiosk user):
#   * * * * * REVBANK_PORTAL_URL=https://members.base48.cz REVBANK_API_KEY=secret /usr/local/bin/revbank-sync.sh

set -euo pipefail

# --- Lock (prevent concurrent runs) ---
exec 9>"${REVBANK_DATA_DIR:-/home/kiosk/.revbank}/.sync.lock"
flock -n 9 || { echo "[INFO] Another sync already running" >&2; exit 0; }

# --- Configuration ---
PORTAL_URL="${REVBANK_PORTAL_URL:?REVBANK_PORTAL_URL not set}"
API_KEY="${REVBANK_API_KEY:?REVBANK_API_KEY not set}"
DATA_DIR="${REVBANK_DATA_DIR:-/home/kiosk/.revbank}"
CURSOR_FILE="${DATA_DIR}/.sync_cursor"
LOG_TAG="revbank-sync"

# --- Helpers ---
log_info()  { logger -t "$LOG_TAG" "$*" 2>/dev/null || echo "[INFO] $*" >&2; }
log_error() { logger -t "$LOG_TAG" -p user.err "$*" 2>/dev/null || echo "[ERROR] $*" >&2; }

# --- Parse accounts file ---
# Format variant 1: "username\t0"
# Format variant 2: "username              +25.00 2026-03-10_23:34:35 +@..."
# System accounts start with + - * and are skipped.
parse_accounts() {
    local mode="${1:-user}"  # "user" or "system"
    awk -v mode="$mode" '
    {
        name = $1
        is_system = (name ~ /^[+\-*]/)
        # Filter by mode
        if (mode == "user" && is_system) next
        if (mode == "system" && !is_system) next
        # Skip empty lines and comments
        if (name == "" || name ~ /^#/) next

        if (NF == 2 && $2 == "0") {
            # Format: "username\t0" — zero balance, never used
            balance_str = "0.00"
            last_tx = ""
        } else if (NF >= 3) {
            # Format: "username  +25.00 2026-03-10_23:34:35 ..."
            balance_str = $2
            last_tx = $3
            # Strip leading +
            gsub(/^\+/, "", balance_str)
        } else {
            next
        }

        # Convert to cents (integer)
        cents = int(balance_str * 100 + (balance_str < 0 ? -0.5 : 0.5))

        # Output as JSON-friendly TSV: username, cents, last_tx
        printf "%s\t%d\t%s\n", name, cents, last_tx
    }
    ' "$DATA_DIR/accounts"
}

# --- Parse log CHECKOUT lines ---
# Format: "2026-03-07_04:09:09 CHECKOUT 1 vega         LOSE   1  10.00  # cheap noodles"
# We extract user-facing transactions (LOSE = purchase, GAIN = deposit)
# for non-system accounts.
parse_transactions() {
    local since_line="${1:-0}"

    awk -v since_line="$since_line" '
    NR <= since_line { next }
    $2 != "CHECKOUT" { next }
    {
        ts = $1
        tx_id = $3
        account = $4
        direction = $5
        # qty = $6 (not used)
        amount_str = $7

        # Skip system accounts
        if (account ~ /^[+\-*]/) next

        # Skip zero-amount entries (skim markers etc)
        if (direction == "====") next

        # Build description from everything after #
        desc = ""
        found_hash = 0
        for (i = 8; i <= NF; i++) {
            if ($i == "#") { found_hash = 1; continue }
            if (found_hash) {
                if (desc != "") desc = desc " "
                desc = desc $i
            }
        }

        # Amount in cents, negative for LOSE (purchase)
        cents = int(amount_str * 100 + 0.5)
        if (direction == "LOSE") cents = -cents

        # Unique transaction ID: tx_id + account (each checkout has multiple lines)
        unique_id = ts "_T" tx_id "_" account

        printf "%s\t%s\t%s\t%d\t%s\n", unique_id, account, ts, cents, desc
    }
    ' "$DATA_DIR/log"
}

# --- Build JSON payload ---
# Each sub-array is written to a temp file and recombined with --slurpfile
# (not --argjson). On a full re-sync the transactions array is multi-MB; passing
# it via --argjson would exceed Linux MAX_ARG_STRLEN (128 KiB per argv entry) and
# fail with "Argument list too long". Files have no such limit.
build_json() {
    local cursor
    cursor=$(cat "$CURSOR_FILE" 2>/dev/null || echo "0")

    local tmpdir
    tmpdir=$(mktemp -d "${TMPDIR:-/tmp}/revbank-sync-json.XXXXXX")

    # Parse user accounts into JSON array
    parse_accounts user | jq -Rn '
        [inputs | split("\t") |
            {
                username: .[0],
                balance_cents: (.[1] | tonumber),
                last_transaction_at: (if .[2] == "" then null else .[2] end)
            }
        ]
    ' > "$tmpdir/accounts.json"

    # Parse system accounts (cash, sales, etc.) as {name: balance} map
    parse_accounts system | jq -Rn '
        [inputs | split("\t") | {key: .[0], value: (.[1] | tonumber)}]
        | from_entries
    ' > "$tmpdir/system.json"

    # Parse new transactions into JSON array
    parse_transactions "$cursor" | jq -Rn '
        [inputs | split("\t") |
            {
                id: .[0],
                username: .[1],
                created_at: .[2],
                amount_cents: (.[3] | tonumber),
                description: .[4]
            }
        ]
    ' > "$tmpdir/transactions.json"

    # Combine — --slurpfile reads each file as a stream of JSON values, so the
    # single array in each file is bound as element [0].
    local combined
    combined=$(jq -n \
        --slurpfile accounts "$tmpdir/accounts.json" \
        --slurpfile transactions "$tmpdir/transactions.json" \
        --slurpfile system_accounts "$tmpdir/system.json" \
        '{accounts: $accounts[0], transactions: $transactions[0], system_accounts: $system_accounts[0]}')

    rm -rf "$tmpdir"
    printf '%s\n' "$combined"
}

# --- Main ---
main() {
    # Verify data directory exists
    if [ ! -d "$DATA_DIR" ]; then
        log_error "Data directory $DATA_DIR not found"
        exit 1
    fi

    if [ ! -f "$DATA_DIR/accounts" ]; then
        log_error "Accounts file not found: $DATA_DIR/accounts"
        exit 1
    fi

    # Build payload
    local payload
    payload=$(build_json)

    local account_count transaction_count
    account_count=$(echo "$payload" | jq '.accounts | length')
    transaction_count=$(echo "$payload" | jq '.transactions | length')

    # Skip sync if nothing to send
    if [ "$account_count" -eq 0 ] && [ "$transaction_count" -eq 0 ]; then
        exit 0
    fi

    # POST to portal
    # NOTE: payload is sent from a temp file via --data-binary @file, not inline
    # -d "$payload". A full re-sync (reset cursor) serializes the entire log and
    # can exceed Linux MAX_ARG_STRLEN (128 KiB per argv entry), which would make
    # an inline -d fail with "Argument list too long". A file has no such limit.
    local http_code response payload_file
    payload_file=$(mktemp "${TMPDIR:-/tmp}/revbank-sync-payload.XXXXXX")
    trap 'rm -f "$payload_file"' RETURN
    printf '%s' "$payload" > "$payload_file"

    response=$(curl -s -w "\n%{http_code}" \
        --max-time 30 \
        -X POST \
        -H "Content-Type: application/json" \
        -H "Authorization: Bearer ${API_KEY}" \
        --data-binary @"$payload_file" \
        "${PORTAL_URL}/api/bar/sync")

    http_code=$(echo "$response" | tail -1)
    local body
    body=$(echo "$response" | sed '$d')

    if [ "$http_code" -ne 200 ]; then
        log_error "Sync failed (HTTP $http_code): $body"
        exit 1
    fi

    # Update cursor to current log line count (for incremental sync)
    wc -l < "$DATA_DIR/log" > "$CURSOR_FILE"

    local synced_accounts synced_tx matched
    synced_accounts=$(echo "$body" | jq -r '.accounts_synced // 0')
    synced_tx=$(echo "$body" | jq -r '.transactions_synced // 0')
    matched=$(echo "$body" | jq -r '.users_matched // 0')

    log_info "Synced: ${synced_accounts} accounts, ${synced_tx} transactions, ${matched} matched"
}

main "$@"

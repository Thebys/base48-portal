-- name: GetUserByKeycloakID :one
SELECT * FROM users WHERE keycloak_id = ? LIMIT 1;

-- name: GetUserByID :one
SELECT * FROM users WHERE id = ? LIMIT 1;

-- name: GetUserByEmail :one
SELECT * FROM users WHERE LOWER(email) = LOWER(sqlc.arg(email)) LIMIT 1;

-- name: GetUserByPaymentsID :one
SELECT * FROM users WHERE payments_id = ? LIMIT 1;

-- name: LinkKeycloakID :one
UPDATE users SET
    keycloak_id = ?,
    updated_at = CURRENT_TIMESTAMP
WHERE LOWER(email) = LOWER(sqlc.arg(email)) AND keycloak_id IS NULL
RETURNING *;

-- name: ListUsers :many
SELECT * FROM users ORDER BY realname, email;

-- name: CreateUser :one
INSERT INTO users (
    keycloak_id, email, username, realname, phone, alt_contact,
    level_id, level_actual_amount, payments_id, state,
    is_council, is_staff, locale
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: UpdateUser :one
UPDATE users SET
    email = ?,
    username = ?,
    realname = ?,
    phone = ?,
    alt_contact = ?,
    level_id = ?,
    level_actual_amount = ?,
    payments_id = ?,
    state = ?,
    is_council = ?,
    is_staff = ?,
    keys_granted = ?,
    keys_returned = ?,
    updated_at = CURRENT_TIMESTAMP
WHERE id = ?
RETURNING *;

-- name: UpdateUserProfile :one
UPDATE users SET
    phone = ?,
    alt_contact = ?,
    updated_at = CURRENT_TIMESTAMP
WHERE id = ?
RETURNING *;

-- name: UpdateUserCustomFee :one
UPDATE users SET
    level_actual_amount = ?,
    updated_at = CURRENT_TIMESTAMP
WHERE id = ?
RETURNING *;

-- name: UpdateUserKeycloakInfo :one
UPDATE users SET
    username = ?,
    locale = ?,
    updated_at = CURRENT_TIMESTAMP
WHERE id = ?
RETURNING *;

-- name: GrantUserKeys :one
UPDATE users SET
    keys_granted = CURRENT_TIMESTAMP,
    keys_returned = NULL,
    updated_at = CURRENT_TIMESTAMP
WHERE id = ?
RETURNING *;

-- name: ReturnUserKeys :one
UPDATE users SET
    keys_returned = CURRENT_TIMESTAMP,
    updated_at = CURRENT_TIMESTAMP
WHERE id = ?
RETURNING *;

-- name: GetLevel :one
SELECT * FROM levels WHERE id = ? LIMIT 1;

-- name: ListActiveLevels :many
SELECT * FROM levels WHERE active = 1 ORDER BY CAST(amount AS REAL) ASC;

-- name: UpdateUserLevel :one
UPDATE users SET
    level_id = ?,
    level_actual_amount = '0',
    updated_at = CURRENT_TIMESTAMP
WHERE id = ?
RETURNING *;

-- name: ListAllLevels :many
SELECT * FROM levels ORDER BY CAST(amount AS REAL) ASC;

-- name: CreateLevel :one
INSERT INTO levels (name, amount, active) VALUES (?, ?, 1) RETURNING *;

-- name: UpdateLevelActive :one
UPDATE levels SET active = ? WHERE id = ? RETURNING *;

-- name: UpdateLevel :one
UPDATE levels SET name = ?, amount = ? WHERE id = ? RETURNING *;

-- name: DeleteLevel :exec
DELETE FROM levels WHERE id = ?;

-- name: CountUsersByLevel :many
SELECT level_id, COUNT(*) as count FROM users GROUP BY level_id;

-- name: CountFeesByLevel :many
SELECT level_id, COUNT(*) as count FROM fees GROUP BY level_id;

-- name: GetPayment :one
SELECT * FROM payments WHERE id = ? LIMIT 1;

-- name: ListPaymentsByUser :many
SELECT * FROM payments WHERE user_id = ? ORDER BY date DESC;

-- name: ListUnassignedPayments :many
SELECT * FROM payments WHERE user_id IS NULL AND project_id IS NULL AND dismissed_at IS NULL ORDER BY date DESC;

-- name: ListDismissedPayments :many
SELECT * FROM payments WHERE dismissed_at IS NOT NULL ORDER BY dismissed_at DESC;

-- name: DismissPayment :one
UPDATE payments SET
    dismissed_at = CURRENT_TIMESTAMP,
    dismissed_by = ?,
    dismissed_reason = ?,
    staff_comment = ?
WHERE id = ?
RETURNING *;

-- name: UndismissPayment :one
UPDATE payments SET
    dismissed_at = NULL,
    dismissed_by = NULL,
    dismissed_reason = NULL
WHERE id = ?
RETURNING *;

-- name: UpsertPayment :one
-- substr normalizes Go time.Time to YYYY-MM-DD for SQLite date function compat
INSERT INTO payments (
    user_id, project_id, date, amount, kind, kind_id,
    local_account, remote_account, identification, raw_data, staff_comment
) VALUES (
    sqlc.arg(user_id), sqlc.arg(project_id), substr(sqlc.arg(date), 1, 10),
    sqlc.arg(amount), sqlc.arg(kind), sqlc.arg(kind_id),
    sqlc.arg(local_account), sqlc.arg(remote_account), sqlc.arg(identification),
    sqlc.arg(raw_data), sqlc.arg(staff_comment)
)
ON CONFLICT(kind, kind_id) DO UPDATE SET
    user_id = excluded.user_id,
    project_id = excluded.project_id,
    date = excluded.date,
    amount = excluded.amount,
    local_account = excluded.local_account,
    remote_account = excluded.remote_account,
    identification = excluded.identification,
    raw_data = excluded.raw_data,
    staff_comment = excluded.staff_comment
RETURNING *;

-- name: GetPaymentByKindAndID :one
SELECT * FROM payments WHERE kind = ? AND kind_id = ? LIMIT 1;

-- name: InsertOutgoingPayment :execrows
-- Record an outgoing FIO payment once. A row already there is left alone, so
-- the project and note an admin set survive every later sync. Returns 0 rows
-- for a payment already recorded.
-- substr normalizes Go time.Time to YYYY-MM-DD, as in UpsertPayment.
INSERT INTO payments (
    project_id, date, amount, kind, kind_id,
    local_account, remote_account, identification, raw_data, public_note
) VALUES (
    sqlc.arg(project_id), substr(sqlc.arg(date), 1, 10), sqlc.arg(amount), 'fio', sqlc.arg(kind_id),
    sqlc.arg(local_account), sqlc.arg(remote_account), sqlc.arg(identification),
    sqlc.arg(raw_data), sqlc.arg(public_note)
)
ON CONFLICT(kind, kind_id) DO NOTHING;

-- name: ListOutgoingPayments :many
-- Outgoing payments of one year (YYYY), newest first, with their project.
SELECT p.id, p.date, p.amount, p.remote_account, p.identification, p.raw_data,
       p.staff_comment, p.public_note, p.project_id, pr.name AS project_name
FROM payments p
LEFT JOIN projects pr ON pr.id = p.project_id
WHERE CAST(p.amount AS REAL) < 0
  AND substr(p.date, 1, 4) = CAST(sqlc.arg(year) AS TEXT)
ORDER BY p.date DESC, p.id DESC;

-- name: ListOutgoingPaymentYears :many
SELECT DISTINCT CAST(substr(date, 1, 4) AS TEXT) AS year
FROM payments
WHERE CAST(amount AS REAL) < 0
ORDER BY year DESC;

-- name: AssignOutgoingPayment :one
-- Set the project and the note members see of an outgoing payment. Refuses
-- (sql.ErrNoRows) anything that is not outgoing: incoming payments are
-- assigned on the unmatched payments page.
UPDATE payments SET
    project_id = sqlc.arg(project_id),
    public_note = sqlc.arg(public_note)
WHERE id = sqlc.arg(id) AND CAST(amount AS REAL) < 0
RETURNING *;

-- name: AssignPayment :one
UPDATE payments SET
    user_id = ?,
    staff_comment = ?
WHERE id = ?
RETURNING *;

-- name: GetFee :one
SELECT * FROM fees WHERE id = ? LIMIT 1;

-- name: ListFeesByUser :many
SELECT * FROM fees WHERE user_id = ? ORDER BY period_start DESC;

-- name: CreateFee :one
-- substr normalizes Go time.Time format ("2026-02-01 00:00:00 +0000 UTC") to "2026-02-01"
INSERT INTO fees (user_id, level_id, period_start, amount)
VALUES (sqlc.arg(user_id), sqlc.arg(level_id), substr(sqlc.arg(period_start), 1, 10), sqlc.arg(amount))
RETURNING *;

-- name: GetFeeByUserAndPeriod :one
SELECT * FROM fees WHERE user_id = sqlc.arg(user_id) AND period_start = substr(sqlc.arg(period_start), 1, 10) LIMIT 1;

-- name: DeleteFee :exec
DELETE FROM fees WHERE id = ?;

-- name: ListAcceptedUsersForFees :many
SELECT u.*, l.amount as level_amount
FROM users u
JOIN levels l ON u.level_id = l.id
WHERE u.state = 'accepted'
ORDER BY u.id;

-- name: GetUserBalance :one
-- Calculate membership fee balance (only payments matching user's payments_id VS)
-- ROUND prevents float precision artifacts (e.g. 6.00000000000093) that break int64 scan
SELECT CAST(ROUND(
    COALESCE((
        SELECT SUM(CAST(p.amount AS REAL))
        FROM payments p
        JOIN users u ON p.user_id = u.id
        WHERE p.user_id = ?
        AND p.identification = u.payments_id
    ), 0) -
    COALESCE((SELECT SUM(CAST(f.amount AS REAL)) FROM fees f WHERE f.user_id = ?), 0)
) AS INTEGER) as balance;

-- name: ListUserBalances :many
-- Batch-compute membership fee balance for all users (avoids N+1 per-user queries)
-- Uses same formula as GetUserBalance: payments matching user's VS minus fees
SELECT u.id as user_id, CAST(ROUND(
    COALESCE((
        SELECT SUM(CAST(p.amount AS REAL))
        FROM payments p
        WHERE p.user_id = u.id
        AND p.identification = u.payments_id
    ), 0) -
    COALESCE((SELECT SUM(CAST(f.amount AS REAL)) FROM fees f WHERE f.user_id = u.id), 0)
) AS INTEGER) as balance
FROM users u;

-- name: GetUserLastFeeMonth :one
-- The last month a member was charged for, as 'YYYY-MM', '' when never.
SELECT CAST(COALESCE(MAX(substr(period_start, 1, 7)), '') AS TEXT) AS month
FROM fees WHERE user_id = ?;

-- name: ListLastFeeMonths :many
-- The last month each member was charged for, as 'YYYY-MM'.
SELECT user_id, CAST(MAX(substr(period_start, 1, 7)) AS TEXT) AS month
FROM fees GROUP BY user_id;

-- name: GetLatestChargedMonth :one
-- The latest month anybody was charged for, up to the given 'YYYY-MM', so
-- that a fee an admin entered ahead does not count. '' when none.
SELECT CAST(COALESCE(MAX(substr(period_start, 1, 7)), '') AS TEXT) AS month
FROM fees WHERE substr(period_start, 1, 7) <= CAST(sqlc.arg(until) AS TEXT);

-- name: CreateLog :one
INSERT INTO system_logs (subsystem, level, user_id, message, metadata)
VALUES (?, ?, ?, ?, ?)
RETURNING *;

-- name: ListLogsFiltered :many
SELECT * FROM system_logs
WHERE (? = '' OR subsystem = ?)
  AND (? = '' OR level = ?)
  AND (? = 0 OR user_id = ?)
ORDER BY created_at DESC LIMIT ?;

-- name: GetDistinctSubsystems :many
SELECT DISTINCT subsystem FROM system_logs ORDER BY subsystem;

-- name: GetDistinctLevels :many
SELECT DISTINCT level FROM system_logs ORDER BY level;

-- ============================================================================
-- PROJECTS (Fundraising / Special VS)
-- ============================================================================

-- name: ListProjects :many
-- Open projects first, newest first; closed ones after them.
SELECT * FROM projects ORDER BY archived_at IS NOT NULL, id DESC;

-- name: SetProjectArchived :execrows
-- archived_at NULL opens a closed project again.
UPDATE projects SET archived_at = ? WHERE id = ?;

-- name: GetProject :one
SELECT * FROM projects WHERE id = ? LIMIT 1;

-- name: GetProjectByPaymentsID :one
-- Find project by any of its VS identifiers (in project_vs table)
SELECT p.* FROM projects p
JOIN project_vs pv ON p.id = pv.project_id
WHERE pv.vs = ? LIMIT 1;

-- name: CreateProject :one
INSERT INTO projects (name, payments_id, description)
VALUES (?, ?, ?)
RETURNING *;

-- name: DeleteProject :exec
DELETE FROM projects WHERE id = ?;

-- name: DeleteProjectVSByProject :exec
-- Run with DeleteProject: foreign keys are not enforced on every connection,
-- so ON DELETE CASCADE cannot be relied on, and a symbol left behind would
-- still count as taken.
DELETE FROM project_vs WHERE project_id = ?;

-- name: GetProjectPayments :many
-- A project's payments, incoming and outgoing. A payment belongs to a project
-- through project_id only; see migration 021.
SELECT * FROM payments
WHERE project_id = ?
ORDER BY date DESC, id DESC;

-- name: ListAssignedProjectPayments :many
-- Every payment that belongs to some project, for the fundraising page.
SELECT * FROM payments
WHERE project_id IS NOT NULL
ORDER BY date DESC, id DESC;

-- name: ListProjectTotals :many
-- What each project raised (incoming) and spent (outgoing, as a positive
-- number), for all projects in one go.
SELECT pr.id AS project_id,
    CAST(COALESCE(SUM(CASE WHEN CAST(p.amount AS REAL) > 0 THEN CAST(p.amount AS REAL) END), 0) AS REAL) AS raised,
    CAST(COALESCE(-SUM(CASE WHEN CAST(p.amount AS REAL) < 0 THEN CAST(p.amount AS REAL) END), 0) AS REAL) AS spent
FROM projects pr
LEFT JOIN payments p ON p.project_id = pr.id
GROUP BY pr.id;

-- name: AssignPaymentsByProjectVS :execrows
-- A VS was added to a project: the payments carrying it that belong to nobody
-- yet join the project, e.g. those sent before the symbol was known.
UPDATE payments SET project_id = sqlc.arg(project_id)
WHERE project_id IS NULL AND user_id IS NULL AND identification = sqlc.arg(vs);

-- name: UnassignPaymentsByProjectVS :execrows
-- A VS was removed from a project: the payments it brought in leave again.
UPDATE payments SET project_id = NULL
WHERE project_id = sqlc.arg(project_id) AND user_id IS NULL AND identification = sqlc.arg(vs);

-- name: CountProjectPayments :one
SELECT COUNT(*) FROM payments WHERE project_id = ?;

-- ============================================================================
-- PROJECT VS (Multiple VS identifiers per project)
-- ============================================================================

-- name: ListProjectVS :many
SELECT * FROM project_vs WHERE project_id = ? ORDER BY created_at;

-- name: ListAllProjectVS :many
-- Every project's symbols, for listing all projects without a query each.
SELECT * FROM project_vs ORDER BY project_id, created_at, id;

-- name: AddProjectVS :one
INSERT INTO project_vs (project_id, vs, note)
VALUES (?, ?, ?)
RETURNING *;

-- name: RemoveProjectVS :exec
DELETE FROM project_vs WHERE project_id = ? AND vs = ?;

-- name: GetProjectVSByVS :one
SELECT * FROM project_vs WHERE vs = ? LIMIT 1;

-- ============================================================================
-- EMAIL OUTBOX
-- ============================================================================

-- name: CreateEmailOutbox :one
-- substr normalizes Go time.Time to YYYY-MM-DD HH:MM:SS for SQLite datetime compat
INSERT INTO email_outbox (
    user_id, recipient, subject, template_name,
    rendered_html, status, max_attempts, next_retry_at
) VALUES (
    sqlc.arg(user_id), sqlc.arg(recipient), sqlc.arg(subject), sqlc.arg(template_name),
    sqlc.arg(rendered_html), sqlc.arg(status),
    sqlc.arg(max_attempts), substr(sqlc.arg(next_retry_at), 1, 19)
)
RETURNING *;

-- name: GetEmailOutbox :one
SELECT * FROM email_outbox WHERE id = ? LIMIT 1;

-- name: ListRecentEmailOutbox :many
SELECT * FROM email_outbox ORDER BY created_at DESC LIMIT ?;

-- name: MarkEmailSent :exec
-- The mail has left. Recorded whatever happened to the row meanwhile: it is a
-- fact, and a lease keeps admins from cancelling a row while it is sent.
UPDATE email_outbox SET
    status = 'sent',
    attempts = sqlc.arg(attempts),
    last_error = NULL,
    sent_at = datetime('now'),
    leased_until = NULL
WHERE id = sqlc.arg(id);

-- name: RescheduleEmail :exec
-- A delivery attempt failed and the email has tries left: try again after the
-- delay, given as an SQLite modifier such as '+10 minutes'.
UPDATE email_outbox SET
    attempts = sqlc.arg(attempts),
    last_error = sqlc.arg(last_error),
    next_retry_at = datetime('now', CAST(sqlc.arg(delay) AS TEXT)),
    leased_until = NULL
WHERE id = sqlc.arg(id) AND status = 'pending';

-- name: MarkEmailFailed :exec
-- The last try failed. Only a row still pending turns failed.
UPDATE email_outbox SET
    status = 'failed',
    attempts = sqlc.arg(attempts),
    last_error = sqlc.arg(last_error),
    leased_until = NULL
WHERE id = sqlc.arg(id) AND status = 'pending';

-- name: ListPendingEmailOutbox :many
-- Everything still waiting, the next to leave first. Never cut short: the
-- admin must see every email that is going to go out.
SELECT * FROM email_outbox
WHERE status = 'pending'
ORDER BY next_retry_at IS NULL, next_retry_at, id;

-- name: ListFinishedEmailOutbox :many
-- Sent, failed and cancelled emails, newest first.
SELECT * FROM email_outbox
WHERE status <> 'pending'
ORDER BY created_at DESC, id DESC
LIMIT ?;

-- name: ListEmailOutboxWithStatus :many
-- Emails in one state, newest first.
SELECT * FROM email_outbox
WHERE status = ?
ORDER BY created_at DESC, id DESC
LIMIT ?;

-- name: CountPendingEmailsByTemplate :one
-- How many waiting emails a change of the template's texts would reach: the
-- last look renders the reminders again, with the texts saved by then.
SELECT COUNT(*) FROM email_outbox
WHERE status = 'pending' AND template_name = ?;

-- name: CountEmailOutboxByStatus :many
SELECT status, COUNT(*) as count FROM email_outbox GROUP BY status;

-- name: CountEmailOutboxSentToday :one
SELECT COUNT(*) as count FROM email_outbox
WHERE status = 'sent' AND substr(sent_at, 1, 10) = DATE('now');

-- name: ClaimScheduledEmails :many
-- Atomically lease every due email to the calling worker for five minutes and
-- hand the rows over. The server and the cron daemon are separate processes on
-- the same database file, so a plain SELECT would let both pick up the same
-- row and mail a member twice. A worker that dies mid-send lets its lease
-- expire and the row is picked up again; next_retry_at stays the schedule.
UPDATE email_outbox SET
    leased_until = datetime('now', '+5 minutes')
WHERE status = 'pending'
  AND next_retry_at IS NOT NULL
  AND datetime(next_retry_at) <= datetime('now')
  AND (leased_until IS NULL OR datetime(leased_until) <= datetime('now'))
RETURNING *;

-- name: ClaimEmailOutbox :one
-- Lease one pending email for an admin's "send now" and make it due at once,
-- so that if the sender dies the worker still delivers it. No rows when the
-- email is not pending or a worker is sending it right now.
UPDATE email_outbox SET
    next_retry_at = datetime('now'),
    leased_until = datetime('now', '+5 minutes')
WHERE id = ? AND status = 'pending'
  AND (leased_until IS NULL OR datetime(leased_until) <= datetime('now'))
RETURNING *;

-- name: RequeueFailedEmail :one
-- Put a failed email back in the queue, due now, with its tries reset.
UPDATE email_outbox SET
    status = 'pending',
    attempts = 0,
    next_retry_at = datetime('now')
WHERE id = ? AND status = 'failed'
RETURNING *;

-- name: ListRecentMembershipEmails :many
-- Membership reminders already queued or sent, newest first. The manual
-- reminder picker folds these per user to show "last reminded N days ago" and
-- to spot a reminder still waiting in the outbox.
SELECT * FROM email_outbox
WHERE user_id IS NOT NULL
  AND template_name IN ('prepaid_ending.html', 'negative_balance.html', 'debt_warning.html')
  AND status IN ('pending', 'sent')
  AND created_at > datetime('now', '-180 days')
ORDER BY created_at DESC;

-- name: CancelEmailOutbox :one
-- An admin withdraws a pending email. No rows while a worker is sending it.
UPDATE email_outbox SET
    status = 'cancelled',
    last_error = sqlc.arg(reason),
    leased_until = NULL
WHERE id = sqlc.arg(id) AND status = 'pending'
  AND (leased_until IS NULL OR datetime(leased_until) <= datetime('now'))
RETURNING *;

-- name: CancelLeasedEmailOutbox :exec
-- The worker holding the lease withdraws the email after its last look.
UPDATE email_outbox SET
    status = 'cancelled',
    last_error = sqlc.arg(reason),
    leased_until = NULL
WHERE id = sqlc.arg(id) AND status = 'pending';

-- name: GetMembershipEmailSince :one
-- The newest membership-fee reminder queued or sent to the user since the given
-- UTC time, as 'YYYY-MM-DD HH:MM:SS' like created_at. The monthly run sends
-- each member at most one of them a calendar month.
SELECT * FROM email_outbox
WHERE user_id = sqlc.arg(user_id)
  AND template_name IN ('prepaid_ending.html', 'negative_balance.html', 'debt_warning.html')
  AND status IN ('pending', 'sent')
  AND created_at >= CAST(sqlc.arg(since) AS TEXT)
ORDER BY created_at DESC
LIMIT 1;

-- name: GetRecentEmailByUserAndTemplateWithin :one
-- Anti-spam with a caller-chosen window, given as an SQLite modifier such as
-- '-14 days'. Used by reminders whose interval is an admin setting.
SELECT * FROM email_outbox
WHERE user_id = sqlc.arg(user_id) AND template_name = sqlc.arg(template_name)
  AND status IN ('pending', 'sent')
  AND created_at > datetime('now', sqlc.arg(window))
ORDER BY created_at DESC
LIMIT 1;

-- name: UpdateEmailOutboxContent :exec
-- Re-render a still-pending email just before it leaves, so it quotes the
-- figure as it stands at send time rather than at queue time. Subject and
-- template may change too, e.g. a debt warning that has shrunk to a plain
-- negative-balance reminder.
UPDATE email_outbox SET
    subject = sqlc.arg(subject),
    template_name = sqlc.arg(template_name),
    template_data = NULL, -- read by nothing; drop the column once a rollback to 1.17 is off the table
    rendered_html = sqlc.arg(rendered_html)
WHERE id = sqlc.arg(id) AND status = 'pending';

-- ============================================================================
-- EMAIL TEMPLATE CONTENT (editable text blocks)
-- ============================================================================

-- name: ListEmailTemplateContentByTemplateLang :many
SELECT * FROM email_template_content
WHERE template_name = ? AND lang = ? ORDER BY block_name;

-- ============================================================================
-- SETTINGS (key-value cache for external data)
-- ============================================================================

-- name: GetSetting :one
SELECT * FROM settings WHERE key = ? LIMIT 1;

-- name: UpsertSetting :one
INSERT INTO settings (key, value, updated_at)
VALUES (?, ?, CURRENT_TIMESTAMP)
ON CONFLICT(key) DO UPDATE SET
    value = excluded.value,
    updated_at = CURRENT_TIMESTAMP
RETURNING *;

-- name: DeleteEmailTemplateContent :exec
-- Drop an override so the block falls back to the default text in code.
DELETE FROM email_template_content
WHERE template_name = ? AND block_name = ? AND lang = ?;

-- name: UpsertEmailTemplateContent :one
INSERT INTO email_template_content (template_name, block_name, lang, content, updated_by)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(template_name, block_name, lang) DO UPDATE SET
    content = excluded.content,
    updated_by = excluded.updated_by,
    updated_at = CURRENT_TIMESTAMP
RETURNING *;

-- ============================================================================
-- USER STATE UPDATE
-- ============================================================================

-- name: UpdateUserState :one
UPDATE users SET
    state = ?,
    updated_at = CURRENT_TIMESTAMP
WHERE id = ?
RETURNING *;

-- name: UpdateUserLocale :one
UPDATE users SET
    locale = ?,
    updated_at = CURRENT_TIMESTAMP
WHERE id = ?
RETURNING *;

-- name: UpdateUserEmail :one
-- Admin-only: change email for users not linked to Keycloak (legacy migration)
UPDATE users SET
    email = ?,
    updated_at = CURRENT_TIMESTAMP
WHERE id = ? AND (keycloak_id IS NULL OR keycloak_id = '')
RETURNING *;

-- ============================================================================
-- USER VARIABLE SYMBOL (payments_id) ALLOCATION
-- ============================================================================

-- name: UpdateUserPaymentsID :one
UPDATE users SET
    payments_id = ?,
    updated_at = CURRENT_TIMESTAMP
WHERE id = ?
RETURNING *;

-- name: GetMaxNumericPaymentsID :one
-- Find the highest numeric payments_id among users (for auto-allocation)
SELECT CAST(COALESCE(MAX(CAST(payments_id AS INTEGER)), 0) AS INTEGER) as max_vs
FROM users
WHERE payments_id IS NOT NULL
AND payments_id GLOB '[0-9]*'
AND LENGTH(payments_id) <= 10;

-- ============================================================================
-- FINANCIAL OVERVIEW
-- ============================================================================

-- name: ListMonthlyFeesByUser :many
-- Each member's fees per month, for the finance history.
SELECT user_id, substr(period_start, 1, 7) AS month,
    COUNT(*) AS fee_count,
    CAST(SUM(CAST(amount AS REAL)) AS REAL) AS amount
FROM fees
GROUP BY user_id, substr(period_start, 1, 7)
ORDER BY user_id, month;

-- name: ListMonthlyMemberPaymentsByUser :many
-- Each member's payments per month, by the rule of ListUserBalances: only
-- payments with the member's own VS count.
SELECT p.user_id, substr(p.date, 1, 7) AS month,
    COUNT(*) AS payment_count,
    CAST(SUM(CAST(p.amount AS REAL)) AS REAL) AS amount
FROM payments p
JOIN users u ON u.id = p.user_id
WHERE p.identification = u.payments_id
GROUP BY p.user_id, substr(p.date, 1, 7)
ORDER BY p.user_id, month;

-- name: UpsertBankBalance :exec
-- Writes only a changed balance: the sync restates 85 days on every run.
INSERT INTO bank_balances (account, day, balance) VALUES (?, ?, ?)
ON CONFLICT (account, day) DO UPDATE SET balance = excluded.balance
WHERE bank_balances.balance <> excluded.balance;

-- name: ListBankBalances :many
SELECT * FROM bank_balances ORDER BY day, account;

-- name: ListLatestBalances :many
-- Each account's last stored balance. With MAX() in a GROUP BY, SQLite takes
-- the bare column balance from the row with MAX(day).
SELECT account, CAST(MAX(day) AS TEXT) AS day, balance
FROM bank_balances
GROUP BY account
ORDER BY account;

-- name: ListRecentPayments :many
-- List all payments from the current and previous month (for bank statement view)
SELECT * FROM payments
WHERE date >= date('now', 'start of month', '-1 month')
ORDER BY date DESC;

-- ============================================================================
-- REVBANK (Hackerspace kiosk bar/snack accounts)
-- ============================================================================

-- name: GetUserByUsername :one
SELECT * FROM users WHERE LOWER(username) = LOWER(sqlc.arg(username)) LIMIT 1;

-- name: UpsertRevbankAccount :one
INSERT INTO revbank_accounts (username, user_id, balance_cents, last_transaction_at, synced_at)
VALUES (sqlc.arg(username), sqlc.arg(user_id), sqlc.arg(balance_cents), sqlc.arg(last_transaction_at), CURRENT_TIMESTAMP)
ON CONFLICT(username) DO UPDATE SET
    user_id = excluded.user_id,
    balance_cents = excluded.balance_cents,
    last_transaction_at = excluded.last_transaction_at,
    synced_at = CURRENT_TIMESTAMP
RETURNING *;

-- name: UpsertRevbankTransaction :exec
INSERT INTO revbank_transactions (transaction_id, username, user_id, amount_cents, description, counter_account, created_at, synced_at)
VALUES (sqlc.arg(transaction_id), sqlc.arg(username), sqlc.arg(user_id), sqlc.arg(amount_cents), sqlc.arg(description), sqlc.arg(counter_account), sqlc.arg(created_at), CURRENT_TIMESTAMP)
ON CONFLICT(transaction_id) DO UPDATE SET
    username = excluded.username,
    user_id = excluded.user_id,
    synced_at = CURRENT_TIMESTAMP;

-- name: GetRevbankAccountByUserID :one
SELECT * FROM revbank_accounts WHERE user_id = ? LIMIT 1;

-- name: GetRevbankAccountByUsername :one
SELECT * FROM revbank_accounts WHERE username = ? LIMIT 1;

-- name: ListRevbankTransactionsByUserID :many
SELECT * FROM revbank_transactions WHERE user_id = ? ORDER BY created_at DESC LIMIT ?;

-- name: ListRevbankTransactionsByUsername :many
SELECT * FROM revbank_transactions WHERE username = ? ORDER BY created_at DESC LIMIT ?;

-- name: ListRevbankAccounts :many
SELECT * FROM revbank_accounts ORDER BY username;

-- name: ListRevbankRecentTransactions :many
SELECT * FROM revbank_transactions ORDER BY created_at DESC LIMIT ?;

-- name: ListRevbankAllTransactions :many
-- Full history for the bar dashboard, which aggregates it in Go: created_at is
-- stored in Go's "+0000 UTC" layout, which SQLite's date functions reject
-- (date()/strftime() return NULL on it), so hour- and weekday-level bucketing
-- cannot be expressed here at all. Deliberately unordered -- computeBarAnalytics
-- sorts, because it is the code that depends on the order.
SELECT * FROM revbank_transactions;

-- name: ListAcceptedBarDebtors :many
-- Accepted members whose linked bar account is at or below the given balance
-- (a negative number of cents, see BarDebtSettings.MaxBalanceCents). Deepest debt first.
SELECT u.id AS user_id, u.email, ra.username AS bar_username, ra.balance_cents
FROM revbank_accounts ra
JOIN users u ON u.id = ra.user_id
WHERE u.state = 'accepted'
  AND ra.balance_cents <= sqlc.arg(max_balance_cents)
ORDER BY ra.balance_cents ASC;

-- name: ListAcceptedUsersWithUsername :many
SELECT id, username, realname, email, state
FROM users
WHERE state = 'accepted' AND username IS NOT NULL AND username != ''
ORDER BY username;

-- ============ Workshop reservations ============

-- name: ListResources :many
SELECT * FROM resources ORDER BY id;

-- name: GetResource :one
SELECT * FROM resources WHERE id = ? LIMIT 1;

-- name: UpdateResource :one
-- capabilities is a JSON array, e.g. '["lift"]'; see migration 017.
UPDATE resources SET
    state = ?,
    blocked_reason = ?,
    capabilities = ?,
    updated_at = CURRENT_TIMESTAMP
WHERE id = ?
RETURNING *;

-- name: CreateReservation :one
-- Atomic insert: succeeds only when the resource is available and no active
-- reservation overlaps. Returns sql.ErrNoRows on conflict.
INSERT INTO reservations (resource_id, user_id, starts_at, ends_at, note)
SELECT sqlc.arg(resource_id), sqlc.arg(user_id), sqlc.arg(starts_at), sqlc.arg(ends_at), sqlc.arg(note)
WHERE EXISTS (
    SELECT 1 FROM resources WHERE id = sqlc.arg(resource_id) AND state = 'available'
)
AND NOT EXISTS (
    SELECT 1 FROM reservations
    WHERE resource_id = sqlc.arg(resource_id)
      AND state = 'active'
      AND starts_at < sqlc.arg(ends_at)
      AND ends_at > sqlc.arg(starts_at)
)
RETURNING *;

-- name: GetReservation :one
SELECT * FROM reservations WHERE id = ? LIMIT 1;

-- name: ListActiveReservations :many
-- Active reservations that have not ended yet (now = 'YYYY-MM-DD HH:MM' local).
SELECT r.id, r.resource_id, r.user_id, r.starts_at, r.ends_at, r.note, r.state,
       u.username, u.email
FROM reservations r
JOIN users u ON u.id = r.user_id
WHERE r.state = 'active' AND r.ends_at > sqlc.arg(now)
ORDER BY r.resource_id, r.starts_at;

-- name: ListReservationHistory :many
-- Reservations that are over (already past, or force-ended by an admin)
-- and started on/after `since`. Both args are 'YYYY-MM-DD HH:MM' local, the
-- same format as starts_at/ends_at, so the comparison stays lexicographic.
-- Cancelled ones are left out: that slot was never actually used.
SELECT r.id, r.resource_id, r.user_id, r.starts_at, r.ends_at, r.note, r.state,
       u.username, u.email
FROM reservations r
JOIN users u ON u.id = r.user_id
WHERE r.state != 'cancelled'
  AND r.starts_at >= sqlc.arg(since)
  AND (r.state != 'active' OR r.ends_at <= sqlc.arg(now))
ORDER BY r.starts_at DESC;

-- name: CancelOwnReservation :one
UPDATE reservations SET
    state = 'cancelled',
    ended_by = sqlc.arg(user_id),
    updated_at = CURRENT_TIMESTAMP
WHERE id = sqlc.arg(id) AND user_id = sqlc.arg(user_id) AND state = 'active'
RETURNING *;

-- name: UpdateOwnReservationEnd :one
-- Extend/shorten own reservation. Fails (no rows) when the new end overlaps
-- another active reservation or is not after the start.
UPDATE reservations SET
    ends_at = sqlc.arg(ends_at),
    updated_at = CURRENT_TIMESTAMP
WHERE reservations.id = sqlc.arg(id)
  AND reservations.user_id = sqlc.arg(user_id)
  AND reservations.state = 'active'
  AND sqlc.arg(ends_at) > reservations.starts_at
  AND NOT EXISTS (
      SELECT 1 FROM reservations other
      WHERE other.resource_id = reservations.resource_id
        AND other.id != reservations.id
        AND other.state = 'active'
        AND other.starts_at < sqlc.arg(ends_at)
        AND other.ends_at > reservations.starts_at
  )
RETURNING *;

-- name: BumpReservation :one
-- Admin force-end of any active reservation.
UPDATE reservations SET
    state = 'bumped',
    ended_by = sqlc.arg(admin_user_id),
    updated_at = CURRENT_TIMESTAMP
WHERE id = sqlc.arg(id) AND state = 'active'
RETURNING *;

-- ============================================================================
-- Access grants: access to parts of the portal regardless of membership state.
-- Known scopes live in internal/access.
-- ============================================================================

-- name: ListAccessGrantScopes :many
-- Scopes granted to one user; read on every page that checks access.
SELECT scope FROM access_grants WHERE user_id = ? ORDER BY scope;

-- name: ListAccessGrants :many
-- Grants of one user with the admin who gave them, for the admin profile.
SELECT g.id, g.user_id, g.scope, g.note, g.granted_by, g.created_at,
       gb.email AS granted_by_email
FROM access_grants g
LEFT JOIN users gb ON gb.id = g.granted_by
WHERE g.user_id = ?
ORDER BY g.scope;

-- name: ListUserIDsWithAccessGrant :many
SELECT user_id FROM access_grants WHERE scope = ?;

-- name: CreateAccessGrant :one
-- Returns sql.ErrNoRows when the user already holds the grant.
INSERT INTO access_grants (user_id, scope, note, granted_by)
VALUES (?, ?, ?, ?)
ON CONFLICT (user_id, scope) DO NOTHING
RETURNING *;

-- name: DeleteAccessGrant :one
-- Returns sql.ErrNoRows when there was no such grant.
DELETE FROM access_grants WHERE user_id = ? AND scope = ?
RETURNING *;

-- ============================================================================
-- Revoked sessions: logged out before they expired. See migration 020.
-- ============================================================================

-- name: RevokeSession :exec
INSERT INTO revoked_sessions (id, expires_at) VALUES (?, ?)
ON CONFLICT (id) DO NOTHING;

-- name: IsSessionRevoked :one
SELECT EXISTS (SELECT 1 FROM revoked_sessions WHERE id = ?);

-- name: DeleteExpiredRevokedSessions :exec
DELETE FROM revoked_sessions WHERE expires_at < ?;

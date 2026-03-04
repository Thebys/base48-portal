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
SELECT * FROM projects ORDER BY id DESC;

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

-- name: GetProjectPayments :many
-- Get all payments for a project:
-- 1. Payments explicitly assigned to project (project_id set)
-- 2. Payments matching any of project's VS identifiers (from project_vs table)
SELECT DISTINCT p.* FROM payments p
WHERE p.project_id = sqlc.arg(project_id)
   OR p.identification IN (SELECT pv.vs FROM project_vs pv WHERE pv.project_id = sqlc.arg(project_id))
ORDER BY p.date DESC;

-- name: GetProjectBalance :one
-- Sum all payments for a project (by project_id OR by any VS in project_vs)
SELECT COALESCE(SUM(CAST(amount AS REAL)), 0) as total
FROM (
    SELECT DISTINCT p.id, p.amount FROM payments p
    WHERE p.project_id = sqlc.arg(project_id)
       OR p.identification IN (SELECT pv.vs FROM project_vs pv WHERE pv.project_id = sqlc.arg(project_id))
) sub;

-- ============================================================================
-- PROJECT VS (Multiple VS identifiers per project)
-- ============================================================================

-- name: ListProjectVS :many
SELECT * FROM project_vs WHERE project_id = ? ORDER BY created_at;

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
    template_data, rendered_html, status, max_attempts, next_retry_at
) VALUES (
    sqlc.arg(user_id), sqlc.arg(recipient), sqlc.arg(subject), sqlc.arg(template_name),
    sqlc.arg(template_data), sqlc.arg(rendered_html), sqlc.arg(status),
    sqlc.arg(max_attempts), substr(sqlc.arg(next_retry_at), 1, 19)
)
RETURNING *;

-- name: GetEmailOutbox :one
SELECT * FROM email_outbox WHERE id = ? LIMIT 1;

-- name: ListRecentEmailOutbox :many
SELECT * FROM email_outbox ORDER BY created_at DESC LIMIT ?;

-- name: UpdateEmailOutboxStatus :one
UPDATE email_outbox SET
    status = sqlc.arg(status),
    attempts = sqlc.arg(attempts),
    last_error = sqlc.arg(last_error),
    next_retry_at = substr(sqlc.arg(next_retry_at), 1, 19),
    sent_at = substr(sqlc.arg(sent_at), 1, 19)
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: CountEmailOutboxByStatus :many
SELECT status, COUNT(*) as count FROM email_outbox GROUP BY status;

-- name: CountEmailOutboxSentToday :one
SELECT COUNT(*) as count FROM email_outbox
WHERE status = 'sent' AND substr(sent_at, 1, 10) = DATE('now');

-- name: ListPendingScheduledEmails :many
SELECT * FROM email_outbox
WHERE status = 'pending' AND next_retry_at IS NOT NULL AND datetime(next_retry_at) <= datetime('now')
ORDER BY created_at;

-- name: CancelEmailOutbox :one
UPDATE email_outbox SET
    status = 'cancelled',
    next_retry_at = NULL
WHERE id = ? AND status = 'pending'
RETURNING *;

-- name: GetRecentEmailByUserAndTemplate :one
-- Anti-spam: check if an email of this type was already sent/queued for this user within 30 days
SELECT * FROM email_outbox
WHERE user_id = ? AND template_name = ?
  AND status IN ('pending', 'sent')
  AND created_at > datetime('now', '-30 days')
ORDER BY created_at DESC
LIMIT 1;

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

-- name: ListMonthlyPaymentStats :many
SELECT substr(date, 1, 7) as period,
    COUNT(*) as payment_count,
    CAST(SUM(CAST(amount AS REAL)) AS INTEGER) as payment_total
FROM payments
WHERE CAST(amount AS REAL) >= 5
    AND user_id IS NOT NULL
    AND date >= date('now', 'start of month', '-13 months')
GROUP BY 1;

-- name: ListMonthlyFeeStats :many
SELECT substr(period_start, 1, 7) as period,
    COUNT(*) as fee_count,
    CAST(SUM(CAST(amount AS REAL)) AS INTEGER) as fee_total
FROM fees
WHERE period_start >= date('now', 'start of month', '-13 months')
GROUP BY 1;

-- name: ListRecentPayments :many
-- List all payments from the current and previous month (for bank statement view)
SELECT * FROM payments
WHERE date >= date('now', 'start of month', '-1 month')
ORDER BY date DESC;

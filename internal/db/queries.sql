-- name: GetUserByKeycloakID :one
SELECT * FROM users WHERE keycloak_id = ? LIMIT 1;

-- name: GetUserByID :one
SELECT * FROM users WHERE id = ? LIMIT 1;

-- name: GetUserByEmail :one
SELECT * FROM users WHERE email = ? LIMIT 1;

-- name: GetUserByPaymentsID :one
SELECT * FROM users WHERE payments_id = ? LIMIT 1;

-- name: LinkKeycloakID :one
UPDATE users SET
    keycloak_id = ?,
    updated_at = CURRENT_TIMESTAMP
WHERE email = ? AND keycloak_id IS NULL
RETURNING *;

-- name: ListUsers :many
SELECT * FROM users ORDER BY realname, email;

-- name: ListUsersByState :many
SELECT * FROM users WHERE state = ? ORDER BY realname, email;

-- name: CreateUser :one
INSERT INTO users (
    keycloak_id, email, username, realname, phone, alt_contact,
    level_id, level_actual_amount, payments_id, state,
    is_council, is_staff
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
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
    username = ?,
    realname = ?,
    phone = ?,
    alt_contact = ?,
    updated_at = CURRENT_TIMESTAMP
WHERE id = ?
RETURNING *;

-- name: UpdateUserKeycloakInfo :one
UPDATE users SET
    username = ?,
    updated_at = CURRENT_TIMESTAMP
WHERE id = ?
RETURNING *;

-- name: GetLevel :one
SELECT * FROM levels WHERE id = ? LIMIT 1;

-- name: ListLevels :many
SELECT * FROM levels WHERE active = TRUE ORDER BY amount;

-- name: ListAllLevels :many
SELECT * FROM levels ORDER BY amount;

-- name: CreateLevel :one
INSERT INTO levels (name, amount, active)
VALUES (?, ?, ?)
RETURNING *;

-- name: UpdateLevel :one
UPDATE levels SET
    name = ?,
    amount = ?,
    active = ?
WHERE id = ?
RETURNING *;

-- name: GetPayment :one
SELECT * FROM payments WHERE id = ? LIMIT 1;

-- name: ListPaymentsByUser :many
SELECT * FROM payments WHERE user_id = ? ORDER BY date DESC;

-- name: ListMembershipPaymentsByUser :many
-- Only payments that match the user's membership VS (payments_id)
SELECT p.*
FROM payments p
JOIN users u ON p.user_id = u.id
WHERE p.user_id = ?
AND p.identification = u.payments_id
ORDER BY p.date DESC;

-- name: ListUnassignedPayments :many
SELECT * FROM payments WHERE user_id IS NULL ORDER BY date DESC;

-- name: ListRecentPayments :many
SELECT * FROM payments ORDER BY date DESC LIMIT ?;

-- name: CreatePayment :one
INSERT INTO payments (
    user_id, date, amount, kind, kind_id,
    local_account, remote_account, identification, raw_data, staff_comment
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: UpsertPayment :one
INSERT INTO payments (
    user_id, date, amount, kind, kind_id,
    local_account, remote_account, identification, raw_data, staff_comment
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(kind, kind_id) DO UPDATE SET
    date = excluded.date,
    amount = excluded.amount,
    local_account = excluded.local_account,
    remote_account = excluded.remote_account,
    identification = excluded.identification,
    raw_data = excluded.raw_data
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

-- name: ListFeesByPeriod :many
SELECT * FROM fees WHERE period_start = ? ORDER BY user_id;

-- name: CreateFee :one
INSERT INTO fees (user_id, level_id, period_start, amount)
VALUES (?, ?, ?, ?)
RETURNING *;

-- name: GetFeeByUserAndPeriod :one
SELECT * FROM fees WHERE user_id = ? AND period_start = ? LIMIT 1;

-- name: ListAcceptedUsersForFees :many
SELECT u.*, l.amount as level_amount
FROM users u
JOIN levels l ON u.level_id = l.id
WHERE u.state = 'accepted'
ORDER BY u.id;

-- name: GetUserBalance :one
-- Calculate membership fee balance (only payments matching user's payments_id VS)
SELECT
    COALESCE((
        SELECT SUM(CAST(p.amount AS REAL))
        FROM payments p
        JOIN users u ON p.user_id = u.id
        WHERE p.user_id = ?
        AND p.identification = u.payments_id
    ), 0) -
    COALESCE((SELECT SUM(CAST(f.amount AS REAL)) FROM fees f WHERE f.user_id = ?), 0) as balance;

-- name: CountUsersByState :many
SELECT state, COUNT(*) as count FROM users GROUP BY state;

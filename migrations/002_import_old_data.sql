-- Migration: Import data from old rememberportal database
-- This script imports levels, users, payments, and fees from the old system
--
-- Usage:
--   sqlite3 data/portal.db < migrations/002_import_old_data.sql
--
-- Note: This script uses ATTACH DATABASE to import from the old database

-- Attach the old database
ATTACH DATABASE 'migrations/rememberportal.sqlite3' AS old;

-- =============================================================================
-- PART 1: Import Levels
-- =============================================================================

-- Clear existing levels (except Awaiting which we need)
DELETE FROM levels WHERE id > 1;

-- Insert levels from old system
-- Note: We're using the same IDs as the old system for easier mapping
-- Using INSERT OR REPLACE for compatibility with older SQLite versions
INSERT OR REPLACE INTO levels (id, name, amount, active) VALUES
    (1, 'Regular member', '1000', 1),
    (2, 'Student member', '600', 1),
    (3, 'vpsFree.cz org', '0', 1),
    (4, 'Support member', '600', 1),
    (5, 'Regular + 3 m2 + 100W', '2260', 1),
    (6, 'Regular + 1.5 m2', '1280', 1),
    (7, 'Regular + 1 m2', '1120', 1),
    (8, 'Regular + 0.5 m2', '960', 1),
    (9, 'Regular + 2 m2', '1440', 1),
    (10, 'Regular + 3 m2', '1760', 1),
    (11, 'Regular + 4 m2', '2080', 1),
    (12, 'Regular + 5 m2', '2400', 1);

-- =============================================================================
-- PART 2: Import Users
-- =============================================================================

-- Create a temporary mapping table for old_id -> new_id
CREATE TEMP TABLE user_id_map (
    old_id INTEGER PRIMARY KEY,
    new_id INTEGER NOT NULL
);

-- Import users from old database
-- We preserve the old ID as the new ID for easier payment/fee mapping
INSERT INTO users (
    id,              -- Preserve old ID
    email,
    username,        -- Import ident as username
    realname,
    phone,
    alt_contact,
    level_id,
    level_actual_amount,
    payments_id,
    date_joined,
    keys_granted,
    keys_returned,
    state,
    is_council,
    is_staff,
    keycloak_id
)
SELECT
    u.id,                                    -- Preserve ID
    u.email,
    u.ident,                                -- Import ident as username
    u.realname,
    u.phone,
    u.altcontact,
    COALESCE(u.level, 1),                   -- Default to level 1 if NULL
    CAST(u.level_actual_amount AS TEXT),
    CAST(u.payments_id AS TEXT),
    u.date_joined,
    u.keys_granted,
    u.keys_returned,
    LOWER(u.state),                         -- Convert "Accepted" to "accepted"
    u.council,
    u.staff,
    NULL                                     -- Empty keycloak_id, will be linked on first login
FROM old.user u
WHERE u.email NOT LIKE '%@UNKNOWN'          -- Skip placeholder emails
  AND u.email NOT LIKE '%@unknown'
  AND u.email != ''
  AND u.email IS NOT NULL;

-- Populate the mapping table (in case IDs changed)
INSERT INTO user_id_map (old_id, new_id)
SELECT u.id, u.id FROM old.user u
WHERE u.email NOT LIKE '%@UNKNOWN'
  AND u.email NOT LIKE '%@unknown'
  AND u.email != ''
  AND u.email IS NOT NULL;

-- =============================================================================
-- PART 3: Import Payments
-- =============================================================================

-- Import all payment transactions
INSERT INTO payments (
    user_id,
    date,
    amount,
    kind,
    kind_id,
    local_account,
    remote_account,
    identification,
    raw_data,
    staff_comment,
    created_at
)
SELECT
    COALESCE(m.new_id, p.user),  -- Map to new user ID (or keep old if mapping missing)
    p.date,
    CAST(p.amount AS TEXT),      -- Convert numeric to TEXT for precision
    p.kind,
    p.kind_id,
    p.local_account,
    p.remote_account,
    p.identification,
    p.json,                      -- JSON blob with FIO bank data
    COALESCE(p.staff_comment, ''),
    p.date                       -- Use payment date as created_at
FROM old.payment p
LEFT JOIN user_id_map m ON p.user = m.old_id
WHERE p.user IS NOT NULL
ORDER BY p.date ASC;

-- Also import payments without user assignment (orphaned payments)
INSERT INTO payments (
    user_id,
    date,
    amount,
    kind,
    kind_id,
    local_account,
    remote_account,
    identification,
    raw_data,
    staff_comment,
    created_at
)
SELECT
    NULL,                        -- No user assigned
    p.date,
    CAST(p.amount AS TEXT),
    p.kind,
    p.kind_id,
    p.local_account,
    p.remote_account,
    p.identification,
    p.json,
    COALESCE(p.staff_comment, ''),
    p.date
FROM old.payment p
WHERE p.user IS NULL
ORDER BY p.date ASC;

-- =============================================================================
-- PART 4: Import Fees
-- =============================================================================

-- Import expected monthly fees
INSERT INTO fees (
    user_id,
    level_id,
    period_start,
    amount,
    created_at
)
SELECT
    COALESCE(m.new_id, f.user),  -- Map to new user ID
    f.level,                      -- Level ID should match
    DATE(f.period_start),         -- Ensure it's stored as DATE
    CAST(f.amount AS TEXT),       -- Convert numeric to TEXT
    f.period_start                -- Use period_start as created_at
FROM old.fee f
LEFT JOIN user_id_map m ON f.user = m.old_id
WHERE f.user IS NOT NULL
  AND m.new_id IS NOT NULL
ORDER BY f.period_start ASC;

-- =============================================================================
-- PART 5: Cleanup and Summary
-- =============================================================================

-- Drop temporary mapping table
DROP TABLE user_id_map;

-- Detach the old database
DETACH DATABASE old;

-- Print summary
SELECT 'Import Summary:' AS '';
SELECT '===============' AS '';
SELECT 'Users imported: ' || COUNT(*) FROM users;
SELECT 'Payments imported: ' || COUNT(*) FROM payments;
SELECT 'Fees imported: ' || COUNT(*) FROM fees;
SELECT 'Levels active: ' || COUNT(*) FROM levels WHERE active = 1;
SELECT '' AS '';
SELECT 'Payment date range: ' || MIN(date) || ' to ' || MAX(date) FROM payments;
SELECT 'Fee period range: ' || MIN(period_start) || ' to ' || MAX(period_start) FROM fees;
SELECT '' AS '';
SELECT 'Orphaned payments (no user): ' || COUNT(*) FROM payments WHERE user_id IS NULL;

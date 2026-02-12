-- Migration: 014_cleanup_duplicate_keycloak_users.sql
-- Fix: Case-sensitive email matching in GetUserByEmail caused duplicate accounts
-- when a legacy user's email had different casing than the Keycloak login email.
-- Example: legacy "Jelinek443@gmail.com" vs Keycloak "jelinek443@gmail.com"
--
-- This migration removes duplicate (newer) accounts that:
-- 1. Have the same email (case-insensitive) as an older account
-- 2. Are in 'awaiting' or 'rejected' state
-- 3. Have no payments assigned
-- 4. Have no fees generated
--
-- The root cause (case-sensitive email lookup) is fixed in application code.

-- Step 1: Clean up FK references (system_logs, email_outbox) for duplicate users
DELETE FROM system_logs WHERE user_id IN (
    SELECT u2.id
    FROM users u1
    JOIN users u2 ON LOWER(u1.email) = LOWER(u2.email) AND u1.id < u2.id
    WHERE u2.state IN ('awaiting', 'rejected')
    AND NOT EXISTS (SELECT 1 FROM payments WHERE user_id = u2.id)
    AND NOT EXISTS (SELECT 1 FROM fees WHERE user_id = u2.id)
);

DELETE FROM email_outbox WHERE user_id IN (
    SELECT u2.id
    FROM users u1
    JOIN users u2 ON LOWER(u1.email) = LOWER(u2.email) AND u1.id < u2.id
    WHERE u2.state IN ('awaiting', 'rejected')
    AND NOT EXISTS (SELECT 1 FROM payments WHERE user_id = u2.id)
    AND NOT EXISTS (SELECT 1 FROM fees WHERE user_id = u2.id)
);

-- Step 2: Delete the duplicate user accounts
DELETE FROM users
WHERE id IN (
    SELECT u2.id
    FROM users u1
    JOIN users u2 ON LOWER(u1.email) = LOWER(u2.email) AND u1.id < u2.id
    WHERE u2.state IN ('awaiting', 'rejected')
    AND NOT EXISTS (SELECT 1 FROM payments WHERE user_id = u2.id)
    AND NOT EXISTS (SELECT 1 FROM fees WHERE user_id = u2.id)
);

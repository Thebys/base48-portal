-- Migration: 012_normalize_dates.sql
-- Fix: modernc.org/sqlite Go driver serializes time.Time as
-- "2026-02-01 00:00:00 +0000 UTC" or "2026-02-08 00:00:00 +0100 CET"
-- which SQLite's date()/strftime()/datetime() functions cannot parse (returns NULL).
-- This breaks unique indexes, comparisons, and GROUP BY aggregations.
--
-- Solution: Normalize all date values to SQLite-compatible formats:
--   DATE columns    → YYYY-MM-DD          (substr to 10 chars)
--   DATETIME columns → YYYY-MM-DD HH:MM:SS (substr to 19 chars)

-- === fees.period_start (DATE) ===

-- Step 1: Drop the old expression index FIRST (it would block the UPDATE)
DROP INDEX IF EXISTS idx_fees_user_period;

-- Step 2: Normalize period_start to YYYY-MM-DD
UPDATE fees SET period_start = substr(period_start, 1, 10)
WHERE length(period_start) > 10;

-- Step 3: Remove duplicate fees created before normalization
DELETE FROM fees WHERE id NOT IN (
    SELECT MIN(id) FROM fees GROUP BY user_id, period_start
);

-- Step 4: Create clean unique index on normalized column
CREATE UNIQUE INDEX idx_fees_user_period ON fees(user_id, period_start);

-- === payments.date (TIMESTAMP used as DATE) ===
UPDATE payments SET date = substr(date, 1, 10)
WHERE length(date) > 10;

-- === email_outbox.sent_at (DATETIME) ===
UPDATE email_outbox SET sent_at = substr(sent_at, 1, 19)
WHERE sent_at IS NOT NULL AND length(sent_at) > 19;

-- === email_outbox.next_retry_at (DATETIME) ===
UPDATE email_outbox SET next_retry_at = substr(next_retry_at, 1, 19)
WHERE next_retry_at IS NOT NULL AND length(next_retry_at) > 19;

-- === users date columns (TIMESTAMP) — normalize for consistency ===
UPDATE users SET keys_granted = substr(keys_granted, 1, 19)
WHERE keys_granted IS NOT NULL AND length(keys_granted) > 19;

UPDATE users SET keys_returned = substr(keys_returned, 1, 19)
WHERE keys_returned IS NOT NULL AND length(keys_returned) > 19;

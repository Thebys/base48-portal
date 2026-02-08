-- Migration: 011_fix_fee_idempotency.sql
-- Fix: create_monthly_fees could create duplicate fees when SQLite datetime
-- formats didn't match exactly (e.g., "2026-02-01" vs "2026-02-01T00:00:00Z").

-- Step 1: Remove any duplicate fees (keep the earliest for each user+period)
DELETE FROM fees WHERE id NOT IN (
    SELECT MIN(id) FROM fees GROUP BY user_id, date(period_start)
);

-- Step 2: Add unique index to prevent future duplicates
CREATE UNIQUE INDEX IF NOT EXISTS idx_fees_user_period ON fees(user_id, date(period_start));

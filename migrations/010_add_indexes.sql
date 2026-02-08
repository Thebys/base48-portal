-- Migration: 010_add_indexes.sql
-- Add missing database indexes for common query patterns

-- Index on payments.identification for payment matching joins.
-- Payment matching looks up payments by variabilni symbol (identification)
-- to match them against user payments_id. Without this index, every match
-- operation requires a full table scan.
CREATE INDEX IF NOT EXISTS idx_payments_identification ON payments(identification);

-- Composite index on email_outbox(status, next_retry_at) for retry scheduling.
-- The email worker queries pending/failed emails ordered by next_retry_at.
-- The existing idx_email_outbox_status covers status-only filters, but the
-- composite index allows the scheduler to seek directly to retryable rows.
CREATE INDEX IF NOT EXISTS idx_email_outbox_retry ON email_outbox(status, next_retry_at);

-- Index on users.email for email-based lookups (GetUserByEmail).
-- Note: users.email already has a UNIQUE constraint which creates an implicit
-- unique index in SQLite. This explicit named index is added for clarity and
-- to ensure the query planner has a well-known index to reference.
CREATE INDEX IF NOT EXISTS idx_users_email ON users(email);

-- Migration: 018_external_state.sql
-- Adds 'external' to users.state: someone who is neither a member nor an
-- applicant. What they may use comes from access_grants (019), not from the
-- state.
--
-- SQLite cannot change a CHECK constraint in place, so the table is rebuilt as
-- described in https://www.sqlite.org/lang_altertable.html#otheralter:
-- create the new table, copy, drop the old one, rename the new one.
--
-- The migration runner executes this file as a single Exec outside any
-- transaction, so the file brings its own. foreign_keys has to be switched off
-- before BEGIN (the pragma is a no-op inside a transaction): with it on,
-- DROP TABLE users would fail on the payments, fees, reservations… that point
-- at it. Their REFERENCES users(id) clauses name the table, so they resolve to
-- the rebuilt one after the rename.
--
-- Columns and indexes match production as of 2026-10-10: 001, plus locale
-- from 009 and idx_users_email from 010. No triggers or views use users.

PRAGMA foreign_keys = OFF;
BEGIN;

CREATE TABLE users_new (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    keycloak_id TEXT UNIQUE,  -- Nullable for imported users, linked on first login
    email TEXT NOT NULL UNIQUE,
    username TEXT,  -- Username/nickname (ident from old system, synced with Keycloak)
    realname TEXT,
    phone TEXT,
    alt_contact TEXT,
    level_id INTEGER NOT NULL REFERENCES levels(id),
    level_actual_amount TEXT NOT NULL DEFAULT '0', -- For flexible fees
    payments_id TEXT UNIQUE, -- Variabilní symbol
    date_joined TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    keys_granted TIMESTAMP,
    keys_returned TIMESTAMP,
    state TEXT NOT NULL DEFAULT 'awaiting' CHECK (state IN ('awaiting', 'accepted', 'rejected', 'exmember', 'suspended', 'external')),
    is_council BOOLEAN NOT NULL DEFAULT FALSE,
    is_staff BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    locale TEXT NOT NULL DEFAULT 'cs'
);

INSERT INTO users_new (
    id, keycloak_id, email, username, realname, phone, alt_contact,
    level_id, level_actual_amount, payments_id, date_joined,
    keys_granted, keys_returned, state, is_council, is_staff,
    created_at, updated_at, locale
)
SELECT
    id, keycloak_id, email, username, realname, phone, alt_contact,
    level_id, level_actual_amount, payments_id, date_joined,
    keys_granted, keys_returned, state, is_council, is_staff,
    created_at, updated_at, locale
FROM users;

-- Carry the AUTOINCREMENT counter over. The copy only advances it to the
-- highest id still present; ids of deleted users must not be handed out again,
-- because old system_logs and email_outbox rows may still carry them.
DELETE FROM sqlite_sequence WHERE name = 'users_new';
INSERT INTO sqlite_sequence (name, seq)
    SELECT 'users_new', seq FROM sqlite_sequence WHERE name = 'users';

DROP TABLE users;
ALTER TABLE users_new RENAME TO users;

CREATE INDEX IF NOT EXISTS idx_users_state ON users(state);
CREATE INDEX IF NOT EXISTS idx_users_level ON users(level_id);
CREATE INDEX IF NOT EXISTS idx_users_keycloak ON users(keycloak_id) WHERE keycloak_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_users_username ON users(username) WHERE username IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_users_email ON users(email);

COMMIT;
-- Back on, as cmd/server/main.go sets it before running migrations.
PRAGMA foreign_keys = ON;

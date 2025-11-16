-- Migration: 002_allow_null_keycloak_id.sql
-- Allow keycloak_id to be NULL for imported users
-- They will be linked when they first log in via Keycloak

-- SQLite doesn't support ALTER COLUMN, so we need to recreate the table

-- 1. Create new users table with nullable keycloak_id
CREATE TABLE users_new (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    keycloak_id TEXT UNIQUE,  -- Changed: removed NOT NULL, will be NULL for imported users
    email TEXT NOT NULL UNIQUE,
    realname TEXT,
    phone TEXT,
    alt_contact TEXT,
    level_id INTEGER NOT NULL REFERENCES levels(id),
    level_actual_amount TEXT NOT NULL DEFAULT '0',
    payments_id TEXT UNIQUE,
    date_joined TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    keys_granted TIMESTAMP,
    keys_returned TIMESTAMP,
    state TEXT NOT NULL DEFAULT 'awaiting' CHECK (state IN ('awaiting', 'accepted', 'rejected', 'exmember', 'suspended')),
    is_council BOOLEAN NOT NULL DEFAULT FALSE,
    is_staff BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- 2. Copy data from old table
INSERT INTO users_new SELECT * FROM users;

-- 3. Drop old table
DROP TABLE users;

-- 4. Rename new table
ALTER TABLE users_new RENAME TO users;

-- 5. Recreate indexes
CREATE INDEX IF NOT EXISTS idx_users_state ON users(state);
CREATE INDEX IF NOT EXISTS idx_users_level ON users(level_id);
CREATE INDEX IF NOT EXISTS idx_users_keycloak ON users(keycloak_id) WHERE keycloak_id IS NOT NULL;

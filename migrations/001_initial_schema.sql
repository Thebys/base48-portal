-- Migration: 001_initial_schema.sql
-- Base48 Member Portal - Initial database schema

-- Membership levels
CREATE TABLE IF NOT EXISTS levels (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE,
    amount TEXT NOT NULL, -- Decimal stored as TEXT for precision
    active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Member states enum (stored as TEXT in SQLite)
-- Valid values: 'awaiting', 'accepted', 'rejected', 'exmember', 'suspended'

-- Users/Members
CREATE TABLE IF NOT EXISTS users (
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
    state TEXT NOT NULL DEFAULT 'awaiting' CHECK (state IN ('awaiting', 'accepted', 'rejected', 'exmember', 'suspended')),
    is_council BOOLEAN NOT NULL DEFAULT FALSE,
    is_staff BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Payments (actual transactions)
CREATE TABLE IF NOT EXISTS payments (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER REFERENCES users(id),
    date TIMESTAMP NOT NULL,
    amount TEXT NOT NULL, -- Decimal as TEXT
    kind TEXT NOT NULL, -- 'fio', 'manual', etc.
    kind_id TEXT NOT NULL, -- Unique ID within kind
    local_account TEXT NOT NULL,
    remote_account TEXT NOT NULL,
    identification TEXT NOT NULL, -- Variabilní symbol
    raw_data TEXT, -- JSON blob
    staff_comment TEXT,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(kind, kind_id)
);

-- Fees (expected monthly payments)
CREATE TABLE IF NOT EXISTS fees (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL REFERENCES users(id),
    level_id INTEGER NOT NULL REFERENCES levels(id),
    period_start DATE NOT NULL, -- First day of month
    amount TEXT NOT NULL, -- Decimal as TEXT
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Indexes for common queries
CREATE INDEX IF NOT EXISTS idx_users_state ON users(state);
CREATE INDEX IF NOT EXISTS idx_users_level ON users(level_id);
CREATE INDEX IF NOT EXISTS idx_users_keycloak ON users(keycloak_id) WHERE keycloak_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_users_username ON users(username) WHERE username IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_payments_user ON payments(user_id);
CREATE INDEX IF NOT EXISTS idx_payments_date ON payments(date);
CREATE INDEX IF NOT EXISTS idx_fees_user ON fees(user_id);
CREATE INDEX IF NOT EXISTS idx_fees_period ON fees(period_start);

-- Initial data: Default membership levels
INSERT INTO levels (name, amount, active) VALUES
    ('Awaiting', '0', 1),
    ('Student', '500', 1),
    ('Regular', '1000', 1),
    ('Supporter', '2000', 1),
    ('Sponsor', '5000', 1);

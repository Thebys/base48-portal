-- Migration: 016_workshop_reservations.sql
-- Workshop (autodílna) bay reservation system.
-- Resources are generic so future bookable things (lift, tools) reuse the same tables.

CREATE TABLE IF NOT EXISTS resources (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    slug TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL,
    description TEXT,
    state TEXT NOT NULL DEFAULT 'available' CHECK (state IN ('available', 'blocked', 'retired')),
    blocked_reason TEXT,
    capabilities TEXT NOT NULL DEFAULT '[]', -- JSON array, e.g. ["lift"]
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- starts_at/ends_at are TEXT 'YYYY-MM-DD HH:MM' (local time) — comparable
-- lexicographically. Do NOT bind time.Time here (see 012_normalize_dates.sql).
CREATE TABLE IF NOT EXISTS reservations (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    resource_id INTEGER NOT NULL REFERENCES resources(id),
    user_id INTEGER NOT NULL REFERENCES users(id),
    starts_at TEXT NOT NULL,
    ends_at TEXT NOT NULL,
    note TEXT,
    state TEXT NOT NULL DEFAULT 'active' CHECK (state IN ('active', 'cancelled', 'ended', 'bumped')),
    ended_by INTEGER REFERENCES users(id),
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (ends_at > starts_at)
);

CREATE INDEX IF NOT EXISTS idx_reservations_active
    ON reservations(resource_id, starts_at, ends_at) WHERE state = 'active';

INSERT INTO resources (slug, name, description, state, blocked_reason) VALUES
    ('bay-1', 'Stání 1', 'Stání vpravo. Plánovaná instalace dvousloupového zvedáku.', 'available', NULL),
    ('bay-2', 'Stání 2', 'Stání vlevo.', 'available', NULL);

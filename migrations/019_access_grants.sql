-- Migration: 019_access_grants.sql
-- Access to parts of the portal granted regardless of membership state, e.g.
-- the workshop for a non-member. Members get access from their state and
-- need no rows here; a grant only ever adds access.
-- The known scopes live in internal/access. Revoking deletes the row; the
-- history is in system_logs (subsystem 'access').

CREATE TABLE IF NOT EXISTS access_grants (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL REFERENCES users(id),
    scope TEXT NOT NULL,
    note TEXT,
    granted_by INTEGER REFERENCES users(id),
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (user_id, scope)
);

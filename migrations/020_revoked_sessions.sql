-- Migration: 020_revoked_sessions.sql
-- Sessions ended by logout. The session lives in a signed cookie, which stays
-- valid until an hour after sign-in whatever the browser does with it, so
-- logout records the session's id here and GetUser refuses it. A row is
-- needed only until that hour is up; logout deletes the expired ones.

CREATE TABLE IF NOT EXISTS revoked_sessions (
    id TEXT PRIMARY KEY,
    expires_at INTEGER NOT NULL -- unix seconds
);

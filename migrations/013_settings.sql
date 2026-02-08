-- Migration: 013_settings.sql
-- Key-value settings store for caching external data (FIO balance, rent status, etc.)

CREATE TABLE IF NOT EXISTS settings (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

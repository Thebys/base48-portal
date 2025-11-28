-- Migration: 003_system_logs.sql
-- Unified logging for all subsystems (email, fio_sync, cron, etc.)

CREATE TABLE IF NOT EXISTS system_logs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    subsystem TEXT NOT NULL,           -- 'email', 'fio_sync', 'cron', 'keycloak'
    level TEXT NOT NULL,               -- 'info', 'warning', 'error', 'success'
    user_id INTEGER REFERENCES users(id),
    message TEXT NOT NULL,
    metadata TEXT,                     -- JSON for subsystem-specific data
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_system_logs_subsystem ON system_logs(subsystem);
CREATE INDEX IF NOT EXISTS idx_system_logs_level ON system_logs(level);
CREATE INDEX IF NOT EXISTS idx_system_logs_user ON system_logs(user_id);
CREATE INDEX IF NOT EXISTS idx_system_logs_created_at ON system_logs(created_at);

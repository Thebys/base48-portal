-- Email outbox: every email goes through here for visibility, retry, and audit
CREATE TABLE IF NOT EXISTS email_outbox (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER REFERENCES users(id),
    recipient TEXT NOT NULL,
    subject TEXT NOT NULL,
    template_name TEXT NOT NULL,
    template_data TEXT,
    rendered_html TEXT,
    status TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'sent', 'failed', 'cancelled')),
    attempts INTEGER NOT NULL DEFAULT 0,
    max_attempts INTEGER NOT NULL DEFAULT 3,
    last_error TEXT,
    next_retry_at TIMESTAMP,
    sent_at TIMESTAMP,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_email_outbox_status ON email_outbox(status);
CREATE INDEX IF NOT EXISTS idx_email_outbox_created ON email_outbox(created_at);

-- Editable email template content blocks
-- Stores admin overrides for template text; defaults live in Go code
CREATE TABLE IF NOT EXISTS email_template_content (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    template_name TEXT NOT NULL,
    block_name TEXT NOT NULL,
    content TEXT NOT NULL,
    updated_by TEXT,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(template_name, block_name)
);

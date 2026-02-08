-- Add locale preference to users (synced from Keycloak)
ALTER TABLE users ADD COLUMN locale TEXT NOT NULL DEFAULT 'cs';

-- Recreate email_template_content with lang column
CREATE TABLE email_template_content_new (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    template_name TEXT NOT NULL,
    block_name TEXT NOT NULL,
    lang TEXT NOT NULL DEFAULT 'cs',
    content TEXT NOT NULL,
    updated_by TEXT,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(template_name, block_name, lang)
);

INSERT INTO email_template_content_new
    (id, template_name, block_name, lang, content, updated_by, updated_at)
SELECT id, template_name, block_name, 'cs', content, updated_by, updated_at
FROM email_template_content;

DROP TABLE email_template_content;
ALTER TABLE email_template_content_new RENAME TO email_template_content;

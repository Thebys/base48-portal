-- RevBank integration: accounts and transactions from hackerspace kiosk

CREATE TABLE IF NOT EXISTS revbank_accounts (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    username TEXT NOT NULL UNIQUE,
    user_id INTEGER REFERENCES users(id),
    balance_cents INTEGER NOT NULL DEFAULT 0,
    last_transaction_at TIMESTAMP,
    synced_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS revbank_transactions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    transaction_id TEXT NOT NULL UNIQUE,
    username TEXT NOT NULL,
    user_id INTEGER REFERENCES users(id),
    amount_cents INTEGER NOT NULL,
    description TEXT NOT NULL,
    counter_account TEXT,
    created_at TIMESTAMP NOT NULL,
    synced_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_revbank_accounts_user_id ON revbank_accounts(user_id) WHERE user_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_revbank_accounts_username ON revbank_accounts(username);
CREATE INDEX IF NOT EXISTS idx_revbank_transactions_username ON revbank_transactions(username);
CREATE INDEX IF NOT EXISTS idx_revbank_transactions_user_id ON revbank_transactions(user_id) WHERE user_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_revbank_transactions_created ON revbank_transactions(created_at);

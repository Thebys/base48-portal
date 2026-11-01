-- Migration 024: the balance of each of the association's bank accounts, day
-- by day, as the bank states it.
--
-- The finance page charts the balance over the years. It cannot be added up
-- from payments: before 1.13.0 the sync labelled every payment "FIO", from
-- whichever account the token read, and the old account is not synced at all.
-- FIO's statements carry the opening and closing balance, so the balance comes
-- from them: the sync keeps the last 85 days current, portal-cron
-- fio-balances loads older statements. A day is stored when money moved and at the end of
-- each statement; any other day has the balance of the last stored one.
CREATE TABLE IF NOT EXISTS bank_balances (
    account TEXT NOT NULL,   -- domestic number, 2003436319/2010
    day TEXT NOT NULL,       -- YYYY-MM-DD, the balance at the end of that day
    balance REAL NOT NULL,
    PRIMARY KEY (account, day)
);

-- Migration 025: a fundraising project can be closed.
--
-- A closed project stays on the page for the record, below the open ones and
-- without its QR code. Payments that still carry its symbol keep going to it.
ALTER TABLE projects ADD COLUMN archived_at DATETIME;

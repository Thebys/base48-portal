-- Migration 026: a worker that is sending an email holds it with a lease.
--
-- Until now the lease was written into next_retry_at, so a leased row looked
-- like any other pending one: an admin could cancel it while it was being sent
-- (the worker then marked it sent anyway), and "send now" could take a row the
-- worker already had and mail the member twice. next_retry_at is again only
-- the schedule; leased_until says who is busy with the row.
ALTER TABLE email_outbox ADD COLUMN leased_until TIMESTAMP;

-- Migration: 017_workshop_lift_capability.sql
-- Stání 1 is the bay with the two-post lift.
--
-- The 'lift' capability is what the portal renders as the lift badge on the
-- member page; an admin can turn it off again in /admin/workshop, so the bay
-- never needs another migration.
--
-- Guarded on capabilities = '[]' so it stays idempotent and never overwrites a
-- state an admin has already set from the UI.

UPDATE resources
SET capabilities = '["lift"]',
    description  = 'Stání vpravo. Dvousloupový zvedák.',
    updated_at   = CURRENT_TIMESTAMP
WHERE slug = 'bay-1' AND capabilities = '[]';

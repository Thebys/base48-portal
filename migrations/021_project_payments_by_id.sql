-- Migration 021: a payment belongs to a project through project_id alone.
--
-- Until now a project's payments were "project_id = X, or the VS is one of
-- the project's symbols", worked out on every read. That counted a payment
-- assigned to one project but carrying another project's VS in both, and it
-- counted member payments whose VS is also a project alias in the project.
-- From here on a project's symbols only decide which project a payment gets
-- assigned to (the FIO sync, adding a VS in admin); totals and lists read
-- project_id.
--
-- This assigns the payments that so far belonged to a project only through
-- their VS. Payments assigned to a member stay the member's. Outgoing
-- payments carrying a project VS are assigned as well: they were counted in
-- the project so far, and paying an expense with the project's VS is how it
-- gets booked to the project. Guarded by project_id IS NULL, so a second run
-- changes nothing.
UPDATE payments
SET project_id = (
    SELECT MIN(pv.project_id)
    FROM project_vs pv
    JOIN projects pr ON pr.id = pv.project_id
    WHERE pv.vs = payments.identification
)
WHERE project_id IS NULL
  AND user_id IS NULL
  AND identification IN (
    SELECT pv.vs FROM project_vs pv JOIN projects pr ON pr.id = pv.project_id
  );

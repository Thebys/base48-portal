-- Migration: Import data from old rememberportal database
-- This script imports levels and users from the old system

-- First, update the levels table with old member portal levels
-- We'll keep the basic levels from 001_initial_schema.sql and add the extended ones

-- Clear existing levels (except Awaiting which we need)
DELETE FROM levels WHERE id > 1;

-- Insert levels from old system
-- Note: We're using the same IDs as the old system for easier mapping
INSERT INTO levels (id, name, amount, active) VALUES
    (1, 'Regular member', '1000', 1),
    (2, 'Student member', '600', 1),
    (3, 'vpsFree.cz org', '0', 1),
    (4, 'Support member', '600', 1),
    (5, 'Regular + 3 m2 + 100W', '2260', 1),
    (6, 'Regular + 1.5 m2', '1280', 1),
    (7, 'Regular + 1 m2', '1120', 1),
    (8, 'Regular + 0.5 m2', '960', 1),
    (9, 'Regular + 2 m2', '1440', 1),
    (10, 'Regular + 3 m2', '1760', 1),
    (11, 'Regular + 4 m2', '2080', 1),
    (12, 'Regular + 5 m2', '2400', 1)
ON CONFLICT(id) DO UPDATE SET
    name = excluded.name,
    amount = excluded.amount,
    active = excluded.active;

-- Now we'll need to attach the old database and import users
-- Run this manually with sqlite3:
--
-- sqlite3 data/portal.db
-- ATTACH 'migrations/rememberportal.sqlite3' AS old;
--
-- INSERT INTO users (
--     email,
--     realname,
--     phone,
--     alt_contact,
--     level_id,
--     level_actual_amount,
--     payments_id,
--     date_joined,
--     state,
--     is_council,
--     is_staff,
--     keycloak_id
-- )
-- SELECT
--     email,
--     realname,
--     phone,
--     altcontact,
--     COALESCE(level, 1),
--     CAST(level_actual_amount AS TEXT),
--     CAST(payments_id AS TEXT),
--     date_joined,
--     LOWER(state),  -- Convert "Accepted" to "accepted"
--     council,
--     staff,
--     ''  -- Empty keycloak_id, will be linked on first login
-- FROM old.user
-- WHERE email NOT LIKE '%@UNKNOWN'  -- Skip placeholder emails
--   AND email NOT LIKE '%@unknown'
--   AND email != '';
--
-- DETACH old;

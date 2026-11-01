-- Migration 023: a real name that is only blanks is no name.
--
-- 33 members imported from the old portal in February 2026 have realname
-- set to a single space. Code that falls back from the real name to the
-- nickname or e-mail took the space for a name and showed nothing. Guarded
-- by the WHERE, so a second run changes nothing.
UPDATE users SET realname = NULL
WHERE realname IS NOT NULL AND TRIM(realname) = '';

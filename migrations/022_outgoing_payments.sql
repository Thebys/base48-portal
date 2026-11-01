-- Migration 022: outgoing payments, and a note on them members can read.
--
-- The FIO sync now records outgoing payments too (it used to skip them), so
-- members can see what a project spent. public_note is what members see of
-- such a payment besides date, amount and project; staff_comment stays
-- internal. The sync fills it from the bank, an admin can change it.
ALTER TABLE payments ADD COLUMN public_note TEXT;

-- Outgoing payments already here (imported from the old portal) get the same
-- start: the payer's comment from the bank, else the message for the
-- recipient. raw_data comes in two shapes: FIO's own, {"column25": {"value":
-- ...}}, from the old import, and {"column25": "..."} from this portal's sync.
UPDATE payments
SET public_note = COALESCE(
    NULLIF(TRIM(json_extract(raw_data, '$.column25.value')), ''),
    NULLIF(TRIM(json_extract(raw_data, '$.column16.value')), ''),
    CASE WHEN json_type(raw_data, '$.column25') = 'text'
         THEN NULLIF(TRIM(json_extract(raw_data, '$.column25')), '') END,
    CASE WHEN json_type(raw_data, '$.column16') = 'text'
         THEN NULLIF(TRIM(json_extract(raw_data, '$.column16')), '') END
)
WHERE CAST(amount AS REAL) < 0
  AND public_note IS NULL
  AND json_valid(raw_data);

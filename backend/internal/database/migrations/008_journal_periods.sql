-- A warehouse journal covers a period, so a month can be posted in parts: "Post now" on the 15th covers the 1st up to
-- that moment, and the month-end journal (posted automatically) covers the rest. Journals already posted cover their
-- whole month.
ALTER TABLE zoho_journal ADD COLUMN IF NOT EXISTS id BIGSERIAL;
ALTER TABLE zoho_journal ADD COLUMN IF NOT EXISTS period_start TIMESTAMPTZ;
ALTER TABLE zoho_journal ADD COLUMN IF NOT EXISTS period_end TIMESTAMPTZ;
ALTER TABLE zoho_journal ADD COLUMN IF NOT EXISTS automatic BOOLEAN NOT NULL DEFAULT FALSE;
UPDATE zoho_journal SET period_start = (month::timestamp AT TIME ZONE 'Asia/Kolkata'),
  period_end = ((month + interval '1 month')::timestamp AT TIME ZONE 'Asia/Kolkata') WHERE period_start IS NULL;
ALTER TABLE zoho_journal ALTER COLUMN period_start SET NOT NULL, ALTER COLUMN period_end SET NOT NULL;
ALTER TABLE zoho_journal DROP CONSTRAINT IF EXISTS zoho_journal_pkey;
ALTER TABLE zoho_journal ADD PRIMARY KEY (id);
CREATE UNIQUE INDEX IF NOT EXISTS zoho_journal_period_uq ON zoho_journal (property_id, period_start);

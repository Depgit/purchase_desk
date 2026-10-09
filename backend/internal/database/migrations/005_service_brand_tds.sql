-- Brand stays required, with two fixed choices: "Other brand" for unbranded goods and "Service" for services.
-- A service item carries a TDS section and rate; each request line keeps the rate it was raised with.
ALTER TABLE brand ADD COLUMN IF NOT EXISTS kind TEXT NOT NULL DEFAULT 'brand' CHECK (kind IN ('brand','other','service'));
INSERT INTO brand(name, kind) VALUES ('Other brand','other'), ('Service','service')
  ON CONFLICT ((norm_key(name))) DO UPDATE SET kind = EXCLUDED.kind, active = TRUE;
-- Items saved without a brand while it was optional become "Other brand".
UPDATE sku SET brand_id = (SELECT id FROM brand WHERE kind = 'other') WHERE brand_id IS NULL;
ALTER TABLE sku ADD COLUMN IF NOT EXISTS tds_section TEXT NOT NULL DEFAULT '';
ALTER TABLE sku ADD COLUMN IF NOT EXISTS tds_pct NUMERIC(5,2) NOT NULL DEFAULT 0 CHECK (tds_pct >= 0 AND tds_pct <= 30);
ALTER TABLE document_line ADD COLUMN IF NOT EXISTS tds_section TEXT NOT NULL DEFAULT '';
ALTER TABLE document_line ADD COLUMN IF NOT EXISTS tds_pct NUMERIC(5,2) NOT NULL DEFAULT 0;

-- Document numbers carry the property's 2-character document code: PR_HZ_0001, and PR_HZ_0001_01, _02 when one
-- submission splits into a request per vendor. The PO, transfer and GRN copy the request's number (PO_HZ_0001,
-- WT_HZ_0001, GRN_HZ_0001, or GRN_HZ_0001_01 per delivery). doc_seq is the property's last request number; once it
-- is above 0 the code is in use and stays fixed. Existing properties start blank until the admin sets the code.
ALTER TABLE property ADD COLUMN IF NOT EXISTS doc_code TEXT;
ALTER TABLE property ADD COLUMN IF NOT EXISTS doc_seq INT NOT NULL DEFAULT 0;
CREATE UNIQUE INDEX IF NOT EXISTS property_doc_code_key ON property (upper(doc_code)) WHERE doc_code IS NOT NULL;

-- Every bill entered (vendor invoice at goods receipt, utility or AMC bill, corporate bill) gets a purchase voucher
-- number from one series for all properties: PV_0001, PV_0002, ... (PV_10000 after PV_9999). The column default
-- numbers every new invoice; existing invoices are numbered in the order they were entered.
CREATE SEQUENCE IF NOT EXISTS pv_seq START 1;
CREATE OR REPLACE FUNCTION next_voucher() RETURNS TEXT LANGUAGE sql AS $$
  SELECT 'PV_' || lpad(n::text, greatest(4, length(n::text)), '0') FROM (SELECT nextval('pv_seq') AS n) s
$$;
ALTER TABLE invoice ADD COLUMN IF NOT EXISTS voucher_number TEXT UNIQUE;
WITH o AS (SELECT id, row_number() OVER (ORDER BY created_at, id) AS n FROM invoice WHERE voucher_number IS NULL)
UPDATE invoice i SET voucher_number = 'PV_' || lpad(o.n::text, greatest(4, length(o.n::text)), '0') FROM o WHERE i.id = o.id;
SELECT setval('pv_seq', greatest(count(*), 1), count(*) > 0) FROM invoice;
ALTER TABLE invoice ALTER COLUMN voucher_number SET DEFAULT next_voucher();
ALTER TABLE invoice ALTER COLUMN voucher_number SET NOT NULL;

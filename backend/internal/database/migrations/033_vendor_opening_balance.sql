-- 033 (21 Oct 2026): a vendor's opening balance from before Purchase Desk (often a vendor already in Zoho): negative
-- = an advance already paid, positive = old bills still owed, as on a date. The vendor's statement starts with it,
-- and a negative one becomes a paid, carried-forward "opening" advance that the vendor's next invoices take at payment
-- approval (it has no PO, and is never sent to Zoho, where it already is).
ALTER TABLE vendor ADD COLUMN IF NOT EXISTS opening_balance NUMERIC(14,2) NOT NULL DEFAULT 0;
ALTER TABLE vendor ADD COLUMN IF NOT EXISTS opening_as_on DATE;
ALTER TABLE advance ALTER COLUMN document_id DROP NOT NULL;
ALTER TABLE advance ALTER COLUMN property_id DROP NOT NULL;
ALTER TABLE advance ADD COLUMN IF NOT EXISTS opening BOOLEAN NOT NULL DEFAULT FALSE;
CREATE UNIQUE INDEX IF NOT EXISTS advance_opening_uq ON advance (vendor_id) WHERE opening;

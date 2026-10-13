-- A PO on Advance payment terms is not raised until Finance approves its advance. While it waits, the request stays
-- APPROVED with advance_hold_po set to the PO number it will get; when Finance approves the advance the PO is issued
-- under that number and sent to the vendor. If the advance is rejected or cancelled, the hold is cleared and Purchase
-- issues the PO again.
ALTER TABLE document ADD COLUMN IF NOT EXISTS advance_hold_po TEXT;

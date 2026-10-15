-- 020: when a PO actually reached the vendor (in a vendor order sheet, or by the PO email). Until then it shows
-- "Push to vendor". POs issued before this change were emailed at issue, so they count as sent.
ALTER TABLE document ADD COLUMN IF NOT EXISTS sent_to_vendor_at TIMESTAMPTZ;
UPDATE document SET sent_to_vendor_at = coalesce(po_date, updated_at)
  WHERE po_number IS NOT NULL AND po_number <> '' AND NOT is_transfer AND sent_to_vendor_at IS NULL;

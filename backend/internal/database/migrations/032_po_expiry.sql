-- 032 (21 Oct 2026): a PO's delivery date is given when it is raised and can be extended once (with a reason); once
-- it has passed by Flow Settings po_expiry_days, the poexpiry job rejects the undelivered balance (nothing delivered:
-- the PO is cancelled) and carries a paid advance forward to the vendor's next PO.
ALTER TABLE document ADD COLUMN IF NOT EXISTS deliver_by_extended BOOLEAN NOT NULL DEFAULT FALSE;
INSERT INTO setting (key, value) VALUES ('po_expiry_days', '0') ON CONFLICT (key) DO NOTHING;

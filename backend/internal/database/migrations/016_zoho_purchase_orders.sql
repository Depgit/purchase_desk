-- Every vendor PO is kept in step with a Zoho Books purchase order. zoho_po_sync says what is still to send ('create',
-- 'update' after a balance is rejected or the PO closes short, 'cancel' when nothing will be delivered, '' when in
-- step); a background job sends it and keeps any error in zoho_po_error (a "Held: ..." error waits for a mapping).
-- zoho_po_line_id is the id Zoho gave each line, which the bill quotes so Zoho shows the PO as billed.
-- invoice.zoho_tried_at spaces out automatic retries of a bill that Zoho refused.
ALTER TABLE document ADD COLUMN IF NOT EXISTS zoho_po_id TEXT;
ALTER TABLE document ADD COLUMN IF NOT EXISTS zoho_po_sync TEXT NOT NULL DEFAULT '' CHECK (zoho_po_sync IN ('', 'create', 'update', 'cancel'));
ALTER TABLE document ADD COLUMN IF NOT EXISTS zoho_po_error TEXT NOT NULL DEFAULT '';
ALTER TABLE document ADD COLUMN IF NOT EXISTS zoho_po_at TIMESTAMPTZ;
CREATE INDEX IF NOT EXISTS document_zoho_po_sync ON document(zoho_po_sync) WHERE zoho_po_sync <> '';
ALTER TABLE document_line ADD COLUMN IF NOT EXISTS zoho_po_line_id TEXT;
ALTER TABLE invoice ADD COLUMN IF NOT EXISTS zoho_tried_at TIMESTAMPTZ;

-- A balance the vendor never delivers can be rejected by Purchase, the store or the Admin after a part delivery
-- (or before any delivery). The rejected quantity comes off the line's qty, so the PO value, budget and receipt
-- checks follow it; rejected_qty keeps what was taken off, and the request shows ordered = qty + rejected_qty.
ALTER TABLE document_line ADD COLUMN IF NOT EXISTS rejected_qty NUMERIC(14,3) NOT NULL DEFAULT 0 CHECK (rejected_qty >= 0);

INSERT INTO pick_list (name, entries) VALUES
 ('balance_reasons', '{"Vendor cannot supply the balance","Delivery is too late, no longer needed","Bought elsewhere","Item discontinued by the vendor","Other (see remark)"}')
ON CONFLICT (name) DO NOTHING;

INSERT INTO message_template (code, name, channel, body) VALUES
 ('bal', 'Balance rejected (to requester)', 'whatsapp', '{request} for {property}: the undelivered balance ({items}) is rejected by {by}. Reason: {reason}. Raise a new request if it is still needed.'),
 ('pobal', 'Balance cancelled (to vendor)', 'email', E'Dear {vendor} team,\n\nThe undelivered balance on purchase order {po_number} for {property} is cancelled. Please do not supply these items against this PO:\n\n{items}\n\nReason: {reason}\n\nRegards,\nSaltstayz Purchase Team'),
 ('edit', 'Request edited (to approver)', 'email', E'{request} for {property} was changed by {by} before approval and is waiting for you again.\n\nChanges: {changes}\n\nValue now {value} incl. GST.')
ON CONFLICT (code) DO NOTHING;

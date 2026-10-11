-- Vendor advances. After a PO is issued, whoever can issue it may request an advance (an amount or % of the PO, with
-- the vendor's proforma). Finance approves it and marks it paid (UTR). Its number copies the PO with a count:
-- ADV_HZ_0001_01, _02 for PO_HZ_0001; ADV_HZ_0001_01_01 for PO_HZ_0001_01. At payment approval of the vendor's invoice
-- the advance is adjusted (advance_use kind 'invoice'); what is left after the PO ends is refunded by the vendor
-- (kind 'refund') or carried forward to the vendor's next invoices.
CREATE TABLE IF NOT EXISTS advance (
  id               BIGSERIAL PRIMARY KEY,
  number           TEXT NOT NULL UNIQUE,
  document_id      BIGINT NOT NULL REFERENCES document(id),
  vendor_id        BIGINT NOT NULL REFERENCES vendor(id),
  property_id      BIGINT NOT NULL REFERENCES property(id),
  amount           NUMERIC(14,2) NOT NULL CHECK (amount > 0),       -- charged against the PO, including GST
  pct              NUMERIC(6,2),                                     -- % of the PO value when requested as a %
  tds_amount       NUMERIC(14,2) NOT NULL DEFAULT 0,                 -- TDS on the advance (service lines), held back
  reason           TEXT NOT NULL DEFAULT '',
  note             TEXT NOT NULL DEFAULT '',
  proforma_file_id BIGINT REFERENCES file_object(id),
  status           TEXT NOT NULL CHECK (status IN ('PENDING','APPROVED','PAID','REJECTED','CANCELLED')),
  requested_by     BIGINT NOT NULL REFERENCES app_user(id),
  requested_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  decided_by       BIGINT REFERENCES app_user(id),
  decided_at       TIMESTAMPTZ,
  decision_note    TEXT NOT NULL DEFAULT '',
  paid_by          BIGINT REFERENCES app_user(id),
  paid_on          DATE,
  payment_ref      TEXT NOT NULL DEFAULT '',
  carry_forward    BOOLEAN NOT NULL DEFAULT FALSE                   -- left over: adjust on the vendor's other invoices
);
CREATE INDEX IF NOT EXISTS advance_document_idx ON advance (document_id);
CREATE INDEX IF NOT EXISTS advance_vendor_idx ON advance (vendor_id) WHERE status = 'PAID';

CREATE TABLE IF NOT EXISTS advance_use (
  id          BIGSERIAL PRIMARY KEY,
  advance_id  BIGINT NOT NULL REFERENCES advance(id),
  kind        TEXT NOT NULL CHECK (kind IN ('invoice','refund')),
  invoice_id  BIGINT REFERENCES invoice(id),
  amount      NUMERIC(14,2) NOT NULL CHECK (amount > 0),              -- part of the advance used, including its TDS share
  tds_amount  NUMERIC(14,2) NOT NULL DEFAULT 0,                       -- TDS already held on that part
  reference   TEXT NOT NULL DEFAULT '',
  used_on     DATE NOT NULL DEFAULT current_date,
  created_by  BIGINT REFERENCES app_user(id),
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK ((kind = 'invoice') = (invoice_id IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS advance_use_invoice_idx ON advance_use (invoice_id);
CREATE INDEX IF NOT EXISTS advance_use_advance_idx ON advance_use (advance_id);

INSERT INTO pick_list (name, entries) VALUES
 ('advance_reasons', '{"Vendor terms: advance before dispatch","Custom or made-to-order item","New vendor","Large order","Other (see note)"}')
ON CONFLICT (name) DO NOTHING;

INSERT INTO message_template (code, name, channel, body) VALUES
 ('advpaid', 'Advance paid (to vendor)', 'email', E'Dear {vendor} team,\n\nWe have paid an advance of {amount} against purchase order {po_number} for {property} on {date}. Payment reference: {reference}.\n\nAdvance number {advance}. Please arrange dispatch as agreed; the advance will be adjusted against your invoice.\n\nRegards,\nSaltstayz Purchase Team')
ON CONFLICT (code) DO NOTHING;

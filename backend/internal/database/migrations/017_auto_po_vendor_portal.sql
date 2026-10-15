-- 017: POs issued automatically per sub-category, and the delivery days they get.
ALTER TABLE sub_category ADD COLUMN auto_po BOOLEAN NOT NULL DEFAULT FALSE;
INSERT INTO setting (key, value) VALUES ('auto_po_days', '2') ON CONFLICT (key) DO NOTHING;

-- Daily consumption items have one vendor at a property; any other item may be mapped to several vendors there,
-- and the requester picks one while raising the request.
ALTER TABLE sku ADD COLUMN daily_use BOOLEAN NOT NULL DEFAULT TRUE;

-- Vendor portal: one sign-in per vendor, given (and reset) by the Purchase Team.
CREATE TABLE vendor_login (
  vendor_id     BIGINT PRIMARY KEY REFERENCES vendor(id),
  email         CITEXT NOT NULL UNIQUE,
  password_hash TEXT NOT NULL,
  must_change   BOOLEAN NOT NULL DEFAULT TRUE, -- the password the Purchase Team gave is changed at the first sign-in
  active        BOOLEAN NOT NULL DEFAULT TRUE,
  last_login_at TIMESTAMPTZ,
  created_by    BIGINT REFERENCES app_user(id),
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- What the vendor did with a PO: accepted it (it then awaits delivery) or declined it, and when it will be dispatched.
ALTER TABLE document
  ADD COLUMN vendor_status   TEXT NOT NULL DEFAULT '' CHECK (vendor_status IN ('', 'accepted', 'declined')),
  ADD COLUMN vendor_acted_at TIMESTAMPTZ,
  ADD COLUMN vendor_note     TEXT NOT NULL DEFAULT '',
  ADD COLUMN dispatch_date   DATE;

-- An invoice the vendor uploads on the portal; the store's goods receipt starts from it and then uses it.
CREATE TABLE vendor_invoice (
  id             BIGSERIAL PRIMARY KEY,
  document_id    BIGINT NOT NULL REFERENCES document(id),
  vendor_id      BIGINT NOT NULL REFERENCES vendor(id),
  invoice_number TEXT NOT NULL,
  invoice_date   DATE NOT NULL,
  amount         NUMERIC(14,2) NOT NULL,
  file_id        BIGINT NOT NULL REFERENCES file_object(id),
  note           TEXT NOT NULL DEFAULT '',
  goods_receipt_id BIGINT REFERENCES goods_receipt(id), -- set once a goods receipt used it
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX vendor_invoice_doc_idx ON vendor_invoice (document_id);
CREATE UNIQUE INDEX vendor_invoice_no_uq ON vendor_invoice (vendor_id, norm_key(invoice_number));

-- Zoho: vendors (once verified), items and properties are created or linked in Zoho automatically; each one's last
-- problem is kept so the Zoho page can show it. Vendor advances go to Zoho as vendor payments from the account set
-- on the Connection tab, and are applied to the bill they are adjusted on.
ALTER TABLE zoho_connection
  ADD COLUMN auto_masters       BOOLEAN NOT NULL DEFAULT TRUE,
  ADD COLUMN advance_account_id TEXT NOT NULL DEFAULT '';
CREATE TABLE zoho_master_sync (
  kind     TEXT NOT NULL CHECK (kind IN ('vendor', 'item', 'property')),
  ref_id   BIGINT NOT NULL,
  error    TEXT NOT NULL DEFAULT '',
  tried_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (kind, ref_id)
);
ALTER TABLE advance
  ADD COLUMN zoho_payment_id TEXT NOT NULL DEFAULT '',
  ADD COLUMN zoho_error      TEXT NOT NULL DEFAULT '',
  ADD COLUMN zoho_at         TIMESTAMPTZ;
ALTER TABLE advance_use ADD COLUMN zoho_applied BOOLEAN NOT NULL DEFAULT FALSE;

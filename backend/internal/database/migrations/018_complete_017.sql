-- 018: completes 017. A database migrated while 017 was still being written has only its first part; every statement
-- of 017 is repeated here so that it skips what already exists, and adds what is missing. On a database where 017 ran
-- in full, nothing changes.
ALTER TABLE sub_category ADD COLUMN IF NOT EXISTS auto_po BOOLEAN NOT NULL DEFAULT FALSE;
INSERT INTO setting (key, value) VALUES ('auto_po_days', '2') ON CONFLICT (key) DO NOTHING;
ALTER TABLE sku ADD COLUMN IF NOT EXISTS daily_use BOOLEAN NOT NULL DEFAULT TRUE;

CREATE TABLE IF NOT EXISTS vendor_login (
  vendor_id     BIGINT PRIMARY KEY REFERENCES vendor(id),
  email         CITEXT NOT NULL UNIQUE,
  password_hash TEXT NOT NULL,
  must_change   BOOLEAN NOT NULL DEFAULT TRUE,
  active        BOOLEAN NOT NULL DEFAULT TRUE,
  last_login_at TIMESTAMPTZ,
  created_by    BIGINT REFERENCES app_user(id),
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE document
  ADD COLUMN IF NOT EXISTS vendor_status   TEXT NOT NULL DEFAULT '' CHECK (vendor_status IN ('', 'accepted', 'declined')),
  ADD COLUMN IF NOT EXISTS vendor_acted_at TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS vendor_note     TEXT NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS dispatch_date   DATE;

CREATE TABLE IF NOT EXISTS vendor_invoice (
  id               BIGSERIAL PRIMARY KEY,
  document_id      BIGINT NOT NULL REFERENCES document(id),
  vendor_id        BIGINT NOT NULL REFERENCES vendor(id),
  invoice_number   TEXT NOT NULL,
  invoice_date     DATE NOT NULL,
  amount           NUMERIC(14,2) NOT NULL,
  file_id          BIGINT NOT NULL REFERENCES file_object(id),
  note             TEXT NOT NULL DEFAULT '',
  goods_receipt_id BIGINT REFERENCES goods_receipt(id),
  created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS vendor_invoice_doc_idx ON vendor_invoice (document_id);
CREATE UNIQUE INDEX IF NOT EXISTS vendor_invoice_no_uq ON vendor_invoice (vendor_id, norm_key(invoice_number));

ALTER TABLE zoho_connection
  ADD COLUMN IF NOT EXISTS auto_masters       BOOLEAN NOT NULL DEFAULT TRUE,
  ADD COLUMN IF NOT EXISTS advance_account_id TEXT NOT NULL DEFAULT '';
CREATE TABLE IF NOT EXISTS zoho_master_sync (
  kind     TEXT NOT NULL CHECK (kind IN ('vendor', 'item', 'property')),
  ref_id   BIGINT NOT NULL,
  error    TEXT NOT NULL DEFAULT '',
  tried_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (kind, ref_id)
);
ALTER TABLE advance
  ADD COLUMN IF NOT EXISTS zoho_payment_id TEXT NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS zoho_error      TEXT NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS zoho_at         TIMESTAMPTZ;
ALTER TABLE advance_use ADD COLUMN IF NOT EXISTS zoho_applied BOOLEAN NOT NULL DEFAULT FALSE;

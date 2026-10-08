-- Phase 3 and 4: warehouse stock and transfers, corporate purchases and bills, vendor order sheets, Zoho journals.

-- ---------- warehouse ----------
-- The warehouse is an internal vendor: properties order from it like any vendor, and the order becomes a stock transfer.
INSERT INTO vendor (code, vendor_type, legal_name, gst_registered, bank_verified, payment_terms)
SELECT 'WH', 'warehouse', 'Central Warehouse (internal)', FALSE, TRUE, 'Internal transfer'
WHERE NOT EXISTS (SELECT 1 FROM vendor WHERE vendor_type='warehouse');

CREATE TABLE warehouse_stock (
  sku_id      BIGINT PRIMARY KEY REFERENCES sku(id),
  opening_qty NUMERIC(14,3) NOT NULL DEFAULT 0,
  min_level   NUMERIC(14,3) NOT NULL DEFAULT 0,
  updated_by  BIGINT, updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE delivery_partner (
  id          BIGSERIAL PRIMARY KEY,
  name        TEXT NOT NULL,
  kind        TEXT NOT NULL CHECK (kind IN ('own','partner')),
  driver      TEXT NOT NULL DEFAULT '',
  phone       TEXT NOT NULL DEFAULT '',
  active      BOOLEAN NOT NULL DEFAULT TRUE,
  inactive_by BIGINT, inactive_at TIMESTAMPTZ, inactive_reason TEXT,
  created_by  BIGINT, created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX delivery_partner_name_uq ON delivery_partner (norm_key(name));

-- The current delivery schedule of a transfer order; version counts reschedules.
CREATE TABLE delivery (
  document_id   BIGINT PRIMARY KEY REFERENCES document(id),
  partner_id    BIGINT NOT NULL REFERENCES delivery_partner(id),
  delivery_date DATE NOT NULL,
  slot          TEXT NOT NULL,
  vehicle       TEXT NOT NULL DEFAULT '',
  note          TEXT NOT NULL DEFAULT '',
  version       INT NOT NULL DEFAULT 1,
  scheduled_by  BIGINT, scheduled_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE document ADD COLUMN transfer_received_at TIMESTAMPTZ;
ALTER TABLE document ADD COLUMN parent_id BIGINT REFERENCES document(id); -- a dispatch balance kept open becomes a new transfer order

-- ---------- corporate ----------
CREATE TABLE expense_head (
  id          BIGSERIAL PRIMARY KEY,
  name        TEXT NOT NULL,
  active      BOOLEAN NOT NULL DEFAULT TRUE,
  inactive_by BIGINT, inactive_at TIMESTAMPTZ, inactive_reason TEXT,
  created_by  BIGINT, created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX expense_head_name_uq ON expense_head (norm_key(name));

CREATE TABLE corp_department (
  id             BIGSERIAL PRIMARY KEY,
  name           TEXT NOT NULL,
  l1_user_id     BIGINT NOT NULL REFERENCES app_user(id),
  l2_user_id     BIGINT NOT NULL REFERENCES app_user(id),
  backup_user_id BIGINT REFERENCES app_user(id),
  active         BOOLEAN NOT NULL DEFAULT TRUE,
  inactive_by    BIGINT, inactive_at TIMESTAMPTZ, inactive_reason TEXT,
  created_by     BIGINT, created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK (l1_user_id <> l2_user_id),
  CHECK (backup_user_id IS NULL OR (backup_user_id <> l1_user_id AND backup_user_id <> l2_user_id))
);
CREATE UNIQUE INDEX corp_department_name_uq ON corp_department (norm_key(name));

ALTER TABLE document DROP CONSTRAINT document_kind_check;
ALTER TABLE document ADD CONSTRAINT document_kind_check CHECK (kind IN ('purchase','bill','corp_purchase','corp_bill'));
-- Approvers are fixed on a corporate request when it is raised; a later matrix change applies to new requests only.
ALTER TABLE document ADD COLUMN department_id BIGINT REFERENCES corp_department(id);
ALTER TABLE document ADD COLUMN corp_l1_user_id BIGINT REFERENCES app_user(id);
ALTER TABLE document ADD COLUMN corp_l2_user_id BIGINT REFERENCES app_user(id);
ALTER TABLE document ADD COLUMN corp_backup_user_id BIGINT REFERENCES app_user(id);
ALTER TABLE document ADD COLUMN expense_head_id BIGINT REFERENCES expense_head(id);
ALTER TABLE document ADD COLUMN quote_file_id BIGINT REFERENCES file_object(id);
CREATE SEQUENCE cr_seq START 1001;
CREATE SEQUENCE cb_seq START 2001;

-- Corporate lines have no SKU: a description, an expense head and a unit.
ALTER TABLE document_line ALTER COLUMN sku_id DROP NOT NULL;
ALTER TABLE document_line ADD COLUMN description TEXT NOT NULL DEFAULT '';
ALTER TABLE document_line ADD COLUMN unit TEXT NOT NULL DEFAULT '';
ALTER TABLE document_line ADD COLUMN expense_head_id BIGINT REFERENCES expense_head(id);
ALTER TABLE document_line ADD CONSTRAINT document_line_item_check CHECK (sku_id IS NOT NULL OR (description <> '' AND expense_head_id IS NOT NULL));

INSERT INTO role (code, name, step, rights)
SELECT x.code, x.name, x.step, x.rights::text[] FROM (VALUES
  ('creq', 'Corporate Requester', 'creq', '{raise,receive}'),
  ('dh', 'Department Head (L1)', 'dh', '{raise,approve,receive}'),
  ('capp', 'Corporate Approver (L2)', 'capp', '{approve}')) x(code,name,step,rights)
WHERE NOT EXISTS (SELECT 1 FROM role r WHERE r.code=x.code);

-- ---------- vendor order sheets ----------
CREATE TABLE vendor_sheet_log (
  id         BIGSERIAL PRIMARY KEY,
  vendor_id  BIGINT NOT NULL REFERENCES vendor(id),
  version    INT NOT NULL,
  channel    TEXT NOT NULL,
  po_numbers TEXT[] NOT NULL DEFAULT '{}',
  pending    INT NOT NULL DEFAULT 0,
  automatic  BOOLEAN NOT NULL DEFAULT FALSE,
  status     TEXT NOT NULL DEFAULT 'sent',
  sent_by    BIGINT, sent_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX vendor_sheet_log_vendor_idx ON vendor_sheet_log (vendor_id, sent_at DESC);

-- ---------- Zoho journals for warehouse issues ----------
CREATE TABLE zoho_journal (
  property_id BIGINT NOT NULL REFERENCES property(id),
  month       DATE NOT NULL,
  journal_id  TEXT NOT NULL,
  number      TEXT NOT NULL DEFAULT '',
  amount      NUMERIC(14,2) NOT NULL,
  posted_by   BIGINT, posted_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (property_id, month)
);

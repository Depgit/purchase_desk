-- Purchase Desk core schema.
-- Rules: nothing is deleted (masters carry active + inactive_* columns), every table has created_by/created_at.

CREATE EXTENSION IF NOT EXISTS citext;

CREATE OR REPLACE FUNCTION norm_key(t text) RETURNS text
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$ SELECT lower(regexp_replace(coalesce(t,''), '[^A-Za-z0-9]', '', 'g')) $$;

-- ---------- users, roles, rights ----------
CREATE TABLE app_user (
  id              BIGSERIAL PRIMARY KEY,
  name            TEXT NOT NULL,
  email           CITEXT UNIQUE,
  mobile          TEXT UNIQUE,
  password_hash   TEXT,
  department      TEXT NOT NULL DEFAULT '',
  active          BOOLEAN NOT NULL DEFAULT TRUE,
  inactive_by     BIGINT, inactive_at TIMESTAMPTZ, inactive_reason TEXT,
  created_by      BIGINT, created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK (email IS NOT NULL OR mobile IS NOT NULL)
);

-- step = where the role sits in the flow: staff,l1,l2,cc,purchase,store,ap,finance,wh,preop,creq,dh,capp,admin,'' (view only)
-- rights: raise,approve,issue_po,receive,match,pay,masters,upload
CREATE TABLE role (
  id          BIGSERIAL PRIMARY KEY,
  code        TEXT NOT NULL UNIQUE,
  name        TEXT NOT NULL UNIQUE,
  step        TEXT NOT NULL DEFAULT '',
  rights      TEXT[] NOT NULL DEFAULT '{}',
  is_custom   BOOLEAN NOT NULL DEFAULT FALSE,
  active      BOOLEAN NOT NULL DEFAULT TRUE,
  inactive_by BIGINT, inactive_at TIMESTAMPTZ, inactive_reason TEXT,
  created_by  BIGINT, created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE user_role (
  id            BIGSERIAL PRIMARY KEY,
  user_id       BIGINT NOT NULL REFERENCES app_user(id),
  role_id       BIGINT NOT NULL REFERENCES role(id),
  scope         TEXT NOT NULL DEFAULT 'all' CHECK (scope IN ('all','clusters','properties')),
  cluster_ids   BIGINT[] NOT NULL DEFAULT '{}',
  property_ids  BIGINT[] NOT NULL DEFAULT '{}',
  category_ids  BIGINT[] NOT NULL DEFAULT '{}',
  created_by    BIGINT, created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (user_id, role_id)
);

CREATE TABLE otp_code (
  id         BIGSERIAL PRIMARY KEY,
  user_id    BIGINT NOT NULL REFERENCES app_user(id),
  code_hash  TEXT NOT NULL,
  expires_at TIMESTAMPTZ NOT NULL,
  attempts   INT NOT NULL DEFAULT 0,
  used       BOOLEAN NOT NULL DEFAULT FALSE,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ---------- geography and properties ----------
CREATE TABLE city (
  id          BIGSERIAL PRIMARY KEY,
  name        TEXT NOT NULL,
  state_code  CHAR(2) NOT NULL,
  active      BOOLEAN NOT NULL DEFAULT TRUE,
  inactive_by BIGINT, inactive_at TIMESTAMPTZ, inactive_reason TEXT,
  created_by  BIGINT, created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX city_name_uq ON city (norm_key(name));

CREATE TABLE cluster (
  id          BIGSERIAL PRIMARY KEY,
  name        TEXT NOT NULL,
  city_id     BIGINT NOT NULL REFERENCES city(id),
  manager_id  BIGINT REFERENCES app_user(id),
  active      BOOLEAN NOT NULL DEFAULT TRUE,
  inactive_by BIGINT, inactive_at TIMESTAMPTZ, inactive_reason TEXT,
  created_by  BIGINT, created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX cluster_name_uq ON cluster (norm_key(name));

CREATE TABLE property (
  id          BIGSERIAL PRIMARY KEY,
  code        TEXT NOT NULL UNIQUE,
  name        TEXT NOT NULL,
  ezee_code   TEXT NOT NULL,
  cluster_id  BIGINT NOT NULL REFERENCES cluster(id),
  l1_user_id  BIGINT REFERENCES app_user(id),
  kind        TEXT NOT NULL DEFAULT 'live' CHECK (kind IN ('live','warehouse','preopening','corporate')),
  plan_pct    NUMERIC(6,2) NOT NULL DEFAULT 35,
  ird_pct     NUMERIC(6,2) NOT NULL DEFAULT 32,
  banquet_pct NUMERIC(6,2) NOT NULL DEFAULT 38,
  active      BOOLEAN NOT NULL DEFAULT TRUE,
  inactive_by BIGINT, inactive_at TIMESTAMPTZ, inactive_reason TEXT,
  created_by  BIGINT, created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX property_name_uq ON property (norm_key(name));
CREATE UNIQUE INDEX property_ezee_uq ON property (upper(ezee_code));

-- ---------- items ----------
CREATE TABLE category (
  id          BIGSERIAL PRIMARY KEY,
  name        TEXT NOT NULL,
  is_fb       BOOLEAN NOT NULL DEFAULT FALSE,
  created_by  BIGINT, created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX category_name_uq ON category (norm_key(name));

CREATE TABLE sub_category (
  id          BIGSERIAL PRIMARY KEY,
  category_id BIGINT NOT NULL REFERENCES category(id),
  name        TEXT NOT NULL,
  code        CHAR(2) NOT NULL UNIQUE,
  active      BOOLEAN NOT NULL DEFAULT TRUE,
  inactive_by BIGINT, inactive_at TIMESTAMPTZ, inactive_reason TEXT,
  created_by  BIGINT, created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX sub_category_name_uq ON sub_category (norm_key(name));

CREATE TABLE brand (
  id          BIGSERIAL PRIMARY KEY,
  name        TEXT NOT NULL,
  active      BOOLEAN NOT NULL DEFAULT TRUE,
  inactive_by BIGINT, inactive_at TIMESTAMPTZ, inactive_reason TEXT,
  created_by  BIGINT, created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX brand_name_uq ON brand (norm_key(name));

CREATE TABLE pack_size (
  id          BIGSERIAL PRIMARY KEY,
  name        TEXT NOT NULL,
  active      BOOLEAN NOT NULL DEFAULT TRUE,
  inactive_by BIGINT, inactive_at TIMESTAMPTZ, inactive_reason TEXT,
  created_by  BIGINT, created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX pack_size_name_uq ON pack_size (norm_key(name));

CREATE TABLE sku (
  id              BIGSERIAL PRIMARY KEY,
  code            TEXT NOT NULL UNIQUE,
  sub_category_id BIGINT NOT NULL REFERENCES sub_category(id),
  brand_id        BIGINT NOT NULL REFERENCES brand(id),
  item_name       TEXT NOT NULL,
  pack_size_id    BIGINT NOT NULL REFERENCES pack_size(id),
  gst_pct         NUMERIC(5,2) NOT NULL CHECK (gst_pct IN (0,5,12,18,28,40)),
  hsn             TEXT NOT NULL DEFAULT '',
  scope           TEXT NOT NULL DEFAULT 'all' CHECK (scope IN ('all','selected')),
  cluster_ids     BIGINT[] NOT NULL DEFAULT '{}',
  property_ids    BIGINT[] NOT NULL DEFAULT '{}',
  active          BOOLEAN NOT NULL DEFAULT TRUE,
  inactive_by     BIGINT, inactive_at TIMESTAMPTZ, inactive_reason TEXT,
  created_by      BIGINT, created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX sku_combo_uq ON sku (sub_category_id, brand_id, norm_key(item_name), pack_size_id);

-- ---------- vendors ----------
CREATE TABLE vendor (
  id                 BIGSERIAL PRIMARY KEY,
  code               TEXT NOT NULL UNIQUE,
  vendor_type        TEXT NOT NULL CHECK (vendor_type IN ('goods','utility','amc','other','warehouse')),
  legal_name         TEXT NOT NULL,
  address            TEXT NOT NULL DEFAULT '',
  city               TEXT NOT NULL DEFAULT '',
  pin                TEXT NOT NULL DEFAULT '',
  contact_person     TEXT NOT NULL DEFAULT '',
  mobile             TEXT NOT NULL DEFAULT '',
  email              TEXT NOT NULL DEFAULT '',
  gst_registered     BOOLEAN NOT NULL DEFAULT TRUE,
  gstin              TEXT,
  pan                TEXT,
  state_code         CHAR(2) NOT NULL DEFAULT '',
  msme               BOOLEAN NOT NULL DEFAULT FALSE,
  udyam              TEXT NOT NULL DEFAULT '',
  bank_holder        TEXT NOT NULL DEFAULT '',
  bank_account_enc   TEXT NOT NULL DEFAULT '',
  bank_account_last4 TEXT NOT NULL DEFAULT '',
  ifsc               TEXT NOT NULL DEFAULT '',
  bank_proof_file_id BIGINT,
  bank_verified      BOOLEAN NOT NULL DEFAULT FALSE,
  bank_verified_by   BIGINT, bank_verified_at TIMESTAMPTZ,
  payment_terms      TEXT NOT NULL DEFAULT 'Net 30',
  sheet_mode         TEXT NOT NULL DEFAULT 'manual' CHECK (sheet_mode IN ('manual','auto')),
  sheet_time         TEXT NOT NULL DEFAULT '18:00',
  sheet_channel      TEXT NOT NULL DEFAULT 'email' CHECK (sheet_channel IN ('email','whatsapp','both')),
  active             BOOLEAN NOT NULL DEFAULT TRUE,
  inactive_by        BIGINT, inactive_at TIMESTAMPTZ, inactive_reason TEXT,
  created_by         BIGINT, created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX vendor_name_uq ON vendor (norm_key(legal_name));
CREATE UNIQUE INDEX vendor_gstin_uq ON vendor (upper(gstin)) WHERE gstin IS NOT NULL AND gstin <> '';
CREATE UNIQUE INDEX vendor_pan_uq ON vendor (upper(pan)) WHERE pan IS NOT NULL AND pan <> '';

CREATE TABLE vendor_sub_category (
  vendor_id       BIGINT NOT NULL REFERENCES vendor(id),
  sub_category_id BIGINT NOT NULL REFERENCES sub_category(id),
  PRIMARY KEY (vendor_id, sub_category_id)
);

CREATE TABLE bill_type (
  id          BIGSERIAL PRIMARY KEY,
  name        TEXT NOT NULL,
  section     TEXT NOT NULL CHECK (section IN ('utility','amc')),
  default_gst NUMERIC(5,2) NOT NULL DEFAULT 18,
  active      BOOLEAN NOT NULL DEFAULT TRUE,
  inactive_by BIGINT, inactive_at TIMESTAMPTZ, inactive_reason TEXT,
  created_by  BIGINT, created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX bill_type_name_uq ON bill_type (norm_key(name));

CREATE TABLE vendor_bill_type (
  vendor_id    BIGINT NOT NULL REFERENCES vendor(id),
  bill_type_id BIGINT NOT NULL REFERENCES bill_type(id),
  PRIMARY KEY (vendor_id, bill_type_id)
);

CREATE TABLE rate_card (
  id          BIGSERIAL PRIMARY KEY,
  vendor_id   BIGINT NOT NULL REFERENCES vendor(id),
  sku_id      BIGINT NOT NULL REFERENCES sku(id),
  rate        NUMERIC(14,2) NOT NULL CHECK (rate > 0),
  gst_pct     NUMERIC(5,2) NOT NULL,
  valid_from  DATE NOT NULL,
  valid_to    DATE NOT NULL,
  superseded  BOOLEAN NOT NULL DEFAULT FALSE,
  created_by  BIGINT, created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK (valid_to >= valid_from)
);
CREATE INDEX rate_card_lookup ON rate_card (vendor_id, sku_id) WHERE NOT superseded;

CREATE TABLE vendor_mapping (
  id               BIGSERIAL PRIMARY KEY,
  vendor_id        BIGINT NOT NULL REFERENCES vendor(id),
  level            TEXT NOT NULL CHECK (level IN ('sku','sc')),
  sku_id           BIGINT REFERENCES sku(id),
  sub_category_id  BIGINT REFERENCES sub_category(id),
  all_properties   BOOLEAN NOT NULL DEFAULT FALSE,
  estimate_allowed BOOLEAN NOT NULL DEFAULT FALSE,
  active           BOOLEAN NOT NULL DEFAULT TRUE,
  inactive_by      BIGINT, inactive_at TIMESTAMPTZ, inactive_reason TEXT,
  created_by       BIGINT, created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK ((level='sku' AND sku_id IS NOT NULL) OR (level='sc' AND sub_category_id IS NOT NULL))
);
CREATE TABLE vendor_mapping_property (
  mapping_id  BIGINT NOT NULL REFERENCES vendor_mapping(id),
  property_id BIGINT NOT NULL REFERENCES property(id),
  PRIMARY KEY (mapping_id, property_id)
);

-- ---------- files ----------
CREATE TABLE file_object (
  id           BIGSERIAL PRIMARY KEY,
  storage_key  TEXT NOT NULL UNIQUE,
  file_name    TEXT NOT NULL,
  content_type TEXT NOT NULL DEFAULT 'application/octet-stream',
  size_bytes   BIGINT NOT NULL DEFAULT 0,
  restricted   BOOLEAN NOT NULL DEFAULT FALSE, -- bank proofs: Finance and Admin only
  created_by   BIGINT, created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ---------- budgets ----------
CREATE TABLE budget (
  property_id     BIGINT NOT NULL REFERENCES property(id),
  sub_category_id BIGINT NOT NULL REFERENCES sub_category(id),
  month           DATE NOT NULL CHECK (extract(day FROM month) = 1),
  amount          NUMERIC(14,2) NOT NULL DEFAULT 0 CHECK (amount >= 0),
  created_by      BIGINT, created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (property_id, sub_category_id, month)
);
CREATE TABLE fb_sale (
  property_id  BIGINT NOT NULL REFERENCES property(id),
  sale_date    DATE NOT NULL,
  plan_sale    NUMERIC(14,2) NOT NULL DEFAULT 0,
  ird_sale     NUMERIC(14,2) NOT NULL DEFAULT 0,
  banquet_sale NUMERIC(14,2) NOT NULL DEFAULT 0,
  created_by   BIGINT, created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (property_id, sale_date)
);

-- ---------- documents (purchase requests, bills, warehouse transfers) ----------
CREATE SEQUENCE pr_seq START 1001;
CREATE SEQUENCE po_seq START 2001;
CREATE SEQUENCE br_seq START 3001;
CREATE SEQUENCE wt_seq START 5001;
CREATE SEQUENCE grn_seq START 7001;

CREATE TABLE document (
  id               BIGSERIAL PRIMARY KEY,
  number           TEXT NOT NULL UNIQUE,
  kind             TEXT NOT NULL CHECK (kind IN ('purchase','bill')),
  property_id      BIGINT NOT NULL REFERENCES property(id),
  vendor_id        BIGINT NOT NULL REFERENCES vendor(id),
  requester_id     BIGINT NOT NULL REFERENCES app_user(id),
  requester_step   TEXT NOT NULL DEFAULT 'staff',
  status           TEXT NOT NULL,
  route_steps      TEXT[] NOT NULL DEFAULT '{}',
  step_idx         INT NOT NULL DEFAULT 0,
  event_reason     TEXT NOT NULL DEFAULT '',
  event_note       TEXT NOT NULL DEFAULT '',
  routed_reason    TEXT NOT NULL DEFAULT '',
  routed_note      TEXT NOT NULL DEFAULT '',
  cm_approved_at   TIMESTAMPTZ,
  preop_user_id    BIGINT REFERENCES app_user(id),
  return_reason    TEXT NOT NULL DEFAULT '',
  return_note      TEXT NOT NULL DEFAULT '',
  modified_note    TEXT NOT NULL DEFAULT '',
  modified_by      TEXT NOT NULL DEFAULT '',
  modified_at      TIMESTAMPTZ,
  approved_at      TIMESTAMPTZ,
  po_number        TEXT UNIQUE,
  po_date          TIMESTAMPTZ,
  deliver_by       DATE,
  payment_terms    TEXT NOT NULL DEFAULT '',
  po_closed_short  BOOLEAN NOT NULL DEFAULT FALSE,
  -- warehouse transfer
  is_transfer      BOOLEAN NOT NULL DEFAULT FALSE,
  dispatched_at    TIMESTAMPTZ,
  wait_until       DATE,
  wait_note        TEXT NOT NULL DEFAULT '',
  -- utility and AMC bills
  bill_type_id     BIGINT REFERENCES bill_type(id),
  period_start     DATE,
  months           INT,
  remark           TEXT NOT NULL DEFAULT '',
  created_by       BIGINT, created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX document_status_idx ON document (status);
CREATE INDEX document_property_idx ON document (property_id);
CREATE INDEX document_vendor_idx ON document (vendor_id);

CREATE TABLE document_line (
  id             BIGSERIAL PRIMARY KEY,
  document_id    BIGINT NOT NULL REFERENCES document(id),
  sku_id         BIGINT NOT NULL REFERENCES sku(id),
  qty            NUMERIC(14,3) NOT NULL CHECK (qty >= 0),   -- current (approved) quantity
  original_qty   NUMERIC(14,3),                             -- set when an approver changes it
  rate           NUMERIC(14,2) NOT NULL,
  gst_pct        NUMERIC(5,2) NOT NULL,
  estimated      BOOLEAN NOT NULL DEFAULT FALSE,
  dispatched_qty NUMERIC(14,3),
  received_qty   NUMERIC(14,3) NOT NULL DEFAULT 0,
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX document_line_doc_idx ON document_line (document_id);

CREATE TABLE document_log (
  id          BIGSERIAL PRIMARY KEY,
  document_id BIGINT NOT NULL REFERENCES document(id),
  user_id     BIGINT REFERENCES app_user(id),
  user_name   TEXT NOT NULL,
  role_name   TEXT NOT NULL,
  action      TEXT NOT NULL,
  note        TEXT NOT NULL DEFAULT '',
  at          TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX document_log_doc_idx ON document_log (document_id);

-- spend counted on the day a request is finally approved
CREATE TABLE budget_spend (
  id              BIGSERIAL PRIMARY KEY,
  property_id     BIGINT NOT NULL REFERENCES property(id),
  sub_category_id BIGINT NOT NULL REFERENCES sub_category(id),
  spend_date      DATE NOT NULL,
  amount          NUMERIC(14,2) NOT NULL,
  document_id     BIGINT REFERENCES document(id),
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX budget_spend_idx ON budget_spend (property_id, sub_category_id, spend_date);

CREATE TABLE goods_receipt (
  id               BIGSERIAL PRIMARY KEY,
  number           TEXT NOT NULL UNIQUE,
  document_id      BIGINT NOT NULL REFERENCES document(id),
  delivery_no      INT NOT NULL,
  received_by      BIGINT NOT NULL REFERENCES app_user(id),
  received_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  photo_file_ids   BIGINT[] NOT NULL DEFAULT '{}',
  note             TEXT NOT NULL DEFAULT '',
  voided           BOOLEAN NOT NULL DEFAULT FALSE,  -- sent back by Accounts Payable for a recount
  void_reason      TEXT NOT NULL DEFAULT ''
);
CREATE TABLE goods_receipt_line (
  goods_receipt_id BIGINT NOT NULL REFERENCES goods_receipt(id),
  line_id          BIGINT NOT NULL REFERENCES document_line(id),
  qty              NUMERIC(14,3) NOT NULL CHECK (qty >= 0),
  PRIMARY KEY (goods_receipt_id, line_id)
);

-- One vendor invoice = one payable = one Zoho bill.
-- Purchases: one per goods receipt. Utility/AMC bills: one per bill document.
CREATE TABLE invoice (
  id               BIGSERIAL PRIMARY KEY,
  document_id      BIGINT NOT NULL REFERENCES document(id),
  goods_receipt_id BIGINT UNIQUE REFERENCES goods_receipt(id),
  vendor_id        BIGINT NOT NULL REFERENCES vendor(id),
  property_id      BIGINT NOT NULL REFERENCES property(id),
  invoice_number   TEXT NOT NULL,
  invoice_date     DATE NOT NULL,
  amount           NUMERIC(14,2) NOT NULL CHECK (amount > 0),
  gst_pct          NUMERIC(5,2),
  file_id          BIGINT REFERENCES file_object(id),
  status           TEXT NOT NULL CHECK (status IN ('AWAITING','RECEIVED','MATCHED','PAY_APPROVED','PUSHED','PAID','VOID')),
  match_remark     TEXT NOT NULL DEFAULT '',
  matched_by       BIGINT, matched_at TIMESTAMPTZ,
  pay_approved_by  BIGINT, pay_approved_at TIMESTAMPTZ,
  due_date         DATE,
  zoho_bill_id     TEXT,
  zoho_step        INT NOT NULL DEFAULT 0, -- 1 bill saved, 2 attachment sent, 3 marked open
  zoho_voids       INT NOT NULL DEFAULT 0, -- times the bill was voided in Zoho; a fresh push uses a new unique key
  zoho_status      TEXT NOT NULL DEFAULT '',
  zoho_balance     NUMERIC(14,2),
  zoho_paid        NUMERIC(14,2),
  zoho_error       TEXT NOT NULL DEFAULT '',
  zoho_pushed_at   TIMESTAMPTZ,
  paid_at          TIMESTAMPTZ,
  payment_ref      TEXT NOT NULL DEFAULT '',
  created_by       BIGINT, created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX invoice_vendor_number_uq ON invoice (vendor_id, norm_key(invoice_number)) WHERE status <> 'VOID';
CREATE INDEX invoice_status_idx ON invoice (status);

-- ---------- settings, logs, preferences ----------
CREATE TABLE setting (
  key        TEXT PRIMARY KEY,
  value      JSONB NOT NULL,
  updated_by BIGINT, updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE pick_list (
  name       TEXT PRIMARY KEY,
  entries    TEXT[] NOT NULL,
  updated_by BIGINT, updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE message_template (
  code       TEXT PRIMARY KEY,
  name       TEXT NOT NULL,
  channel    TEXT NOT NULL CHECK (channel IN ('email','whatsapp')),
  body       TEXT NOT NULL,
  updated_by BIGINT, updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE user_pref (
  user_id BIGINT NOT NULL REFERENCES app_user(id),
  screen  TEXT NOT NULL,
  value   JSONB NOT NULL,
  PRIMARY KEY (user_id, screen)
);

CREATE TABLE activity_log (
  id        BIGSERIAL PRIMARY KEY,
  user_id   BIGINT,
  user_name TEXT NOT NULL,
  role_name TEXT NOT NULL,
  module    TEXT NOT NULL,
  action    TEXT NOT NULL,
  record    TEXT NOT NULL DEFAULT '',
  details   TEXT NOT NULL DEFAULT '',
  at        TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX activity_log_at_idx ON activity_log (at DESC);

CREATE OR REPLACE FUNCTION forbid_change() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION '% cannot be edited or deleted', TG_TABLE_NAME; END $$;
CREATE TRIGGER activity_log_immutable BEFORE UPDATE OR DELETE ON activity_log FOR EACH ROW EXECUTE FUNCTION forbid_change();
CREATE TRIGGER document_log_immutable BEFORE UPDATE OR DELETE ON document_log FOR EACH ROW EXECUTE FUNCTION forbid_change();

CREATE TABLE message_log (
  id         BIGSERIAL PRIMARY KEY,
  channel    TEXT NOT NULL,
  recipient  TEXT NOT NULL,
  subject    TEXT NOT NULL DEFAULT '',
  body       TEXT NOT NULL,
  status     TEXT NOT NULL, -- sent, failed, logged (no provider configured)
  error      TEXT NOT NULL DEFAULT '',
  ref        TEXT NOT NULL DEFAULT '',
  created_by BIGINT, created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ---------- Zoho Books ----------
CREATE TABLE zoho_connection (
  id                INT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
  accounts_url      TEXT NOT NULL,
  api_url           TEXT NOT NULL,
  org_id            TEXT NOT NULL DEFAULT '',
  org_name          TEXT NOT NULL DEFAULT '',
  plan_name         TEXT NOT NULL DEFAULT '',
  client_id         TEXT NOT NULL DEFAULT '',
  client_secret_enc TEXT NOT NULL DEFAULT '',
  refresh_token_enc TEXT NOT NULL DEFAULT '',
  connected         BOOLEAN NOT NULL DEFAULT FALSE,
  last_error        TEXT NOT NULL DEFAULT '',
  property_tag_id   TEXT NOT NULL DEFAULT '',
  bill_key_field    TEXT NOT NULL DEFAULT 'cf_purchase_desk_id',
  last_sync_at      TIMESTAMPTZ,
  last_payment_read_at TIMESTAMPTZ,
  tested_at         TIMESTAMPTZ,
  updated_by        BIGINT, updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE zoho_account (account_id TEXT PRIMARY KEY, name TEXT NOT NULL, code TEXT NOT NULL DEFAULT '', account_type TEXT NOT NULL, active BOOLEAN NOT NULL, synced_at TIMESTAMPTZ NOT NULL DEFAULT now());
CREATE TABLE zoho_tax (tax_id TEXT PRIMARY KEY, name TEXT NOT NULL, pct NUMERIC(5,2) NOT NULL, tax_type TEXT NOT NULL, specification TEXT NOT NULL DEFAULT '', active BOOLEAN NOT NULL, synced_at TIMESTAMPTZ NOT NULL DEFAULT now());
CREATE TABLE zoho_tag (tag_id TEXT PRIMARY KEY, name TEXT NOT NULL, active BOOLEAN NOT NULL DEFAULT TRUE, synced_at TIMESTAMPTZ NOT NULL DEFAULT now());
CREATE TABLE zoho_tag_option (option_id TEXT PRIMARY KEY, tag_id TEXT NOT NULL, name TEXT NOT NULL, active BOOLEAN NOT NULL DEFAULT TRUE, synced_at TIMESTAMPTZ NOT NULL DEFAULT now());
CREATE TABLE zoho_location (location_id TEXT PRIMARY KEY, name TEXT NOT NULL, gstin TEXT NOT NULL DEFAULT '', state_code TEXT NOT NULL DEFAULT '', active BOOLEAN NOT NULL DEFAULT TRUE, synced_at TIMESTAMPTZ NOT NULL DEFAULT now());
CREATE TABLE zoho_contact (contact_id TEXT PRIMARY KEY, name TEXT NOT NULL, company TEXT NOT NULL DEFAULT '', gst_no TEXT NOT NULL DEFAULT '', gst_treatment TEXT NOT NULL DEFAULT '', state_code TEXT NOT NULL DEFAULT '', active BOOLEAN NOT NULL DEFAULT TRUE, synced_at TIMESTAMPTZ NOT NULL DEFAULT now());
CREATE INDEX zoho_contact_gst_idx ON zoho_contact (upper(gst_no));

CREATE TABLE zoho_map_property (
  property_id   BIGINT PRIMARY KEY REFERENCES property(id),
  tag_option_id TEXT,
  location_id   TEXT,
  updated_by BIGINT, updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- kind: subcat (ref=sub_category id), billtype (ref=bill_type id), special (ref=warehouse|preopening|loss), head (ref=expense_head id)
CREATE TABLE zoho_map_account (
  kind       TEXT NOT NULL,
  ref        TEXT NOT NULL,
  account_id TEXT NOT NULL,
  updated_by BIGINT, updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (kind, ref)
);
CREATE TABLE zoho_map_tax (
  gst_pct    NUMERIC(5,2) NOT NULL,
  supply     TEXT NOT NULL CHECK (supply IN ('intra','inter')),
  tax_id     TEXT NOT NULL,
  updated_by BIGINT, updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (gst_pct, supply)
);
CREATE TABLE zoho_map_vendor (
  vendor_id  BIGINT PRIMARY KEY REFERENCES vendor(id),
  contact_id TEXT NOT NULL UNIQUE,
  how        TEXT NOT NULL CHECK (how IN ('gstin','name','manual','created')),
  updated_by BIGINT, updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

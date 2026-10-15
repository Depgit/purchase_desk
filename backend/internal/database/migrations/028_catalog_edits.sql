-- 028: who keeps the catalogue, and Finance's approval of edits.
--  * New right "catalog": create and edit categories, sub-categories, brands, pack sizes, items and vendors. Accounts
--    Payable gets it; other roles keep "masters" for properties, vendor mapping, rates and bill types.
--  * Editing an approved category, sub-category or item puts it back to pending (unusable) until Finance approves the
--    change; fin_change says what changed and fin_before holds the values before it, so a rejected edit is undone.
--    Editing a vendor sends it back to Finance's verification the same way (vendor.fin_change).
--  * An approved edit of an item or vendor that is already in Zoho is sent to Zoho (zoho_update_due).
--  * vendor_change: changes a vendor asks for on the vendor portal, for Finance to approve.
UPDATE role SET rights = array_append(rights, 'catalog') WHERE step = 'ap' AND NOT 'catalog' = ANY(rights);

ALTER TABLE category     ADD COLUMN IF NOT EXISTS fin_change TEXT NOT NULL DEFAULT '', ADD COLUMN IF NOT EXISTS fin_before JSONB;
ALTER TABLE sub_category ADD COLUMN IF NOT EXISTS fin_change TEXT NOT NULL DEFAULT '', ADD COLUMN IF NOT EXISTS fin_before JSONB;
ALTER TABLE sku          ADD COLUMN IF NOT EXISTS fin_change TEXT NOT NULL DEFAULT '', ADD COLUMN IF NOT EXISTS fin_before JSONB,
                         ADD COLUMN IF NOT EXISTS zoho_update_due BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE vendor       ADD COLUMN IF NOT EXISTS fin_change TEXT NOT NULL DEFAULT '',
                         ADD COLUMN IF NOT EXISTS zoho_update_due BOOLEAN NOT NULL DEFAULT FALSE;

ALTER TABLE zoho_master_sync DROP CONSTRAINT IF EXISTS zoho_master_sync_kind_check;
ALTER TABLE zoho_master_sync ADD CONSTRAINT zoho_master_sync_kind_check
  CHECK (kind IN ('vendor','item','property','subcat','billtype','head','vendor_update','item_update'));

CREATE TABLE IF NOT EXISTS vendor_change (
  id                 BIGSERIAL PRIMARY KEY,
  vendor_id          BIGINT NOT NULL REFERENCES vendor(id),
  data               JSONB NOT NULL,              -- the details the vendor asks for (the vendor edit form's fields)
  changes            TEXT NOT NULL DEFAULT '',    -- what differs from the vendor's current details, in words
  bank_proof_file_id BIGINT REFERENCES file_object(id),
  msme_file_id       BIGINT REFERENCES file_object(id),
  status             TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','approved','rejected','withdrawn')),
  asked_by           TEXT NOT NULL DEFAULT '',    -- the vendor portal email
  asked_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
  decided_by         BIGINT REFERENCES app_user(id),
  decided_at         TIMESTAMPTZ,
  decision_note      TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX IF NOT EXISTS vendor_change_one_pending ON vendor_change (vendor_id) WHERE status = 'pending';

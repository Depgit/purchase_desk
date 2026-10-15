-- 023: Finance approves every new master that goes to Zoho. Until approved it cannot be used (no requests, rates,
-- mappings, items under it...) and is not sent to Zoho; once approved it is created (or linked) in Zoho. New rows
-- start pending; everything that exists now counts as approved. Vendors keep their bank verification as the approval.
ALTER TABLE sku          ADD COLUMN IF NOT EXISTS fin_status TEXT NOT NULL DEFAULT 'pending' CHECK (fin_status IN ('pending','approved','rejected')),
                         ADD COLUMN IF NOT EXISTS fin_by BIGINT REFERENCES app_user(id), ADD COLUMN IF NOT EXISTS fin_at TIMESTAMPTZ, ADD COLUMN IF NOT EXISTS fin_note TEXT NOT NULL DEFAULT '';
ALTER TABLE category     ADD COLUMN IF NOT EXISTS fin_status TEXT NOT NULL DEFAULT 'pending' CHECK (fin_status IN ('pending','approved','rejected')),
                         ADD COLUMN IF NOT EXISTS fin_by BIGINT REFERENCES app_user(id), ADD COLUMN IF NOT EXISTS fin_at TIMESTAMPTZ, ADD COLUMN IF NOT EXISTS fin_note TEXT NOT NULL DEFAULT '';
ALTER TABLE sub_category ADD COLUMN IF NOT EXISTS fin_status TEXT NOT NULL DEFAULT 'pending' CHECK (fin_status IN ('pending','approved','rejected')),
                         ADD COLUMN IF NOT EXISTS fin_by BIGINT REFERENCES app_user(id), ADD COLUMN IF NOT EXISTS fin_at TIMESTAMPTZ, ADD COLUMN IF NOT EXISTS fin_note TEXT NOT NULL DEFAULT '';
ALTER TABLE property     ADD COLUMN IF NOT EXISTS fin_status TEXT NOT NULL DEFAULT 'pending' CHECK (fin_status IN ('pending','approved','rejected')),
                         ADD COLUMN IF NOT EXISTS fin_by BIGINT REFERENCES app_user(id), ADD COLUMN IF NOT EXISTS fin_at TIMESTAMPTZ, ADD COLUMN IF NOT EXISTS fin_note TEXT NOT NULL DEFAULT '';
ALTER TABLE bill_type    ADD COLUMN IF NOT EXISTS fin_status TEXT NOT NULL DEFAULT 'pending' CHECK (fin_status IN ('pending','approved','rejected')),
                         ADD COLUMN IF NOT EXISTS fin_by BIGINT REFERENCES app_user(id), ADD COLUMN IF NOT EXISTS fin_at TIMESTAMPTZ, ADD COLUMN IF NOT EXISTS fin_note TEXT NOT NULL DEFAULT '';
ALTER TABLE expense_head ADD COLUMN IF NOT EXISTS fin_status TEXT NOT NULL DEFAULT 'pending' CHECK (fin_status IN ('pending','approved','rejected')),
                         ADD COLUMN IF NOT EXISTS fin_by BIGINT REFERENCES app_user(id), ADD COLUMN IF NOT EXISTS fin_at TIMESTAMPTZ, ADD COLUMN IF NOT EXISTS fin_note TEXT NOT NULL DEFAULT '';
UPDATE sku SET fin_status='approved' WHERE fin_status='pending';
UPDATE category SET fin_status='approved' WHERE fin_status='pending';
UPDATE sub_category SET fin_status='approved' WHERE fin_status='pending';
UPDATE property SET fin_status='approved' WHERE fin_status='pending';
UPDATE bill_type SET fin_status='approved' WHERE fin_status='pending';
UPDATE expense_head SET fin_status='approved' WHERE fin_status='pending';

-- The Zoho masters sync also creates the expense account of an approved sub-category, bill type or expense head.
ALTER TABLE zoho_master_sync DROP CONSTRAINT IF EXISTS zoho_master_sync_kind_check;
ALTER TABLE zoho_master_sync ADD CONSTRAINT zoho_master_sync_kind_check CHECK (kind IN ('vendor','item','property','subcat','billtype','head'));

-- Brand becomes optional (services have no brand), and vendors can carry an MSME certificate file.
ALTER TABLE sku ALTER COLUMN brand_id DROP NOT NULL;
DROP INDEX IF EXISTS sku_combo_uq;
CREATE UNIQUE INDEX sku_combo_uq ON sku (sub_category_id, coalesce(brand_id, 0), norm_key(item_name), pack_size_id);
ALTER TABLE vendor ADD COLUMN IF NOT EXISTS msme_file_id BIGINT REFERENCES file_object(id);

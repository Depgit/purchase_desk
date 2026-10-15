-- 024: items stocked at the Central Warehouse. A property's request for such an item always goes to the warehouse
-- (a transfer); the warehouse buys a shortfall from the vendor itself.
ALTER TABLE sku ADD COLUMN IF NOT EXISTS wh_stocked BOOLEAN NOT NULL DEFAULT FALSE;

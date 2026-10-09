-- Items read from Zoho Books on every sync (GET /items), for reference next to Purchase Desk's own SKUs.
CREATE TABLE IF NOT EXISTS zoho_item (
  item_id             TEXT PRIMARY KEY,
  name                TEXT NOT NULL,
  sku                 TEXT NOT NULL DEFAULT '',
  hsn_or_sac          TEXT NOT NULL DEFAULT '',
  unit                TEXT NOT NULL DEFAULT '',
  product_type        TEXT NOT NULL DEFAULT '',   -- goods or service
  item_type           TEXT NOT NULL DEFAULT '',   -- sales, purchases, sales_and_purchases, inventory
  brand               TEXT NOT NULL DEFAULT '',
  purchase_rate       NUMERIC(14,2) NOT NULL DEFAULT 0,
  tax_pct             NUMERIC(5,2) NOT NULL DEFAULT 0,
  purchase_account_id TEXT NOT NULL DEFAULT '',
  active              BOOLEAN NOT NULL DEFAULT TRUE,
  synced_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Each Purchase Desk item (SKU) can be linked to one Zoho Books item; the bill line then carries the Zoho item.
CREATE TABLE IF NOT EXISTS zoho_map_item (
  sku_id     BIGINT PRIMARY KEY REFERENCES sku(id),
  item_id    TEXT NOT NULL,
  how        TEXT NOT NULL DEFAULT 'manual' CHECK (how IN ('manual','name')),
  updated_by BIGINT, updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

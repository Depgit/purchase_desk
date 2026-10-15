package zohopush

import (
	"context"

	"purchasedesk/internal/zohoapi"
)

// alignToPO makes each bill line that bills a Zoho purchase order line carry the same Zoho item as that PO line (or no
// item when the PO line has none). Zoho links a bill to a PO only through common items, and an item linked in Purchase
// Desk after the PO went to Zoho would otherwise differ (Zoho error 4197). A PO line Zoho no longer has is not quoted;
// with no line left to bill, the bill does not quote the PO at all. It costs one call, only for a bill on a PO.
func alignToPO(ctx context.Context, c *zohoapi.Client, p map[string]any, poID string) {
	lines, _ := p["line_items"].([]map[string]any)
	if poID == "" || len(lines) == 0 {
		return
	}
	po, err := c.GetPurchaseOrder(ctx, poID)
	if err != nil {
		unlinkPO(p)
		return
	}
	type poLine struct{ item, account string }
	have := map[string]poLine{}
	for _, l := range po.LineItems {
		have[l.ID] = poLine{l.ItemID, l.AccountID}
	}
	linked := 0
	for _, it := range lines {
		id, _ := it["purchaseorder_item_id"].(string)
		if id == "" {
			continue
		}
		pl, ok := have[id]
		if !ok {
			delete(it, "purchaseorder_item_id")
			continue
		}
		if pl.item == "" {
			delete(it, "item_id")
		} else {
			it["item_id"] = pl.item
		}
		if pl.account != "" {
			it["account_id"] = pl.account
		}
		linked++
	}
	if linked == 0 {
		unlinkPO(p)
	}
}

// unlinkPO takes the purchase order out of a bill: the bill then stands on its own in Zoho, with the PO number still
// in its reference.
func unlinkPO(p map[string]any) {
	delete(p, "purchaseorder_ids")
	lines, _ := p["line_items"].([]map[string]any)
	for _, it := range lines {
		delete(it, "purchaseorder_item_id")
	}
}

package masterapproval

// Edits of approved masters. Editing an approved category, sub-category or item saves the change and puts the record
// back to pending: it cannot be used (no new requests, no items under it) until Finance approves the change. The values
// before the edit are kept (fin_before), so Finance can reject the edit and put them back; fin_change says what
// changed. An approved edit of an item already in Zoho is sent to Zoho (zoho_update_due). Vendors follow the same rule
// through their bank verification (see vendors.Update).

import (
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"strings"

	"purchasedesk/internal/access"
	"purchasedesk/internal/auditlog"
	"purchasedesk/internal/database"
)

// bookkeeping columns are not "changes" a user made.
var bookkeeping = map[string]bool{"id": true, "fin_status": true, "fin_by": true, "fin_at": true, "fin_note": true, "fin_change": true, "fin_before": true,
	"created_by": true, "created_at": true, "updated_at": true, "updated_by": true, "inactive_by": true, "inactive_at": true, "inactive_reason": true,
	"active": true, "zoho_update_due": true, "bank_verified": true, "bank_verified_by": true, "bank_verified_at": true, "bank_account_last4": true,
	"sheet_mode": true, "sheet_time": true, "sheet_channel": true, "code": true, "doc_seq": true}

// labels name a changed column the way the screens do.
var labels = map[string]string{"item_name": "name", "gst_pct": "GST %", "hsn": "HSN/SAC", "brand_id": "brand", "pack_size_id": "pack size",
	"sub_category_id": "sub-category", "category_id": "category", "scope": "available at", "cluster_ids": "available at", "property_ids": "available at",
	"tds_section": "TDS", "tds_pct": "TDS", "daily_use": "daily consumption", "wh_stocked": "stocked at warehouse", "auto_po": "auto PO",
	"is_fb": "food & beverage", "legal_name": "legal name", "contact_person": "contact person", "gst_registered": "GST status", "gstin": "GSTIN",
	"pan": "PAN", "state_code": "state code", "msme": "MSME", "udyam": "Udyam number", "msme_file_id": "MSME certificate", "bank_holder": "account holder",
	"bank_account_enc": "bank account", "ifsc": "IFSC", "bank_proof_file_id": "bank proof", "payment_terms": "payment terms", "vendor_type": "vendor type",
	"pin": "PIN code", "supplies": "what they supply", "opening_balance": "opening balance", "opening_as_on": "opening balance date"}

// Snapshot is a master's values now, to compare an edit with (table is the record's table).
func Snapshot(ctx context.Context, q database.Q, table string, id int64) (map[string]any, error) {
	var raw []byte
	if err := q.QueryRow(ctx, `SELECT to_jsonb(t) FROM `+table+` t WHERE id=$1`, id).Scan(&raw); err != nil {
		return nil, err
	}
	out := map[string]any{}
	return out, json.Unmarshal(raw, &out)
}

// Changed lists, in words, what differs between two snapshots (bookkeeping columns left out).
func Changed(before, after map[string]any) []string {
	seen := map[string]bool{}
	out := []string{}
	for k, v := range after {
		if bookkeeping[k] || reflect.DeepEqual(before[k], v) {
			continue
		}
		l := labels[k]
		if l == "" {
			l = strings.ReplaceAll(k, "_", " ")
		}
		if !seen[l] {
			seen[l] = true
			out = append(out, l)
		}
	}
	sort.Strings(out)
	return out
}

// Edited is called inside the save, after an edit of a category, sub-category or item (kind as in tables). If the
// edit changed anything, an approved record waits for Finance again with the values before it kept; a record still
// waiting adds the change to what Finance sees. It reports whether the record now waits for Finance because of it.
func Edited(ctx context.Context, q database.Q, u *access.User, kind string, id int64, before map[string]any) (bool, error) {
	t, ok := tables[kind]
	if !ok {
		return false, nil
	}
	after, err := Snapshot(ctx, q, t.table, id)
	if err != nil {
		return false, err
	}
	changed := Changed(before, after)
	if len(changed) == 0 {
		return false, nil
	}
	what := "Edited by " + u.Name + ": " + strings.Join(changed, ", ")
	status, _ := before["fin_status"].(string)
	switch status {
	case "approved":
		keep := map[string]any{}
		for k, v := range before {
			if !bookkeeping[k] {
				keep[k] = v
			}
		}
		js, err := json.Marshal(keep)
		if err != nil {
			return false, err
		}
		if _, err := q.Exec(ctx, `UPDATE `+t.table+` SET fin_status='pending', fin_by=NULL, fin_at=NULL, fin_change=$2, fin_before=$3 WHERE id=$1`, id, what, js); err != nil {
			return false, err
		}
	case "pending":
		// Waiting already (new, or an earlier edit): Finance sees this change too; the values before the first edit stay.
		if _, err := q.Exec(ctx, `UPDATE `+t.table+` SET fin_change=CASE WHEN fin_before IS NULL THEN fin_change ELSE concat_ws('; ', nullif(fin_change,''), $2::text) END WHERE id=$1`, id, what); err != nil {
			return false, err
		}
		return false, nil
	default:
		return false, nil
	}
	name, _ := after["name"].(string)
	if n, ok := after["item_name"].(string); ok {
		name = n
	}
	return true, auditlog.Write(ctx, q, u, "Masters", t.label+" edited, waits for Finance", name, strings.Join(changed, ", "))
}

// restore puts back the values a rejected edit replaced.
func restore(ctx context.Context, q database.Q, table string, id int64, before []byte) error {
	m := map[string]any{}
	if err := json.Unmarshal(before, &m); err != nil {
		return err
	}
	cols := []string{}
	for k := range m {
		if !bookkeeping[k] {
			cols = append(cols, k)
		}
	}
	if len(cols) == 0 {
		return nil
	}
	sort.Strings(cols)
	list := strings.Join(cols, ",")
	_, err := q.Exec(ctx, `UPDATE `+table+` SET (`+list+`) = (SELECT `+list+` FROM jsonb_populate_record(NULL::`+table+`, $2::jsonb)) WHERE id=$1`, id, before)
	return err
}

// WaitNote is the message after saving a master: a changed approved one now waits for Finance (blank otherwise).
func WaitNote(ctx context.Context, q database.Q, kind string, id int64) string {
	t, ok := tables[kind]
	if !ok {
		return ""
	}
	var waits bool
	_ = q.QueryRow(ctx, `SELECT fin_status='pending' AND fin_before IS NOT NULL FROM `+t.table+` WHERE id=$1`, id).Scan(&waits)
	if !waits {
		return ""
	}
	return "The change waits for Finance's approval; until then this " + strings.ToLower(t.label) + " cannot be used."
}

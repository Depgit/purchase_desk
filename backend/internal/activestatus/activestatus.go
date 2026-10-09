// Package activestatus marks master records active or inactive (nothing is ever deleted)
// and tells the user what marking a record inactive will affect before they confirm.
package activestatus

import (
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"purchasedesk/internal/access"
	"purchasedesk/internal/auditlog"
	"purchasedesk/internal/httpjson"
	"purchasedesk/internal/textclean"
)

type Service struct{ DB *pgxpool.Pool }

func (s *Service) Routes(r chi.Router) {
	r.Post("/masters/{kind}/{id}/active", httpjson.Wrap(s.setActive))
	r.Get("/masters/{kind}/{id}/impact", httpjson.Wrap(s.inactiveImpact))
}

var activeKinds = map[string]struct{ table, label, module string }{
	"city":         {"city", "name", "Properties"},
	"cluster":      {"cluster", "name", "Properties"},
	"property":     {"property", "name", "Properties"},
	"sub_category": {"sub_category", "name", "Items"},
	"brand":        {"brand", "name", "Items"},
	"pack_size":    {"pack_size", "name", "Items"},
	"sku":          {"sku", "code||' '||item_name", "Items"},
	"vendor":       {"vendor", "legal_name", "Vendors"},
	"bill_type":    {"bill_type", "name", "Bills"},
	"mapping":      {"vendor_mapping", "id::text", "Vendor Mapping"},
	"user":         {"app_user", "name", "Users"},
	"role":         {"role", "name", "Users"},
	"department":   {"corp_department", "name", "Corporate"},
	"expense_head": {"expense_head", "name", "Corporate"},
	"partner":      {"delivery_partner", "name", "Warehouse"},
}

func (s *Service) setActive(w http.ResponseWriter, r *http.Request) error {
	p := access.From(r)
	kind := chi.URLParam(r, "kind")
	k, ok := activeKinds[kind]
	if !ok {
		return httpjson.NotFound("Kind")
	}
	if kind == "role" || kind == "user" {
		if err := access.NeedAdmin(p); err != nil {
			return err
		}
	} else if err := access.NeedRight(p, access.Masters, "edit masters"); err != nil {
		return err
	}
	id, err := httpjson.ID(r, "id")
	if err != nil {
		return err
	}
	var in struct {
		Active bool   `json:"active"`
		Reason string `json:"reason"`
	}
	if err := httpjson.Decode(r, &in); err != nil {
		return err
	}
	if kind == "user" && id == p.ID && !in.Active {
		return httpjson.Bad("You cannot mark yourself inactive.")
	}
	if kind == "brand" && !in.Active {
		var bk string
		_ = s.DB.QueryRow(r.Context(), `SELECT kind FROM brand WHERE id=$1`, id).Scan(&bk)
		if bk == "other" || bk == "service" {
			return httpjson.Bad("Other brand and Service are fixed choices and stay active.")
		}
	}
	var label string
	q := fmt.Sprintf(`UPDATE %s SET active=$2, inactive_by=CASE WHEN $2 THEN NULL ELSE $3::bigint END, inactive_at=CASE WHEN $2 THEN NULL ELSE now() END,
		inactive_reason=CASE WHEN $2 THEN NULL ELSE $4 END WHERE id=$1 RETURNING %s`, k.table, k.label)
	if err := s.DB.QueryRow(r.Context(), q, id, in.Active, p.ID, textclean.Clean(in.Reason)).Scan(&label); err != nil {
		return err
	}
	act := "Marked inactive"
	if in.Active {
		act = "Marked active"
	}
	_ = auditlog.Write(r.Context(), s.DB, p, k.module, act, kind+" "+label, textclean.Clean(in.Reason))
	return httpjson.OK(w, map[string]bool{"ok": true})
}

// impact tells the user what marking a record inactive does, before they confirm.
func (s *Service) inactiveImpact(w http.ResponseWriter, r *http.Request) error {
	kind := chi.URLParam(r, "kind")
	id, err := httpjson.ID(r, "id")
	if err != nil {
		return err
	}
	ctx := r.Context()
	count := func(q string) int {
		var n int
		_ = s.DB.QueryRow(ctx, q, id).Scan(&n)
		return n
	}
	openDocs := `status NOT IN ('PAID','REJECTED','CANCELLED','CLOSED','WH_DONE','WH_BACK')`
	msg := "Nothing is deleted. You can mark it active again at any time."
	switch kind {
	case "vendor":
		msg = fmt.Sprintf("%d open requests, orders or bills with this vendor continue as they are. It will no longer be offered on new requests, bills or rate uploads. Its rates are kept.",
			count(`SELECT count(*) FROM document WHERE vendor_id=$1 AND `+openDocs))
	case "sku":
		msg = fmt.Sprintf("%d open requests containing this item continue. It will no longer appear when raising a request or in rate uploads.",
			count(`SELECT count(DISTINCT d.id) FROM document d JOIN document_line l ON l.document_id=d.id WHERE l.sku_id=$1 AND d.`+openDocs))
	case "property":
		msg = fmt.Sprintf("%d open documents for this property continue. No new request or bill can be raised for it, and it drops out of budgets and dashboards.",
			count(`SELECT count(*) FROM document WHERE property_id=$1 AND `+openDocs))
	case "mapping":
		msg = "Open requests and orders continue. This vendor will no longer be the vendor for these items at the mapped properties. Unless another mapping covers them, those items cannot be ordered there."
	case "user":
		msg = "This user can no longer sign in. Everything they raised or approved stays in the records and logs."
	case "sub_category":
		msg = fmt.Sprintf("%d SKUs and their budgets stay as they are. No new SKU or vendor can be added to this sub-category.", count(`SELECT count(*) FROM sku WHERE sub_category_id=$1`))
	case "brand":
		msg = fmt.Sprintf("%d SKUs keep this brand. It can no longer be chosen for a new SKU.", count(`SELECT count(*) FROM sku WHERE brand_id=$1`))
	case "pack_size":
		msg = fmt.Sprintf("%d SKUs keep this pack size. It can no longer be chosen for a new SKU.", count(`SELECT count(*) FROM sku WHERE pack_size_id=$1`))
	case "bill_type":
		msg = fmt.Sprintf("%d open bills of this type continue. It can no longer be picked for a new bill.", count(`SELECT count(*) FROM document WHERE bill_type_id=$1 AND `+openDocs))
	}
	return httpjson.OK(w, map[string]string{"message": msg})
}

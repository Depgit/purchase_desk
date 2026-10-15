// Package masterapproval is Finance's approval of new masters that go to Zoho: categories, sub-categories, items
// (SKUs), properties, bill types and expense heads. A new one starts pending: it cannot be used (no requests, rates,
// mappings or items under it) and is not sent to Zoho. Once Finance approves it, the Zoho sync creates or links it
// (zohowake). A rejected one goes inactive with the reason. Vendors are listed too: their approval is Finance's bank
// verification, done on the vendor's page.
package masterapproval

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"purchasedesk/internal/access"
	"purchasedesk/internal/auditlog"
	"purchasedesk/internal/database"
	"purchasedesk/internal/httpjson"
	"purchasedesk/internal/zohowake"
)

type Service struct{ DB *pgxpool.Pool }

func (s *Service) Routes(r chi.Router) {
	r.Get("/master-approvals", httpjson.Wrap(s.list))
	r.Post("/master-approvals/bulk", httpjson.Wrap(s.bulk))
	r.Post("/master-approvals/{kind}/{id}/{action}", httpjson.Wrap(s.one))
}

// tables are the masters Finance approves, with the words used for them.
var tables = map[string]struct{ table, label string }{
	"category": {"category", "Category"}, "subcat": {"sub_category", "Sub-category"}, "item": {"sku", "Item"},
	"property": {"property", "Property"}, "billtype": {"bill_type", "Bill type"}, "head": {"expense_head", "Expense head"},
}

// order approves parents before what is built on them (a category before its sub-categories, those before items).
var order = map[string]int{"category": 0, "subcat": 1, "item": 2, "property": 3, "billtype": 4, "head": 5}

// Pending is one master waiting for Finance.
type Pending struct {
	Kind      string    `json:"kind"`
	ID        int64     `json:"id"`
	Code      string    `json:"code"`
	Name      string    `json:"name"`
	Detail    string    `json:"detail"`
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
	WaitsFor  string    `json:"waits_for"` // a parent that must be approved first
}

func finance(u *access.User) error {
	if u.IsAdmin || u.HasStep("finance") {
		return nil
	}
	return httpjson.Forbidden("Only Finance approves new masters.")
}

const listSQL = `
SELECT 'category', c.id, '', c.name, CASE WHEN c.is_fb THEN 'Food & beverage category' ELSE 'Category' END, coalesce(u.name,''), c.created_at, ''
  FROM category c LEFT JOIN app_user u ON u.id=c.created_by WHERE c.fin_status='pending'
UNION ALL
SELECT 'subcat', s.id, s.code, s.name, 'Sub-category of '||c.name||' · gets a Zoho expense account', coalesce(u.name,''), s.created_at,
  CASE WHEN c.fin_status<>'approved' THEN 'category '||c.name ELSE '' END
  FROM sub_category s JOIN category c ON c.id=s.category_id LEFT JOIN app_user u ON u.id=s.created_by WHERE s.fin_status='pending'
UNION ALL
SELECT 'item', k.id, k.code, concat_ws(' ', b.name, k.item_name)||' ('||ps.name||')', 'Item · '||sc.name||' · GST '||k.gst_pct::float8||'% · HSN '||coalesce(nullif(k.hsn,''),'—'),
  coalesce(u.name,''), k.created_at, CASE WHEN sc.fin_status<>'approved' THEN 'sub-category '||sc.name WHEN c.fin_status<>'approved' THEN 'category '||c.name ELSE '' END
  FROM sku k JOIN sub_category sc ON sc.id=k.sub_category_id JOIN category c ON c.id=sc.category_id LEFT JOIN brand b ON b.id=k.brand_id
  JOIN pack_size ps ON ps.id=k.pack_size_id LEFT JOIN app_user u ON u.id=k.created_by WHERE k.fin_status='pending'
UNION ALL
SELECT 'property', p.id, p.code, p.name, initcap(p.kind)||' property · '||cl.name||' · gets a Zoho reporting tag option', coalesce(u.name,''), p.created_at, ''
  FROM property p JOIN cluster cl ON cl.id=p.cluster_id LEFT JOIN app_user u ON u.id=p.created_by WHERE p.fin_status='pending'
UNION ALL
SELECT 'billtype', b.id, '', b.name, initcap(b.section)||' bill type · gets a Zoho expense account', coalesce(u.name,''), b.created_at, ''
  FROM bill_type b LEFT JOIN app_user u ON u.id=b.created_by WHERE b.fin_status='pending'
UNION ALL
SELECT 'head', h.id, '', h.name, 'Corporate expense head · gets a Zoho expense account', coalesce(u.name,''), h.created_at, ''
  FROM expense_head h LEFT JOIN app_user u ON u.id=h.created_by WHERE h.fin_status='pending'
UNION ALL
SELECT 'vendor', v.id, v.code, v.legal_name, 'Vendor · approve by verifying its bank details on the vendor page', coalesce(u.name,''), v.created_at, ''
  FROM vendor v LEFT JOIN app_user u ON u.id=v.created_by WHERE v.active AND NOT v.bank_verified AND v.vendor_type<>'warehouse'
ORDER BY 7`

// Count is how many masters wait for Finance (the menu badge).
func Count(ctx context.Context, q database.Q) int {
	var n int
	_ = q.QueryRow(ctx, `SELECT (SELECT count(*) FROM category WHERE fin_status='pending') + (SELECT count(*) FROM sub_category WHERE fin_status='pending')
		+ (SELECT count(*) FROM sku WHERE fin_status='pending') + (SELECT count(*) FROM property WHERE fin_status='pending')
		+ (SELECT count(*) FROM bill_type WHERE fin_status='pending') + (SELECT count(*) FROM expense_head WHERE fin_status='pending')
		+ (SELECT count(*) FROM vendor WHERE active AND NOT bank_verified AND vendor_type<>'warehouse')`).Scan(&n)
	return n
}

func (s *Service) list(w http.ResponseWriter, r *http.Request) error {
	rows, err := s.DB.Query(r.Context(), listSQL)
	if err != nil {
		return err
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Pending])
	if err != nil {
		return err
	}
	return httpjson.OK(w, out)
}

// decide approves or rejects one master inside tx.
func decide(ctx context.Context, tx pgx.Tx, u *access.User, kind string, id int64, approve bool, reason string) (string, error) {
	t, ok := tables[kind]
	if !ok {
		return "", httpjson.Bad("Vendors are approved by verifying their bank details on the vendor's page.")
	}
	var name, status string
	nameCol := "name"
	if kind == "item" {
		nameCol = "code||' '||item_name"
	}
	if err := tx.QueryRow(ctx, `SELECT `+nameCol+`, fin_status FROM `+t.table+` WHERE id=$1 FOR UPDATE`, id).Scan(&name, &status); err != nil {
		return "", httpjson.NotFound(t.label)
	}
	if status != "pending" {
		return "", httpjson.Conflict("%s %s is already %s.", t.label, name, status)
	}
	if !approve {
		if len(strings.TrimSpace(reason)) < 3 {
			return "", httpjson.Bad("Write why %s is rejected.", name)
		}
		set := `fin_status='rejected', fin_by=$2, fin_at=now(), fin_note=$3, active=FALSE, inactive_by=$2, inactive_at=now(), inactive_reason='Rejected by Finance: '||$3`
		if kind == "category" { // categories have no active flag: rejected is enough (nothing under it can be approved)
			set = `fin_status='rejected', fin_by=$2, fin_at=now(), fin_note=$3`
		}
		if _, err := tx.Exec(ctx, `UPDATE `+t.table+` SET `+set+` WHERE id=$1`, id, u.ID, strings.TrimSpace(reason)); err != nil {
			return "", err
		}
		return fmt.Sprintf("%s %s rejected", t.label, name), auditlog.Write(ctx, tx, u, "Masters", t.label+" rejected by Finance", name, reason)
	}
	// A parent first: a category before its sub-categories, a sub-category (and its category) before its items.
	var waits string
	switch kind {
	case "subcat":
		_ = tx.QueryRow(ctx, `SELECT CASE WHEN c.fin_status<>'approved' THEN c.name ELSE '' END FROM sub_category s JOIN category c ON c.id=s.category_id WHERE s.id=$1`, id).Scan(&waits)
	case "item":
		_ = tx.QueryRow(ctx, `SELECT CASE WHEN sc.fin_status<>'approved' THEN 'sub-category '||sc.name WHEN c.fin_status<>'approved' THEN 'category '||c.name ELSE '' END
			FROM sku k JOIN sub_category sc ON sc.id=k.sub_category_id JOIN category c ON c.id=sc.category_id WHERE k.id=$1`, id).Scan(&waits)
	}
	if waits != "" {
		return "", httpjson.Conflict("Approve %s first: %s belongs to it.", waits, name)
	}
	if _, err := tx.Exec(ctx, `UPDATE `+t.table+` SET fin_status='approved', fin_by=$2, fin_at=now() WHERE id=$1`, id, u.ID); err != nil {
		return "", err
	}
	zohowake.Now() // it goes to Zoho now (created there, or linked to the same one already there)
	return fmt.Sprintf("%s %s approved", t.label, name), auditlog.Write(ctx, tx, u, "Masters", t.label+" approved by Finance", name, "")
}

func (s *Service) one(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	if err := finance(u); err != nil {
		return err
	}
	id, err := httpjson.ID(r, "id")
	if err != nil {
		return err
	}
	action := chi.URLParam(r, "action")
	if action != "approve" && action != "reject" {
		return httpjson.NotFound("Action")
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if err := httpjson.Decode(r, &in); err != nil {
		return err
	}
	var msg string
	err = pgx.BeginFunc(r.Context(), s.DB, func(tx pgx.Tx) error {
		var err error
		msg, err = decide(r.Context(), tx, u, chi.URLParam(r, "kind"), id, action == "approve", in.Reason)
		return err
	})
	if err != nil {
		return err
	}
	return httpjson.OK(w, map[string]string{"message": msg})
}

type pick struct {
	Kind string `json:"kind"`
	ID   int64  `json:"id"`
}

// bulk approves (or rejects with one reason) several, parents first, one transaction each, with a result per master.
func (s *Service) bulk(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	if err := finance(u); err != nil {
		return err
	}
	var in struct {
		Items  []pick `json:"items"`
		Action string `json:"action"`
		Reason string `json:"reason"`
	}
	if err := httpjson.Decode(r, &in); err != nil {
		return err
	}
	if len(in.Items) == 0 || (in.Action != "approve" && in.Action != "reject") {
		return httpjson.Bad("Tick at least one, then approve or reject.")
	}
	sort.SliceStable(in.Items, func(i, j int) bool { return order[in.Items[i].Kind] < order[in.Items[j].Kind] })
	type result struct {
		Kind    string `json:"kind"`
		ID      int64  `json:"id"`
		OK      bool   `json:"ok"`
		Message string `json:"message"`
	}
	out := []result{}
	done := 0
	for _, p := range in.Items {
		var msg string
		err := pgx.BeginFunc(r.Context(), s.DB, func(tx pgx.Tx) error {
			var err error
			msg, err = decide(r.Context(), tx, u, p.Kind, p.ID, in.Action == "approve", in.Reason)
			return err
		})
		if err != nil {
			out = append(out, result{p.Kind, p.ID, false, err.Error()})
			continue
		}
		done++
		out = append(out, result{p.Kind, p.ID, true, msg})
	}
	return httpjson.OK(w, map[string]any{"done": done, "failed": len(in.Items) - done, "results": out})
}

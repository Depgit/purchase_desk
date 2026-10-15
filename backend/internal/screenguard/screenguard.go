// Package screenguard enforces screen levels on every API request: a request that changes something needs edit on its
// screen, a screen's own lists need view. One table below maps each route to its screen. Where a route acts on one
// document, invoice or advance, the level is the user's level at that record's property (the role covering it); other
// routes use the user's highest level on the screen. Reference lists every form needs (properties, items, vendors,
// people) stay readable to anyone signed in; only their changes are guarded. The System Admin passes every check.
package screenguard

import (
	"context"
	"net/http"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"purchasedesk/internal/access"
	"purchasedesk/internal/httpjson"
	"purchasedesk/internal/screens"
)

// target says which record a route acts on, so the check uses the level at its property.
type target int

const (
	none target = iota
	doc
	invoice
	advance
)

// rec is the record a route acts on: its property and cluster, the document kind and a bill's section.
type rec struct {
	prop, cluster int64
	kind, section string
}

type rule struct {
	method string // "" any, "GET" reads, "W" any change (POST, PUT, DELETE)
	path   *regexp.Regexp
	level  int
	on     target
	// screens gives the screens any one of which is enough, from the request and the record.
	screens func(r *http.Request, x rec) []string
}

func fixed(keys ...string) func(*http.Request, rec) []string {
	return func(*http.Request, rec) []string { return keys }
}

// byKind picks the screen for a document action from the document's kind.
func byKind(purchase, corp, bill string) func(*http.Request, rec) []string {
	return func(_ *http.Request, x rec) []string {
		switch {
		case strings.HasPrefix(x.kind, "corp_"):
			return []string{corp}
		case x.kind == "bill":
			if bill == "section" {
				return []string{billScreen(x.section)}
			}
			return []string{bill}
		}
		return []string{purchase}
	}
}

func billScreen(section string) string {
	if section == "amc" {
		return "bills_amc"
	}
	return "bills_utility"
}

// listScreen picks the screen of a /requests list from its query: each page asks for its own slice.
func listScreen(r *http.Request, _ rec) []string {
	q := r.URL.Query()
	switch {
	case q.Get("view") == "po":
		return []string{"purchase_orders"}
	case q.Get("view") == "gr":
		return []string{"goods_receipts"}
	case q.Get("kind") == "bill":
		return []string{billScreen(q.Get("section"))}
	case q.Get("kind") == "corp":
		return []string{"corporate"}
	case q.Get("kind") == "purchase":
		return []string{"requests"}
	}
	return []string{"requests", "purchase_orders", "goods_receipts"}
}

// activeScreen maps a master kind (POST /masters/{kind}/{id}/active) to its screen. Users and roles are checked in
// their handlers (who added the user; who may manage roles).
var activeScreen = map[string]string{"city": "cities", "cluster": "cities", "property": "properties", "sub_category": "item_setup",
	"brand": "item_setup", "pack_size": "item_setup", "sku": "items", "vendor": "vendors", "bill_type": "bill_types", "mapping": "vendor_mapping",
	"department": "corporate_setup", "expense_head": "corporate_setup", "partner": "warehouse"}

// uploadScreens maps a bulk upload kind to the screens it belongs to.
var uploadScreens = map[string][]string{"items": {"items"}, "rates": {"vendor_mapping"}, "mappings": {"vendor_mapping"}, "budgets": {"budgets"},
	"fb_sales": {"budgets"}, "property_pct": {"properties", "budgets"}, "properties": {"properties"}, "users": {"users"}, "vendors": {"vendors"},
	"request_lines": {"requests"}, "zoho_tags": {"zoho"}, "warehouse_stock": {"warehouse"},
	"cities": {"cities"}, "clusters": {"cities"}, "categories": {"item_setup"}, "sub_categories": {"item_setup"}, "pack_sizes": {"item_setup"},
	"expense_heads": {"corporate_setup"}, "corp_departments": {"corporate_setup"}, "corp_lines": {"corporate"}}

func seg(r *http.Request, i int) string {
	p := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api"), "/"), "/")
	if i < len(p) {
		return p[i]
	}
	return ""
}

var rules = func() []rule {
	R := func(method, path string, level int, on target, s func(*http.Request, rec) []string) rule {
		return rule{method, regexp.MustCompile("^" + path + "$"), level, on, s}
	}
	V, E := screens.View, screens.Edit
	d := `/documents/\d+`
	return []rule{
		// Purchase flow, one document at a time: the level at the document's property.
		R("W", d+`/(approve|return|reject|route)`, E, doc, byKind("approvals", "corporate", "approvals")),
		R("W", d+`/po`, E, doc, fixed("purchase_orders")),
		R("W", d+`/receipts`, E, doc, byKind("goods_receipts", "corporate", "goods_receipts")),
		R("W", d+`/reject-balance`, E, doc, func(_ *http.Request, x rec) []string {
			if strings.HasPrefix(x.kind, "corp_") {
				return []string{"corporate", "purchase_orders"}
			}
			return []string{"goods_receipts", "purchase_orders"}
		}),
		R("W", d+`/(resubmit|cancel)`, E, doc, byKind("requests", "corporate", "section")),
		R("W", d+`/preop-approver`, E, doc, fixed("requests")),
		R("W", d+`/(dispatch|wait|cannot-supply)`, E, doc, fixed("warehouse")),
		R("W", d+`/receive-transfer`, E, doc, fixed("goods_receipts")),
		R("W", d+`/advances`, E, doc, byKind("purchase_orders", "purchase_orders", "purchase_orders")),
		R("W", d+`/vendor-confirm`, E, doc, fixed("purchase_orders", "approvals", "corporate")), // Purchase, or the approvers who raised it
		R("W", `/requests/\d+`, E, doc, fixed("requests")),
		R("W", `/requests`, E, none, fixed("requests")),
		R("GET", `/requests`, V, none, listScreen),
		R("GET", `/approvals`, V, none, fixed("approvals")),
		R("W", `/approvals/bulk`, E, none, fixed("approvals")),
		R("W", `/purchase-orders/bulk`, E, none, fixed("purchase_orders")),
		R("W", `/bills`, E, none, fixed("bills_utility", "bills_amc")),
		R("W", `/corporate/requests`, E, none, fixed("corporate")),
		// Invoices and payments.
		R("W", `/invoices/\d+/(match|send-back)`, E, invoice, fixed("invoices")),
		R("W", `/invoices/\d+/(approve-payment|mark-paid)`, E, invoice, fixed("payments")),
		R("GET", `/invoices/\d+/(zoho-preview|advances)`, V, invoice, fixed("payments")),
		R("GET", `/invoices/\d+`, V, invoice, fixed("invoices", "payments")),
		R("W", `/invoices/bulk`, E, none, fixed("invoices", "payments")),
		R("GET", `/invoices`, V, none, func(r *http.Request, _ rec) []string {
			if r.URL.Query().Get("document_id") != "" {
				return nil // the invoices on one document, for its page
			}
			return []string{"invoices", "payments"}
		}),
		R("W", `/advances/\d+/cancel`, E, advance, fixed("purchase_orders")),
		R("W", `/advances/\d+/[a-z-]+`, E, advance, fixed("payments")),
		R("GET", `/advances(/\d+)?`, V, none, fixed("payments", "purchase_orders")),
		// Bills, corporate, warehouse, vendor sheets.
		R("GET", `/vendor-sheets.*`, V, none, fixed("vendor_sheets")),
		R("W", `/vendor-sheets/.*`, E, none, fixed("vendor_sheets")),
		R("GET", `/warehouse/.*`, V, none, fixed("warehouse")),
		R("W", `/warehouse/.*`, E, none, fixed("warehouse")),
		R("GET", `/delivery-partners`, V, none, fixed("warehouse")),
		R("W", `/delivery-partners.*`, E, none, fixed("warehouse")),
		// Masters: lists stay readable as references; changes need edit.
		R("GET", `/master-approvals`, V, none, fixed("master_approvals")),
		R("W", `/master-approvals.*`, E, none, fixed("master_approvals")),
		// A vendor's portal sign-in is given by the Purchase Team too, not only by whoever edits vendors.
		R("W", `/vendors/\d+/portal.*`, E, none, fixed("vendors", "purchase_orders")),
		R("W", `/vendors.*`, E, none, fixed("vendors")),
		R("W", `/skus.*`, E, none, fixed("items")),
		R("W", `/(categories|sub-categories|brands|pack-sizes).*`, E, none, fixed("item_setup")),
		R("GET", `/(rates|mappings).*`, V, none, fixed("vendor_mapping")),
		R("W", `/(rates|mappings).*`, E, none, fixed("vendor_mapping")),
		R("W", `/properties/\d+/pct`, E, none, fixed("properties", "budgets")),
		R("W", `/properties.*`, E, none, fixed("properties")),
		R("W", `/(cities|clusters).*`, E, none, fixed("cities")),
		R("GET", `/(budgets/(overview|export)|fb-sales.*)`, V, none, fixed("budgets")),
		R("W", `/budgets`, E, none, fixed("budgets")),
		R("W", `/bill-types.*`, E, none, fixed("bill_types")),
		R("W", `/corporate/(departments|expense-heads).*`, E, none, fixed("corporate_setup")),
		// Settings.
		R("W", `/settings`, E, none, fixed("flow", "corporate_setup")),
		R("W", `/pick-lists/.*`, E, none, fixed("flow")),
		R("GET", `/templates`, V, none, fixed("templates")),
		R("W", `/templates/.*`, E, none, fixed("templates")),
		R("W", `/zoho/(push|read-payments|read-advance-payments)`, E, none, fixed("payments", "zoho")),
		R("W", `/zoho/mappings`, E, none, fixed("zoho", "corporate_setup")),
		R("GET", `/zoho/masters/(vendor|item|property)/\d+`, V, none, fixed("vendors", "items", "properties", "zoho")), // the Zoho card on a master's page
		R("GET", `/zoho/.*`, V, none, fixed("zoho", "corporate_setup")),
		R("W", `/zoho/purchase-orders/\d+/sync`, E, none, fixed("purchase_orders", "zoho")), // Purchase may send its PO to Zoho at once
		R("W", `/zoho/.*`, E, none, fixed("zoho")),
		R("GET", `/activity`, V, none, fixed("activity")),
		R("GET", `/reports.*`, V, none, fixed("reports")),
		R("GET", `/users(/.*)?`, V, none, fixed("users")),
		R("W", `/users.*`, E, none, fixed("users")),
		R("W", `/masters/[a-z_]+/\d+/active`, E, none, func(r *http.Request, _ rec) []string {
			if s, ok := activeScreen[seg(r, 1)]; ok {
				return []string{s}
			}
			return nil
		}),
		R("", `/uploads/[a-z_]+/.*`, V, none, func(r *http.Request, _ rec) []string { return uploadScreens[seg(r, 1)] }),
	}
}()

// uploadWrite marks the upload steps that change data; downloads (sample, export, workbook) need view only.
var uploadWrite = regexp.MustCompile(`^/uploads/[a-z_]+/(check|import)$`)

type Guard struct{ DB *pgxpool.Pool }

// Middleware runs after access.Guard.Middleware.
func (g *Guard) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := access.From(r)
		if u.IsAdmin {
			next.ServeHTTP(w, r)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/api")
		write := r.Method != http.MethodGet && r.Method != http.MethodHead
		for _, ru := range rules {
			if (ru.method == "GET" && write) || (ru.method == "W" && !write) || !ru.path.MatchString(path) {
				continue
			}
			level := ru.level
			if strings.HasPrefix(path, "/uploads/") && uploadWrite.MatchString(path) {
				level = screens.Edit
			}
			var x rec
			if ru.on != none {
				var err error
				if x, err = g.record(r.Context(), ru.on, seg(r, 1)); err != nil {
					break // an unknown record: the handler answers "not found"
				}
			}
			keys := ru.screens(r, x)
			if len(keys) == 0 {
				break
			}
			best := screens.None
			for _, k := range keys {
				l := u.Overall(k)
				if ru.on != none {
					l = u.Level(k, x.prop, x.cluster)
				}
				if l > best {
					best = l
				}
			}
			if best < level {
				httpjson.JSON(w, http.StatusForbidden, refusal(keys[0], best, ru.on != none))
				return
			}
			break
		}
		next.ServeHTTP(w, r)
	})
}

func refusal(key string, have int, atProperty bool) error {
	where := ""
	if atProperty {
		where = " at this property"
	}
	if have == screens.View {
		return httpjson.Forbidden("Your role can only view %s%s, not change it. Ask the System Admin if you need to.", screens.Label(key), where)
	}
	return httpjson.Forbidden("Your role has no access to %s%s.", screens.Label(key), where)
}

// record loads the property, cluster and kind of the document, invoice or advance a route acts on.
func (g *Guard) record(ctx context.Context, on target, id string) (rec, error) {
	var x rec
	q := `SELECT d.property_id, p.cluster_id, d.kind, coalesce(bt.section,'') FROM document d JOIN property p ON p.id=d.property_id
		LEFT JOIN bill_type bt ON bt.id=d.bill_type_id WHERE d.id=$1::bigint`
	switch on {
	case invoice:
		q = `SELECT i.property_id, p.cluster_id, d.kind, coalesce(bt.section,'') FROM invoice i JOIN document d ON d.id=i.document_id
			JOIN property p ON p.id=i.property_id LEFT JOIN bill_type bt ON bt.id=d.bill_type_id WHERE i.id=$1::bigint`
	case advance:
		q = `SELECT a.property_id, p.cluster_id, d.kind, '' FROM advance a JOIN document d ON d.id=a.document_id
			JOIN property p ON p.id=a.property_id WHERE a.id=$1::bigint`
	}
	err := g.DB.QueryRow(ctx, q, id).Scan(&x.prop, &x.cluster, &x.kind, &x.section)
	return x, err
}

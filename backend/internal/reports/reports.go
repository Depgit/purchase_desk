// Package reports produces the 14 management reports. Each takes a date range, property, city and cluster filters,
// can be downloaded, and all of them download together as one Excel pack.
package reports

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"purchasedesk/internal/access"
	"purchasedesk/internal/budgets"
	"purchasedesk/internal/clock"
	"purchasedesk/internal/httpjson"
	"purchasedesk/internal/money"
	"purchasedesk/internal/spreadsheet"
)

type Service struct {
	DB    *pgxpool.Pool
	Guard *access.Guard
}

func (s *Service) Routes(r chi.Router) {
	r.Get("/reports", httpjson.Wrap(s.catalogue))
	r.Get("/reports/pack", httpjson.Wrap(s.pack))
	r.Get("/reports/{key}", httpjson.Wrap(s.one))
}

type Column struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Right bool   `json:"right"`
}

type Result struct {
	Key     string   `json:"key"`
	Title   string   `json:"title"`
	Use     string   `json:"use"`
	Group   string   `json:"group"`
	Columns []Column `json:"columns"`
	Rows    [][]any  `json:"rows"`
	DocIDs  []int64  `json:"document_ids,omitempty"`
}

// Filter is what every report takes.
type Filter struct {
	From, To   time.Time
	Basis      string // req, po, gr
	PropertyID int64
	ClusterID  int64
	City       string
	Period     string // budget reports: mtd, lm, m3, m6, fy
	all        bool
	ids        []int64
}

type def struct {
	title, use, group string
	cols              []Column
	run               func(ctx context.Context, s *Service, f Filter) ([][]any, []int64, error)
}

func c(key, label string) Column  { return Column{Key: key, Label: label} }
func cr(key, label string) Column { return Column{Key: key, Label: label, Right: true} }

var defs map[string]def
var order = []string{"tracker", "three_way", "po_grn", "register", "budget", "budget_months", "category_cluster", "vendor_spend", "sku_spend", "ageing", "fnb", "bills", "payables", "rates"}

func init() {
	defs = map[string]def{
		"tracker": {"Purchase request tracker", "One row per request with approval, PO, goods receipt, invoice, match and payment status", "Purchase flow",
			[]Column{c("req", "Request"), c("date", "Request date"), c("property", "Property"), c("city", "City"), c("vendor", "Vendor"), cr("amount", "Request amount"), c("status", "Stage"),
				c("po", "PO"), c("po_date", "PO date"), c("deliver", "Deliver by"), c("grn", "Last receipt"), cr("received", "Received value"), c("invoices", "Invoices"), cr("invoiced", "Invoiced"),
				c("match", "Match"), c("payment", "Payment"), c("zoho", "Zoho bills"), cr("days", "Days open")}, tracker},
		"three_way": {"PO, GRN and invoice in one view", "Three-way match in a single report: ordered, received and invoiced per delivery", "Purchase flow",
			[]Column{c("po", "PO"), c("req", "Request"), c("vendor", "Vendor"), c("property", "Property"), c("po_date", "PO date"), cr("po_value", "PO value"), c("grn", "Receipt"), c("grn_date", "Receipt date"),
				cr("grn_value", "Received value"), c("short", "Short lines"), c("invoice", "Invoice no."), cr("inv_amt", "Invoice amount"), cr("diff", "Invoice less received"), c("result", "Result"), c("zoho", "Zoho bill")}, threeWay},
		"po_grn": {"PO against GRN, item by item", "Ordered and received quantity for every item on every PO: short and excess receipts", "Purchase flow",
			[]Column{c("po", "PO"), c("property", "Property"), c("vendor", "Vendor"), c("sku", "SKU"), c("item", "Item"), cr("ordered", "Ordered"), cr("received", "Received"), cr("short", "Short"),
				cr("rate", "Rate"), cr("ord_value", "Ordered value"), cr("rcv_value", "Received value"), c("closed", "Closed short")}, poGRN},
		"register": {"Purchase register", "One row per item line bought from a vendor, with its PR, PO, GRN, invoice and voucher numbers (warehouse transfers are left out)", "Purchase flow",
			[]Column{c("req", "PR No."), c("po", "PO No."), c("grn", "GRN No."), c("invoice", "Invoice No."), c("voucher", "Voucher"), c("inv_date", "Invoice date"), c("date", "Date"),
				c("property", "Property"), c("cluster", "Cluster"), c("category", "Category"), c("sub", "Sub-category"), c("sku", "SKU"), c("item", "Item"),
				c("unit", "Unit"), cr("qty", "Quantity"), cr("orig", "Original quantity"), cr("received", "Received quantity"), cr("rate", "Rate ex GST"), cr("gst", "GST %"), cr("amount", "Amount incl. GST"),
				c("vendor", "Vendor"), c("by", "Raised by"), c("status", "Status")}, register},
		"budget": {"Budget against actual", "By property and sub-category, for month to date, last month, 3 months, 6 months or financial year", "Budget",
			[]Column{c("property", "Property"), c("cluster", "Cluster"), c("category", "Category"), c("sub", "Sub-category"), c("period", "Period"), cr("budget", "Budget"), cr("spent", "Approved spend"),
				cr("var", "Over or under"), cr("pct", "Achievement %"), c("status", "Status")}, budgetVsActual},
		"budget_months": {"Budget against actual, month by month", "Trend per property", "Budget",
			[]Column{c("month", "Month"), c("property", "Property"), c("category", "Category"), cr("budget", "Budget"), cr("spent", "Approved spend"), cr("var", "Over or under"), cr("pct", "Achievement %")}, budgetMonths},
		"category_cluster": {"Category summary by cluster", "Management view of budget against spend by category", "Budget",
			[]Column{c("cluster", "Cluster"), c("category", "Category"), cr("budget", "Budget"), cr("spent", "Approved spend"), cr("var", "Over or under"), cr("pct", "Achievement %")}, categoryCluster},
		"vendor_spend": {"Spend by vendor", "Vendor concentration: approved purchases and bills, and what is still to pay", "Vendors, items and bills",
			[]Column{c("code", "Code"), c("vendor", "Vendor"), c("type", "Type"), c("msme", "MSME"), cr("docs", "Documents"), cr("amount", "Approved amount"), cr("share", "Share %"), cr("open", "Still to pay")}, vendorSpend},
		"sku_spend": {"Spend by SKU", "Top items by value", "Vendors, items and bills",
			[]Column{c("sku", "SKU"), c("item", "Item"), c("sub", "Sub-category"), cr("reqs", "Requests"), cr("qty", "Quantity"), cr("amount", "Amount incl. GST"), cr("avg", "Average rate")}, skuSpend},
		"ageing": {"Pending work and ageing", "What is stuck and with whom", "Purchase flow",
			[]Column{c("doc", "Document"), c("type", "Type"), c("property", "Property"), c("with", "Waiting with"), c("stage", "Stage"), cr("days", "Days waiting"), cr("amount", "Amount")}, ageing},
		"fnb": {"F&B cost against sales", "Allowed cost from sales against what was ordered, month to date", "Budget",
			[]Column{c("property", "Property"), c("cluster", "Cluster"), cr("plan", "Plan sale"), cr("ird", "IRD sale"), cr("bq", "Banquet sale"), cr("sale", "Total sale"), cr("allowed", "Allowed cost"),
				cr("ordered", "Ordered"), cr("pct", "Cost % of sale"), c("pace", "Against allowed")}, fnb},
		"bills": {"Utility and AMC register", "All bills with period and status", "Vendors, items and bills",
			[]Column{c("bill", "Bill"), c("section", "Type"), c("item", "Bill type"), c("property", "Property"), c("vendor", "Vendor"), c("invoice", "Invoice no."), c("period", "Period"), cr("months", "Months"),
				cr("amount", "Amount"), cr("prev", "Previous bill"), c("status", "Status")}, bills},
		"payables": {"Payables and due dates", "What is due, including MSME limits", "Vendors, items and bills",
			[]Column{c("invoice", "Invoice"), c("doc", "Document"), c("vendor", "Vendor"), c("msme", "MSME"), c("property", "Property"), cr("amount", "Amount"), cr("tds", "TDS"), cr("net", "Net payable"), c("inv_date", "Invoice date"), c("due", "Pay by"),
				c("msme_limit", "MSME limit"), cr("days", "Days to due"), c("status", "Status"), c("zoho", "Zoho bill")}, payables},
		"rates": {"Vendor rates and validity", "Expiring and expired rates, to plan renewals", "Vendors, items and bills",
			[]Column{c("vendor", "Vendor"), c("sku", "SKU"), c("item", "Item"), cr("rate", "Rate ex GST"), cr("gst", "GST %"), c("from", "Valid from"), c("to", "Valid to"), cr("days", "Days left"), c("status", "Status"), c("mapped", "Mapped")}, rates},
	}
}

func (s *Service) filter(r *http.Request) (Filter, error) {
	q := r.URL.Query()
	f := Filter{Basis: q.Get("basis"), City: q.Get("city"), Period: q.Get("period"), PropertyID: int64(httpjson.QInt(r, "property_id", 0)), ClusterID: int64(httpjson.QInt(r, "cluster_id", 0))}
	today := clock.Today()
	f.From, f.To = clock.MonthStart(today), today
	if t, ok := clock.ParseDate(q.Get("from")); ok {
		f.From = t
	}
	if t, ok := clock.ParseDate(q.Get("to")); ok {
		f.To = t
	}
	if f.Period == "" {
		f.Period = "mtd"
	}
	var err error
	f.all, f.ids, err = s.Guard.Visible(r.Context(), access.From(r))
	return f, err
}

// place is the shared property filter on alias p (property), k (cluster), ci (city), starting at parameter $n.
func place(f Filter, n int) (string, []any) {
	return fmt.Sprintf(` AND ($%d OR p.id = ANY($%d)) AND ($%d=0 OR p.id=$%d) AND ($%d=0 OR p.cluster_id=$%d) AND ($%d='' OR ci.name=$%d)`, n, n+1, n+2, n+2, n+3, n+3, n+4, n+4),
		[]any{f.all, f.ids, f.PropertyID, f.ClusterID, f.City}
}

const placeJoin = ` JOIN property p ON p.id=d.property_id JOIN cluster k ON k.id=p.cluster_id JOIN city ci ON ci.id=k.city_id `

func dateCol(f Filter) string {
	switch f.Basis {
	case "po":
		return "d.po_date"
	case "gr":
		return "(SELECT max(received_at) FROM goods_receipt g WHERE g.document_id=d.id)"
	}
	return "d.created_at"
}

func collect(rows pgx.Rows, err error, width int) ([][]any, []int64, error) {
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	out := [][]any{}
	ids := []int64{}
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			return nil, nil, err
		}
		if len(vals) > width {
			if id, ok := vals[width].(int64); ok {
				ids = append(ids, id)
			}
			vals = vals[:width]
		}
		for i, v := range vals {
			switch x := v.(type) {
			case time.Time:
				vals[i] = clock.YMD(x)
			case float64:
				vals[i] = money.Round2(x)
			case pgtype.Numeric:
				if f, err := x.Float64Value(); err == nil && f.Valid {
					vals[i] = money.Round2(f.Float64)
				} else {
					vals[i] = ""
				}
			case nil:
				vals[i] = ""
			}
		}
		out = append(out, vals)
	}
	return out, ids, rows.Err()
}

var stageSQL = `CASE d.status WHEN 'PENDING' THEN 'In approval' WHEN 'RETURNED' THEN 'Returned' WHEN 'REJECTED' THEN 'Rejected' WHEN 'CANCELLED' THEN 'Cancelled'
	WHEN 'APPROVED' THEN 'Approved, PO pending' WHEN 'PO_ISSUED' THEN 'Awaiting delivery' WHEN 'RECEIVED' THEN 'Received' WHEN 'CLOSED' THEN 'Paid and closed'
	WHEN 'WH_SENT' THEN 'In transit' WHEN 'WH_DONE' THEN 'Received from warehouse' WHEN 'WH_BACK' THEN 'Returned by warehouse' ELSE d.status END`

const lineValue = `l.qty*l.rate*(1+l.gst_pct/100)`

func tracker(ctx context.Context, s *Service, f Filter) ([][]any, []int64, error) {
	w, a := place(f, 3)
	rows, err := s.DB.Query(ctx, `SELECT d.number, d.created_at, p.name, ci.name, v.legal_name,
		(SELECT coalesce(sum(`+lineValue+`),0) FROM document_line l WHERE l.document_id=d.id), `+stageSQL+`,
		coalesce(d.po_number,''), d.po_date, d.deliver_by, (SELECT max(received_at) FROM goods_receipt g WHERE g.document_id=d.id AND NOT g.voided),
		(SELECT coalesce(sum(l.received_qty*l.rate*(1+l.gst_pct/100)),0) FROM document_line l WHERE l.document_id=d.id),
		coalesce((SELECT string_agg(i.invoice_number, ', ') FROM invoice i WHERE i.document_id=d.id AND i.status<>'VOID'),''),
		coalesce((SELECT sum(i.amount) FROM invoice i WHERE i.document_id=d.id AND i.status<>'VOID'),0),
		coalesce((SELECT CASE WHEN bool_and(i.status<>'RECEIVED') THEN 'Matched' ELSE 'To match' END FROM invoice i WHERE i.document_id=d.id AND i.status NOT IN ('VOID','AWAITING')),''),
		coalesce((SELECT CASE WHEN bool_and(i.status='PAID') THEN 'Paid' WHEN bool_or(i.status='PUSHED') THEN 'In Zoho, unpaid' WHEN bool_or(i.status='PAY_APPROVED') THEN 'Approved, not in Zoho' ELSE 'Payment approval pending' END
			FROM invoice i WHERE i.document_id=d.id AND i.status NOT IN ('VOID','AWAITING','RECEIVED')),''),
		coalesce((SELECT string_agg(i.zoho_bill_id, ', ') FROM invoice i WHERE i.document_id=d.id AND i.zoho_bill_id IS NOT NULL),''),
		CASE WHEN d.status IN ('CLOSED','REJECTED','CANCELLED','WH_DONE') THEN NULL ELSE (current_date - d.created_at::date) END, d.id
		FROM document d JOIN vendor v ON v.id=d.vendor_id`+placeJoin+`WHERE d.kind='purchase' AND `+dateCol(f)+` >= $1 AND `+dateCol(f)+` < $2::date + 1`+w+` ORDER BY d.created_at DESC`,
		append([]any{f.From, f.To}, a...)...)
	return collect(rows, err, 18)
}

func threeWay(ctx context.Context, s *Service, f Filter) ([][]any, []int64, error) {
	w, a := place(f, 3)
	rows, err := s.DB.Query(ctx, `SELECT d.po_number, d.number, v.legal_name, p.name, d.po_date,
		(SELECT coalesce(sum(`+lineValue+`),0) FROM document_line l WHERE l.document_id=d.id), g.number, g.received_at,
		coalesce((SELECT sum(gl.qty*l.rate*(1+l.gst_pct/100)) FROM goods_receipt_line gl JOIN document_line l ON l.id=gl.line_id WHERE gl.goods_receipt_id=g.id),0),
		(SELECT count(*) FROM document_line l WHERE l.document_id=d.id AND l.received_qty < l.qty)::text,
		i.invoice_number, i.amount,
		i.amount - coalesce((SELECT sum(gl.qty*l.rate*(1+l.gst_pct/100)) FROM goods_receipt_line gl JOIN document_line l ON l.id=gl.line_id WHERE gl.goods_receipt_id=g.id),0),
		CASE WHEN i.status='RECEIVED' THEN 'To match' WHEN i.match_remark<>'' THEN 'Difference accepted' ELSE 'Matched' END, coalesce(i.zoho_bill_id,''), d.id
		FROM invoice i JOIN goods_receipt g ON g.id=i.goods_receipt_id JOIN document d ON d.id=i.document_id JOIN vendor v ON v.id=d.vendor_id`+placeJoin+`
		WHERE i.status<>'VOID' AND `+dateCol(f)+` >= $1 AND `+dateCol(f)+` < $2::date + 1`+w+` ORDER BY g.received_at DESC`, append([]any{f.From, f.To}, a...)...)
	return collect(rows, err, 15)
}

func poGRN(ctx context.Context, s *Service, f Filter) ([][]any, []int64, error) {
	w, a := place(f, 3)
	rows, err := s.DB.Query(ctx, `SELECT d.po_number, p.name, v.legal_name, k2.code, concat_ws(' ', b.name, k2.item_name), l.qty, l.received_qty, l.qty-l.received_qty, l.rate,
		`+lineValue+`, l.received_qty*l.rate*(1+l.gst_pct/100), CASE WHEN d.po_closed_short THEN 'Yes' ELSE '' END, d.id
		FROM document_line l JOIN document d ON d.id=l.document_id JOIN sku k2 ON k2.id=l.sku_id LEFT JOIN brand b ON b.id=k2.brand_id JOIN vendor v ON v.id=d.vendor_id`+placeJoin+`
		WHERE d.po_number IS NOT NULL AND d.kind='purchase' AND NOT d.is_transfer AND `+dateCol(f)+` >= $1 AND `+dateCol(f)+` < $2::date + 1`+w+` ORDER BY d.po_date DESC, l.id`, append([]any{f.From, f.To}, a...)...)
	return collect(rows, err, 12)
}

func register(ctx context.Context, s *Service, f Filter) ([][]any, []int64, error) {
	w, a := place(f, 3)
	// A line received over several deliveries lists each GRN with its invoice and voucher, in delivery order.
	rows, err := s.DB.Query(ctx, `SELECT d.number, coalesce(d.po_number,''), coalesce(r.grns,''), coalesce(r.invoices,''), coalesce(r.vouchers,''), coalesce(r.inv_dates,''), d.created_at,
		p.name, k.name, cat.name, sc.name, k2.code, concat_ws(' ', b.name, k2.item_name), ps.name,
		l.qty, l.original_qty, coalesce(r.received,0), l.rate, l.gst_pct, `+lineValue+`, v.legal_name, u.name, `+stageSQL+`, d.id
		FROM document_line l JOIN document d ON d.id=l.document_id JOIN sku k2 ON k2.id=l.sku_id LEFT JOIN brand b ON b.id=k2.brand_id JOIN pack_size ps ON ps.id=k2.pack_size_id
		JOIN sub_category sc ON sc.id=k2.sub_category_id JOIN category cat ON cat.id=sc.category_id JOIN vendor v ON v.id=d.vendor_id JOIN app_user u ON u.id=d.requester_id`+placeJoin+`
		LEFT JOIN LATERAL (SELECT string_agg(g.number, ', ' ORDER BY g.delivery_no) AS grns, sum(gl.qty) AS received,
			string_agg(i.invoice_number, ', ' ORDER BY g.delivery_no) AS invoices, string_agg(i.voucher_number, ', ' ORDER BY g.delivery_no) AS vouchers,
			string_agg(to_char(i.invoice_date, 'DD Mon YYYY'), ', ' ORDER BY g.delivery_no) AS inv_dates
			FROM goods_receipt_line gl JOIN goods_receipt g ON g.id=gl.goods_receipt_id AND NOT g.voided
			LEFT JOIN invoice i ON i.goods_receipt_id=g.id AND i.status<>'VOID' WHERE gl.line_id=l.id AND gl.qty>0) r ON TRUE
		WHERE d.kind='purchase' AND NOT d.is_transfer AND `+dateCol(f)+` >= $1 AND `+dateCol(f)+` < $2::date + 1`+w+` ORDER BY d.created_at DESC, l.id`, append([]any{f.From, f.To}, a...)...)
	return collect(rows, err, 23)
}

// months returns the budget months for a period, newest last, and whether the last is the current month to date.
func months(period string) ([]time.Time, string) {
	cur := clock.MonthStart(clock.Today())
	back := func(n int) []time.Time {
		out := []time.Time{}
		for i := n; i >= 1; i-- {
			out = append(out, cur.AddDate(0, -i, 0))
		}
		return out
	}
	switch period {
	case "lm":
		return back(1), "Last month"
	case "m3":
		return back(3), "Last 3 months"
	case "m6":
		return back(6), "Last 6 months"
	case "fy":
		start := time.Date(cur.Year(), 4, 1, 0, 0, 0, 0, clock.IST)
		if cur.Month() < 4 {
			start = start.AddDate(-1, 0, 0)
		}
		out := []time.Time{}
		for m := start; !m.After(cur); m = m.AddDate(0, 1, 0) {
			out = append(out, m)
		}
		return out, "Financial year to date"
	}
	return []time.Time{cur}, "This month to date"
}

type budgetCell struct {
	prop, cluster, cat, sub string
	propID, clusterID       int64
	budget, spent           float64
}

// budgetCells returns budget and approved spend per property and sub-category for each month; the current month counts budget to date.
func (s *Service) budgetCells(ctx context.Context, f Filter, ms []time.Time) (map[time.Time][]*budgetCell, error) {
	w, a := place(f, 1)
	rows, err := s.DB.Query(ctx, `SELECT p.id, p.name, k.id, k.name FROM property p JOIN cluster k ON k.id=p.cluster_id JOIN city ci ON ci.id=k.city_id WHERE p.kind='live' AND p.active`+w+` ORDER BY p.name`, a...)
	if err != nil {
		return nil, err
	}
	type prop struct {
		id, cl     int64
		name, clNm string
	}
	props, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (prop, error) {
		var p prop
		return p, row.Scan(&p.id, &p.name, &p.cl, &p.clNm)
	})
	if err != nil {
		return nil, err
	}
	srows, err := s.DB.Query(ctx, `SELECT sc.id, sc.name, c.name FROM sub_category sc JOIN category c ON c.id=sc.category_id ORDER BY c.name, sc.name`)
	if err != nil {
		return nil, err
	}
	type sub struct {
		id        int64
		name, cat string
	}
	subs, err := pgx.CollectRows(srows, func(row pgx.CollectableRow) (sub, error) {
		var x sub
		return x, row.Scan(&x.id, &x.name, &x.cat)
	})
	if err != nil {
		return nil, err
	}
	out := map[time.Time][]*budgetCell{}
	for _, m := range ms {
		for _, p := range props {
			bm, err := budgets.LoadMonth(ctx, s.DB, p.id, m)
			if err != nil {
				return nil, err
			}
			for _, sc := range subs {
				b := bm.Budget[sc.id]
				if bm.Today < bm.Days {
					b = bm.BudgetToDate(sc.id)
				}
				sp := bm.SpentMonth(sc.id)
				if b == 0 && sp == 0 {
					continue
				}
				out[m] = append(out[m], &budgetCell{prop: p.name, cluster: p.clNm, cat: sc.cat, sub: sc.name, propID: p.id, clusterID: p.cl, budget: b, spent: sp})
			}
		}
	}
	return out, nil
}

func pct(spent, budget float64) any {
	if budget == 0 {
		return ""
	}
	return money.Round2(spent / budget * 100)
}

func budgetVsActual(ctx context.Context, s *Service, f Filter) ([][]any, []int64, error) {
	ms, label := months(f.Period)
	cells, err := s.budgetCells(ctx, f, ms)
	if err != nil {
		return nil, nil, err
	}
	sum := map[string]*budgetCell{}
	keys := []string{}
	for _, m := range ms {
		for _, x := range cells[m] {
			k := x.prop + "|" + x.sub
			if sum[k] == nil {
				cp := *x
				cp.budget, cp.spent = 0, 0
				sum[k] = &cp
				keys = append(keys, k)
			}
			sum[k].budget += x.budget
			sum[k].spent += x.spent
		}
	}
	out := [][]any{}
	for _, k := range keys {
		x := sum[k]
		st := "Within"
		if x.spent > x.budget {
			st = "Over"
		}
		out = append(out, []any{x.prop, x.cluster, x.cat, x.sub, label, money.Round2(x.budget), money.Round2(x.spent), money.Round2(x.spent - x.budget), pct(x.spent, x.budget), st})
	}
	return out, nil, nil
}

func budgetMonths(ctx context.Context, s *Service, f Filter) ([][]any, []int64, error) {
	p := f.Period
	if p == "mtd" {
		p = "m6"
	}
	ms, _ := months(p)
	ms = append(ms, clock.MonthStart(clock.Today()))
	cells, err := s.budgetCells(ctx, f, ms)
	if err != nil {
		return nil, nil, err
	}
	out := [][]any{}
	for _, m := range ms {
		agg := map[string]*budgetCell{}
		keys := []string{}
		for _, x := range cells[m] {
			k := x.prop + "|" + x.cat
			if agg[k] == nil {
				agg[k] = &budgetCell{prop: x.prop, cat: x.cat}
				keys = append(keys, k)
			}
			agg[k].budget += x.budget
			agg[k].spent += x.spent
		}
		for _, k := range keys {
			x := agg[k]
			out = append(out, []any{m.Format("Jan 2006"), x.prop, x.cat, money.Round2(x.budget), money.Round2(x.spent), money.Round2(x.spent - x.budget), pct(x.spent, x.budget)})
		}
	}
	return out, nil, nil
}

func categoryCluster(ctx context.Context, s *Service, f Filter) ([][]any, []int64, error) {
	ms, _ := months(f.Period)
	cells, err := s.budgetCells(ctx, f, ms)
	if err != nil {
		return nil, nil, err
	}
	agg := map[string]*budgetCell{}
	keys := []string{}
	for _, m := range ms {
		for _, x := range cells[m] {
			k := x.cluster + "|" + x.cat
			if agg[k] == nil {
				agg[k] = &budgetCell{cluster: x.cluster, cat: x.cat}
				keys = append(keys, k)
			}
			agg[k].budget += x.budget
			agg[k].spent += x.spent
		}
	}
	out := [][]any{}
	for _, k := range keys {
		x := agg[k]
		out = append(out, []any{x.cluster, x.cat, money.Round2(x.budget), money.Round2(x.spent), money.Round2(x.spent - x.budget), pct(x.spent, x.budget)})
	}
	return out, nil, nil
}

func vendorSpend(ctx context.Context, s *Service, f Filter) ([][]any, []int64, error) {
	w, a := place(f, 3)
	rows, err := s.DB.Query(ctx, `WITH x AS (
		SELECT v.id, v.code, v.legal_name, v.vendor_type, v.msme, d.id did,
		CASE WHEN d.kind IN ('bill','corp_bill') THEN coalesce((SELECT i.amount FROM invoice i WHERE i.document_id=d.id AND i.status<>'VOID' LIMIT 1),0)
		     ELSE (SELECT coalesce(sum(`+lineValue+`),0) FROM document_line l WHERE l.document_id=d.id) END amt,
		coalesce((SELECT sum(i.amount) FROM invoice i WHERE i.document_id=d.id AND i.status IN ('RECEIVED','MATCHED','PAY_APPROVED','PUSHED')),0) open
		FROM document d JOIN vendor v ON v.id=d.vendor_id`+placeJoin+`
		WHERE d.status NOT IN ('PENDING','RETURNED','REJECTED','CANCELLED','WH_BACK') AND NOT d.is_transfer AND d.approved_at >= $1 AND d.approved_at < $2::date + 1`+w+`)
		SELECT code, legal_name, vendor_type, CASE WHEN msme THEN 'MSME' ELSE 'No' END, count(*), sum(amt), sum(amt)*100/nullif(sum(sum(amt)) OVER (),0), sum(open)
		FROM x GROUP BY id, code, legal_name, vendor_type, msme ORDER BY sum(amt) DESC`, append([]any{f.From, f.To}, a...)...)
	return collect(rows, err, 8)
}

func skuSpend(ctx context.Context, s *Service, f Filter) ([][]any, []int64, error) {
	w, a := place(f, 3)
	rows, err := s.DB.Query(ctx, `SELECT k2.code, concat_ws(' ', b.name, k2.item_name)||' · '||ps.name, sc.name, count(DISTINCT d.id), sum(l.qty), sum(`+lineValue+`), sum(l.qty*l.rate)/nullif(sum(l.qty),0)
		FROM document_line l JOIN document d ON d.id=l.document_id JOIN sku k2 ON k2.id=l.sku_id LEFT JOIN brand b ON b.id=k2.brand_id JOIN pack_size ps ON ps.id=k2.pack_size_id
		JOIN sub_category sc ON sc.id=k2.sub_category_id`+placeJoin+`
		WHERE d.kind='purchase' AND d.status NOT IN ('PENDING','RETURNED','REJECTED','CANCELLED','WH_BACK') AND d.approved_at >= $1 AND d.approved_at < $2::date + 1`+w+`
		GROUP BY k2.code, b.name, k2.item_name, ps.name, sc.name ORDER BY sum(`+lineValue+`) DESC`, append([]any{f.From, f.To}, a...)...)
	return collect(rows, err, 7)
}

func ageing(ctx context.Context, s *Service, f Filter) ([][]any, []int64, error) {
	w, a := place(f, 1)
	rows, err := s.DB.Query(ctx, `SELECT coalesce(d.po_number, d.number), CASE d.kind WHEN 'bill' THEN 'Utility or AMC bill' WHEN 'corp_purchase' THEN 'Corporate purchase' WHEN 'corp_bill' THEN 'Corporate bill' ELSE CASE WHEN d.is_transfer THEN 'Warehouse transfer' ELSE 'Purchase' END END,
		p.name, CASE d.status WHEN 'PENDING' THEN CASE d.route_steps[d.step_idx+1] WHEN 'cc' THEN 'Cost Controller' WHEN 'l2' THEN 'Cluster Manager' WHEN 'preop' THEN 'Pre-opening Team' WHEN 'cl1' THEN 'Department L1' WHEN 'cl2' THEN 'Department L2' ELSE 'Approver' END
			WHEN 'RETURNED' THEN 'Requester' WHEN 'APPROVED' THEN CASE WHEN d.kind IN ('bill','corp_bill') THEN 'Finance' ELSE 'Purchase Team' END
			WHEN 'PO_ISSUED' THEN CASE WHEN d.is_transfer OR p.kind<>'live' THEN 'Warehouse Manager' WHEN d.kind='corp_purchase' THEN 'Requester' ELSE 'Receiving / Store' END
			WHEN 'WH_SENT' THEN 'Receiving / Store' ELSE '' END,
		`+stageSQL+`, current_date - d.updated_at::date,
		CASE WHEN d.kind IN ('bill','corp_bill') THEN coalesce((SELECT i.amount FROM invoice i WHERE i.document_id=d.id AND i.status<>'VOID' LIMIT 1),0) ELSE (SELECT coalesce(sum(`+lineValue+`),0) FROM document_line l WHERE l.document_id=d.id) END, d.id
		FROM document d`+placeJoin+`WHERE d.status IN ('PENDING','RETURNED','APPROVED','PO_ISSUED','WH_SENT') AND NOT (d.status='APPROVED' AND d.kind IN ('bill','corp_bill'))`+w+`
		UNION ALL
		SELECT i.invoice_number, 'Invoice', p.name, CASE i.status WHEN 'RECEIVED' THEN 'Accounts Payable' ELSE 'Finance' END,
		CASE i.status WHEN 'RECEIVED' THEN 'To match' WHEN 'MATCHED' THEN 'Payment approval' WHEN 'PAY_APPROVED' THEN 'Push to Zoho' ELSE 'In Zoho, unpaid' END,
		current_date - i.created_at::date, i.amount, d.id
		FROM invoice i JOIN document d ON d.id=i.document_id`+placeJoin+`WHERE i.status IN ('RECEIVED','MATCHED','PAY_APPROVED','PUSHED')`+w+`
		ORDER BY 6 DESC`, a...)
	return collect(rows, err, 7)
}

func fnb(ctx context.Context, s *Service, f Filter) ([][]any, []int64, error) {
	w, a := place(f, 1)
	rows, err := s.DB.Query(ctx, `SELECT p.id, p.name, k.name FROM property p JOIN cluster k ON k.id=p.cluster_id JOIN city ci ON ci.id=k.city_id WHERE p.kind='live' AND p.active`+w+` ORDER BY p.name`, a...)
	if err != nil {
		return nil, nil, err
	}
	type prop struct {
		id         int64
		name, clNm string
	}
	props, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (prop, error) {
		var p prop
		return p, row.Scan(&p.id, &p.name, &p.clNm)
	})
	if err != nil {
		return nil, nil, err
	}
	m := clock.MonthStart(clock.Today())
	out := [][]any{}
	for _, p := range props {
		bm, err := budgets.LoadMonth(ctx, s.DB, p.id, m)
		if err != nil {
			return nil, nil, err
		}
		var a1, b1, c1 float64
		_ = s.DB.QueryRow(ctx, `SELECT coalesce(sum(plan_sale),0), coalesce(sum(ird_sale),0), coalesce(sum(banquet_sale),0) FROM fb_sale WHERE property_id=$1 AND sale_date >= $2 AND sale_date < $3`, p.id, m, m.AddDate(0, 1, 0)).Scan(&a1, &b1, &c1)
		allowed, ordered := 0.0, 0.0
		for sc, fb := range bm.IsFB {
			if fb {
				allowed += bm.BudgetToDate(sc)
				ordered += bm.SpentMonth(sc)
			}
		}
		sale := a1 + b1 + c1
		pc := any("")
		if sale > 0 {
			pc = money.Round2(ordered / sale * 100)
		}
		label, _, _ := budgets.Pace(ordered, allowed)
		out = append(out, []any{p.name, p.clNm, a1, b1, c1, sale, money.Round2(allowed), money.Round2(ordered), pc, label})
	}
	return out, nil, nil
}

func bills(ctx context.Context, s *Service, f Filter) ([][]any, []int64, error) {
	w, a := place(f, 3)
	rows, err := s.DB.Query(ctx, `SELECT d.number, CASE bt.section WHEN 'amc' THEN 'AMC' ELSE 'Utility' END, bt.name, p.name, v.legal_name, coalesce(i.invoice_number,''),
		to_char(d.period_start,'Mon YYYY'), d.months, coalesce(i.amount,0),
		(SELECT i2.amount FROM document x JOIN invoice i2 ON i2.document_id=x.id AND i2.status<>'VOID' WHERE x.kind='bill' AND x.id<>d.id AND x.property_id=d.property_id AND x.bill_type_id=d.bill_type_id
			AND x.status NOT IN ('REJECTED','CANCELLED') AND x.period_start < d.period_start ORDER BY x.period_start DESC LIMIT 1), `+stageSQL+`, d.id
		FROM document d JOIN bill_type bt ON bt.id=d.bill_type_id JOIN vendor v ON v.id=d.vendor_id LEFT JOIN invoice i ON i.document_id=d.id AND i.status<>'VOID'`+placeJoin+`
		WHERE d.kind='bill' AND d.created_at >= $1 AND d.created_at < $2::date + 1`+w+` ORDER BY d.period_start DESC, d.number`, append([]any{f.From, f.To}, a...)...)
	return collect(rows, err, 11)
}

func payables(ctx context.Context, s *Service, f Filter) ([][]any, []int64, error) {
	w, a := place(f, 1)
	rows, err := s.DB.Query(ctx, `SELECT i.invoice_number, coalesce(d.po_number, d.number), v.legal_name, CASE WHEN v.msme THEN 'MSME' ELSE 'No' END, p.name, i.amount, t.tds, i.amount - t.tds - coalesce((SELECT sum(x.amount - x.tds_amount) FROM advance_use x WHERE x.invoice_id=i.id),0), i.invoice_date,
		coalesce(i.due_date, CASE WHEN coalesce(nullif(d.payment_terms,''),v.payment_terms)='15th of next month' THEN (date_trunc('month', i.invoice_date) + interval '1 month 14 days')::date
			ELSE i.invoice_date + (CASE WHEN coalesce(nullif(d.payment_terms,''),v.payment_terms) ~ '^Net [0-9]+$' THEN substring(coalesce(nullif(d.payment_terms,''),v.payment_terms) from 5)::int ELSE 0 END) END),
		CASE WHEN v.msme THEN (coalesce((SELECT received_at::date FROM goods_receipt g WHERE g.id=i.goods_receipt_id), i.created_at::date) + (SELECT (value)::int FROM setting WHERE key='msme_days'))::text ELSE '' END,
		coalesce(i.due_date, i.invoice_date) - current_date,
		CASE i.status WHEN 'MATCHED' THEN 'Payment approval pending' WHEN 'PAY_APPROVED' THEN 'Approved, not in Zoho' WHEN 'PUSHED' THEN 'In Zoho, unpaid' ELSE i.status END, coalesce(i.zoho_bill_id,''), d.id
		FROM invoice i JOIN document d ON d.id=i.document_id JOIN vendor v ON v.id=i.vendor_id`+placeJoin+`
		CROSS JOIN LATERAL (SELECT round(coalesce(sum(gl.qty*l.rate*l.tds_pct/100),0)) AS tds FROM goods_receipt_line gl JOIN document_line l ON l.id=gl.line_id WHERE gl.goods_receipt_id=i.goods_receipt_id) t
		WHERE i.status IN ('MATCHED','PAY_APPROVED','PUSHED')`+w+` ORDER BY 10`, a...)
	return collect(rows, err, 14)
}

func rates(ctx context.Context, s *Service, _ Filter) ([][]any, []int64, error) {
	rows, err := s.DB.Query(ctx, `SELECT v.legal_name, k.code, concat_ws(' ', b.name, k.item_name), r.rate, r.gst_pct, r.valid_from, r.valid_to, r.valid_to - current_date,
		CASE WHEN r.valid_to < current_date THEN 'Expired' WHEN r.valid_from > current_date THEN 'Starts later' WHEN r.valid_to - current_date < 30 THEN 'Expiring soon' ELSE 'Active' END,
		CASE WHEN EXISTS (SELECT 1 FROM vendor_mapping m WHERE m.active AND m.vendor_id=r.vendor_id AND ((m.level='sku' AND m.sku_id=r.sku_id) OR (m.level='sc' AND m.sub_category_id=k.sub_category_id))) THEN 'Yes' ELSE 'No' END
		FROM rate_card r JOIN vendor v ON v.id=r.vendor_id JOIN sku k ON k.id=r.sku_id LEFT JOIN brand b ON b.id=k.brand_id
		WHERE NOT r.superseded ORDER BY r.valid_to, v.legal_name`)
	return collect(rows, err, 10)
}

// Run produces one report.
func (s *Service) Run(ctx context.Context, key string, f Filter) (*Result, error) {
	d, ok := defs[key]
	if !ok {
		return nil, httpjson.NotFound("Report")
	}
	rows, ids, err := d.run(ctx, s, f)
	if err != nil {
		return nil, err
	}
	return &Result{Key: key, Title: d.title, Use: d.use, Group: d.group, Columns: d.cols, Rows: rows, DocIDs: ids}, nil
}

// Item is a report's key, title and group, for the role editor's report list.
type Item struct {
	Key   string `json:"key"`
	Title string `json:"title"`
	Group string `json:"group"`
}

// List gives every report, in order.
func List() []Item {
	out := []Item{}
	for _, k := range order {
		out = append(out, Item{k, defs[k].title, defs[k].group})
	}
	return out
}

// catalogue lists the reports the user's roles include (role.reports).
func (s *Service) catalogue(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	out := []map[string]string{}
	for _, k := range order {
		if !u.SeesReport(k) {
			continue
		}
		d := defs[k]
		out = append(out, map[string]string{"key": k, "title": d.title, "use": d.use, "group": d.group})
	}
	return httpjson.OK(w, out)
}

func (s *Service) one(w http.ResponseWriter, r *http.Request) error {
	if !access.From(r).SeesReport(chi.URLParam(r, "key")) {
		return httpjson.Forbidden("This report is not given to your role. Ask the System Admin to add it under Users & Rights, Roles.")
	}
	f, err := s.filter(r)
	if err != nil {
		return err
	}
	res, err := s.Run(r.Context(), chi.URLParam(r, "key"), f)
	if err != nil {
		return err
	}
	if r.URL.Query().Get("format") == "xlsx" {
		return writeXLSX(w, fmt.Sprintf("report-%s-%s.xlsx", res.Key, clock.YMD(clock.Now())), []*Result{res})
	}
	return httpjson.OK(w, res)
}

func (s *Service) pack(w http.ResponseWriter, r *http.Request) error {
	f, err := s.filter(r)
	if err != nil {
		return err
	}
	all := []*Result{}
	u := access.From(r)
	for _, k := range order {
		if !u.SeesReport(k) {
			continue
		}
		res, err := s.Run(r.Context(), k, f)
		if err != nil {
			return err
		}
		all = append(all, res)
	}
	return writeXLSX(w, fmt.Sprintf("report-pack-%s.xlsx", clock.YMD(clock.Now())), all)
}

func writeXLSX(w http.ResponseWriter, name string, res []*Result) error {
	sheets := []spreadsheet.Sheet{}
	for _, x := range res {
		heads := []string{}
		for _, c := range x.Columns {
			heads = append(heads, c.Label)
		}
		title := x.Title
		title = strings.NewReplacer("Budget against actual", "Budget vs actual", ", month by month", " by month", "Purchase request tracker", "PR tracker", "PO, GRN and invoice in one view", "PO GRN invoice").Replace(title)
		sheets = append(sheets, spreadsheet.Sheet{Name: title, Heads: heads, Rows: x.Rows})
	}
	b, err := spreadsheet.Build(sheets)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, name))
	_, err = w.Write(b)
	return err
}

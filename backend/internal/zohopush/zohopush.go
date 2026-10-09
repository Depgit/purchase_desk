// Package zohopush turns each payment-approved invoice into one Zoho Books bill, and reads payment status back.
// Before a push it checks the four mappings and holds the bill with the reason if one is missing.
// Three calls per bill: save the bill (upsert on a unique key, so a retry never makes a duplicate),
// attach the invoice file, mark the bill open. A step that fails is retried from that step.
package zohopush

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"purchasedesk/internal/access"
	"purchasedesk/internal/auditlog"
	"purchasedesk/internal/clock"
	"purchasedesk/internal/database"
	"purchasedesk/internal/documents"
	"purchasedesk/internal/httpjson"
	"purchasedesk/internal/money"
	"purchasedesk/internal/uploadedfiles"
	"purchasedesk/internal/zohoapi"
	"purchasedesk/internal/zohosync"
)

type Service struct {
	DB          *pgxpool.Pool
	Sync        *zohosync.Service
	Files       *uploadedfiles.Service
	PushEnabled bool
}

func (s *Service) Routes(r chi.Router) {
	r.Get("/zoho/queue", httpjson.Wrap(s.queue))
	r.Get("/invoices/{id}/zoho-preview", httpjson.Wrap(s.preview))
	r.Post("/zoho/push", httpjson.Wrap(s.push))
	r.Post("/zoho/read-payments", httpjson.Wrap(s.readPayments))
}

// bill is everything needed to build one Zoho bill.
type bill struct {
	InvoiceID     int64
	Status        string
	DocID         int64
	DocNumber     string
	Kind          string
	PONumber      string
	GRN           string
	InvoiceNo     string
	InvoiceDate   string
	Amount        float64
	BillGST       float64
	FileID        *int64
	ZohoBillID    string
	ZohoStep      int
	Voids         int
	MatchRemark   string
	PropertyID    int64
	Property      string
	PropertyKind  string
	PropertyState string
	VendorID      int64
	VendorGST     bool
	VendorGSTIN   string
	VendorState   string
	Terms         string
	BillTypeID    *int64
	BillType      string
	Period        string
	Months        int
	Department    string

	TagID, TagOptionID, LocationID string
	TagOK                          bool
	ContactID                      string
	ContactOK                      bool
	BillAccount                    string
	BillAccountOK                  bool
	Lines                          []line
	Connected                      bool
	KeyField                       string
}

type line struct {
	SKUCode, Item, Pack, HSN, SubCat string
	SubCatID                         int64
	Qty, Rate, GST                   float64
	Account                          string
	AccountOK                        bool
}

func load(ctx context.Context, q database.Q, invoiceID int64) (*bill, error) {
	b := &bill{InvoiceID: invoiceID}
	err := q.QueryRow(ctx, `SELECT i.status, d.id, d.number, d.kind, coalesce(d.po_number,''), coalesce(g.number,''), i.invoice_number, i.invoice_date::text, i.amount, coalesce(i.gst_pct,0),
		i.file_id, coalesce(i.zoho_bill_id,''), i.zoho_step, i.zoho_voids, i.match_remark,
		p.id, p.name, p.kind, c.state_code, v.id, v.gst_registered, coalesce(v.gstin,''), v.state_code, coalesce(nullif(d.payment_terms,''), v.payment_terms),
		d.bill_type_id, coalesce(bt.name, eh.name, ''), coalesce(to_char(d.period_start,'Mon YYYY'), cd.name, ''), coalesce(d.months,0),
		coalesce(o.tag_id,''), coalesce(mp.tag_option_id,''), coalesce(mp.location_id,''), coalesce(o.active AND zt.active,false),
		coalesce(mv.contact_id,''), coalesce(zc.active,false), coalesce(ma.account_id,''), coalesce(za.active,false),
		coalesce(z.connected,false), coalesce(z.bill_key_field,''), coalesce(cd.name,'')
		FROM invoice i JOIN document d ON d.id=i.document_id JOIN property p ON p.id=i.property_id JOIN cluster k ON k.id=p.cluster_id JOIN city c ON c.id=k.city_id
		JOIN vendor v ON v.id=i.vendor_id LEFT JOIN goods_receipt g ON g.id=i.goods_receipt_id LEFT JOIN bill_type bt ON bt.id=d.bill_type_id
		LEFT JOIN zoho_connection z ON z.id=1 LEFT JOIN zoho_map_property mp ON mp.property_id=p.id LEFT JOIN zoho_tag_option o ON o.option_id=mp.tag_option_id LEFT JOIN zoho_tag zt ON zt.tag_id=o.tag_id
		LEFT JOIN zoho_map_vendor mv ON mv.vendor_id=v.id LEFT JOIN zoho_contact zc ON zc.contact_id=mv.contact_id
		LEFT JOIN expense_head eh ON eh.id=d.expense_head_id LEFT JOIN corp_department cd ON cd.id=d.department_id
		LEFT JOIN zoho_map_account ma ON (d.kind='bill' AND ma.kind='billtype' AND ma.ref=d.bill_type_id::text) OR (d.kind='corp_bill' AND ma.kind='head' AND ma.ref=d.expense_head_id::text)
		LEFT JOIN zoho_account za ON za.account_id=ma.account_id
		WHERE i.id=$1`, invoiceID).Scan(&b.Status, &b.DocID, &b.DocNumber, &b.Kind, &b.PONumber, &b.GRN, &b.InvoiceNo, &b.InvoiceDate, &b.Amount, &b.BillGST,
		&b.FileID, &b.ZohoBillID, &b.ZohoStep, &b.Voids, &b.MatchRemark,
		&b.PropertyID, &b.Property, &b.PropertyKind, &b.PropertyState, &b.VendorID, &b.VendorGST, &b.VendorGSTIN, &b.VendorState, &b.Terms,
		&b.BillTypeID, &b.BillType, &b.Period, &b.Months,
		&b.TagID, &b.TagOptionID, &b.LocationID, &b.TagOK, &b.ContactID, &b.ContactOK, &b.BillAccount, &b.BillAccountOK, &b.Connected, &b.KeyField, &b.Department)
	if err == pgx.ErrNoRows {
		return nil, httpjson.NotFound("Invoice")
	}
	if err != nil {
		return nil, err
	}
	if b.Kind == "purchase" || b.Kind == "corp_purchase" {
		// Purchases for warehouse stock post to the stock account, and for pre-opening properties to the pre-opening account.
		special := map[string]string{"warehouse": "warehouse", "preopening": "preopening"}[b.PropertyKind]
		// Corporate lines have no SKU and post to the account of their expense head.
		rows, err := q.Query(ctx, `SELECT coalesce(k.code,''), coalesce(concat_ws(' ', b.name, k.item_name), l.description), coalesce(ps.name, l.unit), coalesce(k.hsn,''), coalesce(sc.name, eh.name, ''), coalesce(sc.id,0), gl.qty, l.rate, l.gst_pct,
			coalesce(CASE WHEN l.sku_id IS NULL THEN mh.account_id WHEN $2<>'' THEN ms.account_id ELSE ma.account_id END,''),
			coalesce(CASE WHEN l.sku_id IS NULL THEN zh.active WHEN $2<>'' THEN zs.active ELSE za.active END,false)
			FROM invoice i JOIN goods_receipt_line gl ON gl.goods_receipt_id=i.goods_receipt_id JOIN document_line l ON l.id=gl.line_id
			LEFT JOIN sku k ON k.id=l.sku_id LEFT JOIN brand b ON b.id=k.brand_id LEFT JOIN pack_size ps ON ps.id=k.pack_size_id LEFT JOIN sub_category sc ON sc.id=k.sub_category_id
			LEFT JOIN expense_head eh ON eh.id=l.expense_head_id
			LEFT JOIN zoho_map_account mh ON mh.kind='head' AND mh.ref=l.expense_head_id::text LEFT JOIN zoho_account zh ON zh.account_id=mh.account_id
			LEFT JOIN zoho_map_account ma ON ma.kind='subcat' AND ma.ref=sc.id::text LEFT JOIN zoho_account za ON za.account_id=ma.account_id
			LEFT JOIN zoho_map_account ms ON ms.kind='special' AND ms.ref=$2 LEFT JOIN zoho_account zs ON zs.account_id=ms.account_id
			WHERE i.id=$1 AND gl.qty > 0 ORDER BY l.id`, invoiceID, special)
		if err != nil {
			return nil, err
		}
		b.Lines, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (line, error) {
			var l line
			return l, row.Scan(&l.SKUCode, &l.Item, &l.Pack, &l.HSN, &l.SubCat, &l.SubCatID, &l.Qty, &l.Rate, &l.GST, &l.Account, &l.AccountOK)
		})
		if err != nil {
			return nil, err
		}
	}
	return b, nil
}

func supply(b *bill) string {
	if b.VendorState != "" && b.VendorState == b.PropertyState {
		return "intra"
	}
	return "inter"
}

func taxRates(b *bill) []float64 {
	if b.Kind == "bill" || b.Kind == "corp_bill" {
		return []float64{b.BillGST}
	}
	out := []float64{}
	seen := map[float64]bool{}
	for _, l := range b.Lines {
		if !seen[l.GST] {
			seen[l.GST] = true
			out = append(out, l.GST)
		}
	}
	return out
}

// taxIDs returns the Zoho tax for each GST rate on the bill, for the supply type (same state or another state).
func taxIDs(ctx context.Context, q database.Q, b *bill) (map[float64]string, error) {
	out := map[float64]string{}
	rows, err := q.Query(ctx, `SELECT m.gst_pct, m.tax_id FROM zoho_map_tax m JOIN zoho_tax t ON t.tax_id=m.tax_id AND t.active WHERE m.supply=$1`, supply(b))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var p float64
		var id string
		if err := rows.Scan(&p, &id); err != nil {
			return nil, err
		}
		out[p] = id
	}
	return out, rows.Err()
}

// problems lists what holds a bill back. Any one blocks the push.
func problems(b *bill, taxes map[float64]string) []string {
	o := []string{}
	if !b.Connected {
		o = append(o, "Zoho Books is not connected")
	}
	// The bill carries the tag that the property's option belongs to, so properties may sit under different tags.
	if b.TagOptionID == "" {
		o = append(o, b.Property+" has no Zoho reporting tag")
	} else if b.TagID == "" || !b.TagOK {
		o = append(o, b.Property+"'s reporting tag option is inactive or deleted in Zoho")
	}
	if b.ContactID == "" {
		o = append(o, "The vendor is not linked to a Zoho vendor")
	} else if !b.ContactOK {
		o = append(o, "The linked Zoho vendor is inactive or deleted in Zoho")
	}
	if b.Kind == "bill" || b.Kind == "corp_bill" {
		if b.BillAccount == "" {
			o = append(o, "Bill type or expense head "+b.BillType+" has no expense account")
		} else if !b.BillAccountOK {
			o = append(o, "Bill type "+b.BillType+"'s account is inactive in Zoho")
		}
	} else {
		seen := map[string]bool{}
		for _, l := range b.Lines {
			key := l.SubCat
			if seen[key] {
				continue
			}
			seen[key] = true
			if l.Account == "" {
				if l.SKUCode == "" {
					o = append(o, "Expense head "+l.SubCat+" has no Zoho account")
				} else if b.PropertyKind == "warehouse" {
					o = append(o, "No warehouse stock account is set")
				} else if b.PropertyKind == "preopening" {
					o = append(o, "No pre-opening account is set")
				} else {
					o = append(o, l.SubCat+" has no expense account")
				}
			} else if !l.AccountOK {
				o = append(o, l.SubCat+"'s account is inactive in Zoho")
			}
		}
		if len(b.Lines) == 0 {
			o = append(o, "The goods receipt has no received items")
		}
	}
	if b.VendorGST {
		if b.VendorState == "" || b.PropertyState == "" {
			o = append(o, "State is missing for the vendor or the property")
		}
		for _, g := range taxRates(b) {
			if g > 0 && taxes[g] == "" {
				where := "within the state"
				if supply(b) == "inter" {
					where = "from another state"
				}
				o = append(o, fmt.Sprintf("No Zoho tax for GST %v%% %s", g, where))
			}
		}
	}
	return o
}

// Key is the unique value stored in the Zoho bill's custom field: the document number, plus the goods receipt for part deliveries.
func key(b *bill) string {
	k := b.DocNumber
	if b.GRN != "" {
		k += "/" + b.GRN
	}
	if b.Voids > 0 {
		k += fmt.Sprintf("-R%d", b.Voids)
	}
	return k
}

// Payload builds the Zoho bill exactly as it will be sent. Field names follow the Zoho Books API, India edition.
func payload(b *bill, taxes map[float64]string) (map[string]any, []string) {
	warn := []string{}
	reg := b.VendorGST
	days := money.TermsDays(b.Terms)
	date := b.InvoiceDate
	due := date
	if d, ok := clock.ParseDate(date); ok {
		due = clock.YMD(d.AddDate(0, 0, days))
	}
	tags := []map[string]string{{"tag_id": b.TagID, "tag_option_id": b.TagOptionID}}
	items := []map[string]any{}
	computed := 0.0
	addTax := func(it map[string]any, g float64) {
		if reg && g > 0 {
			it["tax_id"] = taxes[g]
		}
	}
	if b.Kind == "bill" || b.Kind == "corp_bill" {
		rate := math.Round(b.Amount/(1+b.BillGST/100)*100) / 100
		desc := b.DocNumber + " · " + b.Property
		if b.Kind == "corp_bill" {
			desc = b.DocNumber + " · Department " + b.Department
		}
		it := map[string]any{"account_id": b.BillAccount, "name": b.BillType + " · " + b.Period, "description": desc,
			"rate": rate, "quantity": 1, "tags": tags}
		addTax(it, b.BillGST)
		items = append(items, it)
		computed = rate
		if reg {
			computed = rate * (1 + b.BillGST/100)
		}
	} else {
		missingHSN := 0
		for _, l := range b.Lines {
			desc := l.SKUCode + " · " + l.Pack + " · " + b.PONumber
			if l.SKUCode == "" {
				desc = "Department " + b.Department + " · " + l.SubCat + " · " + b.PONumber
			}
			it := map[string]any{"account_id": l.Account, "name": l.Item, "description": desc,
				"quantity": l.Qty, "rate": l.Rate, "tags": tags}
			if l.HSN != "" {
				it["hsn_or_sac"] = l.HSN
			} else {
				missingHSN++
			}
			addTax(it, l.GST)
			items = append(items, it)
			if reg {
				computed += l.Qty * l.Rate * (1 + l.GST/100)
			} else {
				computed += l.Qty * l.Rate
			}
		}
		if missingHSN > 0 {
			warn = append(warn, fmt.Sprintf("HSN code is missing on %d item(s)", missingHSN))
		}
	}
	gstTreatment := "business_none"
	if reg {
		gstTreatment = "business_gst"
	}
	ref := b.PONumber
	if ref == "" {
		ref = b.DocNumber
	}
	p := map[string]any{
		"vendor_id": b.ContactID, "bill_number": b.InvoiceNo, "reference_number": ref, "date": date, "due_date": due,
		"payment_terms": days, "payment_terms_label": b.Terms, "gst_treatment": gstTreatment, "tags": tags, "is_inclusive_tax": false,
		"line_items": items, "notes": "Purchase Desk " + b.DocNumber + " · " + b.Property,
	}
	if b.KeyField != "" {
		p["custom_fields"] = []map[string]string{{"api_name": b.KeyField, "value": key(b)}}
	}
	if reg {
		p["gst_no"] = b.VendorGSTIN
		p["source_of_supply"] = b.VendorState
		p["destination_of_supply"] = b.PropertyState
	}
	if b.LocationID != "" {
		p["location_id"] = b.LocationID
	}
	if adj := money.Round2(b.Amount - computed); math.Abs(adj) >= 0.01 {
		p["adjustment"] = adj
		p["adjustment_description"] = "Difference accepted at invoice match"
		warn = append(warn, "Invoice differs from the computed total by "+money.INR(adj)+"; sent as an adjustment")
	}
	return p, warn
}

// Held returns the reasons each invoice cannot be pushed, keyed by invoice id. It is used by the invoice lists.
func (s *Service) Held(ctx context.Context, ids []int64) (map[int64][]string, error) {
	out := map[int64][]string{}
	for _, id := range ids {
		b, err := load(ctx, s.DB, id)
		if err != nil {
			return nil, err
		}
		taxes, err := taxIDs(ctx, s.DB, b)
		if err != nil {
			return nil, err
		}
		out[id] = problems(b, taxes)
	}
	return out, nil
}

func canPush(u *access.User) bool { return u.IsAdmin || u.HasStep("finance") }

func (s *Service) preview(w http.ResponseWriter, r *http.Request) error {
	id, err := httpjson.ID(r, "id")
	if err != nil {
		return err
	}
	b, err := load(r.Context(), s.DB, id)
	if err != nil {
		return err
	}
	taxes, err := taxIDs(r.Context(), s.DB, b)
	if err != nil {
		return err
	}
	p, warn := payload(b, taxes)
	return httpjson.OK(w, map[string]any{"payload": p, "problems": problems(b, taxes), "warnings": warn, "unique_key": key(b),
		"calls": []string{"PUT /bills (X-Unique-Identifier-Key: " + b.KeyField + ", X-Upsert: true)", "POST /bills/{bill_id}/attachment", "POST /bills/{bill_id}/status/open"}})
}

type queueRow struct {
	InvoiceID  int64      `json:"invoice_id"`
	DocumentID int64      `json:"document_id"`
	DocNumber  string     `json:"document_number"`
	PONumber   string     `json:"po_number"`
	InvoiceNo  string     `json:"invoice_number"`
	Vendor     string     `json:"vendor"`
	Property   string     `json:"property"`
	Amount     float64    `json:"amount"`
	Status     string     `json:"status"`
	ZohoBillID string     `json:"zoho_bill_id"`
	ZohoStatus string     `json:"zoho_status"`
	ZohoError  string     `json:"zoho_error"`
	PushedAt   *time.Time `json:"zoho_pushed_at"`
	Held       []string   `json:"held"`
}

func (s *Service) queue(w http.ResponseWriter, r *http.Request) error {
	rows, err := s.DB.Query(r.Context(), `SELECT i.id, d.id, d.number, coalesce(d.po_number,''), i.invoice_number, v.legal_name, p.name, i.amount, i.status,
		coalesce(i.zoho_bill_id,''), i.zoho_status, i.zoho_error, i.zoho_pushed_at
		FROM invoice i JOIN document d ON d.id=i.document_id JOIN vendor v ON v.id=i.vendor_id JOIN property p ON p.id=i.property_id
		WHERE i.status='PAY_APPROVED' OR (i.zoho_bill_id IS NOT NULL AND i.zoho_pushed_at > now() - interval '60 days')
		ORDER BY (i.status='PAY_APPROVED') DESC, i.id DESC LIMIT 2000`)
	if err != nil {
		return err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (queueRow, error) {
		var x queueRow
		x.Held = []string{}
		return x, row.Scan(&x.InvoiceID, &x.DocumentID, &x.DocNumber, &x.PONumber, &x.InvoiceNo, &x.Vendor, &x.Property, &x.Amount, &x.Status, &x.ZohoBillID, &x.ZohoStatus, &x.ZohoError, &x.PushedAt)
	})
	if err != nil {
		return err
	}
	for i := range out {
		if out[i].Status == documents.InvPayApproved {
			h, err := s.Held(r.Context(), []int64{out[i].InvoiceID})
			if err != nil {
				return err
			}
			out[i].Held = h[out[i].InvoiceID]
		}
	}
	return httpjson.OK(w, out)
}

type pushResult struct {
	InvoiceID int64  `json:"invoice_id"`
	OK        bool   `json:"ok"`
	Message   string `json:"message"`
}

func (s *Service) push(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	if !canPush(u) {
		return httpjson.Forbidden("Only Finance or the System Admin can push to Zoho.")
	}
	if !s.PushEnabled {
		return httpjson.Bad("Pushing to Zoho is switched off on this server (ZOHO_PUSH_ENABLED=false). Switch it on once the custom field and mappings are checked in a test organisation.")
	}
	var in struct {
		IDs []int64 `json:"invoice_ids"`
	}
	if err := httpjson.Decode(r, &in); err != nil {
		return err
	}
	if len(in.IDs) == 0 {
		return httpjson.Bad("Select at least one invoice.")
	}
	client, conn, err := s.Sync.Client(r.Context())
	if err != nil {
		return err
	}
	useKey := false
	if fields, err := client.BillCustomFields(r.Context()); err == nil {
		for _, f := range fields {
			if fmt.Sprint(f["api_name"]) == conn.BillKeyField {
				useKey = true
			}
		}
	}
	res := []pushResult{}
	ok := 0
	// Bills are sent one after another, not in parallel, to stay within Zoho's limits.
	for i, id := range in.IDs {
		msg, err := s.pushOne(r.Context(), u, client, id, useKey)
		if err != nil {
			res = append(res, pushResult{InvoiceID: id, OK: false, Message: err.Error()})
			if zohoapi.DailyLimit(err) {
				for _, rest := range in.IDs[i+1:] {
					res = append(res, pushResult{InvoiceID: rest, OK: false, Message: "Not sent: Zoho's daily call limit is reached. Try again tomorrow."})
				}
				break
			}
			if err == zohoapi.ErrTokenRevoked {
				s.Sync.Fail(r.Context(), err)
				break
			}
			continue
		}
		ok++
		res = append(res, pushResult{InvoiceID: id, OK: true, Message: msg})
	}
	return httpjson.OK(w, map[string]any{"pushed": ok, "failed": len(in.IDs) - ok, "results": res})
}

func (s *Service) pushOne(ctx context.Context, u *access.User, c *zohoapi.Client, id int64, useKey bool) (string, error) {
	var status string
	if err := s.DB.QueryRow(ctx, `SELECT status FROM invoice WHERE id=$1`, id).Scan(&status); err != nil {
		return "", httpjson.NotFound("Invoice")
	}
	if status != documents.InvPayApproved {
		return "", fmt.Errorf("invoice is not approved for payment")
	}
	b, err := load(ctx, s.DB, id)
	if err != nil {
		return "", err
	}
	taxes, err := taxIDs(ctx, s.DB, b)
	if err != nil {
		return "", err
	}
	if pr := problems(b, taxes); len(pr) > 0 {
		_, _ = s.DB.Exec(ctx, `UPDATE invoice SET zoho_error=$2 WHERE id=$1`, id, "Held: "+strings.Join(pr, "; "))
		return "", fmt.Errorf("held: %s", strings.Join(pr, "; "))
	}
	p, _ := payload(b, taxes)
	fail := func(step string, err error) (string, error) {
		_, _ = s.DB.Exec(ctx, `UPDATE invoice SET zoho_error=$2 WHERE id=$1`, id, step+": "+err.Error())
		_ = auditlog.Doc(ctx, s.DB, u, "Finance", b.DocID, b.DocNumber, "Zoho", "Zoho push failed at "+step, err.Error())
		return "", fmt.Errorf("%s: %w", step, err)
	}
	billID := b.ZohoBillID
	if b.ZohoStep < 1 {
		var zb zohoapi.Bill
		if useKey {
			zb, err = c.UpsertBill(ctx, b.KeyField, key(b), p)
		} else {
			delete(p, "custom_fields")
			// Without the unique custom field, look for the bill first so a retry after a lost reply does not create a second one.
			var found *zohoapi.Bill
			if found, err = c.FindBill(ctx, b.ContactID, b.InvoiceNo); err == nil {
				if found != nil {
					zb = *found
				} else {
					zb, err = c.CreateBill(ctx, p)
				}
			}
		}
		if err != nil {
			return fail("saving the bill", err)
		}
		billID = zb.ID
		if _, err := s.DB.Exec(ctx, `UPDATE invoice SET zoho_bill_id=$2, zoho_step=1, zoho_status=$3, zoho_error='' WHERE id=$1`, id, billID, zb.Status); err != nil {
			return "", err
		}
		b.ZohoStep = 1
	}
	if b.ZohoStep < 2 {
		if b.FileID != nil {
			f, err := s.Files.Load(ctx, *b.FileID)
			if err != nil {
				return fail("reading the invoice file", err)
			}
			if err := c.AttachToBill(ctx, billID, f.Name, f.ContentType, f.Data); err != nil {
				return fail("attaching the invoice", err)
			}
		}
		if _, err := s.DB.Exec(ctx, `UPDATE invoice SET zoho_step=2 WHERE id=$1`, id); err != nil {
			return "", err
		}
	}
	zb, err := c.GetBill(ctx, billID)
	if err != nil {
		return fail("reading the bill back", err)
	}
	if zb.Status == "draft" {
		if err := c.MarkBillOpen(ctx, billID); err != nil {
			return fail("marking the bill open", err)
		}
		zb.Status = "open"
	}
	err = pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE invoice SET status='PUSHED', zoho_step=3, zoho_status=$2, zoho_balance=$3, zoho_pushed_at=now(), zoho_error='' WHERE id=$1`, id, zb.Status, zb.Balance); err != nil {
			return err
		}
		return auditlog.Doc(ctx, tx, u, "Finance", b.DocID, b.DocNumber, "Zoho", "Pushed to Zoho · invoice "+b.InvoiceNo, fmt.Sprintf("Zoho bill %s · %d lines · status %s", billID, len(p["line_items"].([]map[string]any)), zb.Status))
	})
	if err != nil {
		return "", err
	}
	return "Pushed as Zoho bill " + billID, nil
}

// ReadPayments asks Zoho for bills changed since the last check and follows their status:
// paid closes the cycle, partially paid shows the balance, void reopens the invoice for correction.
func (s *Service) ReadPayments(ctx context.Context) (int, error) {
	c, conn, err := s.Sync.Client(ctx)
	if err != nil {
		return 0, err
	}
	since := time.Now().AddDate(0, 0, -30)
	if conn.LastPaymentRead != nil {
		since = conn.LastPaymentRead.Add(-10 * time.Minute)
	}
	started := time.Now()
	bills, err := c.ChangedBills(ctx, since)
	if err != nil {
		s.Sync.Fail(ctx, err)
		return 0, err
	}
	changed := 0
	for _, zb := range bills {
		var id, docID int64
		var status, number, docNumber string
		err := s.DB.QueryRow(ctx, `SELECT i.id, i.status, i.invoice_number, d.id, d.number FROM invoice i JOIN document d ON d.id=i.document_id WHERE i.zoho_bill_id=$1`, zb.ID).
			Scan(&id, &status, &number, &docID, &docNumber)
		if err != nil || status != documents.InvPushed {
			continue
		}
		err = pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
			switch zb.Status {
			case "paid":
				pays, _ := c.BillPayments(ctx, zb.ID)
				refs, last := []string{}, ""
				for _, p := range pays {
					if p.ReferenceNo != "" {
						refs = append(refs, p.ReferenceNo)
					}
					if p.Date > last {
						last = p.Date
					}
				}
				paidAt := time.Now()
				if t, ok := clock.ParseDate(last); ok {
					paidAt = t
				}
				if _, err := tx.Exec(ctx, `UPDATE invoice SET status='PAID', zoho_status='paid', zoho_balance=0, zoho_paid=$2, paid_at=$3, payment_ref=$4 WHERE id=$1`,
					id, zb.Total, paidAt, strings.Join(refs, ", ")); err != nil {
					return err
				}
				if err := auditlog.Doc(ctx, tx, nil, "Zoho", docID, docNumber, "Zoho", "Paid in Zoho · invoice "+number, "Status read back from Zoho bill "+zb.ID); err != nil {
					return err
				}
				return documents.CloseIfPaid(ctx, tx, docID)
			case "void":
				if _, err := tx.Exec(ctx, `UPDATE invoice SET status=CASE WHEN goods_receipt_id IS NULL THEN 'MATCHED' ELSE 'RECEIVED' END, zoho_status='void', zoho_bill_id=NULL, zoho_step=0, zoho_voids=zoho_voids+1, zoho_error='Voided in Zoho' WHERE id=$1`, id); err != nil {
					return err
				}
				return auditlog.Doc(ctx, tx, nil, "Zoho", docID, docNumber, "Zoho", "Voided in Zoho · invoice "+number, "Reopened for correction and a fresh push")
			default:
				_, err := tx.Exec(ctx, `UPDATE invoice SET zoho_status=$2, zoho_balance=$3, zoho_paid=$4 WHERE id=$1`, id, zb.Status, zb.Balance, zb.Total-zb.Balance)
				return err
			}
		})
		if err != nil {
			return changed, err
		}
		changed++
	}
	_, err = s.DB.Exec(ctx, `UPDATE zoho_connection SET last_payment_read_at=$1 WHERE id=1`, started)
	return changed, err
}

func (s *Service) readPayments(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	if !canPush(u) {
		return httpjson.Forbidden("Only Finance or the System Admin can read payment status.")
	}
	n, err := s.ReadPayments(r.Context())
	if err != nil {
		return httpjson.Bad("Reading payments failed: %v", err)
	}
	_ = auditlog.Write(r.Context(), s.DB, u, "Zoho", "Payment status read", "Zoho Books", fmt.Sprintf("%d bills updated", n))
	return httpjson.OK(w, map[string]string{"message": fmt.Sprintf("%d pushed bills updated from Zoho.", n)})
}

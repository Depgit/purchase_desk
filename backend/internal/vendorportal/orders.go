package vendorportal

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"purchasedesk/internal/auditlog"
	"purchasedesk/internal/clock"
	"purchasedesk/internal/database"
	"purchasedesk/internal/documents"
	"purchasedesk/internal/httpjson"
	"purchasedesk/internal/money"
	"purchasedesk/internal/uploadedfiles"
)

// A vendor sees its own purchase orders once they are sent to it (order sheet or PO email): not transfers, not a PO
// still waiting for Finance's advance, not one still to push.
const ownPO = `d.vendor_id=$1 AND d.kind IN ('purchase','corp_purchase') AND NOT d.is_transfer AND d.po_number IS NOT NULL AND d.po_number<>''
	AND d.status IN ('PO_ISSUED','RECEIVED','CLOSED','CANCELLED') AND d.sent_to_vendor_at IS NOT NULL`

type orderRow struct {
	ID           int64     `json:"id"`
	PONumber     string    `json:"po_number"`
	PODate       time.Time `json:"po_date"`
	Property     string    `json:"property"`
	City         string    `json:"city"`
	DeliverBy    string    `json:"deliver_by"`
	Terms        string    `json:"payment_terms"`
	Total        float64   `json:"total"`
	Received     float64   `json:"received"`
	Status       string    `json:"status"`
	VendorStatus string    `json:"vendor_status"`
	DispatchDate string    `json:"dispatch_date"`
}

// orders lists the vendor's POs, open ones first, then the last 180 days of finished ones.
func (s *Service) orders(w http.ResponseWriter, r *http.Request) error {
	v := from(r)
	rows, err := s.DB.Query(r.Context(), `SELECT d.id, d.po_number, d.po_date, p.name, c.name, coalesce(d.deliver_by::text,''), d.payment_terms,
		coalesce((SELECT sum(`+lineValue+`) FROM document_line l WHERE l.document_id=d.id),0)::float8,
		coalesce((SELECT sum(l.received_qty*l.rate*(1+l.gst_pct/100)) FROM document_line l WHERE l.document_id=d.id),0)::float8,
		d.status, d.vendor_status, coalesce(d.dispatch_date::text,'')
		FROM document d JOIN property p ON p.id=d.property_id JOIN cluster k ON k.id=p.cluster_id JOIN city c ON c.id=k.city_id
		WHERE `+ownPO+` AND (d.status='PO_ISSUED' OR d.updated_at > now() - interval '180 days')
		ORDER BY (d.status='PO_ISSUED') DESC, d.po_date DESC LIMIT 500`, v.ID)
	if err != nil {
		return err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (orderRow, error) {
		var x orderRow
		err := row.Scan(&x.ID, &x.PONumber, &x.PODate, &x.Property, &x.City, &x.DeliverBy, &x.Terms, &x.Total, &x.Received, &x.Status, &x.VendorStatus, &x.DispatchDate)
		x.Total, x.Received = money.Round2(x.Total), money.Round2(x.Received)
		return x, err
	})
	if err != nil {
		return err
	}
	return httpjson.OK(w, out)
}

const lineValue = `l.qty*l.rate*(1+l.gst_pct/100)`

type lineView struct {
	SKUCode  string  `json:"sku_code"`
	Item     string  `json:"item"`
	PackSize string  `json:"pack_size"`
	HSN      string  `json:"hsn"`
	Qty      float64 `json:"qty"`
	Rate     float64 `json:"rate"`
	GST      float64 `json:"gst_pct"`
	Value    float64 `json:"value"`
	Received float64 `json:"received"`
	Rejected float64 `json:"rejected"`
}

// VendorInvoice is an invoice the vendor uploaded for a PO.
type VendorInvoice struct {
	ID             int64     `json:"id"`
	InvoiceNumber  string    `json:"invoice_number"`
	InvoiceDate    string    `json:"invoice_date"`
	Amount         float64   `json:"amount"`
	FileID         int64     `json:"file_id"`
	Note           string    `json:"note"`
	GoodsReceiptID *int64    `json:"goods_receipt_id"`
	CreatedAt      time.Time `json:"created_at"`
}

// Invoices lists the invoices the vendor uploaded for a PO, newest first. The document page shows them, and the
// store's goods receipt starts from the newest one not yet used.
func Invoices(ctx context.Context, q database.Q, docID int64) ([]VendorInvoice, error) {
	rows, err := q.Query(ctx, `SELECT id, invoice_number, invoice_date::text, amount::float8, file_id, note, goods_receipt_id, created_at
		FROM vendor_invoice WHERE document_id=$1 ORDER BY created_at DESC`, docID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[VendorInvoice])
}

type receiptView struct {
	Number     string    `json:"number"`
	ReceivedAt time.Time `json:"received_at"`
	Items      string    `json:"items"`
}

type orderView struct {
	ID           int64           `json:"id"`
	PONumber     string          `json:"po_number"`
	PODate       *time.Time      `json:"po_date"`
	Property     string          `json:"property"`
	City         string          `json:"city"`
	DeliverBy    string          `json:"deliver_by"`
	Terms        string          `json:"payment_terms"`
	Status       string          `json:"status"`
	StatusLabel  string          `json:"status_label"`
	VendorStatus string          `json:"vendor_status"`
	VendorNote   string          `json:"vendor_note"`
	DispatchDate string          `json:"dispatch_date"`
	ClosedShort  bool            `json:"po_closed_short"`
	Total        float64         `json:"total"`
	Lines        []lineView      `json:"lines"`
	Invoices     []VendorInvoice `json:"invoices"`
	Receipts     []receiptView   `json:"receipts"`
	Actions      []string        `json:"actions"` // accept, decline, dispatch, invoice
}

// load reads one of the vendor's POs; another vendor's PO is not found.
func (s *Service) load(ctx context.Context, q pgx.Tx, v *Vendor, id int64, lock bool) (*documents.Doc, error) {
	var d *documents.Doc
	var err error
	if q != nil {
		d, err = documents.Load(ctx, q, id, lock)
	} else {
		d, err = documents.Load(ctx, s.DB, id, false)
	}
	if err != nil {
		return nil, httpjson.NotFound("Purchase order")
	}
	if d.VendorID != v.ID || d.IsTransfer || d.PONumber == "" || (d.Kind != "purchase" && d.Kind != "corp_purchase") ||
		(d.Status != documents.POIssued && d.Status != documents.Received && d.Status != documents.Closed && d.Status != documents.Cancelled) ||
		d.SentToVendor == nil { // not pushed to the vendor yet
		return nil, httpjson.NotFound("Purchase order")
	}
	return d, nil
}

// actions are what the vendor may do with a PO now.
func actions(d *documents.Doc) []string {
	out := []string{}
	if d.Status != documents.POIssued {
		return out
	}
	received := false
	for _, l := range d.Lines {
		if l.ReceivedQty > 0 {
			received = true
		}
	}
	if d.VendorStatus != "accepted" {
		out = append(out, "accept")
	}
	if d.VendorStatus != "declined" && !received {
		out = append(out, "decline")
	}
	if d.VendorStatus == "accepted" {
		out = append(out, "dispatch")
	}
	if d.VendorStatus != "declined" {
		out = append(out, "invoice")
	}
	return out
}

func has(list []string, a string) bool {
	for _, x := range list {
		if x == a {
			return true
		}
	}
	return false
}

func (s *Service) order(w http.ResponseWriter, r *http.Request) error {
	v := from(r)
	id, err := httpjson.ID(r, "id")
	if err != nil {
		return err
	}
	d, err := s.load(r.Context(), nil, v, id, false)
	if err != nil {
		return err
	}
	o := orderView{ID: d.ID, PONumber: d.PONumber, PODate: d.PODate, Property: d.Property, City: d.City, DeliverBy: d.DeliverBy, Terms: d.PaymentTerms,
		Status: d.Status, StatusLabel: d.StatusLabel(), VendorStatus: d.VendorStatus, VendorNote: d.VendorNote, DispatchDate: d.DispatchDate,
		ClosedShort: d.ClosedShort, Total: d.Total, Lines: []lineView{}, Actions: actions(d)}
	for _, l := range d.Lines {
		if l.Qty <= 0 && l.RejectedQty <= 0 {
			continue
		}
		item, pack := strings.TrimSpace(l.Brand+" "+l.ItemName), l.PackSize
		if l.SKUID == 0 {
			item, pack = l.Description, l.Unit
		}
		o.Lines = append(o.Lines, lineView{l.SKUCode, item, pack, l.HSN, l.Qty, l.Rate, l.GST, money.Round2(money.LineValue(l.Qty, l.Rate, l.GST)), l.ReceivedQty, l.RejectedQty})
	}
	if o.Invoices, err = Invoices(r.Context(), s.DB, d.ID); err != nil {
		return err
	}
	rows, err := s.DB.Query(r.Context(), `SELECT g.number, g.received_at,
		coalesce(string_agg(concat_ws(' ', coalesce(k.item_name, l.description), '×', gl.qty::float8::text), ', ' ORDER BY l.id),'')
		FROM goods_receipt g JOIN goods_receipt_line gl ON gl.goods_receipt_id=g.id JOIN document_line l ON l.id=gl.line_id LEFT JOIN sku k ON k.id=l.sku_id
		WHERE g.document_id=$1 AND NOT g.voided GROUP BY g.id ORDER BY g.received_at`, d.ID)
	if err != nil {
		return err
	}
	if o.Receipts, err = pgx.CollectRows(rows, pgx.RowToStructByPos[receiptView]); err != nil {
		return err
	}
	return httpjson.OK(w, o)
}

// act runs a vendor's change to one of its POs in a transaction, after checking the PO allows it now.
func (s *Service) act(r *http.Request, action string, fn func(tx pgx.Tx, d *documents.Doc) (string, error)) (string, error) {
	v := from(r)
	id, err := httpjson.ID(r, "id")
	if err != nil {
		return "", err
	}
	var msg string
	err = pgx.BeginFunc(r.Context(), s.DB, func(tx pgx.Tx) error {
		d, err := s.load(r.Context(), tx, v, id, true)
		if err != nil {
			return err
		}
		if !has(actions(d), action) {
			switch {
			case d.Status != documents.POIssued:
				return httpjson.Conflict("%s is %s; nothing more can be changed on it.", d.PONumber, strings.ToLower(d.StatusLabel()))
			case action == "decline":
				return httpjson.Conflict("%s can no longer be declined: goods have already been received against it.", d.PONumber)
			case action == "dispatch":
				return httpjson.Conflict("Accept %s first, then give the dispatch date.", d.PONumber)
			default:
				return httpjson.Conflict("%s was declined. Contact the Saltstayz Purchase Team to take it up again.", d.PONumber)
			}
		}
		msg, err = fn(tx, d)
		return err
	})
	return msg, err
}

// dispatchDate reads an optional dispatch date: today or later.
func dispatchDate(v string, need bool) (*time.Time, error) {
	if strings.TrimSpace(v) == "" {
		if need {
			return nil, httpjson.Bad("Choose the dispatch date.")
		}
		return nil, nil
	}
	t, ok := clock.ParseDate(v)
	if !ok {
		return nil, httpjson.Bad("The dispatch date is not a date.")
	}
	if t.Before(clock.Today()) {
		return nil, httpjson.Bad("The dispatch date cannot be in the past.")
	}
	return &t, nil
}

func (s *Service) accept(w http.ResponseWriter, r *http.Request) error {
	v := from(r)
	var in struct {
		DispatchDate string `json:"dispatch_date"`
		Note         string `json:"note"`
	}
	if err := httpjson.Decode(r, &in); err != nil {
		return err
	}
	when, err := dispatchDate(in.DispatchDate, false)
	if err != nil {
		return err
	}
	msg, err := s.act(r, "accept", func(tx pgx.Tx, d *documents.Doc) (string, error) {
		if _, err := tx.Exec(r.Context(), `UPDATE document SET vendor_status='accepted', vendor_acted_at=now(), vendor_note=$2, dispatch_date=coalesce($3, dispatch_date), updated_at=now() WHERE id=$1`,
			d.ID, strings.TrimSpace(in.Note), when); err != nil {
			return "", err
		}
		note := strings.TrimSpace(in.Note)
		if when != nil {
			note = strings.TrimSpace("Dispatch on " + when.Format("2 Jan 2006") + ". " + note)
		}
		return d.PONumber + " accepted. Saltstayz now expects the delivery.", auditlog.ByVendor(r.Context(), tx, v.Name, d.ID, d.Number, "Accepted by vendor, awaiting delivery", note)
	})
	if err != nil {
		return err
	}
	return httpjson.OK(w, map[string]string{"message": msg})
}

func (s *Service) decline(w http.ResponseWriter, r *http.Request) error {
	v := from(r)
	var in struct {
		Reason string `json:"reason"`
	}
	if err := httpjson.Decode(r, &in); err != nil {
		return err
	}
	if len(strings.TrimSpace(in.Reason)) < 5 {
		return httpjson.Bad("Say why you cannot supply this order, so the Purchase Team can act on it.")
	}
	msg, err := s.act(r, "decline", func(tx pgx.Tx, d *documents.Doc) (string, error) {
		if _, err := tx.Exec(r.Context(), `UPDATE document SET vendor_status='declined', vendor_acted_at=now(), vendor_note=$2, updated_at=now() WHERE id=$1`,
			d.ID, strings.TrimSpace(in.Reason)); err != nil {
			return "", err
		}
		return d.PONumber + " declined. The Saltstayz Purchase Team has been told.", auditlog.ByVendor(r.Context(), tx, v.Name, d.ID, d.Number, "Declined by vendor", strings.TrimSpace(in.Reason))
	})
	if err != nil {
		return err
	}
	return httpjson.OK(w, map[string]string{"message": msg})
}

func (s *Service) dispatch(w http.ResponseWriter, r *http.Request) error {
	v := from(r)
	var in struct {
		DispatchDate string `json:"dispatch_date"`
		Note         string `json:"note"`
	}
	if err := httpjson.Decode(r, &in); err != nil {
		return err
	}
	when, err := dispatchDate(in.DispatchDate, true)
	if err != nil {
		return err
	}
	msg, err := s.act(r, "dispatch", func(tx pgx.Tx, d *documents.Doc) (string, error) {
		if _, err := tx.Exec(r.Context(), `UPDATE document SET dispatch_date=$2, updated_at=now() WHERE id=$1`, d.ID, when); err != nil {
			return "", err
		}
		return fmt.Sprintf("Dispatch date for %s set to %s.", d.PONumber, when.Format("2 Jan 2006")),
			auditlog.ByVendor(r.Context(), tx, v.Name, d.ID, d.Number, "Dispatch date given by vendor", strings.TrimSpace(when.Format("2 Jan 2006")+". "+strings.TrimSpace(in.Note)))
	})
	if err != nil {
		return err
	}
	return httpjson.OK(w, map[string]string{"message": msg})
}

// invoice keeps the vendor's invoice (number, date, amount, PDF or photo) on the PO. The store's goods receipt starts
// from it; the store still counts what arrived. An invoice not yet used can be uploaded again to correct it.
func (s *Service) invoice(w http.ResponseWriter, r *http.Request) error {
	v := from(r)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		return httpjson.Bad("Could not read the form: %v", err)
	}
	var in struct {
		InvoiceNumber string  `json:"invoice_number"`
		InvoiceDate   string  `json:"invoice_date"`
		Amount        float64 `json:"amount"`
		Note          string  `json:"note"`
	}
	if err := json.Unmarshal([]byte(r.FormValue("data")), &in); err != nil {
		return httpjson.Bad("Could not read the form data.")
	}
	in.InvoiceNumber = strings.TrimSpace(in.InvoiceNumber)
	errs := []string{}
	if in.InvoiceNumber == "" {
		errs = append(errs, "Enter the invoice number.")
	}
	date, ok := clock.ParseDate(in.InvoiceDate)
	if !ok {
		errs = append(errs, "Enter the invoice date.")
	} else if date.After(clock.Today()) {
		errs = append(errs, "The invoice date cannot be in the future.")
	}
	if in.Amount <= 0 {
		errs = append(errs, "Enter the invoice amount including GST.")
	}
	var file *uploadedfiles.File
	if fhs := r.MultipartForm.File["invoice"]; len(fhs) > 0 {
		var err error
		if file, err = uploadedfiles.Read(fhs[0]); err != nil {
			return err
		}
	} else {
		errs = append(errs, "Attach the invoice PDF or photo.")
	}
	if len(errs) > 0 {
		return httpjson.Invalid(errs)
	}
	msg, err := s.act(r, "invoice", func(tx pgx.Tx, d *documents.Doc) (string, error) {
		var entered bool
		_ = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM invoice WHERE vendor_id=$1 AND norm_key(invoice_number)=norm_key($2) AND status<>'VOID')`, v.ID, in.InvoiceNumber).Scan(&entered)
		if entered {
			return "", httpjson.Bad("Invoice %s is already entered by Saltstayz.", in.InvoiceNumber)
		}
		var other string
		_ = tx.QueryRow(r.Context(), `SELECT d.po_number FROM vendor_invoice i JOIN document d ON d.id=i.document_id
			WHERE i.vendor_id=$1 AND norm_key(i.invoice_number)=norm_key($2) AND (i.document_id<>$3 OR i.goods_receipt_id IS NOT NULL)`, v.ID, in.InvoiceNumber, d.ID).Scan(&other)
		if other != "" {
			return "", httpjson.Bad("Invoice %s is already uploaded for %s.", in.InvoiceNumber, other)
		}
		// The same number again on this PO, not yet used, replaces the earlier upload.
		if _, err := tx.Exec(r.Context(), `DELETE FROM vendor_invoice WHERE vendor_id=$1 AND document_id=$2 AND norm_key(invoice_number)=norm_key($3) AND goods_receipt_id IS NULL`,
			v.ID, d.ID, in.InvoiceNumber); err != nil {
			return "", err
		}
		fileID, err := s.Files.Save(r.Context(), tx, nil, file, "vendor-invoices", false)
		if err != nil {
			return "", err
		}
		if _, err := tx.Exec(r.Context(), `INSERT INTO vendor_invoice(document_id,vendor_id,invoice_number,invoice_date,amount,file_id,note) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
			d.ID, v.ID, in.InvoiceNumber, date, money.Round2(in.Amount), fileID, strings.TrimSpace(in.Note)); err != nil {
			return "", err
		}
		return fmt.Sprintf("Invoice %s uploaded for %s. The store checks it against the goods when they arrive.", in.InvoiceNumber, d.PONumber),
			auditlog.ByVendor(r.Context(), tx, v.Name, d.ID, d.Number, "Invoice uploaded by vendor", fmt.Sprintf("%s dated %s, %s", in.InvoiceNumber, date.Format("2 Jan 2006"), money.INR(in.Amount)))
	})
	if err != nil {
		return err
	}
	return httpjson.OK(w, map[string]string{"message": msg})
}

// file opens a file the vendor uploaded.
func (s *Service) file(w http.ResponseWriter, r *http.Request) error {
	v := from(r)
	id, err := httpjson.ID(r, "id")
	if err != nil {
		return err
	}
	var mine bool
	_ = s.DB.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM vendor_invoice WHERE file_id=$1 AND vendor_id=$2)`, id, v.ID).Scan(&mine)
	if !mine {
		return httpjson.NotFound("File")
	}
	return s.Files.Serve(w, r, id)
}

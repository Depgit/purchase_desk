// Package goodsreceipts records what actually arrived against a PO, on the phone at the receiving point:
// received quantity per line, the vendor invoice (number, date, amount, file) and item photos.
// A part delivery either keeps the balance open on the same PO or closes the PO short.
// Each delivery is its own goods receipt with its own invoice and its own three-way match.
package goodsreceipts

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"purchasedesk/internal/access"
	"purchasedesk/internal/advances"
	"purchasedesk/internal/auditlog"
	"purchasedesk/internal/clock"
	"purchasedesk/internal/documents"
	"purchasedesk/internal/httpjson"
	"purchasedesk/internal/money"
	"purchasedesk/internal/uploadedfiles"
)

type Service struct {
	DB    *pgxpool.Pool
	Files *uploadedfiles.Service
}

func (s *Service) Routes(r chi.Router) {
	r.Post("/documents/{id}/receipts", httpjson.Wrap(s.receive))
}

type receiveIn struct {
	Quantities    map[int64]float64 `json:"quantities"`
	InvoiceNumber string            `json:"invoice_number"`
	InvoiceDate   string            `json:"invoice_date"`
	InvoiceAmount float64           `json:"invoice_amount"`
	Balance       string            `json:"balance"` // keep or close
	Note          string            `json:"note"`
}

func (s *Service) receive(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	id, err := httpjson.ID(r, "id")
	if err != nil {
		return err
	}
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		return httpjson.Bad("Could not read the form: %v", err)
	}
	var in receiveIn
	if err := json.Unmarshal([]byte(r.FormValue("data")), &in); err != nil {
		return httpjson.Bad("Could not read the form data.")
	}
	errs := []string{}
	in.InvoiceNumber = strings.TrimSpace(in.InvoiceNumber)
	if in.InvoiceNumber == "" {
		errs = append(errs, "Enter the invoice number from the bill.")
	}
	invDate, ok := clock.ParseDate(in.InvoiceDate)
	if !ok {
		errs = append(errs, "Enter the invoice date.")
	} else if invDate.After(clock.Today()) {
		errs = append(errs, "The invoice date cannot be in the future.")
	}
	if in.InvoiceAmount <= 0 {
		errs = append(errs, "Enter the invoice amount.")
	}
	var invoice *uploadedfiles.File
	if fhs := r.MultipartForm.File["invoice"]; len(fhs) > 0 {
		if invoice, err = uploadedfiles.Read(fhs[0]); err != nil {
			return err
		}
	} else {
		errs = append(errs, "Attach the invoice photo or PDF.")
	}
	photos := []*uploadedfiles.File{}
	for _, fh := range r.MultipartForm.File["photos"] {
		f, err := uploadedfiles.Read(fh)
		if err != nil {
			return err
		}
		photos = append(photos, f)
	}
	if len(errs) > 0 {
		return httpjson.Invalid(errs)
	}
	var msg string
	err = pgx.BeginFunc(r.Context(), s.DB, func(tx pgx.Tx) error {
		d, err := documents.Load(r.Context(), tx, id, true)
		if err != nil {
			return err
		}
		if d.Status != documents.POIssued || d.IsTransfer {
			return httpjson.Conflict("%s is not waiting for a delivery.", d.Number)
		}
		g := documents.CanAct(u, d, d.Owner(), access.Receive)
		if g == nil {
			return httpjson.Forbidden("Only the receiving store for %s can record this delivery.", d.Property)
		}
		var dup bool
		_ = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM invoice WHERE vendor_id=$1 AND norm_key(invoice_number)=norm_key($2) AND status<>'VOID')`, d.VendorID, in.InvoiceNumber).Scan(&dup)
		if dup {
			return httpjson.Bad("This invoice number is already entered for this vendor.")
		}
		total, balance := 0.0, 0.0
		for _, l := range d.Lines {
			q := in.Quantities[l.ID]
			open := l.Qty - l.ReceivedQty
			if q < 0 {
				return httpjson.Bad("Received quantity cannot be below 0.")
			}
			if q > open {
				return httpjson.Bad("%s: %v received is more than the %v still due on this PO. Return the extra to the vendor.", l.ItemName, q, open)
			}
			total += q
			balance += open - q
		}
		if total == 0 {
			return httpjson.Bad("Nothing received. Save a goods receipt only when something has arrived.")
		}
		var deliveryNo int
		_ = tx.QueryRow(r.Context(), `SELECT count(*)+1 FROM goods_receipt WHERE document_id=$1`, id).Scan(&deliveryNo)
		more := balance > 0 && in.Balance != "close"
		// The GRN copies the request's number: GRN_HZ_0001 when everything comes in one delivery, otherwise one per
		// delivery, GRN_HZ_0001_01, _02, ... (a delivery sent back for a recount keeps its number used).
		grn, ok := documents.Derived(d.Number, "GRN")
		if ok && (deliveryNo > 1 || more) {
			grn = fmt.Sprintf("%s_%02d", grn, deliveryNo)
		}
		if !ok {
			var err error
			if grn, err = documents.NextNumber(r.Context(), tx, "grn_seq", "GRN"); err != nil {
				return err
			}
		}
		photoIDs := []int64{}
		for _, p := range photos {
			pid, err := s.Files.Save(r.Context(), tx, u, p, "item-photos", false)
			if err != nil {
				return err
			}
			photoIDs = append(photoIDs, pid)
		}
		var grnID int64
		if err := tx.QueryRow(r.Context(), `INSERT INTO goods_receipt(number,document_id,delivery_no,received_by,photo_file_ids,note) VALUES ($1,$2,$3,$4,$5,$6) RETURNING id`,
			grn, id, deliveryNo, u.ID, photoIDs, strings.TrimSpace(in.Note)).Scan(&grnID); err != nil {
			return err
		}
		for _, l := range d.Lines {
			q := in.Quantities[l.ID]
			if l.Qty-l.ReceivedQty <= 0 && q == 0 {
				continue
			}
			if _, err := tx.Exec(r.Context(), `INSERT INTO goods_receipt_line(goods_receipt_id,line_id,qty) VALUES ($1,$2,$3)`, grnID, l.ID, q); err != nil {
				return err
			}
			if _, err := tx.Exec(r.Context(), `UPDATE document_line SET received_qty=received_qty+$2 WHERE id=$1`, l.ID, q); err != nil {
				return err
			}
		}
		fileID, err := s.Files.Save(r.Context(), tx, u, invoice, "invoices", false)
		if err != nil {
			return err
		}
		var voucher string
		if err := tx.QueryRow(r.Context(), `INSERT INTO invoice(document_id,goods_receipt_id,vendor_id,property_id,invoice_number,invoice_date,amount,file_id,status,created_by)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'RECEIVED',$9) RETURNING voucher_number`, id, grnID, d.VendorID, d.PropertyID, in.InvoiceNumber, invDate, money.Round2(in.InvoiceAmount), fileID, u.ID).Scan(&voucher); err != nil {
			return err
		}
		note := ""
		status := documents.POIssued
		switch {
		case !more && balance == 0:
			status = documents.Received
		case !more:
			status = documents.Received
			note = "Short delivery. PO closed, the balance will not come"
			if _, err := tx.Exec(r.Context(), `UPDATE document SET po_closed_short=TRUE WHERE id=$1`, id); err != nil {
				return err
			}
		default:
			note = fmt.Sprintf("Part delivery. Balance stays open on %s for the next delivery", d.PONumber)
		}
		if _, err := tx.Exec(r.Context(), `UPDATE document SET status=$2, updated_at=now() WHERE id=$1`, id, status); err != nil {
			return err
		}
		if status == documents.Received {
			if err := advances.CancelUnpaid(r.Context(), tx, u, id, d.Number, "Nothing more is due on "+d.PONumber); err != nil {
				return err
			}
		}
		msg = grn + " saved, invoice " + in.InvoiceNumber + " entered as voucher " + voucher
		if status == documents.POIssued {
			msg += ". Balance stays open on " + d.PONumber
		}
		return auditlog.Doc(r.Context(), tx, u, g.Name, d.ID, d.Number, "Purchases", fmt.Sprintf("Goods received (%s, delivery %d, invoice %s, voucher %s)", grn, deliveryNo, in.InvoiceNumber, voucher), note)
	})
	if err != nil {
		return err
	}
	return httpjson.OK(w, map[string]string{"message": msg})
}

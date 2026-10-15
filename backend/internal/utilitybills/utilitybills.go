// Package utilitybills raises utility and AMC bills: a direct invoice entry with no PO, goods receipt or match.
// A bill goes to the Cluster Manager (or the Cost Controller), then to Finance for payment and Zoho.
// A second bill for the same property, bill type and period is blocked.
package utilitybills

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"purchasedesk/internal/access"
	"purchasedesk/internal/auditlog"
	"purchasedesk/internal/clock"
	"purchasedesk/internal/documents"
	"purchasedesk/internal/flowsettings"
	"purchasedesk/internal/httpjson"
	"purchasedesk/internal/indiantax"
	"purchasedesk/internal/money"
	"purchasedesk/internal/screens"
	"purchasedesk/internal/textclean"
	"purchasedesk/internal/uploadedfiles"
)

type Service struct {
	DB    *pgxpool.Pool
	Files *uploadedfiles.Service
}

func (s *Service) Routes(r chi.Router) {
	r.Get("/bills/form", httpjson.Wrap(s.form))
	r.Post("/bills", httpjson.Wrap(s.create))
}

type vendorOpt struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type typeOpt struct {
	ID         int64       `json:"id"`
	Name       string      `json:"name"`
	Section    string      `json:"section"`
	DefaultGST float64     `json:"default_gst"`
	Vendors    []vendorOpt `json:"vendors"`
}

// form lists active bill types with the verified vendors that can bill for each.
func (s *Service) form(w http.ResponseWriter, r *http.Request) error {
	rows, err := s.DB.Query(r.Context(), `SELECT b.id, b.name, b.section, b.default_gst,
		coalesce(json_agg(json_build_object('id',v.id,'name',v.legal_name) ORDER BY v.legal_name) FILTER (WHERE v.id IS NOT NULL),'[]')
		FROM bill_type b LEFT JOIN vendor_bill_type vb ON vb.bill_type_id=b.id LEFT JOIN vendor v ON v.id=vb.vendor_id AND v.active AND v.bank_verified
		WHERE b.active AND b.fin_status='approved' GROUP BY b.id ORDER BY b.section, b.name`)
	if err != nil {
		return err
	}
	types, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (typeOpt, error) {
		var t typeOpt
		var vs []byte
		if err := row.Scan(&t.ID, &t.Name, &t.Section, &t.DefaultGST, &vs); err != nil {
			return t, err
		}
		return t, json.Unmarshal(vs, &t.Vendors)
	})
	if err != nil {
		return err
	}
	st, err := flowsettings.Load(r.Context(), s.DB)
	if err != nil {
		return err
	}
	return httpjson.OK(w, map[string]any{"types": types, "amc_durations": st.AMCDurations, "approver": st.BillApprover})
}

type billIn struct {
	BillTypeID    int64   `json:"bill_type_id"`
	PropertyID    int64   `json:"property_id"`
	VendorID      int64   `json:"vendor_id"`
	PeriodStart   string  `json:"period_start"` // YYYY-MM
	Months        int     `json:"months"`
	InvoiceNumber string  `json:"invoice_number"`
	InvoiceDate   string  `json:"invoice_date"`
	InvoiceAmount float64 `json:"invoice_amount"`
	GST           float64 `json:"gst_pct"`
	Remark        string  `json:"remark"`
}

func (s *Service) create(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	ctx := r.Context()
	if err := r.ParseMultipartForm(uploadedfiles.MaxBytes + (1 << 20)); err != nil {
		return httpjson.Bad("Could not read the form: %v", err)
	}
	var in billIn
	if err := json.Unmarshal([]byte(r.FormValue("data")), &in); err != nil {
		return httpjson.Bad("Could not read the form data.")
	}
	st, err := flowsettings.Load(ctx, s.DB)
	if err != nil {
		return err
	}
	errs := []string{}
	var section, typeName string
	var typeActive bool
	if err := s.DB.QueryRow(ctx, `SELECT section, name, active AND fin_status='approved' FROM bill_type WHERE id=$1`, in.BillTypeID).Scan(&section, &typeName, &typeActive); err != nil || !typeActive {
		errs = append(errs, "Choose an active bill type.")
	}
	var propName, propKind string
	var clusterID int64
	var propActive bool
	if err := s.DB.QueryRow(ctx, `SELECT name, kind, cluster_id, active AND fin_status='approved' FROM property WHERE id=$1`, in.PropertyID).Scan(&propName, &propKind, &clusterID, &propActive); err != nil || !propActive {
		errs = append(errs, "Choose an active property.")
	}
	var vendorOK bool
	_ = s.DB.QueryRow(ctx, `SELECT v.active AND v.bank_verified FROM vendor v JOIN vendor_bill_type vb ON vb.vendor_id=v.id AND vb.bill_type_id=$2 WHERE v.id=$1`, in.VendorID, in.BillTypeID).Scan(&vendorOK)
	if !vendorOK {
		errs = append(errs, "Choose a verified vendor linked to this bill type. Onboard the vendor first under Vendors.")
	}
	start, ok := clock.ParseMonth(in.PeriodStart)
	if !ok {
		errs = append(errs, "Choose the month.")
	}
	if section == "utility" {
		in.Months = 1
	} else if !textclean.Contains(st.AMCDurations, in.Months) {
		errs = append(errs, "Choose an AMC duration from the list.")
	}
	in.InvoiceNumber = strings.TrimSpace(in.InvoiceNumber)
	if in.InvoiceNumber == "" {
		errs = append(errs, "Enter the invoice number.")
	}
	invDate, okd := clock.ParseDate(in.InvoiceDate)
	if !okd {
		errs = append(errs, "Enter the invoice date.")
	}
	if in.InvoiceAmount <= 0 {
		errs = append(errs, "Enter the invoice amount.")
	}
	if !indiantax.ValidRate(in.GST) {
		errs = append(errs, "GST % must be 0, 5, 12, 18, 28 or 40.")
	}
	var file *uploadedfiles.File
	if fhs := r.MultipartForm.File["invoice"]; len(fhs) > 0 {
		if file, err = uploadedfiles.Read(fhs[0]); err != nil {
			return err
		}
	} else {
		errs = append(errs, "Attach the invoice file.")
	}
	if len(errs) == 0 {
		end := start.AddDate(0, in.Months, 0)
		var clash string
		_ = s.DB.QueryRow(ctx, `SELECT number FROM document WHERE kind='bill' AND property_id=$1 AND bill_type_id=$2 AND status NOT IN ('REJECTED','CANCELLED')
			AND period_start < $4 AND (period_start + make_interval(months => months)) > $3 LIMIT 1`, in.PropertyID, in.BillTypeID, start, end).Scan(&clash)
		if clash != "" {
			errs = append(errs, fmt.Sprintf("A bill for %s at this property already covers this period (%s).", typeName, clash))
		}
		var dup bool
		_ = s.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM invoice WHERE vendor_id=$1 AND norm_key(invoice_number)=norm_key($2) AND status<>'VOID')`, in.VendorID, in.InvoiceNumber).Scan(&dup)
		if dup {
			errs = append(errs, "This invoice number is already entered for this vendor.")
		}
	}
	if len(errs) > 0 {
		return httpjson.Invalid(errs)
	}
	screen := map[string]string{"amc": "bills_amc"}[section]
	if screen == "" {
		screen = "bills_utility"
	}
	if u.Level(screen, in.PropertyID, clusterID) < screens.Edit {
		return httpjson.Forbidden("Your role can only view %s at %s, not enter them.", screens.Label(screen), propName)
	}
	g := u.Can("", access.Raise, in.PropertyID, clusterID, nil)
	if g == nil {
		return httpjson.Forbidden("You cannot raise bills for %s.", propName)
	}
	var number, voucher string
	err = pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		var err error
		if number, err = documents.NextNumber(ctx, tx, "br_seq", "BR"); err != nil {
			return err
		}
		var id int64
		if err := tx.QueryRow(ctx, `INSERT INTO document(number,kind,property_id,vendor_id,requester_id,requester_step,status,route_steps,step_idx,bill_type_id,period_start,months,remark,payment_terms,created_by)
			VALUES ($1,'bill',$2,$3,$4,$5,'PENDING',$6,0,$7,$8,$9,$10,(SELECT payment_terms FROM vendor WHERE id=$3),$4) RETURNING id`,
			number, in.PropertyID, in.VendorID, u.ID, g.Step, []string{st.BillApprover}, in.BillTypeID, start, in.Months, strings.TrimSpace(in.Remark)).Scan(&id); err != nil {
			return err
		}
		fid, err := s.Files.Save(ctx, tx, u, file, "invoices", false)
		if err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO invoice(document_id,vendor_id,property_id,invoice_number,invoice_date,amount,gst_pct,file_id,status,created_by)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'AWAITING',$9) RETURNING voucher_number`, id, in.VendorID, in.PropertyID, in.InvoiceNumber, invDate, money.Round2(in.InvoiceAmount), in.GST, fid, u.ID).Scan(&voucher); err != nil {
			return err
		}
		return auditlog.Doc(ctx, tx, u, g.Name, id, number, "Bills", "Bill submitted", typeName+" · "+in.PeriodStart+" · voucher "+voucher)
	})
	if err != nil {
		return err
	}
	return httpjson.OK(w, map[string]string{"message": number + " (voucher " + voucher + ") sent to the " + documents.StepNames[st.BillApprover], "number": number, "voucher_number": voucher})
}

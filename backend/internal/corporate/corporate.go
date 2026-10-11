// Package corporate runs the corporate office's purchases (things still to buy, with a PO) and bills (invoices
// already received, no PO). Lines have no SKU: a description and an expense head that maps to a Zoho account.
// Every request is approved by the department's L1 then its L2, set per department in the approval matrix.
package corporate

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"purchasedesk/internal/access"
	"purchasedesk/internal/approvals"
	"purchasedesk/internal/auditlog"
	"purchasedesk/internal/clock"
	"purchasedesk/internal/documents"
	"purchasedesk/internal/flowsettings"
	"purchasedesk/internal/httpjson"
	"purchasedesk/internal/indiantax"
	"purchasedesk/internal/money"
	"purchasedesk/internal/textclean"
	"purchasedesk/internal/uploadedfiles"
)

type Service struct {
	DB    *pgxpool.Pool
	Files *uploadedfiles.Service
}

func (s *Service) Routes(r chi.Router) {
	r.Get("/corporate/departments", httpjson.Wrap(s.listDepartments))
	r.Post("/corporate/departments", httpjson.Wrap(s.saveDepartment))
	r.Put("/corporate/departments/{id}", httpjson.Wrap(s.saveDepartment))
	r.Get("/corporate/expense-heads", httpjson.Wrap(s.listHeads))
	r.Post("/corporate/expense-heads", httpjson.Wrap(s.createHead))
	r.Get("/corporate/form", httpjson.Wrap(s.form))
	r.Post("/corporate/requests", httpjson.Wrap(s.create))
}

type Department struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	L1ID     int64  `json:"l1_user_id"`
	L1       string `json:"l1"`
	L2ID     int64  `json:"l2_user_id"`
	L2       string `json:"l2"`
	BackupID *int64 `json:"backup_user_id"`
	Backup   string `json:"backup"`
	Active   bool   `json:"active"`
	Open     int    `json:"open_requests"`
}

func (s *Service) departments(ctx context.Context) ([]Department, error) {
	rows, err := s.DB.Query(ctx, `SELECT d.id, d.name, d.l1_user_id, u1.name, d.l2_user_id, u2.name, d.backup_user_id, coalesce(ub.name,''), d.active,
		(SELECT count(*) FROM document x WHERE x.department_id=d.id AND x.status NOT IN ('CLOSED','REJECTED','CANCELLED'))
		FROM corp_department d JOIN app_user u1 ON u1.id=d.l1_user_id JOIN app_user u2 ON u2.id=d.l2_user_id LEFT JOIN app_user ub ON ub.id=d.backup_user_id ORDER BY d.name`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Department, error) {
		var x Department
		return x, row.Scan(&x.ID, &x.Name, &x.L1ID, &x.L1, &x.L2ID, &x.L2, &x.BackupID, &x.Backup, &x.Active, &x.Open)
	})
}

func (s *Service) listDepartments(w http.ResponseWriter, r *http.Request) error {
	out, err := s.departments(r.Context())
	if err != nil {
		return err
	}
	return httpjson.OK(w, out)
}

func (s *Service) saveDepartment(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	if err := access.NeedRight(u, access.Masters, "edit masters"); err != nil {
		return err
	}
	var in struct {
		Name     string `json:"name"`
		L1ID     int64  `json:"l1_user_id"`
		L2ID     int64  `json:"l2_user_id"`
		BackupID int64  `json:"backup_user_id"`
	}
	if err := httpjson.Decode(r, &in); err != nil {
		return err
	}
	in.Name = textclean.Clean(in.Name)
	id := int64(0)
	if chi.URLParam(r, "id") != "" {
		var err error
		if id, err = httpjson.ID(r, "id"); err != nil {
			return err
		}
	}
	errs := []string{}
	if len(in.Name) < 2 {
		errs = append(errs, "Enter the department name.")
	}
	if in.L1ID == 0 || in.L2ID == 0 {
		errs = append(errs, "Choose the L1 and the L2 approver.")
	} else if in.L1ID == in.L2ID {
		errs = append(errs, "L1 and L2 must be two different people.")
	}
	if in.BackupID != 0 && (in.BackupID == in.L1ID || in.BackupID == in.L2ID) {
		errs = append(errs, "The backup must be a third person.")
	}
	var dup bool
	_ = s.DB.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM corp_department WHERE norm_key(name)=norm_key($1) AND id<>$2)`, in.Name, id).Scan(&dup)
	if dup {
		errs = append(errs, "This department already exists.")
	}
	if len(errs) > 0 {
		return httpjson.Invalid(errs)
	}
	var backup any
	if in.BackupID != 0 {
		backup = in.BackupID
	}
	if id == 0 {
		if err := s.DB.QueryRow(r.Context(), `INSERT INTO corp_department(name,l1_user_id,l2_user_id,backup_user_id,created_by) VALUES ($1,$2,$3,$4,$5) RETURNING id`,
			in.Name, in.L1ID, in.L2ID, backup, u.ID).Scan(&id); err != nil {
			return err
		}
		_ = auditlog.Write(r.Context(), s.DB, u, "Corporate", "Department added", in.Name, "")
	} else {
		if _, err := s.DB.Exec(r.Context(), `UPDATE corp_department SET name=$2, l1_user_id=$3, l2_user_id=$4, backup_user_id=$5 WHERE id=$1`, id, in.Name, in.L1ID, in.L2ID, backup); err != nil {
			return err
		}
		_ = auditlog.Write(r.Context(), s.DB, u, "Corporate", "Approval matrix changed", in.Name, "Open requests keep their approvers; new requests use this")
	}
	return httpjson.OK(w, map[string]int64{"id": id})
}

type Head struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Active bool   `json:"active"`
	Used   int    `json:"used"`
}

func (s *Service) heads(ctx context.Context) ([]Head, error) {
	rows, err := s.DB.Query(ctx, `SELECT h.id, h.name, h.active,
		(SELECT count(*) FROM document_line l WHERE l.expense_head_id=h.id) + (SELECT count(*) FROM document d WHERE d.expense_head_id=h.id)
		FROM expense_head h ORDER BY h.name`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Head, error) {
		var x Head
		return x, row.Scan(&x.ID, &x.Name, &x.Active, &x.Used)
	})
}

func (s *Service) listHeads(w http.ResponseWriter, r *http.Request) error {
	out, err := s.heads(r.Context())
	if err != nil {
		return err
	}
	return httpjson.OK(w, out)
}

func (s *Service) createHead(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	if err := access.NeedRight(u, access.Masters, "edit masters"); err != nil {
		return err
	}
	var in struct {
		Name string `json:"name"`
	}
	if err := httpjson.Decode(r, &in); err != nil {
		return err
	}
	in.Name = textclean.Clean(in.Name)
	if len(in.Name) < 3 {
		return httpjson.Bad("Enter the expense head name.")
	}
	var dup bool
	_ = s.DB.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM expense_head WHERE norm_key(name)=norm_key($1))`, in.Name).Scan(&dup)
	if dup {
		return httpjson.Bad("This expense head already exists.")
	}
	var id int64
	if err := s.DB.QueryRow(r.Context(), `INSERT INTO expense_head(name,created_by) VALUES ($1,$2) RETURNING id`, in.Name, u.ID).Scan(&id); err != nil {
		return err
	}
	_ = auditlog.Write(r.Context(), s.DB, u, "Corporate", "Expense head added", in.Name, "Map it to a Zoho account under Settings, Zoho Books")
	return httpjson.OK(w, map[string]int64{"id": id})
}

// office returns the corporate office property, which carries the Corporate Office reporting tag in Zoho.
func (s *Service) office(ctx context.Context) (int64, string, error) {
	var id int64
	var name string
	err := s.DB.QueryRow(ctx, `SELECT id, name FROM property WHERE kind='corporate' AND active ORDER BY id LIMIT 1`).Scan(&id, &name)
	if err != nil {
		return 0, "", httpjson.Bad("Add the corporate office under Settings, Properties, with stage Corporate office.")
	}
	return id, name, nil
}

type vendorOpt struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Type string `json:"vendor_type"`
}

func (s *Service) form(w http.ResponseWriter, r *http.Request) error {
	_, office, err := s.office(r.Context())
	if err != nil {
		return err
	}
	deps, err := s.departments(r.Context())
	if err != nil {
		return err
	}
	heads, err := s.heads(r.Context())
	if err != nil {
		return err
	}
	rows, err := s.DB.Query(r.Context(), `SELECT id, legal_name, vendor_type FROM vendor WHERE active AND bank_verified AND vendor_type<>'warehouse' ORDER BY legal_name`)
	if err != nil {
		return err
	}
	vs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (vendorOpt, error) {
		var v vendorOpt
		return v, row.Scan(&v.ID, &v.Name, &v.Type)
	})
	if err != nil {
		return err
	}
	st, err := flowsettings.Load(r.Context(), s.DB)
	if err != nil {
		return err
	}
	return httpjson.OK(w, map[string]any{"office": office, "departments": deps, "expense_heads": heads, "vendors": vs, "l1_only_below": st.CorpL1OnlyBelow})
}

type lineIn struct {
	Description string  `json:"description"`
	HeadID      int64   `json:"expense_head_id"`
	Qty         float64 `json:"qty"`
	Unit        string  `json:"unit"`
	Rate        float64 `json:"rate"`
	GST         float64 `json:"gst_pct"`
}

type createIn struct {
	Kind          string   `json:"kind"` // purchase or bill
	DepartmentID  int64    `json:"department_id"`
	VendorID      int64    `json:"vendor_id"`
	Note          string   `json:"note"`
	Lines         []lineIn `json:"lines"`
	HeadID        int64    `json:"expense_head_id"`
	InvoiceNumber string   `json:"invoice_number"`
	InvoiceDate   string   `json:"invoice_date"`
	InvoiceAmount float64  `json:"invoice_amount"`
	GST           float64  `json:"gst_pct"`
}

func (s *Service) create(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	ctx := r.Context()
	if err := r.ParseMultipartForm(40 << 20); err != nil {
		return httpjson.Bad("Could not read the form: %v", err)
	}
	var in createIn
	if err := json.Unmarshal([]byte(r.FormValue("data")), &in); err != nil {
		return httpjson.Bad("Could not read the form data.")
	}
	officeID, _, err := s.office(ctx)
	if err != nil {
		return err
	}
	var clusterID int64
	_ = s.DB.QueryRow(ctx, `SELECT cluster_id FROM property WHERE id=$1`, officeID).Scan(&clusterID)
	g := u.Can("", access.Raise, officeID, clusterID, nil)
	if g == nil {
		return httpjson.Forbidden("You cannot raise corporate requests.")
	}
	errs := []string{}
	var dept Department
	if err := s.DB.QueryRow(ctx, `SELECT id, name, l1_user_id, l2_user_id, backup_user_id FROM corp_department WHERE id=$1 AND active`, in.DepartmentID).Scan(&dept.ID, &dept.Name, &dept.L1ID, &dept.L2ID, &dept.BackupID); err != nil {
		errs = append(errs, "Choose the department.")
	}
	var vendorOK bool
	var terms string
	_ = s.DB.QueryRow(ctx, `SELECT active AND bank_verified AND vendor_type<>'warehouse', payment_terms FROM vendor WHERE id=$1`, in.VendorID).Scan(&vendorOK, &terms)
	if !vendorOK {
		errs = append(errs, "Choose an active vendor with verified bank details.")
	}
	in.Note = strings.TrimSpace(in.Note)
	if len(in.Note) < 5 {
		errs = append(errs, "Write why this is needed (a few words).")
	}
	headOK := func(id int64) bool {
		var ok bool
		_ = s.DB.QueryRow(ctx, `SELECT active FROM expense_head WHERE id=$1`, id).Scan(&ok)
		return ok
	}
	amount := 0.0
	var invoice, quote *uploadedfiles.File
	switch in.Kind {
	case "purchase":
		if len(in.Lines) == 0 {
			errs = append(errs, "Add at least one line.")
		}
		for i, l := range in.Lines {
			n := i + 1
			if len(strings.TrimSpace(l.Description)) < 3 {
				errs = append(errs, "Line "+strconv.Itoa(n)+": describe the item.")
			}
			if !headOK(l.HeadID) {
				errs = append(errs, "Line "+strconv.Itoa(n)+": choose an active expense head.")
			}
			if l.Qty <= 0 || l.Rate <= 0 {
				errs = append(errs, "Line "+strconv.Itoa(n)+": quantity and rate must be above 0.")
			}
			if !indiantax.ValidRate(l.GST) {
				errs = append(errs, "Line "+strconv.Itoa(n)+": GST % must be 0, 5, 12, 18, 28 or 40.")
			}
			amount += money.LineValue(l.Qty, l.Rate, l.GST)
		}
		if fhs := r.MultipartForm.File["quote"]; len(fhs) > 0 {
			if quote, err = uploadedfiles.Read(fhs[0]); err != nil {
				return err
			}
		}
	case "bill":
		if !headOK(in.HeadID) {
			errs = append(errs, "Choose an active expense head.")
		}
		in.InvoiceNumber = strings.TrimSpace(in.InvoiceNumber)
		if in.InvoiceNumber == "" {
			errs = append(errs, "Enter the invoice number.")
		}
		if d, ok := clock.ParseDate(in.InvoiceDate); !ok || d.After(clock.Today()) {
			errs = append(errs, "Enter the invoice date, today or earlier.")
		}
		if in.InvoiceAmount <= 0 {
			errs = append(errs, "Enter the invoice amount.")
		}
		if !indiantax.ValidRate(in.GST) {
			errs = append(errs, "GST % must be 0, 5, 12, 18, 28 or 40.")
		}
		if fhs := r.MultipartForm.File["invoice"]; len(fhs) > 0 {
			if invoice, err = uploadedfiles.Read(fhs[0]); err != nil {
				return err
			}
		} else {
			errs = append(errs, "Attach the invoice file.")
		}
		var dup bool
		_ = s.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM invoice WHERE vendor_id=$1 AND norm_key(invoice_number)=norm_key($2) AND status<>'VOID')`, in.VendorID, in.InvoiceNumber).Scan(&dup)
		if dup {
			errs = append(errs, "This invoice number is already entered for this vendor.")
		}
		amount = in.InvoiceAmount
	default:
		errs = append(errs, "Choose a corporate purchase or a corporate bill.")
	}
	if len(errs) > 0 {
		return httpjson.Invalid(errs)
	}
	st, err := flowsettings.Load(ctx, s.DB)
	if err != nil {
		return err
	}
	steps := approvals.CorpSteps(st, amount, u.ID, dept.L1ID)
	var number, voucher string
	var id int64
	err = pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		kind, seq, prefix := "corp_purchase", "cr_seq", "CR"
		if in.Kind == "bill" {
			kind, seq, prefix = "corp_bill", "cb_seq", "CB"
		}
		var err error
		if number, err = documents.NextNumber(ctx, tx, seq, prefix); err != nil {
			return err
		}
		var quoteID, head any
		if quote != nil {
			qid, err := s.Files.Save(ctx, tx, u, quote, "quotations", false)
			if err != nil {
				return err
			}
			quoteID = qid
		}
		if in.Kind == "bill" {
			head = in.HeadID
		}
		if err := tx.QueryRow(ctx, `INSERT INTO document(number,kind,property_id,vendor_id,requester_id,requester_step,status,route_steps,step_idx,department_id,
			corp_l1_user_id,corp_l2_user_id,corp_backup_user_id,expense_head_id,quote_file_id,remark,payment_terms,created_by)
			VALUES ($1,$2,$3,$4,$5,$6,'PENDING',$7,0,$8,$9,$10,$11,$12,$13,$14,$15,$5) RETURNING id`,
			number, kind, officeID, in.VendorID, u.ID, g.Step, steps, dept.ID, dept.L1ID, dept.L2ID, dept.BackupID, head, quoteID, in.Note, terms).Scan(&id); err != nil {
			return err
		}
		for _, l := range in.Lines {
			if in.Kind != "purchase" {
				break
			}
			if _, err := tx.Exec(ctx, `INSERT INTO document_line(document_id,description,expense_head_id,qty,unit,rate,gst_pct) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
				id, textclean.Clean(l.Description), l.HeadID, l.Qty, textclean.Default(textclean.Clean(l.Unit), "piece"), money.Round2(l.Rate), l.GST); err != nil {
				return err
			}
		}
		if invoice != nil {
			fid, err := s.Files.Save(ctx, tx, u, invoice, "invoices", false)
			if err != nil {
				return err
			}
			date, _ := clock.ParseDate(in.InvoiceDate)
			if err := tx.QueryRow(ctx, `INSERT INTO invoice(document_id,vendor_id,property_id,invoice_number,invoice_date,amount,gst_pct,file_id,status,created_by)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'AWAITING',$9) RETURNING voucher_number`, id, in.VendorID, officeID, in.InvoiceNumber, date, money.Round2(in.InvoiceAmount), in.GST, fid, u.ID).Scan(&voucher); err != nil {
				return err
			}
		}
		note := dept.Name
		if len(steps) == 1 && steps[0] == "cl2" {
			note += " · raised by the L1, so it goes straight to the L2"
		}
		return auditlog.Doc(ctx, tx, u, g.Name, id, number, "Corporate", "Raised", note)
	})
	if err != nil {
		return err
	}
	msg := number + " sent for approval"
	if voucher != "" {
		msg = number + " (voucher " + voucher + ") sent for approval"
	}
	return httpjson.OK(w, map[string]any{"id": id, "number": number, "voucher_number": voucher, "message": msg})
}

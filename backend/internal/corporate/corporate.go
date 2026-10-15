// Package corporate runs the corporate office's purchases (things still to buy, with a PO) and bills (invoices
// already received, no PO). Lines have no SKU: a description and an expense head that maps to a Zoho account.
// Every request is approved by the department's L1 then its L2, set per department in the approval matrix.
package corporate

import (
	"context"
	"encoding/json"
	"fmt"
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
	"purchasedesk/internal/database"
	"purchasedesk/internal/documents"
	"purchasedesk/internal/flowsettings"
	"purchasedesk/internal/httpjson"
	"purchasedesk/internal/indiantax"
	"purchasedesk/internal/money"
	"purchasedesk/internal/textclean"
	"purchasedesk/internal/uploadedfiles"
	"purchasedesk/internal/vendormapping"
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

// DeptInput is a department and its approvers, from the screen or an upload row.
type DeptInput struct {
	Name     string `json:"name"`
	L1ID     int64  `json:"l1_user_id"`
	L2ID     int64  `json:"l2_user_id"`
	BackupID int64  `json:"backup_user_id"`
}

// ValidateDepartment tidies the input and returns every problem. id is the department being edited, or 0.
func ValidateDepartment(ctx context.Context, q database.Q, in *DeptInput, id int64) []string {
	in.Name = textclean.Clean(in.Name)
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
	_ = q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM corp_department WHERE norm_key(name)=norm_key($1) AND id<>$2)`, in.Name, id).Scan(&dup)
	if dup {
		errs = append(errs, "This department already exists.")
	}
	return errs
}

// SaveDepartment adds (id 0) or updates a validated department. Open requests keep the approvers they were raised with.
func SaveDepartment(ctx context.Context, q database.Q, u *access.User, id int64, in DeptInput) (int64, error) {
	var backup any
	if in.BackupID != 0 {
		backup = in.BackupID
	}
	if id == 0 {
		if err := q.QueryRow(ctx, `INSERT INTO corp_department(name,l1_user_id,l2_user_id,backup_user_id,created_by) VALUES ($1,$2,$3,$4,$5) RETURNING id`,
			in.Name, in.L1ID, in.L2ID, backup, u.ID).Scan(&id); err != nil {
			return 0, err
		}
		return id, auditlog.Write(ctx, q, u, "Corporate", "Department added", in.Name, "")
	}
	if _, err := q.Exec(ctx, `UPDATE corp_department SET name=$2, l1_user_id=$3, l2_user_id=$4, backup_user_id=$5 WHERE id=$1`, id, in.Name, in.L1ID, in.L2ID, backup); err != nil {
		return 0, err
	}
	return id, auditlog.Write(ctx, q, u, "Corporate", "Approval matrix changed", in.Name, "Open requests keep their approvers; new requests use this")
}

func (s *Service) saveDepartment(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	if err := access.NeedRight(u, access.Masters, "edit masters"); err != nil {
		return err
	}
	var in DeptInput
	if err := httpjson.Decode(r, &in); err != nil {
		return err
	}
	id := int64(0)
	if chi.URLParam(r, "id") != "" {
		var err error
		if id, err = httpjson.ID(r, "id"); err != nil {
			return err
		}
	}
	if errs := ValidateDepartment(r.Context(), s.DB, &in, id); len(errs) > 0 {
		return httpjson.Invalid(errs)
	}
	id, err := SaveDepartment(r.Context(), s.DB, u, id, in)
	if err != nil {
		return err
	}
	return httpjson.OK(w, map[string]int64{"id": id})
}

type Head struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Active    bool   `json:"active"`
	Used      int    `json:"used"`
	FinStatus string `json:"fin_status"` // pending until Finance approves a new head
}

func (s *Service) heads(ctx context.Context) ([]Head, error) {
	rows, err := s.DB.Query(ctx, `SELECT h.id, h.name, h.active,
		(SELECT count(*) FROM document_line l WHERE l.expense_head_id=h.id) + (SELECT count(*) FROM document d WHERE d.expense_head_id=h.id)
		, h.fin_status FROM expense_head h ORDER BY h.name`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Head, error) {
		var x Head
		return x, row.Scan(&x.ID, &x.Name, &x.Active, &x.Used, &x.FinStatus)
	})
}

func (s *Service) listHeads(w http.ResponseWriter, r *http.Request) error {
	out, err := s.heads(r.Context())
	if err != nil {
		return err
	}
	return httpjson.OK(w, out)
}

// ValidateHead tidies a new expense head's name and returns every problem.
func ValidateHead(ctx context.Context, q database.Q, name *string) []string {
	*name = textclean.Clean(*name)
	if len(*name) < 3 {
		return []string{"Enter the expense head name."}
	}
	var dup bool
	_ = q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM expense_head WHERE norm_key(name)=norm_key($1))`, *name).Scan(&dup)
	if dup {
		return []string{"This expense head already exists."}
	}
	return nil
}

// SaveHead adds a validated expense head. It waits for Finance's approval before it can be used.
func SaveHead(ctx context.Context, q database.Q, u *access.User, name string) (int64, error) {
	var id int64
	if err := q.QueryRow(ctx, `INSERT INTO expense_head(name,created_by) VALUES ($1,$2) RETURNING id`, name, u.ID).Scan(&id); err != nil {
		return 0, err
	}
	return id, auditlog.Write(ctx, q, u, "Corporate", "Expense head added", name, "Map it to a Zoho account under Settings, Zoho Books")
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
	if errs := ValidateHead(r.Context(), s.DB, &in.Name); len(errs) > 0 {
		return httpjson.Bad("%s", errs[0])
	}
	id, err := SaveHead(r.Context(), s.DB, u, in.Name)
	if err != nil {
		return err
	}
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

// Item is an item (SKU) a corporate purchase may order: active, approved by Finance, and available at the corporate
// office (its Available at is all properties, the office's cluster, or the office). Corporate lines never create items.
type Item struct {
	ID       int64   `json:"id"`
	Code     string  `json:"code"`
	Name     string  `json:"name"`
	Pack     string  `json:"pack_size"`
	GST      float64 `json:"gst_pct"`
	SubCat   string  `json:"sub_category"`
	Category string  `json:"category"`
}

// Rate is a vendor's current rate card for an item.
type Rate struct {
	VendorID int64   `json:"vendor_id"`
	SKUID    int64   `json:"sku_id"`
	Rate     float64 `json:"rate"`
	GST      float64 `json:"gst_pct"`
}

const officeItemsSQL = `SELECT k.id, k.code, concat_ws(' ', b.name, k.item_name), ps.name, k.gst_pct::float8, sc.name, c.name
	FROM sku k JOIN sub_category sc ON sc.id=k.sub_category_id JOIN category c ON c.id=sc.category_id JOIN pack_size ps ON ps.id=k.pack_size_id
	LEFT JOIN brand b ON b.id=k.brand_id
	WHERE k.active AND sc.active AND k.fin_status='approved' AND sc.fin_status='approved' AND c.fin_status='approved'
	AND (k.scope='all' OR $2::bigint=ANY(k.cluster_ids) OR $1::bigint=ANY(k.property_ids))`

// officeItems lists the items a corporate purchase may order; one item when id is given.
func officeItems(ctx context.Context, q database.Q, officeID, clusterID, id int64) ([]Item, error) {
	rows, err := q.Query(ctx, officeItemsSQL+` AND ($3=0 OR k.id=$3) ORDER BY c.name, sc.name, 3`, officeID, clusterID, id)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Item])
}

// OfficeItem finds an item a corporate purchase may order by its SKU code, or says why it cannot.
func OfficeItem(ctx context.Context, q database.Q, code string) (Item, string) {
	var officeID, clusterID int64
	if err := q.QueryRow(ctx, `SELECT id, cluster_id FROM property WHERE kind='corporate' AND active ORDER BY id LIMIT 1`).Scan(&officeID, &clusterID); err != nil {
		return Item{}, "There is no corporate office property"
	}
	var id int64
	if err := q.QueryRow(ctx, `SELECT id FROM sku WHERE upper(code)=upper(trim($1))`, code).Scan(&id); err != nil {
		return Item{}, fmt.Sprintf("Unknown SKU code %q. Corporate lines use items already in the item master", code)
	}
	found, err := officeItems(ctx, q, officeID, clusterID, id)
	if err != nil || len(found) == 0 {
		return Item{}, fmt.Sprintf("%s is not available at the corporate office (inactive, waiting for Finance, or its Available at leaves the office out)", strings.ToUpper(strings.TrimSpace(code)))
	}
	return found[0], ""
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
	officeID, _, _ := s.office(r.Context())
	var clusterID int64
	_ = s.DB.QueryRow(r.Context(), `SELECT cluster_id FROM property WHERE id=$1`, officeID).Scan(&clusterID)
	items, err := officeItems(r.Context(), s.DB, officeID, clusterID, 0)
	if err != nil {
		return err
	}
	// Every vendor's current rate for those items: the form fills the rate in from the chosen vendor's rate card.
	rows, err = s.DB.Query(r.Context(), `SELECT DISTINCT ON (vendor_id, sku_id) vendor_id, sku_id, rate::float8, gst_pct::float8 FROM rate_card
		WHERE NOT superseded AND current_date BETWEEN valid_from AND valid_to ORDER BY vendor_id, sku_id, valid_from DESC, id DESC`)
	if err != nil {
		return err
	}
	rates, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Rate])
	if err != nil {
		return err
	}
	return httpjson.OK(w, map[string]any{"office": office, "departments": deps, "expense_heads": heads, "vendors": vs, "l1_only_below": st.CorpL1OnlyBelow, "items": items, "rates": rates})
}

// lineIn is one corporate purchase line: an existing item, its expense head, quantity and (when the vendor has no rate
// card for it) the rate. Description, unit and GST come from the item; the rate from the vendor's rate card if it has one.
type lineIn struct {
	SKUID       int64   `json:"sku_id"`
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
		_ = s.DB.QueryRow(ctx, `SELECT active AND fin_status='approved' FROM expense_head WHERE id=$1`, id).Scan(&ok)
		return ok
	}
	amount := 0.0
	var invoice, quote *uploadedfiles.File
	switch in.Kind {
	case "purchase":
		if len(in.Lines) == 0 {
			errs = append(errs, "Add at least one item.")
		}
		for i := range in.Lines {
			l := &in.Lines[i]
			n := "Line " + strconv.Itoa(i+1) + ": "
			// Only items already in the item master: a corporate line never adds one.
			found, err := officeItems(ctx, s.DB, officeID, clusterID, l.SKUID)
			if err != nil {
				return err
			}
			if l.SKUID == 0 || len(found) == 0 {
				errs = append(errs, n+"pick an item from the list (an active, approved item available at the corporate office). A new item is added under Items first.")
				continue
			}
			it := found[0]
			l.Description, l.Unit, l.GST = it.Name, it.Pack, it.GST
			if rate, gst, ok := vendormapping.CurrentRate(ctx, s.DB, in.VendorID, l.SKUID); ok {
				l.Rate, l.GST = rate, gst // the vendor's rate card decides the rate
			}
			if !headOK(l.HeadID) {
				errs = append(errs, n+"choose an active expense head.")
			}
			if l.Qty <= 0 || l.Rate <= 0 {
				errs = append(errs, n+"quantity and rate must be above 0 ("+it.Name+" has no rate card with this vendor, so enter the rate).")
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
			if _, err := tx.Exec(ctx, `INSERT INTO document_line(document_id,sku_id,description,expense_head_id,qty,unit,rate,gst_pct,tds_section,tds_pct)
				SELECT $1,$2,$3,$4,$5,$6,$7,$8,k.tds_section,k.tds_pct FROM sku k WHERE k.id=$2`,
				id, l.SKUID, l.Description, l.HeadID, l.Qty, l.Unit, money.Round2(l.Rate), l.GST); err != nil {
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

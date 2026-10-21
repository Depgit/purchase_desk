// Package vendors onboards vendors of every type, checks GSTIN, PAN, IFSC and Udyam formats,
// blocks duplicates, and lets Finance verify bank details. A vendor is usable only when active and bank verified.
package vendors

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"purchasedesk/internal/access"
	"purchasedesk/internal/auditlog"
	"purchasedesk/internal/database"
	"purchasedesk/internal/encryption"
	"purchasedesk/internal/httpjson"
	"purchasedesk/internal/indiantax"
	"purchasedesk/internal/masterapproval"
	"purchasedesk/internal/money"
	"purchasedesk/internal/textclean"
	"purchasedesk/internal/uploadedfiles"
	"purchasedesk/internal/zohowake"
)

type Service struct {
	DB    *pgxpool.Pool
	Box   *encryption.Box
	Files *uploadedfiles.Service
}

func (s *Service) Routes(r chi.Router) {
	r.Get("/vendors", httpjson.Wrap(s.list))
	r.Get("/vendors/{id}", httpjson.Wrap(s.get))
	r.Post("/vendors", httpjson.Wrap(s.save))
	r.Put("/vendors/{id}", httpjson.Wrap(s.save))
	r.Post("/vendors/{id}/verify", httpjson.Wrap(s.verify))
	r.Put("/vendors/{id}/sending", httpjson.Wrap(s.setSending))
}

type Vendor struct {
	ID             int64    `json:"id"`
	Code           string   `json:"code"`
	Type           string   `json:"vendor_type"`
	Name           string   `json:"legal_name"`
	Address        string   `json:"address"`
	City           string   `json:"city"`
	PIN            string   `json:"pin"`
	Contact        string   `json:"contact_person"`
	Mobile         string   `json:"mobile"`
	Email          string   `json:"email"`
	GSTRegistered  bool     `json:"gst_registered"`
	GSTIN          string   `json:"gstin"`
	PAN            string   `json:"pan"`
	StateCode      string   `json:"state_code"`
	MSME           bool     `json:"msme"`
	Udyam          string   `json:"udyam"`
	BankHolder     string   `json:"bank_holder"`
	BankMasked     string   `json:"bank_account_masked"`
	IFSC           string   `json:"ifsc"`
	BankProofID    *int64   `json:"bank_proof_file_id"`
	MSMEFileID     *int64   `json:"msme_file_id"`
	BankVerified   bool     `json:"bank_verified"`
	BankVerifiedBy string   `json:"bank_verified_by"`
	Terms          string   `json:"payment_terms"`
	SheetMode      string   `json:"sheet_mode"`
	SheetTime      string   `json:"sheet_time"`
	SheetChannel   string   `json:"sheet_channel"`
	Active         bool     `json:"active"`
	InactiveReason string   `json:"inactive_reason"`
	SubCategoryIDs []int64  `json:"sub_category_ids"`
	BillTypeIDs    []int64  `json:"bill_type_ids"`
	Supplies       []string `json:"supplies"`
	FinChange      string   `json:"fin_change"`      // an edit waiting for Finance: what changed
	Opening        float64  `json:"opening_balance"` // before Purchase Desk: negative = advance already paid, positive = old bills owed
	OpeningAsOn    string   `json:"opening_as_on"`
}

const selectVendor = `SELECT v.id, v.code, v.vendor_type, v.legal_name, v.address, v.city, v.pin, v.contact_person, v.mobile, v.email,
	v.gst_registered, coalesce(v.gstin,''), coalesce(v.pan,''), v.state_code, v.msme, v.udyam, v.bank_holder, v.bank_account_last4, v.ifsc,
	v.bank_proof_file_id, v.msme_file_id, v.bank_verified, coalesce(bu.name,''), v.payment_terms, v.sheet_mode, v.sheet_time, v.sheet_channel, v.active, coalesce(v.inactive_reason,''),
	coalesce((SELECT array_agg(sub_category_id ORDER BY sub_category_id) FROM vendor_sub_category WHERE vendor_id=v.id),'{}'),
	coalesce((SELECT array_agg(bill_type_id ORDER BY bill_type_id) FROM vendor_bill_type WHERE vendor_id=v.id),'{}'),
	coalesce((SELECT array_agg(sc.name ORDER BY sc.name) FROM vendor_sub_category x JOIN sub_category sc ON sc.id=x.sub_category_id WHERE x.vendor_id=v.id),'{}') ||
	coalesce((SELECT array_agg(b.name ORDER BY b.name) FROM vendor_bill_type x JOIN bill_type b ON b.id=x.bill_type_id WHERE x.vendor_id=v.id),'{}'),
	v.fin_change, v.opening_balance::float8, coalesce(v.opening_as_on::text,'')
	FROM vendor v LEFT JOIN app_user bu ON bu.id=v.bank_verified_by`

func scan(row pgx.CollectableRow) (Vendor, error) {
	var v Vendor
	var last4 string
	err := row.Scan(&v.ID, &v.Code, &v.Type, &v.Name, &v.Address, &v.City, &v.PIN, &v.Contact, &v.Mobile, &v.Email,
		&v.GSTRegistered, &v.GSTIN, &v.PAN, &v.StateCode, &v.MSME, &v.Udyam, &v.BankHolder, &last4, &v.IFSC,
		&v.BankProofID, &v.MSMEFileID, &v.BankVerified, &v.BankVerifiedBy, &v.Terms, &v.SheetMode, &v.SheetTime, &v.SheetChannel, &v.Active, &v.InactiveReason,
		&v.SubCategoryIDs, &v.BillTypeIDs, &v.Supplies, &v.FinChange, &v.Opening, &v.OpeningAsOn)
	if last4 != "" {
		v.BankMasked = "••••••" + last4
	}
	return v, err
}

func (s *Service) list(w http.ResponseWriter, r *http.Request) error {
	rows, err := s.DB.Query(r.Context(), selectVendor+` ORDER BY v.code`)
	if err != nil {
		return err
	}
	all, err := pgx.CollectRows(rows, scan)
	if err != nil {
		return err
	}
	return httpjson.OK(w, all)
}

func (s *Service) get(w http.ResponseWriter, r *http.Request) error {
	id, err := httpjson.ID(r, "id")
	if err != nil {
		return err
	}
	rows, err := s.DB.Query(r.Context(), selectVendor+` WHERE v.id=$1`, id)
	if err != nil {
		return err
	}
	v, err := pgx.CollectOneRow(rows, scan)
	if err != nil {
		return err
	}
	return httpjson.OK(w, v)
}

// Input is a vendor from the onboarding form or an upload row.
type Input struct {
	Type           string  `json:"vendor_type"`
	Name           string  `json:"legal_name"`
	Address        string  `json:"address"`
	City           string  `json:"city"`
	PIN            string  `json:"pin"`
	Contact        string  `json:"contact_person"`
	Mobile         string  `json:"mobile"`
	Email          string  `json:"email"`
	GSTRegistered  bool    `json:"gst_registered"`
	GSTIN          string  `json:"gstin"`
	PAN            string  `json:"pan"`
	StateCode      string  `json:"state_code"`
	MSME           bool    `json:"msme"`
	Udyam          string  `json:"udyam"`
	BankHolder     string  `json:"bank_holder"`
	BankAccount    string  `json:"bank_account"`
	BankAccount2   string  `json:"bank_account_confirm"`
	IFSC           string  `json:"ifsc"`
	Terms          string  `json:"payment_terms"`
	SubCategoryIDs []int64 `json:"sub_category_ids"`
	BillTypeIDs    []int64 `json:"bill_type_ids"`
	// Opening balance from before Purchase Desk (nil keeps it): negative = advance already paid, positive = old bills
	// owed, as on OpeningAsOn (blank: today). A negative one is taken off the vendor's next invoices (syncOpening).
	Opening     *float64 `json:"opening_balance"`
	OpeningAsOn string   `json:"opening_as_on"`

	msmeFile   *uploadedfiles.File // MSME (Udyam) certificate attached to this save, if any
	msmeFileID *int64              // set once the certificate is saved
}

// readInput accepts JSON, or a multipart form with a "data" JSON field and optional "bank_proof" and "msme_certificate" files.
func readInput(r *http.Request) (Input, *uploadedfiles.File, error) {
	var in Input
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
		if err := r.ParseMultipartForm(uploadedfiles.MaxBytes + (1 << 20)); err != nil {
			return in, nil, httpjson.Bad("Could not read the form: %v", err)
		}
		if err := json.Unmarshal([]byte(r.FormValue("data")), &in); err != nil {
			return in, nil, httpjson.Bad("Could not read the form data.")
		}
		var f *uploadedfiles.File
		if _, fh, err := r.FormFile("bank_proof"); err == nil {
			if f, err = uploadedfiles.Read(fh); err != nil {
				return in, nil, err
			}
		}
		if _, fh, err := r.FormFile("msme_certificate"); err == nil {
			if in.msmeFile, err = uploadedfiles.Read(fh); err != nil {
				return in, nil, err
			}
		}
		return in, f, nil
	}
	return in, nil, httpjson.Decode(r, &in)
}

func (s *Service) stateFor(ctx context.Context, in *Input) {
	if in.GSTRegistered && len(in.GSTIN) >= 2 {
		in.StateCode = indiantax.StateFromGSTIN(in.GSTIN)
		return
	}
	if in.StateCode == "" && in.City != "" {
		_ = s.DB.QueryRow(ctx, `SELECT state_code FROM city WHERE norm_key(name)=norm_key($1)`, in.City).Scan(&in.StateCode)
	}
	in.StateCode = strings.ToUpper(strings.TrimSpace(in.StateCode))
}

// Validate tidies the input and returns every problem. needProof is false for opening-data uploads, where Finance attaches the proof when verifying.
func (s *Service) Validate(ctx context.Context, in *Input, id int64, proof *uploadedfiles.File, needProof bool) []string {
	errs := []string{}
	in.Name = textclean.Clean(in.Name)
	in.City, in.Contact, in.Address, in.BankHolder = textclean.Clean(in.City), textclean.Clean(in.Contact), textclean.Clean(in.Address), textclean.Clean(in.BankHolder)
	in.GSTIN, in.PAN, in.IFSC, in.Udyam = textclean.Upper(in.GSTIN), textclean.Upper(in.PAN), textclean.Upper(in.IFSC), textclean.Upper(in.Udyam)
	in.Mobile, in.PIN, in.Email = strings.TrimSpace(in.Mobile), strings.TrimSpace(in.PIN), strings.TrimSpace(in.Email)
	in.BankAccount, in.BankAccount2 = strings.TrimSpace(in.BankAccount), strings.TrimSpace(in.BankAccount2)
	errs = append(errs, checkOpening(in)...)
	if !textclean.Contains([]string{"goods", "utility", "amc", "other"}, in.Type) {
		errs = append(errs, "Choose the vendor type.")
	}
	if len(in.Name) < 3 {
		errs = append(errs, "Enter the legal entity name.")
	}
	if id == 0 && (in.Address == "" || in.City == "") {
		errs = append(errs, "Enter address and city.")
	}
	if id == 0 && !indiantax.PIN.MatchString(in.PIN) || id != 0 && in.PIN != "" && !indiantax.PIN.MatchString(in.PIN) {
		errs = append(errs, "PIN code must be 6 digits.")
	}
	if id == 0 && in.Contact == "" {
		errs = append(errs, "Enter a contact person.")
	}
	if (id == 0 || in.Mobile != "") && !indiantax.Mobile.MatchString(in.Mobile) {
		errs = append(errs, "Mobile number must be 10 digits.")
	}
	if (id == 0 || in.Email != "") && !indiantax.Email.MatchString(in.Email) {
		errs = append(errs, "Enter a valid email.")
	}
	if in.GSTRegistered {
		if !indiantax.GSTIN.MatchString(in.GSTIN) {
			errs = append(errs, "GSTIN format is not valid.")
		} else if in.PAN == "" {
			in.PAN = in.GSTIN[2:12] // characters 3 to 12 of a GSTIN are the PAN
		} else if in.GSTIN[2:12] != in.PAN {
			errs = append(errs, "PAN does not match the PAN inside the GSTIN.")
		}
	} else {
		in.GSTIN = ""
	}
	if in.PAN == "" {
		errs = append(errs, "Enter the PAN.")
	} else if !indiantax.PAN.MatchString(in.PAN) {
		errs = append(errs, "PAN format is not valid.")
	}
	if in.MSME && !indiantax.Udyam.MatchString(in.Udyam) {
		errs = append(errs, "Udyam number format is UDYAM-XX-00-0000000.")
	}
	// An MSME vendor needs its Udyam certificate on file, as proof for the 45-day payment rule.
	if in.MSME && needProof && in.msmeFile == nil {
		var onFile bool
		if id != 0 {
			_ = s.DB.QueryRow(ctx, `SELECT msme_file_id IS NOT NULL FROM vendor WHERE id=$1`, id).Scan(&onFile)
		}
		if !onFile {
			errs = append(errs, "Attach the MSME (Udyam) certificate.")
		}
	}
	if !in.MSME {
		in.Udyam = ""
	}
	if id == 0 || in.BankAccount != "" || in.IFSC != "" {
		if in.BankHolder == "" && id == 0 {
			errs = append(errs, "Enter the account holder name.")
		}
		if id != 0 && in.BankAccount == "" {
			errs = append(errs, "To change the IFSC, enter the account number again (twice), so Finance verifies the new bank details.")
		} else if !indiantax.Acct.MatchString(in.BankAccount) {
			errs = append(errs, "Account number must be 9 to 18 digits.")
		} else if in.BankAccount != in.BankAccount2 {
			errs = append(errs, "Account numbers do not match.")
		}
		if !indiantax.IFSC.MatchString(in.IFSC) {
			errs = append(errs, "IFSC format is not valid.")
		}
		if proof == nil && needProof {
			errs = append(errs, "Attach the cancelled cheque or bank letter.")
		}
	}
	if !textclean.Contains(money.PaymentTerms, in.Terms) {
		errs = append(errs, "Choose the payment terms.")
	}
	if in.Type == "goods" && len(in.SubCategoryIDs) == 0 {
		errs = append(errs, "Tick at least one sub-category.")
	}
	if in.Type != "goods" && len(in.BillTypeIDs) == 0 && in.Type != "other" {
		errs = append(errs, "Tick at least one bill type.")
	}
	var dup string
	_ = s.DB.QueryRow(ctx, `SELECT CASE WHEN norm_key(legal_name)=norm_key($1) THEN 'A vendor with this name already exists.'
		WHEN $2<>'' AND upper(coalesce(gstin,''))=$2 THEN 'A vendor with this GSTIN already exists.'
		ELSE 'A vendor with this PAN already exists.' END
		FROM vendor WHERE id<>$4 AND (norm_key(legal_name)=norm_key($1) OR ($2<>'' AND upper(coalesce(gstin,''))=$2) OR ($3<>'' AND upper(coalesce(pan,''))=$3)) LIMIT 1`,
		in.Name, in.GSTIN, in.PAN, id).Scan(&dup)
	if dup != "" {
		errs = append(errs, dup)
	}
	s.stateFor(ctx, in)
	return errs
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (s *Service) save(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	if err := access.NeedRight(u, access.Catalog, "create or edit vendors"); err != nil {
		return err
	}
	in, proof, err := readInput(r)
	if err != nil {
		return err
	}
	id := int64(0)
	if chi.URLParam(r, "id") != "" {
		if id, err = httpjson.ID(r, "id"); err != nil {
			return err
		}
	}
	if errs := s.Validate(r.Context(), &in, id, proof, true); len(errs) > 0 {
		return httpjson.Invalid(errs)
	}
	var code string
	resent := false
	err = pgx.BeginFunc(r.Context(), s.DB, func(tx pgx.Tx) error {
		var proofID *int64
		if proof != nil {
			fid, err := s.Files.Save(r.Context(), tx, u, proof, "bank-proofs", true)
			if err != nil {
				return err
			}
			proofID = &fid
		}
		if in.msmeFile != nil {
			fid, err := s.Files.Save(r.Context(), tx, u, in.msmeFile, "msme-certificates", false)
			if err != nil {
				return err
			}
			in.msmeFileID = &fid
		}
		acctEnc, last4 := "", ""
		if in.BankAccount != "" {
			var err error
			if acctEnc, err = s.Box.Seal(in.BankAccount); err != nil {
				return err
			}
			last4 = in.BankAccount[len(in.BankAccount)-4:]
		}
		if id == 0 {
			var err error
			if id, code, err = s.Insert(r.Context(), tx, u, &in, proofID, acctEnc, last4); err != nil {
				return err
			}
		} else {
			var err error
			if code, resent, err = s.Update(r.Context(), tx, u, id, &in, proofID, acctEnc, last4); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	return httpjson.OK(w, map[string]any{"id": id, "code": code, "sent_for_verification": resent})
}

// Update saves a validated edit of vendor id and replaces what it supplies. Blank address, contact, bank holder,
// account and IFSC keep what is on file. A change to the bank account, GST details or PAN sends it back to Finance.
func (s *Service) Update(ctx context.Context, tx pgx.Tx, u *access.User, id int64, in *Input, proofID *int64, acctEnc, last4 string) (string, bool, error) {
	var code, oldGSTIN, oldPAN string
	var oldGST bool
	if err := tx.QueryRow(ctx, `SELECT code, gst_registered, coalesce(gstin,''), coalesce(pan,'') FROM vendor WHERE id=$1 FOR UPDATE`, id).Scan(&code, &oldGST, &oldGSTIN, &oldPAN); err != nil {
		return "", false, err
	}
	resent := in.BankAccount != "" || oldGST != in.GSTRegistered || oldGSTIN != in.GSTIN || oldPAN != in.PAN
	before, err := masterapproval.Snapshot(ctx, tx, "vendor", id)
	if err != nil {
		return "", false, err
	}
	before["supplies"] = supplies(ctx, tx, id)
	if _, err := tx.Exec(ctx, `UPDATE vendor SET vendor_type=$2, legal_name=$3, address=coalesce(nullif($4,''),address), city=coalesce(nullif($5,''),city), pin=coalesce(nullif($6,''),pin),
		contact_person=coalesce(nullif($7,''),contact_person), mobile=$8, email=$9, gst_registered=$10, gstin=$11, pan=$12, state_code=$13, msme=$14, udyam=$15,
		bank_holder=coalesce(nullif($16,''),bank_holder), payment_terms=$17,
		bank_account_enc=CASE WHEN $18<>'' THEN $18 ELSE bank_account_enc END, bank_account_last4=CASE WHEN $19<>'' THEN $19 ELSE bank_account_last4 END,
		ifsc=CASE WHEN $20<>'' THEN $20 ELSE ifsc END, bank_proof_file_id=coalesce($21,bank_proof_file_id), msme_file_id=coalesce($23,msme_file_id),
		bank_verified=CASE WHEN $22 THEN FALSE ELSE bank_verified END WHERE id=$1`,
		id, in.Type, in.Name, in.Address, in.City, in.PIN, in.Contact, in.Mobile, in.Email, in.GSTRegistered, nullIfEmpty(in.GSTIN), nullIfEmpty(in.PAN), in.StateCode, in.MSME, in.Udyam,
		in.BankHolder, in.Terms, acctEnc, last4, in.IFSC, proofID, resent, in.msmeFileID); err != nil {
		return "", false, err
	}
	if err := s.syncOpening(ctx, tx, u, id, in); err != nil {
		return "", false, err
	}
	act := "Vendor edited"
	if resent {
		act = "Vendor edited, sent for re-verification"
	}
	if err := auditlog.Write(ctx, tx, u, "Vendors", act, code, in.Name); err != nil {
		return "", false, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM vendor_sub_category WHERE vendor_id=$1`, id); err != nil {
		return "", false, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM vendor_bill_type WHERE vendor_id=$1`, id); err != nil {
		return "", false, err
	}
	if in.Type == "goods" {
		if _, err := tx.Exec(ctx, `INSERT INTO vendor_sub_category(vendor_id,sub_category_id) SELECT $1, unnest($2::bigint[]) ON CONFLICT DO NOTHING`, id, in.SubCategoryIDs); err != nil {
			return "", false, err
		}
	}
	// Any change to a verified vendor sends it back to Finance (it cannot be used until verified again), and once
	// verified an edit of a vendor already in Zoho goes to Zoho.
	after, err := masterapproval.Snapshot(ctx, tx, "vendor", id)
	if err != nil {
		return "", false, err
	}
	after["supplies"] = supplies(ctx, tx, id)
	reVerify := false
	if changed := masterapproval.Changed(before, after); len(changed) > 0 {
		what := "Edited by " + u.Name + ": " + strings.Join(changed, ", ")
		wasVerified, _ := before["bank_verified"].(bool)
		if _, err := tx.Exec(ctx, `UPDATE vendor SET bank_verified=FALSE,
			fin_change=CASE WHEN $3 THEN $2 ELSE concat_ws('; ', nullif(fin_change,''), $2::text) END,
			zoho_update_due=EXISTS (SELECT 1 FROM zoho_map_vendor m WHERE m.vendor_id=$1) WHERE id=$1`, id, what, wasVerified); err != nil {
			return "", false, err
		}
		reVerify = wasVerified
		if err := auditlog.Write(ctx, tx, u, "Vendors", "Vendor edited, waits for Finance", code, strings.Join(changed, ", ")); err != nil {
			return "", false, err
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO vendor_bill_type(vendor_id,bill_type_id) SELECT $1, unnest($2::bigint[]) ON CONFLICT DO NOTHING`, id, in.BillTypeIDs); err != nil {
		return "", false, err
	}
	return code, resent || reVerify, nil
}

// supplies is what a vendor supplies: its sub-categories (goods) or bill types, to compare an edit with.
func supplies(ctx context.Context, q database.Q, id int64) []int64 {
	var out []int64
	_ = q.QueryRow(ctx, `SELECT coalesce(array_agg(x ORDER BY x),'{}') FROM (SELECT sub_category_id x FROM vendor_sub_category WHERE vendor_id=$1
		UNION ALL SELECT -bill_type_id FROM vendor_bill_type WHERE vendor_id=$1) s`, id).Scan(&out)
	return out
}

// Seal encrypts a bank account number for storage and gives its last four digits ("" for none).
func (s *Service) Seal(acct string) (string, string, error) {
	if acct == "" {
		return "", "", nil
	}
	enc, err := s.Box.Seal(acct)
	if err != nil {
		return "", "", err
	}
	return enc, acct[len(acct)-4:], nil
}

func (s *Service) verify(w http.ResponseWriter, r *http.Request) error {
	zohowake.Now() // something may now go to Zoho
	u := access.From(r)
	if !u.IsAdmin && !u.HasStep("finance") {
		return httpjson.Forbidden("Only Finance or the System Admin can verify bank details.")
	}
	id, err := httpjson.ID(r, "id")
	if err != nil {
		return err
	}
	var code, name, last4 string
	if err := s.DB.QueryRow(r.Context(), `UPDATE vendor SET bank_verified=TRUE, bank_verified_by=$2, bank_verified_at=now(), fin_change='' WHERE id=$1 RETURNING code, legal_name, bank_account_last4`, id, u.ID).Scan(&code, &name, &last4); err != nil {
		return err
	}
	if last4 == "" {
		return httpjson.Bad("This vendor has no bank account on record.")
	}
	_ = auditlog.Write(r.Context(), s.DB, u, "Vendors", "Bank details verified", code, name)
	return httpjson.OK(w, map[string]bool{"ok": true})
}

// setSending sets how the vendor's order sheet goes out: manually, or automatically at a time each day.
func (s *Service) setSending(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	if !u.IsAdmin && !u.HasStep("cc") {
		return httpjson.Forbidden("Only the Cost Controller or the System Admin can change order sheet sending.")
	}
	id, err := httpjson.ID(r, "id")
	if err != nil {
		return err
	}
	var in struct{ Mode, Time, Channel string }
	if err := httpjson.Decode(r, &in); err != nil {
		return err
	}
	if in.Mode != "manual" && in.Mode != "auto" {
		return httpjson.Bad("Sending is manual or automatic.")
	}
	if in.Mode == "auto" && (len(in.Time) != 5 || in.Time[2] != ':') {
		return httpjson.Bad("Choose the time of day.")
	}
	if !textclean.Contains([]string{"email", "whatsapp", "both"}, in.Channel) {
		return httpjson.Bad("Choose email, WhatsApp or both.")
	}
	var code string
	if err := s.DB.QueryRow(r.Context(), `UPDATE vendor SET sheet_mode=$2, sheet_time=$3, sheet_channel=$4 WHERE id=$1 RETURNING code`, id, in.Mode, in.Time, in.Channel).Scan(&code); err != nil {
		return err
	}
	_ = auditlog.Write(r.Context(), s.DB, u, "Vendor Orders", "Sending set to "+in.Mode, code, in.Time+" · "+in.Channel)
	return httpjson.OK(w, map[string]bool{"ok": true})
}

// Usable reports whether a vendor can be used on new requests and bills.
func Usable(ctx context.Context, q database.Q, id int64) bool {
	var ok bool
	_ = q.QueryRow(ctx, `SELECT active AND bank_verified FROM vendor WHERE id=$1`, id).Scan(&ok)
	return ok
}

// Insert saves a validated new vendor as bank verification pending, with its sub-categories or bill types.
func (s *Service) Insert(ctx context.Context, tx pgx.Tx, u *access.User, in *Input, proofID *int64, acctEnc, last4 string) (int64, string, error) {
	if acctEnc == "" && in.BankAccount != "" {
		var err error
		if acctEnc, err = s.Box.Seal(in.BankAccount); err != nil {
			return 0, "", err
		}
		last4 = in.BankAccount[len(in.BankAccount)-4:]
	}
	var n int
	if _, err := tx.Exec(ctx, `LOCK TABLE vendor IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return 0, "", err
	}
	_ = tx.QueryRow(ctx, `SELECT coalesce(max(substring(code from 2)::int),0)+1 FROM vendor WHERE code ~ '^V[0-9]+$'`).Scan(&n)
	code := fmt.Sprintf("V%03d", n)
	var id int64
	if err := tx.QueryRow(ctx, `INSERT INTO vendor(code,vendor_type,legal_name,address,city,pin,contact_person,mobile,email,gst_registered,gstin,pan,state_code,msme,udyam,
		bank_holder,bank_account_enc,bank_account_last4,ifsc,bank_proof_file_id,payment_terms,created_by,msme_file_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23) RETURNING id`,
		code, in.Type, in.Name, in.Address, in.City, in.PIN, in.Contact, in.Mobile, in.Email, in.GSTRegistered, nullIfEmpty(in.GSTIN), nullIfEmpty(in.PAN), in.StateCode,
		in.MSME, in.Udyam, in.BankHolder, acctEnc, last4, in.IFSC, proofID, in.Terms, u.ID, in.msmeFileID).Scan(&id); err != nil {
		return 0, "", err
	}
	if err := s.syncOpening(ctx, tx, u, id, in); err != nil {
		return 0, "", err
	}
	if in.Type == "goods" {
		if _, err := tx.Exec(ctx, `INSERT INTO vendor_sub_category(vendor_id,sub_category_id) SELECT $1, unnest($2::bigint[]) ON CONFLICT DO NOTHING`, id, in.SubCategoryIDs); err != nil {
			return 0, "", err
		}
	} else if _, err := tx.Exec(ctx, `INSERT INTO vendor_bill_type(vendor_id,bill_type_id) SELECT $1, unnest($2::bigint[]) ON CONFLICT DO NOTHING`, id, in.BillTypeIDs); err != nil {
		return 0, "", err
	}
	return id, code, auditlog.Write(ctx, tx, u, "Vendors", "Vendor onboarded", code, in.Name+" · bank verification pending")
}

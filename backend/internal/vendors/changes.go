package vendors

// Changes a vendor asks for on the vendor portal ("My details"): the vendor proposes new details (address, contact,
// GST, PAN, MSME, bank with its proof); nothing changes until Finance approves them under Masters to approve. Approval
// saves them like an edit by Accounts Payable, counts as Finance's verification, and sends the change to Zoho.

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"purchasedesk/internal/access"
	"purchasedesk/internal/auditlog"
	"purchasedesk/internal/database"
	"purchasedesk/internal/httpjson"
	"purchasedesk/internal/uploadedfiles"
	"purchasedesk/internal/zohowake"
)

// Proposal is what a vendor may change about itself. A blank bank account keeps the one on file.
type Proposal struct {
	Name          string `json:"legal_name"`
	Address       string `json:"address"`
	City          string `json:"city"`
	PIN           string `json:"pin"`
	Contact       string `json:"contact_person"`
	Mobile        string `json:"mobile"`
	Email         string `json:"email"`
	GSTRegistered bool   `json:"gst_registered"`
	GSTIN         string `json:"gstin"`
	PAN           string `json:"pan"`
	StateCode     string `json:"state_code"`
	MSME          bool   `json:"msme"`
	Udyam         string `json:"udyam"`
	BankHolder    string `json:"bank_holder"`
	BankAccount   string `json:"bank_account"`
	BankAccount2  string `json:"bank_account_confirm"`
	IFSC          string `json:"ifsc"`
}

// stored is a proposal as kept until Finance decides: the new bank account only encrypted.
type stored struct {
	Proposal
	AcctEnc string `json:"bank_account_enc"`
	Last4   string `json:"bank_account_last4"`
}

// Change is a vendor's request as the portal shows it.
type Change struct {
	ID        int64      `json:"id"`
	Changes   string     `json:"changes"`
	Status    string     `json:"status"`
	AskedAt   time.Time  `json:"asked_at"`
	DecidedAt *time.Time `json:"decided_at"`
	Note      string     `json:"note"`
}

// Load reads one vendor.
func Load(ctx context.Context, q database.Q, id int64) (Vendor, error) {
	rows, err := q.Query(ctx, selectVendor+` WHERE v.id=$1`, id)
	if err != nil {
		return Vendor{}, err
	}
	return pgx.CollectOneRow(rows, scan)
}

// input is the vendor's details as an edit form would send them (a blank bank account keeps the one on file).
func (v Vendor) input() Input {
	return Input{Type: v.Type, Name: v.Name, Address: v.Address, City: v.City, PIN: v.PIN, Contact: v.Contact, Mobile: v.Mobile, Email: v.Email,
		GSTRegistered: v.GSTRegistered, GSTIN: v.GSTIN, PAN: v.PAN, StateCode: v.StateCode, MSME: v.MSME, Udyam: v.Udyam, BankHolder: v.BankHolder,
		Terms: v.Terms, SubCategoryIDs: v.SubCategoryIDs, BillTypeIDs: v.BillTypeIDs}
}

// apply puts a proposal onto the vendor's details. An unchanged IFSC is left blank, so the bank stays as it is.
func (p Proposal) apply(v Vendor) Input {
	in := v.input()
	in.Name, in.Address, in.City, in.PIN, in.Contact, in.Mobile, in.Email = p.Name, p.Address, p.City, p.PIN, p.Contact, p.Mobile, p.Email
	in.GSTRegistered, in.GSTIN, in.PAN, in.StateCode, in.MSME, in.Udyam = p.GSTRegistered, p.GSTIN, p.PAN, p.StateCode, p.MSME, p.Udyam
	in.BankHolder, in.BankAccount, in.BankAccount2 = p.BankHolder, strings.TrimSpace(p.BankAccount), strings.TrimSpace(p.BankAccount2)
	if !strings.EqualFold(strings.TrimSpace(p.IFSC), v.IFSC) || in.BankAccount != "" {
		in.IFSC = p.IFSC
	}
	return in
}

// differences lists, in words, what a proposal changes.
func differences(v Vendor, in Input, msme bool) []string {
	out := []string{}
	add := func(changed bool, label string) {
		if changed {
			out = append(out, label)
		}
	}
	add(in.Name != v.Name, "legal name")
	add(in.Address != v.Address, "address")
	add(in.City != v.City, "city")
	add(in.PIN != v.PIN, "PIN code")
	add(in.Contact != v.Contact, "contact person")
	add(in.Mobile != v.Mobile, "mobile")
	add(!strings.EqualFold(in.Email, v.Email), "email")
	add(in.GSTRegistered != v.GSTRegistered, "GST status")
	add(in.GSTIN != v.GSTIN, "GSTIN")
	add(in.PAN != v.PAN, "PAN")
	add(in.StateCode != v.StateCode, "state code")
	add(in.MSME != v.MSME || in.Udyam != v.Udyam, "MSME / Udyam")
	add(msme, "MSME certificate")
	add(in.BankHolder != v.BankHolder, "account holder")
	add(in.BankAccount != "", "bank account")
	add(in.IFSC != "" && in.IFSC != v.IFSC, "IFSC")
	return out
}

// Propose checks a vendor's proposed details the same way as a staff edit and saves them for Finance. A newer proposal
// replaces one still waiting. by is the portal email that asked.
func (s *Service) Propose(ctx context.Context, vendorID int64, p Proposal, proof, msme *uploadedfiles.File, by string) (string, error) {
	v, err := Load(ctx, s.DB, vendorID)
	if err != nil {
		return "", httpjson.NotFound("Vendor")
	}
	in := p.apply(v)
	in.msmeFile = msme
	if errs := s.Validate(ctx, &in, vendorID, proof, true); len(errs) > 0 {
		return "", httpjson.Invalid(errs)
	}
	changed := differences(v, in, msme != nil)
	if len(changed) == 0 {
		return "", httpjson.Bad("Nothing is different from your details on file.")
	}
	keep := stored{Proposal: Proposal{Name: in.Name, Address: in.Address, City: in.City, PIN: in.PIN, Contact: in.Contact, Mobile: in.Mobile, Email: in.Email,
		GSTRegistered: in.GSTRegistered, GSTIN: in.GSTIN, PAN: in.PAN, StateCode: in.StateCode, MSME: in.MSME, Udyam: in.Udyam, BankHolder: in.BankHolder, IFSC: in.IFSC}}
	if keep.AcctEnc, keep.Last4, err = s.Seal(in.BankAccount); err != nil {
		return "", err
	}
	data, err := json.Marshal(keep)
	if err != nil {
		return "", err
	}
	err = pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		var proofID, msmeID *int64
		if proof != nil {
			fid, err := s.Files.Save(ctx, tx, nil, proof, "bank-proofs", true)
			if err != nil {
				return err
			}
			proofID = &fid
		}
		if msme != nil {
			fid, err := s.Files.Save(ctx, tx, nil, msme, "msme-certificates", false)
			if err != nil {
				return err
			}
			msmeID = &fid
		}
		if _, err := tx.Exec(ctx, `UPDATE vendor_change SET status='withdrawn', decided_at=now(), decision_note='Replaced by a newer request' WHERE vendor_id=$1 AND status='pending'`, vendorID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO vendor_change(vendor_id,data,changes,bank_proof_file_id,msme_file_id,asked_by) VALUES ($1,$2,$3,$4,$5,$6)`,
			vendorID, data, strings.Join(changed, ", "), proofID, msmeID, by); err != nil {
			return err
		}
		return auditlog.ByVendorActivity(ctx, tx, v.Name, "Vendors", "Vendor asked to change its details", v.Code, strings.Join(changed, ", "))
	})
	if err != nil {
		return "", err
	}
	return "Sent to Saltstayz Finance for approval: " + strings.Join(changed, ", ") + ". Your details change once they approve.", nil
}

// Changes lists a vendor's latest requests (the waiting one first), for the portal.
func Changes(ctx context.Context, q database.Q, vendorID int64) ([]Change, error) {
	rows, err := q.Query(ctx, `SELECT id, changes, status, asked_at, decided_at, decision_note FROM vendor_change WHERE vendor_id=$1 AND status<>'withdrawn'
		ORDER BY (status='pending') DESC, asked_at DESC LIMIT 5`, vendorID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Change])
}

// DecideChange approves or rejects a vendor's request, for Finance under Masters to approve. Approval saves the details
// (as an edit), marks the vendor verified by Finance and lets the change go to Zoho.
func (s *Service) DecideChange(ctx context.Context, tx pgx.Tx, u *access.User, id int64, approve bool, reason string) (string, error) {
	var vendorID int64
	var data []byte
	var status string
	var proofID, msmeID *int64
	if err := tx.QueryRow(ctx, `SELECT vendor_id, data, status, bank_proof_file_id, msme_file_id FROM vendor_change WHERE id=$1 FOR UPDATE`, id).
		Scan(&vendorID, &data, &status, &proofID, &msmeID); err != nil {
		return "", httpjson.NotFound("Vendor change")
	}
	if status != "pending" {
		return "", httpjson.Conflict("This vendor change is already %s.", status)
	}
	v, err := Load(ctx, tx, vendorID)
	if err != nil {
		return "", err
	}
	if !approve {
		if len(strings.TrimSpace(reason)) < 3 {
			return "", httpjson.Bad("Write why the change for %s is rejected; the vendor sees it.", v.Name)
		}
		if _, err := tx.Exec(ctx, `UPDATE vendor_change SET status='rejected', decided_by=$2, decided_at=now(), decision_note=$3 WHERE id=$1`, id, u.ID, strings.TrimSpace(reason)); err != nil {
			return "", err
		}
		return "Change for " + v.Name + " rejected", auditlog.Write(ctx, tx, u, "Vendors", "Vendor's change rejected by Finance", v.Code, reason)
	}
	var keep stored
	if err := json.Unmarshal(data, &keep); err != nil {
		return "", err
	}
	in := keep.Proposal.apply(v)
	in.msmeFileID = msmeID
	if _, _, err := s.Update(ctx, tx, u, vendorID, &in, proofID, keep.AcctEnc, keep.Last4); err != nil {
		return "", err
	}
	// Finance approved these details, so the vendor stays verified.
	if _, err := tx.Exec(ctx, `UPDATE vendor SET bank_verified=TRUE, bank_verified_by=$2, bank_verified_at=now(), fin_change='' WHERE id=$1`, vendorID, u.ID); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `UPDATE vendor_change SET status='approved', decided_by=$2, decided_at=now() WHERE id=$1`, id, u.ID); err != nil {
		return "", err
	}
	zohowake.Now()
	return "Change for " + v.Name + " approved", auditlog.Write(ctx, tx, u, "Vendors", "Vendor's change approved by Finance", v.Code, "")
}

package vendorportal

// "My details": the vendor sees its details on file and asks for changes (address, contact, GST, PAN, MSME, bank with
// its proof). Saltstayz Finance approves them under Masters to approve (vendors.Propose, vendors.DecideChange); until
// then nothing changes.

import (
	"encoding/json"
	"net/http"

	"purchasedesk/internal/httpjson"
	"purchasedesk/internal/uploadedfiles"
	"purchasedesk/internal/vendors"
)

// details are what the vendor sees about itself; the bank account only as its last digits.
func (s *Service) details(w http.ResponseWriter, r *http.Request) error {
	v, err := vendors.Load(r.Context(), s.DB, from(r).ID)
	if err != nil {
		return err
	}
	changes, err := vendors.Changes(r.Context(), s.DB, v.ID)
	if err != nil {
		return err
	}
	return httpjson.OK(w, map[string]any{"vendor": map[string]any{
		"code": v.Code, "legal_name": v.Name, "address": v.Address, "city": v.City, "pin": v.PIN, "contact_person": v.Contact, "mobile": v.Mobile,
		"email": v.Email, "gst_registered": v.GSTRegistered, "gstin": v.GSTIN, "pan": v.PAN, "state_code": v.StateCode, "msme": v.MSME, "udyam": v.Udyam,
		"bank_holder": v.BankHolder, "bank_account_masked": v.BankMasked, "ifsc": v.IFSC, "payment_terms": v.Terms, "supplies": v.Supplies,
	}, "changes": changes})
}

// proposeDetails takes the vendor's new details: a multipart form with "data" and optional "bank_proof" and
// "msme_certificate" files.
func (s *Service) proposeDetails(w http.ResponseWriter, r *http.Request) error {
	if err := r.ParseMultipartForm(uploadedfiles.MaxBytes + (1 << 20)); err != nil {
		return httpjson.Bad("Could not read the form: %v", err)
	}
	var p vendors.Proposal
	if err := json.Unmarshal([]byte(r.FormValue("data")), &p); err != nil {
		return httpjson.Bad("Could not read the form data.")
	}
	var proof, msme *uploadedfiles.File
	var err error
	if _, fh, e := r.FormFile("bank_proof"); e == nil {
		if proof, err = uploadedfiles.Read(fh); err != nil {
			return err
		}
	}
	if _, fh, e := r.FormFile("msme_certificate"); e == nil {
		if msme, err = uploadedfiles.Read(fh); err != nil {
			return err
		}
	}
	v := from(r)
	msg, err := s.Vendors.Propose(r.Context(), v.ID, p, proof, msme, v.Email)
	if err != nil {
		return err
	}
	return httpjson.OK(w, map[string]string{"message": msg})
}

package zohopush

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"purchasedesk/internal/access"
	"purchasedesk/internal/auditlog"
	"purchasedesk/internal/clock"
	"purchasedesk/internal/httpjson"
	"purchasedesk/internal/money"
	"purchasedesk/internal/zohoapi"
)

// SendAll is the zoho-send job: everything that goes to Zoho by itself, in order, so the masters a PO or bill needs
// are there first. It runs every minute and at once after a change (zohowake), and waits while less than a fifth of
// Zoho's daily allowance is left, keeping the rest for the buttons.
func (s *Service) SendAll(ctx context.Context) error {
	if !s.PushEnabled || !s.Sync.Connected(ctx) || zohoapi.LowOnCalls() {
		return nil
	}
	errs := []error{}
	for _, step := range []func(context.Context) error{s.Sync.SyncMasters, s.SyncPOs, s.AutoPushBills, s.SyncAdvances} {
		if err := step(ctx); err != nil {
			if zohoapi.DailyLimit(err) || errors.Is(err, zohoapi.ErrTokenRevoked) {
				return err
			}
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// SyncAdvances sends each paid vendor advance to Zoho as a vendor payment from the account set on the Connection tab,
// left unapplied (an advance), and later applies it to the bill it was adjusted on once that bill is in Zoho.
// Applying a vendor payment to a bill this way is not tested against a live Zoho organisation yet.
func (s *Service) SyncAdvances(ctx context.Context) error {
	if !s.PushEnabled || !s.Sync.Connected(ctx) {
		return nil
	}
	c, conn, err := s.Sync.Client(ctx)
	if err != nil {
		return err
	}
	type adv struct {
		ID                               int64
		Number, PO, Ref, Contact, Vendor string
		Paid                             float64
		PaidOn                           *time.Time
	}
	rows, err := s.DB.Query(ctx, `SELECT a.id, a.number, coalesce(d.po_number,''), a.payment_ref, coalesce(m.contact_id,''), v.legal_name, (a.amount-a.tds_amount)::float8, a.paid_on
		FROM advance a JOIN document d ON d.id=a.document_id JOIN vendor v ON v.id=a.vendor_id LEFT JOIN zoho_map_vendor m ON m.vendor_id=a.vendor_id
		WHERE a.status='PAID' AND a.zoho_payment_id='' AND (a.zoho_error='' OR a.zoho_error LIKE 'Held:%' OR a.zoho_at < now() - interval '30 minutes')
		ORDER BY a.id LIMIT 20`)
	if err != nil {
		return err
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[adv])
	if err != nil {
		return err
	}
	note := func(id int64, msg string) error {
		_, err := s.DB.Exec(ctx, `UPDATE advance SET zoho_error=$2, zoho_at=now() WHERE id=$1`, id, msg)
		return err
	}
	for _, a := range list {
		switch {
		case a.Contact == "":
			if err := note(a.ID, "Held: "+a.Vendor+" is not linked to a Zoho vendor yet."); err != nil {
				return err
			}
			continue
		case conn.AdvanceAccountID == "":
			if err := note(a.ID, "Held: choose the account advances are paid from (Settings, Zoho Books, Connection)."); err != nil {
				return err
			}
			continue
		}
		date := time.Now()
		if a.PaidOn != nil {
			date = *a.PaidOn
		}
		// Finance may have recorded this payment in Zoho as well: link it rather than paying twice in Zoho.
		if found, err := c.VendorPayments(ctx, a.Contact, date.AddDate(0, 0, -15)); err == nil {
			if p, ok := carrying(found, a.Number); ok {
				if _, err := s.DB.Exec(ctx, `UPDATE advance SET zoho_payment_id=$2, zoho_error='', zoho_at=now() WHERE id=$1`, a.ID, p.ID); err != nil {
					return err
				}
				continue
			}
		} else if stop(s.Sync, ctx, err) {
			return err
		}
		body := map[string]any{"vendor_id": a.Contact, "amount": a.Paid, "date": date.Format("2006-01-02"), "payment_mode": "Bank Transfer",
			"reference_number": a.Ref, "paid_through_account_id": conn.AdvanceAccountID,
			"description": strings.TrimSpace(fmt.Sprintf("Advance %s against %s (Purchase Desk)", a.Number, a.PO))}
		id, err := c.CreateVendorPayment(ctx, body)
		if err != nil {
			if stop(s.Sync, ctx, err) {
				return err
			}
			if err := note(a.ID, "Zoho refused it: "+err.Error()); err != nil {
				return err
			}
			continue
		}
		if _, err := s.DB.Exec(ctx, `UPDATE advance SET zoho_payment_id=$2, zoho_error='', zoho_at=now() WHERE id=$1`, a.ID, id); err != nil {
			return err
		}
	}
	return s.applyAdvances(ctx, c, conn.AdvanceAccountID)
}

// applyAdvances applies advances already in Zoho to the bills they were adjusted on, once those bills are open in
// Zoho. Zoho replaces a payment's bill list on update, so the full list of the advance's bills is sent each time.
func (s *Service) applyAdvances(ctx context.Context, c *zohoapi.Client, account string) error {
	rows, err := s.DB.Query(ctx, `SELECT DISTINCT a.id FROM advance_use u JOIN advance a ON a.id=u.advance_id JOIN invoice i ON i.id=u.invoice_id
		WHERE u.kind='invoice' AND NOT u.zoho_applied AND a.zoho_payment_id<>'' AND i.zoho_bill_id IS NOT NULL AND i.zoho_step>=3
		AND (a.zoho_error='' OR a.zoho_at < now() - interval '30 minutes') ORDER BY a.id LIMIT 20`)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return err
	}
	for _, id := range ids {
		var payment, contact, ref string
		var paid float64
		var paidOn *time.Time
		if err := s.DB.QueryRow(ctx, `SELECT a.zoho_payment_id, coalesce(m.contact_id,''), a.payment_ref, (a.amount-a.tds_amount)::float8, a.paid_on
			FROM advance a LEFT JOIN zoho_map_vendor m ON m.vendor_id=a.vendor_id WHERE a.id=$1`, id).Scan(&payment, &contact, &ref, &paid, &paidOn); err != nil {
			return err
		}
		type applied struct {
			Bill   string
			Amount float64
		}
		br, err := s.DB.Query(ctx, `SELECT i.zoho_bill_id, sum(u.amount-u.tds_amount)::float8 FROM advance_use u JOIN invoice i ON i.id=u.invoice_id
			WHERE u.advance_id=$1 AND u.kind='invoice' AND i.zoho_bill_id IS NOT NULL AND i.zoho_step>=3 GROUP BY i.zoho_bill_id ORDER BY 1`, id)
		if err != nil {
			return err
		}
		bills, err := pgx.CollectRows(br, pgx.RowToStructByPos[applied])
		if err != nil {
			return err
		}
		list := []map[string]any{}
		for _, b := range bills {
			list = append(list, map[string]any{"bill_id": b.Bill, "amount_applied": b.Amount})
		}
		date := time.Now()
		if paidOn != nil {
			date = *paidOn
		}
		body := map[string]any{"vendor_id": contact, "amount": paid, "date": date.Format("2006-01-02"), "paid_through_account_id": account, "reference_number": ref, "bills": list}
		if zp, err := c.GetVendorPayment(ctx, payment); err == nil && zp.ID != "" {
			// Keep what Zoho has (Finance may have recorded it there, from another account); only the bills change.
			body["amount"], body["date"], body["reference_number"] = zp.Amount, zp.Date, zp.Reference
			if zp.AccountID != "" {
				body["paid_through_account_id"] = zp.AccountID
			}
		} else if err != nil && stop(s.Sync, ctx, err) {
			return err
		}
		if err := c.UpdateVendorPayment(ctx, payment, body); err != nil {
			if stop(s.Sync, ctx, err) {
				return err
			}
			if _, err := s.DB.Exec(ctx, `UPDATE advance SET zoho_error=$2, zoho_at=now() WHERE id=$1`, id, "Zoho did not apply it to the bill: "+err.Error()); err != nil {
				return err
			}
			continue
		}
		if _, err := s.DB.Exec(ctx, `UPDATE advance_use u SET zoho_applied=TRUE FROM invoice i WHERE i.id=u.invoice_id AND u.advance_id=$1 AND u.kind='invoice' AND i.zoho_bill_id IS NOT NULL AND i.zoho_step>=3`, id); err != nil {
			return err
		}
		if _, err := s.DB.Exec(ctx, `UPDATE advance SET zoho_error='', zoho_at=now() WHERE id=$1`, id); err != nil {
			return err
		}
	}
	return nil
}

// carrying finds the payment that names this advance number in its reference or notes (as a whole word, so
// ADV_HZ_0001_01 does not match ADV_HZ_0001_01_01).
func carrying(list []zohoapi.VendorPayment, number string) (zohoapi.VendorPayment, bool) {
	all := carryingAll(list, number)
	if len(all) == 0 {
		return zohoapi.VendorPayment{}, false
	}
	return all[0], true
}

// carryingAll is every payment that names this advance number.
func carryingAll(list []zohoapi.VendorPayment, number string) []zohoapi.VendorPayment {
	re := regexp.MustCompile(`(?i)(^|[^A-Za-z0-9_])` + regexp.QuoteMeta(number) + `($|[^A-Za-z0-9_])`)
	out := []zohoapi.VendorPayment{}
	for _, p := range list {
		if re.MatchString(p.Reference) || re.MatchString(p.Description) {
			out = append(out, p)
		}
	}
	return out
}

// ReadAdvances looks in Zoho for the payments of approved advances: Finance records the advance as a vendor payment
// in Zoho with the advance number (ADV_...) in its Reference# or Notes, and the advance becomes paid with Zoho's date
// and reference. It reports how many were paid and, for every advance still waiting, why (also kept on the advance,
// so Payments › Advances shows it).
func (s *Service) ReadAdvances(ctx context.Context) (int, []string, error) {
	notes := []string{}
	if !s.Sync.Connected(ctx) || s.Advances == nil {
		return 0, notes, nil
	}
	c, _, err := s.Sync.Client(ctx)
	if err != nil {
		return 0, notes, err
	}
	type adv struct {
		ID                      int64
		Number, Contact, Vendor string
		Due                     float64
		Since                   time.Time
	}
	rows, err := s.DB.Query(ctx, `SELECT a.id, a.number, coalesce(m.contact_id,''), v.legal_name, (a.amount-a.tds_amount)::float8, coalesce(a.decided_at, a.requested_at)
		FROM advance a JOIN vendor v ON v.id=a.vendor_id LEFT JOIN zoho_map_vendor m ON m.vendor_id=a.vendor_id WHERE a.status='APPROVED' ORDER BY 3, a.id`)
	if err != nil {
		return 0, notes, err
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[adv])
	if err != nil {
		return 0, notes, err
	}
	why := func(a adv, msg string) {
		notes = append(notes, a.Number+" ("+a.Vendor+"): "+msg)
		_, _ = s.DB.Exec(ctx, `UPDATE advance SET zoho_error=$2, zoho_at=now() WHERE id=$1`, a.ID, msg)
	}
	paid := 0
	cache := map[string][]zohoapi.VendorPayment{}
	failed := map[string]string{}
	for _, a := range list {
		if a.Contact == "" {
			why(a, a.Vendor+" is not linked to a Zoho vendor yet, so its Zoho payments cannot be read (Settings, Zoho Books, Vendors).")
			continue
		}
		found, ok := cache[a.Contact]
		if msg, bad := failed[a.Contact]; bad {
			why(a, msg)
			continue
		}
		if !ok {
			// One read per vendor, from a few days before the earliest advance waiting.
			since := a.Since
			for _, b := range list {
				if b.Contact == a.Contact && b.Since.Before(since) {
					since = b.Since
				}
			}
			if found, err = c.VendorPayments(ctx, a.Contact, since.AddDate(0, 0, -3)); err != nil {
				if stop(s.Sync, ctx, err) {
					return paid, notes, err
				}
				failed[a.Contact] = "Zoho did not list this vendor's payments: " + err.Error()
				why(a, failed[a.Contact])
				continue
			}
			cache[a.Contact] = found
		}
		// Of the payments naming this advance (not already taken by another), the one for the right amount.
		var p, wrong zohoapi.VendorPayment
		for _, x := range carryingAll(found, a.Number) {
			var used bool
			_ = s.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM advance WHERE zoho_payment_id=$1 AND id<>$2)`, x.ID, a.ID).Scan(&used)
			switch {
			case used:
			case math.Abs(x.Amount-a.Due) <= 1:
				p = x
			case wrong.ID == "":
				wrong = x
			}
			if p.ID != "" {
				break
			}
		}
		if p.ID == "" {
			switch {
			case wrong.ID != "":
				why(a, fmt.Sprintf("Zoho payment %s is %s, but this advance pays the vendor %s. Correct it in Zoho.", wrong.Number, money.INR(wrong.Amount), money.INR(a.Due)))
			default:
				// Point at a likely payment that only lacks the advance number.
				hint := ""
				for _, x := range found {
					var used bool
					_ = s.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM advance WHERE zoho_payment_id=$1)`, x.ID).Scan(&used)
					if !used && math.Abs(x.Amount-a.Due) <= 1 {
						hint = fmt.Sprintf(" Zoho has payment %s of %s on %s for this vendor: if it is this advance, put %s in its Reference#.", x.Number, money.INR(x.Amount), x.Date, a.Number)
						break
					}
				}
				why(a, fmt.Sprintf("No Zoho payment to %s mentions %s yet (%d payments checked).%s", a.Vendor, a.Number, len(found), hint))
			}
			continue
		}
		on, ok := clock.ParseDate(p.Date)
		if !ok {
			on = clock.Today()
		}
		ref := p.Reference
		if ref == "" {
			ref = p.Number
		}
		if _, err := s.Advances.PaidInZoho(ctx, a.ID, on, ref, p.ID, p.Number); err != nil {
			continue // paid or cancelled meanwhile
		}
		paid++
	}
	return paid, notes, nil
}

// readAdvancePayments is the "Read advance payments from Zoho" button.
func (s *Service) readAdvancePayments(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	if !canPush(u) {
		return httpjson.Forbidden("Only Finance or the System Admin can read advance payments.")
	}
	if !s.Sync.Connected(r.Context()) {
		return httpjson.Bad("Connect Zoho Books first (Settings, Zoho Books).")
	}
	n, notes, err := s.ReadAdvances(r.Context())
	if err != nil {
		return httpjson.Bad("Reading advance payments failed: %v", err)
	}
	_ = auditlog.Write(r.Context(), s.DB, u, "Zoho", "Advance payments read", "Zoho Books", fmt.Sprintf("%d advances paid, %d still waiting", n, len(notes)))
	msg := fmt.Sprintf("%d advance%s found paid in Zoho and marked paid.", n, map[bool]string{true: "", false: "s"}[n == 1])
	var waiting int
	_ = s.DB.QueryRow(r.Context(), `SELECT count(*) FROM advance WHERE status='PENDING'`).Scan(&waiting)
	switch {
	case n == 0 && len(notes) == 0 && waiting > 0:
		msg = fmt.Sprintf("No advance is approved and waiting for payment. %d advance%s still wait%s for Finance to approve %s in Purchase Desk first (Payments › Advances).",
			waiting, map[bool]string{true: "", false: "s"}[waiting == 1], map[bool]string{true: "s", false: ""}[waiting == 1], map[bool]string{true: "it", false: "them"}[waiting == 1])
	case n == 0 && len(notes) == 0:
		msg = "No advance is approved and waiting for payment in Purchase Desk."
	}
	return httpjson.OK(w, map[string]any{"message": msg, "not_found": notes})
}

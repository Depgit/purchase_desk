package zohopush

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

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

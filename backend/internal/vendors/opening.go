package vendors

// A vendor's opening balance from before Purchase Desk, typically a vendor already in Zoho with a ledger: negative =
// an advance already paid, positive = old bills still owed, as on a date. The vendor's statement starts with it. A
// negative one is kept as a paid, carried-forward "opening" advance (advance.opening, no PO or property), so the
// vendor's next invoices take it at payment approval like any carried-forward advance; it is never sent to Zoho,
// where it already is.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"purchasedesk/internal/access"
	"purchasedesk/internal/auditlog"
	"purchasedesk/internal/clock"
	"purchasedesk/internal/httpjson"
	"purchasedesk/internal/money"
)

// checkOpening validates the opening balance fields of a save (nil Opening: unchanged).
func checkOpening(in *Input) []string {
	if in.Opening == nil {
		return nil
	}
	*in.Opening = money.Round2(*in.Opening)
	if in.OpeningAsOn == "" {
		return nil
	}
	t, ok := clock.ParseDate(in.OpeningAsOn)
	if !ok {
		return []string{"Enter the opening balance date as YYYY-MM-DD."}
	}
	if t.After(clock.Today()) {
		return []string{"The opening balance date cannot be in the future."}
	}
	return nil
}

// syncOpening saves the opening balance and keeps the vendor's opening advance in step with it. It refuses to make
// the opening advance smaller than what invoices have already taken from it.
func (s *Service) syncOpening(ctx context.Context, tx pgx.Tx, u *access.User, vendorID int64, in *Input) error {
	if in.Opening == nil {
		return nil
	}
	bal := money.Round2(*in.Opening)
	var asOn any
	if in.OpeningAsOn != "" {
		asOn = in.OpeningAsOn
	}
	if _, err := tx.Exec(ctx, `UPDATE vendor SET opening_balance=$2::numeric, opening_as_on=CASE WHEN $2::numeric=0 THEN NULL ELSE coalesce($3::date, opening_as_on, current_date) END WHERE id=$1`, vendorID, bal, asOn); err != nil {
		return err
	}
	var code string
	var on string
	if err := tx.QueryRow(ctx, `SELECT code, coalesce(opening_as_on::text, current_date::text) FROM vendor WHERE id=$1`, vendorID).Scan(&code, &on); err != nil {
		return err
	}
	var advID int64
	var used float64
	_ = tx.QueryRow(ctx, `SELECT id, coalesce((SELECT sum(x.amount) FROM advance_use x WHERE x.advance_id=a.id),0)::float8 FROM advance a WHERE a.vendor_id=$1 AND a.opening`, vendorID).Scan(&advID, &used)
	if advID != 0 && used > 0.005 && -bal < used-0.005 {
		return httpjson.Bad("%s of the opening advance is already taken off this vendor's invoices, so the opening balance cannot be above -%s.", money.INR(used), money.INR(used))
	}
	switch {
	case bal < 0 && advID == 0:
		if _, err := tx.Exec(ctx, `INSERT INTO advance(number,vendor_id,amount,reason,status,requested_by,decided_by,decided_at,paid_by,paid_on,payment_ref,carry_forward,opening)
			VALUES ($1,$2,$3,'Opening balance: advance paid before Purchase Desk','PAID',$4,$4,now(),$4,$5,'Opening balance',TRUE,TRUE)`, "ADV-OPEN-"+code, vendorID, -bal, u.ID, on); err != nil {
			return err
		}
	case bal < 0:
		if _, err := tx.Exec(ctx, `UPDATE advance SET amount=$2, paid_on=$3, status='PAID' WHERE id=$1`, advID, -bal, on); err != nil {
			return err
		}
	case advID != 0:
		if _, err := tx.Exec(ctx, `UPDATE advance SET status='CANCELLED', decision_note='Opening balance changed' WHERE id=$1 AND status='PAID'`, advID); err != nil {
			return err
		}
	}
	if bal == 0 && advID == 0 {
		return nil
	}
	return auditlog.Write(ctx, tx, u, "Vendors", "Opening balance set", code, fmt.Sprintf("%s as on %s", money.INR(bal), on))
}

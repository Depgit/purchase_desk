// Package poexpiry holds vendor POs to their delivery date. The date is given when the PO is raised (the last
// approver, or Purchase); before it passes, Purchase or an approver of the request may extend it once, with a reason.
// Once it has passed by Flow Settings po_expiry_days, the hourly po-expiry job rejects what is still undelivered
// (balancereject.Apply): a part-delivered PO is closed short, one with nothing delivered is cancelled, the vendor and
// the requester are told, and a paid advance on it is carried forward to the vendor's next PO.
package poexpiry

import (
	"context"
	"log"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"purchasedesk/internal/access"
	"purchasedesk/internal/auditlog"
	"purchasedesk/internal/balancereject"
	"purchasedesk/internal/clock"
	"purchasedesk/internal/documents"
	"purchasedesk/internal/flowsettings"
	"purchasedesk/internal/httpjson"
	"purchasedesk/internal/textclean"
)

type Service struct {
	DB      *pgxpool.Pool
	Balance *balancereject.Service // tells the vendor and the requester
}

func (s *Service) Routes(r chi.Router) {
	r.Post("/documents/{id}/deliver-by", httpjson.Wrap(s.extend))
}

// CanExtend reports whether u may move d's delivery date: an open vendor PO not extended before, by Purchase or an
// approver of the request (documents.POKeeper).
func CanExtend(u *access.User, d *documents.Doc) bool {
	return d.Status == documents.POIssued && !d.IsTransfer && !d.DeliverExtended && (d.Kind == "purchase" || d.Kind == "corp_purchase") && documents.POKeeper(u, d) != nil
}

// extend moves a PO's delivery date once, with a reason; the vendor's Zoho PO follows.
func (s *Service) extend(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	id, err := httpjson.ID(r, "id")
	if err != nil {
		return err
	}
	var in struct {
		DeliverBy string `json:"deliver_by"`
		Reason    string `json:"reason"`
	}
	if err := httpjson.Decode(r, &in); err != nil {
		return err
	}
	in.Reason = strings.TrimSpace(in.Reason)
	day, ok := clock.ParseDate(in.DeliverBy)
	if !ok {
		return httpjson.Bad("Choose the new delivery date.")
	}
	if len(in.Reason) < 5 {
		return httpjson.Bad("Write why the delivery date moves (a few words).")
	}
	var msg string
	err = pgx.BeginFunc(r.Context(), s.DB, func(tx pgx.Tx) error {
		d, err := documents.Load(r.Context(), tx, id, true)
		if err != nil {
			return err
		}
		number := textclean.Default(d.PONumber, d.Number)
		if d.DeliverExtended {
			return httpjson.Conflict("The delivery date of %s was extended once already. If the goods still do not come, the balance is rejected; raise a new request if they are needed.", number)
		}
		if !CanExtend(u, d) {
			return httpjson.Forbidden("Only Purchase or an approver of %s can extend its delivery date, while goods are still due.", number)
		}
		if cur, ok := clock.ParseDate(d.DeliverBy); ok && !day.After(cur) {
			return httpjson.Bad("The new date must be after the current one (%s).", cur.Format("2 Jan 2006"))
		}
		if _, err := tx.Exec(r.Context(), `UPDATE document SET deliver_by=$2, deliver_by_extended=TRUE, updated_at=now() WHERE id=$1`, id, day); err != nil {
			return err
		}
		if err := documents.QueueZohoPO(r.Context(), tx, id, "update"); err != nil {
			return err
		}
		msg = number + " now to be delivered by " + day.Format("2 Jan 2006")
		return auditlog.Doc(r.Context(), tx, u, documents.POKeeper(u, d).Name, d.ID, d.Number, "Purchases", "Delivery date extended to "+day.Format("2 Jan 2006")+" (from "+d.DeliverBy+")", in.Reason)
	})
	if err != nil {
		return err
	}
	return httpjson.OK(w, map[string]string{"message": msg})
}

// Run is the po-expiry job: every open vendor PO whose delivery date passed more than Flow Settings po_expiry_days
// ago has its undelivered balance rejected.
func (s *Service) Run(ctx context.Context) error {
	st, err := flowsettings.Load(ctx, s.DB)
	if err != nil {
		return err
	}
	var ids []int64
	rows, err := s.DB.Query(ctx, `SELECT d.id FROM document d WHERE d.status='PO_ISSUED' AND NOT d.is_transfer AND d.kind IN ('purchase','corp_purchase')
		AND d.deliver_by IS NOT NULL AND d.deliver_by + $1::int < $2::date
		AND EXISTS (SELECT 1 FROM document_line l WHERE l.document_id=d.id AND l.qty > l.received_qty) ORDER BY d.id`, st.POExpiryDays, clock.Today())
	if err != nil {
		return err
	}
	if ids, err = pgx.CollectRows(rows, pgx.RowTo[int64]); err != nil {
		return err
	}
	for _, id := range ids {
		var res balancereject.Result
		why := "Delivery date passed"
		err := pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
			d, err := documents.Load(ctx, tx, id, true)
			if err != nil || d.Status != documents.POIssued {
				return err
			}
			why = "Delivery date " + d.DeliverBy + " passed"
			q := map[int64]float64{}
			for _, l := range d.Lines {
				if open := l.Qty - l.ReceivedQty; open > 0 {
					q[l.ID] = open
				}
			}
			res, err = balancereject.Apply(ctx, tx, nil, "System", d, q, why+": the undelivered balance is rejected automatically", true)
			return err
		})
		if err != nil {
			log.Printf("po-expiry %d: %v", id, err)
			continue
		}
		if s.Balance != nil && res.Message != "" {
			s.Balance.Notify(nil, id, res, why)
		}
		log.Printf("po-expiry: %s", res.Message)
	}
	return nil
}

// Package worklist counts what is waiting for the signed-in user, per menu section, for the badges in the left bar.
package worklist

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"purchasedesk/internal/access"
	"purchasedesk/internal/documents"
	"purchasedesk/internal/httpjson"
	"purchasedesk/internal/masterapproval"
	"purchasedesk/internal/screens"
)

type Service struct {
	DB    *pgxpool.Pool
	Guard *access.Guard
}

func (s *Service) Routes(r chi.Router) {
	r.Get("/worklist", httpjson.Wrap(s.counts))
}

func (s *Service) counts(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	all, ids, err := s.Guard.Visible(r.Context(), u)
	if err != nil {
		return err
	}
	docs, err := documents.LoadMany(r.Context(), s.DB, `WHERE d.status IN ('PENDING','RETURNED','APPROVED','PO_ISSUED','WH_SENT') AND ($1 OR d.property_id = ANY($2) OR d.requester_id=$3
		OR $3 IN (d.corp_l1_user_id, d.corp_l2_user_id, d.corp_backup_user_id))`, all, ids, u.ID)
	if err != nil {
		return err
	}
	c := map[string]int{}
	// Vendors with POs raised but not yet pushed in an order sheet, for whoever sends the sheets.
	toPush := map[int64]bool{}
	sendsSheets := (u.IsAdmin || u.HasStep("cc") || u.HasStep("l2") || u.HasStep("preop") || u.HasStep("wh") || u.HasStep("whl2") || u.HasStep("purchase"))
	for _, d := range docs {
		owner := d.Owner()
		switch d.Status {
		case documents.Pending:
			if documents.CanAct(u, d, owner, access.Approve) != nil {
				if d.IsCorp() {
					c["corporate"]++
				} else {
					c["approvals"]++
				}
			}
		case documents.Returned:
			if d.RequesterID == u.ID {
				if d.IsCorp() {
					c["corporate"]++
				} else { // returned bills are listed with the requests
					c["requests"]++
				}
			}
		case documents.Approved:
			if (d.Kind == "purchase" || d.Kind == "corp_purchase") && d.AdvanceHold == "" && documents.CanAct(u, d, "purchase", access.IssuePO) != nil {
				c["purchase_orders"]++
			}
		case documents.POIssued:
			if sendsSheets && d.Kind == "purchase" && !d.IsTransfer && d.SentToVendor == nil {
				toPush[d.VendorID] = true
			}
			// A PO the vendor declined waits for the Purchase Team to sort it out.
			if d.VendorStatus == "declined" && documents.POKeeper(u, d) != nil {
				c["purchase_orders"]++
			}
			if d.IsTransfer {
				if documents.CanAct(u, d, "wh", access.Receive) != nil {
					c["warehouse"]++
				}
			} else if documents.Receiver(u, d) != nil {
				if d.IsCorp() {
					c["corporate"]++
				} else {
					c["goods_receipts"]++
				}
			}
		case documents.WHSent:
			if documents.TransferReceiver(u, d) != nil {
				c["goods_receipts"]++
			}
		}
	}
	if len(toPush) > 0 {
		c["vendor_sheets"] = len(toPush)
	}
	if u.IsAdmin || u.HasStep("finance") {
		c["master_approvals"] = masterapproval.Count(r.Context(), s.DB)
	}
	rows, err := s.DB.Query(r.Context(), `SELECT i.status, i.property_id, p.cluster_id FROM invoice i JOIN property p ON p.id=i.property_id
		WHERE i.status IN ('RECEIVED','MATCHED','PAY_APPROVED') AND ($1 OR i.property_id = ANY($2))`, all, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var st string
		var pid, cid int64
		if err := rows.Scan(&st, &pid, &cid); err != nil {
			return err
		}
		switch st {
		case documents.InvReceived:
			if u.Can("ap", access.Match, pid, cid, nil) != nil {
				c["invoices"]++
			}
		default:
			if u.Can("finance", access.Pay, pid, cid, nil) != nil {
				c["payments"]++
			}
		}
	}
	// Advances waiting for Finance to approve or pay count with the payments.
	ar, err := s.DB.Query(r.Context(), `SELECT a.property_id, p.cluster_id FROM advance a JOIN property p ON p.id=a.property_id
		WHERE a.status IN ('PENDING','APPROVED') AND ($1 OR a.property_id = ANY($2))`, all, ids)
	if err != nil {
		return err
	}
	defer ar.Close()
	for ar.Next() {
		var pid, cid int64
		if err := ar.Scan(&pid, &cid); err != nil {
			return err
		}
		if u.Can("finance", access.Pay, pid, cid, nil) != nil {
			c["payments"]++
		}
	}
	if u.IsAdmin || u.HasStep("finance") {
		var n int
		_ = s.DB.QueryRow(r.Context(), `SELECT count(*) FROM vendor WHERE active AND NOT bank_verified AND vendor_type<>'warehouse'`).Scan(&n)
		c["vendors"] = n
	}
	// A badge counts work the user can do: drop screens their roles may not change.
	for k := range c {
		if u.Overall(k) < screens.Edit {
			delete(c, k)
		}
	}
	return httpjson.OK(w, c)
}

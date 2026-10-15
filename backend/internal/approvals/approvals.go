// Package approvals routes every request to its approvers and records their decisions.
// Routes: warehouse stock or pre-opening property → the chosen Pre-opening Team member;
// event request → Cluster Manager then Cost Controller; normal request → Cost Controller.
// The Cost Controller can send any request to the Cluster Manager, whose approval is then final.
// Utility and AMC bills go to the Cluster Manager (or the Cost Controller, per Flow Settings).
package approvals

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"purchasedesk/internal/access"
	"purchasedesk/internal/auditlog"
	"purchasedesk/internal/budgets"
	"purchasedesk/internal/clock"
	"purchasedesk/internal/database"
	"purchasedesk/internal/documents"
	"purchasedesk/internal/flowsettings"
	"purchasedesk/internal/httpjson"
	"purchasedesk/internal/messaging"
	"purchasedesk/internal/money"
	"purchasedesk/internal/purchaseorders"
	"purchasedesk/internal/screens"
)

type Service struct {
	DB       *pgxpool.Pool
	Guard    *access.Guard
	Messages *messaging.Sender
	// POSent emails the vendor a PO issued automatically at approval (purchaseorders.SendPO).
	POSent func(u *access.User, docID int64)
}

func (s *Service) Routes(r chi.Router) {
	r.Get("/approvals", httpjson.Wrap(s.workbench))
	r.Post("/approvals/bulk", httpjson.Wrap(s.bulk))
	r.Post("/documents/{id}/approve", httpjson.Wrap(s.single("approve")))
	r.Post("/documents/{id}/return", httpjson.Wrap(s.single("return")))
	r.Post("/documents/{id}/reject", httpjson.Wrap(s.single("reject")))
	r.Post("/documents/{id}/route", httpjson.Wrap(s.single("route")))
}

// Chain returns the approval steps for a new purchase request.
func Chain(st flowsettings.Settings, propertyKind string, vendorIsWarehouse, event bool, amount float64, requesterStep string) []string {
	if propertyKind == "warehouse" {
		if requesterStep == "wh" {
			return []string{"cc"} // the Warehouse Manager's own purchase (a shortfall) goes to the Cost Controller
		}
		return []string{"wh"} // raised by Warehouse Staff, approved by the Warehouse Manager
	}
	if propertyKind == "preopening" {
		return []string{"preop"}
	}
	if vendorIsWarehouse && !st.WHApproval {
		return []string{}
	}
	steps := []string{}
	if (event && st.EvtL2) || (st.L2Above > 0 && amount > st.L2Above) {
		steps = append(steps, "l2")
	}
	if st.CCAll || len(steps) == 0 {
		steps = append(steps, "cc")
	}
	return steps
}

func moduleOf(d *documents.Doc) string {
	switch {
	case d.IsCorp():
		return "Corporate"
	case d.Kind == "bill":
		return "Bills"
	}
	return "Purchases"
}

// CorpSteps returns the approval steps for a corporate request: the department's L1 then its L2.
// Below the Admin's threshold the L1 alone is enough; if the requester is the L1, it goes straight to the L2.
func CorpSteps(st flowsettings.Settings, amount float64, requesterID, l1 int64) []string {
	steps := []string{"cl1", "cl2"}
	if st.CorpL1OnlyBelow > 0 && amount < st.CorpL1OnlyBelow {
		steps = []string{"cl1"}
	}
	if requesterID == l1 {
		steps = []string{"cl2"}
	}
	return steps
}

// Decision is what an approver sends.
type Decision struct {
	Quantities map[int64]float64 `json:"quantities"` // line id → new quantity (approve only)
	Reason     string            `json:"reason"`
	Note       string            `json:"note"`
	// At the final approval, what the PO it raises needs: confirmed rates of estimated lines, and the advance when the
	// vendor is on Advance terms.
	purchaseorders.AtApproval
}

type outcome struct {
	docID   int64
	message string
	notify  string // template code to send the requester, if any
	reason  string
	autoPO  string // the PO issued automatically at the final approval
}

// Act applies one decision inside a transaction.
func (s *Service) Act(ctx context.Context, tx pgx.Tx, u *access.User, st flowsettings.Settings, action string, docID int64, dec Decision) (outcome, error) {
	out := outcome{docID: docID}
	d, err := documents.Load(ctx, tx, docID, true)
	if err != nil {
		return out, err
	}
	if d.Status != documents.Pending {
		return out, httpjson.Conflict("%s is no longer waiting for approval.", d.Number)
	}
	step := d.Owner()
	g := documents.CanAct(u, d, step, access.Approve)
	if g == nil {
		return out, httpjson.Forbidden("%s is waiting for the %s, and you cannot act on it.", d.Number, documents.StepNames[step])
	}
	// Screen levels win over rights: approving needs edit on Approvals (Corporate for corporate documents) there.
	screen := approveScreen(d)
	if u.Level(screen, d.PropertyID, d.ClusterID) < screens.Edit {
		return out, httpjson.Forbidden("Your role can only view %s at %s, so it cannot act on %s.", screens.Label(screen), d.Property, d.Number)
	}
	module := moduleOf(d)
	logIt := func(action, note string) error {
		return auditlog.Doc(ctx, tx, u, g.Name, d.ID, d.Number, module, action, note)
	}
	note := strings.TrimSpace(dec.Note)
	switch action {
	case "return", "reject":
		list := map[string]string{"return": "return_reasons", "reject": "reject_reasons"}[action]
		if err := flowsettings.CheckReason(ctx, tx, list, dec.Reason, note); err != nil {
			return out, err
		}
		status := map[string]string{"return": documents.Returned, "reject": documents.Rejected}[action]
		if _, err := tx.Exec(ctx, `UPDATE document SET status=$2, return_reason=$3, return_note=$4, updated_at=now() WHERE id=$1`, d.ID, status, dec.Reason, note); err != nil {
			return out, err
		}
		if action == "reject" && d.IsBill() {
			if _, err := tx.Exec(ctx, `UPDATE invoice SET status='VOID' WHERE document_id=$1 AND status='AWAITING'`, d.ID); err != nil {
				return out, err
			}
		}
		full := dec.Reason
		if note != "" {
			full += " · " + note
		}
		label := map[string]string{"return": "Returned for changes", "reject": "Rejected"}[action]
		out.message, out.notify, out.reason = d.Number+" "+strings.ToLower(label), map[string]string{"return": "ret", "reject": "rej"}[action], full
		return out, logIt(label, full)

	case "route":
		if step != "cc" || d.Kind != "purchase" {
			return out, httpjson.Bad("Only the Cost Controller can send a purchase request to the Cluster Manager.")
		}
		if err := flowsettings.CheckReason(ctx, tx, "route_reasons", dec.Reason, note); err != nil {
			return out, err
		}
		if err := applyQuantities(ctx, tx, u, st, d, dec.Quantities, g.Name, logIt); err != nil {
			return out, err
		}
		steps := append(append([]string{}, d.RouteSteps[:d.StepIdx+1]...), "l2")
		if _, err := tx.Exec(ctx, `UPDATE document SET route_steps=$2, step_idx=$3, routed_reason=$4, routed_note=$5, updated_at=now() WHERE id=$1`,
			d.ID, steps, d.StepIdx+1, dec.Reason, note); err != nil {
			return out, err
		}
		out.message = d.Number + " sent to the Cluster Manager"
		return out, logIt("Sent to Cluster Manager", strings.TrimSpace(dec.Reason+" · "+note))

	case "approve":
		changed := false
		if d.Kind == "purchase" {
			before := d.ModifiedNote
			if err := applyQuantities(ctx, tx, u, st, d, dec.Quantities, g.Name, logIt); err != nil {
				return out, err
			}
			d, err = documents.Load(ctx, tx, docID, false)
			if err != nil {
				return out, err
			}
			changed = d.ModifiedNote != before
		}
		if err := logIt("Approved", note); err != nil {
			return out, err
		}
		extra := ""
		if step == "l2" && d.Kind == "purchase" {
			extra = ", cm_approved_at=now()"
		}
		if _, err := tx.Exec(ctx, `UPDATE document SET step_idx=step_idx+1, updated_at=now()`+extra+` WHERE id=$1`, d.ID); err != nil {
			return out, err
		}
		d.StepIdx++
		out.message = d.Number + " approved"
		if d.StepIdx >= len(d.RouteSteps) {
			res, err := Finalize(ctx, tx, u, g.Name, d, dec.AtApproval)
			if err != nil {
				return out, err
			}
			out.autoPO = res.PO
			switch {
			case res.PO != "" && d.Kind == "purchase":
				out.message += ", " + res.PO + " raised: push it to the vendor with the vendor order sheet"
			case res.PO != "":
				out.message += ", " + res.PO + " issued to the vendor"
			case res.Held != "":
				out.message += ". " + res.Advance + "; " + res.Held + " goes to the vendor once Finance approves it"
			}
			out.notify = "ok"
			if changed {
				out.notify = "mod"
			}
		} else {
			out.message += ", now with the " + documents.StepNames[d.RouteSteps[d.StepIdx]]
		}
		return out, nil
	}
	return out, httpjson.Bad("Unknown action.")
}

// applyQuantities lets an approver reduce a quantity, drop a line (0), or increase it if the setting allows.
// The original quantity is kept and the change is shown to the requester.
func applyQuantities(ctx context.Context, tx pgx.Tx, u *access.User, st flowsettings.Settings, d *documents.Doc, qs map[int64]float64, roleName string, logIt func(string, string) error) error {
	if len(qs) == 0 || d.Kind != "purchase" {
		return nil
	}
	changes := []string{}
	left := 0.0
	for _, l := range d.Lines {
		q, ok := qs[l.ID]
		if !ok {
			left += l.Qty
			continue
		}
		if q < 0 {
			return httpjson.Bad("Quantity cannot be below 0.")
		}
		if q > l.Qty && !st.QtyUp {
			return httpjson.Bad("Approvers can only reduce a quantity.")
		}
		left += q
		if q != l.Qty {
			txt := fmt.Sprintf("%s: %v to %v", l.ItemName, l.Qty, q)
			if q == 0 {
				txt += " (removed)"
			}
			changes = append(changes, txt)
			if _, err := tx.Exec(ctx, `UPDATE document_line SET original_qty=coalesce(original_qty, qty), qty=$2 WHERE id=$1`, l.ID, q); err != nil {
				return err
			}
		}
	}
	if left == 0 {
		return httpjson.Bad("To drop the whole request, use Reject.")
	}
	if len(changes) == 0 {
		return nil
	}
	txt := strings.Join(changes, "; ")
	if _, err := tx.Exec(ctx, `UPDATE document SET modified_note=CASE WHEN modified_note='' THEN $2 ELSE modified_note||'; '||$2 END, modified_by=$3, modified_at=now() WHERE id=$1`,
		d.ID, txt, u.Name+" ("+roleName+")"); err != nil {
		return err
	}
	return logIt("Quantity changed by approver", txt)
}

// Finalize runs when the last approver approves: bills go to Finance; purchases count against the budget,
// an order on the warehouse becomes a transfer order (WT number) in the dispatch queue, and a vendor request gets its
// PO (purchaseorders.Auto: every request with no Purchase Team step, else per sub-category). role is the approver's.
func Finalize(ctx context.Context, tx pgx.Tx, u *access.User, role string, d *documents.Doc, a purchaseorders.AtApproval) (purchaseorders.AutoResult, error) {
	var none purchaseorders.AutoResult
	module := moduleOf(d)
	if d.IsBill() {
		if _, err := tx.Exec(ctx, `UPDATE document SET status='APPROVED', approved_at=now(), updated_at=now() WHERE id=$1`, d.ID); err != nil {
			return none, err
		}
		_, err := tx.Exec(ctx, `UPDATE invoice SET status='MATCHED', matched_at=now(), matched_by=$2 WHERE document_id=$1 AND status='AWAITING'`, d.ID, u.ID)
		return none, err
	}
	if !d.NoBudget() {
		if err := budgets.Commit(ctx, tx, d.PropertyID, d.ID, d.SubCatTotals()); err != nil {
			return none, err
		}
	}
	if d.VendorType == "warehouse" {
		wt, ok := documents.Derived(d.Number, "WT")
		if !ok {
			var err error
			if wt, err = documents.NextNumber(ctx, tx, "wt_seq", "WT"); err != nil {
				return none, err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE document SET status='PO_ISSUED', approved_at=now(), is_transfer=TRUE, po_number=$2, po_date=now(),
			deliver_by=current_date+1, payment_terms='Internal transfer', updated_at=now() WHERE id=$1`, d.ID, wt); err != nil {
			return none, err
		}
		return none, auditlog.Doc(ctx, tx, u, "System", d.ID, d.Number, module, wt+" raised on the warehouse", "")
	}
	if _, err := tx.Exec(ctx, `UPDATE document SET status='APPROVED', approved_at=now(), updated_at=now() WHERE id=$1`, d.ID); err != nil {
		return none, err
	}
	st, err := flowsettings.Load(ctx, tx)
	if err != nil {
		return none, err
	}
	return purchaseorders.Auto(ctx, tx, u, role, d, st.AutoPODays, st.POOnApproval, a)
}

func (s *Service) single(action string) httpjson.Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		u := access.From(r)
		id, err := httpjson.ID(r, "id")
		if err != nil {
			return err
		}
		var dec Decision
		if err := httpjson.Decode(r, &dec); err != nil {
			return err
		}
		st, err := flowsettings.Load(r.Context(), s.DB)
		if err != nil {
			return err
		}
		var out outcome
		err = pgx.BeginFunc(r.Context(), s.DB, func(tx pgx.Tx) error {
			var err error
			out, err = s.Act(r.Context(), tx, u, st, action, id, dec)
			return err
		})
		if err != nil {
			return err
		}
		s.notify(u, out)
		s.sendPO(u, out)
		return httpjson.OK(w, map[string]string{"message": out.message})
	}
}

type bulkIn struct {
	Action     string                      `json:"action"`
	IDs        []int64                     `json:"ids"`
	Quantities map[int64]map[int64]float64 `json:"quantities"`
	Reason     string                      `json:"reason"`
	Note       string                      `json:"note"`
}

type bulkResult struct {
	ID      int64  `json:"id"`
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

// bulk applies the same decision to many requests. Each request is saved on its own, so one failure does not block the rest.
func (s *Service) bulk(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	var in bulkIn
	if err := httpjson.Decode(r, &in); err != nil {
		return err
	}
	if len(in.IDs) == 0 {
		return httpjson.Bad("Select at least one request.")
	}
	st, err := flowsettings.Load(r.Context(), s.DB)
	if err != nil {
		return err
	}
	res := []bulkResult{}
	ok := 0
	for _, id := range in.IDs {
		var out outcome
		err := pgx.BeginFunc(r.Context(), s.DB, func(tx pgx.Tx) error {
			var err error
			note := in.Note
			if in.Action == "approve" && note == "" && len(in.IDs) > 1 {
				note = "Bulk approval"
			}
			out, err = s.Act(r.Context(), tx, u, st, in.Action, id, Decision{Quantities: in.Quantities[id], Reason: in.Reason, Note: note})
			return err
		})
		if err != nil {
			msg := err.Error()
			res = append(res, bulkResult{ID: id, OK: false, Message: msg})
			continue
		}
		ok++
		s.notify(u, out)
		s.sendPO(u, out)
		res = append(res, bulkResult{ID: id, OK: true, Message: out.message})
	}
	return httpjson.OK(w, map[string]any{"done": ok, "failed": len(in.IDs) - ok, "results": res})
}

// sendPO emails the vendor a PO issued automatically at the approval. It runs after the save.
func (s *Service) sendPO(u *access.User, out outcome) {
	if out.autoPO != "" && s.POSent != nil {
		s.POSent(u, out.docID)
	}
}

// notify tells the requester on WhatsApp, using the Admin's message templates. It runs after the save.
func (s *Service) notify(u *access.User, out outcome) {
	if out.notify == "" || s.Messages == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		d, err := documents.Load(ctx, s.DB, out.docID, false)
		if err != nil || d.RequesterMob == "" {
			return
		}
		body, _, err := flowsettings.Template(ctx, s.DB, out.notify)
		if err != nil {
			return
		}
		text, params := flowsettings.Render(body, map[string]string{"request": d.Number, "property": d.Property, "by": u.Name, "value": money.INR(d.Total), "reason": out.reason})
		if _, err := s.Messages.WhatsApp(ctx, d.RequesterMob, "pd_"+out.notify, params, text, d.Number, &u.ID); err != nil {
			log.Printf("notify %s: %v", d.Number, err)
		}
	}()
}

// ---------- workbench ----------

// Row is one request waiting for approval, with budget flags.
type Row struct {
	*documents.Doc
	Owner       string         `json:"owner"`
	OwnerName   string         `json:"owner_name"`
	StatusLabel string         `json:"status_label"`
	Amount      float64        `json:"amount"`
	OverMonth   float64        `json:"over_month"`
	OverWeek    float64        `json:"over_week"`
	Flagged     bool           `json:"flagged"`
	Budget      []budgets.Flag `json:"budget"`
	BillFlags   []string       `json:"bill_flags"`
	PrevBill    *float64       `json:"previous_bill"`
	AgeDays     int            `json:"age_days"`
}

// BillFlags: a utility bill more than the set % above the previous bill of the same type and property,
// and an AMC ending within the warning period.
func BillFlags(ctx context.Context, q database.Q, st flowsettings.Settings, d *documents.Doc) ([]string, *float64) {
	flags := []string{}
	if d.Kind != "bill" || d.BillTypeID == nil {
		return flags, nil
	}
	var prev *float64
	_ = q.QueryRow(ctx, `SELECT i.amount FROM document x JOIN invoice i ON i.document_id=x.id AND i.status<>'VOID'
		WHERE x.kind='bill' AND x.id<>$1 AND x.property_id=$2 AND x.bill_type_id=$3 AND x.status NOT IN ('REJECTED','CANCELLED')
		AND x.period_start < to_date($4,'YYYY-MM') ORDER BY x.period_start DESC, x.id DESC LIMIT 1`, d.ID, d.PropertyID, *d.BillTypeID, d.PeriodStart).Scan(&prev)
	if prev != nil && *prev > 0 && d.BillSection == "utility" {
		if ch := (d.Total - *prev) / *prev; ch > st.BillJumpPct/100 {
			flags = append(flags, fmt.Sprintf("Up %.0f%% on the previous bill of %s", ch*100, money.INR(*prev)))
		}
	}
	if d.BillSection == "amc" && d.PeriodStart != "" {
		if start, ok := clock.ParseMonth(d.PeriodStart); ok {
			end := start.AddDate(0, d.Months, 0)
			if days := int(end.Sub(clock.Today()).Hours() / 24); days >= 0 && days <= st.AMCWarnDays {
				flags = append(flags, fmt.Sprintf("AMC ends within %d days", days))
			}
		}
	}
	return flags, prev
}

// Enrich adds amount, budget flags and bill flags to documents.
func Enrich(ctx context.Context, q database.Q, st flowsettings.Settings, docs []*documents.Doc) ([]Row, error) {
	cache := budgets.NewCache(q)
	out := make([]Row, 0, len(docs))
	for _, d := range docs {
		row := Row{Doc: d, Owner: d.Owner(), StatusLabel: d.StatusLabel(), Amount: d.Total, Budget: []budgets.Flag{}, AgeDays: int(time.Since(d.CreatedAt).Hours() / 24)}
		row.OwnerName = documents.StepNames[row.Owner]
		if d.IsBill() {
			row.BillFlags, row.PrevBill = BillFlags(ctx, q, st, d)
			row.Flagged = len(row.BillFlags) > 0
		} else if !d.NoBudget() && d.Status == documents.Pending {
			m, err := cache.Get(ctx, d.PropertyID)
			if err != nil {
				return nil, err
			}
			row.Budget, row.OverMonth, row.OverWeek = budgets.Check(m, d.SubCatTotals(), st.WeekFlag)
			row.Flagged = row.OverMonth > 0 || row.OverWeek > 0
		}
		if row.BillFlags == nil {
			row.BillFlags = []string{}
		}
		out = append(out, row)
	}
	return out, nil
}

// approveScreen is the screen whose edit level approving d needs: Corporate for corporate documents, else Approvals.
func approveScreen(d *documents.Doc) string {
	if d.IsCorp() {
		return "corporate"
	}
	return "approvals"
}

func (s *Service) workbench(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	all, ids, err := s.Guard.Visible(r.Context(), u)
	if err != nil {
		return err
	}
	docs, err := documents.LoadMany(r.Context(), s.DB, `WHERE d.status='PENDING' AND ($1 OR d.property_id = ANY($2) OR $3 IN (d.corp_l1_user_id, d.corp_l2_user_id, d.corp_backup_user_id)) ORDER BY d.created_at`, all, ids, u.ID)
	if err != nil {
		return err
	}
	mine := []*documents.Doc{}
	for _, d := range docs {
		if documents.CanAct(u, d, d.Owner(), access.Approve) != nil && u.Level(approveScreen(d), d.PropertyID, d.ClusterID) >= screens.Edit {
			mine = append(mine, d)
		}
	}
	st, err := flowsettings.Load(r.Context(), s.DB)
	if err != nil {
		return err
	}
	rows, err := Enrich(r.Context(), s.DB, st, mine)
	if err != nil {
		return err
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Amount > rows[j].Amount })
	return httpjson.OK(w, rows)
}

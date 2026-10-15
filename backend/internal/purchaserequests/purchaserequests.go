// Package purchaserequests raises purchase requests and shows them.
// The requester picks items, never the vendor: each item's vendor and rate come from the vendor mapping,
// and a request with items from several vendors is split into one request per vendor.
package purchaserequests

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
	"purchasedesk/internal/approvals"
	"purchasedesk/internal/auditlog"
	"purchasedesk/internal/balancereject"
	"purchasedesk/internal/budgets"
	"purchasedesk/internal/clock"
	"purchasedesk/internal/documents"
	"purchasedesk/internal/flowsettings"
	"purchasedesk/internal/httpjson"
	"purchasedesk/internal/messaging"
	"purchasedesk/internal/money"
	"purchasedesk/internal/purchaseorders"
	"purchasedesk/internal/screens"
	"purchasedesk/internal/textclean"
	"purchasedesk/internal/vendormapping"
	"purchasedesk/internal/vendorportal"
)

type Service struct {
	DB       *pgxpool.Pool
	Guard    *access.Guard
	Messages *messaging.Sender
}

func (s *Service) Routes(r chi.Router) {
	r.Get("/requests", httpjson.Wrap(s.list))
	r.Get("/requests/form", httpjson.Wrap(s.form))
	r.Post("/requests", httpjson.Wrap(s.create))
	r.Put("/requests/{id}", httpjson.Wrap(s.edit))
	r.Get("/documents/{id}", httpjson.Wrap(s.detail))
	r.Post("/documents/{id}/resubmit", httpjson.Wrap(s.resubmit))
	r.Post("/documents/{id}/cancel", httpjson.Wrap(s.cancel))
	r.Post("/documents/{id}/preop-approver", httpjson.Wrap(s.setPreop))
	r.Get("/search", httpjson.Wrap(s.search))
}

// raiseGrant returns the role a user raises with at a property, or nil.
func raiseGrant(u *access.User, propKind string, propID, clusterID int64, cats []int64) *access.Grant {
	if u.IsAdmin {
		return &access.Grant{Name: "System Admin", Step: "admin"}
	}
	for _, step := range []string{"whs", "wh", "staff", "l1", "chef", "purchase", "creq", "dh"} {
		g := u.Can(step, access.Raise, propID, clusterID, cats)
		if g == nil {
			continue
		}
		// The warehouse raises through its Warehouse Staff (the Warehouse Manager approves) and the Warehouse Manager, who
		// buys what the warehouse is short of (his own goes to the Cost Controller). A pre-opening property is open to the
		// Warehouse Manager and to anyone whose role is assigned to it. Live properties are raised for by property staff,
		// managers, property chefs and Purchase.
		switch propKind {
		case "warehouse":
			if step != "whs" && step != "wh" {
				continue
			}
		case "preopening":
			if step == "whs" {
				continue
			}
		default:
			if step == "wh" || step == "whs" {
				continue
			}
		}
		return g
	}
	return nil
}

type propInfo struct {
	ID, ClusterID int64
	Name, Kind    string
	Active        bool
	Approved      bool // by Finance (a new property)
}

func (s *Service) property(ctx context.Context, id int64) (propInfo, error) {
	var p propInfo
	err := s.DB.QueryRow(ctx, `SELECT id, cluster_id, name, kind, active, fin_status='approved' FROM property WHERE id=$1`, id).Scan(&p.ID, &p.ClusterID, &p.Name, &p.Kind, &p.Active, &p.Approved)
	if err == nil && !p.Approved {
		return p, httpjson.Bad("%s waits for Finance to approve it. Requests can be raised there once it is approved.", p.Name)
	}
	if err != nil {
		return p, httpjson.Bad("Choose a property.")
	}
	return p, nil
}

type person struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

func (s *Service) preopMembers(ctx context.Context) ([]person, error) {
	rows, err := s.DB.Query(ctx, `SELECT DISTINCT u.id, u.name FROM app_user u JOIN user_role ur ON ur.user_id=u.id JOIN role r ON r.id=ur.role_id
		WHERE u.active AND r.active AND r.step='preop' ORDER BY u.name`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (person, error) {
		var p person
		return p, row.Scan(&p.ID, &p.Name)
	})
}

// form returns what the request form needs for a property: orderable items, budget left, approvers.
func (s *Service) form(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	pid := int64(httpjson.QInt(r, "property_id", 0))
	p, err := s.property(r.Context(), pid)
	if err != nil {
		return err
	}
	g := raiseGrant(u, p.Kind, p.ID, p.ClusterID, nil)
	if g == nil {
		return httpjson.Forbidden("You cannot raise requests for %s.", p.Name)
	}
	items, err := vendormapping.ForProperty(r.Context(), s.DB, p.ID)
	if err != nil {
		return err
	}
	allowed := items[:0]
	for _, it := range items {
		if len(g.CategoryIDs) == 0 || textclean.Contains(g.CategoryIDs, it.CategoryID) {
			allowed = append(allowed, it)
		}
	}
	type budgetLine struct {
		SubCategoryID int64   `json:"sub_category_id"`
		SubCategory   string  `json:"sub_category"`
		Category      string  `json:"category"`
		MonthBudget   float64 `json:"month_budget"`
		MonthSpent    float64 `json:"month_spent"`
		WeekBudget    float64 `json:"week_budget"`
		WeekSpent     float64 `json:"week_spent"`
	}
	bud := []budgetLine{}
	if p.Kind == "live" {
		m, err := budgets.LoadMonth(r.Context(), s.DB, p.ID, clock.Today())
		if err != nil {
			return err
		}
		seen := map[int64]bool{}
		for _, it := range allowed {
			if seen[it.SubCatID] {
				continue
			}
			seen[it.SubCatID] = true
			wk := m.CurrentWeek()
			bud = append(bud, budgetLine{it.SubCatID, it.SubCat, it.Category, m.Budget[it.SubCatID], money.Round2(m.SpentMonth(it.SubCatID)),
				money.Round2(m.WeekBudget(it.SubCatID, wk)), money.Round2(m.WeekSpent(it.SubCatID, wk))})
		}
	}
	members, err := s.preopMembers(r.Context())
	if err != nil {
		return err
	}
	lists, err := flowsettings.PickLists(r.Context(), s.DB)
	if err != nil {
		return err
	}
	st, err := flowsettings.Load(r.Context(), s.DB)
	if err != nil {
		return err
	}
	often, last, err := s.history(r.Context(), p.ID)
	if err != nil {
		return err
	}
	return httpjson.OK(w, map[string]any{"property_id": p.ID, "property": p.Name, "property_kind": p.Kind, "items": allowed, "budget": bud,
		"preop_members": members, "event_reasons": lists["event_reasons"], "settings": st, "often": often, "last": last})
}

type lastLine struct {
	SKUID int64   `json:"sku_id"`
	Qty   float64 `json:"qty"`
}

// lastOrder is the latest submission at a property (every request split off it), for "Repeat last order".
type lastOrder struct {
	Numbers []string   `json:"numbers"`
	Date    time.Time  `json:"date"`
	Lines   []lastLine `json:"lines"`
}

// history gives the items most often ordered at a property in the last 45 days, most frequent first, and its latest
// submission (nil when there is none). Rejected and cancelled requests do not count.
func (s *Service) history(ctx context.Context, propertyID int64) ([]int64, *lastOrder, error) {
	rows, err := s.DB.Query(ctx, `SELECT l.sku_id FROM document_line l JOIN document d ON d.id=l.document_id
		WHERE d.kind='purchase' AND d.property_id=$1 AND l.sku_id IS NOT NULL AND d.status NOT IN ('REJECTED','CANCELLED') AND d.created_at > now() - interval '45 days'
		GROUP BY l.sku_id ORDER BY count(*) DESC, max(d.created_at) DESC LIMIT 24`, propertyID)
	if err != nil {
		return nil, nil, err
	}
	often, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return nil, nil, err
	}
	var last lastOrder
	err = s.DB.QueryRow(ctx, `SELECT array_agg(d.number ORDER BY d.number), max(d.created_at) FROM document d
		WHERE d.kind='purchase' AND d.property_id=$1 AND d.status NOT IN ('REJECTED','CANCELLED') AND d.parent_id IS NULL
		AND d.created_at=(SELECT max(created_at) FROM document WHERE kind='purchase' AND property_id=$1 AND parent_id IS NULL AND status NOT IN ('REJECTED','CANCELLED'))`, propertyID).
		Scan(&last.Numbers, &last.Date)
	if err != nil || len(last.Numbers) == 0 {
		return often, nil, nil
	}
	rows, err = s.DB.Query(ctx, `SELECT l.sku_id, sum(coalesce(l.original_qty, l.qty))::float8 FROM document_line l JOIN document d ON d.id=l.document_id
		WHERE d.number = ANY($1) AND l.sku_id IS NOT NULL GROUP BY l.sku_id ORDER BY min(l.id)`, last.Numbers)
	if err != nil {
		return nil, nil, err
	}
	last.Lines, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (lastLine, error) {
		var x lastLine
		return x, row.Scan(&x.SKUID, &x.Qty)
	})
	if err != nil {
		return nil, nil, err
	}
	return often, &last, nil
}

type lineIn struct {
	SKUID    int64   `json:"sku_id"`
	Qty      float64 `json:"qty"`
	EstRate  float64 `json:"est_rate"`
	VendorID int64   `json:"vendor_id"` // the vendor picked for an item not for daily consumption; 0 = its default
}

type createIn struct {
	PropertyID  int64    `json:"property_id"`
	Lines       []lineIn `json:"lines"`
	EventReason string   `json:"event_reason"`
	EventNote   string   `json:"event_note"`
	PreopUserID int64    `json:"preop_user_id"`
}

type newLine struct {
	sku               int64
	name              string
	qty, rate, gst    float64
	est               bool
	categoryID        int64
	vendorID          int64
	vendorIsWarehouse bool
}

// prepared is a checked request: its property, the role it is raised with, and its lines grouped by vendor.
type prepared struct {
	p        propInfo
	g        *access.Grant
	event    bool
	preop    any
	vendors  []int64 // in the order the items were added
	byVendor map[int64][]newLine
}

// prepare checks a new or edited request the same way: the property, each item's vendor and rate from the mapping,
// the raise right, the event reason, and the pre-opening approver.
func (s *Service) prepare(ctx context.Context, u *access.User, in createIn) (*prepared, error) {
	p, err := s.property(ctx, in.PropertyID)
	if err != nil {
		return nil, err
	}
	if !p.Active {
		return nil, httpjson.Bad("%s is inactive. No new request can be raised for it.", p.Name)
	}
	if u.Level("requests", p.ID, p.ClusterID) < screens.Edit {
		return nil, httpjson.Forbidden("Your role can only view Purchase Requests at %s, not raise them.", p.Name)
	}
	if len(in.Lines) == 0 {
		return nil, httpjson.Bad("Add at least one item.")
	}
	// The same item twice from the same vendor is one line; from two vendors (an item not for daily consumption) two.
	type lineKey struct{ sku, vendor int64 }
	merged := map[lineKey]*lineIn{}
	order := []lineKey{}
	for i := range in.Lines {
		l := in.Lines[i]
		k := lineKey{l.SKUID, l.VendorID}
		if x, ok := merged[k]; ok {
			x.Qty += l.Qty
			continue
		}
		merged[k] = &l
		order = append(order, k)
	}
	errs := []string{}
	lines := []newLine{}
	cats := []int64{}
	for _, k := range order {
		l := merged[k]
		src, ok, err := vendormapping.One(ctx, s.DB, l.SKUID, p.ID, l.VendorID)
		if err != nil {
			return nil, err
		}
		if !ok {
			errs = append(errs, fmt.Sprintf("Item %d is not available at %s.", l.SKUID, p.Name))
			continue
		}
		if l.VendorID != 0 && (src.VendorID == nil || *src.VendorID != l.VendorID) {
			if src.DailyUse {
				errs = append(errs, fmt.Sprintf("%s is a daily consumption item: it comes only from %s at %s.", src.ItemName, src.VendorName, p.Name))
			} else {
				errs = append(errs, fmt.Sprintf("The vendor picked for %s does not supply it at %s. Pick one from the list.", src.ItemName, p.Name))
			}
			continue
		}
		if !src.Orderable {
			errs = append(errs, fmt.Sprintf("%s %s cannot be ordered here: %s.", src.SKUCode, src.ItemName, strings.ToLower(src.Problem)))
			continue
		}
		if l.Qty <= 0 {
			errs = append(errs, fmt.Sprintf("Enter a quantity for %s.", src.ItemName))
			continue
		}
		nl := newLine{sku: l.SKUID, name: src.ItemName, qty: l.Qty, categoryID: src.CategoryID, vendorID: *src.VendorID, vendorIsWarehouse: src.IsWarehouse}
		if src.Rate != nil {
			nl.rate, nl.gst = *src.Rate, *src.RateGST
		} else {
			if l.EstRate <= 0 {
				errs = append(errs, fmt.Sprintf("Enter an estimated rate for %s. %s has no rate for it yet.", src.ItemName, src.VendorName))
				continue
			}
			nl.rate, nl.gst, nl.est = money.Round2(l.EstRate), src.SKUGST, true
		}
		lines = append(lines, nl)
		cats = append(cats, src.CategoryID)
	}
	if len(errs) > 0 {
		return nil, httpjson.Invalid(errs)
	}
	g := raiseGrant(u, p.Kind, p.ID, p.ClusterID, textclean.Uniq(cats))
	if g == nil {
		return nil, httpjson.Forbidden("You cannot raise requests for these items at %s.", p.Name)
	}
	event := textclean.Clean(in.EventReason) != ""
	if event {
		if p.Kind != "live" {
			return nil, httpjson.Bad("Event requests are for live properties.")
		}
		if err := flowsettings.CheckReason(ctx, s.DB, "event_reasons", in.EventReason, in.EventNote); err != nil {
			return nil, err
		}
		if len(strings.TrimSpace(in.EventNote)) < 5 {
			return nil, httpjson.Bad("Add the event details so the Cluster Manager can decide.")
		}
	}
	var preop any
	if p.Kind == "preopening" { // a warehouse request goes to the Warehouse Manager instead
		members, err := s.preopMembers(ctx)
		if err != nil {
			return nil, err
		}
		ok := false
		for _, m := range members {
			ok = ok || m.ID == in.PreopUserID
		}
		if !ok {
			return nil, httpjson.Bad("Choose the Pre-opening Team member who approves this.")
		}
		preop = in.PreopUserID
	}
	out := &prepared{p: p, g: g, event: event, preop: preop, byVendor: map[int64][]newLine{}}
	for _, l := range lines {
		if _, ok := out.byVendor[l.vendorID]; !ok {
			out.vendors = append(out.vendors, l.vendorID)
		}
		out.byVendor[l.vendorID] = append(out.byVendor[l.vendorID], l)
	}
	return out, nil
}

// steps is the approval route for one vendor's lines.
func (pr *prepared) steps(st flowsettings.Settings, ls []newLine) []string {
	amount := 0.0
	for _, l := range ls {
		amount += money.LineValue(l.qty, l.rate, l.gst)
	}
	return approvals.Chain(st, pr.p.Kind, ls[0].vendorIsWarehouse, pr.event, amount, pr.g.Step)
}

func insertLines(ctx context.Context, tx pgx.Tx, docID int64, ls []newLine) error {
	for _, l := range ls {
		if _, err := tx.Exec(ctx, `INSERT INTO document_line(document_id,sku_id,qty,rate,gst_pct,estimated,tds_section,tds_pct)
			SELECT $1,$2,$3,$4,$5,$6,k.tds_section,k.tds_pct FROM sku k WHERE k.id=$2`, docID, l.sku, l.qty, l.rate, l.gst, l.est); err != nil {
			return err
		}
	}
	return nil
}

// insertPR saves one vendor's request as num. createdAt ties requests from one submission together (see raised_with).
func (s *Service) insertPR(ctx context.Context, tx pgx.Tx, u *access.User, pr *prepared, in createIn, num string, steps []string, ls []newLine, createdAt time.Time, action string) (int64, string, error) {
	var id int64
	if err := tx.QueryRow(ctx, `INSERT INTO document(number,kind,property_id,vendor_id,requester_id,requester_step,status,route_steps,step_idx,event_reason,event_note,preop_user_id,created_by,created_at)
		VALUES ($1,'purchase',$2,$3,$4,$5,'PENDING',$6,0,$7,$8,$9,$4,$10) RETURNING id`,
		num, pr.p.ID, ls[0].vendorID, u.ID, pr.g.Step, steps, textclean.Clean(in.EventReason), strings.TrimSpace(in.EventNote), pr.preop, createdAt).Scan(&id); err != nil {
		return 0, "", err
	}
	if err := insertLines(ctx, tx, id, ls); err != nil {
		return 0, "", err
	}
	if err := auditlog.Doc(ctx, tx, u, pr.g.Name, id, num, "Purchases", action, ""); err != nil {
		return 0, "", err
	}
	return id, num, finalizeIfNoSteps(ctx, tx, u, id, num, steps)
}

// finalizeIfNoSteps approves at once a request that needs no approval (an order on the warehouse, by default).
func finalizeIfNoSteps(ctx context.Context, tx pgx.Tx, u *access.User, id int64, num string, steps []string) error {
	if len(steps) > 0 {
		return nil
	}
	d, err := documents.Load(ctx, tx, id, true)
	if err != nil {
		return err
	}
	if err := auditlog.Doc(ctx, tx, u, "System", id, num, "Purchases", "Approved", "No approval needed under the current flow settings"); err != nil {
		return err
	}
	_, err = approvals.Finalize(ctx, tx, u, "System", d, purchaseorders.AtApproval{})
	return err
}

func (s *Service) create(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	ctx := r.Context()
	var in createIn
	if err := httpjson.Decode(r, &in); err != nil {
		return err
	}
	pr, err := s.prepare(ctx, u, in)
	if err != nil {
		return err
	}
	st, err := flowsettings.Load(ctx, s.DB)
	if err != nil {
		return err
	}
	created := []string{}
	ids := []int64{}
	err = pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		var now time.Time
		if err := tx.QueryRow(ctx, `SELECT now()`).Scan(&now); err != nil {
			return err
		}
		nums, err := documents.RequestNumbers(ctx, tx, pr.p.ID, len(pr.vendors))
		if err != nil {
			return err
		}
		for i, v := range pr.vendors {
			ls := pr.byVendor[v]
			id, num, err := s.insertPR(ctx, tx, u, pr, in, nums[i], pr.steps(st, ls), ls, now, "Request raised")
			if err != nil {
				return err
			}
			created = append(created, num)
			ids = append(ids, id)
		}
		return nil
	})
	if err != nil {
		return err
	}
	msg := created[0] + " sent for approval"
	if len(created) > 1 {
		msg = fmt.Sprintf("%d requests sent, one per vendor: %s", len(created), strings.Join(created, ", "))
	}
	return httpjson.OK(w, map[string]any{"numbers": created, "ids": ids, "message": msg})
}

// onScreens keeps only the actions whose screen the user may edit at the document's property: screen levels win
// over rights, so a button never shows for a step the server would refuse (see screenguard).
func onScreens(u *access.User, d *documents.Doc, actions []string) []string {
	corp, bill := d.IsCorp(), d.Kind == "bill"
	billScreen := "bills_utility"
	if d.BillSection == "amc" {
		billScreen = "bills_amc"
	}
	pick := func(purchase, corpScreen, billSc string) []string {
		switch {
		case corp:
			return []string{corpScreen}
		case bill:
			return []string{billSc}
		}
		return []string{purchase}
	}
	need := map[string][]string{
		"approve": pick("approvals", "corporate", "approvals"), "return": pick("approvals", "corporate", "approvals"),
		"reject": pick("approvals", "corporate", "approvals"), "route": {"approvals"}, "preop_approver": {"requests"},
		"resubmit": pick("requests", "corporate", billScreen), "cancel": pick("requests", "corporate", billScreen),
		"issue_po": {"purchase_orders"}, "vendor_confirm": {"purchase_orders", "approvals", "corporate"}, "dispatch": {"warehouse"}, "receive": pick("goods_receipts", "corporate", "goods_receipts"),
		"reject_balance": {"goods_receipts", "purchase_orders"}, "receive_transfer": {"goods_receipts"}, "edit": {"requests"},
	}
	if corp {
		need["reject_balance"] = []string{"corporate", "purchase_orders"}
	}
	out := []string{}
	for _, a := range actions {
		for _, sc := range need[a] {
			if u.Level(sc, d.PropertyID, d.ClusterID) >= screens.Edit {
				out = append(out, a)
				break
			}
		}
	}
	return out
}

// Editable reports whether the requester can still change everything on a purchase request: nobody has approved it
// yet (pending at its first step), or it was returned for changes. Only the person who raised it may.
func Editable(u *access.User, d *documents.Doc) bool {
	if d.Kind != "purchase" || d.IsTransfer || u.ID != d.RequesterID {
		return false
	}
	return (d.Status == documents.Pending && d.StepIdx == 0) || d.Status == documents.Returned
}

// edit replaces a request's property, items, quantities and event details before anyone approves it, or when it is
// returned. It goes back to the start of its (recalculated) route. If the new items come from several vendors, this
// request keeps one vendor's items and the rest become new requests from the same submission.
func (s *Service) edit(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	ctx := r.Context()
	id, err := httpjson.ID(r, "id")
	if err != nil {
		return err
	}
	var in createIn
	if err := httpjson.Decode(r, &in); err != nil {
		return err
	}
	pr, err := s.prepare(ctx, u, in)
	if err != nil {
		return err
	}
	st, err := flowsettings.Load(ctx, s.DB)
	if err != nil {
		return err
	}
	var msg, changes string
	var notifyID int64
	err = pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		d, err := documents.Load(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if d.RequesterID != u.ID {
			return httpjson.Forbidden("Only %s, who raised %s, can edit it.", d.Requester, d.Number)
		}
		if !Editable(u, d) {
			return httpjson.Conflict("%s can no longer be edited: an approver has already acted on it.", d.Number)
		}
		keep := pr.vendors[0]
		for _, v := range pr.vendors {
			if v == d.VendorID {
				keep = v
			}
		}
		ls := pr.byVendor[keep]
		steps := pr.steps(st, ls)
		changes = describeEdit(d, pr)
		number, splitNums, err := editNumbers(ctx, tx, d, pr.p.ID, len(pr.vendors)-1)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM document_line WHERE document_id=$1`, d.ID); err != nil {
			return err
		}
		if err := insertLines(ctx, tx, d.ID, ls); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE document SET property_id=$2, vendor_id=$3, requester_step=$4, status='PENDING', route_steps=$5, step_idx=0,
			event_reason=$6, event_note=$7, preop_user_id=$8, routed_reason='', routed_note='', cm_approved_at=NULL,
			modified_note='', modified_by='', modified_at=NULL, number=$9, updated_at=now() WHERE id=$1`,
			d.ID, pr.p.ID, keep, pr.g.Step, steps, textclean.Clean(in.EventReason), strings.TrimSpace(in.EventNote), pr.preop, number); err != nil {
			return err
		}
		if number != d.Number {
			why := "moved to " + pr.p.Name
			if pr.p.ID == d.PropertyID {
				why = "split into one request per vendor"
			}
			if err := auditlog.Doc(ctx, tx, u, "Requester", d.ID, number, "Purchases", "Renumbered from "+d.Number+" to "+number, why); err != nil {
				return err
			}
		}
		action := "Edited before approval"
		if d.Status == documents.Returned {
			action = "Changed and resubmitted"
		}
		if err := auditlog.Doc(ctx, tx, u, "Requester", d.ID, number, "Purchases", action, changes); err != nil {
			return err
		}
		if err := finalizeIfNoSteps(ctx, tx, u, d.ID, number, steps); err != nil {
			return err
		}
		msg = number + " updated and sent for approval again"
		if number != d.Number {
			msg = d.Number + " is now " + number + ", updated and sent for approval again"
		}
		if len(steps) > 0 {
			notifyID = d.ID
		}
		split := []string{}
		for _, v := range pr.vendors {
			if v == keep {
				continue
			}
			ls := pr.byVendor[v]
			_, num, err := s.insertPR(ctx, tx, u, pr, in, splitNums[len(split)], pr.steps(st, ls), ls, d.CreatedAt, "Request raised (split from "+number+" when it was edited)")
			if err != nil {
				return err
			}
			split = append(split, num)
		}
		if len(split) > 0 {
			msg += fmt.Sprintf(". Items from other vendors went on new requests: %s", strings.Join(split, ", "))
			return auditlog.Doc(ctx, tx, u, "Requester", d.ID, number, "Purchases", "Items from other vendors moved to "+strings.Join(split, ", "), "")
		}
		return nil
	})
	if err != nil {
		return err
	}
	if notifyID != 0 {
		s.notifyApprovers(u, notifyID, changes)
	}
	return httpjson.OK(w, map[string]any{"id": id, "message": msg})
}

// editNumbers gives an edited request its number and the numbers for the extra requests it splits into (one per
// further vendor). A request keeps its number unless it moves to another property (a fresh number there) or now
// splits: PR_HZ_0001 becomes PR_HZ_0001_01 with new PR_HZ_0001_02, ..., and PR_HZ_0001_01 keeps its number while the
// new ones follow the highest suffix already used. An older number (PR-1001) that splits takes a fresh number.
func editNumbers(ctx context.Context, tx pgx.Tx, d *documents.Doc, propertyID int64, extra int) (string, []string, error) {
	if propertyID != d.PropertyID || (extra > 0 && !documents.IsRequestNumber(d.Number)) {
		nums, err := documents.RequestNumbers(ctx, tx, propertyID, extra+1)
		if err != nil {
			return "", nil, err
		}
		return nums[0], nums[1:], nil
	}
	if extra == 0 {
		return d.Number, nil, nil
	}
	if _, err := tx.Exec(ctx, `SELECT 1 FROM property WHERE id=$1 FOR UPDATE`, propertyID); err != nil {
		return "", nil, err
	}
	base, suffix := documents.RequestBase(d.Number)
	if suffix > 0 {
		nums, err := documents.NextSplits(ctx, tx, base, extra)
		return d.Number, nums, err
	}
	nums, err := documents.NextSplits(ctx, tx, base, extra+1)
	if err != nil {
		return "", nil, err
	}
	return nums[0], nums[1:], nil
}

// describeEdit lists what changed, for the history and the approver: property, items added, removed or changed, event.
func describeEdit(d *documents.Doc, pr *prepared) string {
	out := []string{}
	if pr.p.ID != d.PropertyID {
		out = append(out, "property "+d.Property+" to "+pr.p.Name)
	}
	before := map[int64]documents.Line{}
	for _, l := range d.Lines {
		if l.Qty > 0 {
			before[l.SKUID] = l
		}
	}
	after := map[int64]bool{}
	for _, v := range pr.vendors {
		for _, l := range pr.byVendor[v] {
			after[l.sku] = true
			old, ok := before[l.sku]
			switch {
			case !ok:
				out = append(out, fmt.Sprintf("%s added (%v)", l.name, l.qty))
			case old.Qty != l.qty:
				out = append(out, fmt.Sprintf("%s: %v to %v", l.name, old.Qty, l.qty))
			}
		}
	}
	for _, l := range d.Lines {
		if l.Qty > 0 && !after[l.SKUID] {
			out = append(out, l.ItemName+" removed")
		}
	}
	if (d.EventReason != "") != pr.event {
		if pr.event {
			out = append(out, "marked as an event order")
		} else {
			out = append(out, "no longer an event order")
		}
	}
	if len(out) == 0 {
		return "No item changes"
	}
	return strings.Join(out, "; ")
}

// notifyApprovers emails the people who hold the request's first approval step that it changed. It runs after the save.
func (s *Service) notifyApprovers(u *access.User, docID int64, changes string) {
	if s.Messages == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		d, err := documents.Load(ctx, s.DB, docID, false)
		if err != nil || d.Status != documents.Pending {
			return
		}
		body, _, err := flowsettings.Template(ctx, s.DB, "edit")
		if err != nil {
			return
		}
		text := flowsettings.Fill(body, map[string]string{"request": d.Number, "property": d.Property, "by": u.Name, "changes": changes, "value": money.INR(d.Total)})
		var only int64
		if d.Owner() == "preop" && d.PreopUserID != nil {
			only = *d.PreopUserID
		}
		rows, err := s.DB.Query(ctx, `SELECT DISTINCT u.email FROM app_user u JOIN user_role ur ON ur.user_id=u.id JOIN role r ON r.id=ur.role_id
			WHERE u.active AND r.active AND r.step=$1 AND $2=ANY(r.rights) AND coalesce(u.email,'')<>'' AND ($5=0 OR u.id=$5)
			AND (ur.scope='all' OR (ur.scope='clusters' AND $3::bigint=ANY(ur.cluster_ids)) OR (ur.scope='properties' AND $4::bigint=ANY(ur.property_ids)))`,
			d.Owner(), access.Approve, d.ClusterID, d.PropertyID, only)
		if err != nil {
			return
		}
		emails, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return
		}
		for _, to := range emails {
			if _, err := s.Messages.Email(ctx, to, d.Number+" changed by the requester · "+d.Property, text, nil, d.Number, &u.ID); err != nil {
				log.Printf("edit notify %s: %v", d.Number, err)
			}
		}
	}()
}

// ListRow is a document in a list.
type ListRow struct {
	ID           int64     `json:"id"`
	Number       string    `json:"number"`
	Kind         string    `json:"kind"`
	PONumber     string    `json:"po_number"`
	CreatedAt    time.Time `json:"created_at"`
	PropertyID   int64     `json:"property_id"`
	Property     string    `json:"property"`
	ClusterID    int64     `json:"cluster_id"`
	Cluster      string    `json:"cluster"`
	City         string    `json:"city"`
	Category     string    `json:"category"`
	Items        string    `json:"items"`
	Vendor       string    `json:"vendor"`
	VendorMSME   bool      `json:"vendor_msme"`
	Requester    string    `json:"requester"`
	RequesterID  int64     `json:"requester_id"`
	Editable     bool      `json:"editable"`
	Amount       float64   `json:"amount"`
	Status       string    `json:"status"`
	StatusLabel  string    `json:"status_label"`
	Owner        string    `json:"owner"`
	Modified     bool      `json:"modified"`
	Event        bool      `json:"event"`
	DeliverBy    string    `json:"deliver_by"`
	Terms        string    `json:"payment_terms"`
	IsTransfer   bool      `json:"is_transfer"`
	BillType     string    `json:"bill_type"`
	BillSection  string    `json:"bill_section"`
	PeriodStart  string    `json:"period_start"`
	Months       int       `json:"months"`
	PropertyKind string    `json:"property_kind"`
	ClosedShort  bool      `json:"po_closed_short"`
	Received     float64   `json:"received_value"`
	MyTurn       bool      `json:"my_turn"`
	Department   string    `json:"department"`
}

// MyTurn reports whether the next action on a document is the user's.
func MyTurn(u *access.User, d *documents.Doc) bool {
	owner := d.Owner()
	if owner == "" {
		return false
	}
	right := map[string]string{documents.Pending: access.Approve, documents.Approved: access.IssuePO, documents.POIssued: access.Receive, documents.WHSent: access.Receive}[d.Status]
	if d.Status == documents.WHSent {
		return documents.TransferReceiver(u, d) != nil
	}
	if d.Status == documents.POIssued && !d.IsTransfer {
		return documents.Receiver(u, d) != nil
	}
	return documents.CanAct(u, d, owner, right) != nil
}

func summarize(d *documents.Doc) string {
	if d.Kind == "bill" {
		return d.BillType
	}
	if d.Kind == "corp_bill" {
		return d.Head
	}
	live := []documents.Line{}
	for _, l := range d.Lines {
		if l.Qty > 0 {
			live = append(live, l)
		}
	}
	if len(live) == 0 {
		return ""
	}
	t := fmt.Sprintf("%s × %v", live[0].ItemName, live[0].Qty)
	if len(live) > 1 {
		t += fmt.Sprintf(" +%d more", len(live)-1)
	}
	return t
}

func ToRow(d *documents.Doc) ListRow {
	cat := ""
	if len(d.Lines) > 0 {
		cat = d.Lines[0].Category
	} else if d.Kind == "bill" {
		cat = "Utilities & AMC"
	} else if d.IsCorp() {
		cat = "Corporate"
	}
	rec := 0.0
	for _, l := range d.Lines {
		rec += money.LineValue(l.ReceivedQty, l.Rate, l.GST)
	}
	return ListRow{ID: d.ID, Number: d.Number, Kind: d.Kind, PONumber: d.PONumber, CreatedAt: d.CreatedAt, PropertyID: d.PropertyID, Property: d.Property,
		ClusterID: d.ClusterID, Cluster: d.Cluster, City: d.City, Category: cat, Items: summarize(d), Vendor: d.Vendor, VendorMSME: d.VendorMSME, Requester: d.Requester, RequesterID: d.RequesterID,
		Amount: d.Total, Status: d.Status, StatusLabel: d.StatusLabel(), Owner: d.Owner(), Modified: d.ModifiedNote != "", Event: d.EventReason != "",
		DeliverBy: d.DeliverBy, Terms: d.PaymentTerms, IsTransfer: d.IsTransfer, BillType: d.BillType, BillSection: d.BillSection, PeriodStart: d.PeriodStart,
		Months: d.Months, PropertyKind: d.PropertyKind, ClosedShort: d.ClosedShort, Received: money.Round2(rec)}
}

// list returns documents in the user's scope: every open one, plus closed ones from the last N days (default 90).
// ?kind=purchase|bill, ?section=utility|amc, ?view=po|gr for the PO and goods receipt lists.
func (s *Service) list(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	all, ids, err := s.Guard.Visible(r.Context(), u)
	if err != nil {
		return err
	}
	q := r.URL.Query()
	days := httpjson.QInt(r, "days", 90)
	where := `WHERE ($1 OR d.property_id = ANY($2) OR d.requester_id=$6 OR $6 IN (d.corp_l1_user_id, d.corp_l2_user_id, d.corp_backup_user_id)) AND ($3='' OR d.kind=$3 OR ($3='corp' AND d.kind IN ('corp_purchase','corp_bill'))) AND ($4='' OR bt.section=$4)
		AND (d.status NOT IN ('CLOSED','REJECTED','CANCELLED','WH_DONE','WH_BACK') OR d.updated_at > now() - make_interval(days => $5))`
	switch q.Get("view") {
	case "po":
		where += ` AND (d.status='APPROVED' OR d.po_number IS NOT NULL) AND d.kind IN ('purchase','corp_purchase')`
	case "gr":
		where += ` AND d.po_number IS NOT NULL AND d.kind IN ('purchase','corp_purchase')`
	}
	docs, err := documents.LoadMany(r.Context(), s.DB, where+` ORDER BY d.created_at DESC LIMIT 20000`, all, ids, q.Get("kind"), q.Get("section"), days, u.ID)
	if err != nil {
		return err
	}
	out := make([]ListRow, 0, len(docs))
	for _, d := range docs {
		row := ToRow(d)
		row.MyTurn = MyTurn(u, d)
		row.Editable = Editable(u, d)
		row.Department = d.Department
		out = append(out, row)
	}
	return httpjson.OK(w, out)
}

// stepRight is the right each flow step needs to act. Steps not listed (requester, corporate approvers fixed on the
// request) are not looked up.
var stepRight = map[string]string{"l1": access.Approve, "l2": access.Approve, "cc": access.Approve, "preop": access.Approve,
	"purchase": access.IssuePO, "store": access.Receive, "wh": ""}

// ownerPeople names the active users who can take the document's next step at its property. nil means the step is not
// looked up; an empty list means nobody holds that role there, so the document is stuck until the Admin gives someone access.
func (s *Service) ownerPeople(ctx context.Context, d *documents.Doc) ([]string, error) {
	step := d.Owner()
	if step == "requester" {
		return []string{d.Requester}, nil // the person who raised it (receives the goods, or changes a returned request)
	}
	right, ok := stepRight[step]
	if !ok {
		return nil, nil
	}
	rows, err := s.DB.Query(ctx, `SELECT DISTINCT u.name FROM app_user u JOIN user_role ur ON ur.user_id=u.id JOIN role r ON r.id=ur.role_id
		WHERE u.active AND r.active AND r.step=$1 AND ($2='' OR $2=ANY(r.rights))
		AND (ur.scope='all' OR (ur.scope='clusters' AND $3::bigint=ANY(ur.cluster_ids)) OR (ur.scope='properties' AND $4::bigint=ANY(ur.property_ids)))
		ORDER BY 1`, step, right, d.ClusterID, d.PropertyID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

type receiptOut struct {
	ID         int64     `json:"id"`
	Number     string    `json:"number"`
	DeliveryNo int       `json:"delivery_no"`
	By         string    `json:"received_by"`
	At         time.Time `json:"received_at"`
	Photos     []int64   `json:"photo_file_ids"`
	Voided     bool      `json:"voided"`
	VoidReason string    `json:"void_reason"`
	InvoiceID  *int64    `json:"invoice_id"`
	Lines      []struct {
		LineID int64   `json:"line_id"`
		Qty    float64 `json:"qty"`
	} `json:"lines"`
}

type logOut struct {
	At     time.Time `json:"at"`
	User   string    `json:"user"`
	Role   string    `json:"role"`
	Action string    `json:"action"`
	Note   string    `json:"note"`
}

func (s *Service) detail(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	id, err := httpjson.ID(r, "id")
	if err != nil {
		return err
	}
	d, err := documents.Load(r.Context(), s.DB, id, false)
	if err != nil {
		return err
	}
	assigned := d.CorpL1 != nil && *d.CorpL1 == u.ID || d.CorpL2 != nil && *d.CorpL2 == u.ID || d.CorpBackup != nil && *d.CorpBackup == u.ID
	if !s.Guard.SeesProperty(r.Context(), u, d.PropertyID) && d.RequesterID != u.ID && !assigned {
		return httpjson.Forbidden("This document is outside your access.")
	}
	st, err := flowsettings.Load(r.Context(), s.DB)
	if err != nil {
		return err
	}
	rows, err := approvals.Enrich(r.Context(), s.DB, st, []*documents.Doc{d})
	if err != nil {
		return err
	}
	lr, err := s.DB.Query(r.Context(), `SELECT at, user_name, role_name, action, note FROM document_log WHERE document_id=$1 ORDER BY at, id`, id)
	if err != nil {
		return err
	}
	logs, err := pgx.CollectRows(lr, func(row pgx.CollectableRow) (logOut, error) {
		var l logOut
		return l, row.Scan(&l.At, &l.User, &l.Role, &l.Action, &l.Note)
	})
	if err != nil {
		return err
	}
	rr, err := s.DB.Query(r.Context(), `SELECT g.id, g.number, g.delivery_no, u.name, g.received_at, g.photo_file_ids, g.voided, g.void_reason,
		(SELECT i.id FROM invoice i WHERE i.goods_receipt_id=g.id) FROM goods_receipt g JOIN app_user u ON u.id=g.received_by WHERE g.document_id=$1 ORDER BY g.id`, id)
	if err != nil {
		return err
	}
	receipts, err := pgx.CollectRows(rr, func(row pgx.CollectableRow) (receiptOut, error) {
		var x receiptOut
		return x, row.Scan(&x.ID, &x.Number, &x.DeliveryNo, &x.By, &x.At, &x.Photos, &x.Voided, &x.VoidReason, &x.InvoiceID)
	})
	if err != nil {
		return err
	}
	for i := range receipts {
		lr, err := s.DB.Query(r.Context(), `SELECT line_id, qty FROM goods_receipt_line WHERE goods_receipt_id=$1`, receipts[i].ID)
		if err != nil {
			return err
		}
		for lr.Next() {
			var x struct {
				LineID int64   `json:"line_id"`
				Qty    float64 `json:"qty"`
			}
			if err := lr.Scan(&x.LineID, &x.Qty); err != nil {
				lr.Close()
				return err
			}
			receipts[i].Lines = append(receipts[i].Lines, x)
		}
		lr.Close()
	}
	actions := []string{}
	owner := d.Owner()
	switch d.Status {
	case documents.Pending:
		if documents.CanAct(u, d, owner, access.Approve) != nil {
			actions = append(actions, "approve", "return", "reject")
			if owner == "cc" && d.Kind == "purchase" {
				actions = append(actions, "route")
			}
		}
		if d.PropertyKind == "preopening" && d.Owner() == "preop" && (u.IsAdmin || u.HasStep("wh") || u.HasStep("preop")) {
			actions = append(actions, "preop_approver")
		}
	case documents.Returned:
		if documents.CanAct(u, d, "requester", "") != nil {
			actions = append(actions, "resubmit", "cancel")
		}
	case documents.Approved:
		if (d.Kind == "purchase" || d.Kind == "corp_purchase") && d.AdvanceHold == "" && documents.CanAct(u, d, "purchase", access.IssuePO) != nil {
			actions = append(actions, "issue_po")
		}
	case documents.POIssued:
		if d.IsTransfer {
			if documents.CanAct(u, d, "wh", access.Receive) != nil {
				actions = append(actions, "dispatch")
			}
		} else if documents.Receiver(u, d) != nil {
			actions = append(actions, "receive")
		}
		if !d.IsTransfer && d.VendorStatus != "accepted" && documents.POKeeper(u, d) != nil {
			actions = append(actions, "vendor_confirm")
		}
		if balancereject.Who(u, d) != nil {
			actions = append(actions, "reject_balance")
		}
	case documents.WHSent:
		if documents.TransferReceiver(u, d) != nil {
			actions = append(actions, "receive_transfer")
		}
	}
	if Editable(u, d) {
		actions = append(actions, "edit")
	}
	actions = onScreens(u, d, actions)
	// A request is split into one PR per vendor when it is raised. The others from the same submission share its requester,
	// property and created_at (one transaction), so each PR can show where the rest of the order went.
	type sibling struct {
		ID       int64  `json:"id"`
		Number   string `json:"number"`
		PONumber string `json:"po_number"`
		Vendor   string `json:"vendor"`
		Items    int    `json:"items"`
		Status   string `json:"status"`
	}
	siblings := []sibling{}
	if d.Kind == "purchase" {
		docs, err := documents.LoadMany(r.Context(), s.DB, `WHERE d.kind='purchase' AND d.id<>$1 AND d.parent_id IS NULL AND d.requester_id=$2 AND d.property_id=$3 AND d.created_at=$4 ORDER BY d.id`,
			d.ID, d.RequesterID, d.PropertyID, d.CreatedAt)
		if err != nil {
			return err
		}
		for _, x := range docs {
			n := 0
			for _, l := range x.Lines {
				if l.Qty > 0 {
					n++
				}
			}
			siblings = append(siblings, sibling{x.ID, x.Number, x.PONumber, x.Vendor, n, x.StatusLabel()})
		}
	}
	owners, err := s.ownerPeople(r.Context(), d)
	if err != nil {
		return err
	}
	// Invoices the vendor uploaded on the vendor portal; the goods receipt starts from the newest one not yet used.
	vinv, err := vendorportal.Invoices(r.Context(), s.DB, d.ID)
	if err != nil {
		return err
	}
	// raises_po: approving now is the last step and raises the PO (no Purchase Team step), so the Approve box asks for
	// the rates of estimated lines and, on Advance terms, the advance.
	raisesPO := false
	if textclean.Contains(actions, "approve") && d.StepIdx == len(d.RouteSteps)-1 && (d.Kind == "purchase" || d.Kind == "corp_purchase") && d.VendorType != "warehouse" {
		if st, err := flowsettings.Load(r.Context(), s.DB); err == nil {
			raisesPO = st.POOnApproval
		}
	}
	return httpjson.OK(w, map[string]any{"document": rows[0], "log": logs, "receipts": receipts, "actions": actions, "raised_with": siblings, "owner_people": owners,
		"vendor_invoices": vinv, "vendor_portal": vendorportal.HasLogin(r.Context(), s.DB, d.VendorID), "raises_po": raisesPO})
}

func (s *Service) resubmit(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	id, err := httpjson.ID(r, "id")
	if err != nil {
		return err
	}
	var in struct {
		Quantities    map[int64]float64 `json:"quantities"`
		InvoiceNumber string            `json:"invoice_number"`
		InvoiceAmount float64           `json:"invoice_amount"`
	}
	if err := httpjson.Decode(r, &in); err != nil {
		return err
	}
	st, err := flowsettings.Load(r.Context(), s.DB)
	if err != nil {
		return err
	}
	var number string
	err = pgx.BeginFunc(r.Context(), s.DB, func(tx pgx.Tx) error {
		d, err := documents.Load(r.Context(), tx, id, true)
		if err != nil {
			return err
		}
		number = d.Number
		if d.Status != documents.Returned {
			return httpjson.Conflict("%s is not waiting for changes.", d.Number)
		}
		if documents.CanAct(u, d, "requester", "") == nil {
			return httpjson.Forbidden("Only the person who raised it can change and resubmit.")
		}
		steps := d.RouteSteps
		if d.IsBill() {
			no := strings.TrimSpace(in.InvoiceNumber)
			if no == "" || in.InvoiceAmount <= 0 {
				return httpjson.Bad("Enter the invoice number and amount.")
			}
			var dup bool
			_ = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM invoice WHERE vendor_id=$1 AND norm_key(invoice_number)=norm_key($2) AND status<>'VOID' AND document_id<>$3)`, d.VendorID, no, d.ID).Scan(&dup)
			if dup {
				return httpjson.Bad("This invoice number is already entered for this vendor.")
			}
			if _, err := tx.Exec(r.Context(), `UPDATE invoice SET invoice_number=$2, amount=$3 WHERE document_id=$1 AND status='AWAITING'`, d.ID, no, money.Round2(in.InvoiceAmount)); err != nil {
				return err
			}
			steps = []string{st.BillApprover}
			if d.IsCorp() {
				steps = approvals.CorpSteps(st, in.InvoiceAmount, d.RequesterID, deref(d.CorpL1))
			}
		} else {
			total := 0.0
			amount := 0.0
			for _, l := range d.Lines {
				q, ok := in.Quantities[l.ID]
				if !ok {
					q = l.Qty
				}
				if q < 0 {
					return httpjson.Bad("Quantity cannot be below 0.")
				}
				total += q
				amount += money.LineValue(q, l.Rate, l.GST)
				if _, err := tx.Exec(r.Context(), `UPDATE document_line SET qty=$2 WHERE id=$1`, l.ID, q); err != nil {
					return err
				}
			}
			if total == 0 {
				return httpjson.Bad("Enter a quantity for at least one item, or cancel the request.")
			}
			steps = approvals.Chain(st, d.PropertyKind, d.VendorType == "warehouse", d.EventReason != "", amount, d.RequesterStep)
			if d.IsCorp() {
				steps = approvals.CorpSteps(st, amount, d.RequesterID, deref(d.CorpL1))
			}
			if len(steps) == 0 {
				steps = []string{"cc"}
			}
		}
		if _, err := tx.Exec(r.Context(), `UPDATE document SET status='PENDING', route_steps=$2, step_idx=0, routed_reason='', routed_note='', cm_approved_at=NULL, updated_at=now() WHERE id=$1`, d.ID, steps); err != nil {
			return err
		}
		return auditlog.Doc(r.Context(), tx, u, "Requester", d.ID, d.Number, "Purchases", "Changed and resubmitted", "")
	})
	if err != nil {
		return err
	}
	return httpjson.OK(w, map[string]string{"message": number + " resubmitted"})
}

func deref(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

func (s *Service) cancel(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	id, err := httpjson.ID(r, "id")
	if err != nil {
		return err
	}
	var number string
	err = pgx.BeginFunc(r.Context(), s.DB, func(tx pgx.Tx) error {
		d, err := documents.Load(r.Context(), tx, id, true)
		if err != nil {
			return err
		}
		number = d.Number
		if d.Status != documents.Returned {
			return httpjson.Conflict("Only a returned request can be cancelled.")
		}
		if documents.CanAct(u, d, "requester", "") == nil {
			return httpjson.Forbidden("Only the person who raised it can cancel it.")
		}
		if _, err := tx.Exec(r.Context(), `UPDATE document SET status='CANCELLED', updated_at=now() WHERE id=$1`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(r.Context(), `UPDATE invoice SET status='VOID' WHERE document_id=$1 AND status='AWAITING'`, id); err != nil {
			return err
		}
		return auditlog.Doc(r.Context(), tx, u, "Requester", d.ID, d.Number, "Purchases", "Cancelled by requester", "")
	})
	if err != nil {
		return err
	}
	return httpjson.OK(w, map[string]string{"message": number + " cancelled"})
}

func (s *Service) setPreop(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	if !u.IsAdmin && !u.HasStep("wh") && !u.HasStep("preop") {
		return httpjson.Forbidden("The Warehouse Manager or the Pre-opening Team chooses the approver.")
	}
	id, err := httpjson.ID(r, "id")
	if err != nil {
		return err
	}
	var in struct {
		UserID int64 `json:"user_id"`
	}
	if err := httpjson.Decode(r, &in); err != nil {
		return err
	}
	members, err := s.preopMembers(r.Context())
	if err != nil {
		return err
	}
	name := ""
	for _, m := range members {
		if m.ID == in.UserID {
			name = m.Name
		}
	}
	if name == "" {
		return httpjson.Bad("Choose a member of the Pre-opening Team.")
	}
	var number string
	err = pgx.BeginFunc(r.Context(), s.DB, func(tx pgx.Tx) error {
		d, err := documents.Load(r.Context(), tx, id, true)
		if err != nil {
			return err
		}
		number = d.Number
		if d.Status != documents.Pending || d.Owner() != "preop" {
			return httpjson.Conflict("%s is not waiting for the Pre-opening Team.", d.Number)
		}
		if _, err := tx.Exec(r.Context(), `UPDATE document SET preop_user_id=$2, updated_at=now() WHERE id=$1`, id, in.UserID); err != nil {
			return err
		}
		return auditlog.Doc(r.Context(), tx, u, "", d.ID, d.Number, "Purchases", "Pre-opening approver changed", textclean.Default(d.PreopUser, "Team")+" to "+name)
	})
	if err != nil {
		return err
	}
	return httpjson.OK(w, map[string]string{"message": number + " now waits for " + name})
}

type hit struct {
	ID     int64  `json:"id"`
	Kind   string `json:"kind"`
	Label  string `json:"label"`
	Number string `json:"number"`
}

// search finds a request, PO or invoice by number, within the user's scope.
func (s *Service) search(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(q) < 2 {
		return httpjson.OK(w, []hit{})
	}
	all, ids, err := s.Guard.Visible(r.Context(), u)
	if err != nil {
		return err
	}
	// Numbers contain _ (PR_HZ_0001), a LIKE wildcard, so the text is escaped. GRN and voucher (PV_0001) numbers find
	// their request too.
	like := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q)
	rows, err := s.DB.Query(r.Context(), `SELECT DISTINCT d.id, d.kind, d.number, coalesce(d.po_number,''), coalesce(string_agg(DISTINCT i.invoice_number, ', '),'')
		FROM document d LEFT JOIN invoice i ON i.document_id=d.id LEFT JOIN goods_receipt g ON g.document_id=d.id
		WHERE ($2 OR d.property_id = ANY($3)) AND (d.number ILIKE '%'||$1||'%' OR d.po_number ILIKE '%'||$1||'%' OR i.invoice_number ILIKE '%'||$1||'%'
			OR i.voucher_number ILIKE '%'||$1||'%' OR g.number ILIKE '%'||$1||'%')
		GROUP BY d.id ORDER BY d.id DESC LIMIT 20`, like, all, ids)
	if err != nil {
		return err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (hit, error) {
		var h hit
		var po, inv string
		err := row.Scan(&h.ID, &h.Kind, &h.Number, &po, &inv)
		h.Label = h.Number
		if po != "" {
			h.Label += " · " + po
		}
		if inv != "" {
			h.Label += " · invoice " + inv
		}
		return h, err
	})
	if err != nil {
		return err
	}
	sort.SliceStable(out, func(i, j int) bool { return strings.EqualFold(out[i].Number, q) })
	return httpjson.OK(w, out)
}

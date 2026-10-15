// Package documents loads a purchase request or bill with its lines, says who owns the next step,
// and generates document numbers. Every workflow package builds on it.
package documents

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"purchasedesk/internal/access"
	"purchasedesk/internal/database"
	"purchasedesk/internal/httpjson"
	"purchasedesk/internal/money"
	"purchasedesk/internal/screens"
	"purchasedesk/internal/zohowake"
)

// Document statuses.
const (
	Pending   = "PENDING"
	Returned  = "RETURNED"
	Rejected  = "REJECTED"
	Cancelled = "CANCELLED"
	Approved  = "APPROVED"
	POIssued  = "PO_ISSUED"
	Received  = "RECEIVED" // every line received, or the PO closed short (at receipt, or by rejecting the balance)
	Closed    = "CLOSED"   // every invoice paid
	WHSent    = "WH_SENT"
	WHDone    = "WH_DONE"
	WHBack    = "WH_BACK"
)

// Invoice statuses.
const (
	InvAwaiting    = "AWAITING" // bill waiting for approval
	InvReceived    = "RECEIVED" // goods receipt saved, Accounts Payable to match
	InvMatched     = "MATCHED"
	InvPayApproved = "PAY_APPROVED"
	InvPushed      = "PUSHED"
	InvPaid        = "PAID"
	InvVoid        = "VOID"
)

type Line struct {
	ID          int64    `json:"id"`
	SKUID       int64    `json:"sku_id"`
	SKUCode     string   `json:"sku_code"`
	ItemName    string   `json:"item_name"`
	Brand       string   `json:"brand"`
	PackSize    string   `json:"pack_size"`
	HSN         string   `json:"hsn"`
	SubCatID    int64    `json:"sub_category_id"`
	SubCat      string   `json:"sub_category"`
	CategoryID  int64    `json:"category_id"`
	Category    string   `json:"category"`
	Qty         float64  `json:"qty"`
	OriginalQty *float64 `json:"original_qty"`
	Rate        float64  `json:"rate"`
	GST         float64  `json:"gst_pct"`
	Estimated   bool     `json:"estimated"`
	Dispatched  *float64 `json:"dispatched_qty"`
	ReceivedQty float64  `json:"received_qty"`
	RejectedQty float64  `json:"rejected_qty"` // undelivered balance rejected after the PO; already taken off Qty
	Value       float64  `json:"value"`
	Description string   `json:"description"`
	Unit        string   `json:"unit"`
	HeadID      *int64   `json:"expense_head_id"`
}

type Doc struct {
	ID            int64      `json:"id"`
	Number        string     `json:"number"`
	Kind          string     `json:"kind"`
	PropertyID    int64      `json:"property_id"`
	Property      string     `json:"property"`
	PropertyKind  string     `json:"property_kind"`
	PropertyState string     `json:"property_state"`
	ClusterID     int64      `json:"cluster_id"`
	Cluster       string     `json:"cluster"`
	City          string     `json:"city"`
	ClusterMgrID  *int64     `json:"-"`
	VendorID      int64      `json:"vendor_id"`
	Vendor        string     `json:"vendor"`
	VendorType    string     `json:"vendor_type"`
	VendorMSME    bool       `json:"vendor_msme"`
	VendorGST     bool       `json:"vendor_gst_registered"`
	VendorTerms   string     `json:"vendor_terms"`
	VendorEmail   string     `json:"-"`
	VendorMobile  string     `json:"-"`
	RequesterID   int64      `json:"requester_id"`
	Requester     string     `json:"requester"`
	RequesterMob  string     `json:"-"`
	RequesterStep string     `json:"requester_step"`
	Status        string     `json:"status"`
	RouteSteps    []string   `json:"route_steps"`
	StepIdx       int        `json:"step_idx"`
	EventReason   string     `json:"event_reason"`
	EventNote     string     `json:"event_note"`
	RoutedReason  string     `json:"routed_reason"`
	RoutedNote    string     `json:"routed_note"`
	CMApprovedAt  *time.Time `json:"cm_approved_at"`
	PreopUserID   *int64     `json:"preop_user_id"`
	PreopUser     string     `json:"preop_user"`
	ReturnReason  string     `json:"return_reason"`
	ReturnNote    string     `json:"return_note"`
	ModifiedNote  string     `json:"modified_note"`
	ModifiedBy    string     `json:"modified_by"`
	ModifiedAt    *time.Time `json:"modified_at"`
	ApprovedAt    *time.Time `json:"approved_at"`
	PONumber      string     `json:"po_number"`
	PODate        *time.Time `json:"po_date"`
	DeliverBy     string     `json:"deliver_by"`
	PaymentTerms  string     `json:"payment_terms"`
	ClosedShort   bool       `json:"po_closed_short"`
	AdvanceHold   string     `json:"advance_hold_po"` // set while the PO waits for Finance to approve its advance: the PO number it will get
	ZohoPOID      string     `json:"zoho_po_id"`      // the Zoho Books purchase order kept in step with this PO
	ZohoPOSync    string     `json:"zoho_po_sync"`    // what is still to send to Zoho: create, update, cancel, or "" when in step
	ZohoPOError   string     `json:"zoho_po_error"`
	VendorStatus  string     `json:"vendor_status"` // what the vendor did with the PO: "", accepted (awaiting delivery) or declined
	VendorActedAt *time.Time `json:"vendor_acted_at"`
	VendorNote    string     `json:"vendor_note"`
	DispatchDate  string     `json:"dispatch_date"`     // when the vendor says it will dispatch
	SentToVendor  *time.Time `json:"sent_to_vendor_at"` // when the PO reached the vendor (order sheet or PO email); nil = still to push
	// ReceiptByRequester (Flow Settings): goods at a live property are received by the person who raised the request,
	// so the receipt step is theirs; anyone with the Receiving / Store role there can still receive.
	ReceiptByRequester bool       `json:"-"`
	IsTransfer         bool       `json:"is_transfer"`
	DispatchedAt       *time.Time `json:"dispatched_at"`
	WaitUntil          string     `json:"wait_until"`
	WaitNote           string     `json:"wait_note"`
	BillTypeID         *int64     `json:"bill_type_id"`
	BillType           string     `json:"bill_type"`
	BillSection        string     `json:"bill_section"`
	PeriodStart        string     `json:"period_start"`
	Months             int        `json:"months"`
	Remark             string     `json:"remark"`
	CreatedAt          time.Time  `json:"created_at"`
	ParentID           *int64     `json:"parent_id"`
	TransferRecv       *time.Time `json:"transfer_received_at"`
	DepartmentID       *int64     `json:"department_id"`
	Department         string     `json:"department"`
	CorpL1             *int64     `json:"corp_l1_user_id"`
	CorpL1Name         string     `json:"corp_l1"`
	CorpL2             *int64     `json:"corp_l2_user_id"`
	CorpL2Name         string     `json:"corp_l2"`
	CorpBackup         *int64     `json:"corp_backup_user_id"`
	CorpBackupNm       string     `json:"corp_backup"`
	HeadID             *int64     `json:"expense_head_id"`
	Head               string     `json:"expense_head"`
	QuoteFileID        *int64     `json:"quote_file_id"`
	Delivery           *Delivery  `json:"delivery"`
	Lines              []Line     `json:"lines"`
	Total              float64    `json:"total"`
}

// Delivery is the schedule of a warehouse transfer.
type Delivery struct {
	PartnerID int64  `json:"partner_id"`
	Partner   string `json:"partner"`
	Driver    string `json:"driver"`
	Phone     string `json:"phone"`
	Date      string `json:"date"`
	Slot      string `json:"slot"`
	Vehicle   string `json:"vehicle"`
	Note      string `json:"note"`
	Version   int    `json:"version"`
}

// IsCorp reports a corporate purchase or bill.
func (d *Doc) IsCorp() bool { return d.Kind == "corp_purchase" || d.Kind == "corp_bill" }

// IsBill reports a document paid without a PO: a utility, AMC or corporate bill.
func (d *Doc) IsBill() bool { return d.Kind == "bill" || d.Kind == "corp_bill" }

const selectDoc = `SELECT d.id, d.number, d.kind, d.property_id, p.name, p.kind, c.state_code, p.cluster_id, k.name, c.name, k.manager_id,
	d.vendor_id, v.legal_name, v.vendor_type, v.msme, v.gst_registered, v.payment_terms, v.email, v.mobile,
	d.requester_id, ru.name, coalesce(ru.mobile,''), d.requester_step, d.status, d.route_steps, d.step_idx,
	d.event_reason, d.event_note, d.routed_reason, d.routed_note, d.cm_approved_at, d.preop_user_id, coalesce(pu.name,''),
	d.return_reason, d.return_note, d.modified_note, d.modified_by, d.modified_at, d.approved_at,
	coalesce(d.po_number,''), d.po_date, coalesce(d.deliver_by::text,''), d.payment_terms, d.po_closed_short, coalesce(d.advance_hold_po,''), coalesce(d.zoho_po_id,''), d.zoho_po_sync, d.zoho_po_error,
	d.vendor_status, d.vendor_acted_at, d.vendor_note, coalesce(d.dispatch_date::text,''), d.sent_to_vendor_at,
	coalesce((SELECT value='true'::jsonb FROM setting WHERE key='receipt_by_requester'), false),
	d.is_transfer, d.dispatched_at, coalesce(d.wait_until::text,''), d.wait_note,
	d.bill_type_id, coalesce(bt.name,''), coalesce(bt.section,''), coalesce(to_char(d.period_start,'YYYY-MM'),''), coalesce(d.months,0), d.remark, d.created_at,
	d.parent_id, d.transfer_received_at, d.department_id, coalesce(cd.name,''), d.corp_l1_user_id, coalesce(u1.name,''), d.corp_l2_user_id, coalesce(u2.name,''),
	d.corp_backup_user_id, coalesce(ub.name,''), d.expense_head_id, coalesce(eh.name,''), d.quote_file_id,
	dl.partner_id, coalesce(dp.name,''), coalesce(dp.driver,''), coalesce(dp.phone,''), coalesce(dl.delivery_date::text,''), coalesce(dl.slot,''), coalesce(dl.vehicle,''), coalesce(dl.note,''), coalesce(dl.version,0)
	FROM document d JOIN property p ON p.id=d.property_id JOIN cluster k ON k.id=p.cluster_id JOIN city c ON c.id=k.city_id
	JOIN vendor v ON v.id=d.vendor_id JOIN app_user ru ON ru.id=d.requester_id
	LEFT JOIN app_user pu ON pu.id=d.preop_user_id LEFT JOIN bill_type bt ON bt.id=d.bill_type_id
	LEFT JOIN corp_department cd ON cd.id=d.department_id LEFT JOIN app_user u1 ON u1.id=d.corp_l1_user_id LEFT JOIN app_user u2 ON u2.id=d.corp_l2_user_id
	LEFT JOIN app_user ub ON ub.id=d.corp_backup_user_id LEFT JOIN expense_head eh ON eh.id=d.expense_head_id
	LEFT JOIN delivery dl ON dl.document_id=d.id LEFT JOIN delivery_partner dp ON dp.id=dl.partner_id`

func scanDoc(row pgx.Row) (*Doc, error) {
	d := &Doc{}
	var dl deliveryScan
	err := row.Scan(&d.ID, &d.Number, &d.Kind, &d.PropertyID, &d.Property, &d.PropertyKind, &d.PropertyState, &d.ClusterID, &d.Cluster, &d.City, &d.ClusterMgrID,
		&d.VendorID, &d.Vendor, &d.VendorType, &d.VendorMSME, &d.VendorGST, &d.VendorTerms, &d.VendorEmail, &d.VendorMobile,
		&d.RequesterID, &d.Requester, &d.RequesterMob, &d.RequesterStep, &d.Status, &d.RouteSteps, &d.StepIdx,
		&d.EventReason, &d.EventNote, &d.RoutedReason, &d.RoutedNote, &d.CMApprovedAt, &d.PreopUserID, &d.PreopUser,
		&d.ReturnReason, &d.ReturnNote, &d.ModifiedNote, &d.ModifiedBy, &d.ModifiedAt, &d.ApprovedAt,
		&d.PONumber, &d.PODate, &d.DeliverBy, &d.PaymentTerms, &d.ClosedShort, &d.AdvanceHold, &d.ZohoPOID, &d.ZohoPOSync, &d.ZohoPOError,
		&d.VendorStatus, &d.VendorActedAt, &d.VendorNote, &d.DispatchDate, &d.SentToVendor, &d.ReceiptByRequester,
		&d.IsTransfer, &d.DispatchedAt, &d.WaitUntil, &d.WaitNote,
		&d.BillTypeID, &d.BillType, &d.BillSection, &d.PeriodStart, &d.Months, &d.Remark, &d.CreatedAt,
		&d.ParentID, &d.TransferRecv, &d.DepartmentID, &d.Department, &d.CorpL1, &d.CorpL1Name, &d.CorpL2, &d.CorpL2Name,
		&d.CorpBackup, &d.CorpBackupNm, &d.HeadID, &d.Head, &d.QuoteFileID,
		&dl.PartnerID, &dl.Partner, &dl.Driver, &dl.Phone, &dl.Date, &dl.Slot, &dl.Vehicle, &dl.Note, &dl.Version)
	if dl.PartnerID != nil {
		d.Delivery = &Delivery{PartnerID: *dl.PartnerID, Partner: dl.Partner, Driver: dl.Driver, Phone: dl.Phone, Date: dl.Date, Slot: dl.Slot, Vehicle: dl.Vehicle, Note: dl.Note, Version: dl.Version}
	}
	return d, err
}

type deliveryScan struct {
	PartnerID                                         *int64
	Partner, Driver, Phone, Date, Slot, Vehicle, Note string
	Version                                           int
}

// Corporate lines have no SKU: the description stands in for the item and the expense head for the sub-category.
const selectLines = `SELECT l.id, l.document_id, coalesce(l.sku_id,0), coalesce(k.code,''), coalesce(k.item_name, l.description), coalesce(b.name,''), coalesce(ps.name, l.unit), coalesce(k.hsn,''),
	coalesce(sc.id,0), coalesce(sc.name, eh.name, ''), coalesce(c.id,0), coalesce(c.name, 'Corporate'),
	l.qty, l.original_qty, l.rate, l.gst_pct, l.estimated, l.dispatched_qty, l.received_qty, l.rejected_qty, l.description, l.unit, l.expense_head_id
	FROM document_line l LEFT JOIN sku k ON k.id=l.sku_id LEFT JOIN brand b ON b.id=k.brand_id LEFT JOIN pack_size ps ON ps.id=k.pack_size_id
	LEFT JOIN sub_category sc ON sc.id=k.sub_category_id LEFT JOIN category c ON c.id=sc.category_id LEFT JOIN expense_head eh ON eh.id=l.expense_head_id`

// Load reads a document with its lines. lock takes a row lock for a state change inside a transaction.
func Load(ctx context.Context, q database.Q, id int64, lock bool) (*Doc, error) {
	sql := selectDoc + ` WHERE d.id=$1`
	if lock {
		sql += ` FOR UPDATE OF d`
	}
	d, err := scanDoc(q.QueryRow(ctx, sql, id))
	if err == pgx.ErrNoRows {
		return nil, httpjson.NotFound("Document")
	}
	if err != nil {
		return nil, err
	}
	m := map[int64]*Doc{d.ID: d}
	if err := loadLines(ctx, q, m); err != nil {
		return nil, err
	}
	if d.IsBill() {
		_ = q.QueryRow(ctx, `SELECT amount FROM invoice WHERE document_id=$1 AND status<>'VOID' ORDER BY id DESC LIMIT 1`, d.ID).Scan(&d.Total)
	}
	return d, nil
}

// LoadMany reads documents matching a WHERE clause (on alias d), with lines.
func LoadMany(ctx context.Context, q database.Q, where string, args ...any) ([]*Doc, error) {
	rows, err := q.Query(ctx, selectDoc+` `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Doc{}
	m := map[int64]*Doc{}
	for rows.Next() {
		d, err := scanDoc(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
		m[d.ID] = d
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return out, nil
	}
	if err := loadLines(ctx, q, m); err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(out))
	for _, d := range out {
		if d.IsBill() {
			ids = append(ids, d.ID)
		}
	}
	if len(ids) > 0 {
		br, err := q.Query(ctx, `SELECT DISTINCT ON (document_id) document_id, amount FROM invoice WHERE document_id = ANY($1) AND status<>'VOID' ORDER BY document_id, id DESC`, ids)
		if err != nil {
			return nil, err
		}
		defer br.Close()
		for br.Next() {
			var id int64
			var a float64
			if err := br.Scan(&id, &a); err != nil {
				return nil, err
			}
			m[id].Total = a
		}
	}
	return out, nil
}

func loadLines(ctx context.Context, q database.Q, m map[int64]*Doc) error {
	ids := make([]int64, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	rows, err := q.Query(ctx, selectLines+` WHERE l.document_id = ANY($1) ORDER BY l.id`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var l Line
		var docID int64
		if err := rows.Scan(&l.ID, &docID, &l.SKUID, &l.SKUCode, &l.ItemName, &l.Brand, &l.PackSize, &l.HSN, &l.SubCatID, &l.SubCat, &l.CategoryID, &l.Category,
			&l.Qty, &l.OriginalQty, &l.Rate, &l.GST, &l.Estimated, &l.Dispatched, &l.ReceivedQty, &l.RejectedQty, &l.Description, &l.Unit, &l.HeadID); err != nil {
			return err
		}
		l.Value = money.Round2(money.LineValue(l.Qty, l.Rate, l.GST))
		d := m[docID]
		d.Lines = append(d.Lines, l)
		d.Total = money.Round2(d.Total + l.Value)
	}
	for _, d := range m {
		if d.Lines == nil {
			d.Lines = []Line{}
		}
	}
	return rows.Err()
}

// NoBudget is true for the warehouse, pre-opening properties and the corporate office.
func (d *Doc) NoBudget() bool { return d.PropertyKind != "live" || d.IsCorp() }

// Categories lists the item categories on the document, for category-limited roles.
func (d *Doc) Categories() []int64 {
	seen := map[int64]bool{}
	out := []int64{}
	for _, l := range d.Lines {
		if l.CategoryID != 0 && !seen[l.CategoryID] {
			seen[l.CategoryID] = true
			out = append(out, l.CategoryID)
		}
	}
	return out
}

// SubCatTotals is the value incl. GST per sub-category, used for budget spend and flags.
func (d *Doc) SubCatTotals() map[int64]float64 {
	t := map[int64]float64{}
	for _, l := range d.Lines {
		if l.SubCatID != 0 {
			t[l.SubCatID] += money.LineValue(l.Qty, l.Rate, l.GST)
		}
	}
	return t
}

// Owner is the flow step that owns the next action on the document itself, or "".
// "requester" means the person who raised it. Invoices have their own owners.
func (d *Doc) Owner() string {
	switch d.Status {
	case Pending:
		if d.StepIdx < len(d.RouteSteps) {
			return d.RouteSteps[d.StepIdx]
		}
	case Returned:
		return "requester"
	case Approved:
		if d.AdvanceHold != "" {
			return "finance" // the PO waits for Finance to approve its advance
		}
		if d.Kind == "purchase" || d.Kind == "corp_purchase" {
			return "purchase"
		}
	case POIssued:
		if d.Kind == "corp_purchase" {
			return "requester" // the corporate requester confirms receipt and enters the invoice
		}
		if d.IsTransfer || d.PropertyKind == "preopening" || (d.PropertyKind == "warehouse" && !d.ReceiptByRequester) {
			return "wh"
		}
		if d.ReceiptByRequester {
			return "requester"
		}
		return "store"
	case WHSent:
		if d.ReceiptByRequester {
			return "requester"
		}
		return "store"
	}
	return ""
}

var StepNames = map[string]string{
	"staff": "Property Staff", "l1": "Property Manager", "l2": "Cluster Manager", "cc": "Cost Controller", "purchase": "Purchase Team",
	"store": "Receiving / Store", "ap": "Accounts Payable", "finance": "Finance", "wh": "Warehouse Manager", "preop": "Pre-opening Team",
	"creq": "Corporate Requester", "dh": "Department Head", "capp": "Corporate Approver", "admin": "System Admin", "requester": "Requester",
	"cl1": "Department L1", "cl2": "Department L2", "chef": "Property Chef", "hchef": "Head Chef", "whs": "Warehouse Staff",
}

// StatusLabel is the plain-English status shown in lists.
func (d *Doc) StatusLabel() string {
	switch d.Status {
	case Pending:
		o := d.Owner()
		if o == "preop" && d.PreopUser != "" {
			return "With " + d.PreopUser + ", Pre-opening"
		}
		if o == "cl1" {
			return "With " + d.CorpL1Name + " (L1)"
		}
		if o == "cl2" {
			return "With " + d.CorpL2Name + " (L2)"
		}
		return "With " + StepNames[o]
	case Returned:
		return "Returned for changes"
	case Rejected:
		return "Rejected"
	case Cancelled:
		if d.PONumber != "" {
			return "PO cancelled, nothing delivered"
		}
		return "Cancelled"
	case Approved:
		if d.IsBill() {
			return "Approved, with Finance"
		}
		if d.AdvanceHold != "" {
			return "PO waiting for advance approval"
		}
		return "Approved, PO pending"
	case POIssued:
		if d.IsTransfer {
			if d.Delivery != nil {
				return "Delivery scheduled, " + d.Delivery.Date
			}
			if d.WaitUntil != "" {
				return "Warehouse awaiting stock"
			}
			return "Warehouse to dispatch"
		}
		if d.Kind == "corp_purchase" {
			return "PO issued, receipt to confirm"
		}
		switch d.VendorStatus {
		case "declined":
			return "Declined by vendor"
		case "accepted":
			if d.DispatchDate != "" {
				return "Awaiting delivery, dispatch " + d.DispatchDate
			}
			return "Awaiting delivery"
		}
		if d.SentToVendor == nil {
			return "Push to vendor" // raised, but not yet sent in a vendor order sheet
		}
		return "Sent to vendor"
	case Received:
		return "Received, invoice in progress"
	case Closed:
		return "Paid and closed"
	case WHSent:
		return "In transit from warehouse"
	case WHDone:
		return "Received, closed"
	case WHBack:
		return "Returned by warehouse"
	}
	return d.Status
}

// CanAct returns the role a user acts as at a step on this document, or nil.
// Pre-opening approvals belong to the assigned team member.
func CanAct(u *access.User, d *Doc, step, right string) *access.Grant {
	if step == "requester" {
		if u.IsAdmin || u.ID == d.RequesterID {
			return &access.Grant{Name: "Requester", Step: "requester"}
		}
		return nil
	}
	if step == "cl1" || step == "cl2" {
		// Corporate approvers are fixed on the request. The backup can act at either step, never on a request they raised.
		if u.IsAdmin {
			return &access.Grant{Name: "System Admin", Step: "admin"}
		}
		if !u.HasRight(access.Approve) {
			return nil
		}
		assigned := d.CorpL1
		label := "Department L1"
		if step == "cl2" {
			assigned, label = d.CorpL2, "Department L2"
		}
		if assigned != nil && *assigned == u.ID {
			return &access.Grant{Name: label, Step: step}
		}
		if d.CorpBackup != nil && *d.CorpBackup == u.ID && d.RequesterID != u.ID {
			return &access.Grant{Name: "Backup approver", Step: step}
		}
		return nil
	}
	g := u.Can(step, right, d.PropertyID, d.ClusterID, d.Categories())
	if g == nil {
		return nil
	}
	if step == "preop" && !u.IsAdmin && d.PreopUserID != nil && *d.PreopUserID != u.ID {
		return nil
	}
	if step == "l2" && !u.IsAdmin && d.ClusterMgrID != nil && *d.ClusterMgrID != u.ID && g.Scope == "all" {
		// A Cluster Manager with all-property scope still acts only where they manage the cluster, unless scoped explicitly.
		return nil
	}
	return g
}

// Receiver returns the role a user records a delivery on this vendor PO as, or nil: the receiving step (the store, or
// the corporate requester), or the person who raised a purchase request, on their own PO, when their role at the
// property may edit Goods Receipts.
func Receiver(u *access.User, d *Doc) *access.Grant {
	if d.Status != POIssued || d.IsTransfer {
		return nil
	}
	if d.Kind == "corp_purchase" {
		return CanAct(u, d, "requester", "") // the corporate requester confirms receipt
	}
	return receiver(u, d)
}

// TransferReceiver returns the role a user receives a warehouse delivery (in transit) at a property as, or nil: the
// same people as for a vendor PO.
func TransferReceiver(u *access.User, d *Doc) *access.Grant {
	if d.Status != WHSent || !d.IsTransfer {
		return nil
	}
	return receiver(u, d)
}

// receiver: the Warehouse Manager at a warehouse or pre-opening property; elsewhere anyone with the Receiving / Store
// role there, and the person who raised the request when their role there may edit Goods Receipts (Flow Settings
// decides whose step it is: the requester's or the store's).
func receiver(u *access.User, d *Doc) *access.Grant {
	if !d.IsTransfer && (d.PropertyKind == "warehouse" || d.PropertyKind == "preopening") {
		if g := CanAct(u, d, "wh", access.Receive); g != nil {
			return g
		}
		if g := CanAct(u, d, "whs", access.Receive); g != nil && d.PropertyKind == "warehouse" {
			return g // Warehouse Staff receive at the warehouse
		}
	} else if g := CanAct(u, d, "store", access.Receive); g != nil {
		return g
	}
	if d.Kind == "purchase" && u.ID == d.RequesterID && u.Level("goods_receipts", d.PropertyID, d.ClusterID) >= screens.Edit {
		return &access.Grant{Name: "Requester", Step: "requester"}
	}
	return nil
}

// MarkSentToVendor records that a PO reached its vendor (the PO email, or a vendor order sheet); the first time counts.
func MarkSentToVendor(ctx context.Context, q database.Q, docID int64) error {
	_, err := q.Exec(ctx, `UPDATE document SET sent_to_vendor_at=now() WHERE id=$1 AND sent_to_vendor_at IS NULL`, docID)
	return err
}

// POKeeper returns the role a user looks after a vendor PO as, or nil: the Purchase Team where it still works, or,
// with no Purchase Team step, any approver on the request's route (they raised the PO by approving it). They confirm
// it for the vendor and sort out a vendor's decline.
func POKeeper(u *access.User, d *Doc) *access.Grant {
	if g := CanAct(u, d, "purchase", access.IssuePO); g != nil {
		return g
	}
	for _, step := range d.RouteSteps {
		if g := CanAct(u, d, step, access.Approve); g != nil {
			return g
		}
	}
	return nil
}

// NextNumber returns the next number from a sequence with a prefix, for example PR-1001.
func NextNumber(ctx context.Context, q database.Q, seq, prefix string) (string, error) {
	var n int64
	if err := q.QueryRow(ctx, fmt.Sprintf(`SELECT nextval('%s')`, seq)).Scan(&n); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s-%d", prefix, n), nil
}

// CloseIfPaid closes a fully received purchase, or an approved bill, once every invoice on it is paid.
func CloseIfPaid(ctx context.Context, q database.Q, docID int64) error {
	_, err := q.Exec(ctx, `UPDATE document d SET status='CLOSED', updated_at=now()
		WHERE d.id=$1 AND d.status IN ('RECEIVED','APPROVED') AND (d.kind IN ('bill','corp_bill') OR d.status='RECEIVED')
		AND EXISTS (SELECT 1 FROM invoice i WHERE i.document_id=d.id AND i.status='PAID')
		AND NOT EXISTS (SELECT 1 FROM invoice i WHERE i.document_id=d.id AND i.status NOT IN ('PAID','VOID'))`, docID)
	return err
}

// ReleaseHeldPO issues a PO that was waiting for Finance to approve its advance: it takes the number it was held
// under. It returns that number, or "" when the document was not on hold.
func ReleaseHeldPO(ctx context.Context, q database.Q, docID int64) (string, error) {
	zohowake.Now() // something may now go to Zoho
	var po string
	err := q.QueryRow(ctx, `UPDATE document SET status='PO_ISSUED', po_number=advance_hold_po, po_date=now(), advance_hold_po=NULL, zoho_po_sync='create', zoho_po_error='', updated_at=now()
		WHERE id=$1 AND status='APPROVED' AND advance_hold_po IS NOT NULL RETURNING po_number`, docID).Scan(&po)
	if err == pgx.ErrNoRows {
		return "", nil
	}
	return po, err
}

// ClearHeldPO takes a request off advance hold when its advance is rejected or cancelled: it stays approved, for
// Purchase to issue the PO again. It returns the number the PO had been held under, or "".
func ClearHeldPO(ctx context.Context, q database.Q, docID int64) (string, error) {
	var po string
	err := q.QueryRow(ctx, `UPDATE document d SET advance_hold_po=NULL, updated_at=now() FROM (SELECT advance_hold_po FROM document WHERE id=$1) o
		WHERE d.id=$1 AND d.advance_hold_po IS NOT NULL RETURNING o.advance_hold_po`, docID).Scan(&po)
	if err == pgx.ErrNoRows {
		return "", nil
	}
	return po, err
}

// QueueZohoPO records that a vendor PO must be sent to Zoho Books (action create, update or cancel); the zoho-purchase-
// orders job sends it. A change before the PO ever reached Zoho is sent as the create (which carries the latest
// state), and a cancel of a PO Zoho never had is dropped. Warehouse transfers are internal and never go to Zoho.
func QueueZohoPO(ctx context.Context, q database.Q, docID int64, action string) error {
	zohowake.Now() // something may now go to Zoho
	_, err := q.Exec(ctx, `UPDATE document SET zoho_po_sync = CASE
			WHEN $2='create' THEN 'create'
			WHEN zoho_po_id IS NULL AND $2='cancel' THEN ''
			WHEN zoho_po_id IS NULL THEN 'create'
			ELSE $2 END,
		zoho_po_error='' WHERE id=$1 AND kind IN ('purchase','corp_purchase') AND NOT is_transfer AND po_number IS NOT NULL AND po_number<>''`, docID, action)
	return err
}

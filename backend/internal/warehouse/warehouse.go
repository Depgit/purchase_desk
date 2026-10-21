// Package warehouse runs the central warehouse: the stock ledger per SKU
// (closing = opening + purchased − issued − pilferage − in transit), the dispatch queue of transfer orders,
// delivery scheduling with delivery partners, dispatch (with short dispatch), receipt at the property, and pilferage.
// Stock movements are derived from goods receipts and transfer orders, never typed in, so the ledger reconciles to documents.
package warehouse

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"purchasedesk/internal/access"
	"purchasedesk/internal/auditlog"
	"purchasedesk/internal/budgets"
	"purchasedesk/internal/clock"
	"purchasedesk/internal/csvfile"
	"purchasedesk/internal/database"
	"purchasedesk/internal/documents"
	"purchasedesk/internal/flowsettings"
	"purchasedesk/internal/httpjson"
	"purchasedesk/internal/messaging"
	"purchasedesk/internal/money"
	"purchasedesk/internal/textclean"
	"purchasedesk/internal/uploadedfiles"
)

type Service struct {
	DB       *pgxpool.Pool
	Guard    *access.Guard
	Files    *uploadedfiles.Service
	Messages *messaging.Sender
}

func (s *Service) Routes(r chi.Router) {
	r.Get("/warehouse/stock", httpjson.Wrap(s.stockList))
	r.Put("/warehouse/stock/{sku}", httpjson.Wrap(s.setMin))
	r.Get("/warehouse/transfers", httpjson.Wrap(s.transfers))
	r.Get("/warehouse/movements", httpjson.Wrap(s.movements))
	r.Get("/delivery-partners", httpjson.Wrap(s.partners))
	r.Post("/delivery-partners", httpjson.Wrap(s.savePartner))
	r.Put("/delivery-partners/{id}", httpjson.Wrap(s.savePartner))
	r.Post("/warehouse/schedule", httpjson.Wrap(s.schedule))
	r.Post("/documents/{id}/wait", httpjson.Wrap(s.wait))
	r.Post("/documents/{id}/dispatch", httpjson.Wrap(s.dispatch))
	r.Post("/documents/{id}/cannot-supply", httpjson.Wrap(s.cannotSupply))
	r.Post("/documents/{id}/receive-transfer", httpjson.Wrap(s.receiveTransfer))
}

func canWH(u *access.User) bool { return u.IsAdmin || u.HasStep("whl2") } // stock and dispatch: Warehouse Approver L2

// ---------- stock ledger ----------

type Stock struct {
	SKUID     int64   `json:"sku_id"`
	Code      string  `json:"sku_code"`
	Item      string  `json:"item"`
	Brand     string  `json:"brand"`
	PackSize  string  `json:"pack_size"`
	SubCat    string  `json:"sub_category"`
	Opening   float64 `json:"opening"`
	Purchased float64 `json:"purchased"`
	Issued    float64 `json:"issued"`
	Pilferage float64 `json:"pilferage"`
	InTransit float64 `json:"in_transit"`
	Closing   float64 `json:"closing"`
	MinLevel  float64 `json:"min_level"`
	Rate      float64 `json:"rate"`
	Value     float64 `json:"value"`
	PilValue  float64 `json:"pilferage_value"`
	Status    string  `json:"status"`
	Due       float64 `json:"due"`        // ordered by properties on approved transfers, not yet dispatched
	OnOrder   float64 `json:"on_order"`   // the warehouse's own POs with vendors, not yet received
	Short     float64 `json:"short"`      // due − closing − on order: what the warehouse still has to buy
	Stocked   bool    `json:"wh_stocked"` // the item is marked stocked at the warehouse
}

const stockSQL = `WITH pur AS (
		SELECT l.sku_id, sum(gl.qty) q FROM goods_receipt_line gl JOIN goods_receipt g ON g.id=gl.goods_receipt_id AND NOT g.voided
		JOIN document d ON d.id=g.document_id JOIN property p ON p.id=d.property_id AND p.kind='warehouse' JOIN document_line l ON l.id=gl.line_id
		WHERE l.sku_id IS NOT NULL GROUP BY 1),
	iss AS (SELECT l.sku_id, sum(l.received_qty) q, sum(coalesce(l.dispatched_qty,0)-l.received_qty) pil FROM document_line l JOIN document d ON d.id=l.document_id
		WHERE d.is_transfer AND d.status='WH_DONE' GROUP BY 1),
	tr AS (SELECT l.sku_id, sum(coalesce(l.dispatched_qty,0)) q FROM document_line l JOIN document d ON d.id=l.document_id WHERE d.is_transfer AND d.status='WH_SENT' GROUP BY 1),
	due AS (SELECT l.sku_id, sum(l.qty) q FROM document_line l JOIN document d ON d.id=l.document_id WHERE d.is_transfer AND d.status='PO_ISSUED' GROUP BY 1),
	ord AS (SELECT l.sku_id, sum(greatest(l.qty-l.received_qty,0)) q FROM document_line l JOIN document d ON d.id=l.document_id JOIN property p ON p.id=d.property_id AND p.kind='warehouse'
		WHERE NOT d.is_transfer AND d.kind='purchase' AND d.status IN ('APPROVED','PO_ISSUED') GROUP BY 1)
	SELECT k.id, k.code, k.item_name, coalesce(b.name,''), ps.name, sc.name, coalesce(ws.opening_qty,0), coalesce(pur.q,0), coalesce(iss.q,0), coalesce(iss.pil,0), coalesce(tr.q,0), coalesce(ws.min_level,0),
	coalesce((SELECT r.rate FROM rate_card r JOIN vendor v ON v.id=r.vendor_id AND v.vendor_type='warehouse' WHERE r.sku_id=k.id AND NOT r.superseded ORDER BY r.valid_from DESC LIMIT 1),
	         (SELECT l.rate FROM goods_receipt_line gl JOIN document_line l ON l.id=gl.line_id JOIN document d ON d.id=l.document_id JOIN property p ON p.id=d.property_id AND p.kind='warehouse' WHERE l.sku_id=k.id ORDER BY gl.goods_receipt_id DESC LIMIT 1), 0),
	coalesce(due.q,0), coalesce(ord.q,0), k.wh_stocked
	FROM sku k LEFT JOIN brand b ON b.id=k.brand_id JOIN pack_size ps ON ps.id=k.pack_size_id JOIN sub_category sc ON sc.id=k.sub_category_id
	LEFT JOIN warehouse_stock ws ON ws.sku_id=k.id LEFT JOIN pur ON pur.sku_id=k.id LEFT JOIN iss ON iss.sku_id=k.id LEFT JOIN tr ON tr.sku_id=k.id
	LEFT JOIN due ON due.sku_id=k.id LEFT JOIN ord ON ord.sku_id=k.id
	WHERE ws.sku_id IS NOT NULL OR pur.sku_id IS NOT NULL OR iss.sku_id IS NOT NULL OR tr.sku_id IS NOT NULL OR due.sku_id IS NOT NULL OR (k.wh_stocked AND k.active)`

// Ledger returns stock per SKU, keyed by SKU id.
func Ledger(ctx context.Context, q database.Q) (map[int64]*Stock, []*Stock, error) {
	rows, err := q.Query(ctx, stockSQL+` ORDER BY k.code`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	m := map[int64]*Stock{}
	list := []*Stock{}
	for rows.Next() {
		x := &Stock{}
		if err := rows.Scan(&x.SKUID, &x.Code, &x.Item, &x.Brand, &x.PackSize, &x.SubCat, &x.Opening, &x.Purchased, &x.Issued, &x.Pilferage, &x.InTransit, &x.MinLevel, &x.Rate, &x.Due, &x.OnOrder, &x.Stocked); err != nil {
			return nil, nil, err
		}
		x.Closing = x.Opening + x.Purchased - x.Issued - x.Pilferage - x.InTransit
		x.Value, x.PilValue = money.Round2(x.Closing*x.Rate), money.Round2(x.Pilferage*x.Rate)
		x.Short = math.Max(0, x.Due-math.Max(x.Closing, 0)-x.OnOrder)
		switch {
		case x.Closing <= 0:
			x.Status = "Out of stock"
		case x.Closing < x.MinLevel:
			x.Status = "Reorder"
		default:
			x.Status = "OK"
		}
		m[x.SKUID] = x
		list = append(list, x)
	}
	return m, list, rows.Err()
}

func (s *Service) stockList(w http.ResponseWriter, r *http.Request) error {
	_, list, err := Ledger(r.Context(), s.DB)
	if err != nil {
		return err
	}
	return httpjson.OK(w, list)
}

func (s *Service) setMin(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	if !canWH(u) {
		return httpjson.Forbidden("Only the Warehouse Approver L2 can change reorder levels.")
	}
	sku, err := httpjson.ID(r, "sku")
	if err != nil {
		return err
	}
	var in struct {
		MinLevel float64 `json:"min_level"`
	}
	if err := httpjson.Decode(r, &in); err != nil {
		return err
	}
	if in.MinLevel < 0 {
		return httpjson.Bad("Reorder level must be 0 or more.")
	}
	if _, err := s.DB.Exec(r.Context(), `INSERT INTO warehouse_stock(sku_id,min_level,updated_by) VALUES ($1,$2,$3) ON CONFLICT (sku_id) DO UPDATE SET min_level=EXCLUDED.min_level, updated_by=EXCLUDED.updated_by, updated_at=now()`, sku, in.MinLevel, u.ID); err != nil {
		return err
	}
	_ = auditlog.Write(r.Context(), s.DB, u, "Warehouse", "Reorder level changed", fmt.Sprint(sku), fmt.Sprint(in.MinLevel))
	return httpjson.OK(w, map[string]bool{"ok": true})
}

// UploadCheck and UploadCommit load opening stock and reorder levels from a file (registered with bulk upload).
func UploadCheck(ctx context.Context, q database.Q, _ *access.User, row csvfile.Row, dup func(string) bool) (bool, bool, string, any) {
	var id int64
	var active bool
	if err := q.QueryRow(ctx, `SELECT id, active FROM sku WHERE upper(code)=upper($1)`, row.Get("sku_code")).Scan(&id, &active); err != nil {
		return false, false, "Unknown SKU code", nil
	}
	if !active {
		return false, false, "SKU is inactive", nil
	}
	var open, min float64
	if _, err := fmt.Sscanf(row.Get("opening_qty"), "%g", &open); err != nil || open < 0 {
		return false, false, "Opening quantity must be a number, 0 or more", nil
	}
	if v := row.Get("reorder_level"); v != "" {
		if _, err := fmt.Sscanf(v, "%g", &min); err != nil || min < 0 {
			return false, false, "Reorder level must be 0 or more", nil
		}
	}
	if dup(fmt.Sprint(id)) {
		return false, false, "Duplicate row in the file", nil
	}
	return true, false, "Opening stock set", [3]float64{float64(id), open, min}
}

func UploadCommit(ctx context.Context, tx pgx.Tx, u *access.User, objs []any) error {
	for _, o := range objs {
		a := o.([3]float64)
		if _, err := tx.Exec(ctx, `INSERT INTO warehouse_stock(sku_id,opening_qty,min_level,updated_by) VALUES ($1,$2,$3,$4)
			ON CONFLICT (sku_id) DO UPDATE SET opening_qty=EXCLUDED.opening_qty, min_level=EXCLUDED.min_level, updated_by=EXCLUDED.updated_by, updated_at=now()`, int64(a[0]), a[1], a[2], u.ID); err != nil {
			return err
		}
	}
	return nil
}

// ---------- transfers ----------

type TransferRow struct {
	*documents.Doc
	StatusLabel string `json:"status_label"`
	Short       int    `json:"short_lines"`
	AgeHours    int    `json:"age_hours"`
}

// transfers lists transfer orders by stage, and inbound POs for the warehouse and pre-opening properties.
func (s *Service) transfers(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	all, ids, err := s.Guard.Visible(r.Context(), u)
	if err != nil {
		return err
	}
	docs, err := documents.LoadMany(r.Context(), s.DB, `WHERE ((d.is_transfer AND (d.status IN ('PO_ISSUED','WH_SENT') OR d.updated_at > now() - interval '30 days'))
		OR (d.status='PO_ISSUED' AND NOT d.is_transfer AND p.kind IN ('warehouse','preopening'))) AND ($1 OR d.property_id = ANY($2) OR $3)
		ORDER BY d.created_at`, all, ids, u.IsAdmin || u.HasStep("whl1") || u.HasStep("whl2"))
	if err != nil {
		return err
	}
	stock, _, err := Ledger(r.Context(), s.DB)
	if err != nil {
		return err
	}
	out := []TransferRow{}
	for _, d := range docs {
		row := TransferRow{Doc: d, StatusLabel: d.StatusLabel(), AgeHours: int(time.Since(d.CreatedAt).Hours())}
		if d.PODate != nil {
			row.AgeHours = int(time.Since(*d.PODate).Hours())
		}
		if d.IsTransfer && d.Status == documents.POIssued {
			for _, l := range d.Lines {
				if st := stock[l.SKUID]; l.Qty > 0 && (st == nil || st.Closing < l.Qty) {
					row.Short++
				}
			}
		}
		out = append(out, row)
	}
	return httpjson.OK(w, out)
}

type movement struct {
	At      time.Time `json:"at"`
	Kind    string    `json:"kind"`
	Ref     string    `json:"ref"`
	DocID   int64     `json:"document_id"`
	Party   string    `json:"party"`
	SKUCode string    `json:"sku_code"`
	Item    string    `json:"item"`
	Qty     float64   `json:"qty"`
	Value   float64   `json:"value"`
}

func (s *Service) movements(w http.ResponseWriter, r *http.Request) error {
	rows, err := s.DB.Query(r.Context(), `
		SELECT g.received_at, 'Purchase received', coalesce(d.po_number,d.number), d.id, v.legal_name, k.code, k.item_name, gl.qty, gl.qty*l.rate
		FROM goods_receipt_line gl JOIN goods_receipt g ON g.id=gl.goods_receipt_id AND NOT g.voided JOIN document d ON d.id=g.document_id
		JOIN property p ON p.id=d.property_id AND p.kind='warehouse' JOIN vendor v ON v.id=d.vendor_id JOIN document_line l ON l.id=gl.line_id JOIN sku k ON k.id=l.sku_id
		UNION ALL
		SELECT d.dispatched_at, 'Dispatched to property', d.po_number, d.id, p.name, k.code, k.item_name, -l.dispatched_qty, -l.dispatched_qty*l.rate
		FROM document_line l JOIN document d ON d.id=l.document_id AND d.is_transfer AND d.dispatched_at IS NOT NULL JOIN property p ON p.id=d.property_id JOIN sku k ON k.id=l.sku_id
		WHERE coalesce(l.dispatched_qty,0) > 0
		UNION ALL
		SELECT d.transfer_received_at, 'Pilferage', d.po_number, d.id, p.name, k.code, k.item_name, l.dispatched_qty-l.received_qty, (l.dispatched_qty-l.received_qty)*l.rate
		FROM document_line l JOIN document d ON d.id=l.document_id AND d.is_transfer AND d.status='WH_DONE' JOIN property p ON p.id=d.property_id JOIN sku k ON k.id=l.sku_id
		WHERE l.dispatched_qty > l.received_qty
		ORDER BY 1 DESC LIMIT 5000`)
	if err != nil {
		return err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (movement, error) {
		var m movement
		return m, row.Scan(&m.At, &m.Kind, &m.Ref, &m.DocID, &m.Party, &m.SKUCode, &m.Item, &m.Qty, &m.Value)
	})
	if err != nil {
		return err
	}
	return httpjson.OK(w, out)
}

// ---------- delivery partners ----------

type Partner struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Driver string `json:"driver"`
	Phone  string `json:"phone"`
	Active bool   `json:"active"`
	Open   int    `json:"open_deliveries"`
}

func (s *Service) partners(w http.ResponseWriter, r *http.Request) error {
	rows, err := s.DB.Query(r.Context(), `SELECT p.id, p.name, p.kind, p.driver, p.phone, p.active,
		(SELECT count(*) FROM delivery dl JOIN document d ON d.id=dl.document_id AND d.status IN ('PO_ISSUED','WH_SENT') WHERE dl.partner_id=p.id)
		FROM delivery_partner p ORDER BY p.name`)
	if err != nil {
		return err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Partner, error) {
		var x Partner
		return x, row.Scan(&x.ID, &x.Name, &x.Kind, &x.Driver, &x.Phone, &x.Active, &x.Open)
	})
	if err != nil {
		return err
	}
	return httpjson.OK(w, out)
}

func (s *Service) savePartner(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	if !canWH(u) {
		return httpjson.Forbidden("Only the Warehouse Approver L2 can change delivery partners.")
	}
	var in Partner
	if err := httpjson.Decode(r, &in); err != nil {
		return err
	}
	in.Name = textclean.Clean(in.Name)
	if len(in.Name) < 3 {
		return httpjson.Bad("Enter the name.")
	}
	if in.Kind != "own" && in.Kind != "partner" {
		return httpjson.Bad("Choose own vehicle or delivery partner.")
	}
	id := int64(0)
	if chi.URLParam(r, "id") != "" {
		var err error
		if id, err = httpjson.ID(r, "id"); err != nil {
			return err
		}
	}
	var dup bool
	_ = s.DB.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM delivery_partner WHERE norm_key(name)=norm_key($1) AND id<>$2)`, in.Name, id).Scan(&dup)
	if dup {
		return httpjson.Bad("This delivery partner already exists.")
	}
	if id == 0 {
		if err := s.DB.QueryRow(r.Context(), `INSERT INTO delivery_partner(name,kind,driver,phone,created_by) VALUES ($1,$2,$3,$4,$5) RETURNING id`, in.Name, in.Kind, in.Driver, in.Phone, u.ID).Scan(&id); err != nil {
			return err
		}
	} else if _, err := s.DB.Exec(r.Context(), `UPDATE delivery_partner SET name=$2, kind=$3, driver=$4, phone=$5 WHERE id=$1`, id, in.Name, in.Kind, in.Driver, in.Phone); err != nil {
		return err
	}
	_ = auditlog.Write(r.Context(), s.DB, u, "Warehouse", "Delivery partner saved", in.Name, in.Kind)
	return httpjson.OK(w, map[string]int64{"id": id})
}

// lockTransfer loads a transfer order for a change by the Warehouse Approver L2.
func lockTransfer(ctx context.Context, tx pgx.Tx, u *access.User, id int64, status string) (*documents.Doc, *access.Grant, error) {
	d, err := documents.Load(ctx, tx, id, true)
	if err != nil {
		return nil, nil, err
	}
	if !d.IsTransfer || d.Status != status {
		return nil, nil, httpjson.Conflict("%s is not at this step any more.", d.PONumber)
	}
	g := documents.CanAct(u, d, "whx", "")
	if g == nil {
		return nil, nil, httpjson.Forbidden("Only a Warehouse Approver (L1 or L2) can do this.")
	}
	return d, g, nil
}

// schedule assigns a delivery partner, date and slot to one or several transfer orders (one trip).
func (s *Service) schedule(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	var in struct {
		IDs       []int64 `json:"ids"`
		PartnerID int64   `json:"partner_id"`
		Date      string  `json:"date"`
		Slot      string  `json:"slot"`
		Vehicle   string  `json:"vehicle"`
		Note      string  `json:"note"`
	}
	if err := httpjson.Decode(r, &in); err != nil {
		return err
	}
	day, ok := clock.ParseDate(in.Date)
	if !ok || day.Before(clock.Today()) {
		return httpjson.Bad("Choose a delivery date from today onwards.")
	}
	lists, err := flowsettings.PickLists(r.Context(), s.DB)
	if err != nil {
		return err
	}
	if !textclean.Contains(lists["delivery_slots"], in.Slot) {
		return httpjson.Bad("Choose a time slot.")
	}
	var pname string
	var active bool
	if err := s.DB.QueryRow(r.Context(), `SELECT name, active FROM delivery_partner WHERE id=$1`, in.PartnerID).Scan(&pname, &active); err != nil || !active {
		return httpjson.Bad("Choose an active delivery partner.")
	}
	if len(in.IDs) == 0 {
		return httpjson.Bad("Select at least one order.")
	}
	notify := []int64{}
	err = pgx.BeginFunc(r.Context(), s.DB, func(tx pgx.Tx) error {
		for _, id := range in.IDs {
			d, g, err := lockTransfer(r.Context(), tx, u, id, documents.POIssued)
			if err != nil {
				return err
			}
			act := "Delivery scheduled"
			if d.Delivery != nil {
				act = "Delivery rescheduled"
			}
			if _, err := tx.Exec(r.Context(), `INSERT INTO delivery(document_id,partner_id,delivery_date,slot,vehicle,note,version,scheduled_by) VALUES ($1,$2,$3,$4,$5,$6,1,$7)
				ON CONFLICT (document_id) DO UPDATE SET partner_id=EXCLUDED.partner_id, delivery_date=EXCLUDED.delivery_date, slot=EXCLUDED.slot, vehicle=EXCLUDED.vehicle,
				note=EXCLUDED.note, version=delivery.version+1, scheduled_by=EXCLUDED.scheduled_by, scheduled_at=now()`, id, in.PartnerID, day, in.Slot, strings.TrimSpace(in.Vehicle), strings.TrimSpace(in.Note), u.ID); err != nil {
				return err
			}
			if _, err := tx.Exec(r.Context(), `UPDATE document SET wait_until=NULL, wait_note='', updated_at=now() WHERE id=$1`, id); err != nil {
				return err
			}
			if err := auditlog.Doc(r.Context(), tx, u, g.Name, d.ID, d.Number, "Warehouse", act, fmt.Sprintf("%s, %s · %s", in.Date, in.Slot, pname)); err != nil {
				return err
			}
			notify = append(notify, id)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, id := range notify {
		s.tell(u, id, map[string]string{"date": in.Date, "slot": in.Slot})
	}
	msg := fmt.Sprintf("Delivery scheduled for %s, %s", in.Date, in.Slot)
	if len(in.IDs) > 1 {
		msg = fmt.Sprintf("%d orders scheduled on one trip", len(in.IDs))
	}
	return httpjson.OK(w, map[string]string{"message": msg})
}

// tell sends the requester the "delivery scheduled" WhatsApp template.
func (s *Service) tell(u *access.User, docID int64, extra map[string]string) {
	if s.Messages == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		d, err := documents.Load(ctx, s.DB, docID, false)
		if err != nil || d.RequesterMob == "" {
			return
		}
		body, _, err := flowsettings.Template(ctx, s.DB, "dlv")
		if err != nil {
			return
		}
		vals := map[string]string{"request": d.Number, "property": d.Property}
		for k, v := range extra {
			vals[k] = v
		}
		text, params := flowsettings.Render(body, vals)
		if _, err := s.Messages.WhatsApp(ctx, d.RequesterMob, "pd_dlv", params, text, d.Number, &u.ID); err != nil {
			log.Printf("delivery notice %s: %v", d.Number, err)
		}
	}()
}

func (s *Service) wait(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	id, err := httpjson.ID(r, "id")
	if err != nil {
		return err
	}
	var in struct{ Date, Note string }
	if err := httpjson.Decode(r, &in); err != nil {
		return err
	}
	day, ok := clock.ParseDate(in.Date)
	if !ok || day.Before(clock.Today()) {
		return httpjson.Bad("Choose a date from today onwards.")
	}
	err = pgx.BeginFunc(r.Context(), s.DB, func(tx pgx.Tx) error {
		d, g, err := lockTransfer(r.Context(), tx, u, id, documents.POIssued)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(r.Context(), `DELETE FROM delivery WHERE document_id=$1`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(r.Context(), `UPDATE document SET wait_until=$2, wait_note=$3, updated_at=now() WHERE id=$1`, id, day, strings.TrimSpace(in.Note)); err != nil {
			return err
		}
		return auditlog.Doc(r.Context(), tx, u, g.Name, d.ID, d.Number, "Warehouse", "Warehouse awaiting stock", "Expected by "+in.Date+" "+in.Note)
	})
	if err != nil {
		return err
	}
	return httpjson.OK(w, map[string]string{"message": "The property now sees stock is expected by " + in.Date})
}

// cannotSupply returns the order to the property and releases its budget. There is no second vendor to fall back on.
func (s *Service) cannotSupply(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	id, err := httpjson.ID(r, "id")
	if err != nil {
		return err
	}
	var in struct{ Reason, Note string }
	if err := httpjson.Decode(r, &in); err != nil {
		return err
	}
	if err := flowsettings.CheckReason(r.Context(), s.DB, "wh_back_reasons", in.Reason, in.Note); err != nil {
		return err
	}
	var number string
	err = pgx.BeginFunc(r.Context(), s.DB, func(tx pgx.Tx) error {
		d, g, err := lockTransfer(r.Context(), tx, u, id, documents.POIssued)
		if err != nil {
			return err
		}
		number = d.PONumber
		if _, err := tx.Exec(r.Context(), `UPDATE document SET status='WH_BACK', return_reason=$2, return_note=$3, updated_at=now() WHERE id=$1`, id, in.Reason, strings.TrimSpace(in.Note)); err != nil {
			return err
		}
		if !d.NoBudget() {
			release := map[int64]float64{}
			for k, v := range d.SubCatTotals() {
				release[k] = -v
			}
			if err := budgets.Commit(r.Context(), tx, d.PropertyID, d.ID, release); err != nil {
				return err
			}
		}
		return auditlog.Doc(r.Context(), tx, u, g.Name, d.ID, d.Number, "Warehouse", "Returned by warehouse, not supplied", in.Reason+" "+in.Note)
	})
	if err != nil {
		return err
	}
	return httpjson.OK(w, map[string]string{"message": number + " returned to the property"})
}

func (s *Service) dispatch(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	id, err := httpjson.ID(r, "id")
	if err != nil {
		return err
	}
	var in struct {
		Quantities map[int64]float64 `json:"quantities"`
		Balance    string            `json:"balance"` // keep or close
	}
	if err := httpjson.Decode(r, &in); err != nil {
		return err
	}
	st, err := flowsettings.Load(r.Context(), s.DB)
	if err != nil {
		return err
	}
	var msg string
	err = pgx.BeginFunc(r.Context(), s.DB, func(tx pgx.Tx) error {
		d, g, err := lockTransfer(r.Context(), tx, u, id, documents.POIssued)
		if err != nil {
			return err
		}
		if d.Delivery == nil && st.NeedSchedule {
			return httpjson.Bad("Schedule the delivery first, so the property knows when to expect it.")
		}
		if _, err := tx.Exec(r.Context(), `LOCK TABLE warehouse_stock IN SHARE ROW EXCLUSIVE MODE`); err != nil {
			return err
		}
		stock, _, err := Ledger(r.Context(), tx)
		if err != nil {
			return err
		}
		total := 0.0
		balance := []documents.Line{}
		for _, l := range d.Lines {
			q := in.Quantities[l.ID]
			if q < 0 || q > l.Qty {
				return httpjson.Bad("%s: dispatch cannot be more than the %v ordered.", l.ItemName, l.Qty)
			}
			have := 0.0
			if sv := stock[l.SKUID]; sv != nil {
				have = sv.Closing
			}
			if q > have {
				return httpjson.Bad("%s: only %v in stock.", l.ItemName, have)
			}
			total += q
			if l.Qty-q > 0 {
				bl := l
				bl.Qty = l.Qty - q
				balance = append(balance, bl)
			}
			if _, err := tx.Exec(r.Context(), `UPDATE document_line SET dispatched_qty=$2 WHERE id=$1`, l.ID, q); err != nil {
				return err
			}
		}
		if total == 0 {
			return httpjson.Bad("Nothing to dispatch. If there is no stock, set the expected date or return the order.")
		}
		if _, err := tx.Exec(r.Context(), `UPDATE document SET status='WH_SENT', dispatched_at=now(), updated_at=now() WHERE id=$1`, id); err != nil {
			return err
		}
		note := ""
		if len(balance) > 0 {
			if in.Balance == "close" {
				note = "Short dispatch. Balance closed as rejected"
				release := map[int64]float64{}
				for _, l := range balance {
					release[l.SubCatID] -= money.LineValue(l.Qty, l.Rate, l.GST)
				}
				if !d.NoBudget() {
					if err := budgets.Commit(r.Context(), tx, d.PropertyID, d.ID, release); err != nil {
						return err
					}
				}
			} else {
				var n int
				_ = tx.QueryRow(r.Context(), `SELECT count(*)+1 FROM document WHERE parent_id=$1`, id).Scan(&n)
				// The balance order is PR_HZ_0001_B1 with transfer WT_HZ_0001_B1 (older numbers: PR-1001-B1 and the next WT).
				number := fmt.Sprintf("%s-B%d", d.Number, n)
				wt, ok := documents.Derived(d.Number, "WT")
				if ok {
					number = fmt.Sprintf("%s_B%d", d.Number, n)
					wt, _ = documents.Derived(number, "WT")
				} else {
					var err error
					if wt, err = documents.NextNumber(r.Context(), tx, "wt_seq", "WT"); err != nil {
						return err
					}
				}
				var nid int64
				if err := tx.QueryRow(r.Context(), `INSERT INTO document(number,kind,property_id,vendor_id,requester_id,requester_step,status,route_steps,step_idx,approved_at,po_number,po_date,
					deliver_by,payment_terms,is_transfer,parent_id,created_by)
					SELECT $2, kind, property_id, vendor_id, requester_id, requester_step, 'PO_ISSUED', route_steps, step_idx, approved_at, $3, now(),
					current_date+1, payment_terms, TRUE, id, $4 FROM document WHERE id=$1 RETURNING id`, id, number, wt, u.ID).Scan(&nid); err != nil {
					return err
				}
				for _, l := range balance {
					if _, err := tx.Exec(r.Context(), `INSERT INTO document_line(document_id,sku_id,qty,rate,gst_pct) VALUES ($1,$2,$3,$4,$5)`, nid, l.SKUID, l.Qty, l.Rate, l.GST); err != nil {
						return err
					}
				}
				note = "Short dispatch. Balance kept open as " + wt
				if err := auditlog.Doc(r.Context(), tx, u, g.Name, nid, number, "Warehouse", "Balance of "+d.PONumber+" kept open as "+wt, ""); err != nil {
					return err
				}
			}
		}
		msg = d.PONumber + " dispatched"
		if note != "" {
			msg += ". " + note
		}
		return auditlog.Doc(r.Context(), tx, u, g.Name, d.ID, d.Number, "Warehouse", "Dispatched from warehouse", note)
	})
	if err != nil {
		return err
	}
	return httpjson.OK(w, map[string]string{"message": msg})
}

// receiveTransfer records what the property received; anything short of what was dispatched is booked as pilferage.
func (s *Service) receiveTransfer(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	id, err := httpjson.ID(r, "id")
	if err != nil {
		return err
	}
	if err := r.ParseMultipartForm(40 << 20); err != nil {
		return httpjson.Bad("Could not read the form: %v", err)
	}
	var in struct {
		Quantities map[int64]float64 `json:"quantities"`
	}
	if err := json.Unmarshal([]byte(r.FormValue("data")), &in); err != nil {
		return httpjson.Bad("Could not read the form data.")
	}
	photos := []*uploadedfiles.File{}
	for _, fh := range r.MultipartForm.File["photos"] {
		f, err := uploadedfiles.Read(fh)
		if err != nil {
			return err
		}
		photos = append(photos, f)
	}
	var msg string
	err = pgx.BeginFunc(r.Context(), s.DB, func(tx pgx.Tx) error {
		d, err := documents.Load(r.Context(), tx, id, true)
		if err != nil {
			return err
		}
		if !d.IsTransfer || d.Status != documents.WHSent {
			return httpjson.Conflict("%s is not in transit.", d.PONumber)
		}
		g := documents.TransferReceiver(u, d)
		if g == nil {
			return httpjson.Forbidden("Only the person who raised this order, or the receiving store at %s, can record this receipt.", d.Property)
		}
		short := []string{}
		for _, l := range d.Lines {
			sent := 0.0
			if l.Dispatched != nil {
				sent = *l.Dispatched
			}
			q, ok := in.Quantities[l.ID]
			if !ok {
				q = sent
			}
			if q < 0 || q > sent {
				return httpjson.Bad("%s: received cannot be more than the %v dispatched.", l.ItemName, sent)
			}
			if q < sent {
				short = append(short, fmt.Sprintf("%s short %v", l.ItemName, sent-q))
			}
			if _, err := tx.Exec(r.Context(), `UPDATE document_line SET received_qty=$2 WHERE id=$1`, l.ID, q); err != nil {
				return err
			}
		}
		photoIDs := []int64{}
		for _, p := range photos {
			pid, err := s.Files.Save(r.Context(), tx, u, p, "item-photos", false)
			if err != nil {
				return err
			}
			photoIDs = append(photoIDs, pid)
		}
		grn, ok := documents.Derived(d.Number, "GRN")
		if !ok {
			var err error
			if grn, err = documents.NextNumber(r.Context(), tx, "grn_seq", "GRN"); err != nil {
				return err
			}
		}
		var grnID int64
		if err := tx.QueryRow(r.Context(), `INSERT INTO goods_receipt(number,document_id,delivery_no,received_by,photo_file_ids,note) VALUES ($1,$2,1,$3,$4,'Warehouse transfer') RETURNING id`, grn, id, u.ID, photoIDs).Scan(&grnID); err != nil {
			return err
		}
		if _, err := tx.Exec(r.Context(), `UPDATE document SET status='WH_DONE', transfer_received_at=now(), updated_at=now() WHERE id=$1`, id); err != nil {
			return err
		}
		note := ""
		if len(short) > 0 {
			note = "Pilferage: " + strings.Join(short, "; ")
		}
		msg = d.PONumber + " received"
		if note != "" {
			msg += ". Shortage booked as pilferage"
		}
		return auditlog.Doc(r.Context(), tx, u, g.Name, d.ID, d.Number, "Warehouse", "Received from warehouse", note)
	})
	if err != nil {
		return err
	}
	return httpjson.OK(w, map[string]string{"message": msg})
}

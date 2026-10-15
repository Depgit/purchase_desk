package zohopush

// Purchase orders: every vendor PO is kept in step with a Zoho Books purchase order under the same number. Issuing a
// PO queues a create (documents.QueueZohoPO), a rejected balance or a PO closed short queues an update, and a PO
// cancelled with nothing delivered queues a cancel. The zoho-purchase-orders job sends what is queued every minute;
// "Sync now" sends one at once. A PO that cannot go yet (a missing mapping) keeps "Held: ..." and is tried again on
// every run; a refusal from Zoho is tried again after half an hour. Bills approved for payment are pushed the same
// way (zoho-bills), and a bill for a PO quotes the Zoho PO and its lines, so Zoho shows the PO as billed.

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"purchasedesk/internal/access"
	"purchasedesk/internal/auditlog"
	"purchasedesk/internal/clock"
	"purchasedesk/internal/database"
	"purchasedesk/internal/httpjson"
	"purchasedesk/internal/money"
	"purchasedesk/internal/zohoapi"
	"purchasedesk/internal/zohosync"
)

func (s *Service) poRoutes(r chi.Router) {
	r.Get("/zoho/purchase-orders", httpjson.Wrap(s.poList))
	r.Post("/zoho/purchase-orders/{id}/sync", httpjson.Wrap(s.poSyncNow))
}

// po is a vendor PO with what its Zoho purchase order needs, beyond the shared bill fields.
type po struct {
	*bill
	PODate, DeliverBy, Sync, Status string
	ClosedShort                     bool
}

// loadPO reads a PO with its mappings: the same property tag, vendor, accounts and taxes a bill uses. Lines carry the
// quantity still on order, or what was received once the PO is closed short.
func loadPO(ctx context.Context, q database.Q, docID int64) (*po, error) {
	b := &bill{DocID: docID}
	p := &po{bill: b}
	err := q.QueryRow(ctx, `SELECT d.number, d.kind, coalesce(d.po_number,''), coalesce(d.po_date::date::text,''), coalesce(d.deliver_by::text,''), d.zoho_po_sync, d.status, d.po_closed_short,
		coalesce(d.zoho_po_id,''), p.id, p.name, p.kind, c.state_code, v.id, v.gst_registered, coalesce(v.gstin,''), v.state_code, coalesce(nullif(d.payment_terms,''), v.payment_terms),
		coalesce(o.tag_id,''), coalesce(mp.tag_option_id,''), coalesce(mp.location_id,''), coalesce(o.active AND zt.active,false), coalesce(zt.name,''),
		coalesce(mv.contact_id,''), coalesce(zc.active,false), coalesce(z.connected,false), coalesce(cd.name,'')
		FROM document d JOIN property p ON p.id=d.property_id JOIN cluster k ON k.id=p.cluster_id JOIN city c ON c.id=k.city_id JOIN vendor v ON v.id=d.vendor_id
		LEFT JOIN zoho_connection z ON z.id=1 LEFT JOIN zoho_map_property mp ON mp.property_id=p.id LEFT JOIN zoho_tag_option o ON o.option_id=mp.tag_option_id LEFT JOIN zoho_tag zt ON zt.tag_id=o.tag_id
		LEFT JOIN zoho_map_vendor mv ON mv.vendor_id=v.id LEFT JOIN zoho_contact zc ON zc.contact_id=mv.contact_id LEFT JOIN corp_department cd ON cd.id=d.department_id
		WHERE d.id=$1`, docID).Scan(&b.DocNumber, &b.Kind, &b.PONumber, &p.PODate, &p.DeliverBy, &p.Sync, &p.Status, &p.ClosedShort,
		&b.ZohoPOID, &b.PropertyID, &b.Property, &b.PropertyKind, &b.PropertyState, &b.VendorID, &b.VendorGST, &b.VendorGSTIN, &b.VendorState, &b.Terms,
		&b.TagID, &b.TagOptionID, &b.LocationID, &b.TagOK, &b.TagName, &b.ContactID, &b.ContactOK, &b.Connected, &b.Department)
	if err == pgx.ErrNoRows {
		return nil, httpjson.NotFound("Purchase order")
	}
	if err != nil {
		return nil, err
	}
	special := map[string]string{"warehouse": "warehouse", "preopening": "preopening"}[b.PropertyKind]
	rows, err := q.Query(ctx, `SELECT l.id, coalesce(l.zoho_po_line_id,''), coalesce(k.code,''), coalesce(concat_ws(' ', b.name, k.item_name), l.description), coalesce(ps.name, l.unit), coalesce(k.hsn,''),
		coalesce(sc.name, eh.name, ''), coalesce(sc.id,0), CASE WHEN $3 THEN l.received_qty ELSE l.qty END, l.rate, l.gst_pct,
		coalesce(CASE WHEN l.sku_id IS NULL THEN mh.account_id WHEN $2<>'' THEN ms.account_id ELSE ma.account_id END,''),
		coalesce(CASE WHEN l.sku_id IS NULL THEN zh.active WHEN $2<>'' THEN zs.active ELSE za.active END,false),
		coalesce(zi.item_id,'')
		FROM document_line l LEFT JOIN sku k ON k.id=l.sku_id LEFT JOIN brand b ON b.id=k.brand_id LEFT JOIN pack_size ps ON ps.id=k.pack_size_id LEFT JOIN sub_category sc ON sc.id=k.sub_category_id
		LEFT JOIN expense_head eh ON eh.id=l.expense_head_id
		LEFT JOIN zoho_map_account mh ON mh.kind='head' AND mh.ref=l.expense_head_id::text LEFT JOIN zoho_account zh ON zh.account_id=mh.account_id
		LEFT JOIN zoho_map_account ma ON ma.kind='subcat' AND ma.ref=sc.id::text LEFT JOIN zoho_account za ON za.account_id=ma.account_id
		LEFT JOIN zoho_map_account ms ON ms.kind='special' AND ms.ref=$2 LEFT JOIN zoho_account zs ON zs.account_id=ms.account_id
		LEFT JOIN zoho_map_item mi ON mi.sku_id=l.sku_id LEFT JOIN zoho_item zi ON zi.item_id=mi.item_id AND zi.active
		WHERE l.document_id=$1 AND CASE WHEN $3 THEN l.received_qty ELSE l.qty END > 0 ORDER BY l.id`, docID, special, p.ClosedShort)
	if err != nil {
		return nil, err
	}
	b.Lines, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (line, error) {
		var l line
		return l, row.Scan(&l.LineID, &l.POLineID, &l.SKUCode, &l.Item, &l.Pack, &l.HSN, &l.SubCat, &l.SubCatID, &l.Qty, &l.Rate, &l.GST, &l.Account, &l.AccountOK, &l.ItemID)
	})
	return p, err
}

// poPayload builds the Zoho purchase order as it will be sent. Field names follow the Zoho Books API, India edition.
func poPayload(p *po, taxes map[float64]string) map[string]any {
	b := p.bill
	reg := b.VendorGST
	tags := []map[string]string{{"tag_id": b.TagID, "tag_option_id": b.TagOptionID}}
	items := []map[string]any{}
	for _, l := range b.Lines {
		desc := l.SKUCode + " · " + l.Pack
		if l.SKUCode == "" {
			desc = "Department " + b.Department + " · " + l.SubCat
		}
		it := map[string]any{"account_id": l.Account, "name": l.Item, "description": desc, "quantity": l.Qty, "rate": l.Rate, "tags": tags}
		if l.ItemID != "" {
			it["item_id"] = l.ItemID
		}
		if l.POLineID != "" {
			it["line_item_id"] = l.POLineID // an update keeps Zoho's line, so bills already made against it stay linked
		}
		if l.HSN != "" {
			it["hsn_or_sac"] = l.HSN
		}
		if reg && (l.GST > 0 || taxes[l.GST] != "") {
			it["tax_id"] = taxes[l.GST]
		}
		items = append(items, it)
	}
	gstTreatment := "business_none"
	if reg {
		gstTreatment = "business_gst"
	}
	days := money.TermsDays(b.Terms)
	if d, ok := clock.ParseDate(p.PODate); ok {
		days = int(money.DueDate(d, b.Terms).Sub(d).Hours()/24 + 0.5)
	}
	out := map[string]any{
		"vendor_id": b.ContactID, "purchaseorder_number": b.PONumber, "reference_number": b.DocNumber, "date": p.PODate,
		"payment_terms": days, "payment_terms_label": b.Terms, "gst_treatment": gstTreatment, "tags": tags, "is_inclusive_tax": false,
		"line_items": items, "notes": "Purchase Desk " + b.DocNumber + " · " + b.Property,
	}
	if p.DeliverBy != "" {
		out["delivery_date"] = p.DeliverBy
	}
	if reg {
		out["gst_no"] = b.VendorGSTIN
		out["source_of_supply"] = b.VendorState
		out["destination_of_supply"] = b.PropertyState
	}
	if b.LocationID != "" {
		out["location_id"] = b.LocationID
	}
	return out
}

// syncPO sends one queued PO change to Zoho and records the outcome on the document. u is nil for the job.
func (s *Service) syncPO(ctx context.Context, u *access.User, c *zohoapi.Client, docID int64) (string, error) {
	p, err := loadPO(ctx, s.DB, docID)
	if err != nil {
		return "", err
	}
	b := p.bill
	record := func(msg string) {
		_, _ = s.DB.Exec(ctx, `UPDATE document SET zoho_po_error=$2, zoho_po_at=now() WHERE id=$1`, docID, msg)
	}
	done := func(id, action string, z zohoapi.PurchaseOrder) (string, error) {
		err := pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `UPDATE document SET zoho_po_id=$2, zoho_po_sync='', zoho_po_error='', zoho_po_at=now() WHERE id=$1`, docID, id); err != nil {
				return err
			}
			// Zoho returns the lines in the order they were sent: keep each line's Zoho id for the bills.
			for i, l := range b.Lines {
				if i < len(z.LineItems) && z.LineItems[i].ID != "" {
					if _, err := tx.Exec(ctx, `UPDATE document_line SET zoho_po_line_id=$2 WHERE id=$1`, l.LineID, z.LineItems[i].ID); err != nil {
						return err
					}
				}
			}
			return auditlog.Doc(ctx, tx, u, "Finance", docID, b.DocNumber, "Zoho", "Zoho purchase order "+action+" · "+b.PONumber, "Zoho id "+id)
		})
		return "Zoho purchase order " + action + ": " + b.PONumber, err
	}
	switch p.Sync {
	case "":
		return "Already in step with Zoho", nil
	case "cancel":
		if b.ZohoPOID != "" {
			if err := c.MarkPurchaseOrderCancelled(ctx, b.ZohoPOID); err != nil {
				record("Cancelling in Zoho: " + err.Error())
				return "", err
			}
		}
		return done(b.ZohoPOID, "cancelled", zohoapi.PurchaseOrder{})
	}
	taxes, err := taxIDs(ctx, s.DB, b)
	if err != nil {
		return "", err
	}
	if pr := problems(b, taxes); len(pr) > 0 {
		record("Held: " + strings.Join(pr, "; "))
		return "", fmt.Errorf("held: %s", strings.Join(pr, "; "))
	}
	body := poPayload(p, taxes)
	send := func(body map[string]any) (zohoapi.PurchaseOrder, error) {
		if b.ZohoPOID != "" {
			return c.UpdatePurchaseOrder(ctx, b.ZohoPOID, body)
		}
		// A retry after a lost reply must not create the PO twice: look it up by number first.
		if found, err := c.FindPurchaseOrder(ctx, b.PONumber); err != nil {
			return zohoapi.PurchaseOrder{}, err
		} else if found != nil {
			b.ZohoPOID = found.ID
			return c.UpdatePurchaseOrder(ctx, found.ID, body)
		}
		return c.CreatePurchaseOrder(ctx, body)
	}
	z, err := send(body)
	// As with bills, a reporting tag allowed only on lines (or only on the whole document) is refused at the other level.
	for _, level := range []string{"line", "bill"} {
		if err == nil || !tagLevelRefused(err) {
			break
		}
		z, err = send(tagsAt(body, level))
	}
	if err != nil {
		if tagLevelRefused(err) {
			err = fmt.Errorf("%w. In Zoho, open Settings, Reporting Tags, %s, and switch it on for Purchases (purchase orders)", err, b.TagName)
		}
		record("Zoho refused it: " + err.Error())
		return "", err
	}
	action := "updated"
	if p.Sync == "create" && b.ZohoPOID == "" {
		action = "created"
	}
	if z.Status == "draft" {
		if err := c.MarkPurchaseOrderOpen(ctx, z.ID); err != nil {
			record("Marking it open in Zoho: " + err.Error())
			return "", err
		}
	}
	if len(z.LineItems) == 0 {
		if full, err := c.GetPurchaseOrder(ctx, z.ID); err == nil {
			z.LineItems = full.LineItems
		}
	}
	return done(z.ID, action, z)
}

// SyncPOs sends every queued PO change: the zoho-purchase-orders job. Held POs are checked again on every run (they
// cost no Zoho call until their mapping is in place); a PO Zoho refused waits half an hour before the next try.
func (s *Service) SyncPOs(ctx context.Context) error {
	if !s.PushEnabled || !s.Sync.Connected(ctx) {
		return nil
	}
	c, _, err := s.Sync.Client(ctx)
	if err != nil {
		return err
	}
	rows, err := s.DB.Query(ctx, `SELECT id FROM document WHERE zoho_po_sync<>'' AND (zoho_po_error='' OR zoho_po_error LIKE 'Held:%' OR zoho_po_at < now() - interval '30 minutes')
		ORDER BY id LIMIT 50`)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := s.syncPO(ctx, nil, c, id); err != nil && stop(s.Sync, ctx, err) {
			return err
		}
	}
	return nil
}

// AutoPushBills pushes bills approved for payment without waiting for the button: the zoho-bills job. The checks are
// the same as for a push by hand; a bill held for a mapping is checked again on every run, one Zoho refused waits half
// an hour.
func (s *Service) AutoPushBills(ctx context.Context) error {
	if !s.PushEnabled || !s.Sync.Connected(ctx) {
		return nil
	}
	c, conn, err := s.Sync.Client(ctx)
	if err != nil {
		return err
	}
	rows, err := s.DB.Query(ctx, `SELECT id FROM invoice WHERE status='PAY_APPROVED' AND zoho_step<3
		AND (zoho_error='' OR zoho_error LIKE 'Held:%' OR zoho_tried_at IS NULL OR zoho_tried_at < now() - interval '30 minutes') ORDER BY id LIMIT 20`)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil || len(ids) == 0 {
		return err
	}
	useKey := s.billKey(ctx, c, conn)
	for _, id := range ids {
		_, _ = s.DB.Exec(ctx, `UPDATE invoice SET zoho_tried_at=now() WHERE id=$1`, id)
		if _, err := s.pushOne(ctx, nil, c, id, useKey); err != nil && stop(s.Sync, ctx, err) {
			return err
		}
	}
	return nil
}

// stop says whether a run should end after this error: Zoho's daily limit, or a revoked connection.
func stop(sync *zohosync.Service, ctx context.Context, err error) bool {
	if zohoapi.DailyLimit(err) {
		return true
	}
	if err == zohoapi.ErrTokenRevoked {
		sync.Fail(ctx, err)
		return true
	}
	return false
}

// poSyncNow sends one PO's queued change at once (Finance, the System Admin or Purchase).
func (s *Service) poSyncNow(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	if !(canPush(u) || u.HasRight(access.IssuePO)) {
		return httpjson.Forbidden("Only Finance, Purchase or the System Admin can sync a PO with Zoho.")
	}
	if !s.PushEnabled {
		return httpjson.Bad("Sending to Zoho is switched off on this server (ZOHO_PUSH_ENABLED=false).")
	}
	id, err := httpjson.ID(r, "id")
	if err != nil {
		return err
	}
	c, _, err := s.Sync.Client(r.Context())
	if err != nil {
		return err
	}
	msg, err := s.syncPO(r.Context(), u, c, id)
	if err != nil {
		return httpjson.Bad("%s", err.Error())
	}
	return httpjson.OK(w, map[string]string{"message": msg})
}

type poRow struct {
	ID       int64  `json:"id"`
	Number   string `json:"number"`
	PO       string `json:"po_number"`
	Vendor   string `json:"vendor"`
	Property string `json:"property"`
	Sync     string `json:"sync"`
	Error    string `json:"error"`
	ZohoID   string `json:"zoho_po_id"`
	At       string `json:"at"`
}

// poList gives the POs still to send to Zoho, those held or refused, and the last ones sent.
func (s *Service) poList(w http.ResponseWriter, r *http.Request) error {
	rows, err := s.DB.Query(r.Context(), `SELECT d.id, d.number, coalesce(d.po_number,''), v.legal_name, p.name, d.zoho_po_sync, d.zoho_po_error, coalesce(d.zoho_po_id,''),
		coalesce(to_char(d.zoho_po_at AT TIME ZONE 'Asia/Kolkata','DD Mon HH24:MI'),'')
		FROM document d JOIN vendor v ON v.id=d.vendor_id JOIN property p ON p.id=d.property_id
		WHERE d.zoho_po_sync<>'' OR d.zoho_po_error<>'' OR (d.zoho_po_id IS NOT NULL AND d.zoho_po_at > now() - interval '30 days')
		ORDER BY (d.zoho_po_sync<>'') DESC, d.zoho_po_at DESC NULLS FIRST LIMIT 200`)
	if err != nil {
		return err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (poRow, error) {
		var x poRow
		return x, row.Scan(&x.ID, &x.Number, &x.PO, &x.Vendor, &x.Property, &x.Sync, &x.Error, &x.ZohoID, &x.At)
	})
	if err != nil {
		return err
	}
	return httpjson.OK(w, map[string]any{"enabled": s.PushEnabled, "purchase_orders": out})
}

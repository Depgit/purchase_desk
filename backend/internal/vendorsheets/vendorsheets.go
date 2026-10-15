// Package vendorsheets builds each vendor's order sheet: one Excel file covering every property it serves,
// so it can plan supply. Only approved quantities and rates are shared; requests still in approval show as a status only.
// Sheets go by email (with the file) and WhatsApp, manually or automatically at the vendor's set time each day.
package vendorsheets

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
	"purchasedesk/internal/clock"
	"purchasedesk/internal/documents"
	"purchasedesk/internal/flowsettings"
	"purchasedesk/internal/httpjson"
	"purchasedesk/internal/messaging"
	"purchasedesk/internal/money"
	"purchasedesk/internal/spreadsheet"
	"purchasedesk/internal/textclean"
)

type Service struct {
	DB       *pgxpool.Pool
	Messages *messaging.Sender
}

func (s *Service) Routes(r chi.Router) {
	r.Get("/vendor-sheets", httpjson.Wrap(s.list))
	r.Get("/vendor-sheets/{id}", httpjson.Wrap(s.preview))
	r.Get("/vendor-sheets/{id}/xlsx", httpjson.Wrap(s.xlsx))
	r.Post("/vendor-sheets/{id}/send", httpjson.Wrap(s.send))
}

type vendorInfo struct {
	ID                        int64
	Code, Name, Email, Mobile string
	Mode, Time, Channel       string
}

// Sheet is everything in one vendor's order sheet.
type Sheet struct {
	VendorID   int64               `json:"vendor_id"`
	Vendor     string              `json:"vendor"`
	Version    int                 `json:"version"`
	Confirmed  int                 `json:"confirmed_pos"`
	ConfValue  float64             `json:"confirmed_value"`
	Approved   int                 `json:"approved_without_po"`
	ApprValue  float64             `json:"approved_value"`
	Waiting    int                 `json:"in_approval"`
	Properties int                 `json:"properties_confirmed"`
	PONumbers  []string            `json:"po_numbers"`
	NewPOs     int                 `json:"new_since_last"`
	DueDates   []string            `json:"due_dates"`
	Tabs       []spreadsheet.Sheet `json:"tabs"`
	FileName   string              `json:"file_name"`
}

func slug(s string) string {
	s = strings.ReplaceAll(s, "&", "and")
	out := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			return r
		}
		return '-'
	}, s)
	for strings.Contains(out, "--") {
		out = strings.ReplaceAll(out, "--", "-")
	}
	return strings.Trim(out, "-")
}

// Build assembles the vendor's sheet from approved requests and issued POs.
func (s *Service) Build(ctx context.Context, vendorID int64) (*Sheet, *vendorInfo, error) {
	v := &vendorInfo{ID: vendorID}
	if err := s.DB.QueryRow(ctx, `SELECT code, legal_name, email, mobile, sheet_mode, sheet_time, sheet_channel FROM vendor WHERE id=$1`, vendorID).
		Scan(&v.Code, &v.Name, &v.Email, &v.Mobile, &v.Mode, &v.Time, &v.Channel); err != nil {
		return nil, nil, httpjson.NotFound("Vendor")
	}
	st, err := flowsettings.Load(ctx, s.DB)
	if err != nil {
		return nil, nil, err
	}
	docs, err := documents.LoadMany(ctx, s.DB, `WHERE d.vendor_id=$1 AND d.kind='purchase' AND NOT d.is_transfer AND d.status IN ('PENDING','RETURNED','APPROVED','PO_ISSUED') ORDER BY d.created_at`, vendorID)
	if err != nil {
		return nil, nil, err
	}
	var lastPOs []string
	var version int
	_ = s.DB.QueryRow(ctx, `SELECT count(*)+1 FROM vendor_sheet_log WHERE vendor_id=$1`, vendorID).Scan(&version)
	_ = s.DB.QueryRow(ctx, `SELECT po_numbers FROM vendor_sheet_log WHERE vendor_id=$1 ORDER BY sent_at DESC LIMIT 1`, vendorID).Scan(&lastPOs)
	sh := &Sheet{VendorID: vendorID, Vendor: v.Name, Version: version, PONumbers: []string{}, DueDates: []string{}}
	type cell struct{ conf, appr float64 }
	type skuInfo struct {
		code, brand, item, pack string
		rate, gst               float64
		conf, appr              float64
	}
	skus := map[int64]*skuInfo{}
	byProp := map[int64][]*documents.Doc{}
	propName := map[int64]string{}
	matrix := map[[2]int64]*cell{}
	for _, d := range docs {
		propName[d.PropertyID] = d.Property
		byProp[d.PropertyID] = append(byProp[d.PropertyID], d)
		include := d.Status == documents.POIssued || (d.Status == documents.Approved && st.SheetApproved)
		switch d.Status {
		case documents.POIssued:
			sh.Confirmed++
			sh.PONumbers = append(sh.PONumbers, d.PONumber)
			if !textclean.Contains(lastPOs, d.PONumber) {
				sh.NewPOs++
			}
			if d.DeliverBy != "" && !textclean.Contains(sh.DueDates, d.DeliverBy) {
				sh.DueDates = append(sh.DueDates, d.DeliverBy)
			}
		case documents.Approved:
			if st.SheetApproved {
				sh.Approved++
			}
		default:
			sh.Waiting++
		}
		if !include {
			continue
		}
		for _, l := range d.Lines {
			q := l.Qty - l.ReceivedQty
			if q <= 0 {
				continue
			}
			k := skus[l.SKUID]
			if k == nil {
				k = &skuInfo{code: l.SKUCode, brand: l.Brand, item: l.ItemName, pack: l.PackSize, rate: l.Rate, gst: l.GST}
				skus[l.SKUID] = k
			}
			c := matrix[[2]int64{l.SKUID, d.PropertyID}]
			if c == nil {
				c = &cell{}
				matrix[[2]int64{l.SKUID, d.PropertyID}] = c
			}
			v := money.LineValue(q, l.Rate, l.GST)
			if d.Status == documents.POIssued {
				k.conf += q
				c.conf += q
				sh.ConfValue += v
			} else {
				k.appr += q
				c.appr += q
				sh.ApprValue += v
			}
		}
	}
	sort.Strings(sh.DueDates)
	sh.ConfValue, sh.ApprValue = money.Round2(sh.ConfValue), money.Round2(sh.ApprValue)
	// Every property the vendor serves, through its mappings.
	rows, err := s.DB.Query(ctx, `SELECT DISTINCT p.id, p.name, c.name FROM property p JOIN cluster k ON k.id=p.cluster_id JOIN city c ON c.id=k.city_id
		WHERE p.active AND EXISTS (SELECT 1 FROM vendor_mapping m WHERE m.vendor_id=$1 AND m.active AND (m.all_properties OR EXISTS (SELECT 1 FROM vendor_mapping_property mp WHERE mp.mapping_id=m.id AND mp.property_id=p.id)))
		ORDER BY p.name`, vendorID)
	if err != nil {
		return nil, nil, err
	}
	type prop struct {
		id         int64
		name, city string
	}
	served, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (prop, error) {
		var p prop
		return p, row.Scan(&p.id, &p.name, &p.city)
	})
	if err != nil {
		return nil, nil, err
	}
	seen := map[int64]bool{}
	for _, p := range served {
		seen[p.id] = true
	}
	for id, n := range propName {
		if !seen[id] {
			served = append(served, prop{id: id, name: n})
		}
	}
	status := [][]any{}
	for _, p := range served {
		ds := byProp[p.id]
		var pos, due []string
		items, conf, appr := 0, 0.0, 0.0
		hasAppr, hasWait, isNew := false, false, false
		for _, d := range ds {
			switch d.Status {
			case documents.POIssued:
				pos = append(pos, d.PONumber)
				if d.DeliverBy != "" {
					due = append(due, d.DeliverBy)
				}
				items += len(d.Lines)
				conf += d.Total
				if !textclean.Contains(lastPOs, d.PONumber) {
					isNew = true
				}
			case documents.Approved:
				hasAppr = true
				appr += d.Total
			default:
				hasWait = true
			}
		}
		state := "No order in this cycle"
		switch {
		case len(pos) > 0 && hasAppr && st.SheetApproved:
			state = "Confirmed, and more approved with PO to follow"
		case len(pos) > 0 && hasWait:
			state = "Confirmed, and another request in approval"
		case len(pos) > 0:
			state = "Confirmed, PO issued"
		case hasAppr && st.SheetApproved:
			state = "Approved, PO to follow"
		case hasWait:
			state = "In approval, not yet approved"
		}
		included := "No"
		if len(pos) > 0 {
			included = "Yes"
			sh.Properties++
		}
		change := ""
		if isNew {
			change = "New since last sheet"
		}
		if !st.SheetApproved {
			appr = 0
		}
		status = append(status, []any{p.name, p.city, included, state, strings.Join(pos, ", "), strings.Join(due, ", "), items, money.Round2(conf), money.Round2(appr), change})
	}
	sort.SliceStable(status, func(i, j int) bool { return status[i][2].(string) > status[j][2].(string) })
	ids := make([]int64, 0, len(skus))
	for id := range skus {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return skus[ids[i]].code < skus[ids[j]].code })
	totals := [][]any{}
	for _, id := range ids {
		k := skus[id]
		totals = append(totals, []any{k.code, k.brand, k.item, k.pack, k.conf, k.appr, k.conf + k.appr, k.rate, k.gst})
	}
	mat := func(pick func(*cell) float64) spreadsheet.Sheet {
		props := []int64{}
		for _, p := range served {
			for _, id := range ids {
				if c := matrix[[2]int64{id, p.id}]; c != nil && pick(c) > 0 {
					props = append(props, p.id)
					break
				}
			}
		}
		heads := []string{"SKU", "Brand", "Item", "Pack size"}
		for _, p := range props {
			heads = append(heads, propName[p])
		}
		heads = append(heads, "Total")
		out := [][]any{}
		for _, id := range ids {
			k := skus[id]
			row := []any{k.code, k.brand, k.item, k.pack}
			t := 0.0
			for _, p := range props {
				q := 0.0
				if c := matrix[[2]int64{id, p}]; c != nil {
					q = pick(c)
				}
				t += q
				row = append(row, q)
			}
			if t > 0 {
				out = append(out, append(row, t))
			}
		}
		return spreadsheet.Sheet{Heads: heads, Rows: out}
	}
	prev := "first sheet"
	if version > 1 {
		var at time.Time
		_ = s.DB.QueryRow(ctx, `SELECT sent_at FROM vendor_sheet_log WHERE vendor_id=$1 ORDER BY sent_at DESC LIMIT 1`, vendorID).Scan(&at)
		prev = "previous sheet sent " + at.In(clock.IST).Format("2 Jan 2006 15:04")
	}
	summary := spreadsheet.Sheet{Name: "Summary", Heads: []string{"Order sheet for", v.Name}, Rows: [][]any{
		{"Vendor code", v.Code}, {"Prepared on", clock.Now().Format("2 Jan 2006 15:04")}, {"Version", fmt.Sprintf("%d, %s", version, prev)},
		{"Properties with a confirmed PO", sh.Properties}, {"Confirmed POs", sh.Confirmed}, {"Confirmed value incl. GST", sh.ConfValue},
		{"Approved requests, PO to follow", sh.Approved}, {"Approved value incl. GST, PO to follow", sh.ApprValue}, {"Requests still in approval", sh.Waiting},
		{"How to read this file", "Supply only against confirmed PO numbers. Approved quantities have cleared approval and their POs will follow. Requests still in approval are shown by status only."},
	}}
	confirmed := mat(func(c *cell) float64 { return c.conf })
	confirmed.Name = "Property x SKU confirmed"
	sh.Tabs = []spreadsheet.Sheet{summary,
		{Name: "Property status", Heads: []string{"Property", "City", "Order included", "Status", "PO number", "Deliver by", "Lines", "Confirmed value", "Approved value, PO to follow", "Change"}, Rows: status},
		{Name: "SKU totals", Heads: []string{"SKU", "Brand", "Item", "Pack size", "Confirmed qty (PO issued)", "Approved qty (PO to follow)", "Total qty", "Rate ex GST", "GST %"}, Rows: totals},
		confirmed}
	if st.SheetApproved {
		ap := mat(func(c *cell) float64 { return c.appr })
		ap.Name = "Property x SKU approved"
		sh.Tabs = append(sh.Tabs, ap)
	}
	for _, p := range served {
		rows := [][]any{}
		for _, d := range byProp[p.id] {
			if d.Status != documents.POIssued {
				continue
			}
			for _, l := range d.Lines {
				q := l.Qty - l.ReceivedQty
				if q > 0 {
					rows = append(rows, []any{d.PONumber, d.DeliverBy, l.SKUCode, l.Brand, l.ItemName, l.PackSize, q, l.Rate, l.GST, money.Round2(money.LineValue(q, l.Rate, l.GST))})
				}
			}
		}
		if len(rows) > 0 {
			name := p.name
			if i := strings.Index(name, ","); i > 0 {
				name = name[:i]
			}
			sh.Tabs = append(sh.Tabs, spreadsheet.Sheet{Name: name, Heads: []string{"PO number", "Deliver by", "SKU", "Brand", "Item", "Pack size", "Quantity", "Rate ex GST", "GST %", "Amount incl. GST"}, Rows: rows})
		}
	}
	sh.FileName = fmt.Sprintf("Order-sheet-%s-%s-v%d.xlsx", slug(v.Name), clock.YMD(clock.Now()), version)
	return sh, v, nil
}

type listRow struct {
	VendorID  int64      `json:"vendor_id"`
	Code      string     `json:"code"`
	Vendor    string     `json:"vendor"`
	Confirmed int        `json:"confirmed_pos"`
	Props     int        `json:"properties_confirmed"`
	ConfValue float64    `json:"confirmed_value"`
	Approved  int        `json:"approved_without_po"`
	Waiting   int        `json:"in_approval"`
	Mode      string     `json:"sheet_mode"`
	Time      string     `json:"sheet_time"`
	Channel   string     `json:"sheet_channel"`
	LastSent  *time.Time `json:"last_sent_at"`
	LastVer   int        `json:"last_version"`
	Unsent    int        `json:"new_pos"`
}

func (s *Service) list(w http.ResponseWriter, r *http.Request) error {
	rows, err := s.DB.Query(r.Context(), `SELECT v.id, v.code, v.legal_name, v.sheet_mode, v.sheet_time, v.sheet_channel,
		(SELECT max(sent_at) FROM vendor_sheet_log l WHERE l.vendor_id=v.id), (SELECT count(*) FROM vendor_sheet_log l WHERE l.vendor_id=v.id)
		FROM vendor v WHERE v.active AND v.vendor_type='goods' AND EXISTS (SELECT 1 FROM document d WHERE d.vendor_id=v.id AND d.kind='purchase' AND d.status IN ('PENDING','RETURNED','APPROVED','PO_ISSUED'))
		ORDER BY v.legal_name`)
	if err != nil {
		return err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (listRow, error) {
		var x listRow
		return x, row.Scan(&x.VendorID, &x.Code, &x.Vendor, &x.Mode, &x.Time, &x.Channel, &x.LastSent, &x.LastVer)
	})
	if err != nil {
		return err
	}
	for i := range out {
		sh, _, err := s.Build(r.Context(), out[i].VendorID)
		if err != nil {
			return err
		}
		out[i].Confirmed, out[i].Props, out[i].ConfValue, out[i].Approved, out[i].Waiting, out[i].Unsent = sh.Confirmed, sh.Properties, sh.ConfValue, sh.Approved, sh.Waiting, sh.NewPOs
	}
	return httpjson.OK(w, out)
}

func (s *Service) message(ctx context.Context, sh *Sheet, code string) (string, []string) {
	body, _, err := flowsettings.Template(ctx, s.DB, code)
	if err != nil {
		return "", nil
	}
	pending := ""
	if sh.Approved > 0 {
		pending = fmt.Sprintf("Approved, PO to follow: %d requests, value %s.", sh.Approved, money.INR(sh.ApprValue))
	}
	due := strings.Join(sh.DueDates, ", ")
	if due == "" {
		due = "as per PO"
	}
	return flowsettings.Render(body, map[string]string{"vendor": sh.Vendor, "version": fmt.Sprint(sh.Version), "po_count": fmt.Sprint(sh.Confirmed),
		"properties": fmt.Sprint(sh.Properties), "value": money.INR(sh.ConfValue), "due": due, "pending_line": pending})
}

func (s *Service) preview(w http.ResponseWriter, r *http.Request) error {
	id, err := httpjson.ID(r, "id")
	if err != nil {
		return err
	}
	sh, v, err := s.Build(r.Context(), id)
	if err != nil {
		return err
	}
	email, _ := s.message(r.Context(), sh, "vse")
	wa, _ := s.message(r.Context(), sh, "vsw")
	return httpjson.OK(w, map[string]any{"sheet": sh, "email": v.Email, "mobile": v.Mobile, "email_body": email, "whatsapp_body": wa,
		"sending": map[string]string{"mode": v.Mode, "time": v.Time, "channel": v.Channel}, "email_ready": s.Messages.EmailReady(), "whatsapp_ready": s.Messages.WhatsAppReady()})
}

func (s *Service) xlsx(w http.ResponseWriter, r *http.Request) error {
	id, err := httpjson.ID(r, "id")
	if err != nil {
		return err
	}
	sh, _, err := s.Build(r.Context(), id)
	if err != nil {
		return err
	}
	b, err := spreadsheet.Build(sh.Tabs)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, sh.FileName))
	_, err = w.Write(b)
	return err
}

// Send emails the file and sends the WhatsApp summary, then logs what was included.
func (s *Service) Send(ctx context.Context, u *access.User, vendorID int64, channel string, automatic bool) (string, error) {
	sh, v, err := s.Build(ctx, vendorID)
	if err != nil {
		return "", err
	}
	if sh.Confirmed == 0 && sh.Approved == 0 {
		return "", httpjson.Bad("%s has no confirmed or approved order to send.", v.Name)
	}
	var by *int64
	if u != nil {
		by = &u.ID
	}
	results := []string{}
	if channel == "email" || channel == "both" {
		if v.Email == "" {
			return "", httpjson.Bad("%s has no email address on the vendor master.", v.Name)
		}
		b, err := spreadsheet.Build(sh.Tabs)
		if err != nil {
			return "", err
		}
		body, _ := s.message(ctx, sh, "vse")
		subject := fmt.Sprintf("Saltstayz order sheet v%d: %d confirmed POs for %d properties", sh.Version, sh.Confirmed, sh.Properties)
		st, err := s.Messages.Email(ctx, v.Email, subject, body, []messaging.Attachment{{Name: sh.FileName, ContentType: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", Data: b}}, v.Code, by)
		if err != nil {
			return "", httpjson.Bad("Email failed: %v", err)
		}
		results = append(results, "email "+st)
	}
	if channel == "whatsapp" || channel == "both" {
		if v.Mobile == "" {
			return "", httpjson.Bad("%s has no mobile number on the vendor master.", v.Name)
		}
		text, params := s.message(ctx, sh, "vsw")
		st, err := s.Messages.WhatsApp(ctx, v.Mobile, "pd_vsw", params, text, v.Code, by)
		if err != nil {
			return "", httpjson.Bad("WhatsApp failed: %v", err)
		}
		results = append(results, "WhatsApp "+st)
	}
	if _, err := s.DB.Exec(ctx, `INSERT INTO vendor_sheet_log(vendor_id,version,channel,po_numbers,pending,automatic,status,sent_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		vendorID, sh.Version, channel, sh.PONumbers, sh.Waiting, automatic, strings.Join(results, ", "), by); err != nil {
		return "", err
	}
	how := "sent"
	if automatic {
		how = "sent automatically"
	}
	// Every PO in the sheet has now reached the vendor: "Push to vendor" becomes "Sent to vendor".
	rows, err := s.DB.Query(ctx, `UPDATE document SET sent_to_vendor_at=now() WHERE vendor_id=$1 AND po_number = ANY($2) AND sent_to_vendor_at IS NULL
		RETURNING id, number, po_number`, vendorID, sh.PONumbers)
	if err != nil {
		return "", err
	}
	type pushed struct {
		ID         int64
		Number, PO string
	}
	sent, err := pgx.CollectRows(rows, pgx.RowToStructByPos[pushed])
	if err != nil {
		return "", err
	}
	for _, p := range sent {
		_ = auditlog.Doc(ctx, s.DB, u, "", p.ID, p.Number, "Vendor Orders", "Sent to vendor", fmt.Sprintf("%s in order sheet v%d (%s)", p.PO, sh.Version, strings.Join(results, ", ")))
	}
	_ = auditlog.Write(ctx, s.DB, u, "Vendor Orders", fmt.Sprintf("Order sheet v%d %s (%s)", sh.Version, how, strings.Join(results, ", ")), v.Code, fmt.Sprintf("%s · %d confirmed POs", v.Name, sh.Confirmed))
	return fmt.Sprintf("Order sheet v%d for %s: %s", sh.Version, v.Name, strings.Join(results, ", ")), nil
}

func (s *Service) send(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	// Whoever approves requests sends the order sheets (there is no Purchase Team step): the Cluster Manager, the Cost
	// Controller, the pre-opening team, and Purchase or the Admin where they still work.
	if !u.IsAdmin && !u.HasStep("cc") && !u.HasStep("l2") && !u.HasStep("preop") && !u.HasStep("wh") && !u.HasStep("purchase") {
		return httpjson.Forbidden("Whoever approves purchase requests (Cluster Manager, Cost Controller, pre-opening team, Warehouse Manager) sends order sheets.")
	}
	id, err := httpjson.ID(r, "id")
	if err != nil {
		return err
	}
	var in struct {
		Channel string `json:"channel"`
	}
	if err := httpjson.Decode(r, &in); err != nil {
		return err
	}
	if !textclean.Contains([]string{"email", "whatsapp", "both"}, in.Channel) {
		return httpjson.Bad("Choose email, WhatsApp or both.")
	}
	msg, err := s.Send(r.Context(), u, id, in.Channel, false)
	if err != nil {
		return err
	}
	return httpjson.OK(w, map[string]string{"message": msg})
}

// RunAuto sends sheets for vendors set to automatic whose time is now, on days a new PO was issued since the last sheet.
func (s *Service) RunAuto(ctx context.Context) error {
	now := clock.Now().Format("15:04")
	rows, err := s.DB.Query(ctx, `SELECT id, sheet_channel FROM vendor WHERE active AND sheet_mode='auto' AND sheet_time=$1`, now)
	if err != nil {
		return err
	}
	type due struct {
		id int64
		ch string
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (due, error) {
		var d due
		return d, row.Scan(&d.id, &d.ch)
	})
	if err != nil {
		return err
	}
	for _, d := range list {
		sh, _, err := s.Build(ctx, d.id)
		if err != nil || sh.NewPOs == 0 {
			continue
		}
		if _, err := s.Send(ctx, nil, d.id, d.ch, true); err != nil {
			log.Printf("automatic order sheet for vendor %d: %v", d.id, err)
		}
	}
	return nil
}

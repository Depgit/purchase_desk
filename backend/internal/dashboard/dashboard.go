// Package dashboard gives each person the numbers for their dashboard: tiles, section lists and the
// "needs attention" alerts, all inside their property scope. Which tiles and sections show is chosen on screen
// (four standard views plus one the person arranges), and saved per user.
package dashboard

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"purchasedesk/internal/access"
	"purchasedesk/internal/approvals"
	"purchasedesk/internal/budgets"
	"purchasedesk/internal/clock"
	"purchasedesk/internal/documents"
	"purchasedesk/internal/flowsettings"
	"purchasedesk/internal/httpjson"
	"purchasedesk/internal/invoices"
	"purchasedesk/internal/money"
	"purchasedesk/internal/warehouse"
)

type Service struct {
	DB       *pgxpool.Pool
	Guard    *access.Guard
	Invoices *invoices.Service
}

func (s *Service) Routes(r chi.Router) {
	r.Get("/dashboard", httpjson.Wrap(s.get))
}

type Tile struct {
	Value   string `json:"value"`
	Sub     string `json:"sub"`
	Alert   bool   `json:"alert"`
	Section string `json:"section"`
}

type Alert struct {
	Urgent bool   `json:"urgent"`
	Text   string `json:"text"`
	Link   string `json:"link"`
}

type DocRow struct {
	ID       int64   `json:"id"`
	Number   string  `json:"number"`
	Property string  `json:"property"`
	Details  string  `json:"details"`
	Amount   float64 `json:"amount"`
	Status   string  `json:"status"`
	Flag     string  `json:"flag"`
	Days     int     `json:"days"`
}

type Out struct {
	DefaultView string          `json:"default_view"`
	Tiles       map[string]Tile `json:"tiles"`
	Sections    map[string]any  `json:"sections"`
	Alerts      []Alert         `json:"alerts"`
}

func defaultView(u *access.User) string {
	if u.IsAdmin {
		return "mgmt"
	}
	for _, g := range u.Grants {
		switch g.Step {
		case "staff", "l1", "store":
			return "ops"
		case "cc", "l2", "purchase":
			return "cc"
		case "ap", "finance":
			return "fin"
		case "capp":
			return "mgmt"
		}
	}
	return "my"
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return itoa(n) + " " + many
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	b := []byte{}
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}

func toRow(r approvals.Row) DocRow {
	flag := ""
	switch {
	case r.OverMonth > 0:
		flag = "Over month by " + money.INR(r.OverMonth)
	case r.OverWeek > 0:
		flag = "Over week by " + money.INR(r.OverWeek)
	case len(r.BillFlags) > 0:
		flag = r.BillFlags[0]
	}
	details := ""
	if r.Kind == "bill" {
		details = r.BillType
	} else if len(r.Lines) > 0 {
		details = r.Lines[0].ItemName
		if len(r.Lines) > 1 {
			details += " +" + itoa(len(r.Lines)-1) + " more"
		}
	}
	num := r.Number
	if r.PONumber != "" {
		num = r.PONumber
	}
	return DocRow{ID: r.ID, Number: num, Property: r.Property, Details: details, Amount: r.Amount, Status: r.StatusLabel, Flag: flag, Days: int(time.Since(r.CreatedAt).Hours() / 24)}
}

func top(rows []DocRow, n int) []DocRow {
	if len(rows) > n {
		return rows[:n]
	}
	return rows
}

func (s *Service) get(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	u := access.From(r)
	all, ids, err := s.Guard.Visible(ctx, u)
	if err != nil {
		return err
	}
	st, err := flowsettings.Load(ctx, s.DB)
	if err != nil {
		return err
	}
	docs, err := documents.LoadMany(ctx, s.DB, `WHERE (d.status IN ('PENDING','RETURNED','APPROVED','PO_ISSUED','WH_SENT') OR d.created_at > now() - interval '24 hours')
		AND ($1 OR d.property_id = ANY($2) OR d.requester_id=$3 OR $3 IN (d.corp_l1_user_id, d.corp_l2_user_id, d.corp_backup_user_id)) ORDER BY d.created_at DESC`, all, ids, u.ID)
	if err != nil {
		return err
	}
	rows, err := approvals.Enrich(ctx, s.DB, st, docs)
	if err != nil {
		return err
	}
	out := Out{DefaultView: defaultView(u), Tiles: map[string]Tile{}, Sections: map[string]any{}, Alerts: []Alert{}}
	var mine, l2, cc, over, fresh, dlv, billsW, corp, unsched []DocRow
	returned := 0
	sum := func(rs []DocRow) float64 {
		t := 0.0
		for _, x := range rs {
			t += x.Amount
		}
		return t
	}
	waitingLong := 0
	for _, row := range rows {
		d := row.Doc
		dr := toRow(row)
		owner := d.Owner()
		if d.Status == documents.Pending {
			if owner == "l2" {
				l2 = append(l2, dr)
			}
			if owner == "cc" {
				cc = append(cc, dr)
				if row.Flagged {
					over = append(over, dr)
				}
			}
			if d.Kind == "bill" {
				billsW = append(billsW, dr)
			}
		}
		if d.IsCorp() && d.Status != documents.Closed {
			corp = append(corp, dr)
		}
		if time.Since(d.CreatedAt) < 24*time.Hour && d.Kind == "purchase" {
			fresh = append(fresh, dr)
		}
		if (d.Status == documents.POIssued || d.Status == documents.WHSent) && d.Kind == "purchase" {
			dlv = append(dlv, dr)
			if d.IsTransfer && d.Status == documents.POIssued && d.Delivery == nil && d.PODate != nil && time.Since(*d.PODate) > 24*time.Hour {
				unsched = append(unsched, dr)
			}
		}
		right := map[string]string{documents.Pending: access.Approve, documents.Approved: access.IssuePO, documents.POIssued: access.Receive, documents.WHSent: access.Receive}[d.Status]
		act := owner != "" && documents.CanAct(u, d, owner, right) != nil
		if d.Status == documents.Approved && d.IsBill() {
			act = false
		}
		if act {
			mine = append(mine, dr)
			if d.Status == documents.Pending && time.Since(d.CreatedAt) > 24*time.Hour {
				waitingLong++
			}
			if d.Status == documents.Returned {
				returned++
			}
		}
	}
	sort.SliceStable(over, func(i, j int) bool { return over[i].Amount > over[j].Amount })
	sort.SliceStable(dlv, func(i, j int) bool { return dlv[i].Days > dlv[j].Days })
	invs, err := s.Invoices.List(ctx, u, []string{"RECEIVED", "MATCHED", "PAY_APPROVED", "PUSHED"}, 0)
	if err != nil {
		return err
	}
	type payRow struct {
		ID         int64   `json:"invoice_id"`
		DocumentID int64   `json:"document_id"`
		Invoice    string  `json:"invoice"`
		Vendor     string  `json:"vendor"`
		MSME       bool    `json:"msme"`
		Property   string  `json:"property"`
		Amount     float64 `json:"amount"`
		PayBy      string  `json:"pay_by"`
		Status     string  `json:"status"`
		Held       string  `json:"held"`
	}
	var pay, msme, held []payRow
	payTotal, msmeTotal := 0.0, 0.0
	soon := clock.YMD(clock.Today().AddDate(0, 0, 7))
	for _, x := range invs {
		pr := payRow{ID: x.ID, DocumentID: x.DocumentID, Invoice: x.Number, Vendor: x.Vendor, MSME: x.VendorMSME, Property: x.Property, Amount: x.Amount, PayBy: x.PayBy, Status: x.Status, Held: strings.Join(x.Held, "; ")}
		if x.Status == documents.InvReceived {
			if u.Can("ap", access.Match, x.PropertyID, x.ClusterID, nil) != nil {
				mine = append(mine, DocRow{ID: x.DocumentID, Number: x.Number, Property: x.Property, Details: "Invoice to match · " + x.Vendor, Amount: x.Amount, Status: "To match"})
			}
			continue
		}
		pay = append(pay, pr)
		payTotal += x.Amount
		if u.Can("finance", access.Pay, x.PropertyID, x.ClusterID, nil) != nil && x.Status != documents.InvPushed {
			mine = append(mine, DocRow{ID: x.DocumentID, Number: x.Number, Property: x.Property, Details: "Payment · " + x.Vendor, Amount: x.Amount, Status: x.Status})
		}
		if x.VendorMSME && x.PayBy != "" && x.PayBy <= soon {
			msme = append(msme, pr)
			msmeTotal += x.Amount
		}
		if len(x.Held) > 0 {
			held = append(held, pr)
		}
	}
	sort.SliceStable(pay, func(i, j int) bool { return pay[i].PayBy < pay[j].PayBy })
	// Budget across the user's live properties, month to date.
	brows, err := s.DB.Query(ctx, `SELECT p.id, p.name, k.name FROM property p JOIN cluster k ON k.id=p.cluster_id WHERE p.active AND p.kind='live' AND ($1 OR p.id = ANY($2)) ORDER BY p.name`, all, ids)
	if err != nil {
		return err
	}
	type bp struct {
		id         int64
		name, clNm string
	}
	props, err := pgx.CollectRows(brows, func(row pgx.CollectableRow) (bp, error) {
		var x bp
		return x, row.Scan(&x.id, &x.name, &x.clNm)
	})
	if err != nil {
		return err
	}
	type catRow struct {
		Category     string  `json:"category"`
		BudgetToDate float64 `json:"budget_to_date"`
		Spent        float64 `json:"spent"`
		MonthBudget  float64 `json:"month_budget"`
		LastMonth    float64 `json:"last_month_same_days"`
		Pace         string  `json:"pace"`
		Tone         string  `json:"tone"`
	}
	type paceRow struct {
		PropertyID   int64   `json:"property_id"`
		Property     string  `json:"property"`
		Cluster      string  `json:"cluster"`
		BudgetToDate float64 `json:"budget_to_date"`
		Spent        float64 `json:"spent"`
		Pace         string  `json:"pace"`
		Ratio        float64 `json:"ratio"`
	}
	cats := map[string]*catRow{}
	catOrder := []string{}
	var paceRows []paceRow
	var totB, totS float64
	scrows, err := s.DB.Query(ctx, `SELECT sc.id, c.name FROM sub_category sc JOIN category c ON c.id=sc.category_id ORDER BY c.name`)
	if err != nil {
		return err
	}
	type scCat struct {
		id  int64
		cat string
	}
	scs, err := pgx.CollectRows(scrows, func(row pgx.CollectableRow) (scCat, error) {
		var x scCat
		return x, row.Scan(&x.id, &x.cat)
	})
	if err != nil {
		return err
	}
	today := clock.Today()
	lastStart := clock.MonthStart(today).AddDate(0, -1, 0)
	for _, p := range props {
		m, err := budgets.LoadMonth(ctx, s.DB, p.id, today)
		if err != nil {
			return err
		}
		var pb, ps float64
		for _, sc := range scs {
			b, sp := m.BudgetToDate(sc.id), m.SpentMonth(sc.id)
			pb += b
			ps += sp
			c := cats[sc.cat]
			if c == nil {
				c = &catRow{Category: sc.cat}
				cats[sc.cat] = c
				catOrder = append(catOrder, sc.cat)
			}
			c.BudgetToDate += b
			c.Spent += sp
			c.MonthBudget += m.Budget[sc.id]
		}
		totB += pb
		totS += ps
		label, _, ratio := budgets.Pace(ps, pb)
		if ratio > 0.05 {
			paceRows = append(paceRows, paceRow{p.id, p.name, p.clNm, money.Round2(pb), money.Round2(ps), label, ratio})
		}
	}
	// Last month, the same number of days, for movement.
	lrows, err := s.DB.Query(ctx, `SELECT c.name, coalesce(sum(b.amount),0) FROM budget_spend b JOIN sub_category sc ON sc.id=b.sub_category_id JOIN category c ON c.id=sc.category_id
		JOIN property p ON p.id=b.property_id WHERE p.kind='live' AND ($1 OR p.id = ANY($2)) AND b.spend_date >= $3 AND b.spend_date < $4 GROUP BY c.name`,
		all, ids, lastStart, lastStart.AddDate(0, 0, today.Day()))
	if err == nil {
		for lrows.Next() {
			var c string
			var v float64
			if lrows.Scan(&c, &v) == nil && cats[c] != nil {
				cats[c].LastMonth = money.Round2(v)
			}
		}
		lrows.Close()
	}
	catList := []catRow{}
	for _, k := range catOrder {
		c := cats[k]
		c.Pace, c.Tone, _ = budgets.Pace(c.Spent, c.BudgetToDate)
		c.BudgetToDate, c.Spent, c.MonthBudget = money.Round2(c.BudgetToDate), money.Round2(c.Spent), money.Round2(c.MonthBudget)
		catList = append(catList, *c)
	}
	sort.SliceStable(paceRows, func(i, j int) bool { return paceRows[i].Ratio > paceRows[j].Ratio })
	// Warehouse.
	_, ledger, err := warehouse.Ledger(ctx, s.DB)
	if err != nil {
		return err
	}
	low := []*warehouse.Stock{}
	for _, x := range ledger {
		if x.Closing < x.MinLevel {
			low = append(low, x)
		}
	}
	// Rates of mapped vendors expiring within 15 days, or expired.
	type rateRow struct {
		Vendor string  `json:"vendor"`
		SKU    string  `json:"sku"`
		Item   string  `json:"item"`
		Rate   float64 `json:"rate"`
		To     string  `json:"valid_to"`
		Days   int     `json:"days_left"`
	}
	rrows, err := s.DB.Query(ctx, `SELECT v.legal_name, k.code, k.item_name, r.rate, r.valid_to::text, r.valid_to - current_date FROM rate_card r JOIN vendor v ON v.id=r.vendor_id JOIN sku k ON k.id=r.sku_id
		WHERE NOT r.superseded AND r.valid_to < current_date + 15 AND NOT EXISTS (SELECT 1 FROM rate_card n WHERE n.vendor_id=r.vendor_id AND n.sku_id=r.sku_id AND NOT n.superseded AND n.valid_from > r.valid_from)
		AND EXISTS (SELECT 1 FROM vendor_mapping m WHERE m.active AND m.vendor_id=r.vendor_id AND ((m.level='sku' AND m.sku_id=r.sku_id) OR (m.level='sc' AND m.sub_category_id=k.sub_category_id)))
		ORDER BY r.valid_to LIMIT 50`)
	if err != nil {
		return err
	}
	expiring, err := pgx.CollectRows(rrows, func(row pgx.CollectableRow) (rateRow, error) {
		var x rateRow
		return x, row.Scan(&x.Vendor, &x.SKU, &x.Item, &x.Rate, &x.To, &x.Days)
	})
	if err != nil {
		return err
	}
	var bankPending int
	_ = s.DB.QueryRow(ctx, `SELECT count(*) FROM vendor WHERE active AND NOT bank_verified AND vendor_type<>'warehouse'`).Scan(&bankPending)
	// Updates on the user's own requests, and decisions by others.
	type update struct {
		DocumentID int64     `json:"document_id"`
		Number     string    `json:"number"`
		Action     string    `json:"action"`
		User       string    `json:"user"`
		Note       string    `json:"note"`
		At         time.Time `json:"at"`
	}
	urows, err := s.DB.Query(ctx, `SELECT d.id, coalesce(d.po_number,d.number), l.action, l.user_name||' ('||l.role_name||')', l.note, l.at FROM document_log l JOIN document d ON d.id=l.document_id
		WHERE l.at > now() - interval '7 days' AND coalesce(l.user_id,0)<>$1 AND (d.requester_id=$1 OR ($2 AND (l.action ILIKE 'Approved%' OR l.action ILIKE 'Rejected%' OR l.action ILIKE 'Returned%' OR l.action ILIKE 'Quantity changed%' OR l.action ILIKE 'Sent to%')))
		ORDER BY l.at DESC LIMIT 15`, u.ID, u.HasRight(access.Approve) && !u.IsAdmin)
	if err != nil {
		return err
	}
	updates, err := pgx.CollectRows(urows, func(row pgx.CollectableRow) (update, error) {
		var x update
		return x, row.Scan(&x.DocumentID, &x.Number, &x.Action, &x.User, &x.Note, &x.At)
	})
	if err != nil {
		return err
	}
	pct := 0
	if totB > 0 {
		pct = int(totS / totB * 100)
	}
	t := out.Tiles
	t["mine"] = Tile{itoa(len(mine)), "Waiting for you", false, "mine"}
	t["bud"] = Tile{itoa(pct) + "%", money.INR(totS) + " of " + money.INR(totB) + " to date", pct > 105, "bud"}
	t["over"] = Tile{itoa(len(over)), money.INR(sum(over)) + " with the Cost Controller", len(over) > 0, "cc"}
	t["new"] = Tile{itoa(len(fresh)), money.INR(sum(fresh)) + " in the last 24 hours", false, "new"}
	t["l2"] = Tile{itoa(len(l2)), money.INR(sum(l2)), false, "l2"}
	t["cc"] = Tile{itoa(len(cc)), money.INR(sum(cc)), false, "cc"}
	t["dlv"] = Tile{itoa(len(dlv)), plural(len(unsched), "warehouse order not scheduled", "warehouse orders not scheduled"), len(unsched) > 0, "dlv"}
	t["pay"] = Tile{money.INR(payTotal), plural(len(pay), "invoice", "invoices"), false, "pay"}
	t["msme"] = Tile{itoa(len(msme)), money.INR(msmeTotal), len(msme) > 0, "pay"}
	t["zoho"] = Tile{itoa(len(held)), "held back from Zoho", len(held) > 0, "zoho"}
	t["bills"] = Tile{itoa(len(billsW)), money.INR(sum(billsW)) + " with approvers", false, "bills"}
	t["corp"] = Tile{itoa(len(corp)), money.INR(sum(corp)), false, "corp"}
	t["wh"] = Tile{itoa(len(low)), "below the reorder level", len(low) > 0, "wh"}
	t["rates"] = Tile{itoa(len(expiring)), "mapped rates, next 15 days", len(expiring) > 0, "rates"}
	sec := out.Sections
	sec["mine"] = top(mine, 15)
	sec["bud"] = catList
	sec["top"] = top2(paceRows, 12)
	sec["new"] = top(fresh, 12)
	sec["l2"] = top(l2, 12)
	sec["cc"] = top(append(over, nonFlagged(cc, over)...), 12)
	sec["dlv"] = top(dlv, 12)
	sec["bills"] = top(billsW, 12)
	sec["pay"] = top3(pay, 12)
	sec["zoho"] = top3(held, 12)
	sec["corp"] = top(corp, 12)
	sec["wh"] = low
	sec["rates"] = expiring
	sec["upd"] = updates
	sec["age"] = top(longest(mine), 12)
	// Needs attention, filtered by what the person does; urgent first.
	al := []Alert{}
	add := func(on, urgent bool, text, link string) {
		if on {
			al = append(al, Alert{urgent, text, link})
		}
	}
	A := u.IsAdmin
	add(waitingLong > 0 && (A || u.HasStep("cc") || u.HasStep("l2") || u.HasStep("preop") || u.HasRight(access.Approve)), true, plural(waitingLong, "approval has", "approvals have")+" waited more than a day", "/approvals")
	add(len(over) > 0 && (A || u.HasStep("cc")), false, plural(len(over), "request with the Cost Controller is", "requests with the Cost Controller are")+" over budget, worth "+money.INR(sum(over)), "/approvals")
	add(len(msme) > 0 && (A || u.HasStep("ap") || u.HasStep("finance")), true, plural(len(msme), "MSME invoice", "MSME invoices")+" must be paid within 7 days ("+money.INR(msmeTotal)+")", "/payments")
	add(len(held) > 0 && (A || u.HasStep("finance")), false, plural(len(held), "approved invoice is", "approved invoices are")+" held back from Zoho by a missing mapping", "/settings/zoho")
	add(bankPending > 0 && (A || u.HasStep("finance")), false, plural(bankPending, "vendor is", "vendors are")+" waiting for bank verification", "/vendors")
	add(len(low) > 0 && (A || u.HasStep("wh") || u.HasStep("purchase")), true, plural(len(low), "warehouse item is", "warehouse items are")+" below the reorder level", "/warehouse")
	add(len(unsched) > 0 && (A || u.HasStep("wh")), false, plural(len(unsched), "warehouse order", "warehouse orders")+" older than a day "+map[bool]string{true: "has", false: "have"}[len(unsched) == 1]+" no delivery scheduled", "/warehouse")
	add(len(expiring) > 0 && (A || u.HasStep("purchase")), false, plural(len(expiring), "mapped vendor rate expires", "mapped vendor rates expire")+" within 15 days or "+map[bool]string{true: "has", false: "have"}[len(expiring) == 1]+" expired", "/vendor-mapping")
	add(returned > 0, false, plural(returned, "request was", "requests were")+" returned to you for changes", "/requests")
	corpMine := 0
	for _, row := range rows {
		if row.Doc.IsCorp() && row.Doc.Status == documents.Pending && documents.CanAct(u, row.Doc, row.Doc.Owner(), access.Approve) != nil {
			corpMine++
		}
	}
	add(corpMine > 0, false, plural(corpMine, "corporate request is", "corporate requests are")+" waiting for you", "/corporate")
	sort.SliceStable(al, func(i, j int) bool { return al[i].Urgent && !al[j].Urgent })
	out.Alerts = al
	return httpjson.OK(w, out)
}

func nonFlagged(all, flagged []DocRow) []DocRow {
	seen := map[int64]bool{}
	for _, f := range flagged {
		seen[f.ID] = true
	}
	out := []DocRow{}
	for _, x := range all {
		if !seen[x.ID] {
			out = append(out, x)
		}
	}
	return out
}

func longest(rows []DocRow) []DocRow {
	out := append([]DocRow{}, rows...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Days > out[j].Days })
	return out
}

func top2[T any](rows []T, n int) []T {
	if len(rows) > n {
		return rows[:n]
	}
	if rows == nil {
		return []T{}
	}
	return rows
}

func top3[T any](rows []T, n int) []T { return top2(rows, n) }

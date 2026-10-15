// Package budgets keeps one budget per property, sub-category and month, splits it into weeks and days,
// tracks approved spend against it and raises the over-budget flags.
// F&B day budgets up to today follow the uploaded sales: Plan sale × Plan % + IRD sale × IRD % + Banquet sale × Banquet %,
// shared between F&B sub-categories in the ratio of their month budgets.
package budgets

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"purchasedesk/internal/access"
	"purchasedesk/internal/auditlog"
	"purchasedesk/internal/clock"
	"purchasedesk/internal/csvfile"
	"purchasedesk/internal/database"
	"purchasedesk/internal/httpjson"
	"purchasedesk/internal/money"
)

type Service struct {
	DB    *pgxpool.Pool
	Guard *access.Guard
}

func (s *Service) Routes(r chi.Router) {
	r.Get("/budgets/overview", httpjson.Wrap(s.overview))
	r.Get("/budgets/property", httpjson.Wrap(s.propertyDetail))
	r.Put("/budgets", httpjson.Wrap(s.setBudget))
	r.Get("/budgets/export", httpjson.Wrap(s.export))
	r.Get("/fb-sales", httpjson.Wrap(s.fbOverview))
	r.Get("/fb-sales/export", httpjson.Wrap(s.salesExport))
}

// PendingFinance names the property or sub-category of a budget that Finance has not approved yet, or "".
func PendingFinance(ctx context.Context, q database.Q, propertyID, subCategoryID int64) string {
	var name string
	_ = q.QueryRow(ctx, `SELECT coalesce((SELECT name FROM property WHERE id=$1 AND fin_status<>'approved'), (SELECT name FROM sub_category WHERE id=$2 AND fin_status<>'approved'), '')`,
		propertyID, subCategoryID).Scan(&name)
	return name
}

// Month holds one property's budget picture for a month, per sub-category.
type Month struct {
	PropertyID int64
	Start      time.Time
	Days       int
	Today      int // day of month counted as "today" (1-based); 0 for a future month, Days for a past month
	Budget     map[int64]float64
	IsFB       map[int64]bool
	Spend      map[int64][]float64 // per day index
	Allowed    []float64           // F&B allowed cost per day from sales
	FBTotal    float64             // sum of F&B month budgets
}

// Weeks are 7-day blocks counted from the 1st.
func (m *Month) Weeks() [][2]int {
	out := [][2]int{}
	for a := 1; a <= m.Days; a += 7 {
		b := a + 6
		if b > m.Days {
			b = m.Days
		}
		out = append(out, [2]int{a, b})
	}
	return out
}

// DayBudget for day index k (0-based).
func (m *Month) DayBudget(sc int64, k int) float64 {
	if m.IsFB[sc] && k < m.Today && m.FBTotal > 0 {
		return m.Allowed[k] * m.Budget[sc] / m.FBTotal
	}
	return m.Budget[sc] / float64(m.Days)
}

func (m *Month) Sum(sc int64, from, to int, f func(int64, int) float64) float64 {
	t := 0.0
	for k := from; k < to && k < m.Days; k++ {
		t += f(sc, k)
	}
	return t
}

func (m *Month) spentDay(sc int64, k int) float64 {
	if s := m.Spend[sc]; s != nil {
		return s[k]
	}
	return 0
}

func (m *Month) BudgetToDate(sc int64) float64 { return m.Sum(sc, 0, m.Today, m.DayBudget) }
func (m *Month) SpentMonth(sc int64) float64   { return m.Sum(sc, 0, m.Days, m.spentDay) }
func (m *Month) CurrentWeek() int {
	if m.Today <= 0 {
		return 0
	}
	return (m.Today - 1) / 7
}
func (m *Month) WeekBudget(sc int64, w int) float64 {
	wk := m.Weeks()[w]
	return m.Sum(sc, wk[0]-1, wk[1], m.DayBudget)
}
func (m *Month) WeekSpent(sc int64, w int) float64 {
	wk := m.Weeks()[w]
	return m.Sum(sc, wk[0]-1, wk[1], m.spentDay)
}

// LoadMonth reads budgets, spend and F&B sales for a property and month.
func LoadMonth(ctx context.Context, q database.Q, propertyID int64, month time.Time) (*Month, error) {
	start := clock.MonthStart(month)
	m := &Month{PropertyID: propertyID, Start: start, Days: clock.DaysInMonth(start), Budget: map[int64]float64{}, IsFB: map[int64]bool{}, Spend: map[int64][]float64{}}
	t := clock.Today()
	switch {
	case start.After(t):
		m.Today = 0
	case clock.MonthStart(t).Equal(start):
		m.Today = t.Day()
	default:
		m.Today = m.Days
	}
	rows, err := q.Query(ctx, `SELECT sc.id, c.is_fb, coalesce(b.amount,0) FROM sub_category sc JOIN category c ON c.id=sc.category_id
		LEFT JOIN budget b ON b.sub_category_id=sc.id AND b.property_id=$1 AND b.month=$2`, propertyID, start)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		var fb bool
		var a float64
		if err := rows.Scan(&id, &fb, &a); err != nil {
			rows.Close()
			return nil, err
		}
		m.Budget[id], m.IsFB[id] = a, fb
		if fb {
			m.FBTotal += a
		}
	}
	rows.Close()
	end := start.AddDate(0, 1, 0)
	rows, err = q.Query(ctx, `SELECT sub_category_id, extract(day FROM spend_date)::int, sum(amount) FROM budget_spend
		WHERE property_id=$1 AND spend_date >= $2 AND spend_date < $3 GROUP BY 1,2`, propertyID, start, end)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var sc int64
		var d int
		var a float64
		if err := rows.Scan(&sc, &d, &a); err != nil {
			rows.Close()
			return nil, err
		}
		if m.Spend[sc] == nil {
			m.Spend[sc] = make([]float64, m.Days)
		}
		m.Spend[sc][d-1] += a
	}
	rows.Close()
	m.Allowed = make([]float64, m.Days)
	var pp, pi, pb float64
	if err := q.QueryRow(ctx, `SELECT plan_pct, ird_pct, banquet_pct FROM property WHERE id=$1`, propertyID).Scan(&pp, &pi, &pb); err != nil {
		return nil, err
	}
	rows, err = q.Query(ctx, `SELECT extract(day FROM sale_date)::int, plan_sale, ird_sale, banquet_sale FROM fb_sale WHERE property_id=$1 AND sale_date >= $2 AND sale_date < $3`, propertyID, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var d int
		var a, b, c float64
		if err := rows.Scan(&d, &a, &b, &c); err != nil {
			return nil, err
		}
		m.Allowed[d-1] = (a*pp + b*pi + c*pb) / 100
	}
	return m, rows.Err()
}

// Pace compares spend to date with budget to date: more than 5% above is over pace, more than 5% below is under.
func Pace(spent, budgetToDate float64) (label, tone string, ratio float64) {
	if budgetToDate <= 0 {
		if spent > 0 {
			return "No budget", "bad", 1
		}
		return "No budget", "off", 0
	}
	v := (spent - budgetToDate) / budgetToDate
	switch {
	case v > 0.05:
		return fmt.Sprintf("%.0f%% over pace", v*100), "bad", v
	case v < -0.05:
		return fmt.Sprintf("%.0f%% under pace", -v*100), "ok", v
	}
	return "On pace", "ok", v
}

// Flag says how far a request takes a sub-category over its month and current-week budget.
type Flag struct {
	SubCategoryID int64   `json:"sub_category_id"`
	Amount        float64 `json:"amount"`
	MonthBudget   float64 `json:"month_budget"`
	MonthSpent    float64 `json:"month_spent"`
	OverMonth     float64 `json:"over_month"`
	WeekBudget    float64 `json:"week_budget"`
	WeekSpent     float64 `json:"week_spent"`
	OverWeek      float64 `json:"over_week"`
	DayBudget     float64 `json:"day_budget"`
	DaySpent      float64 `json:"day_spent"`
	Week          int     `json:"week"`
	IsFB          bool    `json:"is_fb"`
}

// Check computes flags for amounts per sub-category against this month. weekFlag follows the flow setting.
func Check(m *Month, totals map[int64]float64, weekFlag bool) (flags []Flag, overMonth, overWeek float64) {
	w := m.CurrentWeek()
	for sc, amt := range totals {
		f := Flag{SubCategoryID: sc, Amount: money.Round2(amt), MonthBudget: m.Budget[sc], MonthSpent: money.Round2(m.SpentMonth(sc)),
			WeekBudget: money.Round2(m.WeekBudget(sc, w)), WeekSpent: money.Round2(m.WeekSpent(sc, w)), Week: w + 1, IsFB: m.IsFB[sc]}
		if m.Today > 0 {
			f.DayBudget, f.DaySpent = money.Round2(m.DayBudget(sc, m.Today-1)), money.Round2(m.spentDay(sc, m.Today-1))
		}
		f.OverMonth = money.Round2(max(0, f.MonthSpent+amt-f.MonthBudget))
		if weekFlag {
			f.OverWeek = money.Round2(max(0, f.WeekSpent+amt-f.WeekBudget))
		}
		overMonth, overWeek = max(overMonth, f.OverMonth), max(overWeek, f.OverWeek)
		flags = append(flags, f)
	}
	return flags, overMonth, overWeek
}

// Cache loads each property's month once per request.
type Cache struct {
	q database.Q
	m map[int64]*Month
}

func NewCache(q database.Q) *Cache { return &Cache{q: q, m: map[int64]*Month{}} }

func (c *Cache) Get(ctx context.Context, propertyID int64) (*Month, error) {
	if m, ok := c.m[propertyID]; ok {
		return m, nil
	}
	m, err := LoadMonth(ctx, c.q, propertyID, clock.Today())
	if err != nil {
		return nil, err
	}
	c.m[propertyID] = m
	return m, nil
}

// Commit counts approved spend on today's date. Pass negative totals to release spend.
func Commit(ctx context.Context, q database.Q, propertyID, docID int64, totals map[int64]float64) error {
	for sc, a := range totals {
		if a == 0 {
			continue
		}
		if _, err := q.Exec(ctx, `INSERT INTO budget_spend(property_id,sub_category_id,spend_date,amount,document_id) VALUES ($1,$2,$3,$4,$5)`,
			propertyID, sc, clock.Today(), money.Round2(a), docID); err != nil {
			return err
		}
	}
	return nil
}

// Set saves a month budget.
func Set(ctx context.Context, q database.Q, u *access.User, propertyID, subCatID int64, month time.Time, amount float64) (old float64, err error) {
	_ = q.QueryRow(ctx, `SELECT amount FROM budget WHERE property_id=$1 AND sub_category_id=$2 AND month=$3`, propertyID, subCatID, clock.MonthStart(month)).Scan(&old)
	_, err = q.Exec(ctx, `INSERT INTO budget(property_id,sub_category_id,month,amount,created_by) VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (property_id,sub_category_id,month) DO UPDATE SET amount=EXCLUDED.amount`, propertyID, subCatID, clock.MonthStart(month), money.Round2(amount), u.ID)
	return old, err
}

// SetSales saves one day of F&B sales for a property, replacing that day if it was uploaded before.
func SetSales(ctx context.Context, q database.Q, u *access.User, propertyID int64, day time.Time, plan, ird, banquet float64) error {
	_, err := q.Exec(ctx, `INSERT INTO fb_sale(property_id,sale_date,plan_sale,ird_sale,banquet_sale,created_by) VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (property_id,sale_date) DO UPDATE SET plan_sale=EXCLUDED.plan_sale, ird_sale=EXCLUDED.ird_sale, banquet_sale=EXCLUDED.banquet_sale`,
		propertyID, day, plan, ird, banquet, u.ID)
	return err
}

func canEdit(u *access.User) bool { return u.IsAdmin || u.HasStep("cc") }

func monthParam(r *http.Request) time.Time {
	if m, ok := clock.ParseMonth(r.URL.Query().Get("month")); ok {
		return m
	}
	return clock.MonthStart(clock.Today())
}

type subCat struct {
	ID         int64
	Name       string
	CategoryID int64
	Category   string
	IsFB       bool
}

func loadSubCats(ctx context.Context, q database.Q) ([]subCat, error) {
	rows, err := q.Query(ctx, `SELECT sc.id, sc.name, c.id, c.name, c.is_fb FROM sub_category sc JOIN category c ON c.id=sc.category_id WHERE sc.active ORDER BY c.name, sc.name`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (subCat, error) {
		var x subCat
		return x, row.Scan(&x.ID, &x.Name, &x.CategoryID, &x.Category, &x.IsFB)
	})
}

type propRow struct {
	ID, ClusterID int64
	Code, Name    string
	Cluster       string
}

func (s *Service) liveProperties(ctx context.Context, u *access.User, clusters []int64) ([]propRow, error) {
	all, ids, err := s.Guard.Visible(ctx, u)
	if err != nil {
		return nil, err
	}
	rows, err := s.DB.Query(ctx, `SELECT p.id, p.cluster_id, p.code, p.name, k.name FROM property p JOIN cluster k ON k.id=p.cluster_id
		WHERE p.active AND p.kind='live' AND (cardinality($1::bigint[])=0 OR p.cluster_id = ANY($1)) AND ($2 OR p.id = ANY($3)) ORDER BY p.name`, clusters, all, ids)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (propRow, error) {
		var x propRow
		return x, row.Scan(&x.ID, &x.ClusterID, &x.Code, &x.Name, &x.Cluster)
	})
}

type catPace struct {
	CategoryID   int64   `json:"category_id"`
	Category     string  `json:"category"`
	Budget       float64 `json:"month_budget"`
	BudgetToDate float64 `json:"budget_to_date"`
	Spent        float64 `json:"spent"`
	Pace         string  `json:"pace"`
	Tone         string  `json:"tone"`
	Ratio        float64 `json:"ratio"`
}

type overviewRow struct {
	PropertyID   int64     `json:"property_id"`
	Code         string    `json:"code"`
	Property     string    `json:"property"`
	Cluster      string    `json:"cluster"`
	Budget       float64   `json:"month_budget"`
	BudgetToDate float64   `json:"budget_to_date"`
	Spent        float64   `json:"spent"`
	Left         float64   `json:"left"`
	Pace         string    `json:"pace"`
	Tone         string    `json:"tone"`
	Ratio        float64   `json:"ratio"`
	Categories   []catPace `json:"categories"`
}

// Overview gives every property's month: budget, budget to date, approved spend and pace, by category.
func Overview(ctx context.Context, q database.Q, props []propRow, scs []subCat, month time.Time) ([]overviewRow, error) {
	out := []overviewRow{}
	for _, p := range props {
		m, err := LoadMonth(ctx, q, p.ID, month)
		if err != nil {
			return nil, err
		}
		row := overviewRow{PropertyID: p.ID, Code: p.Code, Property: p.Name, Cluster: p.Cluster, Categories: []catPace{}}
		cats := map[int64]*catPace{}
		order := []int64{}
		for _, sc := range scs {
			b, bt, sp := m.Budget[sc.ID], m.BudgetToDate(sc.ID), m.SpentMonth(sc.ID)
			row.Budget += b
			row.BudgetToDate += bt
			row.Spent += sp
			c := cats[sc.CategoryID]
			if c == nil {
				c = &catPace{CategoryID: sc.CategoryID, Category: sc.Category}
				cats[sc.CategoryID] = c
				order = append(order, sc.CategoryID)
			}
			c.Budget += b
			c.BudgetToDate += bt
			c.Spent += sp
		}
		for _, id := range order {
			c := cats[id]
			c.Pace, c.Tone, c.Ratio = Pace(c.Spent, c.BudgetToDate)
			c.Budget, c.BudgetToDate, c.Spent = money.Round2(c.Budget), money.Round2(c.BudgetToDate), money.Round2(c.Spent)
			row.Categories = append(row.Categories, *c)
		}
		row.Pace, row.Tone, row.Ratio = Pace(row.Spent, row.BudgetToDate)
		row.Budget, row.BudgetToDate, row.Spent = money.Round2(row.Budget), money.Round2(row.BudgetToDate), money.Round2(row.Spent)
		row.Left = money.Round2(row.Budget - row.Spent)
		out = append(out, row)
	}
	return out, nil
}

func (s *Service) overview(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	props, err := s.liveProperties(r.Context(), u, httpjson.QInts(r, "cluster_id"))
	if err != nil {
		return err
	}
	scs, err := loadSubCats(r.Context(), s.DB)
	if err != nil {
		return err
	}
	out, err := Overview(r.Context(), s.DB, props, scs, monthParam(r))
	if err != nil {
		return err
	}
	return httpjson.OK(w, out)
}

type scDetail struct {
	SubCategoryID int64     `json:"sub_category_id"`
	SubCategory   string    `json:"sub_category"`
	CategoryID    int64     `json:"category_id"`
	Category      string    `json:"category"`
	IsFB          bool      `json:"is_fb"`
	Budget        float64   `json:"month_budget"`
	BudgetToDate  float64   `json:"budget_to_date"`
	Spent         float64   `json:"spent"`
	Pace          string    `json:"pace"`
	Tone          string    `json:"tone"`
	Weeks         []weekRow `json:"weeks"`
	DayBudget     []float64 `json:"day_budget"`
	DaySpent      []float64 `json:"day_spent"`
}

type weekRow struct {
	From   int     `json:"from"`
	To     int     `json:"to"`
	Budget float64 `json:"budget"`
	Spent  float64 `json:"spent"`
}

func (s *Service) propertyDetail(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	pid := int64(httpjson.QInt(r, "property_id", 0))
	if !s.Guard.SeesProperty(r.Context(), u, pid) {
		return httpjson.Forbidden("This property is outside your access.")
	}
	m, err := LoadMonth(r.Context(), s.DB, pid, monthParam(r))
	if err != nil {
		return err
	}
	scs, err := loadSubCats(r.Context(), s.DB)
	if err != nil {
		return err
	}
	out := []scDetail{}
	for _, sc := range scs {
		d := scDetail{SubCategoryID: sc.ID, SubCategory: sc.Name, CategoryID: sc.CategoryID, Category: sc.Category, IsFB: sc.IsFB,
			Budget: m.Budget[sc.ID], BudgetToDate: money.Round2(m.BudgetToDate(sc.ID)), Spent: money.Round2(m.SpentMonth(sc.ID))}
		d.Pace, d.Tone, _ = Pace(d.Spent, d.BudgetToDate)
		for i := range m.Weeks() {
			wk := m.Weeks()[i]
			d.Weeks = append(d.Weeks, weekRow{From: wk[0], To: wk[1], Budget: money.Round2(m.WeekBudget(sc.ID, i)), Spent: money.Round2(m.WeekSpent(sc.ID, i))})
		}
		for k := 0; k < m.Days; k++ {
			d.DayBudget = append(d.DayBudget, money.Round2(m.DayBudget(sc.ID, k)))
			d.DaySpent = append(d.DaySpent, money.Round2(m.spentDay(sc.ID, k)))
		}
		out = append(out, d)
	}
	return httpjson.OK(w, map[string]any{"month": clock.YMD(m.Start)[:7], "days": m.Days, "today": m.Today, "current_week": m.CurrentWeek() + 1,
		"allowed_fb": m.Allowed, "sub_categories": out, "can_edit": canEdit(u)})
}

func (s *Service) setBudget(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	if !canEdit(u) {
		return httpjson.Forbidden("Only the Cost Controller or the System Admin can change budgets.")
	}
	var in struct {
		PropertyID    int64   `json:"property_id"`
		SubCategoryID int64   `json:"sub_category_id"`
		Month         string  `json:"month"`
		Amount        float64 `json:"amount"`
	}
	if err := httpjson.Decode(r, &in); err != nil {
		return err
	}
	m, ok := clock.ParseMonth(in.Month)
	if !ok {
		return httpjson.Bad("Month must be YYYY-MM.")
	}
	if in.Amount < 0 {
		return httpjson.Bad("Budget must be 0 or more.")
	}
	if wait := PendingFinance(r.Context(), s.DB, in.PropertyID, in.SubCategoryID); wait != "" {
		return httpjson.Bad("%s waits for Finance to approve it; its budget can be set once approved.", wait)
	}
	err := pgx.BeginFunc(r.Context(), s.DB, func(tx pgx.Tx) error {
		old, err := Set(r.Context(), tx, u, in.PropertyID, in.SubCategoryID, m, in.Amount)
		if err != nil {
			return err
		}
		var label string
		_ = tx.QueryRow(r.Context(), `SELECT p.name||' · '||sc.name FROM property p, sub_category sc WHERE p.id=$1 AND sc.id=$2`, in.PropertyID, in.SubCategoryID).Scan(&label)
		return auditlog.Write(r.Context(), tx, u, "Budgets", "Budget changed", label, fmt.Sprintf("%s: %s to %s", in.Month, money.INR(old), money.INR(in.Amount)))
	})
	if err != nil {
		return err
	}
	return httpjson.OK(w, map[string]bool{"ok": true})
}

func (s *Service) export(w http.ResponseWriter, r *http.Request) error {
	month := monthParam(r)
	rows, err := s.DB.Query(r.Context(), `SELECT p.code, p.name, sc.name, coalesce(b.amount,0)::text FROM property p CROSS JOIN sub_category sc
		LEFT JOIN budget b ON b.property_id=p.id AND b.sub_category_id=sc.id AND b.month=$1
		WHERE p.active AND p.kind='live' AND sc.active ORDER BY p.code, sc.name`, month)
	if err != nil {
		return err
	}
	out := [][]string{}
	for rows.Next() {
		var a, b, c, d string
		if err := rows.Scan(&a, &b, &c, &d); err != nil {
			return err
		}
		out = append(out, []string{a, b, clock.YMD(month)[:7], c, d})
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="budgets-%s.csv"`, clock.YMD(month)[:7]))
	_, err = w.Write(csvfile.Write([]string{"property_code", "property_name", "month", "sub_category", "monthly_budget"}, out))
	return err
}

type fbRow struct {
	PropertyID int64   `json:"property_id"`
	Property   string  `json:"property"`
	Cluster    string  `json:"cluster"`
	Plan       float64 `json:"plan_sale"`
	IRD        float64 `json:"ird_sale"`
	Banquet    float64 `json:"banquet_sale"`
	Sale       float64 `json:"total_sale"`
	Allowed    float64 `json:"allowed_cost"`
	Ordered    float64 `json:"ordered"`
	CostPct    float64 `json:"cost_pct_of_sale"`
	Pace       string  `json:"pace"`
	Tone       string  `json:"tone"`
	Budget     float64 `json:"month_budget"`
	PlanPct    float64 `json:"plan_pct"`
	IRDPct     float64 `json:"ird_pct"`
	BqPct      float64 `json:"banquet_pct"`
}

func (s *Service) fbOverview(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	month := monthParam(r)
	props, err := s.liveProperties(r.Context(), u, httpjson.QInts(r, "cluster_id"))
	if err != nil {
		return err
	}
	out := []fbRow{}
	for _, p := range props {
		m, err := LoadMonth(r.Context(), s.DB, p.ID, month)
		if err != nil {
			return err
		}
		x := fbRow{PropertyID: p.ID, Property: p.Name, Cluster: p.Cluster}
		_ = s.DB.QueryRow(r.Context(), `SELECT coalesce(sum(plan_sale),0), coalesce(sum(ird_sale),0), coalesce(sum(banquet_sale),0) FROM fb_sale
			WHERE property_id=$1 AND sale_date >= $2 AND sale_date < $3`, p.ID, m.Start, m.Start.AddDate(0, 1, 0)).Scan(&x.Plan, &x.IRD, &x.Banquet)
		_ = s.DB.QueryRow(r.Context(), `SELECT plan_pct, ird_pct, banquet_pct FROM property WHERE id=$1`, p.ID).Scan(&x.PlanPct, &x.IRDPct, &x.BqPct)
		x.Sale = x.Plan + x.IRD + x.Banquet
		for sc, fb := range m.IsFB {
			if fb {
				x.Allowed += m.BudgetToDate(sc)
				x.Ordered += m.SpentMonth(sc)
				x.Budget += m.Budget[sc]
			}
		}
		if x.Sale > 0 {
			x.CostPct = money.Round2(x.Ordered / x.Sale * 100)
		}
		x.Pace, x.Tone, _ = Pace(x.Ordered, x.Allowed)
		x.Allowed, x.Ordered = money.Round2(x.Allowed), money.Round2(x.Ordered)
		out = append(out, x)
	}
	return httpjson.OK(w, out)
}

func (s *Service) salesExport(w http.ResponseWriter, r *http.Request) error {
	month := monthParam(r)
	rows, err := s.DB.Query(r.Context(), `SELECT p.code, p.name, f.sale_date::text, f.plan_sale::text, f.ird_sale::text, f.banquet_sale::text
		FROM fb_sale f JOIN property p ON p.id=f.property_id WHERE f.sale_date >= $1 AND f.sale_date < $2 ORDER BY p.code, f.sale_date`, month, month.AddDate(0, 1, 0))
	if err != nil {
		return err
	}
	out := [][]string{}
	for rows.Next() {
		var a, b, c, d, e, f string
		if err := rows.Scan(&a, &b, &c, &d, &e, &f); err != nil {
			return err
		}
		out = append(out, []string{a, b, c, d, e, f})
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="fnb-sales-%s.csv"`, clock.YMD(month)[:7]))
	_, err = w.Write(csvfile.Write([]string{"property_code", "property_name", "date", "plan_sale", "ird_sale", "banquet_sale"}, out))
	return err
}

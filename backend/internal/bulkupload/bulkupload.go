// Package bulkupload loads opening and bulk data from CSV files: items, vendor rates, budgets, F&B sales,
// property cost %, properties, users, vendors, request lines and the property-to-Zoho-tag file.
// Every row is checked before anything is saved, errors are shown per row, and only clean rows are imported.
package bulkupload

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
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
	"purchasedesk/internal/geography"
	"purchasedesk/internal/httpjson"
	"purchasedesk/internal/items"
	"purchasedesk/internal/money"
	"purchasedesk/internal/textclean"
	"purchasedesk/internal/users"
	"purchasedesk/internal/vendormapping"
	"purchasedesk/internal/vendors"
)

type Service struct {
	DB      *pgxpool.Pool
	Vendors *vendors.Service
	kinds   map[string]*kind
}

// kind describes one upload.
type kind struct {
	title    string
	module   string
	required []string
	sample   string
	allowed  func(u *access.User) bool
	check    func(ctx context.Context, x *run, row csvfile.Row) result
	commit   func(ctx context.Context, tx pgx.Tx, x *run, objs []any) error
	export   exportFn                                           // "Download current data", in the upload's columns; nil when there is none
	keyOf    func(row csvfile.Row) string                       // the row's key in that file; an uploaded row equal to it is skipped as unchanged
	onSame   func(ctx context.Context, x *run, row csvfile.Row) // sees a skipped unchanged row (a user's other roles)
	after    func(ctx context.Context, x *run) []result         // extra changes found once every row is read (a role row deleted)
	computed []string                                           // columns the workbook fills in itself (a vendor's name); not read and not compared
	derived  map[string]string                                  // column → the column it fills in from when that one has a value (category from item); not compared then
}

type result struct {
	ok, skip bool
	msg      string
	obj      any
}

// run carries one upload's context and the keys seen so far, to catch duplicate rows in the file.
type run struct {
	s          *Service
	u          *access.User
	q          database.Q
	seen       map[string]bool
	memo       map[string]any // rows that build one record together (a user's roles)
	propertyID int64
	vendorID   int64 // the vendor a corporate purchase's lines are for (their rate card)
}

func (x *run) dup(k string) bool {
	if x.seen[k] {
		return true
	}
	x.seen[k] = true
	return false
}

func num(v string) (float64, bool) {
	f, err := strconv.ParseFloat(strings.ReplaceAll(strings.TrimSpace(v), ",", ""), 64)
	return f, err == nil
}

func bad(msg string, a ...any) result { return result{msg: fmt.Sprintf(msg, a...)} }

// userLogin is the email (or, without one, the mobile) that ties a user's role rows together.
func userLogin(row csvfile.Row) string {
	if e := strings.ToLower(strings.TrimSpace(row.Get("email"))); e != "" {
		return e
	}
	return strings.TrimSpace(row.Get("mobile"))
}

// userGrant reads a users row's role, property access ("all", "cluster:A;B" or property codes) and category limits.
func userGrant(ctx context.Context, x *run, row csvfile.Row) (users.RoleGrant, string) {
	var roleID int64
	if err := x.q.QueryRow(ctx, `SELECT id FROM role WHERE active AND (lower(code)=lower($1) OR norm_key(name)=norm_key($1))`, row.Get("role")).Scan(&roleID); err != nil {
		return users.RoleGrant{}, fmt.Sprintf("Unknown role %q", row.Get("role"))
	}
	g := users.RoleGrant{RoleID: roleID, Scope: "all", ClusterIDs: []int64{}, PropertyIDs: []int64{}, CategoryIDs: []int64{}, SubCategoryIDs: []int64{}}
	acc := strings.TrimSpace(row.Get("access"))
	switch {
	case acc == "" || strings.EqualFold(acc, "all"):
	case strings.HasPrefix(strings.ToLower(acc), "cluster:"):
		g.Scope = "clusters"
		for _, n := range strings.Split(acc[8:], ";") {
			var id int64
			if err := x.q.QueryRow(ctx, `SELECT id FROM cluster WHERE norm_key(name)=norm_key($1)`, n).Scan(&id); err != nil {
				return g, fmt.Sprintf("Unknown cluster %q", n)
			}
			g.ClusterIDs = append(g.ClusterIDs, id)
		}
	default:
		g.Scope = "properties"
		for _, c := range strings.Split(acc, ";") {
			var id int64
			if err := x.q.QueryRow(ctx, `SELECT id FROM property WHERE upper(code)=upper($1)`, strings.TrimSpace(c)).Scan(&id); err != nil {
				return g, fmt.Sprintf("Unknown property code %q", c)
			}
			g.PropertyIDs = append(g.PropertyIDs, id)
		}
	}
	for _, c := range strings.Split(row.Get("categories"), ";") {
		if strings.TrimSpace(c) == "" {
			continue
		}
		var id int64
		if err := x.q.QueryRow(ctx, `SELECT id FROM category WHERE norm_key(name)=norm_key($1)`, c).Scan(&id); err != nil {
			return g, fmt.Sprintf("Unknown category %q", c)
		}
		g.CategoryIDs = append(g.CategoryIDs, id)
	}
	// The sub-categories a Warehouse Approver L1 approves: names or 2-letter codes (blank = all). Other roles ignore it.
	for _, c := range strings.Split(row.Get("sub_categories"), ";") {
		if strings.TrimSpace(c) == "" {
			continue
		}
		var id int64
		if err := x.q.QueryRow(ctx, `SELECT id FROM sub_category WHERE norm_key(name)=norm_key($1) OR upper(code)=upper(trim($1)) ORDER BY (upper(code)=upper(trim($1))) DESC LIMIT 1`, c).Scan(&id); err != nil {
			return g, fmt.Sprintf("Unknown sub-category %q", c)
		}
		g.SubCategoryIDs = append(g.SubCategoryIDs, id)
	}
	return g, ""
}

// editRow is a checked row: an edit of record id, or a new record when id is 0.
type editRow struct {
	id  int64
	obj any
}

func (s *Service) Routes(r chi.Router) {
	s.kinds = s.define()
	s.kinds["mappings"] = s.mappingsKind()
	for name, k := range s.smallMasterKinds() {
		s.kinds[name] = k
	}
	for name, k := range s.corporateKinds() {
		s.kinds[name] = k
	}
	s.kinds["approval_matrix"] = s.matrixKind()
	r.Get("/uploads/{kind}/sample", httpjson.Wrap(s.sample))
	r.Get("/uploads/{kind}/export", httpjson.Wrap(s.export))
	r.Get("/uploads/{kind}/workbook", httpjson.Wrap(s.workbook))
	r.Post("/uploads/{kind}/check", httpjson.Wrap(s.checkOnly))
	r.Post("/uploads/{kind}/import", httpjson.Wrap(s.importRows))
}

func hasUpload(u *access.User) bool { return u.HasRight(access.Upload) }

func (s *Service) define() map[string]*kind {
	masters := func(u *access.User) bool { return hasUpload(u) && u.HasRight(access.Masters) }
	catalog := func(u *access.User) bool { return hasUpload(u) && u.HasRight(access.Catalog) }
	return map[string]*kind{
		"items": {
			title: "Items", module: "Items", allowed: catalog,
			required: []string{"category", "sub_category", "item_name", "unit", "gst_percent"}, // brand may be blank for services
			sample:   "sku_code,category,sub_category,brand,item_name,unit,gst_percent,hsn,properties,tds_section,daily_consumption\n,Housekeeping,Guest Amenities,Aroma Leaf,Body lotion 30 ml,box of 250,18,33049910,ALL,,yes\n,Engineering,Electrical,BrightVolt,Pool pump capacitor,piece,18,,P01;P23,,no\n",
			keyOf:    key("sku_code"),
			check: func(ctx context.Context, x *run, row csvfile.Row) result {
				var scID, catID int64
				if err := x.q.QueryRow(ctx, `SELECT sc.id, c.id FROM sub_category sc JOIN category c ON c.id=sc.category_id WHERE norm_key(c.name)=norm_key($1) AND norm_key(sc.name)=norm_key($2)`,
					row.Get("category"), row.Get("sub_category")).Scan(&scID, &catID); err != nil {
					return bad("Sub-category %q not found in category %q", row.Get("sub_category"), row.Get("category"))
				}
				// A row with a sku_code edits that SKU; its sub-category (and so its code) stays as it is.
				id := int64(0)
				if code := strings.TrimSpace(row.Get("sku_code")); code != "" {
					var cur int64
					if err := x.q.QueryRow(ctx, `SELECT id, sub_category_id FROM sku WHERE upper(code)=upper($1)`, code).Scan(&id, &cur); err != nil {
						return bad("Unknown SKU code %s. Leave sku_code blank to add a new item", code)
					}
					if cur != scID {
						return bad("The sub-category of %s cannot change. Add it as a new item and mark %s inactive", code, code)
					}
					if x.dup("code" + strings.ToUpper(code)) {
						return bad("Duplicate row in the file")
					}
				}
				var packID int64
				if err := x.q.QueryRow(ctx, `SELECT id FROM pack_size WHERE norm_key(name)=norm_key($1)`, row.Get("unit")).Scan(&packID); err != nil {
					return bad("Pack size %q is not in the list. Add it under Settings, Item Setup", row.Get("unit"))
				}
				gst, ok := num(row.Get("gst_percent"))
				if !ok {
					return bad("GST %% must be a number")
				}
				in := &items.SKUInput{SubCategoryID: scID, BrandName: row.Get("brand"), ItemName: row.Get("item_name"), PackSizeID: packID, GST: gst, HSN: row.Get("hsn"), Scope: "all", TDSSection: strings.ToUpper(strings.TrimSpace(row.Get("tds_section")))}
				// stocked_at_warehouse is optional: blank keeps what is on file (no for a new item).
				if v := row.Get("stocked_at_warehouse"); strings.TrimSpace(v) != "" {
					st, ok := yesNo(v)
					if !ok {
						return bad("stocked_at_warehouse must be yes or no")
					}
					in.WHStocked = &st
				}
				// daily_consumption is optional: blank keeps what is on file (yes for a new item).
				if v := row.Get("daily_consumption"); strings.TrimSpace(v) != "" {
					daily, ok := yesNo(v)
					if !ok {
						return bad("daily_consumption must be yes or no")
					}
					in.DailyUse = &daily
				}
				all, clusters, props, problem := scope(ctx, x.q, row.Get("properties"))
				if problem != "" {
					return bad("%s", problem)
				}
				if !all {
					in.Scope, in.ClusterIDs, in.PropertyIDs = "selected", clusters, props
				}
				if errs := items.Validate(ctx, x.q, in, id); len(errs) > 0 {
					return bad("%s", strings.Join(errs, " "))
				}
				if x.dup(fmt.Sprintf("%d|%s|%s|%d", scID, textclean.Key(in.BrandName), textclean.Key(in.ItemName), packID)) {
					return bad("Duplicate row in the file")
				}
				msg := "New SKU"
				if id != 0 {
					msg = "Updates " + strings.ToUpper(strings.TrimSpace(row.Get("sku_code")))
				}
				if in.NewBrand() {
					msg += " · new brand " + in.BrandName
				}
				return result{ok: true, msg: msg, obj: &editRow{id, in}}
			},
			commit: func(ctx context.Context, tx pgx.Tx, x *run, objs []any) error {
				for _, o := range objs {
					e := o.(*editRow)
					in := e.obj.(*items.SKUInput)
					var err error
					if e.id == 0 {
						_, err = items.Create(ctx, tx, x.u, in)
					} else {
						_, err = items.Update(ctx, tx, x.u, e.id, in)
					}
					if err != nil {
						return err
					}
				}
				return nil
			},
			export: func(ctx context.Context, q database.Q, r *http.Request) ([]string, [][]string, error) {
				heads := []string{"sku_code", "category", "sub_category", "brand", "item_name", "unit", "gst_percent", "hsn", "properties", "tds_section", "daily_consumption", "stocked_at_warehouse"}
				rows, err := rowsOf(ctx, q, `SELECT k.code, c.name, sc.name, coalesce(b.name,''), k.item_name, ps.name, k.gst_pct::float8::text, k.hsn,
					`+fmt.Sprintf(scopeSQL, "k.scope='all'", "k.cluster_ids", "k.property_ids")+`, coalesce(k.tds_section,''), CASE WHEN k.daily_use THEN 'yes' ELSE 'no' END, CASE WHEN k.wh_stocked THEN 'yes' ELSE 'no' END
					FROM sku k JOIN sub_category sc ON sc.id=k.sub_category_id JOIN category c ON c.id=sc.category_id LEFT JOIN brand b ON b.id=k.brand_id JOIN pack_size ps ON ps.id=k.pack_size_id
					WHERE k.active ORDER BY k.code`)
				return heads, rows, err
			},
		},
		"rates": {
			title: "Vendor rates", module: "Vendor Rates", allowed: masters,
			required: []string{"vendor_code", "sku_code", "rate_ex_gst", "gst_percent", "valid_from", "valid_to"},
			sample:   "vendor_code,vendor_name,sku_code,item_name,unit,rate_ex_gst,gst_percent,valid_from,valid_to\nV001,Shree Balaji Hotel Supplies,GA-0001,Soap bar 20 g,box of 500,1380,18,2026-11-01,2027-03-31\n",
			keyOf:    key("vendor_code", "sku_code"),
			export: func(ctx context.Context, q database.Q, r *http.Request) ([]string, [][]string, error) {
				heads := []string{"vendor_code", "vendor_name", "sku_code", "item_name", "unit", "rate_ex_gst", "gst_percent", "valid_from", "valid_to"}
				rows, err := rowsOf(ctx, q, `SELECT DISTINCT ON (rc.vendor_id, rc.sku_id) v.code, v.legal_name, k.code, concat_ws(' ', b.name, k.item_name), ps.name,
					rc.rate::float8::text, rc.gst_pct::float8::text, to_char(rc.valid_from,'YYYY-MM-DD'), to_char(rc.valid_to,'YYYY-MM-DD')
					FROM rate_card rc JOIN vendor v ON v.id=rc.vendor_id JOIN sku k ON k.id=rc.sku_id LEFT JOIN brand b ON b.id=k.brand_id JOIN pack_size ps ON ps.id=k.pack_size_id
					WHERE NOT rc.superseded AND rc.valid_to >= current_date AND v.active AND k.active ORDER BY rc.vendor_id, rc.sku_id, rc.valid_from DESC`)
				return heads, rows, err
			},
			check: func(ctx context.Context, x *run, row csvfile.Row) result {
				if row.Get("rate_ex_gst") == "" {
					return result{skip: true, msg: "Skipped, no rate entered"}
				}
				var vid, sid int64
				if err := x.q.QueryRow(ctx, `SELECT id FROM vendor WHERE upper(code)=upper($1)`, row.Get("vendor_code")).Scan(&vid); err != nil {
					return bad("Unknown vendor code")
				}
				if err := x.q.QueryRow(ctx, `SELECT id FROM sku WHERE upper(code)=upper($1)`, row.Get("sku_code")).Scan(&sid); err != nil {
					return bad("Unknown SKU code")
				}
				rate, ok1 := num(row.Get("rate_ex_gst"))
				gst, ok2 := num(row.Get("gst_percent"))
				if !ok1 || !ok2 {
					return bad("Rate and GST %% must be numbers")
				}
				in := vendormapping.RateInput{VendorID: vid, SKUID: sid, Rate: rate, GST: gst, From: row.Get("valid_from"), To: row.Get("valid_to")}
				msg, errs := vendormapping.CheckRate(ctx, x.q, in)
				if len(errs) > 0 {
					return bad("%s", strings.Join(errs, " "))
				}
				if x.dup(fmt.Sprintf("%d|%d", vid, sid)) {
					return bad("Duplicate row in the file")
				}
				return result{ok: true, msg: msg, obj: in}
			},
			commit: func(ctx context.Context, tx pgx.Tx, x *run, objs []any) error {
				for _, o := range objs {
					if err := vendormapping.AddRate(ctx, tx, x.u, o.(vendormapping.RateInput)); err != nil {
						return err
					}
				}
				return nil
			},
		},
		"budgets": {
			title: "Monthly budgets", module: "Budgets", allowed: func(u *access.User) bool { return hasUpload(u) && (u.IsAdmin || u.HasStep("cc")) },
			required: []string{"property_code", "sub_category", "monthly_budget"},
			sample:   "property_code,property_name,month,sub_category,monthly_budget\nP01,Sector 45 Gurugram," + clock.YMD(clock.Today())[:7] + ",Guest Amenities,65000\n",
			keyOf:    key("property_code", "month", "sub_category"),
			export: func(ctx context.Context, q database.Q, r *http.Request) ([]string, [][]string, error) {
				m := clock.MonthStart(clock.Today())
				if v, ok := clock.ParseMonth(r.URL.Query().Get("month")); ok {
					m = v
				}
				heads := []string{"property_code", "property_name", "month", "sub_category", "monthly_budget"}
				rows, err := rowsOf(ctx, q, `SELECT p.code, p.name, to_char($1::date,'YYYY-MM'), sc.name, coalesce(bu.amount,0)::float8::text
					FROM property p CROSS JOIN sub_category sc LEFT JOIN budget bu ON bu.property_id=p.id AND bu.sub_category_id=sc.id AND bu.month=$1
					WHERE p.active AND p.kind='live' AND sc.active ORDER BY p.code, sc.name`, m)
				return heads, rows, err
			},
			check: func(ctx context.Context, x *run, row csvfile.Row) result {
				var pid, sid int64
				if err := x.q.QueryRow(ctx, `SELECT id FROM property WHERE upper(code)=upper($1)`, row.Get("property_code")).Scan(&pid); err != nil {
					return bad("Unknown property code")
				}
				if err := x.q.QueryRow(ctx, `SELECT id FROM sub_category WHERE norm_key(name)=norm_key($1)`, row.Get("sub_category")).Scan(&sid); err != nil {
					return bad("Unknown sub-category")
				}
				if wait := budgets.PendingFinance(ctx, x.q, pid, sid); wait != "" {
					return bad("%s waits for Finance approval: set its budget once approved", wait)
				}
				m := clock.MonthStart(clock.Today())
				if v := row.Get("month"); v != "" {
					var ok bool
					if m, ok = clock.ParseMonth(v); !ok {
						return bad("Month must be YYYY-MM")
					}
				}
				amt, ok := num(row.Get("monthly_budget"))
				if !ok || amt < 0 {
					return bad("Budget must be a number, 0 or more")
				}
				if x.dup(fmt.Sprintf("%d|%d|%s", pid, sid, clock.YMD(m))) {
					return bad("Duplicate row in the file")
				}
				var old float64
				_ = x.q.QueryRow(ctx, `SELECT amount FROM budget WHERE property_id=$1 AND sub_category_id=$2 AND month=$3`, pid, sid, m).Scan(&old)
				msg := "Was " + money.INR(old)
				if old == amt {
					msg = "No change"
				}
				return result{ok: true, msg: msg, obj: [4]any{pid, sid, m, amt}}
			},
			commit: func(ctx context.Context, tx pgx.Tx, x *run, objs []any) error {
				for _, o := range objs {
					a := o.([4]any)
					if _, err := budgets.Set(ctx, tx, x.u, a[0].(int64), a[1].(int64), a[2].(clockTime), a[3].(float64)); err != nil {
						return err
					}
				}
				return nil
			},
		},
		"fb_sales": {
			title: "F&B sales", module: "Budgets", allowed: func(u *access.User) bool { return hasUpload(u) && (u.IsAdmin || u.HasStep("cc")) },
			required: []string{"property_code", "date", "plan_sale", "ird_sale", "banquet_sale"},
			sample:   "property_code,property_name,date,plan_sale,ird_sale,banquet_sale\nP01,Sector 45 Gurugram," + clock.YMD(clock.Today()) + ",21500,6200,0\n",
			check: func(ctx context.Context, x *run, row csvfile.Row) result {
				var pid int64
				if err := x.q.QueryRow(ctx, `SELECT id FROM property WHERE upper(code)=upper($1)`, row.Get("property_code")).Scan(&pid); err != nil {
					return bad("Unknown property code")
				}
				d, ok := clock.ParseDate(row.Get("date"))
				if !ok || d.After(clock.Today()) {
					return bad("Date must be YYYY-MM-DD, today or earlier")
				}
				a, ok1 := num(row.Get("plan_sale"))
				b, ok2 := num(row.Get("ird_sale"))
				c, ok3 := num(row.Get("banquet_sale"))
				if !ok1 || !ok2 || !ok3 || a < 0 || b < 0 || c < 0 {
					return bad("Sales must be numbers, 0 or more")
				}
				if x.dup(fmt.Sprintf("%d|%s", pid, clock.YMD(d))) {
					return bad("Duplicate row in the file")
				}
				var exists bool
				_ = x.q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM fb_sale WHERE property_id=$1 AND sale_date=$2)`, pid, d).Scan(&exists)
				msg := "New day"
				if exists {
					msg = "Replaces the day already uploaded"
				}
				return result{ok: true, msg: msg, obj: [5]any{pid, d, a, b, c}}
			},
			commit: func(ctx context.Context, tx pgx.Tx, x *run, objs []any) error {
				for _, o := range objs {
					a := o.([5]any)
					if err := budgets.SetSales(ctx, tx, x.u, a[0].(int64), a[1].(clockTime), a[2].(float64), a[3].(float64), a[4].(float64)); err != nil {
						return err
					}
				}
				return nil
			},
		},
		"property_pct": {
			title: "F&B cost % by property", module: "Properties", allowed: masters,
			required: []string{"property_code", "plan_cost_pct", "ird_cost_pct", "banquet_cost_pct"},
			sample:   "property_code,property_name,plan_cost_pct,ird_cost_pct,banquet_cost_pct\nP01,Sector 45 Gurugram,34,31,38\n",
			keyOf:    key("property_code"),
			export: func(ctx context.Context, q database.Q, r *http.Request) ([]string, [][]string, error) {
				heads := []string{"property_code", "property_name", "plan_cost_pct", "ird_cost_pct", "banquet_cost_pct"}
				rows, err := rowsOf(ctx, q, `SELECT code, name, plan_pct::float8::text, ird_pct::float8::text, banquet_pct::float8::text FROM property WHERE active AND kind='live' ORDER BY code`)
				return heads, rows, err
			},
			check: func(ctx context.Context, x *run, row csvfile.Row) result {
				var pid int64
				var op, oi, ob float64
				if err := x.q.QueryRow(ctx, `SELECT id, plan_pct, ird_pct, banquet_pct FROM property WHERE upper(code)=upper($1)`, row.Get("property_code")).Scan(&pid, &op, &oi, &ob); err != nil {
					return bad("Unknown property code")
				}
				a, ok1 := num(row.Get("plan_cost_pct"))
				b, ok2 := num(row.Get("ird_cost_pct"))
				c, ok3 := num(row.Get("banquet_cost_pct"))
				if !ok1 || !ok2 || !ok3 || a < 0 || a > 100 || b < 0 || b > 100 || c < 0 || c > 100 {
					return bad("Each cost %% must be 0 to 100")
				}
				if x.dup(fmt.Sprint(pid)) {
					return bad("Duplicate row in the file")
				}
				return result{ok: true, msg: fmt.Sprintf("Was %v / %v / %v", op, oi, ob), obj: [4]any{pid, a, b, c}}
			},
			commit: func(ctx context.Context, tx pgx.Tx, x *run, objs []any) error {
				for _, o := range objs {
					a := o.([4]any)
					if _, err := tx.Exec(ctx, `UPDATE property SET plan_pct=$2, ird_pct=$3, banquet_pct=$4 WHERE id=$1`, a[0], a[1], a[2], a[3]); err != nil {
						return err
					}
				}
				return nil
			},
		},
		"properties": {
			title: "Properties", module: "Properties", allowed: masters,
			required: []string{"property_name", "ezee_code", "cluster"},
			sample:   "property_code,property_name,doc_code,ezee_code,cluster,stage,property_manager_login,plan_cost_pct,ird_cost_pct,banquet_cost_pct\n,\"Sector 45, Gurugram\",SG,EZ-10401,Gurugram North,live,ravi.kumar@saltstayz.com,35,32,38\n",
			keyOf:    key("property_code"),
			check: func(ctx context.Context, x *run, row csvfile.Row) result {
				in := geography.PropertyInput{Kind: "live", PlanPct: 35, IRDPct: 32, BanquetPct: 38}
				// A row with a property_code edits that property; a blank stage or cost % keeps what is on file.
				id := int64(0)
				if code := strings.TrimSpace(row.Get("property_code")); code != "" {
					if err := x.q.QueryRow(ctx, `SELECT id, kind, plan_pct::float8, ird_pct::float8, banquet_pct::float8 FROM property WHERE upper(code)=upper($1)`, code).
						Scan(&id, &in.Kind, &in.PlanPct, &in.IRDPct, &in.BanquetPct); err != nil {
						return bad("Unknown property code %s. Leave property_code blank to add a new property", code)
					}
					if x.dup("code" + strings.ToUpper(code)) {
						return bad("Duplicate row in the file")
					}
				}
				// doc_code starts the property's document numbers (PR_SG_0001); blank on an edit keeps the one on file.
				in.Name, in.EzeeCode, in.DocCode = row.Get("property_name"), row.Get("ezee_code"), row.Get("doc_code")
				if v := row.Get("stage"); strings.TrimSpace(v) != "" {
					in.Kind = strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(v), "-", ""), " ", ""))
				}
				if err := x.q.QueryRow(ctx, `SELECT id FROM cluster WHERE norm_key(name)=norm_key($1)`, row.Get("cluster")).Scan(&in.ClusterID); err != nil {
					return bad("Unknown cluster %q. Add it under Cities & Clusters", row.Get("cluster"))
				}
				if l := row.Get("property_manager_login"); l != "" {
					var uid int64
					if err := x.q.QueryRow(ctx, `SELECT id FROM app_user WHERE email=$1::citext OR mobile=$1`, strings.TrimSpace(l)).Scan(&uid); err != nil {
						return bad("No user signs in with %s", l)
					}
					in.L1UserID = &uid
				}
				for col, dst := range map[string]*float64{"plan_cost_pct": &in.PlanPct, "ird_cost_pct": &in.IRDPct, "banquet_cost_pct": &in.BanquetPct} {
					if v := row.Get(col); v != "" {
						f, ok := num(v)
						if !ok {
							return bad("%s must be a number", col)
						}
						*dst = f
					}
				}
				if errs := geography.ValidateProperty(ctx, x.q, &in, id); len(errs) > 0 {
					return bad("%s", strings.Join(errs, " "))
				}
				if x.dup(textclean.Key(in.Name)) || x.dup("ez"+in.EzeeCode) || (in.DocCode != "" && x.dup("doc"+in.DocCode)) {
					return bad("Duplicate row in the file")
				}
				msg := "New property"
				if id != 0 {
					msg = "Updates " + strings.ToUpper(strings.TrimSpace(row.Get("property_code")))
				}
				return result{ok: true, msg: msg, obj: &editRow{id, in}}
			},
			commit: func(ctx context.Context, tx pgx.Tx, x *run, objs []any) error {
				for _, o := range objs {
					e := o.(*editRow)
					in := e.obj.(geography.PropertyInput)
					var err error
					if e.id == 0 {
						_, _, err = geography.CreateProperty(ctx, tx, x.u, in)
					} else {
						err = geography.UpdateProperty(ctx, tx, x.u, e.id, in)
					}
					if err != nil {
						return err
					}
				}
				return nil
			},
			export: func(ctx context.Context, q database.Q, r *http.Request) ([]string, [][]string, error) {
				heads := []string{"property_code", "property_name", "doc_code", "ezee_code", "cluster", "stage", "property_manager_login", "plan_cost_pct", "ird_cost_pct", "banquet_cost_pct"}
				rows, err := rowsOf(ctx, q, `SELECT p.code, p.name, coalesce(p.doc_code,''), p.ezee_code, k.name, p.kind, coalesce(u.email::text, u.mobile, ''),
					p.plan_pct::float8::text, p.ird_pct::float8::text, p.banquet_pct::float8::text
					FROM property p JOIN cluster k ON k.id=p.cluster_id LEFT JOIN app_user u ON u.id=p.l1_user_id WHERE p.active ORDER BY p.code`)
				return heads, rows, err
			},
		},
		"users": {
			title: "Users", module: "Users", allowed: func(u *access.User) bool { return u.IsAdmin },
			required: []string{"name", "department", "role"},
			sample:   "name,employee_id,email,mobile,department,role,access,categories,password\nRavi Kumar,SS1001,ravi.kumar@saltstayz.com,9810000001,Operations,Property Manager (L1),P01,,\nNeha Bansal,SS1002,neha.bansal@saltstayz.com,,Operations,Cluster Manager (L2),cluster:Gurugram North,,\nSunil Yadav,SS1003,,9810000002,Housekeeping,Property Staff,P01,Housekeeping,\n",
			// A user's unchanged role rows are skipped but still count: if another of their rows changed, their roles are
			// rebuilt from every row, unchanged ones included. A user whose rows are all unchanged is not touched.
			keyOf: func(row csvfile.Row) string {
				if l := userLogin(row); l != "" && strings.TrimSpace(row.Get("role")) != "" {
					return l + "|" + textclean.Key(row.Get("role"))
				}
				return ""
			},
			onSame: func(ctx context.Context, x *run, row csvfile.Row) {
				g, problem := userGrant(ctx, x, row)
				if problem != "" {
					return
				}
				login := userLogin(row)
				if e, ok := x.memo["user:"+login].(*editRow); ok {
					in := e.obj.(*users.UserInput)
					in.Roles = append(in.Roles, g)
					return
				}
				pending, _ := x.memo["pending:"+login].([]users.RoleGrant)
				x.memo["pending:"+login] = append(pending, g)
			},
			// A user whose rows are all unchanged but who holds more roles than the file lists had a role row deleted:
			// their roles become the rows left. A user missing from the file entirely is left as is.
			after: func(ctx context.Context, x *run) []result {
				out := []result{}
				for k, v := range x.memo {
					login, ok := strings.CutPrefix(k, "pending:")
					if !ok || x.memo["user:"+login] != nil {
						continue
					}
					grants := v.([]users.RoleGrant)
					var id int64
					var n int
					in := &users.UserInput{}
					if err := x.q.QueryRow(ctx, `SELECT u.id, u.name, u.employee_id, coalesce(u.email::text,''), coalesce(u.mobile,''), u.department, (SELECT count(*) FROM user_role ur WHERE ur.user_id=u.id)
						FROM app_user u WHERE ($1 LIKE '%@%' AND u.email=$1::citext) OR u.mobile=$1 LIMIT 1`, login).Scan(&id, &in.Name, &in.EmployeeID, &in.Email, &in.Mobile, &in.Department, &n); err != nil || n <= len(grants) {
						continue
					}
					in.Roles = grants
					if errs := users.ValidateUser(ctx, x.q, in, id, 0); len(errs) > 0 {
						out = append(out, bad("%s: %s", in.Name, strings.Join(errs, " ")))
						continue
					}
					out = append(out, result{ok: true, msg: fmt.Sprintf("Removes %d role(s) from %s", n-len(grants), in.Name), obj: &editRow{id, in}})
				}
				return out
			},
			check: func(ctx context.Context, x *run, row csvfile.Row) result {
				g, problem := userGrant(ctx, x, row)
				if problem != "" {
					return bad("%s", problem)
				}
				// One row per role: rows with the same email (or mobile) build one user, and an existing user's roles become
				// exactly the rows in the file. A blank password keeps the current one.
				login := userLogin(row)
				if e, ok := x.memo["user:"+login].(*editRow); ok && login != "" {
					in := e.obj.(*users.UserInput)
					for _, have := range in.Roles {
						if have.RoleID == g.RoleID {
							return bad("This role is listed twice for %s", in.Name)
						}
					}
					in.Roles = append(in.Roles, g)
					return result{ok: true, msg: "Adds the role " + row.Get("role") + " to " + in.Name}
				}
				pending, _ := x.memo["pending:"+login].([]users.RoleGrant)
				in := &users.UserInput{Name: row.Get("name"), EmployeeID: row.Get("employee_id"), Email: row.Get("email"), Mobile: row.Get("mobile"), Department: row.Get("department"), Password: row.Get("password"), Roles: append(pending, g)}
				id := int64(0)
				_ = x.q.QueryRow(ctx, `SELECT id FROM app_user WHERE ($1<>'' AND email=$1::citext) OR ($2<>'' AND mobile=$2) ORDER BY (email=$1::citext) DESC NULLS LAST LIMIT 1`,
					strings.TrimSpace(in.Email), strings.TrimSpace(in.Mobile)).Scan(&id)
				// selfID 0: whether the Admin keeps their own Admin role is checked once all the rows are read.
				if errs := users.ValidateUser(ctx, x.q, in, id, 0); len(errs) > 0 {
					return bad("%s", strings.Join(errs, " "))
				}
				e := &editRow{id, in}
				if login != "" {
					x.memo["user:"+login] = e
				}
				msg := "New user"
				if id != 0 {
					msg = "Updates " + in.Name
				}
				return result{ok: true, msg: msg, obj: e}
			},
			commit: func(ctx context.Context, tx pgx.Tx, x *run, objs []any) error {
				for _, o := range objs {
					e, ok := o.(*editRow)
					if !ok {
						continue // an extra role row, already added to its user
					}
					in := e.obj.(*users.UserInput)
					if e.id != 0 && e.id == x.u.ID {
						admin := false
						for _, g := range in.Roles {
							var step string
							_ = tx.QueryRow(ctx, `SELECT step FROM role WHERE id=$1`, g.RoleID).Scan(&step)
							admin = admin || step == "admin"
						}
						if !admin {
							return httpjson.Bad("You cannot remove your own System Admin role. Keep a row with the System Admin role for %s.", in.Name)
						}
					}
					if _, err := users.SaveUser(ctx, tx, x.u, e.id, *in); err != nil {
						return err
					}
				}
				return nil
			},
			export: func(ctx context.Context, q database.Q, r *http.Request) ([]string, [][]string, error) {
				heads := []string{"name", "employee_id", "email", "mobile", "department", "role", "access", "categories", "sub_categories", "password"}
				rows, err := rowsOf(ctx, q, `SELECT u.name, u.employee_id, coalesce(u.email::text,''), coalesce(u.mobile,''), u.department, r.name,
					CASE ur.scope WHEN 'all' THEN 'all' WHEN 'clusters' THEN 'cluster:'||(SELECT string_agg(c.name, ';' ORDER BY c.name) FROM cluster c WHERE c.id = ANY(ur.cluster_ids))
						ELSE (SELECT string_agg(p.code, ';' ORDER BY p.code) FROM property p WHERE p.id = ANY(ur.property_ids)) END,
					coalesce((SELECT string_agg(c.name, ';' ORDER BY c.name) FROM category c WHERE c.id = ANY(ur.category_ids)),''),
					coalesce((SELECT string_agg(sc.name, ';' ORDER BY sc.name) FROM sub_category sc WHERE sc.id = ANY(ur.sub_category_ids)),''), ''
					FROM app_user u JOIN user_role ur ON ur.user_id=u.id JOIN role r ON r.id=ur.role_id WHERE u.active ORDER BY u.name, r.id`)
				return heads, rows, err
			},
		},
		"vendors": {
			title: "Vendors", module: "Vendors", allowed: catalog,
			required: []string{"vendor_type", "legal_name", "address", "city", "pin", "contact_person", "mobile", "email", "pan", "bank_holder", "bank_account", "ifsc", "payment_terms", "supplies"},
			sample:   "vendor_code,vendor_type,legal_name,address,city,pin,contact_person,mobile,email,gstin,pan,msme,udyam,bank_holder,bank_account,ifsc,payment_terms,supplies\n,goods,Shree Balaji Hotel Supplies,Plot 12 Udyog Vihar,Gurugram,122016,Rakesh Gupta,9810011111,orders@shreebalaji.example,06AABCS4107K1Z1,AABCS4107K,yes,UDYAM-HR-05-0012345,Shree Balaji Hotel Supplies,123456789012,HDFC0001234,Net 30,Guest Amenities;Cleaning Chemicals\n,utility,Electricity DISCOM,Sector 29,Gurugram,122001,Billing desk,9810022222,billing@discom.example,,AABCD1234E,no,,DHBVN,987654321098,SBIN0001234,Net 15,Electricity\n",
			keyOf:    key("vendor_code"),
			check: func(ctx context.Context, x *run, row csvfile.Row) result {
				in := vendors.Input{Type: strings.ToLower(row.Get("vendor_type")), Name: row.Get("legal_name"), Address: row.Get("address"), City: row.Get("city"), PIN: row.Get("pin"),
					Contact: row.Get("contact_person"), Mobile: row.Get("mobile"), Email: row.Get("email"), GSTIN: row.Get("gstin"), PAN: row.Get("pan"),
					MSME: yes(row.Get("msme")), Udyam: row.Get("udyam"),
					BankHolder: row.Get("bank_holder"), BankAccount: strings.TrimSpace(row.Get("bank_account")), IFSC: row.Get("ifsc"), Terms: row.Get("payment_terms")}
				in.BankAccount2 = in.BankAccount
				in.GSTRegistered = strings.TrimSpace(in.GSTIN) != ""
				// A row with a vendor_code edits that vendor. Bank details, GSTIN and PAN are locked here: they change only
				// on the vendor screen, where Finance re-verifies them. Account numbers are never in the download.
				id := int64(0)
				if code := strings.TrimSpace(row.Get("vendor_code")); code != "" {
					var vt, gstin, pan, holder, ifsc string
					var gstReg bool
					if err := x.q.QueryRow(ctx, `SELECT id, vendor_type, gst_registered, coalesce(gstin,''), coalesce(pan,''), coalesce(bank_holder,''), coalesce(ifsc,'') FROM vendor WHERE upper(code)=upper($1)`, code).
						Scan(&id, &vt, &gstReg, &gstin, &pan, &holder, &ifsc); err != nil {
						return bad("Unknown vendor code %s. Leave vendor_code blank to add a new vendor", code)
					}
					if vt == "warehouse" {
						return bad("The Central Warehouse is set up by the system and cannot be edited here")
					}
					if x.dup("code" + strings.ToUpper(code)) {
						return bad("Duplicate row in the file")
					}
					locked := []string{}
					differs := func(col, cur string) bool {
						v := strings.TrimSpace(row.Get(col))
						return v != "" && !strings.EqualFold(textclean.Key(v), textclean.Key(cur))
					}
					if in.BankAccount != "" {
						locked = append(locked, "bank_account")
					}
					for col, cur := range map[string]string{"bank_holder": holder, "ifsc": ifsc, "gstin": gstin, "pan": pan} {
						if differs(col, cur) {
							locked = append(locked, col)
						}
					}
					if gstin != "" && row.Has("gstin") && strings.TrimSpace(row.Get("gstin")) == "" {
						locked = append(locked, "gstin")
					}
					if len(locked) > 0 {
						sort.Strings(locked)
						return bad("%s cannot be changed in a bulk upload. Change bank details, GSTIN and PAN on the vendor screen, so Finance re-verifies them", strings.Join(locked, ", "))
					}
					in.GSTRegistered, in.GSTIN, in.PAN = gstReg, gstin, pan
					in.BankHolder, in.BankAccount, in.BankAccount2, in.IFSC = "", "", "", ""
				}
				for _, n := range strings.Split(row.Get("supplies"), ";") {
					if strings.TrimSpace(n) == "" {
						continue
					}
					var sid int64
					if in.Type == "goods" {
						if err := x.q.QueryRow(ctx, `SELECT id FROM sub_category WHERE norm_key(name)=norm_key($1)`, n).Scan(&sid); err != nil {
							return bad("Unknown sub-category %q", n)
						}
						in.SubCategoryIDs = append(in.SubCategoryIDs, sid)
					} else {
						if err := x.q.QueryRow(ctx, `SELECT id FROM bill_type WHERE norm_key(name)=norm_key($1)`, n).Scan(&sid); err != nil {
							return bad("Unknown bill type %q", n)
						}
						in.BillTypeIDs = append(in.BillTypeIDs, sid)
					}
				}
				if errs := x.s.Vendors.Validate(ctx, &in, id, nil, false); len(errs) > 0 {
					return bad("%s", strings.Join(errs, " "))
				}
				if x.dup(textclean.Key(in.Name)) || x.dup("pan"+in.PAN) {
					return bad("Duplicate row in the file")
				}
				msg := "New vendor, bank verification pending"
				if id != 0 {
					msg = "Updates " + strings.ToUpper(strings.TrimSpace(row.Get("vendor_code")))
				}
				return result{ok: true, msg: msg, obj: &editRow{id, &in}}
			},
			commit: func(ctx context.Context, tx pgx.Tx, x *run, objs []any) error {
				for _, o := range objs {
					e := o.(*editRow)
					in := e.obj.(*vendors.Input)
					if e.id == 0 {
						if _, _, err := x.s.Vendors.Insert(ctx, tx, x.u, in, nil, "", ""); err != nil {
							return err
						}
						continue
					}
					enc, last4, err := x.s.Vendors.Seal(in.BankAccount)
					if err != nil {
						return err
					}
					if _, _, err := x.s.Vendors.Update(ctx, tx, x.u, e.id, in, nil, enc, last4); err != nil {
						return err
					}
				}
				return nil
			},
			export: func(ctx context.Context, q database.Q, r *http.Request) ([]string, [][]string, error) {
				heads := []string{"vendor_code", "vendor_type", "legal_name", "address", "city", "pin", "contact_person", "mobile", "email", "gstin", "pan", "msme", "udyam", "bank_holder", "bank_account", "ifsc", "payment_terms", "supplies"}
				rows, err := rowsOf(ctx, q, `SELECT v.code, v.vendor_type, v.legal_name, coalesce(v.address,''), coalesce(v.city,''), coalesce(v.pin,''), coalesce(v.contact_person,''),
					coalesce(v.mobile,''), coalesce(v.email,''), coalesce(v.gstin,''), coalesce(v.pan,''), CASE WHEN v.msme THEN 'yes' ELSE 'no' END, coalesce(v.udyam,''),
					coalesce(v.bank_holder,''), '', coalesce(v.ifsc,''), v.payment_terms,
					CASE WHEN v.vendor_type='goods' THEN coalesce((SELECT string_agg(sc.name, ';' ORDER BY sc.name) FROM vendor_sub_category x JOIN sub_category sc ON sc.id=x.sub_category_id WHERE x.vendor_id=v.id),'')
						ELSE coalesce((SELECT string_agg(b.name, ';' ORDER BY b.name) FROM vendor_bill_type x JOIN bill_type b ON b.id=x.bill_type_id WHERE x.vendor_id=v.id),'') END
					FROM vendor v WHERE v.active AND v.vendor_type<>'warehouse' ORDER BY v.code`)
				return heads, rows, err
			},
		},
		"request_lines": {
			title: "Request lines", module: "Purchases", allowed: func(u *access.User) bool { return u.HasRight(access.Raise) },
			required: []string{"sku_code", "quantity"},
			sample:   "sku_code,item_name,quantity,rate\nGA-0001,Soap bar 20 g,4,\n",
			check: func(ctx context.Context, x *run, row csvfile.Row) result {
				var sid int64
				if err := x.q.QueryRow(ctx, `SELECT id FROM sku WHERE upper(code)=upper($1)`, row.Get("sku_code")).Scan(&sid); err != nil {
					return bad("Unknown SKU code")
				}
				if row.Get("quantity") == "" {
					return result{skip: true, msg: "Skipped, no quantity"}
				}
				q, ok := num(row.Get("quantity"))
				if !ok || q <= 0 {
					return bad("Quantity must be above 0")
				}
				src, found, err := vendormapping.One(ctx, x.q, sid, x.propertyID, 0)
				if err != nil || !found {
					return bad("Item is not available at this property")
				}
				if !src.Orderable {
					return bad("%s", src.Problem)
				}
				est := 0.0
				if src.Rate == nil {
					if est, ok = num(row.Get("rate")); !ok || est <= 0 {
						return bad("%s has no rate for this item. Enter an estimated rate", src.VendorName)
					}
				}
				if x.dup(fmt.Sprint(sid)) {
					return bad("Duplicate row in the file")
				}
				return result{ok: true, msg: src.VendorName, obj: map[string]any{"sku_id": sid, "qty": q, "est_rate": est}}
			},
		},
		"zoho_tags": {
			title: "Property to Zoho reporting tag", module: "Zoho", allowed: func(u *access.User) bool { return u.IsAdmin || u.HasStep("finance") },
			required: []string{"property_code", "reporting_tag_option"},
			sample:   "property_code,property_name,reporting_tag,reporting_tag_option\nP01,Sector 45 Gurugram,Haryana,Sector 45 Gurugram\n",
			keyOf:    key("property_code"),
			export: func(ctx context.Context, q database.Q, r *http.Request) ([]string, [][]string, error) {
				heads := []string{"property_code", "property_name", "reporting_tag", "reporting_tag_option"}
				rows, err := rowsOf(ctx, q, `SELECT p.code, p.name, coalesce(t.name,''), coalesce(o.name,'') FROM property p
					LEFT JOIN zoho_map_property m ON m.property_id=p.id LEFT JOIN zoho_tag_option o ON o.option_id=m.tag_option_id LEFT JOIN zoho_tag t ON t.tag_id=o.tag_id
					WHERE p.active ORDER BY p.code`)
				return heads, rows, err
			},
			check: func(ctx context.Context, x *run, row csvfile.Row) result {
				if strings.TrimSpace(row.Get("reporting_tag_option")) == "" {
					return result{skip: true, msg: "Skipped, no option entered"}
				}
				var pid int64
				if err := x.q.QueryRow(ctx, `SELECT id FROM property WHERE upper(code)=upper($1)`, row.Get("property_code")).Scan(&pid); err != nil {
					return bad("Unknown property code")
				}
				// The option may sit under any reporting tag. reporting_tag is optional; it settles a name used under two tags.
				var oid string
				var n int
				if err := x.q.QueryRow(ctx, `SELECT count(*), coalesce(min(o.option_id),'') FROM zoho_tag_option o JOIN zoho_tag t ON t.tag_id=o.tag_id
					WHERE o.active AND t.active AND norm_key(o.name)=norm_key($1) AND ($2='' OR norm_key(t.name)=norm_key($2))`,
					row.Get("reporting_tag_option"), row.Get("reporting_tag")).Scan(&n, &oid); err != nil || n == 0 {
					return bad("This option is not in any Zoho reporting tag. Sync from Zoho first if you just added it")
				}
				if n > 1 {
					return bad("This option name is used under more than one reporting tag. Fill the reporting_tag column")
				}
				if x.dup(fmt.Sprint(pid)) {
					return bad("Duplicate row in the file")
				}
				return result{ok: true, msg: "Will be mapped", obj: [2]any{pid, oid}}
			},
			commit: func(ctx context.Context, tx pgx.Tx, x *run, objs []any) error {
				for _, o := range objs {
					a := o.([2]any)
					if _, err := tx.Exec(ctx, `INSERT INTO zoho_map_property(property_id,tag_option_id,updated_by) VALUES ($1,$2,$3)
						ON CONFLICT (property_id) DO UPDATE SET tag_option_id=EXCLUDED.tag_option_id, updated_by=EXCLUDED.updated_by, updated_at=now()`, a[0], a[1], x.u.ID); err != nil {
						return err
					}
				}
				return nil
			},
		},
	}
}

// Register adds an upload kind defined by another package (for example warehouse opening stock).
func (s *Service) Register(name, title, module string, required []string, sample string, allowed func(*access.User) bool,
	check func(ctx context.Context, q database.Q, u *access.User, row csvfile.Row, dup func(string) bool) (ok, skip bool, msg string, obj any),
	commit func(ctx context.Context, tx pgx.Tx, u *access.User, objs []any) error) {
	if s.kinds == nil {
		s.kinds = s.define()
	}
	s.kinds[name] = &kind{title: title, module: module, required: required, sample: sample, allowed: allowed,
		check: func(ctx context.Context, x *run, row csvfile.Row) result {
			ok, skip, msg, obj := check(ctx, x.q, x.u, row, x.dup)
			return result{ok: ok, skip: skip, msg: msg, obj: obj}
		},
		commit: func(ctx context.Context, tx pgx.Tx, x *run, objs []any) error { return commit(ctx, tx, x.u, objs) }}
}

func (s *Service) kind(r *http.Request) (*kind, error) {
	k, ok := s.kinds[chi.URLParam(r, "kind")]
	if !ok {
		return nil, httpjson.NotFound("Upload type")
	}
	u := access.From(r)
	if !k.allowed(u) {
		return nil, httpjson.Forbidden("You do not have the right to upload %s. The Admin gives the Upload opening and bulk data right under Users & Rights.", strings.ToLower(k.title))
	}
	return k, nil
}

func (s *Service) sample(w http.ResponseWriter, r *http.Request) error {
	k, err := s.kind(r)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="sample-%s.csv"`, chi.URLParam(r, "kind")))
	_, err = w.Write([]byte(k.sample))
	return err
}

type rowOut struct {
	Sheet   string            `json:"sheet"`
	Line    int               `json:"line"`
	Values  map[string]string `json:"values"`
	OK      bool              `json:"ok"`
	Skip    bool              `json:"skip"`
	Message string            `json:"message"`
	Object  any               `json:"object,omitempty"`
}

// checkRows checks one table of an upload against q and returns every row's result and the clean rows' objects.
func (s *Service) checkRows(r *http.Request, q database.Q, p part) ([]rowOut, []any, error) {
	k := p.kind
	where := ""
	if p.sheet != "" {
		where = "Sheet " + p.sheet + ": "
	}
	miss := []string{}
	for _, h := range k.required {
		if !textclean.Contains(p.heads, h) {
			miss = append(miss, h)
		}
	}
	if len(miss) > 0 {
		return nil, nil, httpjson.Bad("%sMissing column: %s.", where, strings.Join(miss, ", "))
	}
	if len(p.rows) > 20000 {
		return nil, nil, httpjson.Bad("%sUpload at most 20,000 rows at a time.", where)
	}
	x := &run{s: s, u: access.From(r), q: q, seen: map[string]bool{}, memo: map[string]any{}, propertyID: int64(httpjson.QInt(r, "property_id", 0)), vendorID: int64(httpjson.QInt(r, "vendor_id", 0))}
	cur, err := s.currentRows(r, q, k)
	if err != nil {
		return nil, nil, err
	}
	out := []rowOut{}
	objs := []any{}
	for _, row := range p.rows {
		if cur != nil && cur.same(k, row) {
			if k.onSame != nil {
				k.onSame(r.Context(), x, row)
			}
			out = append(out, rowOut{Sheet: p.sheet, Line: row.Line, Values: row.Values, Skip: true, Message: "No change"})
			continue
		}
		res := k.check(r.Context(), x, row)
		ro := rowOut{Sheet: p.sheet, Line: row.Line, Values: row.Values, OK: res.ok, Skip: res.skip, Message: res.msg}
		if res.ok {
			objs = append(objs, res.obj)
			if k.commit == nil {
				ro.Object = res.obj
			}
		}
		out = append(out, ro)
	}
	if k.after != nil {
		for i, res := range k.after(r.Context(), x) {
			out = append(out, rowOut{Sheet: p.sheet, Line: -1 - i, Values: map[string]string{}, OK: res.ok, Message: res.msg})
			if res.ok {
				objs = append(objs, res.obj)
			}
		}
	}
	return out, objs, nil
}

// load reads the upload and checks the user may change every list in it.
func (s *Service) load(r *http.Request) ([]part, error) {
	k, err := s.kind(r)
	if err != nil {
		return nil, err
	}
	ps, err := s.parts(r, chi.URLParam(r, "kind"), k)
	if err != nil {
		return nil, err
	}
	u := access.From(r)
	for _, p := range ps {
		if !p.kind.allowed(u) {
			return nil, httpjson.Forbidden("You do not have the right to change %s (sheet %s). Remove that sheet from the file, or ask the System Admin.", strings.ToLower(p.kind.title), p.sheet)
		}
	}
	return ps, nil
}

// checkOnly shows what an upload would do. A workbook is tried out in a transaction that is rolled back, sheet by
// sheet in order, so a row that depends on an earlier sheet (a vendor's new sub-category, then its rate) checks right.
func (s *Service) checkOnly(w http.ResponseWriter, r *http.Request) error {
	ps, err := s.load(r)
	if err != nil {
		return err
	}
	if len(ps) == 1 && ps[0].sheet == "" {
		rows, objs, err := s.checkRows(r, s.DB, ps[0])
		if err != nil {
			return err
		}
		return httpjson.OK(w, map[string]any{"rows": rows, "valid": len(objs)})
	}
	all := []rowOut{}
	valid := 0
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		return err
	}
	defer tx.Rollback(r.Context())
	for _, p := range ps {
		rows, objs, err := s.checkRows(r, tx, p)
		if err != nil {
			return err
		}
		if len(objs) > 0 && p.kind.commit != nil {
			if err := applySheet(r.Context(), tx, s, access.From(r), p, objs); err != nil {
				for i := range rows {
					if rows[i].OK {
						rows[i].OK, rows[i].Message = false, "Not saved: "+err.Error()
					}
				}
				objs = nil
			}
		}
		all = append(all, rows...)
		valid += len(objs)
	}
	return httpjson.OK(w, map[string]any{"rows": all, "valid": valid})
}

// applySheet saves one checked table inside a savepoint, so a failed sheet in a dry run does not spoil the next one.
func applySheet(ctx context.Context, tx pgx.Tx, s *Service, u *access.User, p part, objs []any) error {
	sp, err := tx.Begin(ctx)
	if err != nil {
		return err
	}
	if err := p.kind.commit(ctx, sp, &run{s: s, u: u, q: sp, seen: map[string]bool{}, memo: map[string]any{}}, objs); err != nil {
		_ = sp.Rollback(ctx)
		return err
	}
	return sp.Commit(ctx)
}

// importRows checks the upload again inside one transaction and saves only the clean rows, sheet by sheet in order.
// If any sheet cannot be saved, nothing is saved.
func (s *Service) importRows(w http.ResponseWriter, r *http.Request) error {
	ps, err := s.load(r)
	if err != nil {
		return err
	}
	u := access.From(r)
	imported, skipped := 0, 0
	err = pgx.BeginFunc(r.Context(), s.DB, func(tx pgx.Tx) error {
		for _, p := range ps {
			if p.kind.commit == nil {
				return httpjson.Bad("This upload adds rows to the form; use Check file.")
			}
			rows, objs, err := s.checkRows(r, tx, p)
			if err != nil {
				return err
			}
			skipped += len(rows) - len(objs)
			if len(objs) == 0 {
				continue
			}
			if err := p.kind.commit(r.Context(), tx, &run{s: s, u: u, q: tx, seen: map[string]bool{}, memo: map[string]any{}}, objs); err != nil {
				if p.sheet != "" {
					return httpjson.Bad("Sheet %s could not be saved, so nothing was saved: %v", p.sheet, err)
				}
				return err
			}
			imported += len(objs)
			if err := auditlog.Write(r.Context(), tx, u, p.kind.module, "Bulk upload", p.kind.title, fmt.Sprintf("%d rows imported, %d rows not imported", len(objs), len(rows)-len(objs))); err != nil {
				return err
			}
		}
		if imported == 0 {
			return httpjson.Bad("No changed rows to import.")
		}
		return nil
	})
	if err != nil {
		return err
	}
	return httpjson.OK(w, map[string]any{"imported": imported, "skipped": skipped, "message": fmt.Sprintf("%d rows imported", imported)})
}

type clockTime = time.Time

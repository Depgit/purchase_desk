// Package bulkupload loads opening and bulk data from CSV files: items, vendor rates, budgets, F&B sales,
// property cost %, properties, users, vendors, request lines and the property-to-Zoho-tag file.
// Every row is checked before anything is saved, errors are shown per row, and only clean rows are imported.
package bulkupload

import (
	"context"
	"fmt"
	"net/http"
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
	"purchasedesk/internal/releasephase"
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
	propertyID int64
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

func (s *Service) Routes(r chi.Router) {
	s.kinds = s.define()
	// Uploads that belong to a later release phase are left out until RELEASE_PHASE reaches it.
	for name, phase := range map[string]int{"budgets": releasephase.Budgets, "fb_sales": releasephase.Budgets, "property_pct": releasephase.Budgets, "zoho_tags": releasephase.Zoho} {
		if !releasephase.On(phase) {
			delete(s.kinds, name)
		}
	}
	r.Get("/uploads/{kind}/sample", httpjson.Wrap(s.sample))
	r.Post("/uploads/{kind}/check", httpjson.Wrap(s.checkOnly))
	r.Post("/uploads/{kind}/import", httpjson.Wrap(s.importRows))
}

func hasUpload(u *access.User) bool { return u.HasRight(access.Upload) }

func (s *Service) define() map[string]*kind {
	masters := func(u *access.User) bool { return hasUpload(u) && u.HasRight(access.Masters) }
	return map[string]*kind{
		"items": {
			title: "Items", module: "Items", allowed: masters,
			required: []string{"category", "sub_category", "brand", "item_name", "unit", "gst_percent"},
			sample:   "category,sub_category,brand,item_name,unit,gst_percent,hsn,properties\nHousekeeping,Guest Amenities,Aroma Leaf,Body lotion 30 ml,box of 250,18,33049910,ALL\nEngineering,Electrical,BrightVolt,Pool pump capacitor,piece,18,,P01;P23\n",
			check: func(ctx context.Context, x *run, row csvfile.Row) result {
				var scID, catID int64
				if err := x.q.QueryRow(ctx, `SELECT sc.id, c.id FROM sub_category sc JOIN category c ON c.id=sc.category_id WHERE norm_key(c.name)=norm_key($1) AND norm_key(sc.name)=norm_key($2)`,
					row.Get("category"), row.Get("sub_category")).Scan(&scID, &catID); err != nil {
					return bad("Sub-category %q not found in category %q", row.Get("sub_category"), row.Get("category"))
				}
				var packID int64
				if err := x.q.QueryRow(ctx, `SELECT id FROM pack_size WHERE norm_key(name)=norm_key($1)`, row.Get("unit")).Scan(&packID); err != nil {
					return bad("Pack size %q is not in the list. Add it under Settings, Item Setup", row.Get("unit"))
				}
				gst, ok := num(row.Get("gst_percent"))
				if !ok {
					return bad("GST %% must be a number")
				}
				in := &items.SKUInput{SubCategoryID: scID, BrandName: row.Get("brand"), ItemName: row.Get("item_name"), PackSizeID: packID, GST: gst, HSN: row.Get("hsn"), Scope: "all"}
				if p := strings.TrimSpace(row.Get("properties")); p != "" && !strings.EqualFold(p, "ALL") {
					in.Scope = "selected"
					for _, code := range strings.FieldsFunc(p, func(r rune) bool { return r == ';' || r == '|' }) {
						var id int64
						if err := x.q.QueryRow(ctx, `SELECT id FROM property WHERE upper(code)=upper($1)`, strings.TrimSpace(code)).Scan(&id); err != nil {
							return bad("Unknown property code %s", code)
						}
						in.PropertyIDs = append(in.PropertyIDs, id)
					}
				}
				if errs := items.Validate(ctx, x.q, in, 0); len(errs) > 0 {
					return bad("%s", strings.Join(errs, " "))
				}
				if x.dup(fmt.Sprintf("%d|%s|%s|%d", scID, textclean.Key(in.BrandName), textclean.Key(in.ItemName), packID)) {
					return bad("Duplicate row in the file")
				}
				msg := "New SKU"
				if in.NewBrand() {
					msg += " · new brand " + in.BrandName
				}
				return result{ok: true, msg: msg, obj: in}
			},
			commit: func(ctx context.Context, tx pgx.Tx, x *run, objs []any) error {
				for _, o := range objs {
					if _, err := items.Create(ctx, tx, x.u, o.(*items.SKUInput)); err != nil {
						return err
					}
				}
				return nil
			},
		},
		"rates": {
			title: "Vendor rates", module: "Vendor Rates", allowed: masters,
			required: []string{"vendor_code", "sku_code", "rate_ex_gst", "gst_percent", "valid_from", "valid_to"},
			sample:   "vendor_code,vendor_name,sku_code,item_name,unit,rate_ex_gst,gst_percent,valid_from,valid_to\nV001,Shree Balaji Hotel Supplies,GA-0001,Soap bar 20 g,box of 500,1380,18,2026-11-01,2027-03-31\n",
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
			check: func(ctx context.Context, x *run, row csvfile.Row) result {
				var pid, sid int64
				if err := x.q.QueryRow(ctx, `SELECT id FROM property WHERE upper(code)=upper($1)`, row.Get("property_code")).Scan(&pid); err != nil {
					return bad("Unknown property code")
				}
				if err := x.q.QueryRow(ctx, `SELECT id FROM sub_category WHERE norm_key(name)=norm_key($1)`, row.Get("sub_category")).Scan(&sid); err != nil {
					return bad("Unknown sub-category")
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
			sample:   "property_name,ezee_code,cluster,stage,property_manager_login,plan_cost_pct,ird_cost_pct,banquet_cost_pct\n\"Sector 45, Gurugram\",EZ-10401,Gurugram North,live,ravi.kumar@saltstayz.com,35,32,38\n",
			check: func(ctx context.Context, x *run, row csvfile.Row) result {
				in := geography.PropertyInput{Name: row.Get("property_name"), EzeeCode: row.Get("ezee_code"), Kind: strings.ToLower(strings.ReplaceAll(textclean.Default(row.Get("stage"), "live"), "-", "")), PlanPct: 35, IRDPct: 32, BanquetPct: 38}
				if err := x.q.QueryRow(ctx, `SELECT id FROM cluster WHERE norm_key(name)=norm_key($1)`, row.Get("cluster")).Scan(&in.ClusterID); err != nil {
					return bad("Unknown cluster %q. Add it under Cities & Clusters", row.Get("cluster"))
				}
				if l := row.Get("property_manager_login"); l != "" {
					var id int64
					if err := x.q.QueryRow(ctx, `SELECT id FROM app_user WHERE email=$1::citext OR mobile=$1`, strings.TrimSpace(l)).Scan(&id); err != nil {
						return bad("No user signs in with %s", l)
					}
					in.L1UserID = &id
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
				if errs := geography.ValidateProperty(ctx, x.q, &in, 0); len(errs) > 0 {
					return bad("%s", strings.Join(errs, " "))
				}
				if x.dup(textclean.Key(in.Name)) || x.dup("ez"+in.EzeeCode) {
					return bad("Duplicate row in the file")
				}
				return result{ok: true, msg: "New property", obj: in}
			},
			commit: func(ctx context.Context, tx pgx.Tx, x *run, objs []any) error {
				for _, o := range objs {
					if _, _, err := geography.CreateProperty(ctx, tx, x.u, o.(geography.PropertyInput)); err != nil {
						return err
					}
				}
				return nil
			},
		},
		"users": {
			title: "Users", module: "Users", allowed: func(u *access.User) bool { return u.IsAdmin },
			required: []string{"name", "department", "role"},
			sample:   "name,email,mobile,department,role,access,categories,password\nRavi Kumar,ravi.kumar@saltstayz.com,9810000001,Operations,Property Manager (L1),P01,,\nNeha Bansal,neha.bansal@saltstayz.com,,Operations,Cluster Manager (L2),cluster:Gurugram North,,\nSunil Yadav,,9810000002,Housekeeping,Property Staff,P01,Housekeeping,\n",
			check: func(ctx context.Context, x *run, row csvfile.Row) result {
				var roleID int64
				if err := x.q.QueryRow(ctx, `SELECT id FROM role WHERE active AND (lower(code)=lower($1) OR norm_key(name)=norm_key($1))`, row.Get("role")).Scan(&roleID); err != nil {
					return bad("Unknown role %q", row.Get("role"))
				}
				g := users.RoleGrant{RoleID: roleID, Scope: "all"}
				acc := strings.TrimSpace(row.Get("access"))
				switch {
				case acc == "" || strings.EqualFold(acc, "all"):
				case strings.HasPrefix(strings.ToLower(acc), "cluster:"):
					g.Scope = "clusters"
					for _, n := range strings.Split(acc[8:], ";") {
						var id int64
						if err := x.q.QueryRow(ctx, `SELECT id FROM cluster WHERE norm_key(name)=norm_key($1)`, n).Scan(&id); err != nil {
							return bad("Unknown cluster %q", n)
						}
						g.ClusterIDs = append(g.ClusterIDs, id)
					}
				default:
					g.Scope = "properties"
					for _, c := range strings.Split(acc, ";") {
						var id int64
						if err := x.q.QueryRow(ctx, `SELECT id FROM property WHERE upper(code)=upper($1)`, strings.TrimSpace(c)).Scan(&id); err != nil {
							return bad("Unknown property code %q", c)
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
						return bad("Unknown category %q", c)
					}
					g.CategoryIDs = append(g.CategoryIDs, id)
				}
				in := users.UserInput{Name: row.Get("name"), Email: row.Get("email"), Mobile: row.Get("mobile"), Department: row.Get("department"), Password: row.Get("password"), Roles: []users.RoleGrant{g}}
				if errs := users.ValidateUser(ctx, x.q, &in, 0, x.u.ID); len(errs) > 0 {
					return bad("%s", strings.Join(errs, " "))
				}
				if (in.Email != "" && x.dup(in.Email)) || (in.Mobile != "" && x.dup(in.Mobile)) {
					return bad("Duplicate row in the file")
				}
				return result{ok: true, msg: "New user", obj: in}
			},
			commit: func(ctx context.Context, tx pgx.Tx, x *run, objs []any) error {
				for _, o := range objs {
					if _, err := users.SaveUser(ctx, tx, x.u, 0, o.(users.UserInput)); err != nil {
						return err
					}
				}
				return nil
			},
		},
		"vendors": {
			title: "Vendors", module: "Vendors", allowed: masters,
			required: []string{"vendor_type", "legal_name", "address", "city", "pin", "contact_person", "mobile", "email", "pan", "bank_holder", "bank_account", "ifsc", "payment_terms", "supplies"},
			sample:   "vendor_type,legal_name,address,city,pin,contact_person,mobile,email,gstin,pan,msme,udyam,bank_holder,bank_account,ifsc,payment_terms,supplies\ngoods,Shree Balaji Hotel Supplies,Plot 12 Udyog Vihar,Gurugram,122016,Rakesh Gupta,9810011111,orders@shreebalaji.example,06AABCS4107K1Z1,AABCS4107K,yes,UDYAM-HR-05-0012345,Shree Balaji Hotel Supplies,123456789012,HDFC0001234,Net 30,Guest Amenities;Cleaning Chemicals\nutility,Electricity DISCOM,Sector 29,Gurugram,122001,Billing desk,9810022222,billing@discom.example,,AABCD1234E,no,,DHBVN,987654321098,SBIN0001234,Net 15,Electricity\n",
			check: func(ctx context.Context, x *run, row csvfile.Row) result {
				in := vendors.Input{Type: strings.ToLower(row.Get("vendor_type")), Name: row.Get("legal_name"), Address: row.Get("address"), City: row.Get("city"), PIN: row.Get("pin"),
					Contact: row.Get("contact_person"), Mobile: row.Get("mobile"), Email: row.Get("email"), GSTIN: row.Get("gstin"), PAN: row.Get("pan"),
					MSME: textclean.Contains([]string{"yes", "y", "true", "1"}, strings.ToLower(row.Get("msme"))), Udyam: row.Get("udyam"),
					BankHolder: row.Get("bank_holder"), BankAccount: row.Get("bank_account"), BankAccount2: row.Get("bank_account"), IFSC: row.Get("ifsc"), Terms: row.Get("payment_terms")}
				in.GSTRegistered = strings.TrimSpace(in.GSTIN) != ""
				for _, n := range strings.Split(row.Get("supplies"), ";") {
					if strings.TrimSpace(n) == "" {
						continue
					}
					var id int64
					if in.Type == "goods" {
						if err := x.q.QueryRow(ctx, `SELECT id FROM sub_category WHERE norm_key(name)=norm_key($1)`, n).Scan(&id); err != nil {
							return bad("Unknown sub-category %q", n)
						}
						in.SubCategoryIDs = append(in.SubCategoryIDs, id)
					} else {
						if err := x.q.QueryRow(ctx, `SELECT id FROM bill_type WHERE norm_key(name)=norm_key($1)`, n).Scan(&id); err != nil {
							return bad("Unknown bill type %q", n)
						}
						in.BillTypeIDs = append(in.BillTypeIDs, id)
					}
				}
				if errs := x.s.Vendors.Validate(ctx, &in, 0, nil, false); len(errs) > 0 {
					return bad("%s", strings.Join(errs, " "))
				}
				if x.dup(textclean.Key(in.Name)) || x.dup("pan"+in.PAN) {
					return bad("Duplicate row in the file")
				}
				return result{ok: true, msg: "New vendor, bank verification pending", obj: &in}
			},
			commit: func(ctx context.Context, tx pgx.Tx, x *run, objs []any) error {
				for _, o := range objs {
					if _, _, err := x.s.Vendors.Insert(ctx, tx, x.u, o.(*vendors.Input), nil, "", ""); err != nil {
						return err
					}
				}
				return nil
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
				src, found, err := vendormapping.One(ctx, x.q, sid, x.propertyID)
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
			check: func(ctx context.Context, x *run, row csvfile.Row) result {
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
	Line    int               `json:"line"`
	Values  map[string]string `json:"values"`
	OK      bool              `json:"ok"`
	Skip    bool              `json:"skip"`
	Message string            `json:"message"`
	Object  any               `json:"object,omitempty"`
}

func (s *Service) checkAll(r *http.Request, q database.Q, k *kind) ([]rowOut, []any, error) {
	var in struct {
		Text string `json:"text"`
	}
	if err := httpjson.Decode(r, &in); err != nil {
		return nil, nil, err
	}
	heads, rows, err := csvfile.Parse(in.Text)
	if err != nil {
		return nil, nil, httpjson.Bad("%v", err)
	}
	miss := []string{}
	for _, h := range k.required {
		if !textclean.Contains(heads, h) {
			miss = append(miss, h)
		}
	}
	if len(miss) > 0 {
		return nil, nil, httpjson.Bad("Missing column: %s.", strings.Join(miss, ", "))
	}
	if len(rows) > 20000 {
		return nil, nil, httpjson.Bad("Upload at most 20,000 rows at a time.")
	}
	x := &run{s: s, u: access.From(r), q: q, seen: map[string]bool{}, propertyID: int64(httpjson.QInt(r, "property_id", 0))}
	out := []rowOut{}
	objs := []any{}
	for _, row := range rows {
		res := k.check(r.Context(), x, row)
		ro := rowOut{Line: row.Line, Values: row.Values, OK: res.ok, Skip: res.skip, Message: res.msg}
		if res.ok {
			objs = append(objs, res.obj)
			if k.commit == nil {
				ro.Object = res.obj
			}
		}
		out = append(out, ro)
	}
	return out, objs, nil
}

func (s *Service) checkOnly(w http.ResponseWriter, r *http.Request) error {
	k, err := s.kind(r)
	if err != nil {
		return err
	}
	rows, objs, err := s.checkAll(r, s.DB, k)
	if err != nil {
		return err
	}
	return httpjson.OK(w, map[string]any{"rows": rows, "valid": len(objs)})
}

// importRows checks the file again inside the transaction and saves only the clean rows.
func (s *Service) importRows(w http.ResponseWriter, r *http.Request) error {
	k, err := s.kind(r)
	if err != nil {
		return err
	}
	if k.commit == nil {
		return httpjson.Bad("This upload adds rows to the form; use Check file.")
	}
	u := access.From(r)
	var rows []rowOut
	var objs []any
	err = pgx.BeginFunc(r.Context(), s.DB, func(tx pgx.Tx) error {
		var err error
		if rows, objs, err = s.checkAll(r, tx, k); err != nil {
			return err
		}
		if len(objs) == 0 {
			return httpjson.Bad("No clean rows to import.")
		}
		if err := k.commit(r.Context(), tx, &run{s: s, u: u, q: tx, seen: map[string]bool{}}, objs); err != nil {
			return err
		}
		return auditlog.Write(r.Context(), tx, u, k.module, "Bulk upload", k.title, fmt.Sprintf("%d rows imported, %d rows not imported", len(objs), len(rows)-len(objs)))
	})
	if err != nil {
		return err
	}
	return httpjson.OK(w, map[string]any{"imported": len(objs), "skipped": len(rows) - len(objs), "message": fmt.Sprintf("%d rows imported", len(objs))})
}

type clockTime = time.Time

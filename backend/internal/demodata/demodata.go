// Package demodata loads a small sample company (properties, users for every role, vendors, items, rates,
// mappings, budgets and F&B sales) so every flow can be tried. It runs only when SEED_DEMO=true, and loads only the
// release phases that are switched on; raising RELEASE_PHASE adds the next phases' samples on the next start.
// Every sample user signs in with the password Demo@1234.
package demodata

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"purchasedesk/internal/clock"
	"purchasedesk/internal/encryption"
	"purchasedesk/internal/passwords"
	"purchasedesk/internal/releasephase"
)

const Password = "Demo@1234"

// loadedKey records, in the setting table, the highest release phase whose sample data is loaded.
const loadedKey = "demo_loaded_phase"

// Load adds the sample data for every phase up to the switched-on one that is not loaded yet.
// Phase 1 data loads only into an empty database. Raising RELEASE_PHASE later adds the next phases' samples.
func Load(ctx context.Context, db *pgxpool.Pool, box *encryption.Box) error {
	upTo := releasephase.Current()
	var props, loaded int
	if err := db.QueryRow(ctx, `SELECT (SELECT count(*) FROM property), coalesce((SELECT (value)::int FROM setting WHERE key=$1),0)`, loadedKey).Scan(&props, &loaded); err != nil {
		return err
	}
	if loaded == 0 && props > 0 {
		return nil // real data, or a database seeded before phases existed
	}
	if loaded >= upTo {
		return nil
	}
	hash, err := passwords.Hash(Password)
	if err != nil {
		return err
	}
	acct, _ := box.Seal("123456789012")
	err = pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
		d := &loader{ctx: ctx, tx: tx, hash: hash, acct: acct}
		if loaded < releasephase.Core {
			d.core()
		}
		if loaded < releasephase.Bills && upTo >= releasephase.Bills {
			d.billsBudgetsCorporate()
		}
		if loaded < releasephase.Warehouse && upTo >= releasephase.Warehouse {
			d.warehouse()
		}
		d.ex(`INSERT INTO setting(key,value) VALUES ($1,to_jsonb($2::int)) ON CONFLICT (key) DO UPDATE SET value=excluded.value, updated_at=now()`, loadedKey, upTo)
		d.ex(`INSERT INTO activity_log(user_name,role_name,module,action,record,details) VALUES ('System','System','Settings','Demo data loaded',$1, 'Sample users sign in with password Demo@1234')`, fmt.Sprintf("up to phase %d", upTo))
		return d.err
	})
	if err == nil {
		log.Printf("demo data loaded up to phase %d; sample users sign in with %s", upTo, Password)
	}
	return err
}

type loader struct {
	ctx  context.Context
	tx   pgx.Tx
	hash string
	acct string
	err  error
}

func (d *loader) ex(sql string, args ...any) {
	if d.err != nil {
		return
	}
	if _, e := d.tx.Exec(d.ctx, sql, args...); e != nil {
		d.err = fmt.Errorf("%w in: %.120s", e, sql)
	}
}

func (d *loader) id(sql string, args ...any) int64 {
	var v int64
	if d.err != nil {
		return 0
	}
	if e := d.tx.QueryRow(d.ctx, sql, args...).Scan(&v); e != nil {
		d.err = fmt.Errorf("%w in: %.120s", e, sql)
	}
	return v
}

func (d *loader) user(name, email, mobile, dep, role, scope string, clusters, props, cats []int64) int64 {
	var em, mo any
	if email != "" {
		em = email
	}
	if mobile != "" {
		mo = mobile
	}
	uid := d.id(`INSERT INTO app_user(name,email,mobile,department,password_hash) VALUES ($1,$2,$3,$4,$5) RETURNING id`, name, em, mo, dep, d.hash)
	if clusters == nil {
		clusters = []int64{}
	}
	if props == nil {
		props = []int64{}
	}
	if cats == nil {
		cats = []int64{}
	}
	d.ex(`INSERT INTO user_role(user_id,role_id,scope,cluster_ids,property_ids,category_ids) SELECT $1,id,$3,$4,$5,$6 FROM role WHERE code=$2`, uid, role, scope, clusters, props, cats)
	return uid
}

func (d *loader) vendor(code, typ, name, city, gstin string, msme, verified bool, terms string) int64 {
	pan := ""
	var g any
	if gstin != "" {
		g, pan = gstin, gstin[2:12]
	} else {
		pan = "AAAPF" + code[1:] + "1F"
		for len(pan) < 10 {
			pan = pan[:5] + "0" + pan[5:]
		}
	}
	state := map[string]string{"Gurugram": "HR", "Delhi": "DL"}[city]
	return d.id(`INSERT INTO vendor(code,vendor_type,legal_name,address,city,pin,contact_person,mobile,email,gst_registered,gstin,pan,state_code,msme,udyam,bank_holder,bank_account_enc,bank_account_last4,ifsc,bank_verified,payment_terms)
		VALUES ($1,$2,$3,'Demo address',$4,'122001','Sales desk','9811100000',$5,$6,$7,$8,$9,$10,$11,$3,$12,'9012','HDFC0001234',$13,$14) RETURNING id`,
		code, typ, name, city, strings.ToLower(strings.ReplaceAll(name, " ", ""))+"@vendor.example", gstin != "", g, pan, state, msme, map[bool]string{true: "UDYAM-HR-05-0012345", false: ""}[msme], d.acct, verified, terms)
}

// byCode looks up an id from data an earlier phase loaded.
func (d *loader) byCode(table, code string) int64 {
	return d.id(`SELECT id FROM `+table+` WHERE code=$1`, code)
}

type demoSKU struct {
	code, name, pack, sc, brand string
	gst, rate                   float64
	hsn                         string
}

var demoSKUs = []demoSKU{
	{"GA-0001", "Soap bar 20 g", "box of 500", "GA", "Aroma Leaf", 18, 1450, "34011190"}, {"GA-0002", "Shampoo 30 ml", "box of 250", "GA", "Aroma Leaf", 18, 1900, "33051090"},
	{"GA-0003", "Dental kit", "box of 200", "GA", "Aroma Leaf", 18, 2200, ""}, {"CC-0001", "Floor cleaner", "5 L can", "CC", "CleanMax", 18, 620, "34022090"},
	{"CC-0002", "Toilet cleaner", "5 L can", "CC", "CleanMax", 18, 540, "34022090"}, {"LN-0001", "Bed sheet double, white", "piece", "LN", "SoftWeave", 5, 780, "63022100"},
	{"LN-0002", "Bath towel 500 GSM", "piece", "LN", "SoftWeave", 5, 310, "63026000"}, {"GR-0001", "Basmati rice", "25 kg bag", "GR", "Daily Harvest", 5, 2350, "10063020"},
	{"GR-0002", "Refined oil", "15 L tin", "GR", "Daily Harvest", 5, 2100, "15121910"}, {"DY-0001", "Milk", "1 L pouch", "DY", "Farm Fresh", 0, 62, "04012000"},
	{"DY-0002", "Paneer", "1 kg", "DY", "Farm Fresh", 5, 360, "04061000"}, {"EL-0001", "LED bulb 9 W", "piece", "EL", "BrightVolt", 18, 85, "85395000"},
	{"EL-0002", "MCB 16 A", "piece", "EL", "BrightVolt", 18, 240, "85362010"}, {"PL-0001", "Bib tap 15 mm", "piece", "PL", "FlowLine", 18, 420, "84818090"},
	{"ST-0001", "A4 paper 75 GSM", "ream", "ST", "PaperPro", 12, 265, "48025690"}, {"ST-0002", "Key card sleeve", "pack of 500", "ST", "PaperPro", 18, 950, ""},
}

// core loads phase 1: cities, clusters, live and pre-opening properties, the purchase-cycle users, items, vendors, mappings and rates.
func (d *loader) core() {
	ex, id, user := d.ex, d.id, d.user
	gur := id(`INSERT INTO city(name,state_code) VALUES ('Gurugram','HR') RETURNING id`)
	del := id(`INSERT INTO city(name,state_code) VALUES ('Delhi','DL') RETURNING id`)
	neha := user("Neha Bansal", "neha@demo.saltstayz.com", "", "Operations", "l2", "all", nil, nil, nil)
	arjun := user("Arjun Malhotra", "arjun@demo.saltstayz.com", "", "Operations", "l2", "all", nil, nil, nil)
	sana := user("Sana Qureshi", "sana@demo.saltstayz.com", "", "Operations", "l2", "all", nil, nil, nil)
	gn := id(`INSERT INTO cluster(name,city_id,manager_id) VALUES ('Gurugram North',$1,$2) RETURNING id`, gur, neha)
	gs := id(`INSERT INTO cluster(name,city_id,manager_id) VALUES ('Gurugram South',$1,$2) RETURNING id`, gur, arjun)
	ds := id(`INSERT INTO cluster(name,city_id,manager_id) VALUES ('Delhi South',$1,$2) RETURNING id`, del, sana)
	ex(`UPDATE user_role SET scope='clusters', cluster_ids=ARRAY[$2::bigint] WHERE user_id=$1`, neha, gn)
	ex(`UPDATE user_role SET scope='clusters', cluster_ids=ARRAY[$2::bigint] WHERE user_id=$1`, arjun, gs)
	ex(`UPDATE user_role SET scope='clusters', cluster_ids=ARRAY[$2::bigint] WHERE user_id=$1`, sana, ds)
	props := map[string]int64{}
	for _, p := range []struct {
		code, doc, name, ezee string
		cl                    int64
		kind                  string
	}{
		{"P01", "SA", "Sector 45, Gurugram", "EZ-10401", gn, "live"}, {"P02", "SB", "Sector 14, Gurugram", "EZ-10402", gn, "live"},
		{"P03", "DL", "DLF Phase 2, Gurugram", "EZ-10403", gs, "live"}, {"P04", "SK", "Saket, Delhi", "EZ-10404", ds, "live"},
		{"P05", "HK", "Hauz Khas, Delhi", "EZ-10405", ds, "live"},
		{"X01", "SX", "Sector 65, Gurugram (pre-opening)", "PRE-001", gs, "preopening"},
	} {
		props[p.code] = id(`INSERT INTO property(code,doc_code,name,ezee_code,cluster_id,kind) VALUES ($1,$2,$3,$4,$5,$6) RETURNING id`, p.code, p.doc, p.name, p.ezee, p.cl, p.kind)
	}
	cats := map[string]int64{}
	for _, c := range [][2]string{{"Housekeeping", "f"}, {"Food & Beverage", "t"}, {"Engineering", "f"}, {"Admin & Front Office", "f"}} {
		cats[c[0]] = id(`INSERT INTO category(name,is_fb) VALUES ($1,$2) RETURNING id`, c[0], c[1] == "t")
	}
	ravi := user("Ravi Kumar", "ravi@demo.saltstayz.com", "9810000001", "Operations", "l1", "properties", nil, []int64{props["P01"]}, nil)
	ex(`UPDATE property SET l1_user_id=$1 WHERE code IN ('P01','P02')`, ravi)
	user("Sunil Yadav", "sunil@demo.saltstayz.com", "9810000002", "Housekeeping", "staff", "properties", nil, []int64{props["P01"]}, nil)
	user("Amit Arora", "amit@demo.saltstayz.com", "", "Finance", "cc", "all", nil, nil, nil)
	user("Pooja Verma", "pooja@demo.saltstayz.com", "", "Purchase", "purchase", "all", nil, nil, nil)
	user("Deepak Singh", "deepak@demo.saltstayz.com", "9810000003", "Stores", "store", "properties", nil, []int64{props["P01"], props["P02"]}, nil)
	user("Kavita Joshi", "kavita@demo.saltstayz.com", "", "Finance", "ap", "all", nil, nil, nil)
	user("Rohit Mehra", "rohit@demo.saltstayz.com", "", "Finance", "finance", "all", nil, nil, nil)
	user("Shalini Rao", "shalini@demo.saltstayz.com", "", "Projects", "preop", "all", nil, nil, nil)
	subs := map[string]int64{}
	for _, s := range [][3]string{{"Guest Amenities", "Housekeeping", "GA"}, {"Cleaning Chemicals", "Housekeeping", "CC"}, {"Linen", "Housekeeping", "LN"},
		{"Dry Groceries", "Food & Beverage", "GR"}, {"Dairy", "Food & Beverage", "DY"}, {"Electrical", "Engineering", "EL"}, {"Plumbing", "Engineering", "PL"},
		{"Stationery", "Admin & Front Office", "ST"}} {
		subs[s[2]] = id(`INSERT INTO sub_category(name,category_id,code) VALUES ($1,$2,$3) RETURNING id`, s[0], cats[s[1]], s[2])
	}
	brands := map[string]int64{}
	for _, b := range []string{"Generic", "Aroma Leaf", "CleanMax", "SoftWeave", "Daily Harvest", "Farm Fresh", "BrightVolt", "FlowLine", "PaperPro"} {
		brands[b] = id(`INSERT INTO brand(name) VALUES ($1) RETURNING id`, b)
	}
	packs := map[string]int64{}
	pack := func(n string) int64 {
		if v, ok := packs[n]; ok {
			return v
		}
		packs[n] = id(`INSERT INTO pack_size(name) VALUES ($1) RETURNING id`, n)
		return packs[n]
	}
	skus := demoSKUs
	skuID := map[string]int64{}
	for _, s := range skus {
		skuID[s.code] = id(`INSERT INTO sku(code,sub_category_id,brand_id,item_name,pack_size_id,gst_pct,hsn) VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING id`,
			s.code, subs[s.sc], brands[s.brand], s.name, pack(s.pack), s.gst, s.hsn)
	}
	vendor := d.vendor
	v := map[string]int64{
		"V001": vendor("V001", "goods", "Shree Balaji Hotel Supplies", "Gurugram", "06AABCS4107K1Z1", true, true, "Net 30"),
		"V002": vendor("V002", "goods", "CleanPro Chemicals", "Delhi", "07AABCC4114K1Z2", false, true, "Net 30"),
		"V003": vendor("V003", "goods", "Gupta Linen House", "Delhi", "07AABCG4121K1Z3", true, true, "Net 45"),
		"V004": vendor("V004", "goods", "Annapurna Traders", "Gurugram", "06AABCA4128K1Z4", false, true, "Net 15"),
		"V005": vendor("V005", "goods", "Fresh Dairy Point", "Gurugram", "", false, true, "Net 15"),
		"V006": vendor("V006", "goods", "Sharma Electricals", "Delhi", "07AABCS4135K1Z5", true, true, "Net 30"),
		"V007": vendor("V007", "goods", "Capital Hardware Store", "Gurugram", "06AABCC4142K1Z6", false, false, "Net 30"),
		"V008": vendor("V008", "goods", "Paper Mart", "Delhi", "07AABCP4149K1Z7", false, true, "Net 30"),
	}
	for vc, sc := range map[string][]string{"V001": {"GA"}, "V002": {"CC"}, "V003": {"LN"}, "V004": {"GR"}, "V005": {"DY"}, "V006": {"EL"}, "V007": {"PL"}, "V008": {"ST"}} {
		for _, c := range sc {
			ex(`INSERT INTO vendor_sub_category VALUES ($1,$2)`, v[vc], subs[c])
		}
	}
	live := []int64{props["P01"], props["P02"], props["P03"], props["P04"], props["P05"]}
	mapSC := func(vc, sc string, all bool, ps []int64, est bool) {
		mid := id(`INSERT INTO vendor_mapping(vendor_id,level,sub_category_id,all_properties,estimate_allowed) VALUES ($1,'sc',$2,$3,$4) RETURNING id`, v[vc], subs[sc], all, est)
		for _, p := range ps {
			ex(`INSERT INTO vendor_mapping_property VALUES ($1,$2)`, mid, p)
		}
	}
	mapSC("V001", "GA", true, nil, false)
	mapSC("V002", "CC", true, nil, false)
	mapSC("V003", "LN", true, nil, true)
	mapSC("V004", "GR", true, nil, false)
	mapSC("V005", "DY", false, live, false)
	mapSC("V006", "EL", true, nil, false)
	mapSC("V007", "PL", true, nil, false)
	mapSC("V008", "ST", true, nil, true)
	vendorOf := map[string]string{"GA": "V001", "CC": "V002", "LN": "V003", "GR": "V004", "DY": "V005", "EL": "V006", "PL": "V007", "ST": "V008"}
	for _, s := range skus {
		if s.code == "LN-0002" {
			continue // linen relies on the estimate rule for this item
		}
		ex(`INSERT INTO rate_card(vendor_id,sku_id,rate,gst_pct,valid_from,valid_to) VALUES ($1,$2,$3,$4,'2026-04-01','2027-03-31')`, v[vendorOf[s.sc]], skuID[s.code], s.rate, s.gst)
	}
	_ = gs
}

// billsBudgetsCorporate loads phase 3: utility and AMC vendors and bill types, this month's budgets and F&B sales,
// and the corporate office with its departments, users, expense heads and vendors.
func (d *loader) billsBudgetsCorporate() {
	ex, id, user, vendor := d.ex, d.id, d.user, d.vendor
	v := map[string]int64{
		"V010": vendor("V010", "utility", "Electricity DISCOM", "Gurugram", "06AABCE4156K1Z8", false, true, "Net 15"),
		"V011": vendor("V011", "utility", "Water and Sewerage Board", "Delhi", "07AABCW4163K1Z9", false, true, "Net 15"),
		"V013": vendor("V013", "amc", "Skyline Lift Services", "Gurugram", "06AABCS4170K1ZA", false, true, "Net 15"),
	}
	bt := map[string]int64{}
	for _, b := range [][3]string{{"Electricity", "utility", "0"}, {"Water", "utility", "0"}, {"Internet / broadband", "utility", "18"}, {"Lift AMC", "amc", "18"}, {"Pest control", "amc", "18"}} {
		bt[b[0]] = id(`INSERT INTO bill_type(name,section,default_gst) VALUES ($1,$2,$3::numeric) RETURNING id`, b[0], b[1], b[2])
	}
	ex(`INSERT INTO vendor_bill_type VALUES ($1,$2),($3,$4),($5,$6)`, v["V010"], bt["Electricity"], v["V011"], bt["Water"], v["V013"], bt["Lift AMC"])
	month := clock.MonthStart(clock.Today())
	budget := map[string]float64{"GA": 60000, "CC": 35000, "LN": 45000, "GR": 160000, "DY": 85000, "EL": 30000, "PL": 25000, "ST": 25000}
	for i, code := range []string{"P01", "P02", "P03", "P04", "P05"} {
		p := d.byCode("property", code)
		for sc, amt := range budget {
			ex(`INSERT INTO budget(property_id,sub_category_id,month,amount) VALUES ($1,$2,$3,$4)`, p, d.byCode("sub_category", sc), month, amt*(1+float64(i)*0.1))
		}
		for day := 1; day <= clock.Today().Day(); day++ {
			dt := month.AddDate(0, 0, day-1)
			ex(`INSERT INTO fb_sale(property_id,sale_date,plan_sale,ird_sale,banquet_sale) VALUES ($1,$2,$3,$4,$5)`, p, dt, 18000+float64((day*37+i*11)%9)*900, 5200+float64(day%5)*300, float64((day+i)%4)*8000)
		}
	}
	// Corporate office, its departments (L1 then L2) and the expense heads used instead of SKUs.
	gs := id(`SELECT id FROM cluster WHERE name='Gurugram South'`)
	co := id(`INSERT INTO property(code,doc_code,name,ezee_code,cluster_id,kind) VALUES ('CO1','CO','Corporate Office, Gurugram','CORP-001',$1,'corporate') RETURNING id`, gs)
	user("Meera Iyer", "meera@demo.saltstayz.com", "", "Admin", "creq", "properties", nil, []int64{co}, nil)
	rakesh := user("Rakesh Jain", "rakesh@demo.saltstayz.com", "", "Admin", "dh", "properties", nil, []int64{co}, nil)
	vivek := user("Vivek Rao", "vivek@demo.saltstayz.com", "", "IT", "dh", "properties", nil, []int64{co}, nil)
	anil := user("Anil Kapoor", "anil@demo.saltstayz.com", "", "Finance", "capp", "properties", nil, []int64{co}, nil)
	ex(`INSERT INTO corp_department(name,l1_user_id,l2_user_id) VALUES ('Admin',$1,$3),('IT',$2,$3)`, rakesh, vivek, anil)
	ex(`INSERT INTO expense_head(name) VALUES ('IT Equipment'),('Software Subscriptions'),('Office Supplies'),('Professional Fees'),('Travel and Conveyance'),('Staff Welfare')`)
	vendor("V020", "other", "Compu Solutions Pvt Ltd", "Gurugram", "06AABCC4177K1ZB", false, true, "Net 30")
	vendor("V021", "other", "Kapoor and Associates", "Delhi", "07AABFK4184K1ZC", true, true, "Net 15")
}

// warehouse loads phase 4: the central warehouse, its manager, what it supplies, its rates, opening stock and delivery partners.
func (d *loader) warehouse() {
	ex, id := d.ex, d.id
	gs := id(`SELECT id FROM cluster WHERE name='Gurugram South'`)
	id(`INSERT INTO property(code,doc_code,name,ezee_code,cluster_id,kind) VALUES ('WH','WH','Central Warehouse, Gurugram','WH-001',$1,'warehouse') RETURNING id`, gs)
	d.user("Vikas Tomar", "vikas@demo.saltstayz.com", "", "Warehouse", "wh", "all", nil, nil, nil)
	wh := id(`SELECT id FROM vendor WHERE vendor_type='warehouse' LIMIT 1`)
	ex(`UPDATE vendor SET city='Gurugram', state_code='HR' WHERE id=$1`, wh)
	for _, sc := range []string{"GA", "LN", "EL", "ST"} {
		ex(`INSERT INTO vendor_sub_category VALUES ($1,$2)`, wh, d.byCode("sub_category", sc))
	}
	// The warehouse supplies bed sheets and LED bulbs to the Gurugram properties.
	for _, sk := range []string{"LN-0001", "EL-0001"} {
		mid := id(`INSERT INTO vendor_mapping(vendor_id,level,sku_id,all_properties) VALUES ($1,'sku',$2,FALSE) RETURNING id`, wh, d.byCode("sku", sk))
		for _, p := range []string{"P01", "P02", "P03"} {
			ex(`INSERT INTO vendor_mapping_property VALUES ($1,$2)`, mid, d.byCode("property", p))
		}
	}
	for _, s := range demoSKUs {
		switch s.code {
		case "LN-0001", "EL-0001", "GA-0001", "ST-0001":
			ex(`INSERT INTO rate_card(vendor_id,sku_id,rate,gst_pct,valid_from,valid_to) VALUES ($1,$2,$3,$4,'2026-04-01','2027-03-31')`, wh, d.byCode("sku", s.code), s.rate*0.9, s.gst)
		}
	}
	// Warehouse opening stock and reorder levels, and the vehicles that deliver to properties.
	for _, x := range []struct {
		sku       string
		open, min float64
	}{{"LN-0001", 450, 150}, {"EL-0001", 900, 300}, {"GA-0001", 60, 20}, {"ST-0001", 160, 60}} {
		ex(`INSERT INTO warehouse_stock(sku_id,opening_qty,min_level) VALUES ($1,$2,$3)`, d.byCode("sku", x.sku), x.open, x.min)
	}
	ex(`INSERT INTO delivery_partner(name,kind,driver,phone) VALUES ('Own vehicle 1 (Tata Ace)','own','Ramesh','9810000111'),('QuickMove Logistics','partner','Assigned per trip','9810000121')`)
}

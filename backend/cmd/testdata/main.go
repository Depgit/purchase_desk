// Command testdata writes a complete test data set for Purchase Desk as Excel files, one per bulk-upload screen, in
// the order they are uploaded, plus a test plan. It is meant for a fresh test database (SEED_DEMO=false):
// codes the app assigns on import (P01, V001, LN-0001 ...) are then predictable, so the later files
// can refer to them. Dates (F&B sales, rate validity) follow the day it is run, so run it again before a new test.
//
//	go run ./cmd/testdata ../testdata
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"purchasedesk/internal/spreadsheet"
)

func main() {
	out := "../testdata"
	if len(os.Args) > 1 {
		out = os.Args[1]
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		panic(err)
	}
	ist, _ := time.LoadLocation("Asia/Kolkata")
	today := time.Now().In(ist)
	for _, f := range files(today) {
		data, err := spreadsheet.Build(f.sheets)
		if err != nil {
			panic(fmt.Errorf("%s: %w", f.name, err))
		}
		if err := os.WriteFile(filepath.Join(out, f.name), data, 0o644); err != nil {
			panic(err)
		}
		fmt.Println("wrote", f.name)
	}
}

type file struct {
	name   string
	sheets []spreadsheet.Sheet
}

func sheet(name string, heads []string, rows [][]string) spreadsheet.Sheet {
	sh := spreadsheet.Sheet{Name: name, Heads: heads, Text: true}
	for _, r := range rows {
		row := make([]any, len(r))
		for i, v := range r {
			row[i] = v
		}
		sh.Rows = append(sh.Rows, row)
	}
	return sh
}

// ---------- masters ----------

type property struct{ code, name, doc, ezee, cluster, stage, manager string }

var properties = []property{
	{"P01", "Test Stay Sector 29, Gurugram", "TA", "EZ-30401", "Gurugram North", "live", "arun.verma@test.saltstayz.com"},
	{"P02", "Test Stay Cyber Hub, Gurugram", "TB", "EZ-30402", "Gurugram North", "live", "arun.verma@test.saltstayz.com"},
	{"P03", "Test Stay Golf Course Road, Gurugram", "TC", "EZ-30403", "Gurugram South", "live", "kiran.rao@test.saltstayz.com"},
	{"P04", "Test Stay Sohna Road, Gurugram", "TD", "EZ-30404", "Gurugram South", "live", "kiran.rao@test.saltstayz.com"},
	{"P05", "Test Stay Connaught Place, New Delhi", "TE", "EZ-30405", "Delhi Central", "live", "sameer.khan@test.saltstayz.com"},
	{"P06", "Test Stay Karol Bagh, New Delhi", "TF", "EZ-30406", "Delhi Central", "live", "sameer.khan@test.saltstayz.com"},
	{"P07", "Test Stay Indiranagar, Bengaluru", "TG", "EZ-30407", "Bengaluru East", "live", "lakshmi.n@test.saltstayz.com"},
	{"P08", "Test Central Warehouse, Gurugram", "TW", "WH-30001", "Gurugram South", "warehouse", ""},
	{"P09", "Test Stay Whitefield, Bengaluru (pre-opening)", "TP", "PRE-30001", "Bengaluru East", "preopening", ""},
	{"P10", "Test Corporate Office, Gurugram", "TO", "CO-30001", "Gurugram North", "corporate", ""},
}

type subcat struct{ code, name, category string }

var subcats = []subcat{
	{"GA", "Guest Amenities", "Housekeeping"}, {"LN", "Linen", "Housekeeping"}, {"CC", "Cleaning Chemicals", "Housekeeping"},
	{"DG", "Dry Groceries", "Food & Beverage"}, {"DY", "Dairy", "Food & Beverage"}, {"BV", "Beverages", "Food & Beverage"},
	{"EL", "Electrical", "Engineering"}, {"PL", "Plumbing", "Engineering"}, {"ST", "Stationery", "Admin & Front Office"},
	{"SV", "Repairs & Maintenance Services", "Services"}, {"LS", "Laundry Services", "Services"},
}

type item struct {
	sc, brand, name, unit, gst, hsn, scope, tds string
	rate                                        float64
}

// items in the order they are uploaded: within a sub-category the app numbers them 0001, 0002 ...
var items = []item{
	{"GA", "Aroma Leaf", "Soap bar 20 g", "box of 500", "18", "34011190", "ALL", "", 1450},
	{"GA", "Aroma Leaf", "Shampoo 30 ml", "box of 250", "18", "33051090", "ALL", "", 1900},
	{"GA", "Aroma Leaf", "Conditioner 30 ml", "box of 250", "18", "33059090", "ALL", "", 1980},
	{"GA", "SmileCare", "Dental kit", "box of 100", "18", "96032100", "ALL", "", 1100},
	{"GA", "Other brand", "Shower cap", "box of 100", "18", "", "ALL", "", 420},
	{"GA", "SoftStep", "Room slippers", "pack of 10", "12", "64059000", "ALL", "", 350},
	{"LN", "SoftWeave", "Bed sheet double, white", "piece", "5", "63023100", "ALL", "", 780},
	{"LN", "SoftWeave", "Pillow cover", "piece", "5", "63023100", "ALL", "", 120},
	{"LN", "SoftWeave", "Bath towel 500 GSM", "piece", "5", "63026000", "ALL", "", 310},
	{"LN", "SoftWeave", "Hand towel", "piece", "5", "63026000", "ALL", "", 140},
	{"LN", "SoftWeave", "Duvet cover king", "piece", "12", "63023100", "ALL", "", 1650},
	{"CC", "CleanMax", "Floor cleaner", "5 L can", "18", "34022090", "ALL", "", 620},
	{"CC", "CleanMax", "Toilet cleaner", "5 L can", "18", "34022090", "ALL", "", 540},
	{"CC", "CleanMax", "Glass cleaner", "5 L can", "18", "34022090", "ALL", "", 580},
	{"CC", "CleanMax", "Dish wash liquid", "5 L can", "18", "34022090", "ALL", "", 560},
	{"CC", "CleanMax", "Hand wash", "5 L can", "18", "34013090", "ALL", "", 640},
	{"DG", "Daily Harvest", "Basmati rice", "25 kg bag", "5", "10063020", "ALL", "", 2350},
	{"DG", "Daily Harvest", "Wheat flour (atta)", "25 kg bag", "5", "11010000", "ALL", "", 950},
	{"DG", "Daily Harvest", "Toor dal", "5 kg bag", "5", "07136000", "ALL", "", 690},
	{"DG", "Daily Harvest", "Sugar", "5 kg bag", "5", "17019990", "ALL", "", 240},
	{"DG", "SunGold", "Refined oil", "5 L can", "5", "15121910", "ALL", "", 820},
	{"DG", "Assam Gold", "Tea leaf", "1 kg pack", "5", "09024020", "ALL", "", 480},
	{"DG", "Other brand", "Salt", "1 kg pack", "0", "25010020", "ALL", "", 25},
	{"DY", "Farm Fresh", "Milk", "1 L pouch", "0", "04011000", "ALL", "", 62},
	{"DY", "Farm Fresh", "Paneer", "1 kg pack", "5", "04069000", "ALL", "", 360},
	{"DY", "Farm Fresh", "Curd", "1 kg pack", "5", "04039010", "ALL", "", 90},
	{"DY", "Farm Fresh", "Butter", "500 g pack", "12", "04051000", "ALL", "", 280},
	{"BV", "AquaPure", "Mineral water 500 ml", "case of 24", "18", "22011010", "ALL", "", 190},
	{"BV", "Fizz", "Soft drink 300 ml", "case of 24", "40", "22021010", "ALL", "", 620},
	{"BV", "BrewHouse", "Coffee beans", "1 kg pack", "18", "09012110", "cluster:Delhi Central;cluster:Bengaluru East", "", 1250},
	{"BV", "Orchard", "Juice 1 L", "case of 12", "12", "20098990", "ALL", "", 1150},
	{"EL", "BrightVolt", "LED bulb 9 W", "piece", "18", "85395000", "ALL", "", 85},
	{"EL", "BrightVolt", "LED tube 20 W", "piece", "18", "85395000", "ALL", "", 210},
	{"EL", "BrightVolt", "MCB 16 A", "piece", "18", "85362010", "ALL", "", 240},
	{"EL", "BrightVolt", "Extension board", "piece", "18", "85366990", "ALL", "", 450},
	{"EL", "PowerCell", "AA battery", "pack of 10", "18", "85061000", "ALL", "", 180},
	{"PL", "FlowLine", "Bib tap 15 mm", "piece", "18", "84818020", "ALL", "", 420},
	{"PL", "FlowLine", "Health faucet", "piece", "18", "84818020", "ALL", "", 650},
	{"PL", "FlowLine", "PVC pipe 1 inch", "piece", "18", "39172390", "ALL", "", 0}, // no rate: estimate allowed
	{"ST", "PaperPro", "A4 paper 75 GSM", "ream", "12", "48025690", "ALL", "", 265},
	{"ST", "PaperPro", "Key card sleeve", "box of 500", "18", "48239090", "P01;P02;P03", "", 950},
	{"ST", "PaperPro", "Guest register", "piece", "12", "48201010", "ALL", "", 180},
	{"ST", "PaperPro", "Ball pen", "pack of 10", "18", "96081019", "ALL", "", 60},
	{"SV", "Service", "AC servicing visit", "service visit", "18", "998719", "ALL", "194C", 1500},
	{"SV", "Service", "Plumbing repair visit", "service visit", "18", "998719", "ALL", "194C-IND", 600},
	{"SV", "Service", "Deep cleaning service", "service visit", "18", "998533", "ALL", "194C", 4500},
	{"LS", "Service", "Laundry per kg", "kg", "18", "999712", "ALL", "194C", 45},
	{"LS", "Service", "Uniform dry cleaning", "piece", "18", "999712", "ALL", "194C", 80},
}

var packSizes = []string{"piece", "box of 100", "box of 250", "box of 500", "pack of 10", "5 L can", "25 kg bag", "5 kg bag", "1 kg pack",
	"1 L pouch", "500 g pack", "case of 24", "case of 12", "ream", "service visit", "kg"}

// skuCodes gives each item its code the way the app assigns them on a fresh database.
func skuCodes() []string {
	n := map[string]int{}
	out := make([]string, len(items))
	for i, it := range items {
		n[it.sc]++
		out[i] = fmt.Sprintf("%s-%04d", it.sc, n[it.sc])
	}
	return out
}

type vendor struct {
	code, typ, name, address, city, pin, contact, mobile, state, pan, msme, terms, supplies string
	ifsc, account                                                                           string
}

var vendors = []vendor{
	{"V001", "goods", "Aroma Hospitality Supplies Pvt Ltd", "Plot 14, Udyog Vihar Phase 4", "Gurugram", "122015", "Rakesh Gupta", "9810030001", "06", "AARCA4101K", "yes", "Net 30", "Guest Amenities", "HDFC0001234", "501000300011"},
	{"V002", "goods", "SoftWeave Linen House", "22 Naraina Industrial Area", "New Delhi", "110028", "Sunil Arora", "9810030002", "07", "AAFFS4102L", "no", "Net 45", "Linen", "ICIC0000456", "002305001234"},
	{"V003", "goods", "CleanMax Chemicals Pvt Ltd", "Sector 37, Pace City", "Gurugram", "122001", "Neha Chawla", "9810030003", "06", "AABCC4103M", "no", "Net 30", "Cleaning Chemicals", "SBIN0001234", "300112233445"},
	{"V004", "goods", "Daily Harvest Traders", "Naya Bazar", "New Delhi", "110006", "Manoj Jain", "9810030004", "07", "AAGFD4104N", "yes", "Net 15", "Dry Groceries", "PUNB0012300", "1234000100056"},
	{"V005", "goods", "Farm Fresh Dairy", "Village Bhondsi", "Gurugram", "122102", "Harish Yadav", "9810030005", "06", "AALFF4105P", "yes", "Net 15", "Dairy", "HDFC0004567", "501000300055"},
	{"V006", "goods", "Fizz Beverages Distributors", "Okhla Phase 2", "New Delhi", "110020", "Imran Sheikh", "9810030006", "07", "AABCF4106Q", "no", "Net 30", "Beverages", "UTIB0000123", "917020000123456"},
	{"V007", "goods", "BrightVolt Electricals", "Sadar Bazar, Gurugram", "Gurugram", "122001", "Karan Saini", "9810030007", "06", "AAJFB4107R", "no", "Advance", "Electrical", "KKBK0004321", "8711223344"},
	{"V008", "goods", "FlowLine Hardware Store", "Chawri Bazar", "New Delhi", "110006", "Ramesh Kumar", "9810030008", "", "AKPPK4108S", "no", "Net 30", "Plumbing;Electrical", "SBIN0005678", "300998877665"},
	{"V009", "goods", "PaperPro Stationers", "Nehru Place", "New Delhi", "110019", "Divya Bhatia", "9810030009", "07", "AAKFP4109T", "yes", "15th of next month", "Stationery", "HDFC0007788", "501000300099"},
	{"V010", "goods", "CoolBreeze Facility Services", "Sector 44", "Gurugram", "122003", "Tarun Negi", "9810030010", "06", "AABCC4110U", "no", "Net 30", "Repairs & Maintenance Services", "ICIC0001122", "002305009876"},
	{"V011", "goods", "Bengaluru Hotel Supplies", "Peenya Industrial Area", "Bengaluru", "560058", "Prakash Rao", "9810030011", "29", "AABCB4111V", "no", "Net 30", "Guest Amenities;Cleaning Chemicals;Dry Groceries;Dairy", "CNRB0001234", "12345678901"},
	{"V012", "goods", "QuickWash Laundry Services", "Kirti Nagar", "New Delhi", "110015", "Farah Siddiqui", "9810030012", "07", "AAHFQ4112W", "no", "Net 15", "Laundry Services", "YESB0000123", "012345678901"},
	{"V013", "utility", "Haryana Power Distribution", "Vidyut Sadan, Sector 6", "Gurugram", "122001", "Billing desk", "9810030013", "06", "AABCH4113X", "no", "Net 15", "Electricity", "SBIN0009999", "300000013013"},
	{"V014", "utility", "City Water Board", "Jal Bhawan", "New Delhi", "110002", "Accounts desk", "9810030014", "", "AAAGC4114Y", "no", "Net 15", "Water", "SBIN0008888", "300000014014"},
	{"V015", "utility", "SkyNet Broadband", "MG Road", "Gurugram", "122002", "Support desk", "9810030015", "06", "AABCS4115Z", "no", "Net 15", "Internet / broadband", "HDFC0001111", "501000300150"},
	{"V016", "amc", "LiftCare Elevators", "Mayapuri Industrial Area", "New Delhi", "110064", "Service desk", "9810030016", "07", "AABCL4116A", "no", "Net 30", "Lift AMC", "ICIC0002222", "002305000016"},
	{"V017", "amc", "PestShield Solutions", "Sector 18", "Gurugram", "122015", "Ritu Sharma", "9810030017", "06", "AAJFP4117B", "yes", "Net 30", "Pest control", "KKBK0002222", "8711000017"},
}

// gstin builds a well-formed GSTIN with a correct check character: state + PAN + entity number + Z + check.
func gstin(state, pan string) string {
	if state == "" {
		return ""
	}
	const chars = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	body := state + pan + "1Z"
	sum := 0
	for i, c := range body {
		v := strings.IndexRune(chars, c) * (1 + i%2)
		sum += v/36 + v%36
	}
	return body + string(chars[(36-sum%36)%36])
}

func udyam(state string, i int) string {
	st := map[string]string{"06": "HR", "07": "DL", "29": "KA", "": "DL"}[state]
	return fmt.Sprintf("UDYAM-%s-%02d-%07d", st, 1+i%30, 1234500+i)
}

type user struct{ name, email, mobile, dept, role, access, categories string }

var users = []user{
	{"Riya Sharma", "riya.sharma@test.saltstayz.com", "", "Housekeeping", "staff", "P01", "Housekeeping"},
	{"Mohan Lal", "mohan.lal@test.saltstayz.com", "", "Operations", "staff", "P02;P03", ""},
	{"Pooja Batra", "pooja.batra@test.saltstayz.com", "", "Operations", "staff", "P05", ""},
	{"Pooja Batra", "pooja.batra@test.saltstayz.com", "", "Operations", "store", "P05", ""},
	{"Arun Verma", "arun.verma@test.saltstayz.com", "", "Operations", "l1", "P01;P02", ""},
	{"Kiran Rao", "kiran.rao@test.saltstayz.com", "", "Operations", "l1", "P03;P04", ""},
	{"Sameer Khan", "sameer.khan@test.saltstayz.com", "", "Operations", "l1", "P05;P06", ""},
	{"Lakshmi N", "lakshmi.n@test.saltstayz.com", "", "Operations", "l1", "P07", ""},
	{"Neeraj Gupta", "neeraj.gupta@test.saltstayz.com", "", "Operations", "l2", "cluster:Gurugram North;Gurugram South", ""},
	{"Farah Ali", "farah.ali@test.saltstayz.com", "", "Operations", "l2", "cluster:Delhi Central", ""},
	{"Deepa Iyer", "deepa.iyer@test.saltstayz.com", "", "Operations", "l2", "cluster:Bengaluru East", ""},
	{"Amit Bansal", "amit.bansal@test.saltstayz.com", "", "Finance", "cc", "all", ""},
	{"Priya Menon", "priya.menon@test.saltstayz.com", "", "Purchase", "purchase", "all", ""},
	{"Rahul Yadav", "rahul.yadav@test.saltstayz.com", "", "Stores", "store", "P01;P02;P03;P04", ""},
	{"Suresh Kumar", "", "9811040001", "Stores", "store", "P06", ""},
	{"Manju Reddy", "manju.reddy@test.saltstayz.com", "", "Stores", "store", "P07;P09", ""},
	{"Kavya Joshi", "kavya.joshi@test.saltstayz.com", "", "Finance", "ap", "all", ""},
	{"Rohan Mehra", "rohan.mehra@test.saltstayz.com", "", "Finance", "finance", "all", ""},
	{"Vikram Singh", "vikram.singh@test.saltstayz.com", "", "Warehouse", "wh", "all", ""},
	{"Ajay Thakur", "ajay.thakur@test.saltstayz.com", "", "Warehouse", "whs", "P08", ""},
	{"Divya Menon", "divya.menon@test.saltstayz.com", "", "Warehouse", "whl1", "P08", ""},
	{"Harish Bhat", "harish.bhat@test.saltstayz.com", "", "Warehouse", "whl1", "P08", ""},
	{"Tarun Khanna", "tarun.khanna@test.saltstayz.com", "", "Warehouse", "whl2", "P08", ""},
	{"Karan Kapoor", "karan.kapoor@test.saltstayz.com", "", "Kitchen", "chef", "P01", "Food & Beverage"},
	{"Nisha Pillai", "nisha.pillai@test.saltstayz.com", "", "Kitchen", "hchef", "cluster:Gurugram North;Gurugram South", ""},
	{"Anjali Rao", "anjali.rao@test.saltstayz.com", "", "Projects", "preop", "all", ""},
	{"Meera Kapoor", "meera.kapoor@test.saltstayz.com", "", "Admin", "creq", "P10", ""},
	{"Rakesh Jain", "rakesh.jain@test.saltstayz.com", "", "Admin", "dh", "P10", ""},
	{"Vivek Nair", "vivek.nair@test.saltstayz.com", "", "IT", "dh", "P10", ""},
	{"Anil Sethi", "anil.sethi@test.saltstayz.com", "", "Finance", "capp", "P10", ""},
	{"Sana Qureshi", "sana.qureshi@test.saltstayz.com", "", "Operations", "l2", "cluster:Delhi Central", ""},
	{"Gaurav Tiwari", "gaurav.tiwari@test.saltstayz.com", "", "Purchase", "purchase", "cluster:Bengaluru East", ""},
}

// whl1Subs are the sub-categories each Warehouse Approver L1 approves (blank = every sub-category).
var whl1Subs = map[string]string{"divya.menon@test.saltstayz.com": "Linen"}

const password = "Test@1234"

// mapping rows: vendor, sub-category (or a SKU code for a SKU mapping), properties, estimate allowed. A cluster entry
// covers the live hotels of that cluster only, so the warehouse (P08) and the pre-opening property (P09) are named.
type mapping struct{ vendor, subcat, sku, props, est string }

// Stationery issues its POs automatically at the final approval; the extension board is not daily consumption.
const (
	autoPOSubcat = "ST"
	notDaily     = "Extension board"
)

var mappings = []mapping{
	{"V001", "Guest Amenities", "", "cluster:Gurugram North;cluster:Gurugram South;cluster:Delhi Central;P08", "no"},
	{"V011", "Guest Amenities", "", "cluster:Bengaluru East;P09", "no"},
	{"V002", "Linen", "", "ALL", "no"},
	{"V003", "Cleaning Chemicals", "", "cluster:Gurugram North;cluster:Gurugram South;cluster:Delhi Central;P08", "no"},
	{"V011", "Cleaning Chemicals", "", "cluster:Bengaluru East;P09", "no"},
	{"V004", "Dry Groceries", "", "cluster:Gurugram North;cluster:Gurugram South;cluster:Delhi Central;P08", "no"},
	{"V011", "Dry Groceries", "", "cluster:Bengaluru East;P09", "no"},
	{"V005", "Dairy", "", "cluster:Gurugram North;cluster:Gurugram South;cluster:Delhi Central;P08", "no"},
	{"V011", "Dairy", "", "cluster:Bengaluru East;P09", "no"},
	{"V006", "Beverages", "", "cluster:Gurugram North;cluster:Gurugram South;cluster:Delhi Central", "no"}, // Bengaluru left unmapped on purpose
	{"V007", "Electrical", "", "ALL", "no"},
	// The extension board is not daily consumption: two vendors hold it by SKU everywhere and the requester picks one.
	{"V007", "", "EL-0004", "ALL", "no"},
	{"V008", "", "EL-0004", "ALL", "no"},
	{"V008", "Plumbing", "", "ALL", "yes"},
	{"V009", "Stationery", "", "ALL", "no"},
	{"V010", "Repairs & Maintenance Services", "", "ALL", "no"},
	{"V012", "Laundry Services", "", "ALL", "no"},
	// The Central Warehouse supplies these to the Gurugram hotels; a SKU mapping wins over the sub-category one.
	{"WH", "", "LN-0001", "P01;P02;P03;P04", "no"},
	{"WH", "", "LN-0002", "P01;P02;P03;P04", "no"},
	{"WH", "", "LN-0003", "P01;P02;P03;P04", "no"},
	{"WH", "", "GA-0001", "P01;P02;P03;P04", "no"},
	{"WH", "", "EL-0001", "P01;P02;P03;P04", "no"},
	{"WH", "", "ST-0001", "P01;P02;P03;P04", "no"},
}

var warehouseStock = []struct {
	sku           string
	open, reorder int
}{{"LN-0001", 420, 150}, {"LN-0002", 520, 200}, {"LN-0003", 380, 150}, {"GA-0001", 60, 20}, {"EL-0001", 900, 300}, {"ST-0001", 12, 60}}

// ---------- files ----------

func files(today time.Time) []file {
	codes := skuCodes()
	monthStart := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, today.Location())
	fyEnd := time.Date(today.Year(), 3, 31, 0, 0, 0, 0, today.Location())
	if today.Month() >= time.April {
		fyEnd = fyEnd.AddDate(1, 0, 0)
	}
	ymd := func(t time.Time) string { return t.Format("2006-01-02") }
	cluster := func(code string) string {
		for _, p := range properties {
			if p.code == code {
				return p.cluster
			}
		}
		return ""
	}

	// 01 cities and clusters
	cities := [][]string{{"Gurugram", "HR"}, {"New Delhi", "DL"}, {"Bengaluru", "KA"}}
	clusters := [][]string{{"Gurugram North", "Gurugram", ""}, {"Gurugram South", "Gurugram", ""}, {"Delhi Central", "New Delhi", ""}, {"Bengaluru East", "Bengaluru", ""}}
	f01 := file{"01-cities-clusters.xlsx", []spreadsheet.Sheet{sheet("Cities", []string{"city", "state_code"}, cities), sheet("Clusters", []string{"cluster", "city", "cluster_manager_login"}, clusters)}}

	// 02 categories and sub-categories
	cats := [][]string{{"Housekeeping", "no"}, {"Food & Beverage", "yes"}, {"Engineering", "no"}, {"Admin & Front Office", "no"}, {"Services", "no"}}
	scRows := [][]string{}
	for _, s := range subcats {
		auto := "no"
		if s.code == autoPOSubcat {
			auto = "yes"
		}
		scRows = append(scRows, []string{s.code, s.name, s.category, auto})
	}
	f02 := file{"02-categories-subcategories.xlsx", []spreadsheet.Sheet{sheet("Categories", []string{"category", "fnb"}, cats), sheet("Sub-categories", []string{"code", "sub_category", "category", "auto_po"}, scRows)}}

	// 03 pack sizes
	ps := [][]string{}
	for _, p := range packSizes {
		ps = append(ps, []string{p})
	}
	f03 := file{"03-pack-sizes.xlsx", []spreadsheet.Sheet{sheet("Pack sizes", []string{"pack_size"}, ps)}}

	// 04 properties (managers come in file 07, once the users exist)
	pHeads := []string{"property_code", "property_name", "doc_code", "ezee_code", "cluster", "stage", "property_manager_login", "plan_cost_pct", "ird_cost_pct", "banquet_cost_pct"}
	pRows := [][]string{}
	for i, p := range properties {
		pRows = append(pRows, []string{"", p.name, p.doc, p.ezee, p.cluster, p.stage, "", fmt.Sprint(33 + i%4), fmt.Sprint(30 + i%3), fmt.Sprint(36 + i%5)})
	}
	f04 := file{"04-properties.xlsx", []spreadsheet.Sheet{sheet("Properties", pHeads, pRows)}}

	// 05 users
	uRows := [][]string{}
	empID := map[string]string{} // one employee ID per person, on each of their role rows
	for _, u := range users {
		login := u.email + u.mobile
		if empID[login] == "" {
			empID[login] = fmt.Sprintf("SS%04d", 1001+len(empID))
		}
		uRows = append(uRows, []string{u.name, empID[login], u.email, u.mobile, u.dept, u.role, u.access, u.categories, whl1Subs[u.email], password})
	}
	f05 := file{"05-users.xlsx", []spreadsheet.Sheet{sheet("Users", []string{"name", "employee_id", "email", "mobile", "department", "role", "access", "categories", "sub_categories", "password"}, uRows)}}

	// 06 cluster managers, 07 property managers (updates, now that the users exist)
	mgr := map[string]string{"Gurugram North": "neeraj.gupta@test.saltstayz.com", "Gurugram South": "neeraj.gupta@test.saltstayz.com", "Delhi Central": "farah.ali@test.saltstayz.com", "Bengaluru East": "deepa.iyer@test.saltstayz.com"}
	cm := [][]string{}
	for _, c := range clusters {
		cm = append(cm, []string{c[0], c[1], mgr[c[0]]})
	}
	f06 := file{"06-cluster-managers.xlsx", []spreadsheet.Sheet{sheet("Clusters", []string{"cluster", "city", "cluster_manager_login"}, cm)}}
	pm := [][]string{}
	for i, p := range properties {
		pm = append(pm, []string{p.code, p.name, p.doc, p.ezee, p.cluster, p.stage, p.manager, fmt.Sprint(33 + i%4), fmt.Sprint(30 + i%3), fmt.Sprint(36 + i%5)})
	}
	f07 := file{"07-property-managers.xlsx", []spreadsheet.Sheet{sheet("Properties", pHeads, pm)}}

	// 08 vendors, items and their rates, in one workbook (applied in that order)
	vHeads := []string{"vendor_code", "vendor_type", "legal_name", "address", "city", "pin", "contact_person", "mobile", "email", "gstin", "pan", "msme", "udyam", "bank_holder", "bank_account", "ifsc", "payment_terms", "supplies"}
	vRows := [][]string{}
	for i, v := range vendors {
		u := ""
		if v.msme == "yes" {
			u = udyam(v.state, i)
		}
		email := "accounts@" + strings.ToLower(strings.NewReplacer(" ", "", ".", "", ",", "", "&", "").Replace(strings.Fields(v.name)[0]+strings.Fields(v.name)[1])) + ".example.in"
		vRows = append(vRows, []string{"", v.typ, v.name, v.address, v.city, v.pin, v.contact, v.mobile, email, gstin(v.state, v.pan), v.pan, v.msme, u, v.name, v.account, v.ifsc, v.terms, v.supplies})
	}
	iHeads := []string{"sku_code", "category", "sub_category", "brand", "item_name", "unit", "gst_percent", "hsn", "properties", "tds_section", "daily_consumption", "stocked_at_warehouse"}
	stocked := map[string]bool{} // the items the Central Warehouse holds: every property's request for them goes there
	for _, w := range warehouseStock {
		stocked[w.sku] = true
	}
	iRows := [][]string{}
	scName := map[string]subcat{}
	for _, s := range subcats {
		scName[s.code] = s
	}
	for i, it := range items {
		s := scName[it.sc]
		wh := "no"
		if stocked[codes[i]] {
			wh = "yes"
		}
		daily := "yes"
		if it.name == notDaily {
			daily = "no"
		}
		iRows = append(iRows, []string{"", s.category, s.name, it.brand, it.name, it.unit, it.gst, it.hsn, it.scope, it.tds, daily, wh})
	}
	// Each item's rate from the vendor its sub-category maps to; Bengaluru Hotel Supplies quotes about 6% more.
	supplier := map[string][]string{"GA": {"V001", "V011"}, "LN": {"V002"}, "CC": {"V003", "V011"}, "DG": {"V004", "V011"}, "DY": {"V005", "V011"}, "BV": {"V006"},
		"EL": {"V007", "V008"}, "PL": {"V008"}, "ST": {"V009"}, "SV": {"V010"}, "LS": {"V012"}}
	vName := map[string]string{}
	for _, v := range vendors {
		vName[v.code] = v.name
	}
	rHeads := []string{"vendor_code", "vendor_name", "sku_code", "item_name", "unit", "rate_ex_gst", "gst_percent", "valid_from", "valid_to"}
	rRows := [][]string{}
	for i, it := range items {
		if it.rate == 0 {
			continue
		}
		for j, v := range supplier[it.sc] {
			rate := it.rate
			if j > 0 {
				rate = float64(int(rate*1.06 + 0.5))
			}
			rRows = append(rRows, []string{v, vName[v], codes[i], it.brand + " " + it.name, it.unit, fmt.Sprint(rate), it.gst, ymd(monthStart), ymd(fyEnd)})
		}
	}
	f08 := file{"08-vendors-items-rates.xlsx", []spreadsheet.Sheet{sheet("Vendors", vHeads, vRows), sheet("Items", iHeads, iRows), sheet("Vendor rates", rHeads, rRows)}}

	// 09 vendor mapping
	mRows := [][]string{}
	for _, m := range mappings {
		cat := ""
		if m.subcat != "" {
			for _, s := range subcats {
				if s.name == m.subcat {
					cat = s.category
				}
			}
		}
		mRows = append(mRows, []string{"", m.vendor, m.sku, cat, m.subcat, m.props, m.est})
	}
	f09 := file{"09-vendor-mapping.xlsx", []spreadsheet.Sheet{sheet("Vendor mapping", []string{"mapping_id", "vendor_code", "sku_code", "category", "sub_category", "properties", "estimate_allowed"}, mRows)}}

	// 10 warehouse rates: the Central Warehouse issues at about 90% of the vendor rate
	whRows := [][]string{}
	for _, w := range warehouseStock {
		for i, c := range codes {
			if c == w.sku {
				whRows = append(whRows, []string{"WH", "Central Warehouse (internal)", c, items[i].brand + " " + items[i].name, items[i].unit, fmt.Sprint(float64(int(items[i].rate*0.9 + 0.5))), items[i].gst, ymd(monthStart), ymd(fyEnd)})
			}
		}
	}
	f10 := file{"10-warehouse-rates.xlsx", []spreadsheet.Sheet{sheet("Vendor rates", rHeads, whRows)}}

	// 11 monthly budgets (month blank = the current month); P02 Guest Amenities is set low on purpose
	bRows := [][]string{}
	base := map[string]int{"GA": 60000, "LN": 50000, "CC": 30000, "DG": 150000, "DY": 80000, "BV": 40000, "EL": 30000, "PL": 20000, "ST": 15000, "SV": 40000, "LS": 25000}
	for pi, p := range properties[:7] {
		for _, s := range subcats {
			amt := base[s.code] * (90 + (pi*7)%25) / 100
			if p.code == "P02" && s.code == "GA" {
				amt = 3000
			}
			bRows = append(bRows, []string{p.code, p.name, "", s.name, fmt.Sprint(amt)})
		}
	}
	f11 := file{"11-budgets.xlsx", []spreadsheet.Sheet{sheet("Monthly budgets", []string{"property_code", "property_name", "month", "sub_category", "monthly_budget"}, bRows)}}

	// 12 F&B sales: each live property, every day of this month up to today
	sRows := [][]string{}
	for pi, p := range properties[:7] {
		for d := monthStart; !d.After(today); d = d.AddDate(0, 0, 1) {
			k := d.Day() + pi*3
			plan := 18000 + (k*1373)%9000
			ird := 4000 + (k*577)%3000
			banquet := 0
			if k%5 == 0 {
				banquet = 25000 + (k*911)%20000
			}
			sRows = append(sRows, []string{p.code, p.name, ymd(d), fmt.Sprint(plan), fmt.Sprint(ird), fmt.Sprint(banquet)})
		}
	}
	f12 := file{"12-fnb-sales.xlsx", []spreadsheet.Sheet{sheet("F&B sales", []string{"property_code", "property_name", "date", "plan_sale", "ird_sale", "banquet_sale"}, sRows)}}

	// 13 warehouse opening stock (ST-0001 starts below its reorder level on purpose)
	wRows := [][]string{}
	for _, w := range warehouseStock {
		for i, c := range codes {
			if c == w.sku {
				wRows = append(wRows, []string{c, items[i].name, fmt.Sprint(w.open), fmt.Sprint(w.reorder)})
			}
		}
	}
	f13 := file{"13-warehouse-opening-stock.xlsx", []spreadsheet.Sheet{sheet("Warehouse opening stock", []string{"sku_code", "item_name", "opening_qty", "reorder_level"}, wRows)}}

	// 14 an item list for a purchase request at P01 (Upload item list on the New request page)
	rl := [][]string{}
	for i, c := range codes {
		it := items[i]
		if !(it.scope == "ALL" || strings.Contains(it.scope, "P01") || strings.Contains(it.scope, "cluster:"+cluster("P01"))) {
			continue
		}
		if len(rl) >= 15 {
			break
		}
		if i%3 != 0 && it.rate != 0 {
			continue
		}
		rate := ""
		if it.rate == 0 {
			rate = "380"
		}
		rl = append(rl, []string{c, it.name, fmt.Sprint(2 + i%5), rate})
	}
	f14 := file{"14-request-item-list-P01.xlsx", []spreadsheet.Sheet{sheet("Request lines", []string{"sku_code", "item_name", "quantity", "rate"}, rl)}}

	return []file{f01, f02, f03, f04, f05, f06, f07, f08, f09, f10, f11, f12, f13, f14, testPlan(codes)}
}

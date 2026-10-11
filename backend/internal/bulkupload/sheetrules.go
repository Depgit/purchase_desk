package bulkupload

// Rules inside the edit workbook, so Excel itself keeps the sheets consistent while someone edits: dropdowns that
// read from the other sheets (a mapping's vendor must be on the Vendors sheet; a sub-category list shows only the
// chosen category's sub-categories), a stop message naming the sheet to change first, and locked cells for keys and
// for fields that only change on screen (vendor bank details, GSTIN and PAN). The server still checks every row.

import (
	"context"
	"fmt"
	"strings"

	"purchasedesk/internal/database"
	"purchasedesk/internal/indiantax"
	"purchasedesk/internal/money"
	"purchasedesk/internal/releasephase"
	"purchasedesk/internal/spreadsheet"
)

type sheetRules struct {
	locked   []string
	rules    []spreadsheet.Validation
	formulas []cellFormula // cells that fill themselves in from the row
	spare    int           // empty rows added below the data with the formulas ready, for new entries
	shades   []spreadsheet.Shade
}

// cellFormula fills a column from other cells of its row, on every row including the spare ones. {r} is the row
// number and {col} a column letter of this sheet; {orig} is the cell's value as downloaded and {orig:col} that
// column's, as Excel text, so a row shows its saved values until the cell it depends on changes.
type cellFormula struct {
	col, tmpl string
}

// Columns of the hidden Lists sheet (see listsSheet) that the mapping formulas look up.
const (
	vendorName    = `IF({vendor_code}{r}="","",IFERROR(VLOOKUP({vendor_code}{r},Lists!$I:$J,2,FALSE),"Not an active goods vendor"))`
	itemCategory  = `IF({item}{r}="",{orig},IFERROR(VLOOKUP({item}{r},Lists!$K:$N,2,FALSE),""))`
	itemSubcat    = `IF({item}{r}="",{orig},IFERROR(VLOOKUP({item}{r},Lists!$K:$N,3,FALSE),""))`
	itemAvailable = `IF({item}{r}="","",IFERROR(VLOOKUP({item}{r},Lists!$K:$N,4,FALSE),"Unknown item: pick it from the list"))`
)

// availableHere is true when the row's item_available_at covers a property: ALL, its code, or its cluster.
func availableHere(code, cluster string) string {
	q := func(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }
	in := `";"&{item_available_at}{r}&";"`
	return `OR({item_available_at}{r}="ALL",ISNUMBER(SEARCH(` + q(";"+code+";") + `,` + in + `)),ISNUMBER(SEARCH(` + q(";cluster:"+cluster+";") + `,` + in + `)))`
}

// withPropertyTicks adds the vendor mapping sheet's tick column per property (headed "P01 · name"): a yes dropdown;
// it shows the saved mapping until the row's item changes, then ticks itself for every property the new item is
// available at (as in Items); greyed where the item is not available.
func withPropertyTicks(rl sheetRules, props []mappingProperty) sheetRules {
	for _, p := range props {
		h, here := p.head, availableHere(p.code, p.cluster)
		rl.rules = append(rl.rules, spreadsheet.Validation{Column: h, List: []string{"yes"}, PromptTitle: "Supplies this property?",
			Prompt: "yes = this vendor supplies the item here. Blank = not here. Grey = the item is not available at this property (see Items).", ErrorTitle: "yes or blank", Error: "Pick yes, or clear the cell."})
		rl.formulas = append(rl.formulas, cellFormula{col: h, tmpl: `IF({item}{r}={orig:item},{orig},IF(` + here + `,"yes",""))`})
		rl.shades = append(rl.shades, spreadsheet.Shade{Column: h, Formula: `AND({item}{r}<>"",NOT(` + here + `))`})
	}
	return rl
}

// The hidden Lists sheet's columns: category → sub-category pairs, the categories, category → SKU pairs, and
// reporting tag → option pairs. A dependent dropdown takes the run of pairs whose first column matches the row.
const (
	subcatsOfCategory = `OFFSET(Lists!$B$1,MATCH({category}2,Lists!$A:$A,0)-1,0,COUNTIF(Lists!$A:$A,{category}2),1)`
	skusOfCategory    = `OFFSET(Lists!$E$1,MATCH({category}2,Lists!$D:$D,0)-1,0,COUNTIF(Lists!$D:$D,{category}2),1)`
	optionsOfTag      = `OFFSET(Lists!$G$1,MATCH({reporting_tag}2,Lists!$F:$F,0)-1,0,COUNTIF(Lists!$F:$F,{reporting_tag}2),1)`
)

func gstRates() []string { return []string{"0", "5", "12", "18", "28", "40"} }

func tdsCodes() []string {
	out := []string{}
	for _, t := range indiantax.TDSSections {
		out = append(out, t.Code)
	}
	return out
}

var (
	ruleCategory = spreadsheet.Validation{Column: "category", Source: "{Lists.categories}", ErrorTitle: "Unknown category",
		Error: "Pick a category from the list. The Ref - Sub-categories sheet shows every category and its sub-categories."}
	ruleSubcat = spreadsheet.Validation{Column: "sub_category", Source: subcatsOfCategory, PromptTitle: "Sub-category",
		Prompt: "Pick the category first: this list then shows only that category's sub-categories.", ErrorTitle: "Not in this category",
		Error: "This sub-category is not in the category on this row. Change the category first, then pick from the list."}
	rulePropsCell = spreadsheet.Validation{Column: "properties", PromptTitle: "Properties",
		Prompt: "ALL, or property codes and cluster:Name separated by ; (see Ref - Properties and Ref - Clusters)."}
)

// rulesFor gives the rules of an editable sheet, by upload kind.
func rulesFor(kind string) sheetRules {
	switch kind {
	case "mappings":
		return sheetRules{locked: []string{"mapping_id", "vendor_name", "item_available_at", "rate_status"}, spare: 300, rules: []spreadsheet.Validation{
			{Column: "vendor_code", Source: "{Lists.vendor_code}", PromptTitle: "Vendor", Prompt: "Pick the vendor's code; the name fills in next to it.",
				ErrorTitle: "Unknown vendor", Error: "Pick an active goods vendor's code from the list. Add a new vendor on the Vendors screen first."},
			{Column: "item", Source: "{Lists.item}", PromptTitle: "Item", Prompt: "Pick the item: its category and sub-category fill in, and item_available_at shows where it can be mapped. Leave blank to map a whole sub-category.",
				ErrorTitle: "Unknown item", Error: "Pick the item from the list. Add a new item on the Items screen first."},
			ruleCategory, ruleSubcat,
			{Column: "estimate_allowed", List: []string{"yes", "no"}, PromptTitle: "Estimate allowed", Prompt: "Sub-category mappings only: yes lets an item without a rate be ordered at an estimated rate.", ErrorTitle: "yes or no", Error: "Write yes or no."},
		}, formulas: []cellFormula{
			{col: "vendor_name", tmpl: vendorName},
			{col: "category", tmpl: itemCategory},
			{col: "sub_category", tmpl: itemSubcat},
			{col: "item_available_at", tmpl: itemAvailable},
		}}
	case "vendors":
		locked := spreadsheet.Validation{PromptTitle: "Locked for existing vendors", Prompt: "Bank details, GSTIN and PAN change only on the vendor screen, where Finance re-verifies them. Fill them only for a new vendor."}
		out := sheetRules{locked: []string{"vendor_code", "bank_holder", "bank_account", "ifsc", "gstin", "pan"}, rules: []spreadsheet.Validation{
			{Column: "vendor_type", List: []string{"goods", "utility", "amc", "other"}, ErrorTitle: "Vendor type", Error: "Pick goods, utility, amc or other."},
			{Column: "msme", List: []string{"yes", "no"}, ErrorTitle: "yes or no", Error: "Write yes or no."},
			{Column: "payment_terms", List: money.PaymentTerms, ErrorTitle: "Payment terms", Error: "Pick payment terms from the list."},
			{Column: "supplies", PromptTitle: "Supplies", Prompt: "Goods vendors: sub-category names from Ref - Sub-categories. Utility, AMC and other vendors: bill types from Ref - Bill types. Separate with ;"},
		}}
		for _, c := range []string{"bank_holder", "bank_account", "ifsc", "gstin", "pan"} {
			v := locked
			v.Column = c
			out.rules = append(out.rules, v)
		}
		return out
	case "items":
		return sheetRules{locked: []string{"sku_code", "category", "sub_category"}, rules: []spreadsheet.Validation{
			ruleCategory, ruleSubcat,
			{Column: "brand", Source: "{Ref - Brands.brand}", AllowOthers: true, ErrorTitle: "New brand", Error: "This brand is not in the list yet. It will be created when you upload. Choose Yes to keep it."},
			{Column: "unit", Source: "{Ref - Pack sizes.unit}", ErrorTitle: "Unknown pack size", Error: "Pick a pack size from the list. Add a new one under Settings, Item Setup first."},
			{Column: "gst_percent", List: gstRates(), ErrorTitle: "GST %", Error: "GST % is 0, 5, 12, 18, 28 or 40."},
			{Column: "tds_section", List: tdsCodes(), ErrorTitle: "TDS section", Error: "Pick a TDS section from the list (service items only), or leave it blank."},
			rulePropsCell,
		}}
	case "rates":
		return sheetRules{rules: []spreadsheet.Validation{
			{Column: "vendor_code", Source: "{Vendors.vendor_code}", ErrorTitle: "Vendor not on the Vendors sheet", Error: "Pick a vendor code from the Vendors sheet. A new vendor must be added there and uploaded first."},
			{Column: "sku_code", Source: "{Items.sku_code}", ErrorTitle: "Item not on the Items sheet", Error: "Pick an SKU code from the Items sheet. A new item must be added there and uploaded first."},
			{Column: "gst_percent", List: gstRates(), ErrorTitle: "GST %", Error: "GST % is 0, 5, 12, 18, 28 or 40."},
			{Column: "valid_from", PromptTitle: "Date", Prompt: "Write the date as YYYY-MM-DD, for example 2026-11-01."},
			{Column: "valid_to", PromptTitle: "Date", Prompt: "Write the date as YYYY-MM-DD, for example 2027-03-31."},
		}}
	case "properties":
		return sheetRules{locked: []string{"property_code"}, rules: []spreadsheet.Validation{
			{Column: "doc_code", PromptTitle: "Document code", Prompt: "2 letters or digits, for example HZ. Starts every PR, PO and GRN number (PR_HZ_0001). Cannot change once used."},
			{Column: "cluster", Source: "{Ref - Clusters.cluster}", ErrorTitle: "Unknown cluster", Error: "Pick a cluster from the list. Add a new one under Settings, Cities & Clusters first."},
			{Column: "stage", List: []string{"live", "preopening", "warehouse", "corporate"}, ErrorTitle: "Stage", Error: "Pick live, preopening, warehouse or corporate."},
			{Column: "property_manager_login", Source: "{Ref - Users.login}", ErrorTitle: "Unknown user", Error: "Pick a user's login from the list (see Ref - Users), or leave it blank."},
		}}
	case "users":
		return sheetRules{rules: []spreadsheet.Validation{
			{Column: "role", Source: "{Ref - Roles.role}", ErrorTitle: "Unknown role", Error: "Pick a role from the list (see Ref - Roles)."},
			{Column: "access", PromptTitle: "Property access", Prompt: "all, or property codes separated by ; (P01;P02), or cluster:Name;Name (see Ref - Properties and Ref - Clusters)."},
			{Column: "categories", PromptTitle: "Category limits", Prompt: "Blank for every category, or names from Ref - Categories separated by ;"},
			{Column: "password", PromptTitle: "Password", Prompt: "Leave blank to keep the current password. A new user without one signs in with a one-time code."},
		}}
	case "budgets":
		return sheetRules{locked: []string{"property_code", "property_name", "month", "sub_category"}}
	case "property_pct":
		return sheetRules{locked: []string{"property_code", "property_name"}}
	case "zoho_tags":
		return sheetRules{locked: []string{"property_code", "property_name"}, rules: []spreadsheet.Validation{
			{Column: "reporting_tag", Source: "{Lists.tags}", ErrorTitle: "Unknown reporting tag", Error: "Pick a reporting tag from the list. Sync from Zoho first if it is new."},
			{Column: "reporting_tag_option", Source: optionsOfTag, PromptTitle: "Option", Prompt: "Pick the reporting tag first: this list then shows only its options.",
				ErrorTitle: "Not under this tag", Error: "This option is not under the reporting tag on this row. Change the tag first, then pick from the list."},
		}}
	}
	return sheetRules{}
}

// listsSheet is the hidden sheet the dependent dropdowns read from.
func listsSheet(ctx context.Context, q database.Q) (spreadsheet.Sheet, error) {
	cols := [][][]string{}
	for _, sql := range []string{
		`SELECT c.name, sc.name FROM sub_category sc JOIN category c ON c.id=sc.category_id WHERE sc.active ORDER BY c.name, sc.name`,
		`SELECT DISTINCT c.name FROM sub_category sc JOIN category c ON c.id=sc.category_id WHERE sc.active ORDER BY c.name`,
		`SELECT c.name, k.code FROM sku k JOIN sub_category sc ON sc.id=k.sub_category_id JOIN category c ON c.id=sc.category_id WHERE k.active ORDER BY c.name, k.code`,
		`SELECT t.name, o.name FROM zoho_tag_option o JOIN zoho_tag t ON t.tag_id=o.tag_id WHERE o.active AND t.active ORDER BY t.name, o.name`,
		`SELECT name FROM zoho_tag WHERE active ORDER BY name`,
		fmt.Sprintf(`SELECT code, legal_name FROM vendor WHERE active AND (vendor_type='goods' OR (vendor_type='warehouse' AND %v)) ORDER BY code`, releasephase.VendorTypeOn("warehouse")),
		`SELECT ` + itemLabelSQL + `, c.name, sc.name, ` + fmt.Sprintf(scopeSQL, "k.scope='all'", "k.cluster_ids", "k.property_ids") + `
			FROM sku k JOIN brand b ON b.id=k.brand_id JOIN pack_size ps ON ps.id=k.pack_size_id JOIN sub_category sc ON sc.id=k.sub_category_id JOIN category c ON c.id=sc.category_id
			WHERE k.active ORDER BY c.name, sc.name, 1`,
	} {
		rows, err := rowsOf(ctx, q, sql)
		if err != nil {
			return spreadsheet.Sheet{}, err
		}
		cols = append(cols, rows)
	}
	widths := []int{2, 1, 2, 2, 1, 2, 4}
	n := 0
	for _, c := range cols {
		if len(c) > n {
			n = len(c)
		}
	}
	sh := spreadsheet.Sheet{Name: "Lists", Heads: []string{"category", "sub_category", "categories", "item_category", "sku_code", "tag", "tag_option", "tags",
		"vendor_code", "vendor_name", "item", "item_category_name", "item_sub_category", "item_available_at"},
		Rows: make([][]any, n), Hidden: true, Protect: true, Text: true}
	for i := 0; i < n; i++ {
		row := []any{}
		for c, rows := range cols {
			for j := 0; j < widths[c]; j++ {
				v := ""
				if i < len(rows) {
					v = rows[i][j]
				}
				row = append(row, v)
			}
		}
		sh.Rows[i] = row
	}
	return sh, nil
}

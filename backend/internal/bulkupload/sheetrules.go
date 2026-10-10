package bulkupload

// Rules inside the edit workbook, so Excel itself keeps the sheets consistent while someone edits: dropdowns that
// read from the other sheets (a mapping's vendor must be on the Vendors sheet; a sub-category list shows only the
// chosen category's sub-categories), a stop message naming the sheet to change first, and locked cells for keys and
// for fields that only change on screen (vendor bank details, GSTIN and PAN). The server still checks every row.

import (
	"context"

	"purchasedesk/internal/database"
	"purchasedesk/internal/indiantax"
	"purchasedesk/internal/money"
	"purchasedesk/internal/spreadsheet"
)

type sheetRules struct {
	locked []string
	rules  []spreadsheet.Validation
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
		return sheetRules{locked: []string{"mapping_id"}, rules: []spreadsheet.Validation{
			{Column: "vendor_code", Source: "{Vendors.vendor_code}", ErrorTitle: "Vendor not on the Vendors sheet",
				Error: "Pick a vendor code from the Vendors sheet. To map a new vendor, add it on the Vendors sheet and upload once, so it gets its code; then map it."},
			{Column: "level", List: []string{"sub_category", "sku"}, ErrorTitle: "Level", Error: "Level is sub_category (the whole sub-category) or sku (one item)."},
			ruleCategory, ruleSubcat,
			{Column: "sku_code", Source: skusOfCategory, PromptTitle: "SKU (level sku only)",
				Prompt: "Pick the category first: this list then shows only that category's items.", ErrorTitle: "Not in this category",
				Error: "This SKU is not in the category on this row (see Ref - Items). Change the category first, then pick from the list."},
			rulePropsCell,
			{Column: "estimate_allowed", List: []string{"yes", "no"}, ErrorTitle: "yes or no", Error: "Write yes or no."},
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
	} {
		rows, err := rowsOf(ctx, q, sql)
		if err != nil {
			return spreadsheet.Sheet{}, err
		}
		cols = append(cols, rows)
	}
	widths := []int{2, 1, 2, 2, 1}
	n := 0
	for _, c := range cols {
		if len(c) > n {
			n = len(c)
		}
	}
	sh := spreadsheet.Sheet{Name: "Lists", Heads: []string{"category", "sub_category", "categories", "item_category", "sku_code", "tag", "tag_option", "tags"},
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

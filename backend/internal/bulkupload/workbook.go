package bulkupload

// The edit workbook: "Download current data" on a page gives one Excel file with the page's own list and the masters
// it depends on, each on its own sheet (Vendor Mapping: the mappings and their vendors), plus read-only reference
// sheets with the exact names to use. Uploading the file back applies every edited sheet in one go, masters first.

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"purchasedesk/internal/access"
	"purchasedesk/internal/clock"
	"purchasedesk/internal/csvfile"
	"purchasedesk/internal/httpjson"
	"purchasedesk/internal/spreadsheet"
)

// bundle is what a page's workbook holds: the lists it edits, in the order they are applied, and reference sheets.
type bundle struct {
	edit []string // upload kinds; a sheet is named after its kind's title
	ref  []refSheet
}

type refSheet struct {
	name  string
	heads []string
	sql   string
}

var (
	refSubcats = refSheet{"Ref - Sub-categories", []string{"category", "sub_category", "code"},
		`SELECT c.name, sc.name, sc.code FROM sub_category sc JOIN category c ON c.id=sc.category_id WHERE sc.active ORDER BY c.name, sc.name`}
	refItems = refSheet{"Ref - Items", []string{"sku_code", "item", "category", "sub_category", "gst_percent"},
		`SELECT k.code, concat_ws(' ', b.name, k.item_name)||' · '||ps.name, c.name, sc.name, k.gst_pct::float8::text FROM sku k LEFT JOIN brand b ON b.id=k.brand_id
		JOIN pack_size ps ON ps.id=k.pack_size_id JOIN sub_category sc ON sc.id=k.sub_category_id JOIN category c ON c.id=sc.category_id WHERE k.active ORDER BY k.code`}
	refBrands = refSheet{"Ref - Brands", []string{"brand"}, `SELECT name FROM brand WHERE active ORDER BY name`}
	refPacks  = refSheet{"Ref - Pack sizes", []string{"unit"}, `SELECT name FROM pack_size WHERE active ORDER BY name`}
	refProps  = refSheet{"Ref - Properties", []string{"property_code", "property", "cluster", "city", "stage"},
		`SELECT p.code, p.name, k.name, c.name, p.kind FROM property p JOIN cluster k ON k.id=p.cluster_id JOIN city c ON c.id=k.city_id WHERE p.active ORDER BY p.code`}
	refClusters = refSheet{"Ref - Clusters", []string{"cluster", "city", "write in a properties cell as"},
		`SELECT k.name, c.name, 'cluster:'||k.name FROM cluster k JOIN city c ON c.id=k.city_id WHERE k.active ORDER BY c.name, k.name`}
	refBillTypes = refSheet{"Ref - Bill types", []string{"section", "bill_type"}, `SELECT section, name FROM bill_type WHERE active ORDER BY section, name`}
	refRoles     = refSheet{"Ref - Roles", []string{"role", "step", "rights"},
		`SELECT name, step, array_to_string(rights, ', ') FROM role WHERE active ORDER BY id`}
	refCategories = refSheet{"Ref - Categories", []string{"category"}, `SELECT name FROM category ORDER BY name`}
	refPeople     = refSheet{"Ref - Users", []string{"name", "login"},
		`SELECT name, coalesce(email::text, mobile, '') FROM app_user WHERE active ORDER BY name`}
	refTagOptions = refSheet{"Ref - Zoho tag options", []string{"reporting_tag", "reporting_tag_option"},
		`SELECT t.name, o.name FROM zoho_tag_option o JOIN zoho_tag t ON t.tag_id=o.tag_id WHERE o.active AND t.active ORDER BY t.name, o.name`}
)

var bundles = map[string]bundle{
	"mappings":     {edit: []string{"vendors", "mappings"}, ref: []refSheet{refItems, refSubcats, refProps, refClusters}},
	"rates":        {edit: []string{"vendors", "items", "rates"}, ref: []refSheet{refSubcats, refBrands, refPacks, refProps, refClusters}},
	"items":        {edit: []string{"items"}, ref: []refSheet{refSubcats, refBrands, refPacks, refProps, refClusters}},
	"vendors":      {edit: []string{"vendors"}, ref: []refSheet{refSubcats, refBillTypes}},
	"properties":   {edit: []string{"properties"}, ref: []refSheet{refClusters, refPeople}},
	"property_pct": {edit: []string{"property_pct"}},
	"users":        {edit: []string{"users"}, ref: []refSheet{refRoles, refProps, refClusters, refCategories}},
	"budgets":      {edit: []string{"budgets"}, ref: []refSheet{refSubcats}},
	"zoho_tags":    {edit: []string{"zoho_tags"}, ref: []refSheet{refTagOptions}},
}

// keys says, per list, which column ties a row to its record.
var keys = map[string]string{"items": "sku_code", "properties": "property_code", "vendors": "vendor_code", "users": "email (or mobile), one row per role",
	"mappings": "mapping_id (category must match the sub-category or SKU; to move a mapping to another category, pick one of that category's sub-categories)", "rates": "vendor_code + sku_code", "budgets": "property_code + month + sub_category", "property_pct": "property_code", "zoho_tags": "property_code"}

func (s *Service) workbook(w http.ResponseWriter, r *http.Request) error {
	name := chi.URLParam(r, "kind")
	b, ok := bundles[name]
	if !ok {
		return httpjson.NotFound("Download of this list")
	}
	u := access.From(r)
	readme := spreadsheet.Sheet{Name: "Read me", Heads: []string{"How to edit in bulk"}, Rows: [][]any{
		{"1. Change what you need on the sheets below, and add new rows. Do not rename the sheets or the column headings."},
		{"2. Save the file as Excel (.xlsx) and upload it on the same page. Every row is checked before anything is saved."},
		{"3. A row with its key filled updates that record; a row with the key left blank is added as new; unchanged rows are skipped."},
		{"4. Sheets named \"Ref - ...\" are lists to copy exact names from. They are read-only and are not uploaded."},
		{"5. Many cells have a dropdown (click the cell, then the arrow). Pick the category first: the sub-category and SKU lists then show only that category's entries."},
		{"6. If a value needs a change on another sheet first (a vendor that is not on the Vendors sheet yet), Excel stops you and says which sheet to change."},
		{"7. Grey cells are locked: the keys of existing rows (so a row stays tied to its record), and vendor bank details, GSTIN and PAN."},
		{"Nothing is deleted by an upload. Mark records inactive on the screen."},
		{""},
		{"Sheets you can edit, in the order they are applied:"},
	}}
	sheets := []spreadsheet.Sheet{}
	for _, kn := range b.edit {
		k := s.kinds[kn]
		if k == nil || k.export == nil || !k.allowed(u) {
			continue
		}
		heads, rows, err := k.export(r.Context(), s.DB, r)
		if err != nil {
			return err
		}
		rl := rulesFor(kn)
		sh := spreadsheet.Sheet{Name: k.title, Heads: heads, Rows: make([][]any, len(rows)), Text: true, Validations: rl.rules, LockedCols: rl.locked, Protect: len(rl.locked) > 0}
		for i, row := range rows {
			sh.Rows[i] = make([]any, len(row))
			for j, v := range row {
				sh.Rows[i][j] = v
			}
		}
		sheets = append(sheets, sh)
		readme.Rows = append(readme.Rows, []any{fmt.Sprintf("  %s: key %s", k.title, keys[kn])})
	}
	if len(sheets) == 0 {
		return httpjson.Forbidden("You do not have the right to edit this list in bulk.")
	}
	if textIn(b.edit, "vendors") {
		readme.Rows = append(readme.Rows, []any{""}, []any{"Vendors: bank_holder, bank_account, ifsc, gstin and pan are locked for existing vendors (change them on the vendor screen, where Finance re-verifies them). Account numbers are never in this file."})
	}
	if textIn(b.edit, "users") {
		readme.Rows = append(readme.Rows, []any{""}, []any{"Users: one row per role. Delete a role's row to remove it, add a row to give one. A blank password keeps the current one. A user left out of the file is not changed."})
	}
	for _, ref := range b.ref {
		rows, err := rowsOf(r.Context(), s.DB, ref.sql)
		if err != nil {
			return err
		}
		sh := spreadsheet.Sheet{Name: ref.name, Heads: ref.heads, Rows: make([][]any, len(rows)), Protect: true}
		for i, row := range rows {
			sh.Rows[i] = make([]any, len(row))
			for j, v := range row {
				sh.Rows[i][j] = v
			}
		}
		sheets = append(sheets, sh)
	}
	lists, err := listsSheet(r.Context(), s.DB)
	if err != nil {
		return err
	}
	sheets = append(sheets, lists)
	return spreadsheet.Send(w, fmt.Sprintf("%s-%s.xlsx", strings.ReplaceAll(name, "_", "-"), clock.YMD(clock.Now())), append([]spreadsheet.Sheet{readme}, sheets...))
}

func textIn(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// part is one table of an upload: the CSV file, or one edited sheet of a workbook.
type part struct {
	sheet string // "" for a CSV file
	kind  *kind
	heads []string
	rows  []csvfile.Row
}

type uploadIn struct {
	Text string `json:"text"` // a CSV file
	XLSX string `json:"xlsx"` // or an Excel workbook, base64
}

// parts reads an upload. A CSV file is one table for the page's own list; a workbook gives every sheet of the page's
// bundle that is in the file, in the bundle's order. Read-me and reference sheets are ignored.
func (s *Service) parts(r *http.Request, name string, k *kind) ([]part, error) {
	var in uploadIn
	if err := httpjson.Decode(r, &in); err != nil {
		return nil, err
	}
	if in.XLSX == "" {
		heads, rows, err := csvfile.Parse(in.Text)
		if err != nil {
			return nil, httpjson.Bad("%v", err)
		}
		return []part{{kind: k, heads: heads, rows: rows}}, nil
	}
	data, err := base64.StdEncoding.DecodeString(in.XLSX)
	if err != nil {
		return nil, httpjson.Bad("The file could not be read.")
	}
	names, tables, err := spreadsheet.Read(data)
	if err != nil {
		return nil, httpjson.Bad("The file could not be read as Excel: %v", err)
	}
	order := []string{name}
	if b, ok := bundles[name]; ok {
		order = b.edit
	}
	out := []part{}
	for _, kn := range order {
		kk := s.kinds[kn]
		if kk == nil {
			continue
		}
		for _, sn := range names {
			if !strings.EqualFold(strings.TrimSpace(sn), sheetTitle(kk.title)) {
				continue
			}
			if len(tables[sn]) < 2 {
				break // header only: nothing to do on this sheet
			}
			heads, rows, err := csvfile.FromTable(tables[sn])
			if err != nil {
				return nil, httpjson.Bad("Sheet %s: %v", sn, err)
			}
			out = append(out, part{sheet: sn, kind: kk, heads: heads, rows: rows})
			break
		}
	}
	if len(out) == 0 {
		return nil, httpjson.Bad("No sheet in this file can be uploaded here. Download the current data from this page and edit that file.")
	}
	return out, nil
}

// sheetTitle is a kind's title as Excel stores it as a sheet name (at most 31 characters, no : \ / ? * [ ]).
func sheetTitle(t string) string {
	t = strings.NewReplacer(":", " ", "\\", " ", "/", " ", "?", " ", "*", " ", "[", "(", "]", ")").Replace(t)
	if r := []rune(t); len(r) > 31 {
		t = string(r[:31])
	}
	return t
}

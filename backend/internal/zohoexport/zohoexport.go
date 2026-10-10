// Package zohoexport downloads, as Excel, what Zoho Books holds right now: on each click it reads the list live from
// Zoho (every page) and writes every field Zoho returns, one column per field. It only reads; nothing is saved.
package zohoexport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"

	"purchasedesk/internal/clock"
	"purchasedesk/internal/httpjson"
	"purchasedesk/internal/spreadsheet"
	"purchasedesk/internal/zohoapi"
	"purchasedesk/internal/zohosync"
)

type Service struct {
	Sync *zohosync.Service
}

func (s *Service) Routes(r chi.Router) {
	r.Get("/zoho/export/{tab}", httpjson.Wrap(s.export))
}

// list is one Zoho list: where to read it and the key its records come under.
type list struct {
	sheet, path, key string
	query            url.Values
	paged            bool
}

var (
	items     = list{"Items", "/items", "items", url.Values{}, true}
	accounts  = list{"Chart of accounts", "/chartofaccounts", "chartofaccounts", url.Values{"filter_by": {"AccountType.All"}}, true}
	taxes     = list{"Taxes", "/settings/taxes", "taxes", nil, true}
	vendors   = list{"Vendors", "/contacts", "contacts", url.Values{"contact_type": {"vendor"}}, true}
	locations = list{"Locations", "/locations", "locations", nil, false}
	bills     = list{"Bills", "/bills", "bills", url.Values{}, true}
	journals  = list{"Journals", "/journals", "journals", url.Values{}, true}
)

// tabs: the lists in each tab's file. "tags" stands for the reporting tags and their options (two sheets).
var tabs = map[string][]string{
	"all":        {"accounts", "taxes", "tags", "locations", "vendors", "items"},
	"properties": {"tags", "locations"},
	"accounts":   {"accounts"},
	"taxes":      {"taxes"},
	"vendors":    {"vendors"},
	"items":      {"items"},
	"bills":      {"bills"},
	"journals":   {"journals"},
}

var lists = map[string]list{"items": items, "accounts": accounts, "taxes": taxes, "vendors": vendors, "locations": locations, "bills": bills, "journals": journals}

func (s *Service) export(w http.ResponseWriter, r *http.Request) error {
	tab := chi.URLParam(r, "tab")
	want, ok := tabs[tab]
	if !ok {
		return httpjson.NotFound("Download")
	}
	c, _, err := s.Sync.Client(r.Context())
	if err != nil {
		return err
	}
	sheets := []spreadsheet.Sheet{}
	for _, k := range want {
		var got []spreadsheet.Sheet
		if k == "tags" {
			got, err = readTags(r.Context(), c)
		} else {
			var sh spreadsheet.Sheet
			sh, err = read(r.Context(), c, lists[k])
			got = []spreadsheet.Sheet{sh}
		}
		if err != nil {
			what := strings.ToLower(lists[k].sheet)
			if k == "tags" {
				what = "reporting tags"
			}
			return httpjson.Bad("Zoho did not send the %s: %v", what, err)
		}
		sheets = append(sheets, got...)
	}
	return spreadsheet.Send(w, fmt.Sprintf("zoho-%s-%s.xlsx", tab, clock.YMD(clock.Now())), sheets)
}

// read fetches every record of a list from Zoho and lays it out as a sheet.
func read(ctx context.Context, c *zohoapi.Client, l list) (spreadsheet.Sheet, error) {
	recs := []json.RawMessage{}
	if l.paged {
		err := c.ListAll(ctx, l.path, l.key, l.query, func(raw json.RawMessage) error {
			recs = append(recs, raw)
			return nil
		})
		if err != nil {
			return spreadsheet.Sheet{}, err
		}
	} else {
		var out map[string]json.RawMessage
		if err := c.Do(ctx, http.MethodGet, l.path, l.query, nil, nil, &out); err != nil {
			return spreadsheet.Sheet{}, err
		}
		_ = json.Unmarshal(out[l.key], &recs)
	}
	if l.key == "contacts" { // Zoho can slip customers into the vendor list; keep vendors only
		recs = only(recs, "contact_type", "vendor")
	}
	return table(l.sheet, recs, nil), nil
}

// readTags fetches the reporting tags, then each tag's options (one call per tag).
func readTags(ctx context.Context, c *zohoapi.Client) ([]spreadsheet.Sheet, error) {
	var out map[string]json.RawMessage
	if err := c.Do(ctx, http.MethodGet, "/reportingtags", nil, nil, nil, &out); err != nil {
		return nil, err
	}
	tags := []json.RawMessage{}
	for _, k := range []string{"tags", "reporting_tags"} {
		var t []json.RawMessage
		if json.Unmarshal(out[k], &t) == nil {
			tags = append(tags, t...)
		}
	}
	opts := []json.RawMessage{}
	optTag := []string{}
	for _, raw := range tags {
		var t struct {
			ID   string `json:"tag_id"`
			Name string `json:"tag_name"`
		}
		_ = json.Unmarshal(raw, &t)
		var o map[string]json.RawMessage
		if err := c.Do(ctx, http.MethodGet, "/reportingtags/options", url.Values{"tag_id": {t.ID}}, nil, nil, &o); err != nil {
			return nil, err
		}
		var list []json.RawMessage
		_ = json.Unmarshal(o["options"], &list)
		for _, x := range flatten(list) {
			opts = append(opts, x)
			optTag = append(optTag, t.Name)
		}
	}
	return []spreadsheet.Sheet{table("Reporting tags", tags, nil), table("Reporting tag options", opts, &lead{"tag_name", optTag})}, nil
}

// flatten lifts nested child options (Zoho nests them under "children") into the same list.
func flatten(list []json.RawMessage) []json.RawMessage {
	out := []json.RawMessage{}
	for _, raw := range list {
		out = append(out, raw)
		var x struct {
			Children []json.RawMessage `json:"children"`
		}
		if json.Unmarshal(raw, &x) == nil && len(x.Children) > 0 {
			out = append(out, flatten(x.Children)...)
		}
	}
	return out
}

func only(recs []json.RawMessage, field, want string) []json.RawMessage {
	out := recs[:0]
	for _, raw := range recs {
		var m map[string]any
		if json.Unmarshal(raw, &m) == nil {
			if v, ok := m[field].(string); ok && v != "" && v != want {
				continue
			}
		}
		out = append(out, raw)
	}
	return out
}

// lead is an extra first column, one value per record (the tag an option belongs to).
type lead struct {
	head string
	vals []string
}

// table makes a sheet with one column per field, in the order Zoho sends them; a field that only some records have
// still gets its column. Nested objects and lists stay as their JSON text.
func table(name string, recs []json.RawMessage, first *lead) spreadsheet.Sheet {
	heads := []string{}
	seen := map[string]int{}
	rows := make([]map[string]any, 0, len(recs))
	for _, raw := range recs {
		keys, vals := ordered(raw)
		m := map[string]any{}
		for i, k := range keys {
			if _, ok := seen[k]; !ok {
				seen[k] = len(heads)
				heads = append(heads, k)
			}
			m[k] = vals[i]
		}
		rows = append(rows, m)
	}
	sh := spreadsheet.Sheet{Name: name, Heads: heads, Rows: make([][]any, 0, len(rows))}
	if first != nil {
		sh.Heads = append([]string{first.head}, heads...)
	}
	for i, m := range rows {
		row := make([]any, 0, len(sh.Heads))
		if first != nil {
			row = append(row, first.vals[i])
		}
		for _, h := range heads {
			v, ok := m[h]
			if !ok {
				v = ""
			}
			row = append(row, v)
		}
		sh.Rows = append(sh.Rows, row)
	}
	return sh
}

// ordered reads one JSON object keeping its key order, turning each value into a cell value.
func ordered(raw json.RawMessage) ([]string, []any) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, nil
	}
	keys, vals := []string{}, []any{}
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			break
		}
		k, _ := t.(string)
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			break
		}
		keys = append(keys, k)
		vals = append(vals, cell(v))
	}
	return keys, vals
}

// cell keeps numbers as numbers and true/false as such; nested values become compact JSON (Excel holds 32,767 characters).
func cell(v json.RawMessage) any {
	var x any
	d := json.NewDecoder(bytes.NewReader(v))
	d.UseNumber()
	if d.Decode(&x) != nil {
		return string(v)
	}
	switch t := x.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		return t
	case json.Number:
		if f, err := t.Float64(); err == nil && len(t.String()) < 16 {
			return f
		}
		return t.String()
	}
	var b bytes.Buffer
	if json.Compact(&b, v) != nil {
		return string(v)
	}
	s := b.String()
	if len(s) > 32000 {
		s = s[:32000]
	}
	return s
}

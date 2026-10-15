package bulkupload

// The edit round trip: "Download current data" writes a master list in exactly its upload's columns, with each row's
// code as its key. Uploading the edited file updates rows whose code exists, adds rows with no code, and skips rows
// that are unchanged, so a re-upload only touches what was edited.

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"purchasedesk/internal/access"
	"purchasedesk/internal/clock"
	"purchasedesk/internal/csvfile"
	"purchasedesk/internal/database"
	"purchasedesk/internal/httpjson"
	"purchasedesk/internal/textclean"
	"purchasedesk/internal/vendormapping"
)

// exportFn lists the current rows in the upload's columns.
type exportFn func(ctx context.Context, q database.Q, r *http.Request) ([]string, [][]string, error)

func (s *Service) export(w http.ResponseWriter, r *http.Request) error {
	k, err := s.kind(r)
	if err != nil {
		return err
	}
	if k.export == nil {
		return httpjson.NotFound("Download of this list")
	}
	heads, rows, err := k.export(r.Context(), s.DB, r)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-%s.csv"`, chi.URLParam(r, "kind"), clock.YMD(clock.Now())))
	_, err = w.Write(append([]byte(string(rune(0xFEFF))), csvfile.Write(heads, rows)...))
	return err
}

// rowsOf runs a query whose every column is text and returns its rows.
func rowsOf(ctx context.Context, q database.Q, sql string, args ...any) ([][]string, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) ([]string, error) {
		vals, err := row.Values()
		out := make([]string, len(vals))
		for i, v := range vals {
			if v != nil {
				out[i] = fmt.Sprint(v)
			}
		}
		return out, err
	})
}

// current holds the exported rows by key, to tell which uploaded rows are unchanged.
type current map[string]map[string]string

func (s *Service) currentRows(r *http.Request, q database.Q, k *kind) (current, error) {
	if k.export == nil || k.keyOf == nil {
		return nil, nil
	}
	heads, rows, err := k.export(r.Context(), q, r)
	if err != nil {
		return nil, err
	}
	out := current{}
	for _, vals := range rows {
		m := map[string]string{}
		for i, h := range heads {
			m[h] = vals[i]
		}
		if key := k.keyOf(csvfile.Row{Values: m}); key != "" {
			out[key] = m
		}
	}
	return out, nil
}

func (c current) same(k *kind, row csvfile.Row) bool {
	old, ok := c[k.keyOf(row)]
	if !ok {
		return false
	}
	for h, v := range old {
		if textclean.Contains(k.computed, h) {
			continue
		}
		if from, ok := k.derived[h]; ok && strings.TrimSpace(row.Get(from)) != "" {
			continue
		}
		if norm(row.Get(strings.ToLower(strings.TrimSpace(h)))) != norm(v) {
			return false
		}
	}
	return true
}

// norm compares cells loosely: case, spaces, list order and number formatting do not count as a change.
func norm(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	if f, err := strconv.ParseFloat(strings.ReplaceAll(v, ",", ""), 64); err == nil {
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	if strings.Contains(v, ";") {
		parts := strings.Split(v, ";")
		for i := range parts {
			parts[i] = strings.TrimSpace(parts[i])
		}
		sort.Strings(parts)
		return strings.Join(parts, ";")
	}
	return strings.Join(strings.Fields(v), " ")
}

func key(cols ...string) func(csvfile.Row) string {
	return func(r csvfile.Row) string {
		parts := []string{}
		for _, c := range cols {
			v := strings.ToUpper(strings.TrimSpace(r.Get(c)))
			if v == "" {
				return ""
			}
			parts = append(parts, v)
		}
		return strings.Join(parts, "|")
	}
}

// scope reads a properties cell: "ALL" (or blank) for every property, or property codes and "cluster:Name" entries
// separated by ";".
func scope(ctx context.Context, q database.Q, cell string) (all bool, clusters, props []int64, problem string) {
	cell = strings.TrimSpace(cell)
	if cell == "" || strings.EqualFold(cell, "all") {
		return true, nil, nil, ""
	}
	for _, part := range strings.FieldsFunc(cell, func(r rune) bool { return r == ';' || r == '|' }) {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		var id int64
		if strings.HasPrefix(strings.ToLower(part), "cluster:") {
			if err := q.QueryRow(ctx, `SELECT id FROM cluster WHERE norm_key(name)=norm_key($1)`, strings.TrimSpace(part[8:])).Scan(&id); err != nil {
				return false, nil, nil, fmt.Sprintf("Unknown cluster %q", part[8:])
			}
			clusters = append(clusters, id)
			continue
		}
		if err := q.QueryRow(ctx, `SELECT id FROM property WHERE upper(code)=upper($1)`, part).Scan(&id); err != nil {
			return false, nil, nil, fmt.Sprintf("Unknown property code %s", part)
		}
		props = append(props, id)
	}
	return false, clusters, props, ""
}

// scopeSQL renders a scope as the properties cell: ALL, or codes and cluster:Name entries.
const scopeSQL = `CASE WHEN %[1]s THEN 'ALL' ELSE concat_ws(';',
	(SELECT string_agg('cluster:'||c.name, ';' ORDER BY c.name) FROM cluster c WHERE c.id = ANY(%[2]s)),
	(SELECT string_agg(p.code, ';' ORDER BY p.code) FROM property p WHERE p.id = ANY(%[3]s))) END`

func yes(v string) bool {
	return textclean.Contains([]string{"yes", "y", "true", "1"}, strings.ToLower(strings.TrimSpace(v)))
}

// ---------- vendor mapping ----------

type mappingRow struct {
	id int64
	in vendormapping.MappingInput
}

func mappingID(v string) (int64, bool) {
	v = strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(v)), "M")
	n, err := strconv.ParseInt(v, 10, 64)
	return n, err == nil && n > 0
}

func (s *Service) mappingsKind() *kind {
	return &kind{
		title: "Vendor mapping", module: "Vendor Mapping", allowed: func(u *access.User) bool { return hasUpload(u) && u.HasRight(access.Masters) },
		required: []string{"vendor_code"},
		sample:   "mapping_id,vendor_code,vendor_name,item,category,sub_category,properties,estimate_allowed\n,V001,Shree Balaji Hotel Supplies,,Housekeeping,Guest Amenities,ALL,no\n,V004,Gupta Linen House,LN-0001,Housekeeping,Linen,P01;cluster:Delhi South,no\n",
		keyOf:    key("mapping_id"),
		computed: []string{"vendor_name", "item_available_at", "rate_status"},
		derived:  map[string]string{"category": "item", "sub_category": "item"},
		check: func(ctx context.Context, x *run, row csvfile.Row) result {
			// A row with an item maps that one SKU; a row without maps the whole sub-category. Older files carry a level
			// column and the SKU in sku_code; the edit workbook has the item label ("... · SKU code") in item.
			code := strings.TrimSpace(row.Get("sku_code"))
			if code == "" {
				code = skuOfItem(row.Get("item"))
			}
			level := strings.ToLower(strings.TrimSpace(row.Get("level")))
			switch level {
			case "sub_category", "subcategory", "sub-category":
				level = "sc"
			case "":
				level = "sc"
				if code != "" {
					level = "sku"
				}
			}
			if level != "sku" && level != "sc" {
				return bad("Level is sku or sub_category")
			}
			var in vendormapping.MappingInput
			// The edit workbook has a tick column per property; a CSV may instead have one properties cell.
			all, clusters, props, problem := tickedProperties(ctx, x.q, row)
			if problem == "" && all && clusters == nil && props == nil && row.Has("properties") {
				all, clusters, props, problem = scope(ctx, x.q, row.Get("properties"))
			}
			if problem != "" {
				return bad("%s", problem)
			}
			in.All, in.ClusterIDs, in.PropertyIDs, in.Estimate = all, clusters, props, yes(row.Get("estimate_allowed"))
			// mapping_id names the mapping to edit; its vendor, level and item may change too (same M-number).
			id := int64(0)
			if v := row.Get("mapping_id"); strings.TrimSpace(v) != "" {
				var ok bool
				if id, ok = mappingID(v); !ok {
					return bad("mapping_id looks like M001")
				}
				var exists bool
				_ = x.q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM vendor_mapping WHERE id=$1)`, id).Scan(&exists)
				if !exists {
					return bad("No mapping %s. Leave mapping_id blank to add a new mapping", v)
				}
			}
			if err := x.q.QueryRow(ctx, `SELECT id FROM vendor WHERE upper(code)=upper($1)`, row.Get("vendor_code")).Scan(&in.VendorID); err != nil {
				return bad("Unknown vendor code %s", row.Get("vendor_code"))
			}
			in.Level = level
			if level == "sku" {
				if err := x.q.QueryRow(ctx, `SELECT id FROM sku WHERE upper(code)=upper($1)`, code).Scan(&in.SKUID); err != nil {
					return bad("Unknown item %s. Pick it from the item list", textclean.Default(strings.TrimSpace(row.Get("item")), code))
				}
				if problem := outsideItem(ctx, x.q, in); problem != "" {
					return bad("%s", problem)
				}
			} else if err := x.q.QueryRow(ctx, `SELECT id FROM sub_category WHERE norm_key(name)=norm_key($1)`, row.Get("sub_category")).Scan(&in.SubCategoryID); err != nil {
				return bad("Unknown sub-category %q", row.Get("sub_category"))
			}
			// The category comes from the sub-category (or the SKU's sub-category). If the row names one, it must match.
			if cat := strings.TrimSpace(row.Get("category")); cat != "" {
				var real, sub string
				_ = x.q.QueryRow(ctx, `SELECT c.name, sc.name FROM sub_category sc JOIN category c ON c.id=sc.category_id
					WHERE sc.id = CASE WHEN $1='sku' THEN (SELECT sub_category_id FROM sku WHERE id=$2) ELSE $3 END`, level, in.SKUID, in.SubCategoryID).Scan(&real, &sub)
				if real != "" && textclean.Key(real) != textclean.Key(cat) {
					what := "Sub-category " + sub
					if level == "sku" {
						what = "Item " + strings.ToUpper(code) + " (" + sub + ")"
					}
					return bad("%s is in category %s, not %s. To map %s, pick one of its sub-categories from the sub_category list", what, real, cat, cat)
				}
			}
			if err := vendormapping.CheckMapping(ctx, x.q, id, &in); err != nil {
				return bad("%s", strings.TrimSuffix(err.Error(), "."))
			}
			if id != 0 && x.dup(fmt.Sprint("m", id)) {
				return bad("Duplicate row in the file")
			}
			msg := "New mapping"
			if id != 0 {
				msg = fmt.Sprintf("Updates M%03d", id)
			}
			return result{ok: true, msg: msg, obj: &mappingRow{id, in}}
		},
		commit: func(ctx context.Context, tx pgx.Tx, x *run, objs []any) error {
			for _, o := range objs {
				m := o.(*mappingRow)
				// Checked again in order, so two rows of the file that clash with each other are caught.
				if err := vendormapping.CheckMapping(ctx, tx, m.id, &m.in); err != nil {
					return err
				}
				if _, err := vendormapping.SaveMapping(ctx, tx, x.u, m.id, &m.in); err != nil {
					return err
				}
			}
			return nil
		},
		export: func(ctx context.Context, q database.Q, r *http.Request) ([]string, [][]string, error) {
			props, err := mappingProperties(ctx, q)
			if err != nil {
				return nil, nil, err
			}
			heads := []string{"mapping_id", "vendor_code", "vendor_name", "item", "category", "sub_category", "estimate_allowed", "rate_status", "item_available_at"}
			for _, p := range props {
				heads = append(heads, p.head)
			}
			rows, err := rowsOf(ctx, q, `SELECT 'M'||lpad(m.id::text,3,'0'), v.code, v.legal_name, CASE WHEN m.level='sku' THEN `+itemLabelSQL+` ELSE '' END,
				cat.name, msc.name, CASE WHEN m.estimate_allowed THEN 'yes' ELSE 'no' END,
				CASE WHEN m.level='sku' THEN CASE WHEN EXISTS (SELECT 1 FROM rate_card rc WHERE rc.vendor_id=m.vendor_id AND rc.sku_id=m.sku_id AND NOT rc.superseded AND current_date BETWEEN rc.valid_from AND rc.valid_to)
					THEN 'Valid rate' ELSE 'No valid rate, estimate used' END
				ELSE (SELECT count(*) FILTER (WHERE EXISTS (SELECT 1 FROM rate_card rc WHERE rc.vendor_id=m.vendor_id AND rc.sku_id=x.id AND NOT rc.superseded AND current_date BETWEEN rc.valid_from AND rc.valid_to))
					||' of '||count(*)||' items with a valid rate' FROM sku x WHERE x.sub_category_id=msc.id AND x.active) END,
				CASE WHEN m.level='sku' THEN `+fmt.Sprintf(scopeSQL, "k.scope='all'", "k.cluster_ids", "k.property_ids")+` ELSE '' END,
				CASE WHEN m.all_properties THEN 'ALL' ELSE coalesce((SELECT string_agg(property_id::text, ';') FROM vendor_mapping_property WHERE mapping_id=m.id),'') END
				FROM vendor_mapping m JOIN vendor v ON v.id=m.vendor_id LEFT JOIN sku k ON k.id=m.sku_id LEFT JOIN brand b ON b.id=k.brand_id LEFT JOIN pack_size ps ON ps.id=k.pack_size_id
				JOIN sub_category msc ON msc.id=coalesce(m.sub_category_id, k.sub_category_id) JOIN category cat ON cat.id=msc.category_id
				WHERE m.active ORDER BY v.code, cat.name, msc.name, k.code`)
			if err != nil {
				return nil, nil, err
			}
			// The last column is the mapping's properties (ALL or ids); it becomes one yes/blank cell per property.
			for i, row := range rows {
				where := row[len(row)-1]
				on := map[string]bool{}
				for _, id := range strings.Split(where, ";") {
					on[id] = true
				}
				out := row[:len(row)-1]
				for _, p := range props {
					tick := ""
					if where == "ALL" || on[fmt.Sprint(p.id)] {
						tick = "yes"
					}
					out = append(out, tick)
				}
				rows[i] = out
			}
			return heads, rows, nil
		},
	}
}

// itemLabelSQL is how an item reads in the edit workbook's item dropdown: brand, name and pack size, then " · " and its
// SKU code, which keeps every label unique. Needs sku k, brand b and pack_size ps.
const itemLabelSQL = `CASE WHEN b.kind IN ('other','service') THEN k.item_name ELSE concat_ws(' ', b.name, k.item_name) END||' ('||ps.name||') · '||k.code`

// skuOfItem reads the SKU code from an item label, or takes a bare SKU code as it is.
func skuOfItem(label string) string {
	label = strings.TrimSpace(label)
	if i := strings.LastIndex(label, " · "); i >= 0 {
		return strings.TrimSpace(label[i+len(" · "):])
	}
	return label
}

// outsideItem refuses mapping an item at a property the item is not set up for (its properties in the Items master).
// ALL is fine: the vendor then supplies the item wherever it is available.
func outsideItem(ctx context.Context, q database.Q, in vendormapping.MappingInput) string {
	if in.All {
		return ""
	}
	var outside []string
	if err := q.QueryRow(ctx, `SELECT coalesce(array_agg(p.code ORDER BY p.code),'{}') FROM property p, sku k
		WHERE k.id=$1 AND k.scope<>'all' AND (p.id = ANY($3) OR (p.active AND p.kind='live' AND p.cluster_id = ANY($2)))
		AND NOT (p.id = ANY(k.property_ids) OR p.cluster_id = ANY(k.cluster_ids))`, in.SKUID, in.ClusterIDs, in.PropertyIDs).Scan(&outside); err != nil {
		return err.Error()
	}
	if len(outside) == 0 {
		return ""
	}
	return fmt.Sprintf("The item is not available at %s (see item_available_at). Add the property to the item in Items first, or remove it here", strings.Join(outside, ", "))
}

// mappingProperty is a property with a tick column on the vendor mapping sheet, headed "P01 · Sector 45, Gurugram".
type mappingProperty struct {
	id                  int64
	code, head, cluster string
}

// mappingProperties lists the properties that order from vendors: live and pre-opening ones, and the warehouse.
func mappingProperties(ctx context.Context, q database.Q) ([]mappingProperty, error) {
	rows, err := q.Query(ctx, `SELECT p.id, p.code, p.code||' · '||p.name, k.name FROM property p JOIN cluster k ON k.id=p.cluster_id
		WHERE p.active AND coalesce(p.code,'')<>'' AND p.kind IN ('live','preopening','warehouse') ORDER BY p.code`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (mappingProperty, error) {
		var p mappingProperty
		return p, row.Scan(&p.id, &p.code, &p.head, &p.cluster)
	})
}

// tickedProperties reads the tick columns of a mapping row (headed "P01 · name"). Every column ticked means ALL, so
// the mapping also covers properties added later; otherwise the ticked ones. A row without tick columns gives
// all=true with no lists, and the caller reads the properties cell instead.
func tickedProperties(ctx context.Context, q database.Q, row csvfile.Row) (all bool, clusters, props []int64, problem string) {
	rows, err := q.Query(ctx, `SELECT upper(code), id FROM property WHERE coalesce(code,'')<>''`)
	if err != nil {
		return false, nil, nil, err.Error()
	}
	ids := map[string]int64{}
	for rows.Next() {
		var c string
		var id int64
		if err := rows.Scan(&c, &id); err != nil {
			rows.Close()
			return false, nil, nil, err.Error()
		}
		ids[c] = id
	}
	rows.Close()
	cols, ticked := 0, []int64{}
	for h, v := range row.Values {
		i := strings.Index(h, " · ")
		if i <= 0 {
			continue
		}
		id, ok := ids[strings.ToUpper(strings.TrimSpace(h[:i]))]
		if !ok {
			continue
		}
		cols++
		if yes(v) {
			ticked = append(ticked, id)
		}
	}
	if cols == 0 {
		return true, nil, nil, ""
	}
	if len(ticked) == 0 {
		return false, nil, nil, "Tick at least one property (yes in its column)"
	}
	if len(ticked) == cols {
		return true, []int64{}, []int64{}, ""
	}
	return false, nil, ticked, ""
}

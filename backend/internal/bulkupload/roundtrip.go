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
		if norm(row.Get(h)) != norm(v) {
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
		required: []string{"vendor_code", "level", "properties"},
		sample:   "mapping_id,vendor_code,vendor_name,level,category,sub_category,sku_code,item,properties,estimate_allowed\n,V001,Shree Balaji Hotel Supplies,sub_category,Housekeeping,Guest Amenities,,,ALL,no\n,V004,Gupta Linen House,sku,Housekeeping,,LN-0001,Bath towel,P01;cluster:Delhi South,no\n",
		keyOf:    key("mapping_id"),
		check: func(ctx context.Context, x *run, row csvfile.Row) result {
			level := strings.ToLower(strings.TrimSpace(row.Get("level")))
			if level == "sub_category" || level == "subcategory" || level == "sub-category" {
				level = "sc"
			}
			if level != "sku" && level != "sc" {
				return bad("Level is sku or sub_category")
			}
			var in vendormapping.MappingInput
			all, clusters, props, problem := scope(ctx, x.q, row.Get("properties"))
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
				if err := x.q.QueryRow(ctx, `SELECT id FROM sku WHERE upper(code)=upper($1)`, row.Get("sku_code")).Scan(&in.SKUID); err != nil {
					return bad("Unknown SKU code %s", row.Get("sku_code"))
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
						what = "SKU " + strings.ToUpper(strings.TrimSpace(row.Get("sku_code"))) + " (" + sub + ")"
					}
					return bad("%s is in category %s, not %s. To map %s, pick one of its sub-categories (see the Ref - Sub-categories sheet)", what, real, cat, cat)
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
			heads := []string{"mapping_id", "vendor_code", "vendor_name", "level", "category", "sub_category", "sku_code", "item", "properties", "estimate_allowed"}
			rows, err := rowsOf(ctx, q, `SELECT 'M'||lpad(m.id::text,3,'0'), v.code, v.legal_name, CASE m.level WHEN 'sku' THEN 'sku' ELSE 'sub_category' END,
				cat.name, CASE WHEN m.level='sc' THEN sc.name ELSE '' END, coalesce(k.code,''), coalesce(concat_ws(' ', b.name, k.item_name)||' · '||ps.name,''),
				`+fmt.Sprintf(scopeSQL, "m.all_properties", "'{}'::bigint[]", "coalesce((SELECT array_agg(property_id) FROM vendor_mapping_property WHERE mapping_id=m.id),'{}')")+`,
				CASE WHEN m.estimate_allowed THEN 'yes' ELSE 'no' END
				FROM vendor_mapping m JOIN vendor v ON v.id=m.vendor_id LEFT JOIN sku k ON k.id=m.sku_id LEFT JOIN brand b ON b.id=k.brand_id LEFT JOIN pack_size ps ON ps.id=k.pack_size_id
				LEFT JOIN sub_category sc ON sc.id=m.sub_category_id
				JOIN sub_category msc ON msc.id=coalesce(m.sub_category_id, k.sub_category_id) JOIN category cat ON cat.id=msc.category_id
				WHERE m.active ORDER BY m.id`)
			return heads, rows, err
		},
	}
}

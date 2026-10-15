package bulkupload

// Bulk upload and edit of the small masters behind properties and items: cities and clusters (one workbook, cities
// first so a new city can be used by a cluster on the next sheet), categories and sub-categories (likewise), and pack
// sizes (new ones are added; an existing one, in any spelling, is refused as already there; rename it on the screen).
// Cities, clusters and categories are matched by name: a known name updates that record, a new name adds one; rename
// them on their screen. Sub-categories are matched by their 2-letter SKU code, which never changes, so a row can
// rename a sub-category or move it to another category. The same checks run as on the screens.

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"purchasedesk/internal/access"
	"purchasedesk/internal/csvfile"
	"purchasedesk/internal/database"
	"purchasedesk/internal/geography"
	"purchasedesk/internal/items"
	"purchasedesk/internal/textclean"
)

var refManagers = refSheet{"Ref - Cluster Managers", []string{"name", "login"},
	`SELECT DISTINCT u.name, coalesce(u.email::text, u.mobile, '') FROM app_user u JOIN user_role ur ON ur.user_id=u.id JOIN role r ON r.id=ur.role_id
	WHERE u.active AND r.step='l2' ORDER BY 1`}

// seenAs counts an unchanged row's name as seen, so a second, changed row for the same record is refused as a duplicate.
func seenAs(col string) func(context.Context, *run, csvfile.Row) {
	return func(_ context.Context, x *run, row csvfile.Row) { x.dup(textclean.Key(row.Get(col))) }
}

// yesNo reads a yes/no cell; blank is no.
func yesNo(v string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "yes", "y", "true", "1":
		return true, true
	case "", "no", "n", "false", "0":
		return false, true
	}
	return false, false
}

func (s *Service) smallMasterKinds() map[string]*kind {
	masters := func(u *access.User) bool { return hasUpload(u) && u.HasRight(access.Masters) }
	return map[string]*kind{
		"cities": {
			title: "Cities", module: "Properties", allowed: masters,
			required: []string{"city", "state_code"},
			sample:   "city,state_code\nGurugram,HR\nBengaluru,KA\n",
			keyOf:    key("city"),
			onSame:   seenAs("city"),
			export: func(ctx context.Context, q database.Q, r *http.Request) ([]string, [][]string, error) {
				rows, err := rowsOf(ctx, q, `SELECT name, state_code FROM city WHERE active ORDER BY name`)
				return []string{"city", "state_code"}, rows, err
			},
			check: func(ctx context.Context, x *run, row csvfile.Row) result {
				in := geography.CityInput{Name: row.Get("city"), StateCode: row.Get("state_code")}
				var id int64
				_ = x.q.QueryRow(ctx, `SELECT id FROM city WHERE norm_key(name)=norm_key($1)`, in.Name).Scan(&id)
				if errs := geography.ValidateCity(ctx, x.q, &in, id); len(errs) > 0 {
					return bad("%s", strings.Join(errs, " "))
				}
				if x.dup(textclean.Key(in.Name)) {
					return bad("Duplicate row in the file")
				}
				return result{ok: true, msg: map[bool]string{true: "Updates " + in.Name, false: "New city"}[id != 0], obj: &editRow{id, in}}
			},
			commit: func(ctx context.Context, tx pgx.Tx, x *run, objs []any) error {
				for _, o := range objs {
					e := o.(*editRow)
					if _, err := geography.SaveCity(ctx, tx, x.u, e.id, e.obj.(geography.CityInput)); err != nil {
						return err
					}
				}
				return nil
			},
		},
		"clusters": {
			title: "Clusters", module: "Properties", allowed: masters,
			required: []string{"cluster", "city"},
			sample:   "cluster,city,cluster_manager_login\nGurugram North,Gurugram,neha.bansal@saltstayz.com\nBengaluru Central,Bengaluru,\n",
			keyOf:    key("cluster"),
			onSame:   seenAs("cluster"),
			export: func(ctx context.Context, q database.Q, r *http.Request) ([]string, [][]string, error) {
				rows, err := rowsOf(ctx, q, `SELECT k.name, c.name, coalesce(u.email::text, u.mobile, '') FROM cluster k JOIN city c ON c.id=k.city_id
					LEFT JOIN app_user u ON u.id=k.manager_id WHERE k.active ORDER BY c.name, k.name`)
				return []string{"cluster", "city", "cluster_manager_login"}, rows, err
			},
			check: func(ctx context.Context, x *run, row csvfile.Row) result {
				in := geography.ClusterInput{Name: row.Get("cluster")}
				if err := x.q.QueryRow(ctx, `SELECT id FROM city WHERE norm_key(name)=norm_key($1) AND active`, row.Get("city")).Scan(&in.CityID); err != nil {
					return bad("Unknown city %q. Add it on the Cities sheet (or under Cities & Clusters) first", row.Get("city"))
				}
				// A blank manager leaves the cluster without one, as the download shows it.
				if l := strings.TrimSpace(row.Get("cluster_manager_login")); l != "" {
					var uid int64
					if err := x.q.QueryRow(ctx, `SELECT id FROM app_user WHERE email=$1::citext OR mobile=$1`, l).Scan(&uid); err != nil {
						return bad("No user signs in with %s", l)
					}
					in.ManagerID = &uid
				}
				var id int64
				_ = x.q.QueryRow(ctx, `SELECT id FROM cluster WHERE norm_key(name)=norm_key($1)`, in.Name).Scan(&id)
				if errs := geography.ValidateCluster(ctx, x.q, &in, id); len(errs) > 0 {
					return bad("%s", strings.Join(errs, " "))
				}
				if x.dup(textclean.Key(in.Name)) {
					return bad("Duplicate row in the file")
				}
				return result{ok: true, msg: map[bool]string{true: "Updates " + in.Name, false: "New cluster"}[id != 0], obj: &editRow{id, in}}
			},
			commit: func(ctx context.Context, tx pgx.Tx, x *run, objs []any) error {
				for _, o := range objs {
					e := o.(*editRow)
					if _, err := geography.SaveCluster(ctx, tx, x.u, e.id, e.obj.(geography.ClusterInput)); err != nil {
						return err
					}
				}
				return nil
			},
		},
		"categories": {
			title: "Categories", module: "Items", allowed: masters,
			required: []string{"category"},
			sample:   "category,fnb\nHousekeeping,no\nFood & Beverage,yes\n",
			keyOf:    key("category"),
			onSame:   seenAs("category"),
			export: func(ctx context.Context, q database.Q, r *http.Request) ([]string, [][]string, error) {
				rows, err := rowsOf(ctx, q, `SELECT name, CASE WHEN is_fb THEN 'yes' ELSE 'no' END FROM category ORDER BY name`)
				return []string{"category", "fnb"}, rows, err
			},
			check: func(ctx context.Context, x *run, row csvfile.Row) result {
				fb, ok := yesNo(row.Get("fnb"))
				if !ok {
					return bad("fnb is yes or no")
				}
				in := items.CategoryInput{Name: row.Get("category"), IsFB: fb}
				var id int64
				_ = x.q.QueryRow(ctx, `SELECT id FROM category WHERE norm_key(name)=norm_key($1)`, in.Name).Scan(&id)
				if errs := items.ValidateCategory(ctx, x.q, &in, id); len(errs) > 0 {
					return bad("%s", strings.Join(errs, " "))
				}
				if x.dup(textclean.Key(in.Name)) {
					return bad("Duplicate row in the file")
				}
				return result{ok: true, msg: map[bool]string{true: "Updates " + in.Name, false: "New category"}[id != 0], obj: &editRow{id, in}}
			},
			commit: func(ctx context.Context, tx pgx.Tx, x *run, objs []any) error {
				for _, o := range objs {
					e := o.(*editRow)
					if _, err := items.SaveCategory(ctx, tx, x.u, e.id, e.obj.(items.CategoryInput)); err != nil {
						return err
					}
				}
				return nil
			},
		},
		"pack_sizes": {
			title: "Pack sizes", module: "Items", allowed: masters,
			required: []string{"pack_size"},
			sample:   "pack_size\nbox of 100\n1 kg pack\npiece\n",
			keyOf:    key("pack_size"),
			onSame:   seenAs("pack_size"),
			export: func(ctx context.Context, q database.Q, r *http.Request) ([]string, [][]string, error) {
				rows, err := rowsOf(ctx, q, `SELECT name FROM pack_size WHERE active ORDER BY name`)
				return []string{"pack_size"}, rows, err
			},
			check: func(ctx context.Context, x *run, row csvfile.Row) result {
				name, problem := items.CheckNamed(ctx, x.q, "pack_size", "Pack size", row.Get("pack_size"), 0)
				if problem != "" {
					return bad("%s", problem)
				}
				if x.dup(textclean.Key(name)) {
					return bad("Duplicate row in the file")
				}
				return result{ok: true, msg: "New pack size", obj: name}
			},
			commit: func(ctx context.Context, tx pgx.Tx, x *run, objs []any) error {
				for _, o := range objs {
					if _, err := items.AddNamed(ctx, tx, x.u, "pack_size", "Pack size", o.(string)); err != nil {
						return err
					}
				}
				return nil
			},
		},
		"sub_categories": {
			title: "Sub-categories", module: "Items", allowed: masters,
			required: []string{"code", "sub_category", "category"},
			sample:   "code,sub_category,category,auto_po\nLN,Linen,Housekeeping,no\nGA,Guest Amenities,Housekeeping,yes\n",
			keyOf:    key("code"),
			onSame: func(ctx context.Context, x *run, row csvfile.Row) {
				x.dup("code" + textclean.Upper(row.Get("code")))
				x.dup(textclean.Key(row.Get("sub_category")))
			},
			export: func(ctx context.Context, q database.Q, r *http.Request) ([]string, [][]string, error) {
				rows, err := rowsOf(ctx, q, `SELECT sc.code, sc.name, c.name, CASE WHEN sc.auto_po THEN 'yes' ELSE 'no' END FROM sub_category sc JOIN category c ON c.id=sc.category_id WHERE sc.active ORDER BY c.name, sc.name`)
				return []string{"code", "sub_category", "category", "auto_po"}, rows, err
			},
			check: func(ctx context.Context, x *run, row csvfile.Row) result {
				in := items.SubCategoryInput{Name: row.Get("sub_category"), Code: row.Get("code")}
				// auto_po is optional: a file without the column, or a blank cell, keeps what is on file.
				if v := row.Get("auto_po"); strings.TrimSpace(v) != "" {
					auto, ok := yesNo(v)
					if !ok {
						return bad("auto_po must be yes or no")
					}
					in.AutoPO = &auto
				}
				if err := x.q.QueryRow(ctx, `SELECT id FROM category WHERE norm_key(name)=norm_key($1)`, row.Get("category")).Scan(&in.CategoryID); err != nil {
					return bad("Unknown category %q. Add it on the Categories sheet (or under Item Setup) first", row.Get("category"))
				}
				var id int64
				var was string
				_ = x.q.QueryRow(ctx, `SELECT id, name FROM sub_category WHERE code=upper(trim($1))`, in.Code).Scan(&id, &was)
				if errs := items.ValidateSubCategory(ctx, x.q, &in, id); len(errs) > 0 {
					return bad("%s", strings.Join(errs, " "))
				}
				if x.dup("code"+in.Code) || x.dup(textclean.Key(in.Name)) {
					return bad("Duplicate row in the file")
				}
				msg := "New sub-category, SKU codes " + in.Code + "-0001 onwards"
				if id != 0 {
					msg = fmt.Sprintf("Updates %s (%s)", was, in.Code)
				}
				return result{ok: true, msg: msg, obj: &editRow{id, in}}
			},
			commit: func(ctx context.Context, tx pgx.Tx, x *run, objs []any) error {
				for _, o := range objs {
					e := o.(*editRow)
					if _, err := items.SaveSubCategory(ctx, tx, x.u, e.id, e.obj.(items.SubCategoryInput)); err != nil {
						return err
					}
				}
				return nil
			},
		},
	}
}

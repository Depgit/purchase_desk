package bulkupload

// Bulk upload for the corporate office: Corporate Setup's departments with their approvers and its expense heads (one
// workbook, heads first), and the lines of a new corporate purchase (checked and handed back to the form, like the
// item list of a property request). Departments are matched by name (rename one on the screen); approvers are named
// by the login they sign in with: email, mobile or employee ID. Expense heads are only added here: an existing name,
// in any spelling, is left as it is, and a new head waits for Finance's approval. The same checks run as on the screens.

import (
	"context"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"purchasedesk/internal/access"
	"purchasedesk/internal/corporate"
	"purchasedesk/internal/csvfile"
	"purchasedesk/internal/database"
	"purchasedesk/internal/indiantax"
	"purchasedesk/internal/textclean"
)

var refCorpPeople = refSheet{"Ref - Users", []string{"name", "login", "employee_id"},
	`SELECT name, coalesce(email::text, mobile, employee_id), employee_id FROM app_user WHERE active ORDER BY name`}

// personByLogin finds an active user by the login they sign in with (email, mobile or employee ID). Blank is 0.
func personByLogin(ctx context.Context, q database.Q, login string) (int64, string) {
	login = strings.TrimSpace(login)
	if login == "" {
		return 0, ""
	}
	var id int64
	if err := q.QueryRow(ctx, `SELECT id FROM app_user WHERE active AND (email=$1::citext OR mobile=$1 OR upper(employee_id)=upper($1))
		ORDER BY (email=$1::citext) IS TRUE DESC, (mobile=$1) IS TRUE DESC LIMIT 1`, login).Scan(&id); err != nil {
		return 0, "No active user signs in with " + login
	}
	return id, ""
}

func (s *Service) corporateKinds() map[string]*kind {
	masters := func(u *access.User) bool { return hasUpload(u) && u.HasRight(access.Masters) }
	return map[string]*kind{
		"expense_heads": {
			title: "Expense heads", module: "Corporate", allowed: masters,
			required: []string{"expense_head"},
			sample:   "expense_head\nTravel and stay\nSoftware subscriptions\n",
			keyOf:    key("expense_head"),
			onSame:   seenAs("expense_head"),
			export: func(ctx context.Context, q database.Q, r *http.Request) ([]string, [][]string, error) {
				rows, err := rowsOf(ctx, q, `SELECT name FROM expense_head WHERE active ORDER BY name`)
				return []string{"expense_head"}, rows, err
			},
			check: func(ctx context.Context, x *run, row csvfile.Row) result {
				name := row.Get("expense_head")
				if x.dup(textclean.Key(name)) {
					return bad("Duplicate row in the file")
				}
				if errs := corporate.ValidateHead(ctx, x.q, &name); len(errs) > 0 {
					return bad("%s", strings.Join(errs, " "))
				}
				return result{ok: true, msg: "New expense head (waits for Finance's approval)", obj: name}
			},
			commit: func(ctx context.Context, tx pgx.Tx, x *run, objs []any) error {
				for _, o := range objs {
					if _, err := corporate.SaveHead(ctx, tx, x.u, o.(string)); err != nil {
						return err
					}
				}
				return nil
			},
		},
		"corp_departments": {
			title: "Departments", module: "Corporate", allowed: masters,
			required: []string{"department", "l1_login", "l2_login"},
			sample:   "department,l1_login,l2_login,backup_login\nAdmin,rakesh.jain@saltstayz.com,anil.sethi@saltstayz.com,\nIT,SS1042,anil.sethi@saltstayz.com,9810000003\n",
			keyOf:    key("department"),
			onSame:   seenAs("department"),
			export: func(ctx context.Context, q database.Q, r *http.Request) ([]string, [][]string, error) {
				login := func(a string) string {
					return `coalesce(` + a + `.email::text, ` + a + `.mobile, ` + a + `.employee_id, '')`
				}
				rows, err := rowsOf(ctx, q, `SELECT d.name, `+login("a")+`, `+login("b")+`, coalesce(`+login("c")+`,'')
					FROM corp_department d JOIN app_user a ON a.id=d.l1_user_id JOIN app_user b ON b.id=d.l2_user_id LEFT JOIN app_user c ON c.id=d.backup_user_id
					WHERE d.active ORDER BY d.name`)
				return []string{"department", "l1_login", "l2_login", "backup_login"}, rows, err
			},
			check: func(ctx context.Context, x *run, row csvfile.Row) result {
				in := corporate.DeptInput{Name: row.Get("department")}
				problems := []string{}
				for _, p := range []struct {
					col string
					to  *int64
				}{{"l1_login", &in.L1ID}, {"l2_login", &in.L2ID}, {"backup_login", &in.BackupID}} {
					id, problem := personByLogin(ctx, x.q, row.Get(p.col))
					if problem != "" {
						problems = append(problems, problem+" ("+p.col+")")
					}
					*p.to = id
				}
				if len(problems) > 0 {
					return bad("%s", strings.Join(problems, ". "))
				}
				var id int64
				_ = x.q.QueryRow(ctx, `SELECT id FROM corp_department WHERE norm_key(name)=norm_key($1)`, in.Name).Scan(&id)
				if errs := corporate.ValidateDepartment(ctx, x.q, &in, id); len(errs) > 0 {
					return bad("%s", strings.Join(errs, " "))
				}
				if x.dup(textclean.Key(in.Name)) {
					return bad("Duplicate row in the file")
				}
				return result{ok: true, msg: map[bool]string{true: "Updates " + in.Name + " (new requests use these approvers)", false: "New department"}[id != 0], obj: &editRow{id, in}}
			},
			commit: func(ctx context.Context, tx pgx.Tx, x *run, objs []any) error {
				for _, o := range objs {
					e := o.(*editRow)
					if _, err := corporate.SaveDepartment(ctx, tx, x.u, e.id, e.obj.(corporate.DeptInput)); err != nil {
						return err
					}
				}
				return nil
			},
		},
		// The lines of a new corporate purchase: checked here, then the form adds them (nothing is saved).
		"corp_lines": {
			title: "Corporate purchase lines", module: "Corporate", allowed: func(u *access.User) bool { return u.HasRight(access.Raise) },
			required: []string{"description", "expense_head", "quantity", "rate", "gst_pct"},
			sample:   "description,expense_head,quantity,unit,rate,gst_pct\nLaptop bag,Office supplies,4,piece,1450,18\nAnnual antivirus licence,Software subscriptions,12,licence,899,18\n",
			check: func(ctx context.Context, x *run, row csvfile.Row) result {
				desc := textclean.Clean(row.Get("description"))
				if desc == "" && row.Get("quantity") == "" {
					return result{skip: true, msg: "Skipped, empty row"}
				}
				if len(desc) < 3 {
					return bad("Describe the item")
				}
				var head int64
				var ok bool
				if err := x.q.QueryRow(ctx, `SELECT id, active AND fin_status='approved' FROM expense_head WHERE norm_key(name)=norm_key($1)`, row.Get("expense_head")).Scan(&head, &ok); err != nil {
					return bad("Unknown expense head %q. Use a name from Corporate Setup, Expense heads", row.Get("expense_head"))
				}
				if !ok {
					return bad("Expense head %q is inactive or still waits for Finance's approval", row.Get("expense_head"))
				}
				qty, okq := num(row.Get("quantity"))
				rate, okr := num(row.Get("rate"))
				if !okq || !okr || qty <= 0 || rate <= 0 {
					return bad("Quantity and rate must be numbers above 0")
				}
				gst, okg := num(row.Get("gst_pct"))
				if !okg || !indiantax.ValidRate(gst) {
					return bad("GST %% must be 0, 5, 12, 18, 28 or 40")
				}
				unit := textclean.Clean(row.Get("unit"))
				if unit == "" {
					unit = "piece"
				}
				return result{ok: true, msg: "Line ready", obj: map[string]any{"description": desc, "expense_head_id": head, "qty": qty, "unit": unit, "rate": rate, "gst_pct": gst}}
			},
		},
	}
}

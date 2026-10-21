package bulkupload

// The approval matrix as an upload: one row per property and item category (or bill type) with its L1 and L2 by
// email, mobile or employee ID. The download lists every row, the empty ones too, so the Admin can fill the gaps in
// Excel; approvalmatrix.Check and Save are the same checks as the Users & Rights screen.

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"purchasedesk/internal/access"
	"purchasedesk/internal/approvalmatrix"
	"purchasedesk/internal/csvfile"
	"purchasedesk/internal/database"
	"purchasedesk/internal/screens"
)

type matrixRow struct {
	cell  approvalmatrix.Cell
	label string
}

func (s *Service) matrixKind() *kind {
	return &kind{
		title: "Approval matrix", module: "Users",
		allowed:  func(u *access.User) bool { return u.IsAdmin || u.Overall("users") >= screens.Edit },
		required: []string{"property_code", "l1_login", "l2_login"},
		sample:   "property_code,category,bill_type,l1_login,l2_login\nP01,Housekeeping,,ravi@example.com,SS1042\nP01,,Electricity,ravi@example.com,SS1042\n",
		keyOf:    key("property_code", "category", "bill_type"),
		onSame:   func(_ context.Context, x *run, row csvfile.Row) { x.dup(matrixKey(row)) },
		export: func(ctx context.Context, q database.Q, r *http.Request) ([]string, [][]string, error) {
			rs, err := approvalmatrix.Rows(ctx, q)
			if err != nil {
				return nil, nil, err
			}
			logins, err := rowsOf(ctx, q, `SELECT id::text, coalesce(email::text, mobile, employee_id, '') FROM app_user`)
			if err != nil {
				return nil, nil, err
			}
			login := map[string]string{}
			for _, l := range logins {
				login[l[0]] = l[1]
			}
			out := [][]string{}
			for _, x := range rs {
				cat, bill := x.Name, ""
				if x.Kind == "bill_type" {
					cat, bill = "", billTypeName(x.Name)
				}
				out = append(out, []string{x.PropertyCode, cat, bill, login[fmt.Sprint(x.L1)], login[fmt.Sprint(x.L2)]})
			}
			return []string{"property_code", "category", "bill_type", "l1_login", "l2_login"}, out, nil
		},
		check: func(ctx context.Context, x *run, row csvfile.Row) result {
			c := approvalmatrix.Cell{}
			if err := x.q.QueryRow(ctx, `SELECT id FROM property WHERE upper(code)=upper(trim($1))`, row.Get("property_code")).Scan(&c.PropertyID); err != nil {
				return bad("Unknown property code %q", row.Get("property_code"))
			}
			cat, bill := strings.TrimSpace(row.Get("category")), strings.TrimSpace(row.Get("bill_type"))
			switch {
			case (cat == "") == (bill == ""):
				return bad("Fill either category or bill_type, not both")
			case cat != "":
				c.Kind = "category"
				if err := x.q.QueryRow(ctx, `SELECT id FROM category WHERE norm_key(name)=norm_key($1)`, cat).Scan(&c.RefID); err != nil {
					return bad("Unknown category %q", cat)
				}
			default:
				c.Kind = "bill_type"
				if err := x.q.QueryRow(ctx, `SELECT id FROM bill_type WHERE norm_key(name)=norm_key($1)`, billTypeName(bill)).Scan(&c.RefID); err != nil {
					return bad("Unknown bill type %q", bill)
				}
			}
			problems := []string{}
			for _, p := range []struct {
				col string
				to  *int64
			}{{"l1_login", &c.L1}, {"l2_login", &c.L2}} {
				id, problem := personByLogin(ctx, x.q, row.Get(p.col))
				if problem != "" {
					problems = append(problems, problem+" ("+p.col+")")
				}
				*p.to = id
			}
			if len(problems) > 0 {
				return bad("%s", strings.Join(problems, ". "))
			}
			label, errs := approvalmatrix.Check(ctx, x.q, c)
			if len(errs) > 0 {
				return bad("%s", strings.Join(errs, " "))
			}
			if x.dup(matrixKey(row)) {
				return bad("Duplicate row in the file")
			}
			msg := "Sets the approvers of " + label
			if c.L1 == 0 || c.L2 == 0 {
				msg += " (still incomplete: requests there are refused until both are set)"
			}
			return result{ok: true, msg: msg, obj: matrixRow{c, label}}
		},
		commit: func(ctx context.Context, tx pgx.Tx, x *run, objs []any) error {
			for _, o := range objs {
				m := o.(matrixRow)
				if err := approvalmatrix.Save(ctx, tx, x.u, m.cell, m.label); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

func matrixKey(row csvfile.Row) string {
	return strings.ToUpper(strings.TrimSpace(row.Get("property_code"))) + "|" + strings.ToLower(strings.TrimSpace(row.Get("category"))) + "|" + strings.ToLower(billTypeName(row.Get("bill_type")))
}

// billTypeName drops the " (utility)" / " (AMC)" the matrix adds to a bill type's name.
func billTypeName(s string) string {
	s = strings.TrimSpace(s)
	for _, suf := range []string{" (utility)", " (AMC)"} {
		s = strings.TrimSuffix(s, suf)
	}
	return s
}

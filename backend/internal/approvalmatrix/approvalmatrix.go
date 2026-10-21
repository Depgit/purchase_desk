// Package approvalmatrix holds who approves what at each property: for every live or pre-opening property and item
// category (purchases), and every property and bill type (utility and AMC bills), the L1 and the L2 approver. A
// request takes its pair when it is raised (purchaserequests, utilitybills). The Users & Rights tab "Approval
// matrix" lists the cells still empty (and the warehouse and C1 / C2 gaps), so the Admin can fill them in.
package approvalmatrix

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"purchasedesk/internal/access"
	"purchasedesk/internal/auditlog"
	"purchasedesk/internal/database"
	"purchasedesk/internal/flowsettings"
	"purchasedesk/internal/httpjson"
	"purchasedesk/internal/screens"
)

type Service struct{ DB *pgxpool.Pool }

func (s *Service) Routes(r chi.Router) {
	r.Get("/approval-matrix", httpjson.Wrap(s.list))
	r.Put("/approval-matrix", httpjson.Wrap(s.save))
}

// Pair is a cell's two approvers (0 = not set).
type Pair struct{ L1, L2 int64 }

// Set reports whether both approvers are named.
func (p Pair) Set() bool { return p.L1 != 0 && p.L2 != 0 }

// ForCategory gives the L1 and L2 for an item category at a property.
func ForCategory(ctx context.Context, q database.Q, propertyID, categoryID int64) Pair {
	return pair(ctx, q, `category_id`, propertyID, categoryID)
}

// ForBillType gives the L1 and L2 for a bill type at a property.
func ForBillType(ctx context.Context, q database.Q, propertyID, billTypeID int64) Pair {
	return pair(ctx, q, `bill_type_id`, propertyID, billTypeID)
}

func pair(ctx context.Context, q database.Q, col string, propertyID, ref int64) Pair {
	var p Pair
	// A person who left (inactive) no longer counts: the cell shows as not set again.
	_ = q.QueryRow(ctx, `SELECT coalesce((SELECT id FROM app_user WHERE id=m.l1_user_id AND active),0), coalesce((SELECT id FROM app_user WHERE id=m.l2_user_id AND active),0)
		FROM approval_matrix m WHERE m.property_id=$1 AND m.`+col+`=$2`, propertyID, ref).Scan(&p.L1, &p.L2)
	return p
}

// Steps turns a cell into approval steps for a request raised by requester: L1 (cl1) then L2 (cl2); one step when
// both are the same person; the requester never approves their own request. problem is set when nobody else is left.
func Steps(p Pair, requester int64) (steps []string, problem string) {
	if p.L1 == p.L2 {
		if p.L1 == requester {
			return nil, "you are both its L1 and its L2"
		}
		return []string{"cl1"}, ""
	}
	if p.L1 != requester {
		steps = append(steps, "cl1")
	}
	if p.L2 != requester {
		steps = append(steps, "cl2")
	}
	return steps, ""
}

// Row is one cell of the matrix as the Users & Rights tab shows it.
type Row struct {
	PropertyID   int64  `json:"property_id"`
	Property     string `json:"property"`
	PropertyCode string `json:"property_code"`
	PropertyKind string `json:"property_kind"`
	ClusterID    int64  `json:"cluster_id"`
	Kind         string `json:"kind"` // category or bill_type
	RefID        int64  `json:"ref_id"`
	Name         string `json:"name"`
	L1           int64  `json:"l1_user_id"`
	L1Name       string `json:"l1"`
	L2           int64  `json:"l2_user_id"`
	L2Name       string `json:"l2"`
}

// Person is someone who may be named in a cell: holds the Approver L1 (or L2) role at those properties.
type Person struct {
	ID         int64   `json:"id"`
	Name       string  `json:"name"`
	Step       string  `json:"step"` // al1 or al2
	All        bool    `json:"all"`
	Clusters   []int64 `json:"cluster_ids"`
	Properties []int64 `json:"property_ids"`
}

func (p Person) covers(propID, clusterID int64) bool {
	if p.All {
		return true
	}
	for _, c := range p.Clusters {
		if c == clusterID {
			return true
		}
	}
	for _, x := range p.Properties {
		if x == propID {
			return true
		}
	}
	return false
}

// Gap is something else still missing for approvals to run: a warehouse sub-category without its L1, a C1 / C2
// limit with nobody to approve it.
type Gap struct {
	What string `json:"what"`
	Fix  string `json:"fix"`
}

func canManage(u *access.User) bool { return u.IsAdmin || u.Overall("users") >= screens.Edit }

// Rows lists every cell: each active live or pre-opening property with each active approved category, and with
// each active bill type.
func Rows(ctx context.Context, q database.Q) ([]Row, error) {
	rs, err := q.Query(ctx, `WITH cells AS (
			SELECT p.id pid, 'category' kind, c.id ref, c.name FROM property p CROSS JOIN category c WHERE c.fin_status='approved'
			UNION ALL SELECT p.id, 'bill_type', b.id, b.name || CASE b.section WHEN 'amc' THEN ' (AMC)' ELSE ' (utility)' END FROM property p CROSS JOIN bill_type b WHERE b.active AND b.fin_status='approved'
		)
		SELECT p.id, p.name, p.code, p.kind, p.cluster_id, x.kind, x.ref, x.name,
			coalesce(u1.id,0), coalesce(u1.name,''), coalesce(u2.id,0), coalesce(u2.name,'')
		FROM cells x JOIN property p ON p.id=x.pid
		LEFT JOIN approval_matrix m ON m.property_id=p.id AND ((x.kind='category' AND m.category_id=x.ref) OR (x.kind='bill_type' AND m.bill_type_id=x.ref))
		LEFT JOIN app_user u1 ON u1.id=m.l1_user_id AND u1.active LEFT JOIN app_user u2 ON u2.id=m.l2_user_id AND u2.active
		WHERE p.active AND p.kind IN ('live','preopening') ORDER BY p.code, x.kind, x.name`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rs, pgx.RowToStructByPos[Row])
}

func people(ctx context.Context, q database.Q) ([]Person, error) {
	rs, err := q.Query(ctx, `SELECT u.id, u.name, r.step, ur.scope='all', ur.cluster_ids, ur.property_ids
		FROM app_user u JOIN user_role ur ON ur.user_id=u.id JOIN role r ON r.id=ur.role_id
		WHERE u.active AND r.active AND r.step IN ('al1','al2') AND 'approve'=ANY(r.rights) ORDER BY u.name`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rs, pgx.RowToStructByPos[Person])
}

// gaps lists what else is missing: warehouse sub-categories without a Warehouse Approver L1, a warehouse without an
// L2, and request types with a C1 / C2 limit but nobody holding that role.
func gaps(ctx context.Context, q database.Q) ([]Gap, error) {
	out := []Gap{}
	rs, err := q.Query(ctx, `SELECT p.name, sc.name FROM property p CROSS JOIN sub_category sc
		WHERE p.active AND p.kind='warehouse' AND sc.active AND sc.fin_status='approved' AND NOT EXISTS (
			SELECT 1 FROM user_role ur JOIN role r ON r.id=ur.role_id JOIN app_user u ON u.id=ur.user_id
			WHERE u.active AND r.active AND r.step='whl1' AND (ur.scope='all' OR (ur.scope='clusters' AND p.cluster_id=ANY(ur.cluster_ids)) OR (ur.scope='properties' AND p.id=ANY(ur.property_ids)))
			AND (cardinality(ur.category_ids)=0 OR sc.category_id=ANY(ur.category_ids)) AND (cardinality(ur.sub_category_ids)=0 OR sc.id=ANY(ur.sub_category_ids)))
		ORDER BY 1, 2`)
	if err != nil {
		return nil, err
	}
	missing := map[string][]string{}
	order := []string{}
	for rs.Next() {
		var p, sc string
		if err := rs.Scan(&p, &sc); err != nil {
			return nil, err
		}
		if _, ok := missing[p]; !ok {
			order = append(order, p)
		}
		missing[p] = append(missing[p], sc)
	}
	rs.Close()
	for _, p := range order {
		out = append(out, Gap{What: fmt.Sprintf("%s: no Warehouse Approver L1 for %s", p, strings.Join(missing[p], ", ")), Fix: "People: give someone the Warehouse Approver L1 role there with these sub-categories ticked"})
	}
	holds := func(step, kind string) bool {
		var ok bool
		_ = q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM property p WHERE p.active AND ($2='' OR p.kind=$2) AND EXISTS (
			SELECT 1 FROM user_role ur JOIN role r ON r.id=ur.role_id JOIN app_user u ON u.id=ur.user_id
			WHERE u.active AND r.active AND r.step=$1 AND (ur.scope='all' OR (ur.scope='clusters' AND p.cluster_id=ANY(ur.cluster_ids)) OR (ur.scope='properties' AND p.id=ANY(ur.property_ids)))))`, step, kind).Scan(&ok)
		return ok
	}
	if !holds("whl2", "warehouse") {
		out = append(out, Gap{What: "The warehouse has no Warehouse Approver L2 (approves warehouse purchases, runs stock and dispatch)", Fix: "People: give someone the Warehouse Approver L2 role"})
	}
	st, err := flowsettings.Load(ctx, q)
	if err != nil {
		return nil, err
	}
	for _, t := range flowsettings.AmountTypes {
		lim := st.AmountApprovals[t.Key]
		for _, c := range []struct {
			step  string
			limit float64
		}{{"c1", lim.C1}, {"c2", lim.C2}} {
			if c.limit > 0 && !holds(c.step, t.PropertyKind) {
				out = append(out, Gap{What: fmt.Sprintf("%s above %s needs %s, but nobody holds that role", t.Label, inr(c.limit), strings.ToUpper(c.step)),
					Fix: "People: give someone the Amount Approver " + strings.ToUpper(c.step) + " role, or change the limit in Flow Settings"})
			}
		}
	}
	return out, nil
}

func inr(v float64) string { return fmt.Sprintf("₹%.0f", v) }

func (s *Service) list(w http.ResponseWriter, r *http.Request) error {
	if !canManage(access.From(r)) {
		return httpjson.Forbidden("Only the System Admin, or a role with Users & Rights = Edit, sets approvers.")
	}
	rs, err := Rows(r.Context(), s.DB)
	if err != nil {
		return err
	}
	ps, err := people(r.Context(), s.DB)
	if err != nil {
		return err
	}
	gs, err := gaps(r.Context(), s.DB)
	if err != nil {
		return err
	}
	return httpjson.OK(w, map[string]any{"rows": rs, "people": ps, "gaps": gs})
}

// Cell sets one row of the matrix; 0 clears an approver.
type Cell struct {
	PropertyID int64  `json:"property_id"`
	Kind       string `json:"kind"` // category or bill_type
	RefID      int64  `json:"ref_id"`
	L1         int64  `json:"l1_user_id"`
	L2         int64  `json:"l2_user_id"`
}

// Check validates a cell the same way for the screen and the upload: a live or pre-opening property, a category or
// bill type, an L1 who holds the Approver L1 role there and an L2 who holds Approver L2. label names the row.
func Check(ctx context.Context, q database.Q, c Cell) (label string, errs []string) {
	var prop, kind string
	var cluster int64
	if err := q.QueryRow(ctx, `SELECT name, kind, cluster_id FROM property WHERE id=$1`, c.PropertyID).Scan(&prop, &kind, &cluster); err != nil || (kind != "live" && kind != "preopening") {
		return "", []string{"Choose a live or pre-opening property."}
	}
	table := map[string]string{"category": "category", "bill_type": "bill_type"}[c.Kind]
	if table == "" {
		return "", []string{"Unknown kind " + c.Kind + "."}
	}
	var name string
	if err := q.QueryRow(ctx, `SELECT name FROM `+table+` WHERE id=$1`, c.RefID).Scan(&name); err != nil {
		return "", []string{"Unknown " + strings.ReplaceAll(table, "_", " ") + "."}
	}
	label = prop + " · " + name
	ps, err := people(ctx, q)
	if err != nil {
		return label, []string{err.Error()}
	}
	holds := func(id int64, step string) bool {
		for _, p := range ps {
			if p.ID == id && p.Step == step && p.covers(c.PropertyID, cluster) {
				return true
			}
		}
		return false
	}
	if c.L1 != 0 && !holds(c.L1, "al1") {
		errs = append(errs, fmt.Sprintf("%s: the L1 must hold the Approver L1 role at %s.", label, prop))
	}
	if c.L2 != 0 && !holds(c.L2, "al2") {
		errs = append(errs, fmt.Sprintf("%s: the L2 must hold the Approver L2 role at %s.", label, prop))
	}
	return label, errs
}

// Save writes a checked cell and logs it.
func Save(ctx context.Context, q database.Q, u *access.User, c Cell, label string) error {
	col := "category_id"
	if c.Kind == "bill_type" {
		col = "bill_type_id"
	}
	nz := func(v int64) any {
		if v == 0 {
			return nil
		}
		return v
	}
	if _, err := q.Exec(ctx, `INSERT INTO approval_matrix (property_id, `+col+`, l1_user_id, l2_user_id, updated_by) VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (property_id, `+col+`) WHERE `+col+` IS NOT NULL DO UPDATE SET l1_user_id=EXCLUDED.l1_user_id, l2_user_id=EXCLUDED.l2_user_id, updated_by=EXCLUDED.updated_by, updated_at=now()`,
		c.PropertyID, c.RefID, nz(c.L1), nz(c.L2), u.ID); err != nil {
		return err
	}
	var n1, n2 string
	_ = q.QueryRow(ctx, `SELECT coalesce((SELECT name FROM app_user WHERE id=$1),'not set'), coalesce((SELECT name FROM app_user WHERE id=$2),'not set')`, c.L1, c.L2).Scan(&n1, &n2)
	return auditlog.Write(ctx, q, u, "Users", "Approvers set", label, "L1 "+n1+", L2 "+n2)
}

// save sets one or several cells (the same L1 and L2 on every ticked row). Requests already raised keep the approvers
// they started with.
func (s *Service) save(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	if !canManage(u) {
		return httpjson.Forbidden("Only the System Admin, or a role with Users & Rights = Edit, sets approvers.")
	}
	var in struct {
		Cells []Cell `json:"cells"`
	}
	if err := httpjson.Decode(r, &in); err != nil {
		return err
	}
	if len(in.Cells) == 0 {
		return httpjson.Bad("Tick at least one row.")
	}
	err := pgx.BeginFunc(r.Context(), s.DB, func(tx pgx.Tx) error {
		errs := []string{}
		for _, c := range in.Cells {
			label, problems := Check(r.Context(), tx, c)
			if len(problems) > 0 {
				errs = append(errs, problems...)
				continue
			}
			if err := Save(r.Context(), tx, u, c, label); err != nil {
				return err
			}
		}
		if len(errs) > 0 {
			return httpjson.Invalid(errs)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return httpjson.OK(w, map[string]string{"message": fmt.Sprintf("Approvers saved for %d row%s. Requests already raised keep the approvers they started with.", len(in.Cells), map[bool]string{true: "", false: "s"}[len(in.Cells) == 1])})
}

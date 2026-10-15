package users

// The access-by-property grid: people down the side, properties across, and the role(s) each person holds at each
// property. A cell can be changed to No access or one role. Only cells that come from a role given for named
// properties can change here; a role given for all properties or for a cluster is changed on the user, so a
// cluster-wide grant is never silently split. Someone who is not the System Admin changes only the users they added,
// only at properties inside their own access (unless their role allows any property) and only to roles they may give.

import (
	"context"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"purchasedesk/internal/access"
	"purchasedesk/internal/auditlog"
	"purchasedesk/internal/httpjson"
	"purchasedesk/internal/textclean"
)

func (s *Service) gridRoutes(r chi.Router) {
	r.Get("/users/access-grid", httpjson.Wrap(s.grid))
	r.Put("/users/{id}/property-access", httpjson.Wrap(s.setPropertyAccess))
}

type gridProp struct {
	ID        int64  `json:"id"`
	Code      string `json:"code"`
	Name      string `json:"name"`
	ClusterID int64  `json:"cluster_id"`
	Mine      bool   `json:"mine"` // the viewer may change access here
}

type gridCell struct {
	Roles    []int64 `json:"roles"`
	Via      string  `json:"via"` // "", properties, clusters, all
	Editable bool    `json:"editable"`
}

type gridRow struct {
	UserID     int64               `json:"user_id"`
	Name       string              `json:"name"`
	EmployeeID string              `json:"employee_id"`
	Department string              `json:"department"`
	Active     bool                `json:"active"`
	Cells      map[string]gridCell `json:"cells"`
}

func (s *Service) gridProps(ctx context.Context, u *access.User) ([]gridProp, error) {
	all, visible, err := s.Guard.Visible(ctx, u)
	if err != nil {
		return nil, err
	}
	rows, err := s.DB.Query(ctx, `SELECT id, code, name, cluster_id, kind FROM property WHERE active ORDER BY code`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []gridProp{}
	for rows.Next() {
		var p gridProp
		var kind string
		if err := rows.Scan(&p.ID, &p.Code, &p.Name, &p.ClusterID, &kind); err != nil {
			return nil, err
		}
		p.Mine = u.IsAdmin || u.UserScopeAny || all || textclean.Contains(visible, p.ID)
		out = append(out, p)
	}
	return out, rows.Err()
}

// cellOf works out a user's roles at one property and where they come from.
func cellOf(roles []RoleGrant, p gridProp) gridCell {
	c := gridCell{Roles: []int64{}}
	for _, g := range roles {
		via := ""
		switch {
		case g.Scope == "all":
			via = "all"
		case g.Scope == "clusters" && textclean.Contains(g.ClusterIDs, p.ClusterID):
			via = "clusters"
		case g.Scope == "properties" && textclean.Contains(g.PropertyIDs, p.ID):
			via = "properties"
		}
		if via == "" {
			continue
		}
		c.Roles = append(c.Roles, g.RoleID)
		if c.Via == "" || c.Via == "properties" {
			c.Via = via
		}
	}
	return c
}

func (s *Service) grid(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	where, args := "", []any{}
	if !u.IsAdmin && !u.HasRight(access.Masters) {
		where, args = "WHERE u.created_by=$1", []any{u.ID}
	}
	users, err := s.load(r.Context(), where, args...)
	if err != nil {
		return err
	}
	props, err := s.gridProps(r.Context(), u)
	if err != nil {
		return err
	}
	if !u.IsAdmin && !u.UserScopeAny {
		mine := []gridProp{}
		for _, p := range props {
			if p.Mine {
				mine = append(mine, p)
			}
		}
		if !u.HasRight(access.Masters) {
			props = mine
		}
	}
	rows := []gridRow{}
	for _, x := range users {
		canUser := u.IsAdmin || (u.AddsUsers() && x.CreatedBy == u.ID)
		row := gridRow{UserID: x.ID, Name: x.Name, EmployeeID: x.EmployeeID, Department: x.Department, Active: x.Active, Cells: map[string]gridCell{}}
		for _, p := range props {
			c := cellOf(x.Roles, p)
			c.Editable = canUser && p.Mine && (c.Via == "" || c.Via == "properties")
			if c.Editable && !u.IsAdmin {
				for _, id := range c.Roles {
					c.Editable = c.Editable && textclean.Contains(u.CreatableRoles, id)
				}
			}
			row.Cells[fmt.Sprint(p.ID)] = c
		}
		rows = append(rows, row)
	}
	return httpjson.OK(w, map[string]any{"properties": props, "rows": rows})
}

// setPropertyAccess gives a user exactly one role at one property (role_id), or takes their access there away (0).
func (s *Service) setPropertyAccess(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	if !u.AddsUsers() {
		return httpjson.Forbidden("Only the System Admin, or a role that may add users, can change access.")
	}
	id, err := httpjson.ID(r, "id")
	if err != nil {
		return err
	}
	var in struct {
		PropertyID int64 `json:"property_id"`
		RoleID     int64 `json:"role_id"`
	}
	if err := httpjson.Decode(r, &in); err != nil {
		return err
	}
	if err := Manages(r.Context(), s.DB, u, id); err != nil {
		return err
	}
	props, err := s.gridProps(r.Context(), u)
	if err != nil {
		return err
	}
	var p *gridProp
	for i := range props {
		if props[i].ID == in.PropertyID {
			p = &props[i]
		}
	}
	if p == nil {
		return httpjson.Bad("Choose an active property.")
	}
	if !p.Mine {
		return httpjson.Forbidden("%s is outside your own properties.", p.Name)
	}
	var roleName, step string
	var roleActive bool
	if in.RoleID != 0 {
		if err := s.DB.QueryRow(r.Context(), `SELECT name, step, active FROM role WHERE id=$1`, in.RoleID).Scan(&roleName, &step, &roleActive); err != nil || !roleActive {
			return httpjson.Bad("Choose an active role.")
		}
		if !u.IsAdmin && !textclean.Contains(u.CreatableRoles, in.RoleID) {
			return httpjson.Forbidden("Your role cannot give %s.", roleName)
		}
	}
	var msg string
	err = pgx.BeginFunc(r.Context(), s.DB, func(tx pgx.Tx) error {
		users, err := s.load(r.Context(), `WHERE u.id=$1`, id)
		if err != nil {
			return err
		}
		if len(users) == 0 {
			return httpjson.NotFound("User")
		}
		x := users[0]
		if _, err := tx.Exec(r.Context(), `SELECT 1 FROM user_role WHERE user_id=$1 FOR UPDATE`, id); err != nil {
			return err
		}
		c := cellOf(x.Roles, *p)
		if c.Via == "all" || c.Via == "clusters" {
			return httpjson.Conflict("%s has access to %s through a role given for %s. Change it on the user.", x.Name, p.Name, map[string]string{"all": "all properties", "clusters": "a cluster"}[c.Via])
		}
		before := []string{}
		left := 0
		for _, g := range x.Roles {
			if !u.IsAdmin && textclean.Contains(c.Roles, g.RoleID) && !textclean.Contains(u.CreatableRoles, g.RoleID) {
				return httpjson.Forbidden("%s holds %s at %s, which your role cannot change.", x.Name, g.RoleName, p.Name)
			}
			ids := g.PropertyIDs
			if g.Scope == "properties" && textclean.Contains(ids, p.ID) {
				before = append(before, g.RoleName)
				rest := []int64{}
				for _, v := range ids {
					if v != p.ID {
						rest = append(rest, v)
					}
				}
				ids = rest
				if len(rest) == 0 {
					if _, err := tx.Exec(r.Context(), `DELETE FROM user_role WHERE user_id=$1 AND role_id=$2`, id, g.RoleID); err != nil {
						return err
					}
				} else if _, err := tx.Exec(r.Context(), `UPDATE user_role SET property_ids=$3 WHERE user_id=$1 AND role_id=$2`, id, g.RoleID, rest); err != nil {
					return err
				}
			}
			if g.Scope != "properties" || len(ids) > 0 {
				left++
			}
		}
		if in.RoleID != 0 {
			var scope string
			var ids []int64
			err := tx.QueryRow(r.Context(), `SELECT scope, property_ids FROM user_role WHERE user_id=$1 AND role_id=$2`, id, in.RoleID).Scan(&scope, &ids)
			switch {
			case err == pgx.ErrNoRows:
				if _, err := tx.Exec(r.Context(), `INSERT INTO user_role(user_id,role_id,scope,property_ids,created_by) VALUES ($1,$2,'properties',$3,$4)`,
					id, in.RoleID, []int64{p.ID}, u.ID); err != nil {
					return err
				}
				left++
			case err != nil:
				return err
			case scope == "properties":
				if _, err := tx.Exec(r.Context(), `UPDATE user_role SET property_ids=$3 WHERE user_id=$1 AND role_id=$2`, id, in.RoleID, append(ids, p.ID)); err != nil {
					return err
				}
			default:
				return httpjson.Conflict("%s already holds %s for %s, which does not include %s. Change it on the user.", x.Name, roleName,
					map[string]string{"all": "all properties", "clusters": "other clusters"}[scope], p.Name)
			}
		}
		if left == 0 {
			return httpjson.Bad("%s would have no role left anywhere. Mark the user inactive instead.", x.Name)
		}
		now := textclean.Default(roleName, "no access")
		msg = fmt.Sprintf("%s at %s: %s", x.Name, p.Name, now)
		return auditlog.Write(r.Context(), tx, u, "Users", "Property access changed", x.Name,
			fmt.Sprintf("%s: %s → %s", p.Code, textclean.Default(joinNames(before), "no access"), now))
	})
	if err != nil {
		return err
	}
	return httpjson.OK(w, map[string]any{"message": msg})
}

func joinNames(xs []string) string {
	out := ""
	for i, x := range xs {
		if i > 0 {
			out += " + "
		}
		out += x
	}
	return out
}

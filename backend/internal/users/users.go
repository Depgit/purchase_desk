// Package users manages who can sign in, their roles, and each role's property and category scope.
// It also manages roles: a role is a named bundle of rights that sits at one step in the flow.
// The System Admin manages every user. A role can carry the create-users right (roles.go): people in it add users,
// giving only the roles the role allows, inside their own properties or at any property, and they see, edit and
// deactivate only the users they added. The access-by-property grid (accessgrid.go) changes a user's role per property.
package users

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
	"purchasedesk/internal/httpjson"
	"purchasedesk/internal/indiantax"
	"purchasedesk/internal/passwords"
	"purchasedesk/internal/releasephase"
	"purchasedesk/internal/textclean"
)

type Service struct {
	DB    *pgxpool.Pool
	Guard *access.Guard
}

func (s *Service) Routes(r chi.Router) {
	r.Get("/people", httpjson.Wrap(s.people))
	r.Get("/users", httpjson.Wrap(s.list))
	r.Post("/users", httpjson.Wrap(s.save))
	r.Put("/users/{id}", httpjson.Wrap(s.save))
	s.roleRoutes(r)
	s.gridRoutes(r)
}

type RoleGrant struct {
	RoleID      int64   `json:"role_id"`
	RoleName    string  `json:"role_name,omitempty"`
	Step        string  `json:"step,omitempty"`
	Scope       string  `json:"scope"`
	ClusterIDs  []int64 `json:"cluster_ids"`
	PropertyIDs []int64 `json:"property_ids"`
	CategoryIDs []int64 `json:"category_ids"`
}

type User struct {
	ID         int64       `json:"id"`
	Name       string      `json:"name"`
	Email      string      `json:"email"`
	Mobile     string      `json:"mobile"`
	Department string      `json:"department"`
	HasPwd     bool        `json:"has_password"`
	Active     bool        `json:"active"`
	Roles      []RoleGrant `json:"roles"`
	CreatedBy  int64       `json:"created_by"`
	CanEdit    bool        `json:"can_edit"` // the viewer may edit or deactivate this user
}

func (s *Service) load(ctx context.Context, where string, args ...any) ([]User, error) {
	rows, err := s.DB.Query(ctx, `SELECT u.id, u.name, coalesce(u.email::text,''), coalesce(u.mobile,''), u.department, u.password_hash IS NOT NULL, u.active,
		coalesce(u.created_by,0)
		FROM app_user u `+where+` ORDER BY u.name`, args...)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (User, error) {
		var x User
		x.Roles = []RoleGrant{}
		return x, row.Scan(&x.ID, &x.Name, &x.Email, &x.Mobile, &x.Department, &x.HasPwd, &x.Active, &x.CreatedBy)
	})
	if err != nil {
		return nil, err
	}
	idx := map[int64]int{}
	for i, u := range out {
		idx[u.ID] = i
	}
	gr, err := s.DB.Query(ctx, `SELECT ur.user_id, ur.role_id, r.name, r.step, ur.scope, ur.cluster_ids, ur.property_ids, ur.category_ids FROM user_role ur JOIN role r ON r.id=ur.role_id ORDER BY ur.id`)
	if err != nil {
		return nil, err
	}
	defer gr.Close()
	for gr.Next() {
		var uid int64
		var g RoleGrant
		if err := gr.Scan(&uid, &g.RoleID, &g.RoleName, &g.Step, &g.Scope, &g.ClusterIDs, &g.PropertyIDs, &g.CategoryIDs); err != nil {
			return nil, err
		}
		if i, ok := idx[uid]; ok {
			out[i].Roles = append(out[i].Roles, g)
		}
	}
	return out, gr.Err()
}

// list gives the System Admin and the Purchase Team every user. Someone allowed to add users sees the users they
// added (and, with the masters right, everyone, but edits only their own).
func (s *Service) list(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	if !u.IsAdmin && !u.HasRight(access.Masters) && !u.AddsUsers() {
		return httpjson.Forbidden("Only the System Admin and the Purchase Team can see the user list.")
	}
	where, args := "", []any{}
	if !u.IsAdmin && !u.HasRight(access.Masters) {
		where, args = "WHERE u.created_by=$1", []any{u.ID}
	}
	out, err := s.load(r.Context(), where, args...)
	if err != nil {
		return err
	}
	for i := range out {
		out[i].CanEdit = u.IsAdmin || (u.AddsUsers() && out[i].CreatedBy == u.ID)
	}
	return httpjson.OK(w, out)
}

// Manages refuses unless u may edit or deactivate user id: the System Admin always; someone allowed to add users,
// only for the users they added.
func Manages(ctx context.Context, q database.Q, u *access.User, id int64) error {
	if u.IsAdmin {
		return nil
	}
	if !u.AddsUsers() {
		return httpjson.Forbidden("Only the System Admin can do this.")
	}
	var by int64
	_ = q.QueryRow(ctx, `SELECT coalesce(created_by,0) FROM app_user WHERE id=$1`, id).Scan(&by)
	if by != u.ID {
		return httpjson.Forbidden("You can change only the users you added. Ask the System Admin for anyone else.")
	}
	return nil
}

type Person struct {
	ID     int64    `json:"id"`
	Name   string   `json:"name"`
	Active bool     `json:"active"`
	Steps  []string `json:"steps"`
}

// people is the short list used in pickers (Cluster Manager, Pre-opening approver, department approvers).
func (s *Service) people(w http.ResponseWriter, r *http.Request) error {
	rows, err := s.DB.Query(r.Context(), `SELECT u.id, u.name, u.active, coalesce(array_agg(DISTINCT r.step) FILTER (WHERE r.id IS NOT NULL),'{}')
		FROM app_user u LEFT JOIN user_role ur ON ur.user_id=u.id LEFT JOIN role r ON r.id=ur.role_id AND r.active GROUP BY u.id ORDER BY u.name`)
	if err != nil {
		return err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Person, error) {
		var p Person
		return p, row.Scan(&p.ID, &p.Name, &p.Active, &p.Steps)
	})
	if err != nil {
		return err
	}
	return httpjson.OK(w, out)
}

// UserInput is a user from the form or an upload row.
type UserInput struct {
	Name       string      `json:"name"`
	Email      string      `json:"email"`
	Mobile     string      `json:"mobile"`
	Department string      `json:"department"`
	Password   string      `json:"password"`
	Roles      []RoleGrant `json:"roles"`
}

// ValidateUser tidies the input and returns every problem. id is the user being edited, or 0; selfID is the Admin doing it.
func ValidateUser(ctx context.Context, q database.Q, in *UserInput, id, selfID int64) []string {
	in.Name, in.Department = textclean.Clean(in.Name), textclean.Clean(in.Department)
	in.Email, in.Mobile = strings.ToLower(strings.TrimSpace(in.Email)), strings.TrimSpace(in.Mobile)
	errs := []string{}
	if len(in.Name) < 3 {
		errs = append(errs, "Enter the name.")
	}
	if in.Email == "" && in.Mobile == "" {
		errs = append(errs, "Enter an email or a mobile number to sign in with.")
	}
	if in.Email != "" && !indiantax.Email.MatchString(in.Email) {
		errs = append(errs, "Enter a valid email.")
	}
	if in.Mobile != "" && !indiantax.Mobile.MatchString(in.Mobile) {
		errs = append(errs, "Mobile number must be 10 digits.")
	}
	if in.Department == "" {
		errs = append(errs, "Enter the department.")
	}
	if in.Password != "" && len(in.Password) < 8 {
		errs = append(errs, "Password must be at least 8 characters.")
	}
	if len(in.Roles) == 0 {
		errs = append(errs, "Give the user at least one role.")
	}
	seen := map[int64]bool{}
	hasAdmin := false
	for i := range in.Roles {
		g := &in.Roles[i]
		if seen[g.RoleID] {
			errs = append(errs, "A role is listed twice.")
		}
		seen[g.RoleID] = true
		var step string
		var active bool
		if err := q.QueryRow(ctx, `SELECT step, active FROM role WHERE id=$1`, g.RoleID).Scan(&step, &active); err != nil || !active {
			errs = append(errs, "Choose active roles.")
		} else if !releasephase.RoleOn(step) {
			errs = append(errs, "Warehouse and corporate roles open in a later phase of the rollout.")
		}
		hasAdmin = hasAdmin || step == "admin"
		if g.Scope == "" {
			g.Scope = "all"
		}
		if !textclean.Contains([]string{"all", "clusters", "properties"}, g.Scope) {
			errs = append(errs, "Property access is all properties, clusters, or named properties.")
		}
		if g.Scope == "clusters" && len(g.ClusterIDs) == 0 || g.Scope == "properties" && len(g.PropertyIDs) == 0 {
			errs = append(errs, "Pick the clusters or properties for each limited role.")
		}
		if g.ClusterIDs == nil || g.Scope != "clusters" {
			g.ClusterIDs = []int64{}
		}
		if g.PropertyIDs == nil || g.Scope != "properties" {
			g.PropertyIDs = []int64{}
		}
		if g.CategoryIDs == nil {
			g.CategoryIDs = []int64{}
		}
	}
	var dup bool
	_ = q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM app_user WHERE id<>$3 AND (($1<>'' AND email=$1::citext) OR ($2<>'' AND mobile=$2)))`, in.Email, in.Mobile, id).Scan(&dup)
	if dup {
		errs = append(errs, "A user with this email or mobile already exists.")
	}
	if id != 0 && id == selfID && !hasAdmin {
		errs = append(errs, "You cannot remove your own System Admin role.")
	}
	return errs
}

// SaveUser creates (id 0) or updates a validated user and replaces their roles.
func SaveUser(ctx context.Context, tx pgx.Tx, u *access.User, id int64, in UserInput) (int64, error) {
	var hash *string
	if in.Password != "" {
		h, err := passwords.Hash(in.Password)
		if err != nil {
			return 0, err
		}
		hash = &h
	}
	email, mobile := any(nil), any(nil)
	if in.Email != "" {
		email = in.Email
	}
	if in.Mobile != "" {
		mobile = in.Mobile
	}
	act := "User changed"
	if id == 0 {
		act = "User added"
		if err := tx.QueryRow(ctx, `INSERT INTO app_user(name,email,mobile,department,password_hash,created_by) VALUES ($1,$2,$3,$4,$5,$6) RETURNING id`,
			in.Name, email, mobile, in.Department, hash, u.ID).Scan(&id); err != nil {
			return 0, err
		}
	} else {
		if _, err := tx.Exec(ctx, `UPDATE app_user SET name=$2, email=$3, mobile=$4, department=$5, password_hash=coalesce($6,password_hash) WHERE id=$1`,
			id, in.Name, email, mobile, in.Department, hash); err != nil {
			return 0, err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM user_role WHERE user_id=$1`, id); err != nil {
			return 0, err
		}
	}
	names := []string{}
	for _, g := range in.Roles {
		var name string
		if err := tx.QueryRow(ctx, `INSERT INTO user_role(user_id,role_id,scope,cluster_ids,property_ids,category_ids,created_by) VALUES ($1,$2,$3,$4,$5,$6,$7)
			RETURNING (SELECT name FROM role WHERE id=$2)`, id, g.RoleID, g.Scope, g.ClusterIDs, g.PropertyIDs, g.CategoryIDs, u.ID).Scan(&name); err != nil {
			return 0, err
		}
		names = append(names, name+" ("+g.Scope+")")
	}
	detail := strings.Join(names, " + ")
	if hash != nil {
		detail += " · password set"
	}
	return id, auditlog.Write(ctx, tx, u, "Users", act, in.Name, detail)
}

func (s *Service) save(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	if !u.AddsUsers() {
		return httpjson.Forbidden("Only the System Admin, or someone the Admin lets add users, can do this.")
	}
	var in UserInput
	if err := httpjson.Decode(r, &in); err != nil {
		return err
	}
	id := int64(0)
	if chi.URLParam(r, "id") != "" {
		var err error
		if id, err = httpjson.ID(r, "id"); err != nil {
			return err
		}
		if err := Manages(r.Context(), s.DB, u, id); err != nil {
			return err
		}
	}
	errs := ValidateUser(r.Context(), s.DB, &in, id, u.ID)
	if !u.IsAdmin {
		errs = append(errs, s.delegateLimits(r.Context(), u, in.Roles)...)
	}
	if len(errs) > 0 {
		return httpjson.Invalid(errs)
	}
	err := pgx.BeginFunc(r.Context(), s.DB, func(tx pgx.Tx) error {
		var err error
		id, err = SaveUser(r.Context(), tx, u, id, in)
		return err
	})
	if err != nil {
		return err
	}
	return httpjson.OK(w, map[string]int64{"id": id})
}

// delegateLimits checks the roles a non-Admin gives: only the roles their role allows, and, unless their role may add
// users at any property, property access inside their own (all properties only if they have all; a cluster only if
// they hold that cluster; named properties only ones they can see).
func (s *Service) delegateLimits(ctx context.Context, u *access.User, roles []RoleGrant) []string {
	errs := []string{}
	all, visible, err := s.Guard.Visible(ctx, u)
	if err != nil {
		return []string{err.Error()}
	}
	clusters := []int64{}
	for _, g := range u.Grants {
		if g.Scope == "clusters" {
			clusters = append(clusters, g.ClusterIDs...)
		}
	}
	for _, g := range roles {
		var name string
		_ = s.DB.QueryRow(ctx, `SELECT name FROM role WHERE id=$1`, g.RoleID).Scan(&name)
		if !textclean.Contains(u.CreatableRoles, g.RoleID) {
			var can string
			_ = s.DB.QueryRow(ctx, `SELECT string_agg(name, ', ' ORDER BY id) FROM role WHERE id = ANY($1)`, u.CreatableRoles).Scan(&can)
			errs = append(errs, fmt.Sprintf("You cannot give the role %s. You may add users as: %s.", textclean.Default(name, "chosen"), can))
			continue
		}
		if all || u.UserScopeAny {
			continue
		}
		switch g.Scope {
		case "all":
			errs = append(errs, fmt.Sprintf("%s: you can give only the properties you have access to, not all properties.", name))
		case "clusters":
			for _, c := range g.ClusterIDs {
				if !textclean.Contains(clusters, c) {
					errs = append(errs, fmt.Sprintf("%s: you can give only the clusters you have access to.", name))
					break
				}
			}
		case "properties":
			for _, p := range g.PropertyIDs {
				if !textclean.Contains(visible, p) {
					errs = append(errs, fmt.Sprintf("%s: you can give only the properties you have access to.", name))
					break
				}
			}
		}
	}
	return errs
}

// Package users manages who can sign in, their roles, and each role's property and category scope.
// It also manages roles: a role is a named bundle of rights that sits at one step in the flow.
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

type Service struct{ DB *pgxpool.Pool }

func (s *Service) Routes(r chi.Router) {
	r.Get("/people", httpjson.Wrap(s.people))
	r.Get("/users", httpjson.Wrap(s.list))
	r.Post("/users", httpjson.Wrap(s.save))
	r.Put("/users/{id}", httpjson.Wrap(s.save))
	r.Get("/roles", httpjson.Wrap(s.listRoles))
	r.Post("/roles", httpjson.Wrap(s.saveRole))
	r.Put("/roles/{id}", httpjson.Wrap(s.saveRole))
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
}

func (s *Service) load(ctx context.Context, where string, args ...any) ([]User, error) {
	rows, err := s.DB.Query(ctx, `SELECT u.id, u.name, coalesce(u.email::text,''), coalesce(u.mobile,''), u.department, u.password_hash IS NOT NULL, u.active
		FROM app_user u `+where+` ORDER BY u.name`, args...)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (User, error) {
		var x User
		x.Roles = []RoleGrant{}
		return x, row.Scan(&x.ID, &x.Name, &x.Email, &x.Mobile, &x.Department, &x.HasPwd, &x.Active)
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

func (s *Service) list(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	if !u.IsAdmin && !u.HasRight(access.Masters) {
		return httpjson.Forbidden("Only the System Admin and the Purchase Team can see the user list.")
	}
	out, err := s.load(r.Context(), "")
	if err != nil {
		return err
	}
	return httpjson.OK(w, out)
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
	if err := access.NeedAdmin(u); err != nil {
		return err
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
	}
	if errs := ValidateUser(r.Context(), s.DB, &in, id, u.ID); len(errs) > 0 {
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

type Role struct {
	ID       int64    `json:"id"`
	Code     string   `json:"code"`
	Name     string   `json:"name"`
	Step     string   `json:"step"`
	Rights   []string `json:"rights"`
	IsCustom bool     `json:"is_custom"`
	Active   bool     `json:"active"`
	Users    int      `json:"users"`
}

func (s *Service) listRoles(w http.ResponseWriter, r *http.Request) error {
	rows, err := s.DB.Query(r.Context(), `SELECT r.id, r.code, r.name, r.step, r.rights, r.is_custom, r.active,
		(SELECT count(*) FROM user_role ur JOIN app_user u ON u.id=ur.user_id WHERE ur.role_id=r.id AND u.active) FROM role r ORDER BY r.id`)
	if err != nil {
		return err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Role, error) {
		var x Role
		return x, row.Scan(&x.ID, &x.Code, &x.Name, &x.Step, &x.Rights, &x.IsCustom, &x.Active, &x.Users)
	})
	if err != nil {
		return err
	}
	shown := out[:0]
	for _, x := range out {
		if releasephase.RoleOn(x.Step) { // roles of later phases stay hidden until RELEASE_PHASE reaches them
			shown = append(shown, x)
		}
	}
	return httpjson.OK(w, shown)
}

func (s *Service) saveRole(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	if err := access.NeedAdmin(u); err != nil {
		return err
	}
	var in struct {
		Name   string   `json:"name"`
		Step   string   `json:"step"`
		Rights []string `json:"rights"`
	}
	if err := httpjson.Decode(r, &in); err != nil {
		return err
	}
	in.Name = textclean.Clean(in.Name)
	rights := []string{}
	for _, x := range in.Rights {
		if !textclean.Contains(access.AllRights, x) {
			return httpjson.Bad("Unknown right %s.", x)
		}
		rights = append(rights, x)
	}
	rights = textclean.Uniq(rights)
	if !textclean.Contains(access.Steps, in.Step) || in.Step == "admin" {
		return httpjson.Bad("Choose where the role works in the flow.")
	}
	id := int64(0)
	if chi.URLParam(r, "id") != "" {
		var err error
		if id, err = httpjson.ID(r, "id"); err != nil {
			return err
		}
		var code, step string
		var custom bool
		if err := s.DB.QueryRow(r.Context(), `SELECT code, step, is_custom FROM role WHERE id=$1`, id).Scan(&code, &step, &custom); err != nil {
			return err
		}
		if step == "admin" {
			return httpjson.Bad("The System Admin always has every right.")
		}
		if !custom {
			in.Step = step // standard roles keep their place in the flow
		}
		if in.Name == "" {
			_ = s.DB.QueryRow(r.Context(), `SELECT name FROM role WHERE id=$1`, id).Scan(&in.Name)
		}
		if _, err := s.DB.Exec(r.Context(), `UPDATE role SET name=$2, step=$3, rights=$4 WHERE id=$1`, id, in.Name, in.Step, rights); err != nil {
			return err
		}
		_ = auditlog.Write(r.Context(), s.DB, u, "Users", "Role rights changed", in.Name, strings.Join(rights, ", "))
		return httpjson.OK(w, map[string]int64{"id": id})
	}
	if len(in.Name) < 3 {
		return httpjson.Bad("Enter the role name.")
	}
	if len(rights) == 0 && in.Step != "" {
		return httpjson.Bad(`Tick at least one right, or choose "No step in the flow".`)
	}
	var dup bool
	_ = s.DB.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM role WHERE norm_key(name)=norm_key($1))`, in.Name).Scan(&dup)
	if dup {
		return httpjson.Bad("A role with this name already exists.")
	}
	var n int
	_ = s.DB.QueryRow(r.Context(), `SELECT count(*)+1 FROM role WHERE is_custom`).Scan(&n)
	if err := s.DB.QueryRow(r.Context(), `INSERT INTO role(code,name,step,rights,is_custom,created_by) VALUES ($1,$2,$3,$4,TRUE,$5) RETURNING id`,
		fmt.Sprintf("custom%d", n), in.Name, in.Step, rights, u.ID).Scan(&id); err != nil {
		return err
	}
	_ = auditlog.Write(r.Context(), s.DB, u, "Users", "Role created", in.Name, "works as "+textclean.Default(in.Step, "view only")+" · "+strings.Join(rights, ", "))
	return httpjson.OK(w, map[string]int64{"id": id})
}

package users

// Roles: a role is a named bundle of action rights at one step of the flow, a level on every screen (none, view,
// edit), the reports it sees, and two special rights: adding users (which roles it may give, own properties or any)
// and managing roles. Roles sit in groups. The System Admin manages everything and is the only one who grants the
// manage-roles right; someone with that right never gives a level, right or report above their own.

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"purchasedesk/internal/access"
	"purchasedesk/internal/auditlog"
	"purchasedesk/internal/database"
	"purchasedesk/internal/httpjson"
	"purchasedesk/internal/reports"
	"purchasedesk/internal/screens"
	"purchasedesk/internal/textclean"
)

func (s *Service) roleRoutes(r chi.Router) {
	r.Get("/roles", httpjson.Wrap(s.listRoles))
	r.Get("/roles/catalogue", httpjson.Wrap(s.roleCatalogue))
	r.Post("/roles", httpjson.Wrap(s.createRole))
	r.Put("/roles/{id}", httpjson.Wrap(s.updateRole))
	r.Post("/roles/{id}/copy", httpjson.Wrap(s.copyRole))
	r.Get("/role-groups", httpjson.Wrap(s.listGroups))
	r.Post("/role-groups", httpjson.Wrap(s.saveGroup))
	r.Put("/role-groups/{id}", httpjson.Wrap(s.saveGroup))
}

type Role struct {
	ID        int64             `json:"id"`
	Code      string            `json:"code"`
	Name      string            `json:"name"`
	Step      string            `json:"step"`
	Rights    []string          `json:"rights"`
	IsCustom  bool              `json:"is_custom"`
	Active    bool              `json:"active"`
	Users     int               `json:"users"`
	GroupID   *int64            `json:"group_id"`
	Group     string            `json:"group"`
	Screens   map[string]string `json:"screens"` // level on every screen, set or starting
	Reports   []string          `json:"reports"` // null: every report
	CanUsers  bool              `json:"can_users"`
	UserRoles []int64           `json:"user_role_ids"`
	UserScope string            `json:"user_scope"`
	CanRoles  bool              `json:"can_roles"`
	set       map[string]string // levels stored on the role
}

func (x *Role) effective() map[string]int {
	return screens.Effective(x.set, x.Rights, x.Step, x.CanUsers, x.CanRoles)
}

const roleSelect = `SELECT r.id, r.code, r.name, r.step, r.rights, r.is_custom, r.active,
	(SELECT count(*) FROM user_role ur JOIN app_user u ON u.id=ur.user_id WHERE ur.role_id=r.id AND u.active),
	r.group_id, coalesce(g.name,''), r.screens, r.reports, r.can_users, r.user_role_ids, r.user_scope, r.can_roles
	FROM role r LEFT JOIN role_group g ON g.id=r.group_id `

func loadRoles(ctx context.Context, q database.Q, where string, args ...any) ([]Role, error) {
	rows, err := q.Query(ctx, roleSelect+where, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Role, error) {
		var x Role
		err := row.Scan(&x.ID, &x.Code, &x.Name, &x.Step, &x.Rights, &x.IsCustom, &x.Active, &x.Users, &x.GroupID, &x.Group,
			&x.set, &x.Reports, &x.CanUsers, &x.UserRoles, &x.UserScope, &x.CanRoles)
		x.Screens = map[string]string{}
		for k, v := range x.effective() {
			x.Screens[k] = screens.LevelName[v]
		}
		return x, err
	})
}

func (s *Service) listRoles(w http.ResponseWriter, r *http.Request) error {
	out, err := loadRoles(r.Context(), s.DB, `ORDER BY coalesce(g.sort, 99), r.id`)
	if err != nil {
		return err
	}
	return httpjson.OK(w, out)
}

// roleCatalogue gives the role editor its lists: every screen, every report, the action rights and the flow steps a
// custom role may sit at.
func (s *Service) roleCatalogue(w http.ResponseWriter, r *http.Request) error {
	return httpjson.OK(w, map[string]any{"screens": screens.All, "reports": reports.List(), "rights": access.AllRights})
}

// group helpers

type Group struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Sort  int    `json:"sort"`
	Roles int    `json:"roles"`
}

func (s *Service) listGroups(w http.ResponseWriter, r *http.Request) error {
	rows, err := s.DB.Query(r.Context(), `SELECT g.id, g.name, g.sort, (SELECT count(*) FROM role r WHERE r.group_id=g.id) FROM role_group g ORDER BY g.sort, g.id`)
	if err != nil {
		return err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Group, error) {
		var x Group
		return x, row.Scan(&x.ID, &x.Name, &x.Sort, &x.Roles)
	})
	if err != nil {
		return err
	}
	return httpjson.OK(w, out)
}

// saveGroup adds a role group, or renames one.
func (s *Service) saveGroup(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	if !u.ManagesRoles() {
		return httpjson.Forbidden("Only the System Admin, or a role allowed to manage roles, can change role groups.")
	}
	var in struct {
		Name string `json:"name"`
	}
	if err := httpjson.Decode(r, &in); err != nil {
		return err
	}
	in.Name = textclean.Clean(in.Name)
	if len(in.Name) < 2 {
		return httpjson.Bad("Enter the group name.")
	}
	id := int64(0)
	if chi.URLParam(r, "id") != "" {
		var err error
		if id, err = httpjson.ID(r, "id"); err != nil {
			return err
		}
	}
	var dup bool
	_ = s.DB.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM role_group WHERE norm_key(name)=norm_key($1) AND id<>$2)`, in.Name, id).Scan(&dup)
	if dup {
		return httpjson.Bad("A role group with this name already exists.")
	}
	if id == 0 {
		if err := s.DB.QueryRow(r.Context(), `INSERT INTO role_group(name, sort) VALUES ($1, (SELECT coalesce(max(sort),0)+1 FROM role_group)) RETURNING id`, in.Name).Scan(&id); err != nil {
			return err
		}
		_ = auditlog.Write(r.Context(), s.DB, u, "Users", "Role group added", in.Name, "")
	} else {
		ct, err := s.DB.Exec(r.Context(), `UPDATE role_group SET name=$2 WHERE id=$1`, id, in.Name)
		if err != nil {
			return err
		}
		if ct.RowsAffected() == 0 {
			return httpjson.NotFound("Role group")
		}
		_ = auditlog.Write(r.Context(), s.DB, u, "Users", "Role group renamed", in.Name, "")
	}
	return httpjson.OK(w, map[string]any{"id": id, "message": "Group saved"})
}

// roleIn is a role from the editor. Fields left out (null) keep what is on file.
type roleIn struct {
	Name       *string            `json:"name"`
	Step       *string            `json:"step"`
	Rights     *[]string          `json:"rights"`
	GroupID    *int64             `json:"group_id"`
	Screens    *map[string]string `json:"screens"`
	Reports    *[]string          `json:"reports"`
	AllReports *bool              `json:"all_reports"`
	CanUsers   *bool              `json:"can_users"`
	UserRoles  *[]int64           `json:"user_role_ids"`
	UserScope  *string            `json:"user_scope"`
	CanRoles   *bool              `json:"can_roles"`
}

// apply puts the editor's changes on a copy of x, checking each value. It returns the changed role and what changed.
func (s *Service) apply(ctx context.Context, q database.Q, x Role, in roleIn) (Role, []string, error) {
	changed := []string{}
	if in.Name != nil {
		n := textclean.Clean(*in.Name)
		if len(n) < 3 {
			return x, nil, httpjson.Bad("Enter the role name.")
		}
		var dup bool
		_ = q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM role WHERE norm_key(name)=norm_key($1) AND id<>$2)`, n, x.ID).Scan(&dup)
		if dup {
			return x, nil, httpjson.Bad("A role with this name already exists.")
		}
		if n != x.Name {
			changed = append(changed, "name")
		}
		x.Name = n
	}
	if in.Step != nil && x.IsCustom {
		if !textclean.Contains(access.Steps, *in.Step) || *in.Step == "admin" {
			return x, nil, httpjson.Bad("Choose where the role works in the flow.")
		}
		if *in.Step != x.Step {
			changed = append(changed, "step")
		}
		x.Step = *in.Step
	}
	if in.Rights != nil {
		rights := []string{}
		for _, r := range *in.Rights {
			if !textclean.Contains(access.AllRights, r) {
				return x, nil, httpjson.Bad("Unknown right %s.", r)
			}
			rights = append(rights, r)
		}
		rights = textclean.Uniq(rights)
		sort.Strings(rights)
		old := append([]string{}, x.Rights...)
		sort.Strings(old)
		if strings.Join(old, ",") != strings.Join(rights, ",") {
			changed = append(changed, "rights")
		}
		x.Rights = rights
	}
	if in.GroupID != nil {
		var ok bool
		_ = q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM role_group WHERE id=$1)`, *in.GroupID).Scan(&ok)
		if !ok {
			return x, nil, httpjson.Bad("Choose a role group.")
		}
		if x.GroupID == nil || *x.GroupID != *in.GroupID {
			changed = append(changed, "group")
		}
		g := *in.GroupID
		x.GroupID = &g
	}
	if in.Screens != nil {
		set := map[string]string{}
		for k, v := range *in.Screens {
			if !screens.Known(k) {
				return x, nil, httpjson.Bad("Unknown screen %s.", k)
			}
			if _, ok := screens.LevelOf(v); !ok {
				return x, nil, httpjson.Bad("A screen level is no access, view or edit.")
			}
			if v == "edit" && screens.IsViewOnly(k) {
				return x, nil, httpjson.Bad("%s can only be viewed by a role; changes there stay with the System Admin.", screens.Label(k))
			}
			set[k] = v
		}
		changed = append(changed, "screen levels")
		x.set = set
	}
	if in.AllReports != nil && *in.AllReports {
		if x.Reports != nil {
			changed = append(changed, "reports")
		}
		x.Reports = nil
	} else if in.Reports != nil {
		keys := []string{}
		for _, k := range *in.Reports {
			found := false
			for _, it := range reports.List() {
				found = found || it.Key == k
			}
			if !found {
				return x, nil, httpjson.Bad("Unknown report %s.", k)
			}
			keys = append(keys, k)
		}
		changed = append(changed, "reports")
		x.Reports = textclean.Uniq(keys)
	}
	if in.CanUsers != nil {
		if *in.CanUsers != x.CanUsers {
			changed = append(changed, "create users")
		}
		x.CanUsers = *in.CanUsers
	}
	if in.UserRoles != nil {
		ids := textclean.Uniq(*in.UserRoles)
		var bad bool
		_ = q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM unnest($1::bigint[]) x LEFT JOIN role r ON r.id=x WHERE r.id IS NULL OR r.step='admin')`, ids).Scan(&bad)
		if bad {
			return x, nil, httpjson.Bad("The roles it may give must be roles other than System Admin.")
		}
		changed = append(changed, "roles it may give")
		x.UserRoles = ids
	}
	if in.UserScope != nil {
		if *in.UserScope != "own" && *in.UserScope != "any" {
			return x, nil, httpjson.Bad("Choose own properties or any property.")
		}
		x.UserScope = *in.UserScope
	}
	if in.CanRoles != nil {
		if *in.CanRoles != x.CanRoles {
			changed = append(changed, "manage roles")
		}
		x.CanRoles = *in.CanRoles
	}
	if !x.CanUsers {
		x.UserRoles = []int64{}
	}
	return x, changed, nil
}

// withinOwn refuses, for someone who is not the System Admin, a role that gives more than they have: a right, a
// screen level or a report they do not hold, the create-users right beyond their own, or the manage-roles right.
func withinOwn(u *access.User, before, after Role) error {
	if u.IsAdmin {
		return nil
	}
	held := map[string]bool{}
	for _, g := range u.Grants {
		for _, r := range g.Rights {
			held[r] = true
		}
	}
	for _, r := range after.Rights {
		if !held[r] && !textclean.Contains(before.Rights, r) {
			return httpjson.Forbidden("You can give only rights you hold yourself. You do not hold %s.", r)
		}
	}
	old, now := before.effective(), after.effective()
	for _, sc := range screens.All {
		if now[sc.Key] > old[sc.Key] && now[sc.Key] > u.Overall(sc.Key) {
			return httpjson.Forbidden("You can give at most %s on %s, your own level.", screens.LevelName[u.Overall(sc.Key)], sc.Label)
		}
	}
	if after.Reports == nil && before.Reports != nil && u.ReportKeys != nil {
		return httpjson.Forbidden("You can give only the reports you see yourself.")
	}
	for _, k := range after.Reports {
		if !u.SeesReport(k) && !textclean.Contains(before.Reports, k) {
			return httpjson.Forbidden("You can give only the reports you see yourself.")
		}
	}
	if after.CanRoles != before.CanRoles {
		return httpjson.Forbidden("Only the System Admin decides who may manage roles.")
	}
	if after.CanUsers && !before.CanUsers && !u.AddsUsers() {
		return httpjson.Forbidden("You can give the create-users right only if your own role has it.")
	}
	for _, id := range after.UserRoles {
		if !textclean.Contains(u.CreatableRoles, id) && !textclean.Contains(before.UserRoles, id) {
			return httpjson.Forbidden("A role may give only roles you can give yourself.")
		}
	}
	if after.UserScope == "any" && before.UserScope != "any" && !u.UserScopeAny {
		return httpjson.Forbidden("You can add users only inside your own properties, so you cannot allow any property.")
	}
	return nil
}

func (s *Service) writeRole(ctx context.Context, tx pgx.Tx, x Role) error {
	_, err := tx.Exec(ctx, `UPDATE role SET name=$2, step=$3, rights=$4, group_id=$5, screens=$6, reports=$7, can_users=$8, user_role_ids=$9,
		user_scope=$10, can_roles=$11 WHERE id=$1`, x.ID, x.Name, x.Step, x.Rights, x.GroupID, x.set, x.Reports, x.CanUsers, x.UserRoles, x.UserScope, x.CanRoles)
	return err
}

func (s *Service) updateRole(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	if !u.ManagesRoles() {
		return httpjson.Forbidden("Only the System Admin, or a role allowed to manage roles, can change roles.")
	}
	id, err := httpjson.ID(r, "id")
	if err != nil {
		return err
	}
	var in roleIn
	if err := httpjson.Decode(r, &in); err != nil {
		return err
	}
	var msg string
	err = pgx.BeginFunc(r.Context(), s.DB, func(tx pgx.Tx) error {
		list, err := loadRoles(r.Context(), tx, `WHERE r.id=$1 FOR UPDATE OF r`, id)
		if err != nil {
			return err
		}
		if len(list) == 0 {
			return httpjson.NotFound("Role")
		}
		before := list[0]
		if before.Step == "admin" {
			return httpjson.Bad("The System Admin role always has every right, so it cannot be changed.")
		}
		after, changed, err := s.apply(r.Context(), tx, before, in)
		if err != nil {
			return err
		}
		if err := withinOwn(u, before, after); err != nil {
			return err
		}
		if err := s.writeRole(r.Context(), tx, after); err != nil {
			return err
		}
		msg = after.Name + " saved"
		if len(changed) == 0 {
			return nil
		}
		return auditlog.Write(r.Context(), tx, u, "Users", "Role changed", after.Name, "Changed: "+strings.Join(changed, ", "))
	})
	if err != nil {
		return err
	}
	return httpjson.OK(w, map[string]any{"id": id, "message": msg})
}

// createRole adds a custom role with a name, step, rights and group. Its screen levels start from its rights; for
// someone who is not the System Admin they are capped at their own levels, and its reports at the ones they see.
func (s *Service) createRole(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	if !u.ManagesRoles() {
		return httpjson.Forbidden("Only the System Admin, or a role allowed to manage roles, can create roles.")
	}
	var in roleIn
	if err := httpjson.Decode(r, &in); err != nil {
		return err
	}
	if in.Name == nil || in.Step == nil {
		return httpjson.Bad("Enter the role name and where it works in the flow.")
	}
	if in.Rights == nil {
		in.Rights = &[]string{}
	}
	var id int64
	var name string
	err := pgx.BeginFunc(r.Context(), s.DB, func(tx pgx.Tx) error {
		var gid int64
		_ = tx.QueryRow(r.Context(), `SELECT id FROM role_group ORDER BY sort, id LIMIT 1`).Scan(&gid)
		blank := Role{IsCustom: true, Rights: []string{}, UserRoles: []int64{}, UserScope: "own", GroupID: &gid, set: map[string]string{}, Reports: []string{}}
		x, _, err := s.apply(r.Context(), tx, blank, in)
		if err != nil {
			return err
		}
		if len(x.Rights) == 0 && x.Step != "" {
			return httpjson.Bad(`Tick at least one right, or choose "No step in the flow".`)
		}
		if in.Reports == nil && (in.AllReports == nil || !*in.AllReports) {
			x.Reports = u.ReportKeys // what the creator sees: every report for the System Admin
		}
		if !u.IsAdmin {
			capped := map[string]string{}
			for k, l := range x.effective() {
				if own := u.Overall(k); l > own {
					l = own
				}
				capped[k] = screens.LevelName[l]
			}
			x.set = capped
			if err := withinOwn(u, Role{UserScope: "own", Reports: []string{}}, x); err != nil {
				return err
			}
		}
		var n int
		_ = tx.QueryRow(r.Context(), `SELECT count(*)+1 FROM role WHERE is_custom`).Scan(&n)
		if err := tx.QueryRow(r.Context(), `INSERT INTO role(code,name,step,rights,is_custom,created_by,group_id) VALUES ($1,$2,$3,$4,TRUE,$5,$6) RETURNING id`,
			fmt.Sprintf("custom%d", n), x.Name, x.Step, x.Rights, u.ID, x.GroupID).Scan(&id); err != nil {
			return err
		}
		x.ID, name = id, x.Name
		if err := s.writeRole(r.Context(), tx, x); err != nil {
			return err
		}
		return auditlog.Write(r.Context(), tx, u, "Users", "Role created", x.Name, "works as "+textclean.Default(x.Step, "view only")+" · "+strings.Join(x.Rights, ", "))
	})
	if err != nil {
		return err
	}
	return httpjson.OK(w, map[string]any{"id": id, "message": name + " created. Set its screens and reports."})
}

// copyRole makes a custom copy of a role ("<name> copy"): the same step, rights, screen levels, reports, group and
// create-users settings, but never the manage-roles right. Someone who is not the System Admin can copy only a role
// that gives no more than they have.
func (s *Service) copyRole(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	if !u.ManagesRoles() {
		return httpjson.Forbidden("Only the System Admin, or a role allowed to manage roles, can copy roles.")
	}
	id, err := httpjson.ID(r, "id")
	if err != nil {
		return err
	}
	var nid int64
	var name string
	err = pgx.BeginFunc(r.Context(), s.DB, func(tx pgx.Tx) error {
		list, err := loadRoles(r.Context(), tx, `WHERE r.id=$1`, id)
		if err != nil {
			return err
		}
		if len(list) == 0 {
			return httpjson.NotFound("Role")
		}
		src := list[0]
		if src.Step == "admin" {
			return httpjson.Bad("The System Admin role cannot be copied.")
		}
		x := src
		x.CanRoles, x.IsCustom = false, true
		x.set = map[string]string{}
		for k, v := range src.effective() {
			x.set[k] = screens.LevelName[v]
		}
		if err := withinOwn(u, Role{UserScope: "own", Reports: []string{}}, x); err != nil {
			return httpjson.Forbidden("%s gives more than your own role, so you cannot copy it.", src.Name)
		}
		name = src.Name + " copy"
		for i := 2; ; i++ {
			var dup bool
			_ = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM role WHERE norm_key(name)=norm_key($1))`, name).Scan(&dup)
			if !dup {
				break
			}
			name = fmt.Sprintf("%s copy %d", src.Name, i)
		}
		var n int
		_ = tx.QueryRow(r.Context(), `SELECT count(*)+1 FROM role WHERE is_custom`).Scan(&n)
		if err := tx.QueryRow(r.Context(), `INSERT INTO role(code,name,step,rights,is_custom,created_by,group_id) VALUES ($1,$2,$3,$4,TRUE,$5,$6) RETURNING id`,
			fmt.Sprintf("custom%d", n), name, x.Step, x.Rights, u.ID, x.GroupID).Scan(&nid); err != nil {
			return err
		}
		x.ID, x.Name = nid, name
		if err := s.writeRole(r.Context(), tx, x); err != nil {
			return err
		}
		return auditlog.Write(r.Context(), tx, u, "Users", "Role copied", name, "from "+src.Name)
	})
	if err != nil {
		return err
	}
	return httpjson.OK(w, map[string]any{"id": nid, "message": name + " created. Rename it and adjust."})
}

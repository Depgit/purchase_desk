// Package access knows who is signed in: their roles, rights and which properties and categories they may act on.
// Rights and scope are enforced here, on the server, not only hidden on screen.
package access

import (
	"context"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"purchasedesk/internal/httpjson"
	"purchasedesk/internal/screens"
	"purchasedesk/internal/sessiontoken"
	"purchasedesk/internal/textclean"
)

// Rights, as ticked in the role matrix.
const (
	Raise   = "raise"
	Approve = "approve"
	IssuePO = "issue_po"
	Receive = "receive"
	Match   = "match"
	Pay     = "pay"
	Masters = "masters"
	// Catalog creates and edits categories, sub-categories, brands, pack sizes, items and vendors (Accounts Payable);
	// Finance approves each new one and each edit.
	Catalog = "catalog"
	Upload  = "upload"
)

var AllRights = []string{Raise, Approve, IssuePO, Receive, Match, Pay, Masters, Catalog, Upload}

// Steps where a role can sit in the flow.
var Steps = []string{"staff", "l1", "l2", "cc", "purchase", "store", "ap", "finance", "wh", "whs", "whl1", "whl2", "preop", "chef", "hchef", "creq", "dh", "capp", "admin", ""}

type Grant struct {
	RoleID      int64    `json:"role_id"`
	Code        string   `json:"code"`
	Name        string   `json:"name"`
	Step        string   `json:"step"`
	Rights      []string `json:"rights"`
	Scope       string   `json:"scope"`
	ClusterIDs  []int64  `json:"cluster_ids"`
	PropertyIDs []int64  `json:"property_ids"`
	CategoryIDs []int64  `json:"category_ids"`
	// SubCategoryIDs limits a Warehouse Approver L1 to these sub-categories (none = every sub-category).
	SubCategoryIDs []int64 `json:"sub_category_ids"`
	// Screens is the role's level on every screen (screens.None, View or Edit), set on the role or its starting level.
	Screens   map[string]int `json:"screens"`
	Reports   []string       `json:"-"` // nil: every report
	CanUsers  bool           `json:"-"`
	UserRoles []int64        `json:"-"`
	UserScope string         `json:"-"`
	CanRoles  bool           `json:"-"`
}

type User struct {
	ID         int64   `json:"id"`
	Name       string  `json:"name"`
	Email      string  `json:"email"`
	Mobile     string  `json:"mobile"`
	Department string  `json:"department"`
	Grants     []Grant `json:"roles"`
	IsAdmin    bool    `json:"is_admin"`
	// Adding users and managing roles come from the user's roles (role.can_users, role.can_roles).
	AddsUsersOn    bool              `json:"adds_users"`
	CreatableRoles []int64           `json:"user_roles"`     // roles this user may give when adding users
	UserScopeAny   bool              `json:"user_scope_any"` // may add users at any property, not only their own
	ManagesRolesOn bool              `json:"manages_roles"`
	ScreenLevels   map[string]string `json:"screens"` // highest level on each screen across the user's roles
	ReportKeys     []string          `json:"reports"` // reports the user sees; null for every report

	visLoaded bool
	visAll    bool
	visIDs    []int64
}

func (u *User) HasRight(right string) bool {
	if u.IsAdmin {
		return true
	}
	for _, g := range u.Grants {
		if textclean.Contains(g.Rights, right) {
			return true
		}
	}
	return false
}

func (u *User) HasStep(step string) bool {
	if u.IsAdmin {
		return true
	}
	for _, g := range u.Grants {
		if g.Step == step {
			return true
		}
	}
	return false
}

// AddsUsers reports whether the user may add users: the System Admin, or a role with the create-users right.
func (u *User) AddsUsers() bool {
	return u.IsAdmin || u.AddsUsersOn
}

// ManagesRoles reports whether the user may create and edit roles: the System Admin, or a role the Admin allowed.
func (u *User) ManagesRoles() bool {
	return u.IsAdmin || u.ManagesRolesOn
}

// Level is the user's level on a screen at one property: the highest level among the roles that cover it.
func (u *User) Level(screen string, propID, clusterID int64) int {
	if u.IsAdmin {
		return screens.Edit
	}
	best := screens.None
	for _, g := range u.Grants {
		if l := g.Screens[screen]; l > best && covers(g, propID, clusterID, nil) {
			best = l
		}
	}
	return best
}

// Overall is the user's highest level on a screen at any property; it decides whether the screen opens at all.
func (u *User) Overall(screen string) int {
	if u.IsAdmin {
		return screens.Edit
	}
	best := screens.None
	for _, g := range u.Grants {
		if l := g.Screens[screen]; l > best {
			best = l
		}
	}
	return best
}

// SeesReport reports whether one of the user's roles includes a report.
func (u *User) SeesReport(key string) bool {
	return u.ReportKeys == nil || textclean.Contains(u.ReportKeys, key)
}

func (u *User) RoleName() string {
	if u.IsAdmin {
		return "System Admin"
	}
	names := []string{}
	for _, g := range u.Grants {
		names = append(names, g.Name)
	}
	if len(names) == 0 {
		return "No role"
	}
	return strings.Join(names, " + ")
}

func covers(g Grant, propID, clusterID int64, cats []int64) bool {
	switch g.Scope {
	case "clusters":
		if !textclean.Contains(g.ClusterIDs, clusterID) {
			return false
		}
	case "properties":
		if !textclean.Contains(g.PropertyIDs, propID) {
			return false
		}
	}
	if len(g.CategoryIDs) > 0 {
		for _, c := range cats {
			if !textclean.Contains(g.CategoryIDs, c) {
				return false
			}
		}
	}
	return true
}

// Can returns the role that lets the user act at a flow step with a right, for a property and its categories,
// or nil. An empty step or right matches any. The System Admin can act at every step.
func (u *User) Can(step, right string, propID, clusterID int64, cats []int64) *Grant {
	if u.IsAdmin {
		return &Grant{Name: "System Admin", Step: "admin"}
	}
	for i, g := range u.Grants {
		if (step == "" || g.Step == step) && (right == "" || textclean.Contains(g.Rights, right)) && covers(g, propID, clusterID, cats) {
			return &u.Grants[i]
		}
	}
	return nil
}

// CanSub is Can for a step whose roles may also be limited to sub-categories (Warehouse Approver L1): each of subs
// must be ticked on the grant, or the grant ticks none.
func (u *User) CanSub(step, right string, propID, clusterID int64, cats, subs []int64) *Grant {
	if u.IsAdmin {
		return &Grant{Name: "System Admin", Step: "admin"}
	}
	for i, g := range u.Grants {
		if g.Step != step || (right != "" && !textclean.Contains(g.Rights, right)) || !covers(g, propID, clusterID, cats) {
			continue
		}
		ok := true
		for _, s := range subs {
			ok = ok && (len(g.SubCategoryIDs) == 0 || textclean.Contains(g.SubCategoryIDs, s))
		}
		if ok {
			return &u.Grants[i]
		}
	}
	return nil
}

func NeedRight(u *User, right, what string) error {
	if !u.HasRight(right) {
		return httpjson.Forbidden("You do not have the right to %s.", what)
	}
	return nil
}

func NeedAdmin(u *User) error {
	if !u.IsAdmin {
		return httpjson.Forbidden("Only the System Admin can do this.")
	}
	return nil
}

type ctxKey int

const userKey ctxKey = 1

// From returns the signed-in user of a request that passed the Guard middleware.
func From(r *http.Request) *User { return r.Context().Value(userKey).(*User) }

type Guard struct {
	DB     *pgxpool.Pool
	Tokens sessiontoken.Tokens
}

func (g *Guard) Load(ctx context.Context, userID int64) (*User, error) {
	u := &User{}
	var active bool
	err := g.DB.QueryRow(ctx, `SELECT u.id, u.name, coalesce(u.email::text,''), coalesce(u.mobile,''), u.department, u.active
		FROM app_user u WHERE u.id=$1`, userID).
		Scan(&u.ID, &u.Name, &u.Email, &u.Mobile, &u.Department, &active)
	if err != nil || !active {
		return nil, httpjson.Unauthorized("Your session has ended. Please sign in again.")
	}
	rows, err := g.DB.Query(ctx, `SELECT r.id, r.code, r.name, r.step, r.rights, ur.scope, ur.cluster_ids, ur.property_ids, ur.category_ids, ur.sub_category_ids,
		r.screens, r.reports, r.can_users, r.user_role_ids, r.user_scope, r.can_roles
		FROM user_role ur JOIN role r ON r.id=ur.role_id WHERE ur.user_id=$1 AND r.active ORDER BY r.id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var gr Grant
		var set map[string]string
		if err := rows.Scan(&gr.RoleID, &gr.Code, &gr.Name, &gr.Step, &gr.Rights, &gr.Scope, &gr.ClusterIDs, &gr.PropertyIDs, &gr.CategoryIDs, &gr.SubCategoryIDs,
			&set, &gr.Reports, &gr.CanUsers, &gr.UserRoles, &gr.UserScope, &gr.CanRoles); err != nil {
			return nil, err
		}
		if gr.Step == "admin" {
			u.IsAdmin = true
		}
		gr.Screens = screens.Effective(set, gr.Rights, gr.Step, gr.CanUsers, gr.CanRoles)
		u.Grants = append(u.Grants, gr)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	u.summarize()
	return u, nil
}

// summarize works out what the user's roles give together, for /me and the checks: the highest level per screen,
// the reports, and the user and role rights.
func (u *User) summarize() {
	u.ScreenLevels = map[string]string{}
	for _, s := range screens.All {
		u.ScreenLevels[s.Key] = screens.LevelName[u.Overall(s.Key)]
	}
	u.CreatableRoles = []int64{}
	if u.IsAdmin {
		u.AddsUsersOn, u.ManagesRolesOn, u.UserScopeAny, u.ReportKeys = true, true, true, nil
		return
	}
	reports, all := []string{}, false
	for _, g := range u.Grants {
		if g.Reports == nil {
			all = true
		}
		reports = append(reports, g.Reports...)
		if g.CanUsers {
			u.AddsUsersOn = true
			u.CreatableRoles = append(u.CreatableRoles, g.UserRoles...)
			u.UserScopeAny = u.UserScopeAny || g.UserScope == "any"
		}
		u.ManagesRolesOn = u.ManagesRolesOn || g.CanRoles
	}
	u.CreatableRoles = textclean.Uniq(u.CreatableRoles)
	if all {
		u.ReportKeys = nil
	} else {
		u.ReportKeys = textclean.Uniq(reports)
	}
}

// Visible returns the properties the user may see, or all=true.
func (g *Guard) Visible(ctx context.Context, u *User) (all bool, ids []int64, err error) {
	if u.visLoaded {
		return u.visAll, u.visIDs, nil
	}
	u.visLoaded = true
	if u.IsAdmin {
		u.visAll = true
		return true, nil, nil
	}
	cl, pr := []int64{}, []int64{}
	for _, gr := range u.Grants {
		switch gr.Scope {
		case "all":
			u.visAll = true
			return true, nil, nil
		case "clusters":
			cl = append(cl, gr.ClusterIDs...)
		case "properties":
			pr = append(pr, gr.PropertyIDs...)
		}
	}
	rows, err := g.DB.Query(ctx, `SELECT id FROM property WHERE cluster_id = ANY($1) OR id = ANY($2)`, cl, pr)
	if err != nil {
		return false, nil, err
	}
	defer rows.Close()
	out := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return false, nil, err
		}
		out = append(out, id)
	}
	u.visIDs = out
	return false, out, rows.Err()
}

// SeesProperty reports whether a property is inside the user's scope.
func (g *Guard) SeesProperty(ctx context.Context, u *User, propID int64) bool {
	all, ids, err := g.Visible(ctx, u)
	return err == nil && (all || textclean.Contains(ids, propID))
}

func (g *Guard) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := r.Header.Get("Authorization")
		tok := strings.TrimPrefix(h, "Bearer ")
		if tok == "" || tok == h {
			tok = r.URL.Query().Get("token") // files opened in a new browser tab
		}
		uid, err := g.Tokens.Parse(tok)
		if err != nil {
			httpjson.JSON(w, http.StatusUnauthorized, httpjson.Unauthorized("Please sign in."))
			return
		}
		u, err := g.Load(r.Context(), uid)
		if err != nil {
			httpjson.JSON(w, http.StatusUnauthorized, httpjson.Unauthorized("Your session has ended. Please sign in again."))
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey, u)))
	})
}

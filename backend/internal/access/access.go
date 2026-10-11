// Package access knows who is signed in: their roles, rights and which properties and categories they may act on.
// Rights and scope are enforced here, on the server, not only hidden on screen.
package access

import (
	"context"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"purchasedesk/internal/httpjson"
	"purchasedesk/internal/releasephase"
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
	Upload  = "upload"
)

var AllRights = []string{Raise, Approve, IssuePO, Receive, Match, Pay, Masters, Upload}

// Steps where a role can sit in the flow.
var Steps = []string{"staff", "l1", "l2", "cc", "purchase", "store", "ap", "finance", "wh", "preop", "creq", "dh", "capp", "admin", ""}

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
}

type User struct {
	ID         int64   `json:"id"`
	Name       string  `json:"name"`
	Email      string  `json:"email"`
	Mobile     string  `json:"mobile"`
	Department string  `json:"department"`
	Grants     []Grant `json:"roles"`
	IsAdmin    bool    `json:"is_admin"`
	// CreatableRoles are the roles the System Admin lets this user give when adding users (see AddsUsers).
	CreatableRoles []int64 `json:"creatable_roles"`
	Phase          int     `json:"phase"` // release phase switched on (RELEASE_PHASE), so screens show only what is open

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

// AddsUsers reports whether the user may add users: the System Admin, or someone the Admin gave creatable roles.
func (u *User) AddsUsers() bool {
	return u.IsAdmin || len(u.CreatableRoles) > 0
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
	u := &User{Phase: releasephase.Current()}
	var active bool
	err := g.DB.QueryRow(ctx, `SELECT u.id, u.name, coalesce(u.email::text,''), coalesce(u.mobile,''), u.department, u.active,
		coalesce((SELECT array_agg(r.id ORDER BY r.id) FROM role r WHERE r.id = ANY(u.creatable_role_ids) AND r.active AND r.step<>'admin'),'{}')
		FROM app_user u WHERE u.id=$1`, userID).
		Scan(&u.ID, &u.Name, &u.Email, &u.Mobile, &u.Department, &active, &u.CreatableRoles)
	if err != nil || !active {
		return nil, httpjson.Unauthorized("Your session has ended. Please sign in again.")
	}
	rows, err := g.DB.Query(ctx, `SELECT r.id, r.code, r.name, r.step, r.rights, ur.scope, ur.cluster_ids, ur.property_ids, ur.category_ids
		FROM user_role ur JOIN role r ON r.id=ur.role_id WHERE ur.user_id=$1 AND r.active ORDER BY r.id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var gr Grant
		if err := rows.Scan(&gr.RoleID, &gr.Code, &gr.Name, &gr.Step, &gr.Rights, &gr.Scope, &gr.ClusterIDs, &gr.PropertyIDs, &gr.CategoryIDs); err != nil {
			return nil, err
		}
		if gr.Step == "admin" {
			u.IsAdmin = true
		}
		u.Grants = append(u.Grants, gr)
	}
	return u, rows.Err()
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

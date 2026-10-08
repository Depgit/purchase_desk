// Package auditlog records every action (who, which role, when, what record) and shows the log.
// The log tables reject edits and deletes at the database level.
package auditlog

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"purchasedesk/internal/access"
	"purchasedesk/internal/database"
	"purchasedesk/internal/httpjson"
)

// Write adds one line to the activity log. Pass the transaction when inside one.
func Write(ctx context.Context, q database.Q, u *access.User, module, action, record, details string) error {
	var uid *int64
	name, role := "System", "System"
	if u != nil {
		uid, name, role = &u.ID, u.Name, u.RoleName()
	}
	_, err := q.Exec(ctx, `INSERT INTO activity_log (user_id,user_name,role_name,module,action,record,details) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		uid, name, role, module, action, record, details)
	return err
}

// Doc adds a line to a document's history and to the activity log.
// roleName is the role the user acted as at this step; empty uses all their roles.
func Doc(ctx context.Context, q database.Q, u *access.User, roleName string, docID int64, number, module, action, note string) error {
	var uid *int64
	name := "System"
	if u != nil {
		uid, name = &u.ID, u.Name
		if roleName == "" {
			roleName = u.RoleName()
		}
	}
	if roleName == "" {
		roleName = "System"
	}
	if _, err := q.Exec(ctx, `INSERT INTO document_log (document_id,user_id,user_name,role_name,action,note) VALUES ($1,$2,$3,$4,$5,$6)`,
		docID, uid, name, roleName, action, note); err != nil {
		return err
	}
	_, err := q.Exec(ctx, `INSERT INTO activity_log (user_id,user_name,role_name,module,action,record,details) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		uid, name, roleName, module, action, number, note)
	return err
}

type Service struct{ DB *pgxpool.Pool }

func (s *Service) Routes(r chi.Router) {
	r.Get("/activity", httpjson.Wrap(s.list))
}

type entry struct {
	ID      int64     `json:"id"`
	At      time.Time `json:"at"`
	User    string    `json:"user"`
	Role    string    `json:"role"`
	Module  string    `json:"module"`
	Action  string    `json:"action"`
	Record  string    `json:"record"`
	Details string    `json:"details"`
}

func (s *Service) list(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	if !u.IsAdmin && !u.HasRight(access.Masters) && !u.HasStep("finance") && !u.HasStep("cc") {
		return httpjson.Forbidden("The activity log is open to the Admin, Finance, the Cost Controller and the Purchase Team.")
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	mod := r.URL.Query().Get("module")
	limit := httpjson.QInt(r, "limit", 1000)
	if limit > 10000 {
		limit = 10000
	}
	rows, err := s.DB.Query(r.Context(), `SELECT id, at, user_name, role_name, module, action, record, details FROM activity_log
		WHERE ($1='' OR module=$1) AND ($2='' OR (user_name||' '||role_name||' '||action||' '||record||' '||details) ILIKE '%'||$2||'%')
		ORDER BY at DESC, id DESC LIMIT $3`, mod, q, limit)
	if err != nil {
		return err
	}
	defer rows.Close()
	out := []entry{}
	for rows.Next() {
		var a entry
		if err := rows.Scan(&a.ID, &a.At, &a.User, &a.Role, &a.Module, &a.Action, &a.Record, &a.Details); err != nil {
			return err
		}
		out = append(out, a)
	}
	return httpjson.OK(w, out)
}

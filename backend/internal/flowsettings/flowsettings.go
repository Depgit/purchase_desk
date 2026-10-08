// Package flowsettings holds the rules the Admin changes without code: flow settings,
// reason pick-lists and the email and WhatsApp message templates.
package flowsettings

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"purchasedesk/internal/access"
	"purchasedesk/internal/auditlog"
	"purchasedesk/internal/database"
	"purchasedesk/internal/httpjson"
	"purchasedesk/internal/textclean"
)

type Service struct{ DB *pgxpool.Pool }

func (s *Service) Routes(r chi.Router) {
	r.Get("/settings", httpjson.Wrap(s.getSettings))
	r.Put("/settings", httpjson.Wrap(s.putSettings))
	r.Get("/pick-lists", httpjson.Wrap(s.getPickLists))
	r.Put("/pick-lists/{name}", httpjson.Wrap(s.putPickList))
	r.Get("/templates", httpjson.Wrap(s.listTemplates))
	r.Put("/templates/{code}", httpjson.Wrap(s.putTemplate))
}

// Settings are the flow rules the Admin changes on the Flow Settings screen.
type Settings struct {
	EvtL2           bool    `json:"evt_l2"`
	L2Above         float64 `json:"l2_above"`
	CCAll           bool    `json:"cc_all"`
	WHApproval      bool    `json:"wh_approval"`
	QtyUp           bool    `json:"qty_up"`
	WeekFlag        bool    `json:"week_flag"`
	BillApprover    string  `json:"bill_approver"`
	BillJumpPct     float64 `json:"bill_jump_pct"`
	AMCWarnDays     int     `json:"amc_warn_days"`
	AMCDurations    []int   `json:"amc_durations"`
	MatchTolPct     float64 `json:"match_tol_pct"`
	MSMEDays        int     `json:"msme_days"`
	NeedSchedule    bool    `json:"need_schedule"`
	SheetApproved   bool    `json:"sheet_approved"`
	CorpL1OnlyBelow float64 `json:"corp_l1_only_below"`
}

// Load reads the current flow settings.
func Load(ctx context.Context, q database.Q) (Settings, error) {
	var st Settings
	rows, err := q.Query(ctx, `SELECT key, value FROM setting`)
	if err != nil {
		return st, err
	}
	defer rows.Close()
	m := map[string]json.RawMessage{}
	for rows.Next() {
		var k string
		var v json.RawMessage
		if err := rows.Scan(&k, &v); err != nil {
			return st, err
		}
		m[k] = v
	}
	b, _ := json.Marshal(m)
	err = json.Unmarshal(b, &st)
	return st, err
}

func (s *Service) getSettings(w http.ResponseWriter, r *http.Request) error {
	st, err := Load(r.Context(), s.DB)
	if err != nil {
		return err
	}
	return httpjson.OK(w, st)
}

func (s *Service) putSettings(w http.ResponseWriter, r *http.Request) error {
	p := access.From(r)
	if err := access.NeedAdmin(p); err != nil {
		return err
	}
	var in map[string]json.RawMessage
	if err := httpjson.Decode(r, &in); err != nil {
		return err
	}
	cur, err := Load(r.Context(), s.DB)
	if err != nil {
		return err
	}
	curJSON, _ := json.Marshal(cur)
	var curMap map[string]json.RawMessage
	_ = json.Unmarshal(curJSON, &curMap)
	merged := map[string]json.RawMessage{}
	for k, v := range curMap {
		merged[k] = v
	}
	for k, v := range in {
		if _, ok := curMap[k]; !ok {
			return httpjson.Bad("Unknown setting %s", k)
		}
		merged[k] = v
	}
	mb, _ := json.Marshal(merged)
	var next Settings
	if err := json.Unmarshal(mb, &next); err != nil {
		return httpjson.Bad("A setting has the wrong type: %v", err)
	}
	if next.BillApprover != "l2" && next.BillApprover != "cc" {
		return httpjson.Bad("Bills are approved by the Cluster Manager or the Cost Controller.")
	}
	if next.L2Above < 0 || next.MatchTolPct < 0 || next.MatchTolPct > 50 || next.MSMEDays <= 0 || next.BillJumpPct < 0 || next.AMCWarnDays < 0 || next.CorpL1OnlyBelow < 0 {
		return httpjson.Bad("Numbers must be 0 or more, and the tolerance at most 50%%.")
	}
	durs := []int{}
	for _, d := range next.AMCDurations {
		if d > 0 && d <= 60 {
			durs = append(durs, d)
		}
	}
	sort.Ints(durs)
	durs = textclean.Uniq(durs)
	if len(durs) == 0 {
		return httpjson.Bad("Enter at least one AMC duration.")
	}
	next.AMCDurations = durs
	nb, _ := json.Marshal(next)
	var nextMap map[string]json.RawMessage
	_ = json.Unmarshal(nb, &nextMap)
	err = pgx.BeginFunc(r.Context(), s.DB, func(tx pgx.Tx) error {
		for k, v := range nextMap {
			if string(curMap[k]) == string(v) {
				continue
			}
			if _, err := tx.Exec(r.Context(), `INSERT INTO setting(key,value,updated_by,updated_at) VALUES ($1,$2,$3,now())
				ON CONFLICT (key) DO UPDATE SET value=EXCLUDED.value, updated_by=EXCLUDED.updated_by, updated_at=now()`, k, v, p.ID); err != nil {
				return err
			}
			if err := auditlog.Write(r.Context(), tx, p, "Settings", "Setting changed", k, fmt.Sprintf("%s to %s", curMap[k], v)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	return httpjson.OK(w, next)
}

// PickLists returns every reason and slot list by name.
func PickLists(ctx context.Context, q database.Q) (map[string][]string, error) {
	rows, err := q.Query(ctx, `SELECT name, entries FROM pick_list`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var n string
		var e []string
		if err := rows.Scan(&n, &e); err != nil {
			return nil, err
		}
		out[n] = e
	}
	return out, rows.Err()
}

func (s *Service) getPickLists(w http.ResponseWriter, r *http.Request) error {
	m, err := PickLists(r.Context(), s.DB)
	if err != nil {
		return err
	}
	return httpjson.OK(w, m)
}

func (s *Service) putPickList(w http.ResponseWriter, r *http.Request) error {
	p := access.From(r)
	if err := access.NeedAdmin(p); err != nil {
		return err
	}
	name := chi.URLParam(r, "name")
	var in struct {
		Entries []string `json:"entries"`
	}
	if err := httpjson.Decode(r, &in); err != nil {
		return err
	}
	es := []string{}
	for _, e := range in.Entries {
		if e = textclean.Clean(e); e != "" && !textclean.Contains(es, e) {
			es = append(es, e)
		}
	}
	if len(es) == 0 {
		return httpjson.Bad("Keep at least one entry.")
	}
	ct, err := s.DB.Exec(r.Context(), `UPDATE pick_list SET entries=$2, updated_by=$3, updated_at=now() WHERE name=$1`, name, es, p.ID)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return httpjson.NotFound("List")
	}
	_ = auditlog.Write(r.Context(), s.DB, p, "Settings", "List changed", name, strings.Join(es, "; "))
	return httpjson.OK(w, es)
}

// CheckReason checks a reason is on the list, and that a reason starting with "Other" carries a remark.
func CheckReason(ctx context.Context, q database.Q, list, reason, note string) error {
	m, err := PickLists(ctx, q)
	if err != nil {
		return err
	}
	if strings.TrimSpace(reason) == "" {
		return httpjson.Bad("Choose a reason.")
	}
	if !textclean.Contains(m[list], reason) {
		return httpjson.Bad("Choose a reason from the list.")
	}
	if strings.HasPrefix(reason, "Other") && len(strings.TrimSpace(note)) < 3 {
		return httpjson.Bad(`Write a remark for "Other".`)
	}
	return nil
}

// Template returns a message template body by code.
func Template(ctx context.Context, q database.Q, code string) (string, string, error) {
	var body, ch string
	err := q.QueryRow(ctx, `SELECT body, channel FROM message_template WHERE code=$1`, code).Scan(&body, &ch)
	return body, ch, err
}

type template struct {
	Code    string `json:"code"`
	Name    string `json:"name"`
	Channel string `json:"channel"`
	Body    string `json:"body"`
}

func (s *Service) listTemplates(w http.ResponseWriter, r *http.Request) error {
	rows, err := s.DB.Query(r.Context(), `SELECT code,name,channel,body FROM message_template ORDER BY code`)
	if err != nil {
		return err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (template, error) {
		var t template
		err := row.Scan(&t.Code, &t.Name, &t.Channel, &t.Body)
		return t, err
	})
	if err != nil {
		return err
	}
	return httpjson.OK(w, out)
}

func (s *Service) putTemplate(w http.ResponseWriter, r *http.Request) error {
	p := access.From(r)
	if err := access.NeedAdmin(p); err != nil {
		return err
	}
	var in struct {
		Body string `json:"body"`
	}
	if err := httpjson.Decode(r, &in); err != nil {
		return err
	}
	if len(strings.TrimSpace(in.Body)) < 10 {
		return httpjson.Bad("The message is too short.")
	}
	code := chi.URLParam(r, "code")
	ct, err := s.DB.Exec(r.Context(), `UPDATE message_template SET body=$2, updated_by=$3, updated_at=now() WHERE code=$1`, code, strings.TrimSpace(in.Body), p.ID)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return httpjson.NotFound("Template")
	}
	_ = auditlog.Write(r.Context(), s.DB, p, "Settings", "Message template changed", code, in.Body)
	return httpjson.OK(w, map[string]bool{"ok": true})
}

// Render fills a template and also returns the values in placeholder order, for WhatsApp template parameters.
func Render(body string, vals map[string]string) (string, []string) {
	params := []string{}
	rest := body
	for {
		i := strings.Index(rest, "{")
		if i < 0 {
			break
		}
		j := strings.Index(rest[i:], "}")
		if j < 0 {
			break
		}
		params = append(params, vals[rest[i+1:i+j]])
		rest = rest[i+j+1:]
	}
	return Fill(body, vals), params
}

// Fill puts values into a template's {placeholders}.
func Fill(body string, vals map[string]string) string {
	out := body
	for k, v := range vals {
		out = strings.ReplaceAll(out, "{"+k+"}", v)
	}
	for strings.Contains(out, "\n\n\n") {
		out = strings.ReplaceAll(out, "\n\n\n", "\n\n")
	}
	return out
}

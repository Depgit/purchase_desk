// Package warehousejournal posts one Zoho Books journal a month per property for goods issued from the warehouse:
// debit each sub-category's expense account for what the property received, debit the stock loss account for pilferage,
// and credit warehouse stock for the total. Every line carries the property's reporting tag.
package warehousejournal

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"purchasedesk/internal/access"
	"purchasedesk/internal/auditlog"
	"purchasedesk/internal/clock"
	"purchasedesk/internal/httpjson"
	"purchasedesk/internal/money"
	"purchasedesk/internal/zohosync"
)

type Service struct {
	DB          *pgxpool.Pool
	Sync        *zohosync.Service
	PushEnabled bool
}

func (s *Service) Routes(r chi.Router) {
	r.Get("/zoho/journals", httpjson.Wrap(s.list))
	r.Get("/zoho/journals/preview", httpjson.Wrap(s.preview))
	r.Post("/zoho/journals", httpjson.Wrap(s.post))
}

type Line struct {
	AccountID string  `json:"account_id"`
	Account   string  `json:"account"`
	Note      string  `json:"note"`
	Debit     float64 `json:"debit"`
	Credit    float64 `json:"credit"`
}

type Journal struct {
	PropertyID      int64      `json:"property_id"`
	Property        string     `json:"property"`
	Month           string     `json:"month"`
	Transfers       int        `json:"transfers"`
	Expense         float64    `json:"expense"`
	Pilferage       float64    `json:"pilferage"`
	Total           float64    `json:"total"`
	Lines           []Line     `json:"lines"`
	Problems        []string   `json:"problems"`
	PostedNo        string     `json:"posted_number"`
	PostedAmt       float64    `json:"posted_amount"`
	PostedAt        *time.Time `json:"posted_at"`
	tagID, optionID string
}

func build(ctx context.Context, q pgx.Tx, db *pgxpool.Pool, propertyID int64, month time.Time) (*Journal, error) {
	var qq interface {
		QueryRow(context.Context, string, ...any) pgx.Row
		Query(context.Context, string, ...any) (pgx.Rows, error)
	} = db
	if q != nil {
		qq = q
	}
	j := &Journal{PropertyID: propertyID, Month: clock.YMD(month)[:7], Lines: []Line{}, Problems: []string{}}
	end := month.AddDate(0, 1, 0)
	if err := qq.QueryRow(ctx, `SELECT p.name, coalesce(o.tag_id,''), coalesce(m.tag_option_id,'') FROM property p
		LEFT JOIN zoho_map_property m ON m.property_id=p.id LEFT JOIN zoho_tag_option o ON o.option_id=m.tag_option_id WHERE p.id=$1`, propertyID).Scan(&j.Property, &j.tagID, &j.optionID); err != nil {
		return nil, err
	}
	_ = qq.QueryRow(ctx, `SELECT count(*) FROM document WHERE is_transfer AND status='WH_DONE' AND property_id=$1 AND transfer_received_at >= $2 AND transfer_received_at < $3`, propertyID, month, end).Scan(&j.Transfers)
	rows, err := qq.Query(ctx, `SELECT sc.id, sc.name, coalesce(ma.account_id,''), coalesce(za.name,''),
		sum(l.received_qty*l.rate), sum((coalesce(l.dispatched_qty,0)-l.received_qty)*l.rate)
		FROM document d JOIN document_line l ON l.document_id=d.id JOIN sku k ON k.id=l.sku_id JOIN sub_category sc ON sc.id=k.sub_category_id
		LEFT JOIN zoho_map_account ma ON ma.kind='subcat' AND ma.ref=sc.id::text LEFT JOIN zoho_account za ON za.account_id=ma.account_id
		WHERE d.is_transfer AND d.status='WH_DONE' AND d.property_id=$1 AND d.transfer_received_at >= $2 AND d.transfer_received_at < $3
		GROUP BY sc.id, sc.name, ma.account_id, za.name ORDER BY sc.name`, propertyID, month, end)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var scID int64
		var sc, acc, accName string
		var exp, pil float64
		if err := rows.Scan(&scID, &sc, &acc, &accName, &exp, &pil); err != nil {
			rows.Close()
			return nil, err
		}
		exp, pil = money.Round2(exp), money.Round2(pil)
		if exp > 0 {
			if acc == "" {
				j.Problems = append(j.Problems, sc+" has no expense account")
			}
			j.Lines = append(j.Lines, Line{AccountID: acc, Account: accName, Note: sc + " received from warehouse", Debit: exp})
			j.Expense += exp
		}
		j.Pilferage += pil
	}
	rows.Close()
	var lossID, lossName, whID, whName string
	_ = qq.QueryRow(ctx, `SELECT coalesce(m.account_id,''), coalesce(a.name,'') FROM zoho_map_account m LEFT JOIN zoho_account a ON a.account_id=m.account_id WHERE m.kind='special' AND m.ref='loss'`).Scan(&lossID, &lossName)
	_ = qq.QueryRow(ctx, `SELECT coalesce(m.account_id,''), coalesce(a.name,'') FROM zoho_map_account m LEFT JOIN zoho_account a ON a.account_id=m.account_id WHERE m.kind='special' AND m.ref='warehouse'`).Scan(&whID, &whName)
	if j.Pilferage > 0 {
		if lossID == "" {
			j.Problems = append(j.Problems, "No stock loss account is set")
		}
		j.Lines = append(j.Lines, Line{AccountID: lossID, Account: lossName, Note: "Dispatched but not received (pilferage)", Debit: money.Round2(j.Pilferage)})
	}
	j.Total = money.Round2(j.Expense + j.Pilferage)
	if j.Total > 0 {
		if whID == "" {
			j.Problems = append(j.Problems, "No warehouse stock account is set")
		}
		j.Lines = append(j.Lines, Line{AccountID: whID, Account: whName, Note: "Stock issued by the Central Warehouse", Credit: j.Total})
	}
	if j.tagID == "" || j.optionID == "" {
		j.Problems = append(j.Problems, j.Property+" has no Zoho reporting tag")
	}
	_ = qq.QueryRow(ctx, `SELECT number, amount, posted_at FROM zoho_journal WHERE property_id=$1 AND month=$2`, propertyID, month).Scan(&j.PostedNo, &j.PostedAmt, &j.PostedAt)
	return j, nil
}

func monthOf(r *http.Request) time.Time {
	if m, ok := clock.ParseMonth(r.URL.Query().Get("month")); ok {
		return m
	}
	return clock.MonthStart(clock.Today())
}

func (s *Service) list(w http.ResponseWriter, r *http.Request) error {
	month := monthOf(r)
	rows, err := s.DB.Query(r.Context(), `SELECT DISTINCT property_id FROM document WHERE is_transfer AND status='WH_DONE' AND transfer_received_at >= $1 AND transfer_received_at < $2
		UNION SELECT property_id FROM zoho_journal WHERE month=$1`, month, month.AddDate(0, 1, 0))
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return err
	}
	out := []*Journal{}
	for _, id := range ids {
		j, err := build(r.Context(), nil, s.DB, id, month)
		if err != nil {
			return err
		}
		out = append(out, j)
	}
	return httpjson.OK(w, out)
}

func (s *Service) preview(w http.ResponseWriter, r *http.Request) error {
	j, err := build(r.Context(), nil, s.DB, int64(httpjson.QInt(r, "property_id", 0)), monthOf(r))
	if err != nil {
		return err
	}
	return httpjson.OK(w, j)
}

func (s *Service) post(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	if !u.IsAdmin && !u.HasStep("finance") {
		return httpjson.Forbidden("Only Finance or the System Admin can post journals.")
	}
	if !s.PushEnabled {
		return httpjson.Bad("Posting to Zoho is switched off on this server (ZOHO_PUSH_ENABLED=false).")
	}
	var in struct {
		PropertyID int64  `json:"property_id"`
		Month      string `json:"month"`
	}
	if err := httpjson.Decode(r, &in); err != nil {
		return err
	}
	month, ok := clock.ParseMonth(in.Month)
	if !ok {
		return httpjson.Bad("Month must be YYYY-MM.")
	}
	if !month.Before(clock.MonthStart(clock.Today())) {
		return httpjson.Bad("Post a month's journal after the month has ended, once every transfer is received.")
	}
	j, err := build(r.Context(), nil, s.DB, in.PropertyID, month)
	if err != nil {
		return err
	}
	if j.PostedNo != "" {
		return httpjson.Bad("The journal for %s, %s is already posted as %s.", j.Property, in.Month, j.PostedNo)
	}
	if len(j.Problems) > 0 {
		return httpjson.Bad("Cannot post yet: %v.", j.Problems)
	}
	if j.Total == 0 {
		return httpjson.Bad("Nothing to post for this month.")
	}
	c, _, err := s.Sync.Client(r.Context())
	if err != nil {
		return err
	}
	tags := []map[string]string{{"tag_id": j.tagID, "tag_option_id": j.optionID}}
	items := []map[string]any{}
	for _, l := range j.Lines {
		side, amt := "debit", l.Debit
		if l.Credit > 0 {
			side, amt = "credit", l.Credit
		}
		items = append(items, map[string]any{"account_id": l.AccountID, "debit_or_credit": side, "amount": amt, "description": l.Note, "tags": tags})
	}
	last := month.AddDate(0, 1, -1)
	ref := fmt.Sprintf("WH-%d-%s", in.PropertyID, in.Month)
	body := map[string]any{"journal_date": clock.YMD(last), "reference_number": ref, "notes": "Purchase Desk warehouse issues to " + j.Property + " for " + in.Month, "line_items": items}
	jid, number, err := c.CreateJournal(r.Context(), body)
	if err != nil {
		return httpjson.Bad("Zoho did not accept the journal: %v", err)
	}
	if _, err := s.DB.Exec(r.Context(), `INSERT INTO zoho_journal(property_id,month,journal_id,number,amount,posted_by) VALUES ($1,$2,$3,$4,$5,$6)`, in.PropertyID, month, jid, number, j.Total, u.ID); err != nil {
		return err
	}
	_ = auditlog.Write(r.Context(), s.DB, u, "Zoho", "Warehouse journal posted", number, fmt.Sprintf("%s · %s · %s", j.Property, in.Month, money.INR(j.Total)))
	return httpjson.OK(w, map[string]string{"message": number + " posted to Zoho for " + j.Property})
}

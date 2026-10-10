// Package warehousejournal posts Zoho Books journals per property for goods issued from the warehouse: debit each
// sub-category's expense account for what the property received, debit the stock loss account for pilferage, and
// credit warehouse stock for the total. Every line carries the property's reporting tag. A journal covers a period:
// Finance can post what has been received so far ("Post now"), and after the month ends the rest of the month is
// posted automatically (PostMonthEnds), starting where the last journal stopped.
package warehousejournal

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
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
	PropertyID      int64     `json:"property_id"`
	Property        string    `json:"property"`
	Month           string    `json:"month"`
	Transfers       int       `json:"transfers"`
	Expense         float64   `json:"expense"`
	Pilferage       float64   `json:"pilferage"`
	Total           float64   `json:"total"`
	Lines           []Line    `json:"lines"`
	Problems        []string  `json:"problems"`
	From            time.Time `json:"from"` // what is waiting: received from here (where the last journal stopped)
	To              time.Time `json:"to"`   // up to now, or the month's end once it is over
	MonthOver       bool      `json:"month_over"`
	Parts           []Part    `json:"parts"` // journals already posted for this property and month
	tagID, optionID string
}

// Part is a journal already posted for part (or all) of a month.
type Part struct {
	Number    string    `json:"number"`
	Amount    float64   `json:"amount"`
	From      time.Time `json:"from"`
	To        time.Time `json:"to"`
	PostedAt  time.Time `json:"posted_at"`
	Automatic bool      `json:"automatic"`
}

// build makes the journal for what a property received from the warehouse in month and is not posted yet: from the
// end of the last journal posted for that month (or the month's start) up to now, or the month's end once it is over.
func build(ctx context.Context, q pgx.Tx, db *pgxpool.Pool, propertyID int64, month time.Time) (*Journal, error) {
	var qq interface {
		QueryRow(context.Context, string, ...any) pgx.Row
		Query(context.Context, string, ...any) (pgx.Rows, error)
	} = db
	if q != nil {
		qq = q
	}
	j := &Journal{PropertyID: propertyID, Month: clock.YMD(month)[:7], Lines: []Line{}, Problems: []string{}, Parts: []Part{}}
	monthEnd := month.AddDate(0, 1, 0)
	rows, err := qq.Query(ctx, `SELECT number, amount::float8, period_start, period_end, posted_at, automatic FROM zoho_journal WHERE property_id=$1 AND month=$2 ORDER BY period_start`, propertyID, month)
	if err != nil {
		return nil, err
	}
	if j.Parts, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (Part, error) {
		var p Part
		return p, row.Scan(&p.Number, &p.Amount, &p.From, &p.To, &p.PostedAt, &p.Automatic)
	}); err != nil {
		return nil, err
	}
	j.From, j.To = month, clock.Now()
	if n := len(j.Parts); n > 0 {
		j.From = j.Parts[n-1].To
	}
	if !j.To.Before(monthEnd) {
		j.To, j.MonthOver = monthEnd, true
	}
	start, end := j.From, j.To
	if !end.After(start) {
		end = start // nothing can be waiting
	}
	if err := qq.QueryRow(ctx, `SELECT p.name, coalesce(o.tag_id,''), coalesce(m.tag_option_id,'') FROM property p
		LEFT JOIN zoho_map_property m ON m.property_id=p.id LEFT JOIN zoho_tag_option o ON o.option_id=m.tag_option_id WHERE p.id=$1`, propertyID).Scan(&j.Property, &j.tagID, &j.optionID); err != nil {
		return nil, err
	}
	_ = qq.QueryRow(ctx, `SELECT count(*) FROM document WHERE is_transfer AND status='WH_DONE' AND property_id=$1 AND transfer_received_at >= $2 AND transfer_received_at < $3`, propertyID, start, end).Scan(&j.Transfers)
	rows, err = qq.Query(ctx, `SELECT sc.id, sc.name, coalesce(ma.account_id,''), coalesce(za.name,''),
		sum(l.received_qty*l.rate), sum((coalesce(l.dispatched_qty,0)-l.received_qty)*l.rate)
		FROM document d JOIN document_line l ON l.document_id=d.id JOIN sku k ON k.id=l.sku_id JOIN sub_category sc ON sc.id=k.sub_category_id
		LEFT JOIN zoho_map_account ma ON ma.kind='subcat' AND ma.ref=sc.id::text LEFT JOIN zoho_account za ON za.account_id=ma.account_id
		WHERE d.is_transfer AND d.status='WH_DONE' AND d.property_id=$1 AND d.transfer_received_at >= $2 AND d.transfer_received_at < $3
		GROUP BY sc.id, sc.name, ma.account_id, za.name ORDER BY sc.name`, propertyID, start, end)
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
	if month.After(clock.MonthStart(clock.Today())) {
		return httpjson.Bad("That month has not started yet.")
	}
	msg, err := s.Post(r.Context(), u, in.PropertyID, month)
	if err != nil {
		return err
	}
	return httpjson.OK(w, map[string]string{"message": msg})
}

// Post sends a property's journal for what it has received in month and is not posted yet: up to now during the
// month ("Post now"), or to the month's end once it is over. u is nil for the automatic month-end run.
func (s *Service) Post(ctx context.Context, u *access.User, propertyID int64, month time.Time) (string, error) {
	if !s.PushEnabled {
		return "", httpjson.Bad("Posting to Zoho is switched off on this server (ZOHO_PUSH_ENABLED=false).")
	}
	var msg string
	err := pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		// One post at a time per property, so the button and the month-end run cannot cover the same days twice.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(7101, $1::int)`, propertyID); err != nil {
			return err
		}
		j, err := build(ctx, tx, s.DB, propertyID, month)
		if err != nil {
			return err
		}
		if len(j.Problems) > 0 {
			return httpjson.Bad("Cannot post %s yet: %s.", j.Property, strings.Join(j.Problems, "; "))
		}
		if j.Total == 0 {
			return httpjson.Bad("Nothing new to post for %s since %s.", j.Property, j.From.In(clock.IST).Format("2 Jan, 3:04 pm"))
		}
		c, _, err := s.Sync.Client(ctx)
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
		// The month-end journal is dated the month's last day; one posted during the month is dated today.
		date := clock.YMD(month.AddDate(0, 1, -1))
		if !j.MonthOver {
			date = clock.YMD(clock.Today())
		}
		period := fmt.Sprintf("%s to %s", j.From.In(clock.IST).Format("2 Jan 3:04 pm"), j.To.In(clock.IST).Format("2 Jan 3:04 pm"))
		if j.MonthOver {
			period = fmt.Sprintf("%s to the end of %s", j.From.In(clock.IST).Format("2 Jan 3:04 pm"), month.Format("January"))
		}
		ref := fmt.Sprintf("WH-%d-%s-%d", propertyID, j.Month, len(j.Parts)+1)
		body := map[string]any{"journal_date": date, "reference_number": ref, "notes": "Purchase Desk warehouse issues to " + j.Property + ", " + period, "line_items": items}
		jid, number, err := c.CreateJournal(ctx, body)
		if err != nil {
			return httpjson.Bad("Zoho did not accept the journal: %v", err)
		}
		var uid *int64
		if u != nil {
			uid = &u.ID
		}
		if _, err := tx.Exec(ctx, `INSERT INTO zoho_journal(property_id,month,journal_id,number,amount,posted_by,period_start,period_end,automatic) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
			propertyID, month, jid, number, j.Total, uid, j.From, j.To, u == nil); err != nil {
			return err
		}
		act := "Warehouse journal posted"
		if u == nil {
			act = "Warehouse journal posted at month end"
		}
		_ = auditlog.Write(ctx, tx, u, "Zoho", act, number, fmt.Sprintf("%s · %s · %s", j.Property, period, money.INR(j.Total)))
		msg = fmt.Sprintf("%s posted to Zoho for %s (%s, %s)", number, j.Property, period, money.INR(j.Total))
		return nil
	})
	return msg, err
}

// PostMonthEnds is the automatic run: once a month is over, every property's journal for the rest of that month (from
// where its last journal stopped) is posted. It looks back two months, so a run missed while the server was down is
// caught up. A property that cannot be posted (a mapping missing) is left for Finance, and logged.
func (s *Service) PostMonthEnds(ctx context.Context) error {
	if !s.PushEnabled || !s.Sync.Connected(ctx) {
		return nil
	}
	this := clock.MonthStart(clock.Today())
	for _, month := range []time.Time{this.AddDate(0, -1, 0), this.AddDate(0, -2, 0)} {
		rows, err := s.DB.Query(ctx, `SELECT DISTINCT d.property_id FROM document d WHERE d.is_transfer AND d.status='WH_DONE' AND d.transfer_received_at >= $1 AND d.transfer_received_at < $2
			AND d.transfer_received_at >= coalesce((SELECT max(z.period_end) FROM zoho_journal z WHERE z.property_id=d.property_id AND z.month=$1), $1)`, month, month.AddDate(0, 1, 0))
		if err != nil {
			return err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
		if err != nil {
			return err
		}
		for _, id := range ids {
			if msg, err := s.Post(ctx, nil, id, month); err != nil {
				log.Printf("warehouse journal, property %d, %s: %v", id, month.Format("2006-01"), err)
			} else {
				log.Print(msg)
			}
		}
	}
	return nil
}

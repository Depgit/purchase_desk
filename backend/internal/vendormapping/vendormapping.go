// Package vendormapping decides which vendor supplies each item at each property, and at what rate.
// Rules: at a property a SKU has exactly one vendor; a SKU mapping wins over a sub-category mapping;
// two vendors cannot hold the same SKU, or the same sub-category, at the same property.
// It also keeps the vendor rate cards (price lists with validity, history kept).
package vendormapping

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"purchasedesk/internal/access"
	"purchasedesk/internal/auditlog"
	"purchasedesk/internal/clock"
	"purchasedesk/internal/csvfile"
	"purchasedesk/internal/database"
	"purchasedesk/internal/httpjson"
	"purchasedesk/internal/indiantax"
	"purchasedesk/internal/releasephase"
)

type Service struct{ DB *pgxpool.Pool }

func (s *Service) Routes(r chi.Router) {
	r.Get("/rates", httpjson.Wrap(s.listRates))
	r.Post("/rates", httpjson.Wrap(s.createRate))
	r.Get("/rates/format", httpjson.Wrap(s.rateFormat))
	r.Get("/mappings", httpjson.Wrap(s.listMappings))
	r.Post("/mappings", httpjson.Wrap(s.saveMapping))
	r.Put("/mappings/{id}", httpjson.Wrap(s.saveMapping))
	r.Put("/mappings/{id}/estimate", httpjson.Wrap(s.setEstimate))
	r.Get("/mappings/check", httpjson.Wrap(s.checkProperty))
	r.Get("/mappings/check-all", httpjson.Wrap(s.checkAll))
}

// ---------- resolution ----------

// Source says where an item comes from at a property.
type Source struct {
	SKUID       int64    `json:"sku_id"`
	SKUCode     string   `json:"sku_code"`
	ItemName    string   `json:"item_name"`
	Brand       string   `json:"brand"`
	PackSize    string   `json:"pack_size"`
	SubCatID    int64    `json:"sub_category_id"`
	SubCat      string   `json:"sub_category"`
	CategoryID  int64    `json:"category_id"`
	Category    string   `json:"category"`
	SKUGST      float64  `json:"sku_gst"`
	MappingID   *int64   `json:"mapping_id"`
	Level       string   `json:"level"`
	VendorID    *int64   `json:"vendor_id"`
	VendorName  string   `json:"vendor_name"`
	IsWarehouse bool     `json:"is_warehouse"`
	Rate        *float64 `json:"rate"`
	RateGST     *float64 `json:"rate_gst"`
	Estimate    bool     `json:"estimate_allowed"`
	Orderable   bool     `json:"orderable"`
	Status      string   `json:"status"` // ok, none (not mapped), bad (mapped but not usable)
	Problem     string   `json:"problem"`
}

const resolveSQL = `SELECT k.id, k.code, k.item_name, coalesce(b.name,''), ps.name, sc.id, sc.name, c.id, c.name, k.gst_pct,
	m.id, coalesce(m.level,''), m.vendor_id, coalesce(v.legal_name,''), coalesce(v.vendor_type='warehouse',false),
	coalesce(v.active,false), coalesce(v.bank_verified,false), coalesce(v.vendor_type,''), coalesce(m.estimate_allowed,false), rc.rate, rc.gst_pct
	FROM sku k
	JOIN sub_category sc ON sc.id=k.sub_category_id JOIN category c ON c.id=sc.category_id
	LEFT JOIN brand b ON b.id=k.brand_id JOIN pack_size ps ON ps.id=k.pack_size_id
	JOIN property p ON p.id=$1
	LEFT JOIN LATERAL (
		SELECT m.* FROM vendor_mapping m
		WHERE m.active AND ((m.level='sku' AND m.sku_id=k.id) OR (m.level='sc' AND m.sub_category_id=k.sub_category_id))
		  AND (m.all_properties OR EXISTS (SELECT 1 FROM vendor_mapping_property mp WHERE mp.mapping_id=m.id AND mp.property_id=$1))
		ORDER BY (m.level='sku') DESC, m.id LIMIT 1) m ON TRUE
	LEFT JOIN vendor v ON v.id=m.vendor_id
	LEFT JOIN LATERAL (
		SELECT r.rate, r.gst_pct FROM rate_card r
		WHERE r.vendor_id=m.vendor_id AND r.sku_id=k.id AND NOT r.superseded AND current_date BETWEEN r.valid_from AND r.valid_to
		ORDER BY r.valid_from DESC, r.id DESC LIMIT 1) rc ON TRUE
	WHERE k.active AND sc.active AND (p.kind <> 'live' OR k.scope='all' OR p.cluster_id = ANY(k.cluster_ids) OR p.id = ANY(k.property_ids))`

func scanSource(row pgx.CollectableRow) (Source, error) {
	var x Source
	var vActive, vBank bool
	var vType string
	err := row.Scan(&x.SKUID, &x.SKUCode, &x.ItemName, &x.Brand, &x.PackSize, &x.SubCatID, &x.SubCat, &x.CategoryID, &x.Category, &x.SKUGST,
		&x.MappingID, &x.Level, &x.VendorID, &x.VendorName, &x.IsWarehouse, &vActive, &vBank, &vType, &x.Estimate, &x.Rate, &x.RateGST)
	if err != nil {
		return x, err
	}
	switch {
	case x.MappingID == nil:
		x.Status, x.Problem = "none", "Not mapped"
	case !vActive:
		x.Status, x.Problem = "bad", x.VendorName+" is inactive"
	case !vBank:
		x.Status, x.Problem = "bad", x.VendorName+" bank details not verified"
	case vType != "goods" && vType != "warehouse":
		x.Status, x.Problem = "bad", x.VendorName+" is not a goods vendor"
	case x.Level == "sc" && !x.Estimate && x.Rate == nil:
		x.Status, x.Problem = "bad", "No valid rate from "+x.VendorName
	default:
		x.Status, x.Orderable = "ok", true
	}
	if x.Level == "sku" && x.Rate == nil && x.Orderable {
		x.Estimate = true // a SKU mapping without a valid rate takes an estimated rate
	}
	return x, nil
}

// ForProperty resolves every active item available at a property.
func ForProperty(ctx context.Context, q database.Q, propertyID int64) ([]Source, error) {
	rows, err := q.Query(ctx, resolveSQL+` ORDER BY c.name, sc.name, k.code`, propertyID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanSource)
}

// One resolves a single SKU at a property. ok is false if the SKU is not available there.
func One(ctx context.Context, q database.Q, skuID, propertyID int64) (Source, bool, error) {
	rows, err := q.Query(ctx, resolveSQL+` AND k.id=$2`, propertyID, skuID)
	if err != nil {
		return Source{}, false, err
	}
	out, err := pgx.CollectRows(rows, scanSource)
	if err != nil || len(out) == 0 {
		return Source{}, false, err
	}
	return out[0], true, nil
}

// CurrentRate is the vendor's valid rate for a SKU today, if any.
func CurrentRate(ctx context.Context, q database.Q, vendorID, skuID int64) (rate, gst float64, ok bool) {
	err := q.QueryRow(ctx, `SELECT rate, gst_pct FROM rate_card WHERE vendor_id=$1 AND sku_id=$2 AND NOT superseded AND current_date BETWEEN valid_from AND valid_to
		ORDER BY valid_from DESC, id DESC LIMIT 1`, vendorID, skuID).Scan(&rate, &gst)
	return rate, gst, err == nil
}

func (s *Service) checkProperty(w http.ResponseWriter, r *http.Request) error {
	pid := int64(httpjson.QInt(r, "property_id", 0))
	if pid == 0 {
		return httpjson.Bad("Choose a property.")
	}
	out, err := ForProperty(r.Context(), s.DB, pid)
	if err != nil {
		return err
	}
	return httpjson.OK(w, out)
}

func (s *Service) checkAll(w http.ResponseWriter, r *http.Request) error {
	rows, err := s.DB.Query(r.Context(), `SELECT p.id, p.code, p.name, k.name FROM property p JOIN cluster k ON k.id=p.cluster_id WHERE p.active ORDER BY p.code`)
	if err != nil {
		return err
	}
	type prop struct {
		id                  int64
		code, name, cluster string
	}
	props, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (prop, error) {
		var p prop
		return p, row.Scan(&p.id, &p.code, &p.name, &p.cluster)
	})
	if err != nil {
		return err
	}
	out := [][]string{}
	for _, p := range props {
		src, err := ForProperty(r.Context(), s.DB, p.id)
		if err != nil {
			return err
		}
		for _, x := range src {
			vendor, through, rate := x.Problem, "", ""
			if x.Orderable {
				vendor = x.VendorName
			}
			if x.Level == "sku" {
				through = "SKU mapping"
			} else if x.Level == "sc" {
				through = "Sub-category mapping"
			}
			if x.Rate != nil {
				rate = fmt.Sprintf("%.2f", *x.Rate)
			}
			out = append(out, []string{p.code, p.name, p.cluster, x.SKUCode, x.Brand, x.ItemName, x.PackSize, x.SubCat, x.Category, vendor, through, rate})
		}
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="Vendor-check-all-properties-%s.csv"`, clock.YMD(clock.Now())))
	_, err = w.Write(csvfile.Write([]string{"property_code", "property", "cluster", "sku_code", "brand", "item", "pack_size", "sub_category", "category", "vendor", "mapped_through", "rate_ex_gst"}, out))
	return err
}

// ---------- rate cards ----------

type Rate struct {
	ID        int64     `json:"id"`
	VendorID  int64     `json:"vendor_id"`
	Vendor    string    `json:"vendor"`
	SKUID     int64     `json:"sku_id"`
	SKUCode   string    `json:"sku_code"`
	Item      string    `json:"item"`
	PackSize  string    `json:"pack_size"`
	SubCat    string    `json:"sub_category"`
	Category  string    `json:"category"`
	Rate      float64   `json:"rate"`
	GST       float64   `json:"gst_pct"`
	From      string    `json:"valid_from"`
	To        string    `json:"valid_to"`
	Status    string    `json:"status"`
	DaysLeft  int       `json:"days_left"`
	CreatedAt time.Time `json:"created_at"`
}

// rateStatus: Active, Expiring soon (under 30 days), Expired, Starts later, Superseded.
const rateStatusSQL = `CASE
	WHEN r.superseded OR EXISTS (SELECT 1 FROM rate_card n WHERE n.vendor_id=r.vendor_id AND n.sku_id=r.sku_id AND NOT n.superseded AND n.id<>r.id
		AND n.valid_from > r.valid_from AND n.valid_from <= current_date) THEN 'Superseded'
	WHEN r.valid_to < current_date THEN 'Expired'
	WHEN r.valid_from > current_date THEN 'Starts later'
	WHEN r.valid_to - current_date < 30 THEN 'Expiring soon'
	ELSE 'Active' END`

func (s *Service) listRates(w http.ResponseWriter, r *http.Request) error {
	vid := int64(httpjson.QInt(r, "vendor_id", 0))
	rows, err := s.DB.Query(r.Context(), `SELECT r.id, r.vendor_id, v.legal_name, r.sku_id, k.code, concat_ws(' ', b.name, k.item_name), ps.name, sc.name, c.name, r.rate, r.gst_pct,
		r.valid_from::text, r.valid_to::text, `+rateStatusSQL+`, (r.valid_to - current_date), r.created_at
		FROM rate_card r JOIN vendor v ON v.id=r.vendor_id JOIN sku k ON k.id=r.sku_id LEFT JOIN brand b ON b.id=k.brand_id JOIN pack_size ps ON ps.id=k.pack_size_id
		JOIN sub_category sc ON sc.id=k.sub_category_id JOIN category c ON c.id=sc.category_id
		WHERE ($1=0 OR r.vendor_id=$1) ORDER BY v.legal_name, k.code, r.valid_from DESC, r.id DESC`, vid)
	if err != nil {
		return err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Rate, error) {
		var x Rate
		return x, row.Scan(&x.ID, &x.VendorID, &x.Vendor, &x.SKUID, &x.SKUCode, &x.Item, &x.PackSize, &x.SubCat, &x.Category, &x.Rate, &x.GST, &x.From, &x.To, &x.Status, &x.DaysLeft, &x.CreatedAt)
	})
	if err != nil {
		return err
	}
	return httpjson.OK(w, out)
}

// RateInput is one rate from the form or an upload row.
type RateInput struct {
	VendorID int64   `json:"vendor_id"`
	SKUID    int64   `json:"sku_id"`
	Rate     float64 `json:"rate"`
	GST      float64 `json:"gst_pct"`
	From     string  `json:"valid_from"`
	To       string  `json:"valid_to"`
}

// CheckRate validates a rate and returns a message describing what saving it does.
func CheckRate(ctx context.Context, q database.Q, in RateInput) (string, []string) {
	errs := []string{}
	var vType string
	var vActive bool
	if err := q.QueryRow(ctx, `SELECT vendor_type, active FROM vendor WHERE id=$1`, in.VendorID).Scan(&vType, &vActive); err != nil || (vType != "goods" && vType != "warehouse") || !releasephase.VendorTypeOn(vType) {
		return "", []string{"Unknown goods vendor."}
	}
	if !vActive {
		errs = append(errs, "Vendor is inactive.")
	}
	var skuActive, approved bool
	var subName string
	if err := q.QueryRow(ctx, `SELECT k.active, sc.name, EXISTS(SELECT 1 FROM vendor_sub_category x WHERE x.vendor_id=$1 AND x.sub_category_id=k.sub_category_id)
		FROM sku k JOIN sub_category sc ON sc.id=k.sub_category_id WHERE k.id=$2`, in.VendorID, in.SKUID).Scan(&skuActive, &subName, &approved); err != nil {
		return "", []string{"Unknown SKU."}
	}
	if !skuActive {
		errs = append(errs, "SKU is inactive.")
	}
	if !approved && vType == "goods" {
		errs = append(errs, "Vendor is not approved for "+subName+".")
	}
	if in.Rate <= 0 {
		errs = append(errs, "Rate must be a number above 0.")
	}
	if !indiantax.ValidRate(in.GST) {
		errs = append(errs, "GST % must be 0, 5, 12, 18, 28 or 40.")
	}
	f, ok1 := clock.ParseDate(in.From)
	t, ok2 := clock.ParseDate(in.To)
	if !ok1 || !ok2 {
		errs = append(errs, "Dates must be YYYY-MM-DD.")
	} else if t.Before(f) {
		errs = append(errs, "Valid to is before valid from.")
	}
	if len(errs) > 0 {
		return "", errs
	}
	if cur, _, ok := CurrentRate(ctx, q, in.VendorID, in.SKUID); ok {
		return fmt.Sprintf("Replaces current rate %.2f", cur), nil
	}
	return "New rate", nil
}

// AddRate saves a checked rate. Rates for the same vendor and SKU that start on or after it are superseded;
// an older rate keeps applying until this one starts.
func AddRate(ctx context.Context, tx pgx.Tx, u *access.User, in RateInput) error {
	if _, err := tx.Exec(ctx, `UPDATE rate_card SET superseded=TRUE WHERE vendor_id=$1 AND sku_id=$2 AND NOT superseded AND valid_from >= $3::date`, in.VendorID, in.SKUID, in.From); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO rate_card(vendor_id,sku_id,rate,gst_pct,valid_from,valid_to,created_by) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		in.VendorID, in.SKUID, in.Rate, in.GST, in.From, in.To, u.ID); err != nil {
		return err
	}
	var label string
	_ = tx.QueryRow(ctx, `SELECT v.code||' / '||k.code FROM vendor v, sku k WHERE v.id=$1 AND k.id=$2`, in.VendorID, in.SKUID).Scan(&label)
	return auditlog.Write(ctx, tx, u, "Vendor Rates", "Rate saved", label, fmt.Sprintf("%.2f + %v%% · %s to %s", in.Rate, in.GST, in.From, in.To))
}

func (s *Service) createRate(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	if err := access.NeedRight(u, access.Masters, "edit masters"); err != nil {
		return err
	}
	var in RateInput
	if err := httpjson.Decode(r, &in); err != nil {
		return err
	}
	if _, errs := CheckRate(r.Context(), s.DB, in); len(errs) > 0 {
		return httpjson.Invalid(errs)
	}
	if err := pgx.BeginFunc(r.Context(), s.DB, func(tx pgx.Tx) error { return AddRate(r.Context(), tx, u, in) }); err != nil {
		return err
	}
	return httpjson.OK(w, map[string]bool{"ok": true})
}

// rateFormat downloads the file a vendor fills: every SKU it is approved for, with the current rate.
func (s *Service) rateFormat(w http.ResponseWriter, r *http.Request) error {
	vid := int64(httpjson.QInt(r, "vendor_id", 0))
	cid := int64(httpjson.QInt(r, "category_id", 0))
	var vcode, vname string
	if err := s.DB.QueryRow(r.Context(), `SELECT code, legal_name FROM vendor WHERE id=$1`, vid).Scan(&vcode, &vname); err != nil {
		return httpjson.Bad("Choose a vendor.")
	}
	rows, err := s.DB.Query(r.Context(), `SELECT k.code, coalesce(b.name,''), k.item_name, ps.name, k.gst_pct,
		(SELECT r.rate::text FROM rate_card r WHERE r.vendor_id=$1 AND r.sku_id=k.id AND NOT r.superseded AND current_date BETWEEN r.valid_from AND r.valid_to ORDER BY r.valid_from DESC LIMIT 1)
		FROM sku k LEFT JOIN brand b ON b.id=k.brand_id JOIN pack_size ps ON ps.id=k.pack_size_id JOIN sub_category sc ON sc.id=k.sub_category_id
		WHERE k.active AND ($2=0 OR sc.category_id=$2) AND EXISTS (SELECT 1 FROM vendor_sub_category x WHERE x.vendor_id=$1 AND x.sub_category_id=k.sub_category_id)
		ORDER BY k.code`, vid, cid)
	if err != nil {
		return err
	}
	out := [][]string{}
	for rows.Next() {
		var code, brand, item, pack string
		var gst float64
		var cur *string
		if err := rows.Scan(&code, &brand, &item, &pack, &gst, &cur); err != nil {
			return err
		}
		c := ""
		if cur != nil {
			c = *cur
		}
		out = append(out, []string{vcode, vname, code, brand, item, pack, c, "", fmt.Sprint(gst), "", ""})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="Rate-format-%s-%s.csv"`, slug(vname), clock.YMD(clock.Now())))
	_, err = w.Write(csvfile.Write([]string{"vendor_code", "vendor_name", "sku_code", "brand", "item_name", "unit", "current_rate", "rate_ex_gst", "gst_percent", "valid_from", "valid_to"}, out))
	return err
}

func slug(s string) string {
	s = strings.ReplaceAll(s, "&", "and")
	out := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			return r
		}
		return '-'
	}, s)
	for strings.Contains(out, "--") {
		out = strings.ReplaceAll(out, "--", "-")
	}
	return strings.Trim(out, "-")
}

// ---------- mappings ----------

type Mapping struct {
	ID          int64   `json:"id"`
	VendorID    int64   `json:"vendor_id"`
	Vendor      string  `json:"vendor"`
	VendorOK    bool    `json:"vendor_usable"`
	Level       string  `json:"level"`
	SKUID       *int64  `json:"sku_id"`
	SKUCode     string  `json:"sku_code"`
	Item        string  `json:"item"`
	SubCatID    int64   `json:"sub_category_id"`
	SubCat      string  `json:"sub_category"`
	Category    string  `json:"category"`
	All         bool    `json:"all_properties"`
	PropertyIDs []int64 `json:"property_ids"`
	Estimate    bool    `json:"estimate_allowed"`
	Active      bool    `json:"active"`
	RatedSKUs   int     `json:"rated_skus"`
	TotalSKUs   int     `json:"total_skus"`
	HasRate     bool    `json:"has_rate"`
}

func (s *Service) listMappings(w http.ResponseWriter, r *http.Request) error {
	level := r.URL.Query().Get("level")
	rows, err := s.DB.Query(r.Context(), `SELECT m.id, m.vendor_id, v.legal_name, v.active AND v.bank_verified, m.level, m.sku_id, coalesce(k.code,''), coalesce(concat_ws(' ', b.name, k.item_name)||' · '||ps.name,''),
		sc.id, sc.name, c.name, m.all_properties, coalesce((SELECT array_agg(property_id ORDER BY property_id) FROM vendor_mapping_property WHERE mapping_id=m.id),'{}'),
		m.estimate_allowed, m.active,
		CASE WHEN m.level='sc' THEN (SELECT count(*) FROM sku x WHERE x.sub_category_id=sc.id AND x.active AND EXISTS (SELECT 1 FROM rate_card rc WHERE rc.vendor_id=m.vendor_id AND rc.sku_id=x.id AND NOT rc.superseded AND current_date BETWEEN rc.valid_from AND rc.valid_to)) ELSE 0 END,
		CASE WHEN m.level='sc' THEN (SELECT count(*) FROM sku x WHERE x.sub_category_id=sc.id AND x.active) ELSE 1 END,
		CASE WHEN m.level='sku' THEN EXISTS (SELECT 1 FROM rate_card rc WHERE rc.vendor_id=m.vendor_id AND rc.sku_id=m.sku_id AND NOT rc.superseded AND current_date BETWEEN rc.valid_from AND rc.valid_to) ELSE FALSE END
		FROM vendor_mapping m JOIN vendor v ON v.id=m.vendor_id
		LEFT JOIN sku k ON k.id=m.sku_id LEFT JOIN brand b ON b.id=k.brand_id LEFT JOIN pack_size ps ON ps.id=k.pack_size_id
		JOIN sub_category sc ON sc.id=coalesce(m.sub_category_id, k.sub_category_id) JOIN category c ON c.id=sc.category_id
		WHERE ($1='' OR m.level=$1) ORDER BY m.active DESC, v.legal_name, sc.name, k.code`, level)
	if err != nil {
		return err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Mapping, error) {
		var x Mapping
		return x, row.Scan(&x.ID, &x.VendorID, &x.Vendor, &x.VendorOK, &x.Level, &x.SKUID, &x.SKUCode, &x.Item, &x.SubCatID, &x.SubCat, &x.Category,
			&x.All, &x.PropertyIDs, &x.Estimate, &x.Active, &x.RatedSKUs, &x.TotalSKUs, &x.HasRate)
	})
	if err != nil {
		return err
	}
	return httpjson.OK(w, out)
}

type mappingIn struct {
	VendorID      int64   `json:"vendor_id"`
	Level         string  `json:"level"`
	SKUID         int64   `json:"sku_id"`
	SubCategoryID int64   `json:"sub_category_id"`
	All           bool    `json:"all_properties"`
	ClusterIDs    []int64 `json:"cluster_ids"`
	PropertyIDs   []int64 `json:"property_ids"`
	Estimate      bool    `json:"estimate_allowed"`
}

func (s *Service) saveMapping(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	if err := access.NeedRight(u, access.Masters, "edit masters"); err != nil {
		return err
	}
	var in mappingIn
	if err := httpjson.Decode(r, &in); err != nil {
		return err
	}
	ctx := r.Context()
	id := int64(0)
	if chi.URLParam(r, "id") != "" {
		var err error
		if id, err = httpjson.ID(r, "id"); err != nil {
			return err
		}
		var sku, sc *int64
		if err := s.DB.QueryRow(ctx, `SELECT vendor_id, level, sku_id, sub_category_id FROM vendor_mapping WHERE id=$1 AND active`, id).Scan(&in.VendorID, &in.Level, &sku, &sc); err != nil {
			return httpjson.Bad("This mapping is inactive. Mark it active first.")
		}
		if sku != nil {
			in.SKUID = *sku
		}
		if sc != nil {
			in.SubCategoryID = *sc
		}
	}
	if in.Level != "sku" && in.Level != "sc" {
		return httpjson.Bad("Map by whole sub-category or by one SKU.")
	}
	var vType string
	if err := s.DB.QueryRow(ctx, `SELECT vendor_type FROM vendor WHERE id=$1 AND active`, in.VendorID).Scan(&vType); err != nil || (vType != "goods" && vType != "warehouse") || !releasephase.VendorTypeOn(vType) {
		return httpjson.Bad("Choose an active goods vendor.")
	}
	if in.Level == "sku" {
		if err := s.DB.QueryRow(ctx, `SELECT sub_category_id FROM sku WHERE id=$1`, in.SKUID).Scan(&in.SubCategoryID); err != nil {
			return httpjson.Bad("Choose the SKU.")
		}
	} else if in.SubCategoryID == 0 {
		return httpjson.Bad("Choose the sub-category.")
	}
	props := []int64{}
	if !in.All {
		rows, err := s.DB.Query(ctx, `SELECT id FROM property WHERE (active AND kind='live' AND cluster_id = ANY($1)) OR id = ANY($2)`, in.ClusterIDs, in.PropertyIDs)
		if err != nil {
			return err
		}
		props, err = pgx.CollectRows(rows, pgx.RowTo[int64])
		if err != nil {
			return err
		}
		if len(props) == 0 {
			return httpjson.Bad("Tick at least one cluster or property.")
		}
	}
	// Block a second vendor for the same SKU or sub-category at the same property.
	var clashVendor string
	var clashProps []string
	err := s.DB.QueryRow(ctx, `SELECT v.legal_name, coalesce(array_agg(DISTINCT p.name) FILTER (WHERE p.id IS NOT NULL),'{}')
		FROM vendor_mapping m JOIN vendor v ON v.id=m.vendor_id
		LEFT JOIN vendor_mapping_property mp ON mp.mapping_id=m.id AND ($5 OR mp.property_id = ANY($6))
		LEFT JOIN property p ON p.id=mp.property_id
		WHERE m.active AND m.id<>$1 AND m.level=$2 AND (($2='sku' AND m.sku_id=$3) OR ($2='sc' AND m.sub_category_id=$4))
		  AND (m.all_properties OR $5 OR EXISTS (SELECT 1 FROM vendor_mapping_property x WHERE x.mapping_id=m.id AND x.property_id = ANY($6)))
		GROUP BY v.legal_name LIMIT 1`, id, in.Level, in.SKUID, in.SubCategoryID, in.All, props).Scan(&clashVendor, &clashProps)
	if err == nil {
		where := "these properties"
		if len(clashProps) > 0 && len(clashProps) <= 3 {
			where = strings.Join(clashProps, ", ")
		} else if len(clashProps) > 3 {
			where = fmt.Sprintf("%d of these properties", len(clashProps))
		}
		return httpjson.Bad("%s is already mapped to this item at %s. One item has one vendor at a property: untick those properties here, or change that mapping first.", clashVendor, where)
	} else if err != pgx.ErrNoRows {
		return err
	}
	est := in.Estimate && in.Level == "sc"
	err = pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		if id == 0 {
			var sku, sc any
			if in.Level == "sku" {
				sku = in.SKUID
			} else {
				sc = in.SubCategoryID
			}
			if err := tx.QueryRow(ctx, `INSERT INTO vendor_mapping(vendor_id,level,sku_id,sub_category_id,all_properties,estimate_allowed,created_by) VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING id`,
				in.VendorID, in.Level, sku, sc, in.All, est, u.ID).Scan(&id); err != nil {
				return err
			}
			if vType == "goods" {
				if _, err := tx.Exec(ctx, `INSERT INTO vendor_sub_category(vendor_id,sub_category_id) VALUES ($1,$2) ON CONFLICT DO NOTHING`, in.VendorID, in.SubCategoryID); err != nil {
					return err
				}
			}
		} else {
			if _, err := tx.Exec(ctx, `UPDATE vendor_mapping SET all_properties=$2, estimate_allowed=$3 WHERE id=$1`, id, in.All, est); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `DELETE FROM vendor_mapping_property WHERE mapping_id=$1`, id); err != nil {
				return err
			}
		}
		if !in.All {
			if _, err := tx.Exec(ctx, `INSERT INTO vendor_mapping_property(mapping_id,property_id) SELECT $1, unnest($2::bigint[])`, id, props); err != nil {
				return err
			}
		}
		where := "all properties"
		if !in.All {
			where = fmt.Sprintf("%d properties", len(props))
		}
		act := "Vendor mapped"
		if chi.URLParam(r, "id") != "" {
			act = "Mapping changed"
		}
		return auditlog.Write(ctx, tx, u, "Vendor Mapping", act, fmt.Sprintf("M%03d", id), fmt.Sprintf("vendor %d · %s · %s", in.VendorID, in.Level, where))
	})
	if err != nil {
		return err
	}
	return httpjson.OK(w, map[string]int64{"id": id})
}

func (s *Service) setEstimate(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	if err := access.NeedRight(u, access.Masters, "edit masters"); err != nil {
		return err
	}
	id, err := httpjson.ID(r, "id")
	if err != nil {
		return err
	}
	var in struct {
		Estimate bool `json:"estimate_allowed"`
	}
	if err := httpjson.Decode(r, &in); err != nil {
		return err
	}
	if _, err := s.DB.Exec(r.Context(), `UPDATE vendor_mapping SET estimate_allowed=$2 WHERE id=$1 AND level='sc'`, id, in.Estimate); err != nil {
		return err
	}
	rule := "SKU rate needed"
	if in.Estimate {
		rule = "estimate allowed"
	}
	_ = auditlog.Write(r.Context(), s.DB, u, "Vendor Mapping", "Rate rule changed", fmt.Sprintf("M%03d", id), rule)
	return httpjson.OK(w, map[string]bool{"ok": true})
}

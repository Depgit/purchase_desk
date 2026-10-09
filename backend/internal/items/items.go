// Package items manages the item hierarchy: category, sub-category, SKU, plus brands and pack sizes.
// One SKU = sub-category + brand + item name + pack size, and the same combination cannot exist twice.
// Brand is optional (services have none). The code field hsn holds an HSN code for goods or a SAC code for services.
package items

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
	"purchasedesk/internal/textclean"
)

type Service struct{ DB *pgxpool.Pool }

func (s *Service) Routes(r chi.Router) {
	r.Get("/categories", httpjson.Wrap(s.listCategories))
	r.Post("/categories", httpjson.Wrap(s.saveCategory))
	r.Put("/categories/{id}", httpjson.Wrap(s.saveCategory))
	r.Get("/sub-categories", httpjson.Wrap(s.listSubCategories))
	r.Post("/sub-categories", httpjson.Wrap(s.saveSubCategory))
	r.Put("/sub-categories/{id}", httpjson.Wrap(s.saveSubCategory))
	r.Get("/brands", httpjson.Wrap(s.listBrands))
	r.Post("/brands", httpjson.Wrap(s.createBrand))
	r.Put("/brands/{id}", httpjson.Wrap(s.createBrand))
	r.Get("/pack-sizes", httpjson.Wrap(s.listPackSizes))
	r.Post("/pack-sizes", httpjson.Wrap(s.createPackSize))
	r.Put("/pack-sizes/{id}", httpjson.Wrap(s.createPackSize))
	r.Get("/skus", httpjson.Wrap(s.listSKUs))
	r.Post("/skus", httpjson.Wrap(s.saveSKU))
	r.Put("/skus/{id}", httpjson.Wrap(s.saveSKU))
}

type Category struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	IsFB     bool   `json:"is_fb"`
	SubCount int    `json:"sub_categories"`
	SKUCount int    `json:"skus"`
}

func (s *Service) listCategories(w http.ResponseWriter, r *http.Request) error {
	rows, err := s.DB.Query(r.Context(), `SELECT c.id, c.name, c.is_fb,
		(SELECT count(*) FROM sub_category sc WHERE sc.category_id=c.id),
		(SELECT count(*) FROM sku JOIN sub_category sc ON sc.id=sku.sub_category_id WHERE sc.category_id=c.id)
		FROM category c ORDER BY c.name`)
	if err != nil {
		return err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Category, error) {
		var c Category
		return c, row.Scan(&c.ID, &c.Name, &c.IsFB, &c.SubCount, &c.SKUCount)
	})
	if err != nil {
		return err
	}
	return httpjson.OK(w, out)
}

// saveCategory adds a category, or edits its name and F&B flag (PUT /categories/{id}).
func (s *Service) saveCategory(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	if err := access.NeedRight(u, access.Masters, "edit masters"); err != nil {
		return err
	}
	var in struct {
		Name string `json:"name"`
		IsFB bool   `json:"is_fb"`
	}
	if err := httpjson.Decode(r, &in); err != nil {
		return err
	}
	in.Name = textclean.Clean(in.Name)
	if len(in.Name) < 3 || strings.ContainsAny(in.Name, `<>",`) {
		return httpjson.Bad("Enter the category name, without commas.")
	}
	id := int64(0)
	if chi.URLParam(r, "id") != "" {
		var err error
		if id, err = httpjson.ID(r, "id"); err != nil {
			return err
		}
	}
	var dup bool
	_ = s.DB.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM category WHERE norm_key(name)=norm_key($1) AND id<>$2)`, in.Name, id).Scan(&dup)
	if dup {
		return httpjson.Bad("This category already exists.")
	}
	if id != 0 {
		var old string
		var oldFB bool
		if err := s.DB.QueryRow(r.Context(), `SELECT name, is_fb FROM category WHERE id=$1`, id).Scan(&old, &oldFB); err != nil {
			return httpjson.NotFound("Category")
		}
		if _, err := s.DB.Exec(r.Context(), `UPDATE category SET name=$2, is_fb=$3 WHERE id=$1`, id, in.Name, in.IsFB); err != nil {
			return err
		}
		_ = auditlog.Write(r.Context(), s.DB, u, "Items", "Category edited", in.Name, fmt.Sprintf("%s, F&B %v → %s, F&B %v", old, oldFB, in.Name, in.IsFB))
		return httpjson.OK(w, map[string]int64{"id": id})
	}
	if err := s.DB.QueryRow(r.Context(), `INSERT INTO category(name,is_fb,created_by) VALUES ($1,$2,$3) RETURNING id`, in.Name, in.IsFB, u.ID).Scan(&id); err != nil {
		return err
	}
	_ = auditlog.Write(r.Context(), s.DB, u, "Items", "Category added", in.Name, "")
	return httpjson.OK(w, map[string]int64{"id": id})
}

type SubCategory struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Code       string `json:"code"`
	CategoryID int64  `json:"category_id"`
	Category   string `json:"category"`
	IsFB       bool   `json:"is_fb"`
	Active     bool   `json:"active"`
	SKUCount   int    `json:"skus"`
	Vendors    int    `json:"vendors"`
}

func (s *Service) listSubCategories(w http.ResponseWriter, r *http.Request) error {
	rows, err := s.DB.Query(r.Context(), `SELECT sc.id, sc.name, sc.code, sc.category_id, c.name, c.is_fb, sc.active,
		(SELECT count(*) FROM sku WHERE sku.sub_category_id=sc.id), (SELECT count(*) FROM vendor_sub_category v WHERE v.sub_category_id=sc.id)
		FROM sub_category sc JOIN category c ON c.id=sc.category_id ORDER BY c.name, sc.name`)
	if err != nil {
		return err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (SubCategory, error) {
		var x SubCategory
		return x, row.Scan(&x.ID, &x.Name, &x.Code, &x.CategoryID, &x.Category, &x.IsFB, &x.Active, &x.SKUCount, &x.Vendors)
	})
	if err != nil {
		return err
	}
	return httpjson.OK(w, out)
}

var reserved = map[string]bool{"PO": true, "PR": true, "WT": true, "BR": true, "GR": true}

func (s *Service) saveSubCategory(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	if err := access.NeedRight(u, access.Masters, "edit masters"); err != nil {
		return err
	}
	var in struct {
		Name       string `json:"name"`
		CategoryID int64  `json:"category_id"`
		Code       string `json:"code"`
	}
	if err := httpjson.Decode(r, &in); err != nil {
		return err
	}
	in.Name, in.Code = textclean.Clean(in.Name), textclean.Upper(in.Code)
	id := int64(0)
	if chi.URLParam(r, "id") != "" {
		var err error
		if id, err = httpjson.ID(r, "id"); err != nil {
			return err
		}
	}
	if len(in.Name) < 3 || strings.ContainsAny(in.Name, `<>",`) {
		return httpjson.Bad("Enter the sub-category name, without commas.")
	}
	var dup bool
	_ = s.DB.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM sub_category WHERE norm_key(name)=norm_key($1) AND id<>$2)`, in.Name, id).Scan(&dup)
	if dup {
		return httpjson.Bad("A sub-category with this name already exists.")
	}
	if id != 0 {
		// The 2-letter code is fixed once SKUs carry it; the name and the category can change.
		var old, oldCat string
		var catID int64
		if err := s.DB.QueryRow(r.Context(), `SELECT s.name, s.category_id, c.name FROM sub_category s JOIN category c ON c.id=s.category_id WHERE s.id=$1`, id).Scan(&old, &catID, &oldCat); err != nil {
			return httpjson.NotFound("Sub-category")
		}
		if in.CategoryID != 0 && in.CategoryID != catID {
			var ok bool
			_ = s.DB.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM category WHERE id=$1)`, in.CategoryID).Scan(&ok)
			if !ok {
				return httpjson.Bad("Choose a category from the list.")
			}
			catID = in.CategoryID
		}
		if _, err := s.DB.Exec(r.Context(), `UPDATE sub_category SET name=$2, category_id=$3 WHERE id=$1`, id, in.Name, catID); err != nil {
			return err
		}
		var newCat string
		_ = s.DB.QueryRow(r.Context(), `SELECT name FROM category WHERE id=$1`, catID).Scan(&newCat)
		_ = auditlog.Write(r.Context(), s.DB, u, "Items", "Sub-category edited", in.Name, old+" ("+oldCat+") → "+in.Name+" ("+newCat+")")
		return httpjson.OK(w, map[string]int64{"id": id})
	}
	if len(in.Code) != 2 || strings.Trim(in.Code, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") != "" {
		return httpjson.Bad("SKU code letters must be exactly 2 letters.")
	}
	if reserved[in.Code] {
		return httpjson.Bad("These letters are used for document numbers. Choose another pair.")
	}
	var used string
	_ = s.DB.QueryRow(r.Context(), `SELECT name FROM sub_category WHERE code=$1`, in.Code).Scan(&used)
	if used != "" {
		return httpjson.Bad("These letters are already used by %s. Choose another pair.", used)
	}
	if err := s.DB.QueryRow(r.Context(), `INSERT INTO sub_category(name,category_id,code,created_by) VALUES ($1,$2,$3,$4) RETURNING id`, in.Name, in.CategoryID, in.Code, u.ID).Scan(&id); err != nil {
		return err
	}
	_ = auditlog.Write(r.Context(), s.DB, u, "Items", "Sub-category added", in.Name, "code "+in.Code)
	return httpjson.OK(w, map[string]int64{"id": id})
}

type Named struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Active bool   `json:"active"`
	SKUs   int    `json:"skus"`
}

func (s *Service) listNamed(w http.ResponseWriter, r *http.Request, table, fk string) error {
	rows, err := s.DB.Query(r.Context(), fmt.Sprintf(`SELECT t.id, t.name, t.active, (SELECT count(*) FROM sku WHERE sku.%s=t.id) FROM %s t ORDER BY t.name`, fk, table))
	if err != nil {
		return err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Named, error) {
		var x Named
		return x, row.Scan(&x.ID, &x.Name, &x.Active, &x.SKUs)
	})
	if err != nil {
		return err
	}
	return httpjson.OK(w, out)
}

func (s *Service) listBrands(w http.ResponseWriter, r *http.Request) error {
	return s.listNamed(w, r, "brand", "brand_id")
}
func (s *Service) listPackSizes(w http.ResponseWriter, r *http.Request) error {
	return s.listNamed(w, r, "pack_size", "pack_size_id")
}

func (s *Service) createNamed(w http.ResponseWriter, r *http.Request, table, label string) error {
	u := access.From(r)
	if err := access.NeedRight(u, access.Masters, "edit masters"); err != nil {
		return err
	}
	var in struct {
		Name string `json:"name"`
	}
	if err := httpjson.Decode(r, &in); err != nil {
		return err
	}
	in.Name = textclean.Clean(in.Name)
	if len(in.Name) < 2 || strings.ContainsAny(in.Name, `<>",`) {
		return httpjson.Bad("Enter the %s, without commas.", strings.ToLower(label))
	}
	id := int64(0)
	if chi.URLParam(r, "id") != "" {
		var err error
		if id, err = httpjson.ID(r, "id"); err != nil {
			return err
		}
	}
	var existing string
	var active bool
	_ = s.DB.QueryRow(r.Context(), fmt.Sprintf(`SELECT name, active FROM %s WHERE norm_key(name)=norm_key($1) AND id<>$2`, table), in.Name, id).Scan(&existing, &active)
	if existing != "" {
		if !active {
			existing += " (inactive)"
		}
		return httpjson.Bad("This %s already exists as %s.", strings.ToLower(label), existing)
	}
	if id != 0 {
		var old string
		if err := s.DB.QueryRow(r.Context(), fmt.Sprintf(`SELECT name FROM %s WHERE id=$1`, table), id).Scan(&old); err != nil {
			return httpjson.NotFound(label)
		}
		if _, err := s.DB.Exec(r.Context(), fmt.Sprintf(`UPDATE %s SET name=$2 WHERE id=$1`, table), id, in.Name); err != nil {
			return err
		}
		_ = auditlog.Write(r.Context(), s.DB, u, "Items", label+" renamed", in.Name, old+" → "+in.Name)
		return httpjson.OK(w, map[string]int64{"id": id})
	}
	if err := s.DB.QueryRow(r.Context(), fmt.Sprintf(`INSERT INTO %s(name,created_by) VALUES ($1,$2) RETURNING id`, table), in.Name, u.ID).Scan(&id); err != nil {
		return err
	}
	_ = auditlog.Write(r.Context(), s.DB, u, "Items", label+" added", in.Name, "")
	return httpjson.OK(w, map[string]int64{"id": id})
}

func (s *Service) createBrand(w http.ResponseWriter, r *http.Request) error {
	return s.createNamed(w, r, "brand", "Brand")
}
func (s *Service) createPackSize(w http.ResponseWriter, r *http.Request) error {
	return s.createNamed(w, r, "pack_size", "Pack size")
}

// ---------- SKUs ----------

type SKU struct {
	ID            int64   `json:"id"`
	Code          string  `json:"code"`
	SubCategoryID int64   `json:"sub_category_id"`
	SubCategory   string  `json:"sub_category"`
	CategoryID    int64   `json:"category_id"`
	Category      string  `json:"category"`
	BrandID       int64   `json:"brand_id"`
	Brand         string  `json:"brand"`
	ItemName      string  `json:"item_name"`
	PackSizeID    int64   `json:"pack_size_id"`
	PackSize      string  `json:"pack_size"`
	GST           float64 `json:"gst_pct"`
	HSN           string  `json:"hsn"`
	Scope         string  `json:"scope"`
	ClusterIDs    []int64 `json:"cluster_ids"`
	PropertyIDs   []int64 `json:"property_ids"`
	Active        bool    `json:"active"`
	RatedVendors  int     `json:"rated_vendors"`
}

func (s *Service) listSKUs(w http.ResponseWriter, r *http.Request) error {
	rows, err := s.DB.Query(r.Context(), `SELECT k.id, k.code, k.sub_category_id, sc.name, c.id, c.name, coalesce(k.brand_id,0), coalesce(b.name,''), k.item_name, k.pack_size_id, ps.name,
		k.gst_pct, k.hsn, k.scope, k.cluster_ids, k.property_ids, k.active,
		(SELECT count(DISTINCT rc.vendor_id) FROM rate_card rc WHERE rc.sku_id=k.id AND NOT rc.superseded AND current_date BETWEEN rc.valid_from AND rc.valid_to)
		FROM sku k JOIN sub_category sc ON sc.id=k.sub_category_id JOIN category c ON c.id=sc.category_id
		LEFT JOIN brand b ON b.id=k.brand_id JOIN pack_size ps ON ps.id=k.pack_size_id ORDER BY k.code`)
	if err != nil {
		return err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (SKU, error) {
		var x SKU
		return x, row.Scan(&x.ID, &x.Code, &x.SubCategoryID, &x.SubCategory, &x.CategoryID, &x.Category, &x.BrandID, &x.Brand, &x.ItemName,
			&x.PackSizeID, &x.PackSize, &x.GST, &x.HSN, &x.Scope, &x.ClusterIDs, &x.PropertyIDs, &x.Active, &x.RatedVendors)
	})
	if err != nil {
		return err
	}
	return httpjson.OK(w, out)
}

// SKUInput is what a form or an upload row gives for a new or edited SKU.
type SKUInput struct {
	SubCategoryID int64   `json:"sub_category_id"`
	BrandName     string  `json:"brand"`
	ItemName      string  `json:"item_name"`
	PackSizeID    int64   `json:"pack_size_id"`
	GST           float64 `json:"gst_pct"`
	HSN           string  `json:"hsn"`
	Scope         string  `json:"scope"`
	ClusterIDs    []int64 `json:"cluster_ids"`
	PropertyIDs   []int64 `json:"property_ids"`

	brandID int64 // filled by Validate when the brand already exists
}

// NewBrand reports whether saving will create a new brand.
func (in *SKUInput) NewBrand() bool { return in.brandID == 0 && in.BrandName != "" }

// brandRef is the value stored in sku.brand_id: the brand's id, or NULL for an item without a brand.
func (in *SKUInput) brandRef() any {
	if in.brandID == 0 {
		return nil
	}
	return in.brandID
}

// Validate tidies the input and returns every problem found. selfID is the SKU being edited, or 0.
func Validate(ctx context.Context, q database.Q, in *SKUInput, selfID int64) []string {
	errs := []string{}
	in.ItemName = textclean.Clean(in.ItemName)
	in.BrandName = textclean.Clean(in.BrandName)
	in.HSN = strings.TrimSpace(in.HSN)
	if in.Scope == "" {
		in.Scope = "all"
	}
	var scActive bool
	var scName string
	err := q.QueryRow(ctx, `SELECT name, active FROM sub_category WHERE id=$1`, in.SubCategoryID).Scan(&scName, &scActive)
	if err != nil {
		errs = append(errs, "Choose a sub-category.")
	} else if !scActive && selfID == 0 {
		errs = append(errs, "Sub-category "+scName+" is inactive.")
	}
	if len(textclean.Key(in.ItemName)) < 3 {
		errs = append(errs, "Item name is too short.")
	}
	var packActive bool
	if err := q.QueryRow(ctx, `SELECT active FROM pack_size WHERE id=$1`, in.PackSizeID).Scan(&packActive); err != nil {
		errs = append(errs, "Pack size is not in the list. Add it under Settings, Item Setup.")
	} else if !packActive && selfID == 0 {
		errs = append(errs, "Pack size is inactive.")
	}
	if !indiantax.ValidRate(in.GST) {
		errs = append(errs, "GST % must be 0, 5, 12, 18, 28 or 40.")
	}
	if in.HSN != "" && !indiantax.HSN.MatchString(in.HSN) {
		errs = append(errs, "HSN or SAC code must be 4 to 8 digits. Goods use HSN; services use a 6-digit SAC starting with 99.")
	}
	in.brandID = 0
	if textclean.Key(in.BrandName) == "" {
		in.BrandName = "" // no brand, for example a service
	} else {
		var bActive bool
		var bName string
		in.brandID = 0
		if err := q.QueryRow(ctx, `SELECT id, name, active FROM brand WHERE norm_key(name)=norm_key($1)`, in.BrandName).Scan(&in.brandID, &bName, &bActive); err == nil {
			if !bActive {
				errs = append(errs, "Brand "+bName+" is inactive. Mark it active under Settings, Item Setup.")
			}
			in.BrandName = bName
		} else if len(in.BrandName) < 2 || strings.ContainsAny(in.BrandName, `<>"`) {
			errs = append(errs, "Brand name is not valid.")
		}
	}
	if in.Scope != "all" && in.Scope != "selected" {
		errs = append(errs, "Availability must be all properties or selected properties.")
	}
	if in.Scope == "selected" && len(in.ClusterIDs) == 0 && len(in.PropertyIDs) == 0 {
		errs = append(errs, "Tick at least one cluster or property.")
	}
	if in.Scope == "all" {
		in.ClusterIDs, in.PropertyIDs = []int64{}, []int64{}
	}
	if in.ClusterIDs == nil {
		in.ClusterIDs = []int64{}
	}
	if in.PropertyIDs == nil {
		in.PropertyIDs = []int64{}
	}
	if len(errs) == 0 && (in.brandID != 0 || in.BrandName == "") {
		var dup string
		_ = q.QueryRow(ctx, `SELECT code || CASE WHEN active THEN '' ELSE ' (inactive)' END FROM sku WHERE sub_category_id=$1 AND coalesce(brand_id,0)=$2 AND norm_key(item_name)=norm_key($3) AND pack_size_id=$4 AND id<>$5`,
			in.SubCategoryID, in.brandID, in.ItemName, in.PackSizeID, selfID).Scan(&dup)
		if dup != "" {
			errs = append(errs, "Already exists as "+dup+".")
		}
	}
	return errs
}

func ensureBrand(ctx context.Context, tx pgx.Tx, u *access.User, in *SKUInput) error {
	if in.brandID != 0 || in.BrandName == "" {
		return nil
	}
	err := tx.QueryRow(ctx, `INSERT INTO brand(name,created_by) VALUES ($1,$2) ON CONFLICT ((norm_key(name))) DO UPDATE SET name=brand.name RETURNING id`, in.BrandName, u.ID).Scan(&in.brandID)
	if err != nil {
		return err
	}
	return auditlog.Write(ctx, tx, u, "Items", "Brand added", in.BrandName, "created while adding a SKU")
}

// Create saves a validated SKU with the next code for its sub-category, for example GA-0001.
func Create(ctx context.Context, tx pgx.Tx, u *access.User, in *SKUInput) (string, error) {
	if err := ensureBrand(ctx, tx, u, in); err != nil {
		return "", err
	}
	var prefix string
	if err := tx.QueryRow(ctx, `SELECT code FROM sub_category WHERE id=$1 FOR UPDATE`, in.SubCategoryID).Scan(&prefix); err != nil {
		return "", err
	}
	var n int
	if err := tx.QueryRow(ctx, `SELECT coalesce(max(substring(code from 4)::int),0)+1 FROM sku WHERE code LIKE $1 || '-%' AND code ~ '^[A-Z]{2}-[0-9]+$'`, prefix).Scan(&n); err != nil {
		return "", err
	}
	code := fmt.Sprintf("%s-%04d", prefix, n)
	_, err := tx.Exec(ctx, `INSERT INTO sku(code,sub_category_id,brand_id,item_name,pack_size_id,gst_pct,hsn,scope,cluster_ids,property_ids,created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, code, in.SubCategoryID, in.brandRef(), in.ItemName, in.PackSizeID, in.GST, in.HSN, in.Scope, in.ClusterIDs, in.PropertyIDs, u.ID)
	if err != nil {
		return "", err
	}
	return code, auditlog.Write(ctx, tx, u, "Items", "SKU added", code, strings.TrimSpace(in.BrandName+" "+in.ItemName))
}

func (s *Service) saveSKU(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	if err := access.NeedRight(u, access.Masters, "edit masters"); err != nil {
		return err
	}
	var in SKUInput
	if err := httpjson.Decode(r, &in); err != nil {
		return err
	}
	id := int64(0)
	if chi.URLParam(r, "id") != "" {
		var err error
		if id, err = httpjson.ID(r, "id"); err != nil {
			return err
		}
		// The sub-category sets the SKU code, so it stays as it is.
		if err := s.DB.QueryRow(r.Context(), `SELECT sub_category_id FROM sku WHERE id=$1`, id).Scan(&in.SubCategoryID); err != nil {
			return err
		}
	}
	if errs := Validate(r.Context(), s.DB, &in, id); len(errs) > 0 {
		return httpjson.Invalid(errs)
	}
	var code string
	err := pgx.BeginFunc(r.Context(), s.DB, func(tx pgx.Tx) error {
		if id == 0 {
			var err error
			code, err = Create(r.Context(), tx, u, &in)
			return err
		}
		if err := ensureBrand(r.Context(), tx, u, &in); err != nil {
			return err
		}
		var old string
		if err := tx.QueryRow(r.Context(), `SELECT k.code||' · '||concat_ws(' ', b.name, k.item_name)||' · '||k.gst_pct||'%' FROM sku k LEFT JOIN brand b ON b.id=k.brand_id WHERE k.id=$1`, id).Scan(&old); err != nil {
			return err
		}
		if err := tx.QueryRow(r.Context(), `UPDATE sku SET brand_id=$2, item_name=$3, pack_size_id=$4, gst_pct=$5, hsn=$6, scope=$7, cluster_ids=$8, property_ids=$9 WHERE id=$1 RETURNING code`,
			id, in.brandRef(), in.ItemName, in.PackSizeID, in.GST, in.HSN, in.Scope, in.ClusterIDs, in.PropertyIDs).Scan(&code); err != nil {
			return err
		}
		return auditlog.Write(r.Context(), tx, u, "Items", "SKU edited", code, fmt.Sprintf("%s → %s · %v%%", old, strings.TrimSpace(in.BrandName+" "+in.ItemName), in.GST))
	})
	if err != nil {
		return err
	}
	return httpjson.OK(w, map[string]string{"code": code})
}

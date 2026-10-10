// Package geography manages cities, clusters and properties (with their eZee code and F&B cost %).
package geography

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
	"purchasedesk/internal/releasephase"
	"purchasedesk/internal/textclean"
)

type Service struct{ DB *pgxpool.Pool }

func (s *Service) Routes(r chi.Router) {
	r.Get("/cities", httpjson.Wrap(s.listCities))
	r.Post("/cities", httpjson.Wrap(s.saveCity))
	r.Put("/cities/{id}", httpjson.Wrap(s.saveCity))
	r.Get("/clusters", httpjson.Wrap(s.listClusters))
	r.Post("/clusters", httpjson.Wrap(s.saveCluster))
	r.Put("/clusters/{id}", httpjson.Wrap(s.saveCluster))
	r.Get("/properties", httpjson.Wrap(s.listProperties))
	r.Post("/properties", httpjson.Wrap(s.saveProperty))
	r.Put("/properties/{id}", httpjson.Wrap(s.saveProperty))
	if releasephase.On(releasephase.Budgets) {
		r.Put("/properties/{id}/pct", httpjson.Wrap(s.savePropertyPct))
	}
}

type City struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	StateCode string `json:"state_code"`
	Active    bool   `json:"active"`
	Clusters  int    `json:"clusters"`
	Props     int    `json:"properties"`
}

func (s *Service) listCities(w http.ResponseWriter, r *http.Request) error {
	rows, err := s.DB.Query(r.Context(), `SELECT c.id, c.name, c.state_code, c.active,
		(SELECT count(*) FROM cluster k WHERE k.city_id=c.id), (SELECT count(*) FROM property p JOIN cluster k ON k.id=p.cluster_id WHERE k.city_id=c.id)
		FROM city c ORDER BY c.name`)
	if err != nil {
		return err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (City, error) {
		var c City
		err := row.Scan(&c.ID, &c.Name, &c.StateCode, &c.Active, &c.Clusters, &c.Props)
		return c, err
	})
	if err != nil {
		return err
	}
	return httpjson.OK(w, out)
}

func (s *Service) saveCity(w http.ResponseWriter, r *http.Request) error {
	p := access.From(r)
	if err := access.NeedRight(p, access.Masters, "edit masters"); err != nil {
		return err
	}
	var in struct {
		Name      string `json:"name"`
		StateCode string `json:"state_code"`
	}
	if err := httpjson.Decode(r, &in); err != nil {
		return err
	}
	in.Name, in.StateCode = textclean.Clean(in.Name), strings.ToUpper(strings.TrimSpace(in.StateCode))
	if len(in.Name) < 2 {
		return httpjson.Bad("Enter the city name.")
	}
	if len(in.StateCode) != 2 {
		return httpjson.Bad("Enter the 2-letter state code, for example HR or DL.")
	}
	id := int64(0)
	if chi.URLParam(r, "id") != "" {
		var err error
		if id, err = httpjson.ID(r, "id"); err != nil {
			return err
		}
	}
	var dup bool
	_ = s.DB.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM city WHERE norm_key(name)=norm_key($1) AND id<>$2)`, in.Name, id).Scan(&dup)
	if dup {
		return httpjson.Bad("This city already exists.")
	}
	if id == 0 {
		err := s.DB.QueryRow(r.Context(), `INSERT INTO city(name,state_code,created_by) VALUES ($1,$2,$3) RETURNING id`, in.Name, in.StateCode, p.ID).Scan(&id)
		if err != nil {
			return err
		}
		_ = auditlog.Write(r.Context(), s.DB, p, "Properties", "City added", in.Name, "State "+in.StateCode)
	} else {
		if _, err := s.DB.Exec(r.Context(), `UPDATE city SET name=$2, state_code=$3 WHERE id=$1`, id, in.Name, in.StateCode); err != nil {
			return err
		}
		_ = auditlog.Write(r.Context(), s.DB, p, "Properties", "City changed", in.Name, "State "+in.StateCode)
	}
	return httpjson.OK(w, map[string]int64{"id": id})
}

type Cluster struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	CityID      int64  `json:"city_id"`
	City        string `json:"city"`
	ManagerID   *int64 `json:"manager_id"`
	ManagerName string `json:"manager_name"`
	Active      bool   `json:"active"`
	Props       int    `json:"properties"`
}

func (s *Service) listClusters(w http.ResponseWriter, r *http.Request) error {
	rows, err := s.DB.Query(r.Context(), `SELECT k.id, k.name, k.city_id, c.name, k.manager_id, coalesce(u.name,''), k.active,
		(SELECT count(*) FROM property p WHERE p.cluster_id=k.id)
		FROM cluster k JOIN city c ON c.id=k.city_id LEFT JOIN app_user u ON u.id=k.manager_id ORDER BY c.name, k.name`)
	if err != nil {
		return err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Cluster, error) {
		var c Cluster
		err := row.Scan(&c.ID, &c.Name, &c.CityID, &c.City, &c.ManagerID, &c.ManagerName, &c.Active, &c.Props)
		return c, err
	})
	if err != nil {
		return err
	}
	return httpjson.OK(w, out)
}

func (s *Service) saveCluster(w http.ResponseWriter, r *http.Request) error {
	p := access.From(r)
	if err := access.NeedRight(p, access.Masters, "edit masters"); err != nil {
		return err
	}
	var in struct {
		Name      string `json:"name"`
		CityID    int64  `json:"city_id"`
		ManagerID *int64 `json:"manager_id"`
	}
	if err := httpjson.Decode(r, &in); err != nil {
		return err
	}
	in.Name = textclean.Clean(in.Name)
	if len(in.Name) < 3 {
		return httpjson.Bad("Enter the cluster name.")
	}
	if in.ManagerID != nil && *in.ManagerID == 0 {
		in.ManagerID = nil
	}
	id := int64(0)
	if chi.URLParam(r, "id") != "" {
		var err error
		if id, err = httpjson.ID(r, "id"); err != nil {
			return err
		}
	}
	var dup bool
	_ = s.DB.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM cluster WHERE norm_key(name)=norm_key($1) AND id<>$2)`, in.Name, id).Scan(&dup)
	if dup {
		return httpjson.Bad("A cluster with this name already exists.")
	}
	if in.ManagerID != nil {
		var ok bool
		_ = s.DB.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM user_role ur JOIN role r ON r.id=ur.role_id WHERE ur.user_id=$1 AND r.step='l2')`, *in.ManagerID).Scan(&ok)
		if !ok {
			return httpjson.Bad("The Cluster Manager must be a user with the Cluster Manager role.")
		}
	}
	if id == 0 {
		if err := s.DB.QueryRow(r.Context(), `INSERT INTO cluster(name,city_id,manager_id,created_by) VALUES ($1,$2,$3,$4) RETURNING id`, in.Name, in.CityID, in.ManagerID, p.ID).Scan(&id); err != nil {
			return err
		}
		_ = auditlog.Write(r.Context(), s.DB, p, "Properties", "Cluster added", in.Name, "")
	} else {
		if _, err := s.DB.Exec(r.Context(), `UPDATE cluster SET name=$2, city_id=$3, manager_id=$4 WHERE id=$1`, id, in.Name, in.CityID, in.ManagerID); err != nil {
			return err
		}
		_ = auditlog.Write(r.Context(), s.DB, p, "Properties", "Cluster changed", in.Name, "")
	}
	return httpjson.OK(w, map[string]int64{"id": id})
}

type Property struct {
	ID          int64   `json:"id"`
	Code        string  `json:"code"`
	Name        string  `json:"name"`
	EzeeCode    string  `json:"ezee_code"`
	ClusterID   int64   `json:"cluster_id"`
	Cluster     string  `json:"cluster"`
	CityID      int64   `json:"city_id"`
	City        string  `json:"city"`
	StateCode   string  `json:"state_code"`
	L1UserID    *int64  `json:"l1_user_id"`
	L1Name      string  `json:"l1_name"`
	L2Name      string  `json:"l2_name"`
	Kind        string  `json:"kind"`
	PlanPct     float64 `json:"plan_pct"`
	IRDPct      float64 `json:"ird_pct"`
	BanquetPct  float64 `json:"banquet_pct"`
	Active      bool    `json:"active"`
	InactiveWhy string  `json:"inactive_reason"`
}

const propertySelect = `SELECT p.id, p.code, p.name, p.ezee_code, p.cluster_id, k.name, c.id, c.name, c.state_code, p.l1_user_id, coalesce(u.name,''), coalesce(m.name,''),
	p.kind, p.plan_pct, p.ird_pct, p.banquet_pct, p.active, coalesce(p.inactive_reason,'')
	FROM property p JOIN cluster k ON k.id=p.cluster_id JOIN city c ON c.id=k.city_id
	LEFT JOIN app_user u ON u.id=p.l1_user_id LEFT JOIN app_user m ON m.id=k.manager_id`

func scanProperty(row pgx.CollectableRow) (Property, error) {
	var x Property
	err := row.Scan(&x.ID, &x.Code, &x.Name, &x.EzeeCode, &x.ClusterID, &x.Cluster, &x.CityID, &x.City, &x.StateCode, &x.L1UserID, &x.L1Name, &x.L2Name,
		&x.Kind, &x.PlanPct, &x.IRDPct, &x.BanquetPct, &x.Active, &x.InactiveWhy)
	return x, err
}

func (s *Service) listProperties(w http.ResponseWriter, r *http.Request) error {
	rows, err := s.DB.Query(r.Context(), propertySelect+` ORDER BY p.code`)
	if err != nil {
		return err
	}
	out, err := pgx.CollectRows(rows, scanProperty)
	if err != nil {
		return err
	}
	return httpjson.OK(w, out)
}

// PropertyInput is a property from the form or an upload row.
type PropertyInput struct {
	Name       string  `json:"name"`
	EzeeCode   string  `json:"ezee_code"`
	ClusterID  int64   `json:"cluster_id"`
	L1UserID   *int64  `json:"l1_user_id"`
	Kind       string  `json:"kind"`
	PlanPct    float64 `json:"plan_pct"`
	IRDPct     float64 `json:"ird_pct"`
	BanquetPct float64 `json:"banquet_pct"`
	CopyFrom   int64   `json:"copy_from"`
}

func validPct(v float64) bool { return v >= 0 && v <= 100 }

// ValidateProperty tidies the input and returns every problem. id is the property being edited, or 0.
func ValidateProperty(ctx context.Context, q database.Q, in *PropertyInput, id int64) []string {
	in.Name, in.EzeeCode = textclean.Clean(in.Name), strings.ToUpper(strings.TrimSpace(in.EzeeCode))
	if in.Kind == "" {
		in.Kind = "live"
	}
	errs := []string{}
	if len(in.Name) < 3 {
		errs = append(errs, "Enter the property name.")
	}
	if in.EzeeCode == "" {
		errs = append(errs, "Enter the eZee code.")
	}
	if !textclean.Contains([]string{"live", "warehouse", "preopening", "corporate"}, in.Kind) {
		errs = append(errs, "Stage must be live, warehouse, pre-opening or corporate.")
	} else if !releasephase.PropertyKindOn(in.Kind) {
		errs = append(errs, "Warehouse and corporate properties open in a later phase of the rollout.")
	}
	if !validPct(in.PlanPct) || !validPct(in.IRDPct) || !validPct(in.BanquetPct) {
		errs = append(errs, "Each cost % must be a number from 0 to 100.")
	}
	if in.L1UserID != nil && *in.L1UserID == 0 {
		in.L1UserID = nil
	}
	var ok bool
	_ = q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM cluster WHERE id=$1 AND active)`, in.ClusterID).Scan(&ok)
	if !ok {
		errs = append(errs, "Choose an active cluster.")
	}
	var dup string
	_ = q.QueryRow(ctx, `SELECT CASE WHEN norm_key(name)=norm_key($1) THEN 'name' ELSE 'ezee' END FROM property WHERE (norm_key(name)=norm_key($1) OR upper(ezee_code)=$2) AND id<>$3 LIMIT 1`, in.Name, in.EzeeCode, id).Scan(&dup)
	if dup == "name" {
		errs = append(errs, "A property with this name already exists.")
	} else if dup == "ezee" {
		errs = append(errs, "This eZee code is already used.")
	}
	return errs
}

// CreateProperty saves a validated new property with the next code (P41, P42, ...).
// CopyFrom gives the new property the same vendor for every item as an existing property.
func CreateProperty(ctx context.Context, tx pgx.Tx, u *access.User, in PropertyInput) (int64, string, error) {
	if _, err := tx.Exec(ctx, `LOCK TABLE property IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return 0, "", err
	}
	var n int
	_ = tx.QueryRow(ctx, `SELECT coalesce(max(substring(code from 2)::int),0)+1 FROM property WHERE code ~ '^P[0-9]+$'`).Scan(&n)
	code := fmt.Sprintf("P%02d", n)
	var id int64
	if err := tx.QueryRow(ctx, `INSERT INTO property(code,name,ezee_code,cluster_id,l1_user_id,kind,plan_pct,ird_pct,banquet_pct,created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING id`, code, in.Name, in.EzeeCode, in.ClusterID, in.L1UserID, in.Kind, in.PlanPct, in.IRDPct, in.BanquetPct, u.ID).Scan(&id); err != nil {
		return 0, "", err
	}
	detail := code + " · " + in.EzeeCode
	if in.CopyFrom > 0 {
		ct, err := tx.Exec(ctx, `INSERT INTO vendor_mapping_property(mapping_id, property_id)
			SELECT mp.mapping_id, $2 FROM vendor_mapping_property mp JOIN vendor_mapping m ON m.id=mp.mapping_id
			WHERE mp.property_id=$1 AND m.active ON CONFLICT DO NOTHING`, in.CopyFrom, id)
		if err != nil {
			return 0, "", err
		}
		detail += fmt.Sprintf(" · vendor mapping copied (%d mappings, plus all-property mappings)", ct.RowsAffected())
	}
	return id, code, auditlog.Write(ctx, tx, u, "Properties", "Property added", in.Name, detail)
}

// UpdateProperty saves a validated edit of property id. Its code stays as it is.
func UpdateProperty(ctx context.Context, tx pgx.Tx, u *access.User, id int64, in PropertyInput) error {
	if _, err := tx.Exec(ctx, `UPDATE property SET name=$2, ezee_code=$3, cluster_id=$4, l1_user_id=$5, kind=$6, plan_pct=$7, ird_pct=$8, banquet_pct=$9 WHERE id=$1`,
		id, in.Name, in.EzeeCode, in.ClusterID, in.L1UserID, in.Kind, in.PlanPct, in.IRDPct, in.BanquetPct); err != nil {
		return err
	}
	return auditlog.Write(ctx, tx, u, "Properties", "Property changed", in.Name, in.Kind)
}

func (s *Service) saveProperty(w http.ResponseWriter, r *http.Request) error {
	p := access.From(r)
	if err := access.NeedRight(p, access.Masters, "edit masters"); err != nil {
		return err
	}
	var in PropertyInput
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
	if errs := ValidateProperty(r.Context(), s.DB, &in, id); len(errs) > 0 {
		return httpjson.Invalid(errs)
	}
	err := pgx.BeginFunc(r.Context(), s.DB, func(tx pgx.Tx) error {
		if id == 0 {
			var err error
			id, _, err = CreateProperty(r.Context(), tx, p, in)
			return err
		}
		return UpdateProperty(r.Context(), tx, p, id, in)
	})
	if err != nil {
		return err
	}
	return httpjson.OK(w, map[string]int64{"id": id})
}

func (s *Service) savePropertyPct(w http.ResponseWriter, r *http.Request) error {
	p := access.From(r)
	if !p.HasRight(access.Masters) && !p.HasStep("cc") {
		return httpjson.Forbidden("The Cost Controller or a user with the Edit masters right can change cost %%.")
	}
	id, err := httpjson.ID(r, "id")
	if err != nil {
		return err
	}
	var in struct{ PlanPct, IRDPct, BanquetPct float64 }
	if err := httpjson.Decode(r, &in); err != nil {
		return err
	}
	if !validPct(in.PlanPct) || !validPct(in.IRDPct) || !validPct(in.BanquetPct) {
		return httpjson.Bad("Each cost %% must be a number from 0 to 100.")
	}
	var name string
	var op, oi, ob float64
	if err := s.DB.QueryRow(r.Context(), `SELECT name, plan_pct, ird_pct, banquet_pct FROM property WHERE id=$1`, id).Scan(&name, &op, &oi, &ob); err != nil {
		return err
	}
	if _, err := s.DB.Exec(r.Context(), `UPDATE property SET plan_pct=$2, ird_pct=$3, banquet_pct=$4 WHERE id=$1`, id, in.PlanPct, in.IRDPct, in.BanquetPct); err != nil {
		return err
	}
	_ = auditlog.Write(r.Context(), s.DB, p, "Properties", "F&B cost % changed", name,
		fmt.Sprintf("Plan %v%% to %v%%, IRD %v%% to %v%%, Banquet %v%% to %v%%", op, in.PlanPct, oi, in.IRDPct, ob, in.BanquetPct))
	return httpjson.OK(w, map[string]bool{"ok": true})
}

// Package zohosync keeps Purchase Desk and Zoho Books in step: the connection settings (secrets stored encrypted),
// the hourly read of accounts, taxes, reporting tags, locations and vendor contacts, and the four mappings
// Finance maintains: property → reporting tag, sub-category / bill type → account, GST rate → tax, vendor → Zoho vendor.
// Purchase Desk only ever creates records in Zoho; it never edits or deletes them.
package zohosync

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"purchasedesk/internal/access"
	"purchasedesk/internal/auditlog"
	"purchasedesk/internal/encryption"
	"purchasedesk/internal/httpjson"
	"purchasedesk/internal/indiantax"
	"purchasedesk/internal/money"
	"purchasedesk/internal/textclean"
	"purchasedesk/internal/zohoapi"
)

type Service struct {
	DB          *pgxpool.Pool
	Box         *encryption.Box
	PushEnabled bool

	mu     sync.Mutex
	client *zohoapi.Client
	key    string
}

func (s *Service) Routes(r chi.Router) {
	r.Get("/zoho/connection", httpjson.Wrap(s.getConnection))
	r.Put("/zoho/connection", httpjson.Wrap(s.putConnection))
	r.Post("/zoho/test", httpjson.Wrap(s.test))
	r.Post("/zoho/connected", httpjson.Wrap(s.setConnected))
	r.Post("/zoho/sync", httpjson.Wrap(s.syncNow))
	r.Get("/zoho/lists", httpjson.Wrap(s.lists))
	r.Get("/zoho/mappings", httpjson.Wrap(s.mappings))
	r.Put("/zoho/mappings", httpjson.Wrap(s.setMapping))
	r.Post("/zoho/properties/match", httpjson.Wrap(s.matchProperties))
	r.Post("/zoho/properties/{id}/create-option", httpjson.Wrap(s.createTagOption))
	r.Post("/zoho/accounts", httpjson.Wrap(s.createAccount))
	r.Post("/zoho/vendors/{id}/link", httpjson.Wrap(s.linkVendor))
	r.Post("/zoho/vendors/{id}/unlink", httpjson.Wrap(s.unlinkVendor))
	r.Post("/zoho/vendors/{id}/create", httpjson.Wrap(s.createVendor))
}

// Conn is the stored connection.
type Conn struct {
	AccountsURL, APIURL, OrgID, OrgName, Plan, ClientID string
	SecretEnc, TokenEnc                                 string
	Connected                                           bool
	LastError, PropertyTagID, BillKeyField              string
	LastSync, LastPaymentRead, Tested                   *time.Time
}

func (s *Service) load(ctx context.Context) (*Conn, error) {
	c := &Conn{}
	err := s.DB.QueryRow(ctx, `SELECT accounts_url, api_url, org_id, org_name, plan_name, client_id, client_secret_enc, refresh_token_enc, connected, last_error,
		property_tag_id, bill_key_field, last_sync_at, last_payment_read_at, tested_at FROM zoho_connection WHERE id=1`).
		Scan(&c.AccountsURL, &c.APIURL, &c.OrgID, &c.OrgName, &c.Plan, &c.ClientID, &c.SecretEnc, &c.TokenEnc, &c.Connected, &c.LastError,
			&c.PropertyTagID, &c.BillKeyField, &c.LastSync, &c.LastPaymentRead, &c.Tested)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return c, err
}

// SeedFromEnv stores the connection from the environment on first start, encrypting the secrets.
func (s *Service) SeedFromEnv(ctx context.Context, cfg zohoapi.Config) error {
	c, err := s.load(ctx)
	if err != nil || c != nil {
		return err
	}
	sec, err := s.Box.Seal(cfg.ClientSecret)
	if err != nil {
		return err
	}
	tok, err := s.Box.Seal(cfg.RefreshToken)
	if err != nil {
		return err
	}
	ready := cfg.OrgID != "" && cfg.ClientID != "" && cfg.ClientSecret != "" && cfg.RefreshToken != ""
	_, err = s.DB.Exec(ctx, `INSERT INTO zoho_connection(id,accounts_url,api_url,org_id,client_id,client_secret_enc,refresh_token_enc,connected) VALUES (1,$1,$2,$3,$4,$5,$6,$7)`,
		cfg.AccountsURL, cfg.APIURL, cfg.OrgID, cfg.ClientID, sec, tok, ready)
	return err
}

func (s *Service) configOf(c *Conn) (zohoapi.Config, error) {
	sec, err := s.Box.Open(c.SecretEnc)
	if err != nil {
		return zohoapi.Config{}, err
	}
	tok, err := s.Box.Open(c.TokenEnc)
	if err != nil {
		return zohoapi.Config{}, err
	}
	return zohoapi.Config{AccountsURL: c.AccountsURL, APIURL: c.APIURL, OrgID: c.OrgID, ClientID: c.ClientID, ClientSecret: sec, RefreshToken: tok}, nil
}

// Client returns an API client for the stored connection. It fails when Zoho is disconnected.
func (s *Service) Client(ctx context.Context) (*zohoapi.Client, *Conn, error) {
	c, err := s.load(ctx)
	if err != nil {
		return nil, nil, err
	}
	if c == nil || !c.Connected {
		return nil, c, httpjson.Bad("Zoho Books is not connected. The System Admin connects it under Settings, Zoho Books.")
	}
	key := c.AccountsURL + c.APIURL + c.OrgID + c.ClientID + c.SecretEnc + c.TokenEnc
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.client == nil || s.key != key {
		cfg, err := s.configOf(c)
		if err != nil {
			return nil, c, err
		}
		s.client, s.key = zohoapi.New(cfg), key
	}
	return s.client, c, nil
}

// Connected reports whether Zoho is switched on and set up.
func (s *Service) Connected(ctx context.Context) bool {
	c, err := s.load(ctx)
	return err == nil && c != nil && c.Connected
}

// Fail records a connection-level error, such as a revoked refresh token.
func (s *Service) Fail(ctx context.Context, err error) {
	if err == zohoapi.ErrTokenRevoked {
		_, _ = s.DB.Exec(ctx, `UPDATE zoho_connection SET connected=FALSE, last_error=$1 WHERE id=1`, err.Error())
		_ = auditlog.Write(ctx, s.DB, nil, "Zoho", "Disconnected", "Zoho Books", err.Error())
		return
	}
	_, _ = s.DB.Exec(ctx, `UPDATE zoho_connection SET last_error=$1 WHERE id=1`, err.Error())
}

func canMap(u *access.User) bool { return u.IsAdmin || u.HasStep("finance") }

func needMap(u *access.User) error {
	if !canMap(u) {
		return httpjson.Forbidden("Only Finance or the System Admin can change Zoho mappings.")
	}
	return nil
}

func mask(s string) string {
	if len(s) <= 9 {
		return strings.Repeat("•", len(s))
	}
	return s[:5] + "••••••••" + s[len(s)-4:]
}

func (s *Service) getConnection(w http.ResponseWriter, r *http.Request) error {
	c, err := s.load(r.Context())
	if err != nil {
		return err
	}
	if c == nil {
		return httpjson.OK(w, map[string]any{"configured": false, "push_enabled": s.PushEnabled})
	}
	var counts struct{ Accounts, Taxes, Options, Locations, Contacts, Bills int }
	_ = s.DB.QueryRow(r.Context(), `SELECT (SELECT count(*) FROM zoho_account WHERE active), (SELECT count(*) FROM zoho_tax WHERE active),
		(SELECT count(*) FROM zoho_tag_option o JOIN zoho_tag t ON t.tag_id=o.tag_id WHERE o.active AND t.active),
		(SELECT count(*) FROM zoho_location WHERE active), (SELECT count(*) FROM zoho_contact WHERE active), (SELECT count(*) FROM invoice WHERE zoho_bill_id IS NOT NULL)`).
		Scan(&counts.Accounts, &counts.Taxes, &counts.Options, &counts.Locations, &counts.Contacts, &counts.Bills)
	return httpjson.OK(w, map[string]any{
		"configured": true, "connected": c.Connected, "accounts_url": c.AccountsURL, "api_url": c.APIURL, "org_id": c.OrgID, "org_name": c.OrgName, "plan": c.Plan,
		"client_id_masked": mask(c.ClientID), "has_secret": c.SecretEnc != "", "has_refresh_token": c.TokenEnc != "", "last_error": c.LastError,
		"property_tag_id": c.PropertyTagID, "bill_key_field": c.BillKeyField, "last_sync_at": c.LastSync, "last_payment_read_at": c.LastPaymentRead,
		"tested_at": c.Tested, "push_enabled": s.PushEnabled, "counts": counts, "can_edit": access.From(r).IsAdmin,
	})
}

type connIn struct {
	AccountsURL   string `json:"accounts_url"`
	APIURL        string `json:"api_url"`
	OrgID         string `json:"org_id"`
	ClientID      string `json:"client_id"`
	ClientSecret  string `json:"client_secret"`
	RefreshToken  string `json:"refresh_token"`
	PropertyTagID string `json:"property_tag_id"`
	BillKeyField  string `json:"bill_key_field"`
}

// putConnection saves new connection values after a successful test call; failing values are refused.
func (s *Service) putConnection(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	if err := access.NeedAdmin(u); err != nil {
		return err
	}
	var in connIn
	if err := httpjson.Decode(r, &in); err != nil {
		return err
	}
	cur, err := s.load(r.Context())
	if err != nil {
		return err
	}
	if cur == nil {
		cur = &Conn{AccountsURL: "https://accounts.zoho.in", APIURL: "https://www.zohoapis.in", BillKeyField: "cf_purchase_desk_id"}
	}
	cfg, err := s.configOf(cur)
	if err != nil {
		return err
	}
	changed := []string{}
	set := func(dst *string, v, label string) {
		v = strings.TrimSpace(v)
		if v != "" && v != *dst {
			*dst = v
			changed = append(changed, label)
		}
	}
	set(&cfg.AccountsURL, strings.TrimRight(in.AccountsURL, "/"), "sign-in address")
	set(&cfg.APIURL, strings.TrimRight(in.APIURL, "/"), "API address")
	set(&cfg.OrgID, in.OrgID, "organisation ID")
	set(&cfg.ClientID, in.ClientID, "client ID")
	set(&cfg.ClientSecret, in.ClientSecret, "client secret")
	set(&cfg.RefreshToken, in.RefreshToken, "refresh token")
	tag, key := cur.PropertyTagID, cur.BillKeyField
	set(&tag, in.PropertyTagID, "property reporting tag")
	set(&key, in.BillKeyField, "bill key field")
	errs := []string{}
	if !strings.HasPrefix(cfg.ClientID, "1000.") {
		errs = append(errs, "The client ID starts with 1000.")
	}
	if cfg.RefreshToken != "" && !strings.HasPrefix(cfg.RefreshToken, "1000.") {
		errs = append(errs, "A Zoho refresh token starts with 1000.")
	}
	if cfg.OrgID == "" || cfg.ClientSecret == "" || cfg.RefreshToken == "" {
		errs = append(errs, "Enter the organisation ID, client secret and refresh token.")
	}
	if len(errs) > 0 {
		return httpjson.Invalid(errs)
	}
	test := zohoapi.New(cfg)
	orgs, err := test.Organizations(r.Context())
	if err != nil {
		return httpjson.Bad("The test call to Zoho failed, so nothing was saved: %v", err)
	}
	var org *zohoapi.Organization
	for i := range orgs {
		if orgs[i].ID == cfg.OrgID {
			org = &orgs[i]
		}
	}
	if org == nil {
		return httpjson.Bad("These credentials work, but organisation %s is not one of theirs. Check the organisation ID.", cfg.OrgID)
	}
	sec, err := s.Box.Seal(cfg.ClientSecret)
	if err != nil {
		return err
	}
	tok, err := s.Box.Seal(cfg.RefreshToken)
	if err != nil {
		return err
	}
	_, err = s.DB.Exec(r.Context(), `INSERT INTO zoho_connection(id,accounts_url,api_url,org_id,org_name,plan_name,client_id,client_secret_enc,refresh_token_enc,connected,last_error,property_tag_id,bill_key_field,tested_at,updated_by,updated_at)
		VALUES (1,$1,$2,$3,$4,$5,$6,$7,$8,TRUE,'',$9,$10,now(),$11,now())
		ON CONFLICT (id) DO UPDATE SET accounts_url=EXCLUDED.accounts_url, api_url=EXCLUDED.api_url, org_id=EXCLUDED.org_id, org_name=EXCLUDED.org_name, plan_name=EXCLUDED.plan_name,
		client_id=EXCLUDED.client_id, client_secret_enc=EXCLUDED.client_secret_enc, refresh_token_enc=EXCLUDED.refresh_token_enc, connected=TRUE, last_error='',
		property_tag_id=EXCLUDED.property_tag_id, bill_key_field=EXCLUDED.bill_key_field, tested_at=now(), updated_by=EXCLUDED.updated_by, updated_at=now()`,
		cfg.AccountsURL, cfg.APIURL, cfg.OrgID, org.Name, org.PlanName, cfg.ClientID, sec, tok, tag, key, u.ID)
	if err != nil {
		return err
	}
	// Logged without the secret values.
	_ = auditlog.Write(r.Context(), s.DB, u, "Zoho", "Connection settings changed", org.Name, textclean.Default(strings.Join(changed, ", "), "no change")+" · test passed")
	return httpjson.OK(w, map[string]string{"message": "Saved. Test passed: connected to " + org.Name})
}

func (s *Service) test(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	if err := access.NeedAdmin(u); err != nil {
		return err
	}
	c, err := s.load(r.Context())
	if err != nil || c == nil {
		return httpjson.Bad("Zoho is not set up yet.")
	}
	cfg, err := s.configOf(c)
	if err != nil {
		return err
	}
	orgs, err := zohoapi.New(cfg).Organizations(r.Context())
	if err != nil {
		s.Fail(r.Context(), err)
		return httpjson.Bad("Test failed: %v", err)
	}
	name, plan := "", ""
	for _, o := range orgs {
		if o.ID == c.OrgID {
			name, plan = o.Name, o.PlanName
		}
	}
	if name == "" {
		return httpjson.Bad("Signed in, but organisation %s was not found for these credentials.", c.OrgID)
	}
	keyOK := "not checked"
	if fields, err := zohoapi.New(cfg).BillCustomFields(r.Context()); err == nil {
		keyOK = "missing"
		for _, f := range fields {
			if fmt.Sprint(f["api_name"]) == c.BillKeyField {
				keyOK = "present"
			}
		}
	}
	_, _ = s.DB.Exec(r.Context(), `UPDATE zoho_connection SET org_name=$1, plan_name=$2, tested_at=now(), last_error='' WHERE id=1`, name, plan)
	_ = auditlog.Write(r.Context(), s.DB, u, "Zoho", "Connection tested", name, "plan "+plan+" · bill key field "+keyOK)
	msg := fmt.Sprintf("Test passed: %s, plan %s.", name, plan)
	if keyOK == "missing" {
		msg += fmt.Sprintf(" The bill custom field %s is missing in Zoho, so pushes check for an existing bill by vendor and bill number instead.", c.BillKeyField)
	}
	return httpjson.OK(w, map[string]string{"message": msg, "bill_key_field": keyOK})
}

func (s *Service) setConnected(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	if err := access.NeedAdmin(u); err != nil {
		return err
	}
	var in struct {
		Connected bool `json:"connected"`
	}
	if err := httpjson.Decode(r, &in); err != nil {
		return err
	}
	if _, err := s.DB.Exec(r.Context(), `UPDATE zoho_connection SET connected=$1 WHERE id=1`, in.Connected); err != nil {
		return err
	}
	act := "Disconnected"
	if in.Connected {
		act = "Connected"
	}
	_ = auditlog.Write(r.Context(), s.DB, u, "Zoho", act, "Zoho Books", "")
	return httpjson.OK(w, map[string]bool{"connected": in.Connected})
}

// ---------- sync ----------

// SyncResult reports what changed.
type SyncResult struct {
	Accounts, Taxes, TagOptions, Locations, Contacts int
	New                                              []string
	VendorsLinked, PropertiesLinked                  int
}

// Sync reads every list from Zoho, marks records gone from Zoho as inactive, then links vendors by GSTIN and properties by tag name.
func (s *Service) Sync(ctx context.Context) (*SyncResult, error) {
	c, conn, err := s.Client(ctx)
	if err != nil {
		return nil, err
	}
	res := &SyncResult{}
	fail := func(err error) (*SyncResult, error) {
		s.Fail(ctx, err)
		return nil, err
	}
	accts, err := c.Accounts(ctx)
	if err != nil {
		return fail(err)
	}
	taxes, err := c.Taxes(ctx)
	if err != nil {
		return fail(err)
	}
	tags, err := c.ReportingTags(ctx)
	if err != nil {
		return fail(err)
	}
	locs, err := c.Locations(ctx)
	if err != nil {
		locs = nil // locations are optional; an organisation without them is fine
	}
	contacts, err := c.Vendors(ctx)
	if err != nil {
		return fail(err)
	}
	err = pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		stamp := time.Now()
		newCount := func(table, key string, id string) bool {
			var exists bool
			_ = tx.QueryRow(ctx, fmt.Sprintf(`SELECT EXISTS(SELECT 1 FROM %s WHERE %s=$1)`, table, key), id).Scan(&exists)
			return !exists
		}
		for _, a := range accts {
			if newCount("zoho_account", "account_id", a.ID) && conn.LastSync != nil {
				res.New = append(res.New, "account "+a.Name)
			}
			if _, err := tx.Exec(ctx, `INSERT INTO zoho_account(account_id,name,code,account_type,active,synced_at) VALUES ($1,$2,$3,$4,$5,$6)
				ON CONFLICT (account_id) DO UPDATE SET name=EXCLUDED.name, code=EXCLUDED.code, account_type=EXCLUDED.account_type, active=EXCLUDED.active, synced_at=EXCLUDED.synced_at`,
				a.ID, a.Name, a.Code, a.Type, a.IsActive, stamp); err != nil {
				return err
			}
		}
		res.Accounts = len(accts)
		for _, t := range taxes {
			if _, err := tx.Exec(ctx, `INSERT INTO zoho_tax(tax_id,name,pct,tax_type,specification,active,synced_at) VALUES ($1,$2,$3,$4,$5,$6,$7)
				ON CONFLICT (tax_id) DO UPDATE SET name=EXCLUDED.name, pct=EXCLUDED.pct, tax_type=EXCLUDED.tax_type, specification=EXCLUDED.specification, active=EXCLUDED.active, synced_at=EXCLUDED.synced_at`,
				t.ID, t.Name, t.Percentage, t.Type, t.Specification, !t.IsInactive, stamp); err != nil {
				return err
			}
		}
		res.Taxes = len(taxes)
		for _, t := range tags {
			if _, err := tx.Exec(ctx, `INSERT INTO zoho_tag(tag_id,name,active,synced_at) VALUES ($1,$2,$3,$4)
				ON CONFLICT (tag_id) DO UPDATE SET name=EXCLUDED.name, active=EXCLUDED.active, synced_at=EXCLUDED.synced_at`, t.ID, t.Name, t.Status != "inactive" && (t.IsActive == nil || *t.IsActive), stamp); err != nil {
				return err
			}
			for _, o := range t.Options {
				active := o.Status != "inactive" && (o.IsActive == nil || *o.IsActive)
				if newCount("zoho_tag_option", "option_id", o.ID) && conn.LastSync != nil {
					res.New = append(res.New, "tag option "+o.Name)
				}
				if _, err := tx.Exec(ctx, `INSERT INTO zoho_tag_option(option_id,tag_id,name,active,synced_at) VALUES ($1,$2,$3,$4,$5)
					ON CONFLICT (option_id) DO UPDATE SET tag_id=EXCLUDED.tag_id, name=EXCLUDED.name, active=EXCLUDED.active, synced_at=EXCLUDED.synced_at`, o.ID, t.ID, o.Name, active, stamp); err != nil {
					return err
				}
				res.TagOptions++
			}
		}
		for _, l := range locs {
			if _, err := tx.Exec(ctx, `INSERT INTO zoho_location(location_id,name,gstin,state_code,active,synced_at) VALUES ($1,$2,$3,$4,$5,$6)
				ON CONFLICT (location_id) DO UPDATE SET name=EXCLUDED.name, gstin=EXCLUDED.gstin, state_code=EXCLUDED.state_code, active=EXCLUDED.active, synced_at=EXCLUDED.synced_at`,
				l.ID, l.Name, l.TaxReg, l.Address.StateCode, l.Status != "inactive", stamp); err != nil {
				return err
			}
		}
		res.Locations = len(locs)
		for _, x := range contacts {
			if newCount("zoho_contact", "contact_id", x.ID) && conn.LastSync != nil {
				res.New = append(res.New, "vendor "+x.Name)
			}
			if _, err := tx.Exec(ctx, `INSERT INTO zoho_contact(contact_id,name,company,gst_no,gst_treatment,state_code,active,synced_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
				ON CONFLICT (contact_id) DO UPDATE SET name=EXCLUDED.name, company=EXCLUDED.company, gst_no=EXCLUDED.gst_no, gst_treatment=EXCLUDED.gst_treatment,
				state_code=EXCLUDED.state_code, active=EXCLUDED.active, synced_at=EXCLUDED.synced_at`,
				x.ID, x.Name, x.Company, strings.ToUpper(x.GSTNo), x.GSTTreatment, x.Place, x.Status == "active", stamp); err != nil {
				return err
			}
		}
		res.Contacts = len(contacts)
		// A record no longer returned by Zoho was deleted there: mark it inactive so bills that need it are held.
		for _, t := range []string{"zoho_account", "zoho_tax", "zoho_tag", "zoho_tag_option", "zoho_contact"} {
			if _, err := tx.Exec(ctx, fmt.Sprintf(`UPDATE %s SET active=FALSE WHERE synced_at < $1`, t), stamp); err != nil {
				return err
			}
		}
		if locs != nil {
			if _, err := tx.Exec(ctx, `UPDATE zoho_location SET active=FALSE WHERE synced_at < $1`, stamp); err != nil {
				return err
			}
		}
		if conn.PropertyTagID == "" {
			// Pick the reporting tag named "Property" if the Admin has not chosen one.
			if _, err := tx.Exec(ctx, `UPDATE zoho_connection SET property_tag_id=coalesce((SELECT tag_id FROM zoho_tag WHERE active AND norm_key(name) IN ('property','properties','hotel') LIMIT 1),'') WHERE id=1`); err != nil {
				return err
			}
		}
		if res.VendorsLinked, err = autoLinkVendors(ctx, tx); err != nil {
			return err
		}
		if res.PropertiesLinked, err = autoLinkProperties(ctx, tx); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE zoho_connection SET last_sync_at=now(), last_error='' WHERE id=1`)
		return err
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

// autoLinkVendors links a vendor when the same GSTIN exists in Zoho, or, for an unregistered vendor, the exact same name.
// A similar name is never linked without a person confirming.
func autoLinkVendors(ctx context.Context, tx pgx.Tx) (int, error) {
	ct, err := tx.Exec(ctx, `INSERT INTO zoho_map_vendor(vendor_id,contact_id,how)
		SELECT DISTINCT ON (v.id) v.id, c.contact_id, 'gstin' FROM vendor v JOIN zoho_contact c ON c.active AND c.gst_no<>'' AND upper(c.gst_no)=upper(v.gstin)
		WHERE v.vendor_type<>'warehouse' AND v.gst_registered AND NOT EXISTS (SELECT 1 FROM zoho_map_vendor m WHERE m.vendor_id=v.id)
		AND NOT EXISTS (SELECT 1 FROM zoho_map_vendor m WHERE m.contact_id=c.contact_id) ORDER BY v.id, c.contact_id
		ON CONFLICT DO NOTHING`)
	if err != nil {
		return 0, err
	}
	n := int(ct.RowsAffected())
	ct, err = tx.Exec(ctx, `INSERT INTO zoho_map_vendor(vendor_id,contact_id,how)
		SELECT DISTINCT ON (v.id) v.id, c.contact_id, 'name' FROM vendor v JOIN zoho_contact c ON c.active AND c.gst_no='' AND norm_key(c.name)=norm_key(v.legal_name)
		WHERE v.vendor_type<>'warehouse' AND NOT v.gst_registered AND NOT EXISTS (SELECT 1 FROM zoho_map_vendor m WHERE m.vendor_id=v.id)
		AND NOT EXISTS (SELECT 1 FROM zoho_map_vendor m WHERE m.contact_id=c.contact_id) ORDER BY v.id, c.contact_id
		ON CONFLICT DO NOTHING`)
	if err != nil {
		return n, err
	}
	return n + int(ct.RowsAffected()), nil
}

// autoLinkProperties links a property to the reporting tag option with exactly the same name, in any tag.
// Properties may sit under different tags (for example one tag per region); the bill carries the option's own tag.
func autoLinkProperties(ctx context.Context, q pgx.Tx) (int, error) {
	ct, err := q.Exec(ctx, `INSERT INTO zoho_map_property(property_id, tag_option_id)
		SELECT DISTINCT ON (p.id) p.id, o.option_id FROM property p
		JOIN zoho_tag_option o ON o.active AND norm_key(o.name)=norm_key(p.name) JOIN zoho_tag t ON t.tag_id=o.tag_id AND t.active
		WHERE p.active ORDER BY p.id, o.option_id ON CONFLICT (property_id) DO UPDATE SET tag_option_id=EXCLUDED.tag_option_id WHERE zoho_map_property.tag_option_id IS NULL`)
	if err != nil {
		return 0, err
	}
	return int(ct.RowsAffected()), nil
}

func (s *Service) syncNow(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	if err := needMap(u); err != nil {
		return err
	}
	res, err := s.Sync(r.Context())
	if err != nil {
		return httpjson.Bad("Sync failed: %v", err)
	}
	msg := fmt.Sprintf("Synced: %d accounts, %d taxes, %d tag options, %d locations, %d vendor contacts. Linked automatically: %d vendors, %d properties.",
		res.Accounts, res.Taxes, res.TagOptions, res.Locations, res.Contacts, res.VendorsLinked, res.PropertiesLinked)
	if len(res.New) > 0 {
		msg += " New in Zoho: " + strings.Join(res.New, "; ") + "."
	}
	_ = auditlog.Write(r.Context(), s.DB, u, "Zoho", "Sync from Zoho", "Zoho Books", msg)
	return httpjson.OK(w, map[string]any{"message": msg, "result": res})
}

type option struct {
	ID     string  `json:"id"`
	Name   string  `json:"name"`
	Code   string  `json:"code"`
	Type   string  `json:"type,omitempty"`
	Pct    float64 `json:"pct"`
	Spec   string  `json:"specification"`
	Extra  string  `json:"extra,omitempty"`
	Active bool    `json:"active"`
	TagID  string  `json:"tag_id,omitempty"`
}

func collect(rows pgx.Rows, err error) ([]option, error) {
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (option, error) {
		var o option
		return o, row.Scan(&o.ID, &o.Name, &o.Code, &o.Type, &o.Pct, &o.Spec, &o.Extra, &o.Active, &o.TagID)
	})
}

func (s *Service) lists(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	acc, err := collect(s.DB.Query(ctx, `SELECT account_id, name, code, account_type, 0::numeric, '', '', active, '' FROM zoho_account ORDER BY code, name`))
	if err != nil {
		return err
	}
	tax, err := collect(s.DB.Query(ctx, `SELECT tax_id, name, '', tax_type, pct, specification, '', active, '' FROM zoho_tax ORDER BY pct, name`))
	if err != nil {
		return err
	}
	tags, err := collect(s.DB.Query(ctx, `SELECT tag_id, name, '', '', 0::numeric, '', '', active, '' FROM zoho_tag ORDER BY name`))
	if err != nil {
		return err
	}
	opts, err := collect(s.DB.Query(ctx, `SELECT option_id, name, '', '', 0::numeric, '', '', active, tag_id FROM zoho_tag_option ORDER BY name`))
	if err != nil {
		return err
	}
	locs, err := collect(s.DB.Query(ctx, `SELECT location_id, name, '', '', 0::numeric, state_code, gstin, active, '' FROM zoho_location ORDER BY name`))
	if err != nil {
		return err
	}
	cons, err := collect(s.DB.Query(ctx, `SELECT contact_id, name, '', gst_treatment, 0::numeric, state_code, gst_no, active, '' FROM zoho_contact ORDER BY name`))
	if err != nil {
		return err
	}
	// Each location carries the reporting tag of its region: the tag with the location's name ("Delhi") or its
	// state's name ("Bangalore" is in KA, so "Karnataka"). The Properties tab then offers only that tag's options.
	for i := range locs {
		for _, t := range tags {
			if t.Active && (textclean.Key(t.Name) == textclean.Key(locs[i].Name) || textclean.Key(t.Name) == textclean.Key(indiantax.StateName(locs[i].Spec))) {
				locs[i].TagID = t.ID
				break
			}
		}
	}
	return httpjson.OK(w, map[string]any{"accounts": acc, "taxes": tax, "tags": tags, "tag_options": opts, "locations": locs, "contacts": cons})
}

type propMap struct {
	PropertyID int64  `json:"property_id"`
	Code       string `json:"code"`
	Name       string `json:"name"`
	ClusterID  int64  `json:"cluster_id"`
	Cluster    string `json:"cluster"`
	Kind       string `json:"kind"`
	State      string `json:"state_code"`
	OptionID   string `json:"tag_option_id"`
	OptionOK   bool   `json:"tag_option_active"`
	LocationID string `json:"location_id"`
	SharedWith int    `json:"shared_with"`
}

type keyMap struct {
	Ref       string `json:"ref"`
	Name      string `json:"name"`
	Group     string `json:"group"`
	AccountID string `json:"account_id"`
	AccountOK bool   `json:"account_active"`
}

type vendorMap struct {
	VendorID   int64  `json:"vendor_id"`
	Code       string `json:"code"`
	Name       string `json:"name"`
	Type       string `json:"vendor_type"`
	GSTIN      string `json:"gstin"`
	State      string `json:"state_code"`
	Verified   bool   `json:"bank_verified"`
	ContactID  string `json:"contact_id"`
	Contact    string `json:"contact_name"`
	ContactGST string `json:"contact_gst"`
	ContactOK  bool   `json:"contact_active"`
	How        string `json:"how"`
	SuggestID  string `json:"suggest_id"`
	Suggest    string `json:"suggest_name"`
}

func (s *Service) mappings(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	rows, err := s.DB.Query(ctx, `SELECT p.id, p.code, p.name, p.cluster_id, k.name, p.kind, c.state_code, coalesce(m.tag_option_id,''), coalesce(o.active,false), coalesce(m.location_id,''),
		(SELECT count(*) FROM zoho_map_property x WHERE x.tag_option_id=m.tag_option_id AND x.property_id<>p.id)
		FROM property p JOIN cluster k ON k.id=p.cluster_id JOIN city c ON c.id=k.city_id
		LEFT JOIN zoho_map_property m ON m.property_id=p.id LEFT JOIN zoho_tag_option o ON o.option_id=m.tag_option_id
		WHERE p.active ORDER BY p.code`)
	if err != nil {
		return err
	}
	props, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (propMap, error) {
		var x propMap
		return x, row.Scan(&x.PropertyID, &x.Code, &x.Name, &x.ClusterID, &x.Cluster, &x.Kind, &x.State, &x.OptionID, &x.OptionOK, &x.LocationID, &x.SharedWith)
	})
	if err != nil {
		return err
	}
	scan := func(rows pgx.Rows, err error) ([]keyMap, error) {
		if err != nil {
			return nil, err
		}
		return pgx.CollectRows(rows, func(row pgx.CollectableRow) (keyMap, error) {
			var x keyMap
			return x, row.Scan(&x.Ref, &x.Name, &x.Group, &x.AccountID, &x.AccountOK)
		})
	}
	subs, err := scan(s.DB.Query(ctx, `SELECT sc.id::text, sc.name, c.name, coalesce(m.account_id,''), coalesce(a.active,false) FROM sub_category sc JOIN category c ON c.id=sc.category_id
		LEFT JOIN zoho_map_account m ON m.kind='subcat' AND m.ref=sc.id::text LEFT JOIN zoho_account a ON a.account_id=m.account_id WHERE sc.active ORDER BY c.name, sc.name`))
	if err != nil {
		return err
	}
	bts, err := scan(s.DB.Query(ctx, `SELECT b.id::text, b.name, b.section, coalesce(m.account_id,''), coalesce(a.active,false) FROM bill_type b
		LEFT JOIN zoho_map_account m ON m.kind='billtype' AND m.ref=b.id::text LEFT JOIN zoho_account a ON a.account_id=m.account_id WHERE b.active ORDER BY b.section, b.name`))
	if err != nil {
		return err
	}
	heads, err := scan(s.DB.Query(ctx, `SELECT h.id::text, h.name, 'Corporate', coalesce(m.account_id,''), coalesce(a.active,false) FROM expense_head h
		LEFT JOIN zoho_map_account m ON m.kind='head' AND m.ref=h.id::text LEFT JOIN zoho_account a ON a.account_id=m.account_id WHERE h.active ORDER BY h.name`))
	if err != nil {
		return err
	}
	spec, err := scan(s.DB.Query(ctx, `SELECT x.ref, x.name, '', coalesce(m.account_id,''), coalesce(a.active,false) FROM (VALUES ('warehouse','Purchases for warehouse stock'),('preopening','Purchases for pre-opening properties'),('loss','Warehouse stock lost in transit (pilferage)')) x(ref,name)
		LEFT JOIN zoho_map_account m ON m.kind='special' AND m.ref=x.ref LEFT JOIN zoho_account a ON a.account_id=m.account_id`))
	if err != nil {
		return err
	}
	trows, err := s.DB.Query(ctx, `SELECT gst_pct::text||'|'||supply, tax_id FROM zoho_map_tax`)
	if err != nil {
		return err
	}
	taxes := map[string]string{}
	for trows.Next() {
		var k, v string
		if err := trows.Scan(&k, &v); err != nil {
			trows.Close()
			return err
		}
		taxes[k] = v
	}
	trows.Close()
	vrows, err := s.DB.Query(ctx, `SELECT v.id, v.code, v.legal_name, v.vendor_type, coalesce(v.gstin,''), v.state_code, v.bank_verified,
		coalesce(m.contact_id,''), coalesce(c.name,''), coalesce(c.gst_no,''), coalesce(c.active,false), coalesce(m.how,''),
		coalesce(sg.contact_id,''), coalesce(sg.name,'')
		FROM vendor v LEFT JOIN zoho_map_vendor m ON m.vendor_id=v.id LEFT JOIN zoho_contact c ON c.contact_id=m.contact_id
		LEFT JOIN LATERAL (SELECT z.contact_id, z.name FROM zoho_contact z WHERE m.vendor_id IS NULL AND z.active
			AND NOT EXISTS (SELECT 1 FROM zoho_map_vendor y WHERE y.contact_id=z.contact_id)
			AND (left(norm_key(z.name),10)=left(norm_key(v.legal_name),10) OR norm_key(z.name) LIKE left(norm_key(v.legal_name),8)||'%') LIMIT 1) sg ON TRUE
		WHERE v.active AND v.vendor_type<>'warehouse' ORDER BY v.code`)
	if err != nil {
		return err
	}
	vendors, err := pgx.CollectRows(vrows, func(row pgx.CollectableRow) (vendorMap, error) {
		var x vendorMap
		return x, row.Scan(&x.VendorID, &x.Code, &x.Name, &x.Type, &x.GSTIN, &x.State, &x.Verified, &x.ContactID, &x.Contact, &x.ContactGST, &x.ContactOK, &x.How, &x.SuggestID, &x.Suggest)
	})
	if err != nil {
		return err
	}
	return httpjson.OK(w, map[string]any{"properties": props, "sub_categories": subs, "bill_types": bts, "expense_heads": heads, "special": spec, "taxes": taxes, "vendors": vendors, "can_edit": canMap(access.From(r))})
}

type mapIn struct {
	Kind  string `json:"kind"` // property_tag, property_location, subcat, billtype, special, head, tax
	Key   string `json:"key"`
	Value string `json:"value"` // empty clears
}

func (s *Service) setMapping(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	if err := needMap(u); err != nil {
		return err
	}
	var in mapIn
	if err := httpjson.Decode(r, &in); err != nil {
		return err
	}
	ctx := r.Context()
	var err error
	label := in.Kind + " " + in.Key
	switch in.Kind {
	case "property_tag", "property_location":
		col := map[string]string{"property_tag": "tag_option_id", "property_location": "location_id"}[in.Kind]
		if in.Value != "" {
			table, key := "zoho_tag_option", "option_id"
			if in.Kind == "property_location" {
				table, key = "zoho_location", "location_id"
			}
			var ok bool
			_ = s.DB.QueryRow(ctx, fmt.Sprintf(`SELECT EXISTS(SELECT 1 FROM %s WHERE %s=$1 AND active)`, table, key), in.Value).Scan(&ok)
			if !ok {
				return httpjson.Bad("This option is not in the latest list from Zoho. Sync first.")
			}
		}
		_, err = s.DB.Exec(ctx, fmt.Sprintf(`INSERT INTO zoho_map_property(property_id,%s,updated_by) VALUES ($1::bigint,nullif($2,''),$3)
			ON CONFLICT (property_id) DO UPDATE SET %s=EXCLUDED.%s, updated_by=EXCLUDED.updated_by, updated_at=now()`, col, col, col), in.Key, in.Value, u.ID)
	case "subcat", "billtype", "special", "head":
		if in.Value == "" {
			_, err = s.DB.Exec(ctx, `DELETE FROM zoho_map_account WHERE kind=$1 AND ref=$2`, in.Kind, in.Key)
		} else {
			var t string
			var ok bool
			_ = s.DB.QueryRow(ctx, `SELECT account_type, active FROM zoho_account WHERE account_id=$1`, in.Value).Scan(&t, &ok)
			if !ok {
				return httpjson.Bad("This account is not active in Zoho.")
			}
			expense := t == "expense" || t == "cost_of_goods_sold" || t == "other_expense"
			if (in.Kind == "subcat" || in.Kind == "billtype" || (in.Kind == "special" && in.Key == "loss")) && !expense {
				return httpjson.Bad("Only expense-type accounts can be used here.")
			}
			_, err = s.DB.Exec(ctx, `INSERT INTO zoho_map_account(kind,ref,account_id,updated_by) VALUES ($1,$2,$3,$4)
				ON CONFLICT (kind,ref) DO UPDATE SET account_id=EXCLUDED.account_id, updated_by=EXCLUDED.updated_by, updated_at=now()`, in.Kind, in.Key, in.Value, u.ID)
		}
	case "tax":
		parts := strings.Split(in.Key, "|")
		if len(parts) != 2 || (parts[1] != "intra" && parts[1] != "inter") {
			return httpjson.Bad("Tax mapping key is rate|intra or rate|inter.")
		}
		if in.Value == "" {
			_, err = s.DB.Exec(ctx, `DELETE FROM zoho_map_tax WHERE gst_pct=$1::numeric AND supply=$2`, parts[0], parts[1])
		} else {
			_, err = s.DB.Exec(ctx, `INSERT INTO zoho_map_tax(gst_pct,supply,tax_id,updated_by) VALUES ($1::numeric,$2,$3,$4)
				ON CONFLICT (gst_pct,supply) DO UPDATE SET tax_id=EXCLUDED.tax_id, updated_by=EXCLUDED.updated_by, updated_at=now()`, parts[0], parts[1], in.Value, u.ID)
		}
	default:
		return httpjson.Bad("Unknown mapping.")
	}
	if err != nil {
		return err
	}
	_ = auditlog.Write(ctx, s.DB, u, "Zoho", "Mapping changed", label, textclean.Default(in.Value, "cleared"))
	return httpjson.OK(w, map[string]bool{"ok": true})
}

func (s *Service) matchProperties(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	if err := needMap(u); err != nil {
		return err
	}
	var n int
	err := pgx.BeginFunc(r.Context(), s.DB, func(tx pgx.Tx) error {
		var err error
		n, err = autoLinkProperties(r.Context(), tx)
		return err
	})
	if err != nil {
		return err
	}
	_ = auditlog.Write(r.Context(), s.DB, u, "Zoho", "Reporting tags matched by name", "Properties", fmt.Sprint(n))
	return httpjson.OK(w, map[string]string{"message": fmt.Sprintf("%d properties matched to a tag option by name.", n)})
}

func (s *Service) createTagOption(w http.ResponseWriter, r *http.Request) error {
	if !s.PushEnabled { // ZOHO_PUSH_ENABLED=false keeps Purchase Desk read-only towards Zoho
		return httpjson.Bad("Writing to Zoho is switched off on this server (ZOHO_PUSH_ENABLED=false). Create it in Zoho, then click Sync now.")
	}
	u := access.From(r)
	if err := needMap(u); err != nil {
		return err
	}
	pid, err := httpjson.ID(r, "id")
	if err != nil {
		return err
	}
	c, conn, err := s.Client(r.Context())
	if err != nil {
		return err
	}
	if conn.PropertyTagID == "" {
		return httpjson.Bad("Choose the reporting tag that new options go under first, on the Connection tab.")
	}
	var name string
	if err := s.DB.QueryRow(r.Context(), `SELECT name FROM property WHERE id=$1`, pid).Scan(&name); err != nil {
		return err
	}
	var exists string
	_ = s.DB.QueryRow(r.Context(), `SELECT option_id FROM zoho_tag_option WHERE tag_id=$1 AND norm_key(name)=norm_key($2)`, conn.PropertyTagID, name).Scan(&exists)
	if exists != "" {
		return httpjson.Bad("An option named %s already exists in Zoho. Pick it from the list.", name)
	}
	tags, err := c.ReportingTags(r.Context())
	if err != nil {
		return httpjson.Bad("Zoho: %v", err)
	}
	for _, t := range tags {
		if t.ID == conn.PropertyTagID {
			if err := c.AddTagOption(r.Context(), t, name); err != nil {
				return httpjson.Bad("Zoho did not create the option: %v", err)
			}
			if _, err := s.Sync(r.Context()); err != nil {
				return httpjson.Bad("Created in Zoho, but reading it back failed: %v", err)
			}
			_ = auditlog.Write(r.Context(), s.DB, u, "Zoho", "Reporting tag option created in Zoho", name, t.Name)
			return httpjson.OK(w, map[string]string{"message": `"` + name + `" created in Zoho and linked.`})
		}
	}
	return httpjson.Bad("The reporting tag for new options was not found in Zoho. Sync and check the Connection tab.")
}

func (s *Service) createAccount(w http.ResponseWriter, r *http.Request) error {
	if !s.PushEnabled { // ZOHO_PUSH_ENABLED=false keeps Purchase Desk read-only towards Zoho
		return httpjson.Bad("Writing to Zoho is switched off on this server (ZOHO_PUSH_ENABLED=false). Create it in Zoho, then click Sync now.")
	}
	u := access.From(r)
	if err := needMap(u); err != nil {
		return err
	}
	var in struct{ Name, Code, Type string }
	if err := httpjson.Decode(r, &in); err != nil {
		return err
	}
	in.Name, in.Code = textclean.Clean(in.Name), strings.TrimSpace(in.Code)
	if len(in.Name) < 3 {
		return httpjson.Bad("Enter the account name.")
	}
	if !textclean.Contains([]string{"expense", "cost_of_goods_sold", "other_expense", "other_current_asset", "other_asset", "stock"}, in.Type) {
		return httpjson.Bad("Choose the account type.")
	}
	var dup bool
	_ = s.DB.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM zoho_account WHERE norm_key(name)=norm_key($1) OR ($2<>'' AND code=$2))`, in.Name, in.Code).Scan(&dup)
	if dup {
		return httpjson.Bad("An account with this name or code already exists in Zoho.")
	}
	c, _, err := s.Client(r.Context())
	if err != nil {
		return err
	}
	a, err := c.CreateAccount(r.Context(), in.Name, in.Code, in.Type)
	if err != nil {
		return httpjson.Bad("Zoho did not create the account: %v", err)
	}
	if _, err := s.DB.Exec(r.Context(), `INSERT INTO zoho_account(account_id,name,code,account_type,active) VALUES ($1,$2,$3,$4,TRUE) ON CONFLICT DO NOTHING`, a.ID, in.Name, in.Code, in.Type); err != nil {
		return err
	}
	_ = auditlog.Write(r.Context(), s.DB, u, "Zoho", "Account created in Zoho", in.Name, in.Type)
	return httpjson.OK(w, map[string]string{"message": in.Name + " created in Zoho.", "account_id": a.ID})
}

func (s *Service) linkVendor(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	if err := needMap(u); err != nil {
		return err
	}
	vid, err := httpjson.ID(r, "id")
	if err != nil {
		return err
	}
	var in struct {
		ContactID string `json:"contact_id"`
	}
	if err := httpjson.Decode(r, &in); err != nil {
		return err
	}
	var cname string
	var active bool
	if err := s.DB.QueryRow(r.Context(), `SELECT name, active FROM zoho_contact WHERE contact_id=$1`, in.ContactID).Scan(&cname, &active); err != nil || !active {
		return httpjson.Bad("Pick an active Zoho vendor.")
	}
	var other string
	_ = s.DB.QueryRow(r.Context(), `SELECT v.legal_name FROM zoho_map_vendor m JOIN vendor v ON v.id=m.vendor_id WHERE m.contact_id=$1 AND m.vendor_id<>$2`, in.ContactID, vid).Scan(&other)
	if other != "" {
		return httpjson.Bad("This Zoho vendor is already linked to %s. One Zoho vendor links to one Purchase Desk vendor.", other)
	}
	if _, err := s.DB.Exec(r.Context(), `INSERT INTO zoho_map_vendor(vendor_id,contact_id,how,updated_by) VALUES ($1,$2,'manual',$3)
		ON CONFLICT (vendor_id) DO UPDATE SET contact_id=EXCLUDED.contact_id, how='manual', updated_by=EXCLUDED.updated_by, updated_at=now()`, vid, in.ContactID, u.ID); err != nil {
		return err
	}
	var vcode string
	_ = s.DB.QueryRow(r.Context(), `SELECT code FROM vendor WHERE id=$1`, vid).Scan(&vcode)
	_ = auditlog.Write(r.Context(), s.DB, u, "Zoho", "Vendor linked", vcode, "Zoho contact "+cname+" ("+in.ContactID+")")
	return httpjson.OK(w, map[string]string{"message": "Linked to " + cname})
}

func (s *Service) unlinkVendor(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	if err := needMap(u); err != nil {
		return err
	}
	vid, err := httpjson.ID(r, "id")
	if err != nil {
		return err
	}
	if _, err := s.DB.Exec(r.Context(), `DELETE FROM zoho_map_vendor WHERE vendor_id=$1`, vid); err != nil {
		return err
	}
	var vcode string
	_ = s.DB.QueryRow(r.Context(), `SELECT code FROM vendor WHERE id=$1`, vid).Scan(&vcode)
	_ = auditlog.Write(r.Context(), s.DB, u, "Zoho", "Vendor unlinked", vcode, "")
	return httpjson.OK(w, map[string]string{"message": "Unlinked"})
}

// createVendor creates the vendor in Zoho from details verified in Purchase Desk. Bank details are not sent.
func (s *Service) createVendor(w http.ResponseWriter, r *http.Request) error {
	if !s.PushEnabled { // ZOHO_PUSH_ENABLED=false keeps Purchase Desk read-only towards Zoho
		return httpjson.Bad("Writing to Zoho is switched off on this server (ZOHO_PUSH_ENABLED=false). Create it in Zoho, then click Sync now.")
	}
	u := access.From(r)
	if err := needMap(u); err != nil {
		return err
	}
	vid, err := httpjson.ID(r, "id")
	if err != nil {
		return err
	}
	var v struct {
		Code, Name, GSTIN, State, Terms, Email, Mobile, Contact string
		GST, Verified                                           bool
	}
	if err := s.DB.QueryRow(r.Context(), `SELECT code, legal_name, coalesce(gstin,''), state_code, payment_terms, email, mobile, contact_person, gst_registered, bank_verified FROM vendor WHERE id=$1`, vid).
		Scan(&v.Code, &v.Name, &v.GSTIN, &v.State, &v.Terms, &v.Email, &v.Mobile, &v.Contact, &v.GST, &v.Verified); err != nil {
		return err
	}
	if !v.Verified {
		return httpjson.Bad("Finance must verify the bank details before the vendor is created in Zoho.")
	}
	var linked, dup bool
	_ = s.DB.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM zoho_map_vendor WHERE vendor_id=$1), EXISTS(SELECT 1 FROM zoho_contact WHERE active AND norm_key(name)=norm_key($2))`, vid, v.Name).Scan(&linked, &dup)
	if linked {
		return httpjson.Bad("This vendor is already linked to a Zoho vendor.")
	}
	if dup {
		return httpjson.Bad("A Zoho vendor with this name already exists. Link to it instead of creating a duplicate.")
	}
	c, _, err := s.Client(r.Context())
	if err != nil {
		return err
	}
	body := map[string]any{"contact_name": v.Name, "company_name": v.Name, "contact_type": "vendor", "payment_terms": money.TermsDays(v.Terms),
		"payment_terms_label": v.Terms, "gst_treatment": "business_none"}
	if v.GST {
		body["gst_treatment"], body["gst_no"] = "business_gst", v.GSTIN
	}
	if v.State != "" {
		body["place_of_contact"] = v.State
	}
	if v.Email != "" || v.Mobile != "" {
		body["contact_persons"] = []map[string]any{{"first_name": textclean.Default(v.Contact, v.Name), "email": v.Email, "mobile": v.Mobile, "is_primary_contact": true}}
	}
	ct, err := c.CreateVendor(r.Context(), body)
	if err != nil {
		return httpjson.Bad("Zoho did not create the vendor: %v", err)
	}
	err = pgx.BeginFunc(r.Context(), s.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(r.Context(), `INSERT INTO zoho_contact(contact_id,name,company,gst_no,gst_treatment,state_code,active) VALUES ($1,$2,$2,$3,$4,$5,TRUE) ON CONFLICT DO NOTHING`,
			ct.ID, v.Name, strings.ToUpper(v.GSTIN), body["gst_treatment"], v.State); err != nil {
			return err
		}
		if _, err := tx.Exec(r.Context(), `INSERT INTO zoho_map_vendor(vendor_id,contact_id,how,updated_by) VALUES ($1,$2,'created',$3)`, vid, ct.ID, u.ID); err != nil {
			return err
		}
		return auditlog.Write(r.Context(), tx, u, "Zoho", "Vendor created in Zoho", v.Code, v.Name+" · contact "+ct.ID+" · "+fmt.Sprint(body["gst_treatment"]))
	})
	if err != nil {
		return err
	}
	return httpjson.OK(w, map[string]string{"message": v.Name + " created in Zoho and linked."})
}

package zohosync

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"purchasedesk/internal/access"
	"purchasedesk/internal/auditlog"
	"purchasedesk/internal/httpjson"
	"purchasedesk/internal/indiantax"
	"purchasedesk/internal/money"
	"purchasedesk/internal/textclean"
	"purchasedesk/internal/zohoapi"
)

// Held is a reason a master cannot go to Zoho yet that costs no Zoho call to check again (a mapping, a verification,
// writing switched off). The automatic job checks a held master on every run; one Zoho refused waits half an hour.
type Held struct{ Err error }

func (h Held) Error() string { return h.Err.Error() }
func (h Held) Unwrap() error { return h.Err }

func held(format string, a ...any) error { return Held{httpjson.Bad(format, a...)} }

// by is the user for updated_by and the log; nil when the automatic job does it.
func by(u *access.User) any {
	if u == nil {
		return nil
	}
	return u.ID
}

func (s *Service) createVendor(w http.ResponseWriter, r *http.Request) error {
	return s.masterButton(w, r, s.VendorToZoho)
}
func (s *Service) createItem(w http.ResponseWriter, r *http.Request) error {
	return s.masterButton(w, r, s.ItemToZoho)
}
func (s *Service) createTagOption(w http.ResponseWriter, r *http.Request) error {
	return s.masterButton(w, r, s.PropertyToZoho)
}

// masterButton is "Create in Zoho" on the Vendors, Items and Properties tabs: the same as the automatic job, now.
func (s *Service) masterButton(w http.ResponseWriter, r *http.Request, fn func(context.Context, *access.User, int64) (string, error)) error {
	u := access.From(r)
	if err := needMap(u); err != nil {
		return err
	}
	id, err := httpjson.ID(r, "id")
	if err != nil {
		return err
	}
	msg, err := fn(r.Context(), u, id)
	if err != nil {
		return err
	}
	return httpjson.OK(w, map[string]string{"message": msg})
}

// VendorToZoho links a verified vendor to the Zoho vendor with its GSTIN (an unregistered one: the only Zoho vendor
// of its name), or creates it in Zoho from the details verified here. Bank details are not sent.
func (s *Service) VendorToZoho(ctx context.Context, u *access.User, vid int64) (string, error) {
	var v struct {
		Code, Name, GSTIN, State, Terms, Email, Mobile, Contact, Type string
		GST, Verified, Active                                         bool
	}
	if err := s.DB.QueryRow(ctx, `SELECT code, legal_name, coalesce(gstin,''), state_code, payment_terms, email, mobile, contact_person, vendor_type, gst_registered, bank_verified, active FROM vendor WHERE id=$1`, vid).
		Scan(&v.Code, &v.Name, &v.GSTIN, &v.State, &v.Terms, &v.Email, &v.Mobile, &v.Contact, &v.Type, &v.GST, &v.Verified, &v.Active); err != nil {
		return "", httpjson.NotFound("Vendor")
	}
	if v.Type == "warehouse" {
		return "", httpjson.Bad("The central warehouse is not a Zoho vendor.")
	}
	if !v.Verified {
		return "", held("Finance must verify the bank details before the vendor goes to Zoho.")
	}
	var linked bool
	_ = s.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM zoho_map_vendor WHERE vendor_id=$1)`, vid).Scan(&linked)
	if linked {
		return "", httpjson.Bad("%s is already linked to a Zoho vendor.", v.Name)
	}
	free := `AND NOT EXISTS (SELECT 1 FROM zoho_map_vendor m WHERE m.contact_id=c.contact_id)`
	var existing, existingName, how string
	if v.GST && v.GSTIN != "" {
		how = "gstin"
		_ = s.DB.QueryRow(ctx, `SELECT c.contact_id, c.name FROM zoho_contact c WHERE c.active AND upper(c.gst_no)=upper($1) `+free+` ORDER BY c.contact_id LIMIT 1`, v.GSTIN).Scan(&existing, &existingName)
	} else {
		how = "name"
		var n int
		_ = s.DB.QueryRow(ctx, `SELECT count(*), min(c.contact_id), min(c.name) FROM zoho_contact c WHERE c.active AND c.gst_no='' AND norm_key(c.name)=norm_key($1) `+free, v.Name).Scan(&n, &existing, &existingName)
		if n != 1 {
			existing = ""
		}
	}
	if existing != "" {
		if _, err := s.DB.Exec(ctx, `INSERT INTO zoho_map_vendor(vendor_id,contact_id,how,updated_by) VALUES ($1,$2,$3,$4) ON CONFLICT (vendor_id) DO NOTHING`, vid, existing, how, by(u)); err != nil {
			return "", err
		}
		_ = auditlog.Write(ctx, s.DB, u, "Zoho", "Vendor linked", v.Code, "Zoho contact "+existingName+" ("+existing+"), same "+map[string]string{"gstin": "GSTIN", "name": "name"}[how])
		return v.Name + " is already in Zoho as " + existingName + ", so it is linked.", nil
	}
	var dup string
	_ = s.DB.QueryRow(ctx, `SELECT name FROM zoho_contact WHERE active AND norm_key(name)=norm_key($1) LIMIT 1`, v.Name).Scan(&dup)
	if dup != "" {
		return "", held("Zoho has a vendor named %s with other GST details. Link the right one on the Vendors tab of Settings, Zoho Books.", dup)
	}
	if !s.PushEnabled {
		return "", held("Writing to Zoho is switched off on this server (ZOHO_PUSH_ENABLED=false).")
	}
	c, _, err := s.Client(ctx)
	if err != nil {
		return "", err
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
	ct, err := c.CreateVendor(ctx, body)
	if err != nil {
		return "", httpjson.Bad("Zoho did not create the vendor: %v", err)
	}
	err = pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO zoho_contact(contact_id,name,company,gst_no,gst_treatment,state_code,active) VALUES ($1,$2,$2,$3,$4,$5,TRUE) ON CONFLICT DO NOTHING`,
			ct.ID, v.Name, strings.ToUpper(v.GSTIN), body["gst_treatment"], v.State); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO zoho_map_vendor(vendor_id,contact_id,how,updated_by) VALUES ($1,$2,'created',$3)`, vid, ct.ID, by(u)); err != nil {
			return err
		}
		return auditlog.Write(ctx, tx, u, "Zoho", "Vendor created in Zoho", v.Code, v.Name+" · contact "+ct.ID+" · "+fmt.Sprint(body["gst_treatment"]))
	})
	if err != nil {
		return "", err
	}
	return v.Name + " created in Zoho and linked.", nil
}

// ItemToZoho creates a Purchase Desk item in Zoho and links it. An active Zoho item with the same SKU code or name is
// linked instead of creating a second one. The purchase account comes from the item's sub-category mapping and the
// GST from the tax mapping, so those must be mapped first.
func (s *Service) ItemToZoho(ctx context.Context, u *access.User, id int64) (string, error) {
	var code, item, brand, brandKind, pack, hsn, subcat, accountID, linked string
	var gst float64
	var rate *float64
	err := s.DB.QueryRow(ctx, `SELECT k.code, k.item_name, coalesce(b.name,''), coalesce(b.kind,''), ps.name, k.hsn, sc.name, coalesce(a.account_id,''), k.gst_pct::float8,
		(SELECT rc.rate::float8 FROM rate_card rc WHERE rc.sku_id=k.id AND NOT rc.superseded ORDER BY rc.valid_from DESC LIMIT 1),
		coalesce((SELECT item_id FROM zoho_map_item x WHERE x.sku_id=k.id),'')
		FROM sku k LEFT JOIN brand b ON b.id=k.brand_id JOIN pack_size ps ON ps.id=k.pack_size_id JOIN sub_category sc ON sc.id=k.sub_category_id
		LEFT JOIN zoho_map_account m ON m.kind='subcat' AND m.ref=sc.id::text LEFT JOIN zoho_account a ON a.account_id=m.account_id AND a.active
		WHERE k.id=$1`, id).Scan(&code, &item, &brand, &brandKind, &pack, &hsn, &subcat, &accountID, &gst, &rate, &linked)
	if err != nil {
		return "", httpjson.NotFound("Item")
	}
	if linked != "" {
		return "", httpjson.Bad("%s is already linked to a Zoho item.", item)
	}
	var fin string
	_ = s.DB.QueryRow(ctx, `SELECT CASE WHEN k.fin_status<>'approved' THEN k.fin_status WHEN sc.fin_status<>'approved' THEN 'sub-category '||sc.fin_status ELSE 'approved' END
		FROM sku k JOIN sub_category sc ON sc.id=k.sub_category_id WHERE k.id=$1`, id).Scan(&fin)
	if fin != "approved" {
		return "", held("%s waits for Finance to approve it (%s); it goes to Zoho once approved.", item, fin)
	}
	name := item
	if brandKind == "brand" && brand != "" {
		name = brand + " " + item
	}
	name += " (" + pack + ")"
	if len([]rune(name)) > 100 {
		name = string([]rune(name)[:100])
	}
	// One of this SKU code or name already in Zoho is linked, not duplicated.
	var existing, existingName string
	_ = s.DB.QueryRow(ctx, `SELECT item_id, name FROM zoho_item WHERE active AND ((sku<>'' AND norm_key(sku)=norm_key($1)) OR norm_key(name)=norm_key($2)) ORDER BY (norm_key(sku)=norm_key($1)) DESC LIMIT 1`, code, name).Scan(&existing, &existingName)
	if existing != "" {
		if _, err := s.DB.Exec(ctx, `INSERT INTO zoho_map_item(sku_id,item_id,how,updated_by) VALUES ($1,$2,'manual',$3)
			ON CONFLICT (sku_id) DO UPDATE SET item_id=EXCLUDED.item_id, how='manual', updated_by=EXCLUDED.updated_by, updated_at=now()`, id, existing, by(u)); err != nil {
			return "", err
		}
		_ = auditlog.Write(ctx, s.DB, u, "Zoho", "Mapping changed", "item "+code, existingName+" (already in Zoho)")
		return existingName + " already exists in Zoho, so it is linked.", nil
	}
	if accountID == "" {
		return "", held("Map the Zoho account for the sub-category %s first (Accounts tab). Zoho needs a purchase account for the item.", subcat)
	}
	// GST: the taxes mapped for this rate within the state and from another state.
	prefs := []map[string]any{}
	for _, sp := range []string{"intra", "inter"} {
		var taxID string
		_ = s.DB.QueryRow(ctx, `SELECT tax_id FROM zoho_map_tax WHERE gst_pct=$1 AND supply=$2`, gst, sp).Scan(&taxID)
		if taxID == "" {
			return "", held("Map the Zoho tax for GST %v%% (%s) first (Taxes tab), so the item carries its GST.", gst, map[string]string{"intra": "within the state", "inter": "from another state"}[sp])
		}
		prefs = append(prefs, map[string]any{"tax_id": taxID, "tax_specification": sp})
	}
	if !s.PushEnabled {
		return "", held("Writing to Zoho is switched off on this server (ZOHO_PUSH_ENABLED=false).")
	}
	product := "goods"
	if brandKind == "service" {
		product = "service"
	}
	r0 := 0.0
	if rate != nil {
		r0 = *rate
	}
	body := map[string]any{"name": name, "sku": code, "unit": pack, "rate": r0, "purchase_rate": r0, "item_type": "purchases", "product_type": product,
		"purchase_account_id": accountID, "purchase_description": subcat, "hsn_or_sac": hsn, "is_taxable": true, "item_tax_preferences": prefs}
	c, _, err := s.Client(ctx)
	if err != nil {
		return "", err
	}
	zi, err := c.CreateItem(ctx, body)
	if err != nil {
		return "", httpjson.Bad("Zoho did not create %s: %v", name, err)
	}
	if zi.ID == "" {
		return "", httpjson.Bad("Zoho did not return the new item %s. Click Sync now and check the Items tab.", name)
	}
	if _, err := s.DB.Exec(ctx, `INSERT INTO zoho_item(item_id,name,sku,hsn_or_sac,unit,product_type,item_type,brand,purchase_rate,tax_pct,purchase_account_id,active,synced_at)
		VALUES ($1,$2,$3,$4,$5,$6,'purchases','',$7,$8,$9,TRUE,now()) ON CONFLICT (item_id) DO NOTHING`, zi.ID, name, code, hsn, pack, product, r0, gst, accountID); err != nil {
		return "", err
	}
	if _, err := s.DB.Exec(ctx, `INSERT INTO zoho_map_item(sku_id,item_id,how,updated_by) VALUES ($1,$2,'manual',$3)
		ON CONFLICT (sku_id) DO UPDATE SET item_id=EXCLUDED.item_id, how='manual', updated_by=EXCLUDED.updated_by, updated_at=now()`, id, zi.ID, by(u)); err != nil {
		return "", err
	}
	_ = auditlog.Write(ctx, s.DB, u, "Zoho", "Item created in Zoho", name, code)
	return name + " created in Zoho and linked.", nil
}

// PropertyToZoho links a property to the reporting tag option of its name, or adds that option under the reporting
// tag of the property's Zoho location (its region, e.g. Delhi). Zoho's live tag list is checked, so a tag that exists
// there (even a draft) is reused, never duplicated. Without a location, the tag chosen on the Connection tab is used.
func (s *Service) PropertyToZoho(ctx context.Context, u *access.User, pid int64) (string, error) {
	var name, locationID, linked string
	if err := s.DB.QueryRow(ctx, `SELECT p.name, coalesce(m.location_id,''), coalesce(m.tag_option_id,'') FROM property p LEFT JOIN zoho_map_property m ON m.property_id=p.id WHERE p.id=$1`, pid).
		Scan(&name, &locationID, &linked); err != nil {
		return "", httpjson.NotFound("Property")
	}
	if linked != "" {
		return "", httpjson.Bad("%s is already linked to a Zoho reporting tag option.", name)
	}
	var fin string
	_ = s.DB.QueryRow(ctx, `SELECT fin_status FROM property WHERE id=$1`, pid).Scan(&fin)
	if fin != "approved" {
		return "", held("%s waits for Finance to approve it; it goes to Zoho once approved.", name)
	}
	link := func(optionID, tagName, what string) (string, error) {
		if _, err := s.DB.Exec(ctx, `INSERT INTO zoho_map_property(property_id,tag_option_id,updated_by) VALUES ($1,$2,$3)
			ON CONFLICT (property_id) DO UPDATE SET tag_option_id=EXCLUDED.tag_option_id, updated_by=EXCLUDED.updated_by, updated_at=now()`, pid, optionID, by(u)); err != nil {
			return "", err
		}
		_ = auditlog.Write(ctx, s.DB, u, "Zoho", what, name, tagName)
		return `"` + name + `" linked to the Zoho option under ` + tagName + `.`, nil
	}
	// An option of the same name already read from Zoho is linked.
	var optID, tagName string
	_ = s.DB.QueryRow(ctx, `SELECT o.option_id, t.name FROM zoho_tag_option o JOIN zoho_tag t ON t.tag_id=o.tag_id AND t.active WHERE o.active AND norm_key(o.name)=norm_key($1) ORDER BY o.option_id LIMIT 1`, name).Scan(&optID, &tagName)
	if optID != "" {
		return link(optID, tagName, "Property linked to Zoho option")
	}
	conn, err := s.load(ctx)
	if err != nil || conn == nil {
		return "", held("Zoho Books is not connected.")
	}
	var locName, locState string
	if locationID != "" {
		_ = s.DB.QueryRow(ctx, `SELECT name, state_code FROM zoho_location WHERE location_id=$1`, locationID).Scan(&locName, &locState)
	}
	if locName == "" && conn.PropertyTagID == "" {
		return "", held("Choose the property's Zoho location first (Properties tab), so its option goes under that region's reporting tag.")
	}
	if !s.PushEnabled {
		return "", held("Writing to Zoho is switched off on this server (ZOHO_PUSH_ENABLED=false).")
	}
	tagID, err := s.regionTag(ctx, locationID)
	if err != nil {
		return "", err
	}
	c, _, err := s.Client(ctx)
	if err != nil {
		return "", err
	}
	live, err := c.ReportingTags(ctx)
	if err != nil {
		return "", httpjson.Bad("Zoho: %v", err)
	}
	if tagID == "" && locName != "" {
		for _, t := range live {
			if textclean.Key(t.Name) == textclean.Key(locName) || textclean.Key(t.Name) == textclean.Key(indiantax.StateName(locState)) {
				tagID = t.ID
				break
			}
		}
	}
	if tagID == "" && locName == "" {
		tagID = conn.PropertyTagID
	}
	var tag *zohoapi.ReportingTag
	for i := range live {
		if live[i].ID == tagID {
			tag = &live[i]
		}
	}
	if tag == nil && tagID != "" {
		return "", httpjson.Bad("The reporting tag was not found in Zoho. Click Sync now and try again.")
	}
	if tag == nil {
		// The region has no reporting tag yet: create one named after the location.
		t, err := c.CreateReportingTag(ctx, locName)
		if err != nil {
			return "", httpjson.Bad("Zoho did not create the reporting tag %s: %v", locName, err)
		}
		if t.ID == "" {
			return "", httpjson.Bad("Zoho did not return the new reporting tag %s. Click Sync now and try again.", locName)
		}
		inactive := false
		t.IsActive = &inactive
		tag = &t
		_ = auditlog.Write(ctx, s.DB, u, "Zoho", "Reporting tag created in Zoho", locName, "for property "+name)
	}
	for _, o := range tag.Options {
		if textclean.Key(o.Name) == textclean.Key(name) && o.ID != "" {
			return link(o.ID, tag.Name, "Property linked to Zoho option")
		}
	}
	if err := c.AddTagOption(ctx, tag.ID, name); err != nil {
		return "", httpjson.Bad("Zoho did not add %s to the reporting tag %s: %v", name, tag.Name, err)
	}
	// A tag made through the API starts as a draft, and an inactive tag cannot be used on a bill: switch it on.
	if tag.Status == "inactive" || (tag.IsActive != nil && !*tag.IsActive) {
		if err := c.ActivateReportingTag(ctx, tag.ID); err != nil {
			return "", httpjson.Bad("%s was added to %s, but Zoho did not activate the tag: %v. Activate it in Zoho, then click Sync now.", name, tag.Name, err)
		}
	}
	if _, err := s.Sync(ctx); err != nil {
		return "", httpjson.Bad("Created in Zoho, but reading it back failed: %v", err)
	}
	var optionID string
	_ = s.DB.QueryRow(ctx, `SELECT option_id FROM zoho_tag_option WHERE tag_id=$1 AND norm_key(name)=norm_key($2) LIMIT 1`, tag.ID, name).Scan(&optionID)
	if optionID == "" {
		return "", httpjson.Bad("Created in Zoho under %s, but it did not come back on sync. Click Sync now and pick it from the list.", tag.Name)
	}
	if _, err := link(optionID, tag.Name, "Reporting tag option created in Zoho"); err != nil {
		return "", err
	}
	return `"` + name + `" created in Zoho under ` + tag.Name + ` and linked.`, nil
}

// accountFor gives a sub-category, bill type or expense head its Zoho expense account once Finance has approved it:
// the active account of exactly its name, or a new expense account named after it.
func (s *Service) accountFor(kind string) func(context.Context, *access.User, int64) (string, error) {
	table := map[string]string{"subcat": "sub_category", "billtype": "bill_type", "head": "expense_head"}[kind]
	return func(ctx context.Context, u *access.User, id int64) (string, error) {
		var name, fin string
		if err := s.DB.QueryRow(ctx, `SELECT name, fin_status FROM `+table+` WHERE id=$1`, id).Scan(&name, &fin); err != nil {
			return "", httpjson.NotFound("Record")
		}
		if fin != "approved" {
			return "", held("%s waits for Finance to approve it; its Zoho account is made once approved.", name)
		}
		link := func(account, how string) (string, error) {
			if _, err := s.DB.Exec(ctx, `INSERT INTO zoho_map_account(kind,ref,account_id,updated_by) VALUES ($1,$2,$3,$4)
				ON CONFLICT (kind,ref) DO UPDATE SET account_id=EXCLUDED.account_id, updated_by=EXCLUDED.updated_by, updated_at=now()`, kind, fmt.Sprint(id), account, by(u)); err != nil {
				return "", err
			}
			_ = auditlog.Write(ctx, s.DB, u, "Zoho", how, name, kind)
			return name + ": " + strings.ToLower(how), nil
		}
		var existing string
		_ = s.DB.QueryRow(ctx, `SELECT account_id FROM zoho_account WHERE active AND norm_key(name)=norm_key($1) ORDER BY account_id LIMIT 1`, name).Scan(&existing)
		if existing != "" {
			return link(existing, "Linked to the Zoho account of the same name")
		}
		if !s.PushEnabled {
			return "", held("Writing to Zoho is switched off on this server (ZOHO_PUSH_ENABLED=false).")
		}
		c, _, err := s.Client(ctx)
		if err != nil {
			return "", err
		}
		a, err := c.CreateAccount(ctx, name, "", "expense")
		if err != nil {
			return "", httpjson.Bad("Zoho did not create the account %s: %v", name, err)
		}
		if _, err := s.DB.Exec(ctx, `INSERT INTO zoho_account(account_id,name,code,account_type,active) VALUES ($1,$2,'','expense',TRUE) ON CONFLICT DO NOTHING`, a.ID, name); err != nil {
			return "", err
		}
		return link(a.ID, "Expense account created in Zoho")
	}
}

// SyncMasters is the automatic side of "Create in Zoho": every verified vendor, active item and active property not
// yet linked is linked to its Zoho record or created there. It runs with the other Zoho sending (see zohopush.SendAll)
// unless switched off on the Connection tab. A held one is checked again on every run, one Zoho refused waits half an
// hour; the last problem of each is kept for the Zoho page.
func (s *Service) SyncMasters(ctx context.Context) error {
	conn, err := s.load(ctx)
	if err != nil || conn == nil || !conn.Connected || !conn.AutoMasters {
		return err
	}
	retry := `NOT EXISTS (SELECT 1 FROM zoho_master_sync z WHERE z.kind=$1 AND z.ref_id=x.id AND z.error NOT LIKE 'Held:%' AND z.tried_at > now() - interval '30 minutes')`
	kinds := []struct {
		kind, sql string
		fn        func(context.Context, *access.User, int64) (string, error)
	}{
		{"vendor", `SELECT x.id FROM vendor x WHERE x.active AND x.bank_verified AND x.vendor_type<>'warehouse' AND NOT EXISTS (SELECT 1 FROM zoho_map_vendor m WHERE m.vendor_id=x.id)`, s.VendorToZoho},
		{"item", `SELECT x.id FROM sku x JOIN sub_category sc ON sc.id=x.sub_category_id WHERE x.active AND x.fin_status='approved' AND sc.fin_status='approved' AND NOT EXISTS (SELECT 1 FROM zoho_map_item m WHERE m.sku_id=x.id)`, s.ItemToZoho},
		{"property", `SELECT x.id FROM property x WHERE x.active AND x.fin_status='approved' AND NOT EXISTS (SELECT 1 FROM zoho_map_property m WHERE m.property_id=x.id AND m.tag_option_id IS NOT NULL AND m.tag_option_id<>'')`, s.PropertyToZoho},
		// Approved sub-categories, bill types and expense heads get their Zoho expense account.
		{"subcat", `SELECT x.id FROM sub_category x WHERE x.active AND x.fin_status='approved' AND NOT EXISTS (SELECT 1 FROM zoho_map_account m WHERE m.kind='subcat' AND m.ref=x.id::text)`, s.accountFor("subcat")},
		{"billtype", `SELECT x.id FROM bill_type x WHERE x.active AND x.fin_status='approved' AND NOT EXISTS (SELECT 1 FROM zoho_map_account m WHERE m.kind='billtype' AND m.ref=x.id::text)`, s.accountFor("billtype")},
		{"head", `SELECT x.id FROM expense_head x WHERE x.active AND x.fin_status='approved' AND NOT EXISTS (SELECT 1 FROM zoho_map_account m WHERE m.kind='head' AND m.ref=x.id::text)`, s.accountFor("head")},
	}
	for _, k := range kinds {
		rows, err := s.DB.Query(ctx, k.sql+` AND `+retry+` ORDER BY x.id LIMIT 25`, k.kind)
		if err != nil {
			return err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
		if err != nil {
			return err
		}
		for _, id := range ids {
			_, err := k.fn(ctx, nil, id)
			if err == nil {
				if _, err := s.DB.Exec(ctx, `DELETE FROM zoho_master_sync WHERE kind=$1 AND ref_id=$2`, k.kind, id); err != nil {
					return err
				}
				continue
			}
			if zohoapi.DailyLimit(err) || errors.Is(err, zohoapi.ErrTokenRevoked) {
				return err
			}
			msg := err.Error()
			var h Held
			if errors.As(err, &h) {
				msg = "Held: " + msg
			}
			if _, err := s.DB.Exec(ctx, `INSERT INTO zoho_master_sync(kind,ref_id,error,tried_at) VALUES ($1,$2,$3,now())
				ON CONFLICT (kind,ref_id) DO UPDATE SET error=EXCLUDED.error, tried_at=now()`, k.kind, id, msg); err != nil {
				return err
			}
		}
	}
	return nil
}

// MasterProblem is a master that has not reached Zoho yet, and why.
type MasterProblem struct {
	Kind    string    `json:"kind"`
	ID      int64     `json:"id"`
	Name    string    `json:"name"`
	Problem string    `json:"problem"`
	At      time.Time `json:"at"`
}

// masterProblems lists the masters the automatic job could not send yet, for the Zoho page.
func (s *Service) masterProblems(w http.ResponseWriter, r *http.Request) error {
	rows, err := s.DB.Query(r.Context(), `SELECT z.kind, z.ref_id,
		coalesce(CASE z.kind WHEN 'vendor' THEN (SELECT code||' · '||legal_name FROM vendor WHERE id=z.ref_id)
			WHEN 'item' THEN (SELECT code||' · '||item_name FROM sku WHERE id=z.ref_id)
			WHEN 'property' THEN (SELECT code||' · '||name FROM property WHERE id=z.ref_id)
			WHEN 'subcat' THEN (SELECT 'Sub-category '||name FROM sub_category WHERE id=z.ref_id)
			WHEN 'billtype' THEN (SELECT 'Bill type '||name FROM bill_type WHERE id=z.ref_id)
			ELSE (SELECT 'Expense head '||name FROM expense_head WHERE id=z.ref_id) END, ''), z.error, z.tried_at
		FROM zoho_master_sync z ORDER BY z.kind, z.ref_id LIMIT 500`)
	if err != nil {
		return err
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByPos[MasterProblem])
	if err != nil {
		return err
	}
	return httpjson.OK(w, out)
}

// MasterStatus says whether one vendor, item or property is in Zoho, and if not, why.
type MasterStatus struct {
	Linked   bool       `json:"linked"`
	ZohoName string     `json:"zoho_name"`
	Problem  string     `json:"problem"`
	At       *time.Time `json:"at"`
	Auto     bool       `json:"auto"` // sent automatically (Connection tab)
	CanSend  bool       `json:"can_send"`
}

// masterStatus is the Zoho Books card on a vendor's, item's or property's page.
func (s *Service) masterStatus(w http.ResponseWriter, r *http.Request) error {
	kind := chi.URLParam(r, "kind")
	id, err := httpjson.ID(r, "id")
	if err != nil {
		return err
	}
	q := map[string]string{
		"vendor":   `SELECT c.name FROM zoho_map_vendor m JOIN zoho_contact c ON c.contact_id=m.contact_id WHERE m.vendor_id=$1`,
		"item":     `SELECT coalesce(i.name, m.item_id) FROM zoho_map_item m LEFT JOIN zoho_item i ON i.item_id=m.item_id WHERE m.sku_id=$1`,
		"property": `SELECT coalesce(t.name||' › '||o.name, m.tag_option_id) FROM zoho_map_property m LEFT JOIN zoho_tag_option o ON o.option_id=m.tag_option_id LEFT JOIN zoho_tag t ON t.tag_id=o.tag_id WHERE m.property_id=$1 AND m.tag_option_id<>''`,
	}[kind]
	if q == "" {
		return httpjson.NotFound("Kind")
	}
	out := MasterStatus{CanSend: canMap(access.From(r))}
	if err := s.DB.QueryRow(r.Context(), q, id).Scan(&out.ZohoName); err == nil {
		out.Linked = true
	}
	var problem string
	var at time.Time
	if err := s.DB.QueryRow(r.Context(), `SELECT error, tried_at FROM zoho_master_sync WHERE kind=$1 AND ref_id=$2`, kind, id).Scan(&problem, &at); err == nil && !out.Linked {
		out.Problem, out.At = strings.TrimPrefix(problem, "Held: "), &at
	}
	if c, err := s.load(r.Context()); err == nil && c != nil {
		out.Auto = c.AutoMasters && c.Connected
	}
	return httpjson.OK(w, out)
}

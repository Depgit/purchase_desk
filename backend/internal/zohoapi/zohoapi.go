// Package zohoapi talks to the Zoho Books API v3 (India data centre by default).
// It renews the one-hour access token from the refresh token by itself, reads lists page by page,
// retries on rate limits, and reports Zoho's own error messages.
package zohoapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"
)

type Config struct {
	AccountsURL  string // https://accounts.zoho.in
	APIURL       string // https://www.zohoapis.in
	OrgID        string
	ClientID     string
	ClientSecret string
	RefreshToken string
}

type Client struct {
	cfg  Config
	http *http.Client

	mu      sync.Mutex
	token   string
	expires time.Time
	callMu  sync.Mutex // one call at a time: Zoho allows 10 concurrent calls; bills are pushed one after another
}

func New(cfg Config) *Client {
	return &Client{cfg: cfg, http: &http.Client{Timeout: 60 * time.Second}}
}

func (c *Client) Config() Config { return c.cfg }

// Error is a reply from Zoho that was not a success.
type Error struct {
	HTTP    int
	Code    int
	Message string
}

func (e *Error) Error() string {
	return fmt.Sprintf("Zoho error %d (HTTP %d): %s", e.Code, e.HTTP, e.Message)
}

// DailyLimit reports the "daily limit of the plan reached" error (HTTP 429, code 45).
func DailyLimit(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.HTTP == 429 && e.Code == 45
}

// Usage is the organisation's daily API allowance as Zoho reports it on every reply (X-Rate-Limit-Limit,
// X-Rate-Limit-Remaining, X-Rate-Limit-Reset in seconds). It is the plan's real figure: 1,000 a day on Free up to
// 10,000 on Premium, Elite and Ultimate, shared by every app using the organisation.
type Usage struct {
	Limit     int       `json:"limit"`
	Remaining int       `json:"remaining"`
	ResetAt   time.Time `json:"reset_at"`
	SeenAt    time.Time `json:"seen_at"` // zero until the first call since the server started
}

// Calls are spaced to stay under Zoho's 100 calls a minute per organisation, with room for the people using Zoho
// at the same time: about 80 a minute.
const callGap = 750 * time.Millisecond

var (
	usageMu  sync.Mutex
	usage    Usage
	lastCall time.Time
)

// CurrentUsage is the allowance seen on the last reply from Zoho.
func CurrentUsage() Usage {
	usageMu.Lock()
	defer usageMu.Unlock()
	return usage
}

// LowOnCalls reports that less than a fifth of today's allowance is left. The automatic jobs then wait for the reset,
// keeping the rest for the buttons people press.
func LowOnCalls() bool {
	u := CurrentUsage()
	return !u.SeenAt.IsZero() && u.Limit > 0 && time.Now().Before(u.ResetAt) && u.Remaining*5 < u.Limit
}

func noteUsage(h http.Header) {
	limit, err1 := strconv.Atoi(h.Get("X-Rate-Limit-Limit"))
	left, err2 := strconv.Atoi(h.Get("X-Rate-Limit-Remaining"))
	if err1 != nil || err2 != nil {
		return
	}
	reset, _ := strconv.Atoi(h.Get("X-Rate-Limit-Reset"))
	usageMu.Lock()
	usage = Usage{Limit: limit, Remaining: left, ResetAt: time.Now().Add(time.Duration(reset) * time.Second), SeenAt: time.Now()}
	usageMu.Unlock()
}

// pace waits until the next call is allowed.
func pace(ctx context.Context) error {
	usageMu.Lock()
	wait := time.Until(lastCall.Add(callGap))
	usageMu.Unlock()
	if wait > 0 {
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	usageMu.Lock()
	lastCall = time.Now()
	usageMu.Unlock()
	return nil
}

// TokenRevoked reports that the refresh token no longer works and a new one is needed.
var ErrTokenRevoked = errors.New("the Zoho refresh token was rejected; enter a new one under Settings, Zoho Books")

// accessToken returns a valid access token, renewing it from the refresh token when it is near expiry.
func (c *Client) accessToken(ctx context.Context, force bool) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !force && c.token != "" && time.Now().Before(c.expires.Add(-2*time.Minute)) {
		return c.token, nil
	}
	if c.cfg.ClientID == "" || c.cfg.ClientSecret == "" || c.cfg.RefreshToken == "" {
		return "", errors.New("Zoho connection settings are not complete")
	}
	form := url.Values{
		"refresh_token": {c.cfg.RefreshToken}, "client_id": {c.cfg.ClientID},
		"client_secret": {c.cfg.ClientSecret}, "grant_type": {"refresh_token"},
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.AccountsURL+"/oauth/v2/token", bytes.NewBufferString(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("could not reach Zoho sign-in: %w", err)
	}
	defer res.Body.Close()
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		Error       string `json:"error"`
	}
	_ = json.NewDecoder(res.Body).Decode(&out)
	if out.AccessToken == "" {
		if out.Error == "invalid_code" || out.Error == "invalid_client" || out.Error == "invalid_client_secret" {
			return "", ErrTokenRevoked
		}
		return "", fmt.Errorf("Zoho did not give an access token: %s", out.Error)
	}
	c.token = out.AccessToken
	if out.ExpiresIn <= 0 {
		out.ExpiresIn = 3600
	}
	c.expires = time.Now().Add(time.Duration(out.ExpiresIn) * time.Second)
	return c.token, nil
}

// Do sends one call. body is JSON-encoded unless it is a *Multipart. headers are extra request headers.
// It renews the token once if Zoho says it expired, and waits and retries when over the per-minute limit.
func (c *Client) Do(ctx context.Context, method, path string, query url.Values, body any, headers map[string]string, out any) error {
	c.callMu.Lock()
	defer c.callMu.Unlock()
	if query == nil {
		query = url.Values{}
	}
	query.Set("organization_id", c.cfg.OrgID)
	u := c.cfg.APIURL + "/books/v3" + path + "?" + query.Encode()
	force := false
	for attempt := 0; attempt < 4; attempt++ {
		tok, err := c.accessToken(ctx, force)
		if err != nil {
			return err
		}
		var rd io.Reader
		ctype := ""
		switch b := body.(type) {
		case nil:
		case *Multipart:
			rd, ctype = bytes.NewReader(b.body), b.contentType
		default:
			j, err := json.Marshal(b)
			if err != nil {
				return err
			}
			rd, ctype = bytes.NewReader(j), "application/json"
		}
		req, err := http.NewRequestWithContext(ctx, method, u, rd)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Zoho-oauthtoken "+tok)
		if ctype != "" {
			req.Header.Set("Content-Type", ctype)
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		if err := pace(ctx); err != nil {
			return err
		}
		res, err := c.http.Do(req)
		if err != nil {
			return fmt.Errorf("could not reach Zoho Books: %w", err)
		}
		noteUsage(res.Header)
		raw, _ := io.ReadAll(io.LimitReader(res.Body, 20<<20))
		res.Body.Close()
		var env struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(raw, &env)
		if res.StatusCode == http.StatusUnauthorized && !force {
			force = true // access token expired: renew with the refresh token and retry
			continue
		}
		if res.StatusCode == http.StatusTooManyRequests && env.Code != 45 && attempt < 3 {
			wait := 20 * time.Second
			if s, err := strconv.Atoi(res.Header.Get("Retry-After")); err == nil && s > 0 {
				wait = time.Duration(s) * time.Second
			}
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return ctx.Err()
			}
			continue
		}
		if res.StatusCode >= 300 || env.Code != 0 {
			msg := env.Message
			if msg == "" {
				msg = string(raw)
				if len(msg) > 300 {
					msg = msg[:300]
				}
			}
			return &Error{HTTP: res.StatusCode, Code: env.Code, Message: msg}
		}
		if out != nil {
			return json.Unmarshal(raw, out)
		}
		return nil
	}
	return errors.New("Zoho Books did not accept the call after several tries")
}

// Multipart is a file upload body.
type Multipart struct {
	body        []byte
	contentType string
}

func NewFileUpload(field, name, contentType string, data []byte) (*Multipart, error) {
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	h := make(map[string][]string)
	h["Content-Disposition"] = []string{fmt.Sprintf(`form-data; name="%s"; filename="%s"`, field, name)}
	h["Content-Type"] = []string{contentType}
	part, err := w.CreatePart(h)
	if err != nil {
		return nil, err
	}
	if _, err := part.Write(data); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return &Multipart{body: b.Bytes(), contentType: w.FormDataContentType()}, nil
}

// ListAll reads every page of a list. Zoho's has_more_page flag is not reliable on some lists,
// so it keeps reading until a page comes back empty.
func (c *Client) ListAll(ctx context.Context, path, key string, query url.Values, each func(json.RawMessage) error) error {
	for page := 1; page <= 200; page++ {
		q := url.Values{}
		for k, v := range query {
			q[k] = v
		}
		q.Set("page", strconv.Itoa(page))
		q.Set("per_page", "200")
		var out map[string]json.RawMessage
		if err := c.Do(ctx, http.MethodGet, path, q, nil, nil, &out); err != nil {
			return err
		}
		var items []json.RawMessage
		if raw, ok := out[key]; ok {
			if err := json.Unmarshal(raw, &items); err != nil {
				return err
			}
		}
		if len(items) == 0 {
			return nil
		}
		for _, it := range items {
			if err := each(it); err != nil {
				return err
			}
		}
	}
	return nil
}

// ---------- typed calls ----------

type Organization struct {
	ID       string `json:"organization_id"`
	Name     string `json:"name"`
	PlanName string `json:"plan_name"`
}

func (c *Client) Organizations(ctx context.Context) ([]Organization, error) {
	var out struct {
		Organizations []Organization `json:"organizations"`
	}
	err := c.Do(ctx, http.MethodGet, "/organizations", nil, nil, nil, &out)
	return out.Organizations, err
}

type Account struct {
	ID       string `json:"account_id"`
	Name     string `json:"account_name"`
	Code     string `json:"account_code"`
	Type     string `json:"account_type"`
	IsActive bool   `json:"is_active"`
}

func (c *Client) Accounts(ctx context.Context) ([]Account, error) {
	out := []Account{}
	err := c.ListAll(ctx, "/chartofaccounts", "chartofaccounts", url.Values{"filter_by": {"AccountType.All"}}, func(raw json.RawMessage) error {
		var a Account
		if err := json.Unmarshal(raw, &a); err != nil {
			return err
		}
		out = append(out, a)
		return nil
	})
	return out, err
}

func (c *Client) CreateAccount(ctx context.Context, name, code, accountType string) (Account, error) {
	var out struct {
		Account Account `json:"chart_of_account"`
	}
	body := map[string]any{"account_name": name, "account_type": accountType}
	if code != "" {
		body["account_code"] = code
	}
	err := c.Do(ctx, http.MethodPost, "/chartofaccounts", nil, body, nil, &out)
	return out.Account, err
}

type Tax struct {
	ID            string  `json:"tax_id"`
	Name          string  `json:"tax_name"`
	Percentage    float64 `json:"tax_percentage"`
	Type          string  `json:"tax_type"`          // tax or tax_group
	SpecificType  string  `json:"tax_specific_type"` // igst, cgst, sgst, ...
	Specification string  `json:"tax_specification"` // intra or inter
	IsInactive    bool    `json:"is_inactive"`
}

func (c *Client) Taxes(ctx context.Context) ([]Tax, error) {
	out := []Tax{}
	err := c.ListAll(ctx, "/settings/taxes", "taxes", nil, func(raw json.RawMessage) error {
		var t Tax
		if err := json.Unmarshal(raw, &t); err != nil {
			return err
		}
		out = append(out, t)
		return nil
	})
	return out, err
}

type TagOption struct {
	ID       string      `json:"tag_option_id"`
	Name     string      `json:"tag_option_name"`
	IsActive *bool       `json:"is_active"`
	Status   string      `json:"status"`
	Children []TagOption `json:"children"`
}

// UnmarshalJSON accepts both shapes Zoho uses for an option: tag_option_id / tag_option_name (older replies)
// and option_id / option_name (GET /reportingtags/options).
func (o *TagOption) UnmarshalJSON(b []byte) error {
	var x struct {
		IsActive *bool       `json:"is_active"`
		Status   string      `json:"status"`
		Children []TagOption `json:"children"`
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	str := func(k string) string {
		var v string
		_ = json.Unmarshal(raw[k], &v)
		return v
	}
	_ = json.Unmarshal(b, &x)
	o.ID = str("tag_option_id")
	if o.ID == "" {
		o.ID = str("option_id")
	}
	o.Name = str("tag_option_name")
	if o.Name == "" {
		o.Name = str("option_name")
	}
	o.IsActive, o.Status, o.Children = x.IsActive, x.Status, x.Children
	return nil
}

type ReportingTag struct {
	ID       string      `json:"tag_id"`
	Name     string      `json:"tag_name"`
	Status   string      `json:"status"`
	IsActive *bool       `json:"is_active"`
	Options  []TagOption `json:"tag_options"`
}

// flatten lists an option and the options nested under it, so a sub-option can be picked too.
func flatten(opts []TagOption) []TagOption {
	out := []TagOption{}
	for _, o := range opts {
		kids := o.Children
		o.Children = nil
		out = append(out, o)
		out = append(out, flatten(kids)...)
	}
	return out
}

// ReportingTags lists every reporting tag with its options. GET /reportingtags returns the tags under "tags".
// Each tag's options come from GET /reportingtags/options?tag_id=...: on 8 Oct 2026 GET /reportingtags/{id}
// returned the tag's settings without options. The older reply shapes are still read as a fallback.
func (c *Client) ReportingTags(ctx context.Context) ([]ReportingTag, error) {
	var out struct {
		Tags          []ReportingTag `json:"tags"`
		ReportingTags []ReportingTag `json:"reporting_tags"`
	}
	if err := c.Do(ctx, http.MethodGet, "/reportingtags", nil, nil, nil, &out); err != nil {
		return nil, err
	}
	tags := append(out.Tags, out.ReportingTags...)
	for i := range tags {
		if len(tags[i].Options) > 0 {
			tags[i].Options = flatten(tags[i].Options)
			continue
		}
		var opts struct {
			Options []TagOption `json:"options"`
		}
		err := c.Do(ctx, http.MethodGet, "/reportingtags/options", url.Values{"tag_id": {tags[i].ID}}, nil, nil, &opts)
		if err == nil && len(opts.Options) > 0 {
			tags[i].Options = flatten(opts.Options)
			continue
		}
		var one map[string]json.RawMessage
		if err := c.Do(ctx, http.MethodGet, "/reportingtags/"+url.PathEscape(tags[i].ID), nil, nil, nil, &one); err != nil {
			return nil, err
		}
		for _, k := range []string{"reporting_tag", "tag"} {
			if raw, ok := one[k]; ok {
				var t ReportingTag
				if json.Unmarshal(raw, &t) == nil && len(t.Options) > 0 {
					tags[i].Options = flatten(t.Options)
				}
			}
		}
		if raw, ok := one["tag_options"]; ok && len(tags[i].Options) == 0 {
			var o []TagOption
			_ = json.Unmarshal(raw, &o)
			tags[i].Options = flatten(o)
		}
	}
	return tags, nil
}

// CreateReportingTag creates a reporting tag (for a region, such as "Delhi") with no options yet. It is enabled on
// purchases (bills) and journals at line level, because a bill carries the property tag on every line.
func (c *Client) CreateReportingTag(ctx context.Context, name string) (ReportingTag, error) {
	var out struct {
		Tag ReportingTag `json:"tag"`
	}
	body := map[string]any{
		"tag_name":     name,
		"is_mandatory": false,
		"entities":     []map[string]any{{"entity_name": "vendors", "is_enabled": false}},
		"multi_preference_entities": map[string]any{"preference": "line_item", "entities": []map[string]any{
			{"entity_name": "purchases", "is_enabled": true}, {"entity_name": "journals", "is_enabled": true}}},
	}
	err := c.Do(ctx, http.MethodPost, "/reportingtags", nil, body, nil, &out)
	return out.Tag, err
}

// liveOption is a reporting tag option as GET /reportingtags/options returns it, with its nested children.
type liveOption struct {
	ID        string       `json:"option_id"`
	Name      string       `json:"option_name"`
	IsActive  *bool        `json:"is_active"`
	IsDefault bool         `json:"is_default"`
	Children  []liveOption `json:"children"`
}

func (o liveOption) body() map[string]any {
	kids := []map[string]any{}
	for _, k := range o.Children {
		kids = append(kids, k.body())
	}
	return map[string]any{"option_id": o.ID, "option_name": o.Name, "is_active": o.IsActive == nil || *o.IsActive, "is_default": o.IsDefault, "children": kids}
}

// AddTagOption adds an option (a property) to a reporting tag. Zoho replaces the whole option list on update
// (PUT /reportingtags/{id}/options, key "options", fields option_id / option_name), so every existing option is
// sent back with its id and children, then the new one.
func (c *Client) AddTagOption(ctx context.Context, tagID, name string) error {
	var cur struct {
		Options []liveOption `json:"options"`
	}
	if err := c.Do(ctx, http.MethodGet, "/reportingtags/options", url.Values{"tag_id": {tagID}}, nil, nil, &cur); err != nil {
		return err
	}
	opts := []map[string]any{}
	for _, o := range cur.Options {
		opts = append(opts, o.body())
	}
	opts = append(opts, map[string]any{"option_name": name, "is_active": true, "is_default": false, "children": []map[string]any{}})
	return c.Do(ctx, http.MethodPut, "/reportingtags/"+url.PathEscape(tagID)+"/options", nil, map[string]any{"options": opts}, nil, nil)
}

// ActivateReportingTag makes a tag usable on transactions. A tag created through the API starts as a draft.
func (c *Client) ActivateReportingTag(ctx context.Context, tagID string) error {
	return c.Do(ctx, http.MethodPost, "/reportingtags/"+url.PathEscape(tagID)+"/active", nil, nil, nil, nil)
}

type Location struct {
	ID      string `json:"location_id"`
	Name    string `json:"location_name"`
	TaxReg  string `json:"tax_reg_no"`
	Status  string `json:"status"`
	Address struct {
		StateCode string `json:"state_code"`
	} `json:"address"`
}

func (c *Client) Locations(ctx context.Context) ([]Location, error) {
	var out struct {
		Locations []Location `json:"locations"`
	}
	err := c.Do(ctx, http.MethodGet, "/locations", nil, nil, nil, &out)
	return out.Locations, err
}

type Contact struct {
	ID           string `json:"contact_id"`
	Name         string `json:"contact_name"`
	Company      string `json:"company_name"`
	GSTNo        string `json:"gst_no"`
	GSTTreatment string `json:"gst_treatment"`
	Place        string `json:"place_of_contact"`
	Status       string `json:"status"`
	ContactType  string `json:"contact_type"`
}

// Vendors reads every vendor contact, active and inactive. Only contact_type=vendor is sent: adding
// filter_by=Status.All makes Zoho drop the vendor filter and return customers too (seen live on 8 Oct 2026:
// 10,382 contacts instead of 962 vendors). Customers are skipped here as well, in case Zoho sends any.
func (c *Client) Vendors(ctx context.Context) ([]Contact, error) {
	out := []Contact{}
	err := c.ListAll(ctx, "/contacts", "contacts", url.Values{"contact_type": {"vendor"}}, func(raw json.RawMessage) error {
		var x Contact
		if err := json.Unmarshal(raw, &x); err != nil {
			return err
		}
		if x.ContactType != "" && x.ContactType != "vendor" {
			return nil
		}
		out = append(out, x)
		return nil
	})
	return out, err
}

// Item is one item of the Zoho Books item list.
type Item struct {
	ID                string  `json:"item_id"`
	Name              string  `json:"name"`
	SKU               string  `json:"sku"`
	HSN               string  `json:"hsn_or_sac"`
	Unit              string  `json:"unit"`
	ProductType       string  `json:"product_type"`
	ItemType          string  `json:"item_type"`
	Brand             string  `json:"brand"`
	PurchaseRate      float64 `json:"purchase_rate"`
	TaxPct            float64 `json:"tax_percentage"`
	PurchaseAccountID string  `json:"purchase_account_id"`
	Status            string  `json:"status"`
}

// Items reads every item, active and inactive (GET /items, 200 a page). Saltstayz had 333 on 9 Oct 2026.
func (c *Client) Items(ctx context.Context) ([]Item, error) {
	out := []Item{}
	err := c.ListAll(ctx, "/items", "items", url.Values{}, func(raw json.RawMessage) error {
		var x Item
		if err := json.Unmarshal(raw, &x); err != nil {
			return err
		}
		out = append(out, x)
		return nil
	})
	return out, err
}

// CreateItem creates an item (POST /items) from a Purchase Desk item. The reply carries it under "item".
func (c *Client) CreateItem(ctx context.Context, body map[string]any) (Item, error) {
	var out struct {
		Item Item `json:"item"`
	}
	err := c.Do(ctx, http.MethodPost, "/items", nil, body, nil, &out)
	return out.Item, err
}

// CreateVendor creates a vendor contact from details verified in Purchase Desk.
func (c *Client) CreateVendor(ctx context.Context, body map[string]any) (Contact, error) {
	var out struct {
		Contact Contact `json:"contact"`
	}
	err := c.Do(ctx, http.MethodPost, "/contacts", nil, body, nil, &out)
	return out.Contact, err
}

type Bill struct {
	ID          string  `json:"bill_id"`
	BillNumber  string  `json:"bill_number"`
	Status      string  `json:"status"`
	Total       float64 `json:"total"`
	Balance     float64 `json:"balance"`
	PaymentMade float64 `json:"payment_made"`
	LastMod     string  `json:"last_modified_time"`
}

// PurchaseOrder is a Zoho purchase order as Purchase Desk reads it back: its id, number, status and the ids Zoho gives
// its lines (in the order they were sent), which a bill quotes to bill against the PO.
type PurchaseOrder struct {
	ID        string `json:"purchaseorder_id"`
	Number    string `json:"purchaseorder_number"`
	Status    string `json:"status"`
	LineItems []struct {
		ID        string `json:"line_item_id"`
		ItemID    string `json:"item_id"`
		AccountID string `json:"account_id"`
	} `json:"line_items"`
}

// NoCommonItems reports Zoho's refusal to link a bill to a purchase order whose lines carry other items (code 4197).
func NoCommonItems(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Code == 4197
}

// CreatePurchaseOrder creates a PO under Purchase Desk's own number (ignore_auto_number_generation), so the number in
// Zoho is the PO number the vendor was sent.
func (c *Client) CreatePurchaseOrder(ctx context.Context, body map[string]any) (PurchaseOrder, error) {
	var out struct {
		PO PurchaseOrder `json:"purchaseorder"`
	}
	err := c.Do(ctx, http.MethodPost, "/purchaseorders", url.Values{"ignore_auto_number_generation": {"true"}}, body, nil, &out)
	return out.PO, err
}

// UpdatePurchaseOrder replaces a PO's details and lines (after a balance is rejected or the PO closes short).
func (c *Client) UpdatePurchaseOrder(ctx context.Context, id string, body map[string]any) (PurchaseOrder, error) {
	var out struct {
		PO PurchaseOrder `json:"purchaseorder"`
	}
	err := c.Do(ctx, http.MethodPut, "/purchaseorders/"+url.PathEscape(id), url.Values{"ignore_auto_number_generation": {"true"}}, body, nil, &out)
	return out.PO, err
}

// FindPurchaseOrder looks for a PO by number, so a retry after a lost reply does not create it twice.
func (c *Client) FindPurchaseOrder(ctx context.Context, number string) (*PurchaseOrder, error) {
	var out struct {
		POs []PurchaseOrder `json:"purchaseorders"`
	}
	if err := c.Do(ctx, http.MethodGet, "/purchaseorders", url.Values{"purchaseorder_number": {number}}, nil, nil, &out); err != nil {
		return nil, err
	}
	for _, p := range out.POs {
		if p.Number == number {
			return &p, nil
		}
	}
	return nil, nil
}

// GetPurchaseOrder reads a PO with its line ids.
func (c *Client) GetPurchaseOrder(ctx context.Context, id string) (PurchaseOrder, error) {
	var out struct {
		PO PurchaseOrder `json:"purchaseorder"`
	}
	err := c.Do(ctx, http.MethodGet, "/purchaseorders/"+url.PathEscape(id), nil, nil, nil, &out)
	return out.PO, err
}

// MarkPurchaseOrderOpen issues a draft PO (status open).
func (c *Client) MarkPurchaseOrderOpen(ctx context.Context, id string) error {
	return c.Do(ctx, http.MethodPost, "/purchaseorders/"+url.PathEscape(id)+"/status/open", nil, nil, nil, nil)
}

// MarkPurchaseOrderCancelled cancels a PO that will not be supplied.
func (c *Client) MarkPurchaseOrderCancelled(ctx context.Context, id string) error {
	return c.Do(ctx, http.MethodPost, "/purchaseorders/"+url.PathEscape(id)+"/status/cancelled", nil, nil, nil, nil)
}

// UpsertBill creates the bill, or updates it if a bill with the same unique custom field value exists (PUT /bills with X-Upsert).
func (c *Client) UpsertBill(ctx context.Context, keyField, keyValue string, body map[string]any) (Bill, error) {
	var out struct {
		Bill Bill `json:"bill"`
	}
	h := map[string]string{"X-Unique-Identifier-Key": keyField, "X-Unique-Identifier-Value": keyValue, "X-Upsert": "true"}
	err := c.Do(ctx, http.MethodPut, "/bills", nil, body, h, &out)
	return out.Bill, err
}

// CreateBill is used when the unique custom field is not set up in Zoho.
func (c *Client) CreateBill(ctx context.Context, body map[string]any) (Bill, error) {
	var out struct {
		Bill Bill `json:"bill"`
	}
	err := c.Do(ctx, http.MethodPost, "/bills", nil, body, nil, &out)
	return out.Bill, err
}

// FindBill looks for a bill by vendor and bill number, to avoid creating a duplicate after a lost reply.
func (c *Client) FindBill(ctx context.Context, vendorID, billNumber string) (*Bill, error) {
	var out struct {
		Bills []Bill `json:"bills"`
	}
	q := url.Values{"vendor_id": {vendorID}, "bill_number": {billNumber}}
	if err := c.Do(ctx, http.MethodGet, "/bills", q, nil, nil, &out); err != nil {
		return nil, err
	}
	for _, b := range out.Bills {
		if b.BillNumber == billNumber {
			return &b, nil
		}
	}
	return nil, nil
}

func (c *Client) AttachToBill(ctx context.Context, billID, name, contentType string, data []byte) error {
	mp, err := NewFileUpload("attachment", name, contentType, data)
	if err != nil {
		return err
	}
	return c.Do(ctx, http.MethodPost, "/bills/"+url.PathEscape(billID)+"/attachment", nil, mp, nil, nil)
}

func (c *Client) MarkBillOpen(ctx context.Context, billID string) error {
	return c.Do(ctx, http.MethodPost, "/bills/"+url.PathEscape(billID)+"/status/open", nil, nil, nil, nil)
}

func (c *Client) GetBill(ctx context.Context, billID string) (Bill, error) {
	var out struct {
		Bill Bill `json:"bill"`
	}
	err := c.Do(ctx, http.MethodGet, "/bills/"+url.PathEscape(billID), nil, nil, nil, &out)
	return out.Bill, err
}

type BillPayment struct {
	PaymentID   string  `json:"payment_id"`
	Date        string  `json:"date"`
	Amount      float64 `json:"amount"`
	ReferenceNo string  `json:"reference_number"`
	PaymentMode string  `json:"payment_mode"`
}

func (c *Client) BillPayments(ctx context.Context, billID string) ([]BillPayment, error) {
	var out struct {
		Payments []BillPayment `json:"payments"`
	}
	err := c.Do(ctx, http.MethodGet, "/bills/"+url.PathEscape(billID)+"/payments", nil, nil, nil, &out)
	return out.Payments, err
}

// ChangedBills lists bills modified since a time, for the payment read-back.
func (c *Client) ChangedBills(ctx context.Context, since time.Time) ([]Bill, error) {
	out := []Bill{}
	q := url.Values{"last_modified_time": {since.Format("2006-01-02T15:04:05-0700")}, "sort_column": {"last_modified_time"}}
	err := c.ListAll(ctx, "/bills", "bills", q, func(raw json.RawMessage) error {
		var b Bill
		if err := json.Unmarshal(raw, &b); err != nil {
			return err
		}
		out = append(out, b)
		return nil
	})
	return out, err
}

// CreateJournal posts a manual journal (used for the monthly warehouse issues).
func (c *Client) CreateJournal(ctx context.Context, body map[string]any) (string, string, error) {
	var out struct {
		Journal struct {
			ID     string `json:"journal_id"`
			Number string `json:"entry_number"`
		} `json:"journal"`
	}
	err := c.Do(ctx, http.MethodPost, "/journals", nil, body, nil, &out)
	return out.Journal.ID, out.Journal.Number, err
}

// CreateVendorPayment records a payment to a vendor (POST /vendorpayments). With no bills it stays in Zoho as an
// unapplied (excess) payment: how a vendor advance is kept until its bill arrives.
func (c *Client) CreateVendorPayment(ctx context.Context, body map[string]any) (string, error) {
	var out struct {
		Payment struct {
			ID string `json:"payment_id"`
		} `json:"vendorpayment"`
	}
	err := c.Do(ctx, http.MethodPost, "/vendorpayments", nil, body, nil, &out)
	return out.Payment.ID, err
}

// UpdateVendorPayment changes a vendor payment (PUT /vendorpayments/{id}); its bills list says how much of it is
// applied to which bill, and replaces the list Zoho had.
func (c *Client) UpdateVendorPayment(ctx context.Context, id string, body map[string]any) error {
	return c.Do(ctx, http.MethodPut, "/vendorpayments/"+url.PathEscape(id), nil, body, nil, nil)
}

// BillCustomFields lists custom fields on bills, to check the unique key field exists.
func (c *Client) BillCustomFields(ctx context.Context) ([]map[string]any, error) {
	var out struct {
		CustomFields map[string][]map[string]any `json:"customfields"`
	}
	err := c.Do(ctx, http.MethodGet, "/settings/customfields", url.Values{"entity": {"bill"}}, nil, nil, &out)
	return out.CustomFields["bill"], err
}

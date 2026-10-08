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
		res, err := c.http.Do(req)
		if err != nil {
			return fmt.Errorf("could not reach Zoho Books: %w", err)
		}
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
	ID       string `json:"tag_option_id"`
	Name     string `json:"tag_option_name"`
	IsActive *bool  `json:"is_active"`
	Status   string `json:"status"`
}

type ReportingTag struct {
	ID      string      `json:"tag_id"`
	Name    string      `json:"tag_name"`
	Status  string      `json:"status"`
	Options []TagOption `json:"tag_options"`
}

// ReportingTags lists reporting tags. GET /reportingtags returns them under "tags"; each tag's options come from GET /reportingtags/{id}.
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
					tags[i].Options = t.Options
				}
			}
		}
		if raw, ok := one["tag_options"]; ok && len(tags[i].Options) == 0 {
			_ = json.Unmarshal(raw, &tags[i].Options)
		}
	}
	return tags, nil
}

// AddTagOption creates a new option (for a property) under a reporting tag.
// Zoho replaces the option list on update, so the existing options are sent back with the new one.
func (c *Client) AddTagOption(ctx context.Context, tag ReportingTag, name string) error {
	opts := []map[string]any{}
	for _, o := range tag.Options {
		opts = append(opts, map[string]any{"tag_option_id": o.ID, "tag_option_name": o.Name})
	}
	opts = append(opts, map[string]any{"tag_option_name": name})
	return c.Do(ctx, http.MethodPut, "/reportingtags/"+url.PathEscape(tag.ID)+"/options", nil, map[string]any{"tag_options": opts}, nil, nil)
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
}

func (c *Client) Vendors(ctx context.Context) ([]Contact, error) {
	out := []Contact{}
	err := c.ListAll(ctx, "/contacts", "contacts", url.Values{"contact_type": {"vendor"}, "filter_by": {"Status.All"}}, func(raw json.RawMessage) error {
		var x Contact
		if err := json.Unmarshal(raw, &x); err != nil {
			return err
		}
		out = append(out, x)
		return nil
	})
	return out, err
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

// BillCustomFields lists custom fields on bills, to check the unique key field exists.
func (c *Client) BillCustomFields(ctx context.Context) ([]map[string]any, error) {
	var out struct {
		CustomFields map[string][]map[string]any `json:"customfields"`
	}
	err := c.Do(ctx, http.MethodGet, "/settings/customfields", url.Values{"entity": {"bill"}}, nil, nil, &out)
	return out.CustomFields["bill"], err
}

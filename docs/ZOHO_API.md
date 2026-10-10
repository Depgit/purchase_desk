# Zoho Books API calls used by Purchase Desk

Every Zoho Books call the application makes, why, where it lives in the code, and a `curl` you can run to test it.
All calls go to the India data centre and carry the organisation ID.

- API base: `https://www.zohoapis.in/books/v3`
- Sign-in base: `https://accounts.zoho.in`
- Every call adds `organization_id=<ZOHO_ORG_ID>` and the header `Authorization: Zoho-oauthtoken <access token>`.
- The client code is in `backend/internal/zohoapi/zohoapi.go`. The sync and mappings are in `backend/internal/zohosync`, pushing bills and reading payments in `backend/internal/zohopush`, and the warehouse journal in `backend/internal/warehousejournal`.

## Quick check (read-only)

```bash
./scripts/zoho-readonly-check.sh
```

The script reads `.env` and runs every read call below (1 to 12). It creates nothing in Zoho. On 8 October 2026 it passed all 10 calls that apply to the "Suraj Testing" organisation. Call 12 was skipped because that organisation has no reporting tag yet.

## Set up a shell for the curl commands

```bash
set -a; . ./.env; set +a
B="$ZOHO_API_URL/books/v3"; O="organization_id=$ZOHO_ORG_ID"
AT=$(curl -s -X POST "$ZOHO_ACCOUNTS_URL/oauth/v2/token" \
  -d "refresh_token=$ZOHO_REFRESH_TOKEN&client_id=$ZOHO_CLIENT_ID&client_secret=$ZOHO_CLIENT_SECRET&grant_type=refresh_token" \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["access_token"])')
H="Authorization: Zoho-oauthtoken $AT"
```

## Read calls (safe to run)

| # | Purpose | Call | Code |
|---|---|---|---|
| 1 | Get a one-hour access token from the refresh token | `POST accounts.zoho.in/oauth/v2/token` | `zohoapi.accessToken` |
| 2 | Test the connection; organisation name and plan | `GET /organizations` | `Organizations` |
| 3 | Chart of accounts, every page | `GET /chartofaccounts?filter_by=AccountType.All&page=N&per_page=200` | `Accounts` |
| 4 | Taxes and tax groups (CGST+SGST groups, IGST) | `GET /settings/taxes` | `Taxes` |
| 5 | Reporting tags | `GET /reportingtags` (list under key `tags`) | `ReportingTags` |
| 6 | Locations (optional branches with GSTIN) | `GET /locations` | `Locations` |
| 7 | Vendor contacts, active and inactive, every page | `GET /contacts?contact_type=vendor&page=N&per_page=200` | `Vendors` |
| 8 | Check the unique bill custom field exists | `GET /settings/customfields?entity=bill` | `BillCustomFields` |
| 9 | Bills changed since the last check, for payment read-back | `GET /bills?last_modified_time=2026-10-01T00:00:00+0530` | `ChangedBills` |
| 10 | One bill: status, balance | `GET /bills/{bill_id}` | `GetBill` |
| 11 | Payments on a bill: date and reference | `GET /bills/{bill_id}/payments` | `BillPayments` |
| 12 | Options of a reporting tag (one call per tag) | `GET /reportingtags/options?tag_id={tag_id}` (list under key `options`) | `ReportingTags` |
| 13b | Items, every page (active and inactive), for the Items tab | `GET /items?page=N&per_page=200` | `Items` |
| 13 | Look for an existing bill before creating one (used when the custom field is missing) | `GET /bills?vendor_id=&bill_number=` | `FindBill` |
| 13c | Download from Zoho (Excel), Settings → Zoho Books: every bill, every page | `GET /bills?page=N&per_page=200` | `zohoexport` (`ListAll`) |
| 13d | Download from Zoho (Excel): every journal, every page | `GET /journals?page=N&per_page=200` | `zohoexport` (`ListAll`) |

```bash
curl -s "$B/organizations?$O" -H "$H"
curl -s "$B/chartofaccounts?$O&filter_by=AccountType.All&per_page=200&page=1" -H "$H"
curl -s "$B/settings/taxes?$O" -H "$H"
curl -s "$B/items?$O&per_page=200&page=1" -H "$H"   # one page of 200; add &page=2, 3 ... for the rest
curl -s "$B/reportingtags?$O" -H "$H"
curl -s "$B/locations?$O" -H "$H"
curl -s "$B/contacts?$O&contact_type=vendor&per_page=200&page=1" -H "$H"   # one page of 200; add &page=2, 3 ... for the rest
curl -s "$B/settings/customfields?$O&entity=bill" -H "$H"
curl -s -G "$B/bills" --data-urlencode "$O" --data-urlencode "last_modified_time=2026-10-01T00:00:00+0530" -H "$H"
curl -s "$B/bills/<bill_id>?$O" -H "$H"
curl -s "$B/bills/<bill_id>/payments?$O" -H "$H"
curl -s "$B/reportingtags/options?$O&tag_id=<tag_id>" -H "$H"
curl -s -G "$B/bills" --data-urlencode "$O" --data-urlencode "vendor_id=<contact_id>" --data-urlencode "bill_number=SB/4471" -H "$H"
```

Things learned while testing:
- On the chart of accounts, `has_more_page` is not reliable: page 1 said `false`, but pages 2 and 3 had data. The app reads pages until one comes back empty.
- On contacts, do not add `filter_by=Status.All` to `contact_type=vendor`: Zoho then drops the vendor filter and returns customers too (10,382 contacts instead of 962 vendors on 8 October 2026). `contact_type=vendor` alone already includes inactive vendors.
- Every list returns at most 200 rows per call. A curl with `page=1` shows only the first 200; the app reads page after page until one comes back empty.
- Reporting tag options come from `GET /reportingtags/options?tag_id=`, under `options`, with fields `option_id`, `option_name`, `is_active` and nested `children`. `GET /reportingtags/{tag_id}` returns the tag's settings without options (seen 8 October 2026).
- The Saltstayz organisation has one tag per region (Delhi, Haryana, Noida, Punjab, Uttarakhand, Karnataka) with the hotels as options, 43 in all. A property maps to an option of any tag, and the bill carries that option's own tag.
- `GET /settings/tags` is retired ("This API is no longer available"). Reporting tags are at `GET /reportingtags`, under the key `tags`.
- Taxes come back as `tax_group` with `tax_specification: intra` (GST5, GST12, ...) and as `tax` with `tax_specific_type: igst` and `tax_specification: inter` (IGST5, ...). The Taxes tab maps them the same way.

## Write calls (run only in a test organisation)

Nothing below runs while `ZOHO_PUSH_ENABLED=false`, including the "Create in Zoho" buttons for an account, a vendor or a tag option. With it off, Purchase Desk only reads from Zoho.

| # | Purpose | Call | Code | Verified live |
|---|---|---|---|---|
| 14 | Save the bill, creating it or updating the one with the same key | `PUT /bills` with headers `X-Unique-Identifier-Key: cf_purchase_desk_id`, `X-Unique-Identifier-Value: <PR-1001/GRN-7001>`, `X-Upsert: true` | `UpsertBill` | Not yet |
| 15 | Create the bill (when the custom field is missing) | `POST /bills` | `CreateBill` | Not yet |
| 16 | Attach the invoice file | `POST /bills/{bill_id}/attachment` (multipart field `attachment`) | `AttachToBill` | Not yet |
| 17 | Mark the bill open (only if Zoho left it as draft) | `POST /bills/{bill_id}/status/open` | `MarkBillOpen` | Not yet |
| 18 | Create a vendor from verified details | `POST /contacts` | `CreateVendor` | Not yet |
| 19 | Create an expense account | `POST /chartofaccounts` | `CreateAccount` | Not yet |
| 20 | Add a property as a reporting tag option, under the tag chosen on the Connection tab | `PUT /reportingtags/{tag_id}/options` (body `options[]` with `option_id`, `option_name`, `is_active`, `children`; the full list is sent back) | `AddTagOption` | Fixed 10 Oct 2026 to the documented body; first live use pending |
| 20b | Activate a reporting tag (one created through the API starts as a draft) | `POST /reportingtags/{tag_id}/active` | `ActivateReportingTag` | First live use pending |
| 20c | Create a Purchase Desk item in Zoho (Items tab, Create in Zoho): name = brand + item (pack), sku = SKU code, item_type purchases, goods or service, purchase account from the sub-category mapping, GST via item_tax_preferences (intra and inter) from the tax mapping, rate from the current rate card | `POST /items` | `CreateItem` | First live use pending |
| 16b | Create a region reporting tag (named after the property's Zoho location) when Zoho has none, enabled on purchases and journals at line level; the property is then added as its option | `POST /reportingtags` | `CreateReportingTag` |
| 21 | Monthly warehouse journal per property | `POST /journals` | `CreateJournal` | Not yet |

Example bill as the app builds it. Open any invoice in Payments and click "Preview what is sent" to see the exact body.

```bash
curl -s -X PUT "$B/bills?$O" -H "$H" -H "Content-Type: application/json" \
  -H "X-Unique-Identifier-Key: cf_purchase_desk_id" -H "X-Unique-Identifier-Value: TEST-0001" -H "X-Upsert: true" \
  -d '{
    "vendor_id": "<contact_id>", "bill_number": "TEST-0001", "reference_number": "PO-2001",
    "date": "2026-10-08", "due_date": "2026-11-07", "payment_terms": 30, "payment_terms_label": "Net 30",
    "gst_treatment": "business_gst", "gst_no": "06AABCS4107K1Z1", "source_of_supply": "HR", "destination_of_supply": "HR",
    "is_inclusive_tax": false,
    "tags": [{"tag_id": "<tag_id>", "tag_option_id": "<option_id>"}],
    "line_items": [{"account_id": "<expense_account_id>", "name": "Aroma Leaf Soap bar 20 g", "description": "GA-0001 · box of 500 · PO-2001",
      "quantity": 3, "rate": 1450, "tax_id": "<GST18 group id>", "hsn_or_sac": "34011190",
      "tags": [{"tag_id": "<tag_id>", "tag_option_id": "<option_id>"}]}],
    "custom_fields": [{"api_name": "cf_purchase_desk_id", "value": "TEST-0001"}]
  }'

curl -s -X POST "$B/bills/<bill_id>/attachment?$O" -H "$H" -F "attachment=@invoice.pdf"
curl -s -X POST "$B/bills/<bill_id>/status/open?$O" -H "$H"

curl -s -X POST "$B/contacts?$O" -H "$H" -H "Content-Type: application/json" \
  -d '{"contact_name":"Test Vendor","company_name":"Test Vendor","contact_type":"vendor","gst_treatment":"business_none","place_of_contact":"HR","payment_terms":30}'

curl -s -X POST "$B/chartofaccounts?$O" -H "$H" -H "Content-Type: application/json" \
  -d '{"account_name":"Test Expense","account_type":"expense"}'

curl -s -X PUT "$B/reportingtags/<tag_id>/options?$O" -H "$H" -H "Content-Type: application/json" \
  -d '{"tag_options":[{"tag_option_id":"<existing option id>","tag_option_name":"<existing name>"},{"tag_option_name":"Sector 45, Gurugram"}]}'

curl -s -X POST "$B/journals?$O" -H "$H" -H "Content-Type: application/json" \
  -d '{"journal_date":"2026-09-30","reference_number":"WH-1-2026-09","notes":"Warehouse issues","line_items":[
    {"account_id":"<expense account>","debit_or_credit":"debit","amount":3510,"description":"Linen received from warehouse","tags":[{"tag_id":"<tag_id>","tag_option_id":"<option_id>"}]},
    {"account_id":"<stock loss account>","debit_or_credit":"debit","amount":702,"description":"Pilferage","tags":[{"tag_id":"<tag_id>","tag_option_id":"<option_id>"}]},
    {"account_id":"<warehouse stock account>","debit_or_credit":"credit","amount":4212,"description":"Stock issued","tags":[{"tag_id":"<tag_id>","tag_option_id":"<option_id>"}]}]}'
```

## Errors the app handles

| Zoho reply | Meaning | What the app does |
|---|---|---|
| HTTP 401 | Access token expired | Gets a new one from the refresh token and retries once |
| HTTP 429, code 44 | More than 100 calls in a minute | Waits (Retry-After, else 20 s) and retries up to 3 times |
| HTTP 429, code 45 | Daily call limit of the plan reached | Stops the batch; the rest stay queued; Finance is told |
| HTTP 429, code 1070 | Too many calls at once | Retries; the app sends one call at a time |
| `invalid_code` on token | Refresh token revoked | Marks Zoho disconnected; the Admin must enter a new token |
| Any other non-zero `code` | Zoho rejected a field | Marks the bill failed with Zoho's message; the push resumes from the failed step next time |

## Before switching pushing on

1. In Zoho Books, go to Settings › Preferences › Bills › Field Customization and create the custom field `cf_purchase_desk_id` (text) with "Do not allow duplicate values".
2. Create a reporting tag named "Property" with one option per property, or create the options from Purchase Desk.
3. In Purchase Desk, open Settings › Zoho Books and click Sync now. Then map properties, accounts (sub-categories, bill types, expense heads, warehouse, pre-opening and stock loss), taxes and vendors.
4. Test a push in a test organisation. Answer the two open questions from the handover: can two vendors use the same bill number, and can TDS be sent on a bill?
5. Set `ZOHO_PUSH_ENABLED=true` in `.env` and restart the API.

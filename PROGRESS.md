# Purchase Desk build progress (handoff)

Last updated: 2026-10-08. **The build is complete.** All five phases are built, wired and tested end to end. For working context read `CLAUDE.md`; for setup read `README.md`.

## How to run

```bash
cd /Users/batman/Documents/backend/Purchase_Desk
docker compose up -d --build        # db (host port 5544), api (8080), web (3000)
open http://localhost:3000
```

- System Admin: `admin@saltstayz.com` / `ChangeMe@123` (from `.env`). Demo users use the password `Demo@1234`; the list is in README and CLAUDE.md.
- Reset the database: `docker compose down -v && docker compose up -d --build`. The local DB was reset on 2026-10-08 and holds only the clean demo company.
- `ZOHO_PUSH_ENABLED=false`: sync, mappings and previews work against the Zoho test org. Pushing bills or journals is blocked on purpose.

## Folder rule (user request)

Every folder does one job and its name says which. Backend packages live in `backend/internal/<job>`; frontend screens live in `frontend/src/<job>`.

## Done and tested

- **Phase 1, core purchase cycle.** Sign-in with password or one-time code. Users, roles and rights with property and category scope enforced on the server. Masters, vendors with tax-ID checks and bank verification, vendor mapping and rate cards. Request form, approval routing, the approvals workbench, PO issue and email, goods receipt with part deliveries, three-way match, payment approval with the MSME limit, mark paid. Activity log, flow settings, templates, bulk uploads, file storage, the column chooser.
- **Phase 2, Zoho Books.** Encrypted connection with a test before saving, hourly sync, auto-link of vendors by GSTIN, the four mappings, held reasons, payload preview, the push in 3 steps with resume, payment read-back, void handling. Every read call passed against the test org.
- **Phase 3, bills, budgets and corporate.** Utility and AMC bills, budgets with week and day split and F&B sales, corporate purchases and bills with department L1 and L2.
- **Phase 4, warehouse and vendor sheets.** Stock ledger, transfers, delivery scheduling, dispatch with short-dispatch balance orders, receipt with pilferage, cannot-supply, the opening-stock upload. Vendor order sheets with preview, xlsx, email and WhatsApp send, and automatic sending.
- **Phase 5, reports and dashboards.** 14 reports with Excel and the pack, five dashboard views with "My view" customisation, the monthly warehouse journal for Zoho.
- **Docs.** `README.md`, `CLAUDE.md`, `docs/ZOHO_API.md`, `scripts/zoho-readonly-check.sh`, `.env.example`.

**Final checks on 2026-10-08:**
- `gofmt` is clean, and `go vet` and `go build` pass. `tsc -b` and `vite build` pass.
- The API end-to-end flows pass: the purchase cycle, bills, warehouse, corporate, vendor sheets, all 14 reports and the dashboards.
- A headless Chrome visit to all 33 screens as admin found no errors.

## Rollout phases (added 2026-10-08)

The client tests one phase at a time. `RELEASE_PHASE` in `.env` switches it, and the local `.env` is set to 1 for the first handover. Details are in README ("Rollout by phase") and CLAUDE.md ("Rollout phases").

- **Code:** the backend package `releasephase` holds the switch. Frontend folders: `releasephase` for the hook and the gate, `mywork` for the home page before phase 5.
- **Tested:**
  - On a fresh phase 1 database, the full cycle works: request, approval, PO, receipt, match, payment approval, mark paid, closed.
  - In phase 1, later-phase URLs return 404, and later-phase roles, vendors and properties are refused.
  - A headless visit to every screen found no errors, for the admin and all nine phase 1 roles.
  - Switching that same database to 3 and then 5 with `.env` alone added the sample data for those phases and kept the phase 1 data. Both original regression scripts then passed in phase 5.
- **MSG91 fix:** the reply body is now checked, because MSG91 can answer HTTP 200 and still refuse a message. `go test ./internal/messaging/` covers it.
- **Backup:** before the reset, the old database was saved to `backups/before-phase-switch-2026-10-08.dump`. The user's second System Admin, "Saltstayz" (admin@gmail.com), was restored with the same password.

## Still open (waiting on Saltstayz)

1. In Zoho, create the bill custom field `cf_purchase_desk_id` with unique values and a "Property" reporting tag. Then map everything, test pushes in a test org, and set `ZOHO_PUSH_ENABLED=true`. The write calls are not yet verified live.
2. Sandbox questions: can two vendors share a bill number, and can TDS go on a bill? Also, are Zoho locations on?
3. Credentials still needed for MSG91 (sender and approved `pd_*` templates), SMTP and Supabase. Hosting is not chosen.
4. The Zoho client secret and refresh token were shared during development. Rotate or delete them after the project.

## Zoho Books API calls the app uses (user asked for this list)

Base: `https://www.zohoapis.in/books/v3`, every call adds `organization_id=<org>`, header `Authorization: Zoho-oauthtoken <access>`.

| Purpose                             | Call                                                                                                                  | Where in code            |
| ----------------------------------- | --------------------------------------------------------------------------------------------------------------------- | ------------------------ |
| Access token from refresh token     | `POST https://accounts.zoho.in/oauth/v2/token` (refresh_token, client_id, client_secret, grant_type=refresh_token)  | zohoapi.accessToken      |
| Test connection, org name and plan  | `GET /organizations`                                                                                                | zohoapi.Organizations    |
| Chart of accounts                   | `GET /chartofaccounts?filter_by=AccountType.All&page=N&per_page=200`                                                | zohoapi.Accounts         |
| Create account                      | `POST /chartofaccounts`                                                                                             | zohoapi.CreateAccount    |
| Taxes and tax groups                | `GET /settings/taxes`                                                                                               | zohoapi.Taxes            |
| Reporting tags                      | `GET /reportingtags` (key `tags`), then `GET /reportingtags/{tag_id}` for options                               | zohoapi.ReportingTags    |
| Create tag option for a property    | `PUT /reportingtags/{tag_id}/options`                                                                               | zohoapi.AddTagOption     |
| Locations (optional)                | `GET /locations`                                                                                                    | zohoapi.Locations        |
| Vendor contacts                     | `GET /contacts?contact_type=vendor&filter_by=Status.All`                                                            | zohoapi.Vendors          |
| Create vendor                       | `POST /contacts`                                                                                                    | zohoapi.CreateVendor     |
| Check bill custom field exists      | `GET /settings/customfields?entity=bill`                                                                            | zohoapi.BillCustomFields |
| Save bill with unique key           | `PUT /bills` + headers `X-Unique-Identifier-Key`, `X-Unique-Identifier-Value`, `X-Upsert: true`               | zohoapi.UpsertBill       |
| Find bill (no custom field)         | `GET /bills?vendor_id=&bill_number=`                                                                                | zohoapi.FindBill         |
| Create bill (no custom field)       | `POST /bills`                                                                                                       | zohoapi.CreateBill       |
| Attach invoice                      | `POST /bills/{bill_id}/attachment` (multipart field `attachment`)                                                 | zohoapi.AttachToBill     |
| Read bill                           | `GET /bills/{bill_id}`                                                                                              | zohoapi.GetBill          |
| Mark open (only if draft)           | `POST /bills/{bill_id}/status/open`                                                                                 | zohoapi.MarkBillOpen     |
| Changed bills for payment read-back | `GET /bills?last_modified_time=2026-10-05T00:00:00+0530`                                                            | zohoapi.ChangedBills     |
| Payments on a bill                  | `GET /bills/{bill_id}/payments`                                                                                     | zohoapi.BillPayments     |
| Monthly warehouse journal           | `POST /journals` (journal_date, reference_number, notes, line_items with debit_or_credit, amount, account_id, tags) | zohoapi.CreateJournal    |

Verified live (read-only) in this session: token refresh, organizations, chartofaccounts (paging), settings/taxes, reportingtags
(empty in the test org), locations, contacts, settings/customfields (cf_purchase_desk_id missing), bills with last_modified_time.
Not yet verified live (would write to Zoho): POST/PUT bills, attachment, status/open, contacts create, chartofaccounts create,
reportingtags options, journals.

## Decisions taken during the build

- Money is float64 rounded to paise at boundaries; quantities NUMERIC(14,3).
- Budget spend counts incl. GST (as the prototype); the warehouse journal uses ex-GST values.
- Receiving more than the open PO quantity is blocked (the vendor takes back extra).
- A Cluster Manager whose role scope is "all" only acts on clusters they manage.
- Corporate requests reuse the `document` / `invoice` tables (kinds `corp_purchase`, `corp_bill`), so AP, Finance and Zoho push
  need no second path. The corporate office is a property with kind `corporate` and carries its own Zoho reporting tag.
- A short warehouse dispatch kept open becomes a new transfer order (`<number>-B1`, new WT number, `parent_id`).

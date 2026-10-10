# CLAUDE.md: Purchase Desk

Read this first. It is enough to work on the project without exploring the whole tree. `README.md` is the human guide (setup, settings, integrations). `PROGRESS.md` is the build log. `docs/ZOHO_API.md` lists every Zoho call.

## What this is

Purchase Desk is Saltstayz's procurement app: request → approval → PO → goods receipt → three-way match → payment approval → push to Zoho Books. It also covers utility and AMC bills, budgets, corporate purchases, the central warehouse, vendor order sheets, 14 reports and role dashboards. It was built from the handover pack: the prototype `purchase-desk-prototype-v26.html`, the Developer Specification and the Zoho Books Integration Module. Screens follow the prototype; rules follow the specification.

**Status (8 Oct 2026):** phases 1 to 5 are all built and tested end to end. The client tests them one at a time: `RELEASE_PHASE` in `.env` switches the app to a phase (see "Rollout phases" below). It is set to 1 for the first handover. Pushing to Zoho stays off (`ZOHO_PUSH_ENABLED=false`) until the Zoho org has the bill custom field `cf_purchase_desk_id` and a "Property" reporting tag. Read calls to Zoho are verified live; write calls are not yet.

**Stack:** Go 1.27 (chi v5, pgx v5 pool, golang-jwt v5, bcrypt, excelize v2), Postgres 16, React 19 + TypeScript + Vite + react-router 7 with plain CSS, nginx, Docker Compose. Files go to local disk or Supabase Storage. Git: branch `main`, first commit made on 8 Oct 2026; commit at the end of each delivered phase.

## Run, reset, verify

```bash
docker compose up -d --build                      # db :5544, api :8080, web :3000 (nginx proxies /api)
docker compose down -v && docker compose up -d --build   # wipe the DB and files, reseed the demo
docker compose logs -f api
cd backend && gofmt -l . && go vet ./... && go build ./...
cd frontend && npx tsc -b && npx vite build      # a >500 kB chunk warning is expected
curl -s localhost:8080/api/health
./scripts/zoho-readonly-check.sh                  # every read-only Zoho call, using .env
docker compose exec -T db psql -U purchasedesk -d purchasedesk -c "..."
```

- After a backend change, rebuild with `docker compose up -d --build api`. After a frontend change, use `--build web`.
- Dev without Docker: Vite on :5173 proxies `/api` to :8080. See README.
- The only automated test is `go test ./internal/messaging/` (the MSG91 reply check). Otherwise verify with the commands above and with API calls (sign in, then call with `Authorization: Bearer <token>`).
- To switch phase: edit `RELEASE_PHASE` in `.env`, then `docker compose up -d api`. The frontend reads the phase from `/me`, so it needs no rebuild.
- `backups/` holds `pg_dump` files (not in git). Restore with `docker compose exec -T db pg_restore -U purchasedesk -d purchasedesk --clean < backups/<file>.dump`.
- `.env` holds real secrets (chmod 600). Never print it or copy values into files. `.env.example` is the template.

**Logins.** Admin: `admin@saltstayz.com` / `ChangeMe@123`. Demo users (all `Demo@1234`, `<name>@demo.saltstayz.com`):

| User | Step | Scope |
|---|---|---|
| sunil | staff | P01 |
| ravi | l1 | P01 (also L1 of P02) |
| neha, arjun, sana | l2 | clusters Gurugram North, Gurugram South, Delhi South |
| amit | cc | all |
| pooja | purchase | all |
| deepak | store | P01, P02 |
| kavita | ap | all |
| rohit | finance | all |
| vikas | wh | all |
| shalini | preop | all |
| meera | creq | CO1 |
| rakesh, vivek | dh | CO1 (Admin and IT department L1) |
| anil | capp | CO1 (L2 of both departments) |

Demo properties: P01 to P05 live, WH Central Warehouse, X01 pre-opening, CO1 corporate office. There are 16 SKUs (LN-, EL-, GA-, ST-, DY-... codes), vendors V001 to V021, and the internal vendor `WH`.

## Folder rule (the user asked for this)

**Every folder does exactly one job, and its name says which.** A new feature gets a new folder named after what it does. Every Go package starts with a `// Package x ...` doc comment saying its job. Do not create `utils`, `common`, `helpers` or `misc`.

## Backend: `backend/internal/<package>` with its routes (all under `/api`)

Wiring lives in `router/router.go`. Startup, migrations and background jobs live in `cmd/server/main.go`. The jobs are zoho-sync (hourly), zoho-payments (every 2 h) and vendor-sheets (every minute, sends at the set time).

- **access**: the signed-in user, rights, steps, scope. Exports `From(r)`, `Guard.Middleware`, `Guard.Visible`, `Guard.SeesProperty`, `User.HasRight`, `User.HasStep`, `User.Can`, `NeedRight`, `NeedAdmin`.
- **activestatus**: mark masters active or inactive, with an impact preview. `POST /masters/{kind}/{id}/active`, `GET /masters/{kind}/{id}/impact`.
- **approvals**: routing (`Chain`, `CorpSteps`), the act on one or many documents, the workbench, `BillFlags`, `Enrich`, `Finalize`, and WhatsApp notify. `GET /approvals`, `POST /approvals/bulk`, `POST /documents/{id}/approve|return|reject|route`.
- **auditlog**: `Write(...)` for the activity log and `Doc(...)` for the document history. `GET /activity`.
- **billtypes**: `GET|POST /bill-types`, `PUT /bill-types/{id}`.
- **budgets**: month budget, week and day split, F&B sales, spend ledger, pace and flags. `GET /budgets/overview|property|export`, `PUT /budgets`, `GET /fb-sales`, `/fb-sales/export`.
- **bulkupload**: CSV check then import, with kinds registered through `Register(...)`. `GET /uploads/{kind}/sample`, `POST /uploads/{kind}/check|import`. Edit round trip (`roundtrip.go`, `workbook.go`): `GET /uploads/{kind}/workbook` gives an .xlsx with a Read me sheet, the page's list plus the masters it depends on as editable sheets (named after each kind's title, applied in order: Vendor Mapping = Vendors then Vendor mapping; Rates = Vendors, Items, Vendor rates), and read-only "Ref - ..." sheets. Check and import take `{text}` (CSV, one list) or `{xlsx}` (base64 workbook). A workbook check is a dry run in a rolled-back transaction, sheet by sheet with savepoints, so a later sheet sees earlier sheets' changes. `GET /uploads/{kind}/export` still gives one list as CSV. A row with its key updates, one without adds, an unchanged row is skipped (`keyOf`); users also use `onSame` and `after` so a deleted role row removes that role. Vendor bank details, GSTIN and PAN are locked on update. The workbook carries Excel rules (`sheetrules.go`): dropdowns that read from other sheets (a mapping's vendor_code from the Vendors sheet; sub-category and SKU lists filtered by the row's category through the hidden `Lists` sheet and OFFSET/MATCH/COUNTIF), stop messages naming the sheet to change first, and protected sheets where existing rows' keys and vendor bank/GST/PAN cells are locked (grey). Reference sheets are read-only. `spreadsheet.Sheet` has `Validations`, `LockedCols`, `Protect` and `Hidden`; `{col}` and `{Sheet.col}` placeholders resolve in rule formulas. A mapping's vendor, level and item may change in place. Updates go through `items.Update`, `geography.UpdateProperty`, `vendors.Update`, `vendormapping.CheckMapping` and `SaveMapping`.
- **clock**: IST helpers `Now`, `Today`, `ParseDate`, `ParseMonth`, `MonthStart`, `DaysInMonth`.
- **config**: env vars, see `.env.example`.
- **corporate**: departments, expense heads, corporate purchases and bills. `GET|POST /corporate/departments`, `PUT .../{id}`, `GET|POST /corporate/expense-heads`, `GET /corporate/form`, `POST /corporate/requests`.
- **csvfile**: reads and writes CSV.
- **dashboard**: tiles, sections and alerts per role view (ops, cc, fin, mgmt, my). `GET /dashboard`.
- **database**: `Connect`, `Migrate`, `Tx`, and the `Q` interface (pool or tx). Holds `migrations/001_core.sql`, `002_seed.sql`, `003_warehouse_corporate_sheets.sql`.
- **demodata**: the sample company when `SEED_DEMO=true`, split by phase (`core`, `billsBudgetsCorporate`, `warehouse`). The setting `demo_loaded_phase` records how far it loaded; raising `RELEASE_PHASE` adds the next sections on the next start. A database with properties but no such setting is treated as real data and left alone.
- **documents**: `Load(ctx,q,id,lock)`, `LoadMany`, `Doc.Owner()` (whose step is next), `CanAct(u,d,step,right)`, `StatusLabel`, `NextNumber(seq,prefix)`, `CloseIfPaid`, plus the status constants.
- **encryption**: AES-256-GCM `Box` for Zoho secrets and bank account numbers.
- **filestorage**: the `Store` interface, with local and Supabase drivers.
- **firstrun**: creates the first System Admin from env.
- **flowsettings**: `Load` gives a `Settings` struct (thresholds, toggles). Also pick lists, message templates, `CheckReason`, `Template`, `Render` (returns text and MSG91 params in placeholder order) and `Fill`. `GET|PUT /settings`, `GET /pick-lists`, `PUT /pick-lists/{name}`, `GET /templates`, `PUT /templates/{code}`.
- **geography**: cities, clusters, properties, F&B cost %. `GET|POST /cities|/clusters|/properties`, `PUT .../{id}`, `PUT /properties/{id}/pct`.
- **goodsreceipts**: receipts with part deliveries, invoice and photos, as multipart with a `data` JSON field. `POST /documents/{id}/receipts`.
- **httpjson**: `Wrap(handler returning error)`, `OK`, `JSON`, `Decode`, `ID(r,"id")`, `QInt`. Errors: `Bad`, `Invalid([]string)`, `Forbidden`, `NotFound`, `Conflict`, `Unauthorized`. Bad, Forbidden and Conflict take a format string.
- **indiantax**: GSTIN, PAN, IFSC, Udyam and HSN checks; GST state codes.
- **invoices**: three-way match, send back, payment approval (MSME 45-day limit), mark paid. `Held` and `ZohoConnected` are injected from zohopush and zohosync. `GET /invoices`, `GET /invoices/{id}`, `POST /invoices/{id}/match|send-back|approve-payment|mark-paid`, `POST /invoices/bulk` (action `match`, in-tolerance only, or `approve-payment`; one transaction each, results per invoice).
- **items**: categories, sub-categories, brands, pack sizes, SKUs. `GET|POST /categories|/sub-categories|/brands|/pack-sizes|/skus`, `PUT /sub-categories/{id}`, `PUT /skus/{id}`.
- **login**: password and one-time code sign-in (12 h session; throttled after 8 failures in 15 min), plus per-screen prefs. `POST /auth/login|/auth/otp/request|/auth/otp/verify`, `GET /me`, `POST /me/password`, `GET|PUT /prefs/{screen}`.
- **messaging**: SMTP email, MSG91 WhatsApp and SMS, all recorded in `message_log`. Without credentials it logs with status `logged`. `msg91Result` reads MSG91's reply body, because MSG91 can answer HTTP 200 with `{"type":"error"}` or `{"hasError":true}`.
- **money**: `Round2`, `LineValue(qty,rate,gst)`, `INR`, `TermsDays`, `PaymentTerms`.
- **passwords**: bcrypt and one-time code hashes.
- **purchaseorders**: `POST /documents/{id}/po` issues the PO and emails the vendor. `POST /purchase-orders/bulk` issues several at their current rates with one delivery date (terms "" = each vendor's own); a request with an estimated rate is refused.
- **purchaserequests**: raise (split per vendor), list, detail with allowed actions, resubmit, cancel, search, and `MyTurn`. `GET|POST /requests`, `GET /requests/form`, `GET /documents/{id}`, `POST /documents/{id}/resubmit|cancel|preop-approver`, `GET /search`. The detail returns `raised_with`: the other PRs split off the same submission (same requester, property and `created_at`).
- **releasephase**: the switched-on phase (`Set`, `Current`, `On(phase)`), the phase constants (`Zoho`, `Bills`, `Budgets`, `Corporate`, `Warehouse`, `Sheets`, `Dashboard`, `Reports`), and `RoleOn`, `VendorTypeOn`, `PropertyKindOn`, `NotYet`.
- **reports**: 14 reports and the Excel pack. `GET /reports`, `GET /reports/{key}` (`?format=xlsx`), `GET /reports/pack`.
- **router**: mounts everything, CORS for dev, `GET /health`.
- **scheduler**: background jobs.
- **sessiontoken**: JWT sign and verify.
- **spreadsheet**: writes .xlsx through excelize; `Send` writes it as a download.
- **textclean**: `Clean`, `Key` (duplicate-name key; SQL `norm_key()` is the same idea), `Upper`, `Contains`, `Uniq`, `Default`.
- **uploadedfiles**: `Save` and serve files. Bank proofs are visible only to Finance and Admin. `GET /files/{id}`.
- **users**: users, roles and grants (`ValidateUser`, `SaveUser`). `GET /people`, `GET|POST /users`, `PUT /users/{id}`, `GET|POST /roles`, `PUT /roles/{id}`.
- **utilitybills**: utility and AMC bills with no PO or receipt; blocks a duplicate period or invoice number. `GET /bills/form`, `POST /bills`.
- **vendormapping**: who supplies what where (SKU beats sub-category), rate cards (`CurrentRate`, `CheckRate`, `AddRate`), property check. `GET|POST /rates`, `GET /rates/format`, `GET|POST /mappings`, `PUT /mappings/{id}`, `PUT /mappings/{id}/estimate`, `GET /mappings/check|check-all`.
- **vendors**: onboarding (`Input`, `Validate`, `Insert`), bank verification, sending preferences. `GET|POST /vendors`, `GET|PUT /vendors/{id}`, `POST /vendors/{id}/verify`, `PUT /vendors/{id}/sending`.
- **vendorsheets**: `Build`, `Send`, `RunAuto`, with sends logged in `vendor_sheet_log`. `GET /vendor-sheets`, `GET /vendor-sheets/{id}`, `GET /vendor-sheets/{id}/xlsx`, `POST /vendor-sheets/{id}/send`.
- **warehouse**: stock ledger (`Ledger`), transfers, delivery scheduling, dispatch, cannot-supply, receive with pilferage, and the opening-stock upload (`UploadCheck`, `UploadCommit`). `GET /warehouse/stock|transfers|movements`, `PUT /warehouse/stock/{sku}`, `GET|POST /delivery-partners`, `PUT /delivery-partners/{id}`, `POST /warehouse/schedule`, `POST /documents/{id}/wait|dispatch|cannot-supply|receive-transfer`.
- **warehousejournal**: the monthly Zoho journal per property, at ex-GST value. `GET /zoho/journals`, `GET /zoho/journals/preview`, `POST /zoho/journals`.
- **worklist**: menu badge counts. `GET /worklist`.
- **zohoapi**: the Zoho HTTP client with token refresh, 429 handling and pagination. Calls are listed in `docs/ZOHO_API.md`.
- **zohoexport**: the "Download from Zoho (Excel)" button on Settings → Zoho Books only. On each click it reads the tab's list live from Zoho (every page) and writes every field Zoho returns; nothing is saved. `GET /zoho/export/{all|properties|accounts|taxes|vendors|items|bills|journals}` (Push queue tab = bills, Warehouse journal tab = journals).
- **zohopush**: builds the bill, pushes it in 3 steps, reads payments back, `Held` reasons. `GET /zoho/queue`, `GET /invoices/{id}/zoho-preview`, `POST /zoho/push`, `POST /zoho/read-payments`.
- **zohosync**: the connection (encrypted, tested before save), list sync (accounts, taxes, tags, locations, vendors, and items into `zoho_item`, read only, shown on the Items tab via `GET /zoho/items`), the four mappings, vendor link and create. `GET|PUT /zoho/connection`, `POST /zoho/test|connected|sync`, `GET /zoho/lists`, `GET|PUT /zoho/mappings` (PUT takes `key` or `keys[]`), `POST /zoho/properties/match`, `POST /zoho/properties/{id}/create-option`, `POST /zoho/accounts`, `POST /zoho/vendors/{id}/link|unlink|create`.

## Frontend: `frontend/src/<folder>`

Routes are in `app/App.tsx`. The left menu, top bar, badges and search are in `layout/Shell.tsx`. Styles are in `styles/app.css` and use the prototype's CSS variables, light and dark.

- **Shared:**
  - `api/client.ts` has `get`, `post`, `put`, `upload(path,data,files)`, `download`, `fileUrl` and `ApiError` (`.errors[]`).
  - `api/useApi.ts` is `useApi<T>(path)`, returning `{data,error,loading,reload,setData}`.
  - `api/types.ts` has Me (with `phase`), Doc, Line, Delivery, ListRow and Invoice. `api/lookups.ts` has `useLookups()` and `refreshLookups()`; it skips `/bill-types` before phase 3 (`setBillsOpen`, called by the session).
  - `releasephase/phase.tsx` has `PHASE`, `usePhase()` (returns `{current, on(phase)}`) and `<PhaseGate phase>`, which shows "Not open yet" for a closed screen.
  - `auth/` holds the sign-in page and `session.tsx` (the current user).
  - `format/format.ts` has `inr`, `qty`, `day`, `dateTime`, `month`, `today`, `thisMonth`, `addDays` and `plural`.
- **ui/:**
  - `DataTable` gives search, filters, a date range (`date={{label, value, onFrom?}}`; `onFrom` lets `/requests` lists widen `days` with `daysBack`), paging, the column chooser saved to `/prefs` and CSV export.
  - Widgets: `Modal`, `Field`, `Select`, `Checks`, `Seg`, `Pill` and `statusTone`, `Tabs`, `Errors`, `ActiveToggle`.
  - `Toast` provides `useToast`, and `useAction` provides `{busy,errors,run}`. `BulkResults` lists the per-item outcome of a bulk action.
- **Screens:**

| Folder | Routes |
|---|---|
| dashboard | `/` from phase 5 |
| mywork | `/` before phase 5: tiles of pending work from `/worklist` |
| approvals | `/approvals` |
| requests | `/requests`, `/requests/new` |
| documents | `/documents/:id`, the hub that shows the next action for any kind; it also holds BudgetBox |
| purchaseorders | `/purchase-orders` |
| goodsreceipts | `/goods-receipts` |
| invoices | `/invoices`, `/invoices/:id` |
| payments | `/payments` |
| bills | `/bills/:section`, `/bills/new/:section` |
| vendors | `/vendors`, `/vendors/new`, `/vendors/:id/edit` |
| items | `/items`, `/settings/item-setup` |
| vendormapping | `/vendor-mapping` |
| vendorsheets | `/vendor-sheets` |
| corporate | `/corporate`, `/corporate/new/:kind`, `/settings/corporate` |
| warehouse | `/warehouse`, with DispatchPanel and ReceiveTransferPanel |
| reports | `/reports` |
| properties | `/settings/properties`, `/settings/cities` |
| budgets | `/settings/budgets` |
| billtypes | `/settings/bill-types` |
| users | `/settings/users` |
| flowsettings | `/settings/flow`, `/settings/templates` |
| zohobooks | `/settings/zoho` |
| activitylog | `/settings/activity` |
| bulkupload | the upload dialog |

## Rollout phases

`RELEASE_PHASE` (1 to 5, default 5) switches on that phase and every phase before it. 1 is the core purchase cycle, 2 Zoho Books, 3 bills, budgets and corporate, 4 warehouse and vendor order sheets, 5 dashboards and reports.

**Where the phase is enforced (server):**
- `router.go` mounts later-phase services only when their phase is on. Their URLs return 404.
- `cmd/server/main.go` starts the Zoho jobs from phase 2 and the vendor-sheet job from phase 4.
- `bulkupload` drops the kinds budgets, fb_sales and property_pct before phase 3, and zoho_tags before phase 2. The warehouse_stock kind is registered in phase 4.
- Validation refuses later-phase records: `users` (roles wh, creq, dh, capp), `vendors` (types utility, amc, other) and `geography` (kinds warehouse, corporate). `vendormapping` refuses the warehouse vendor. Lists of roles and vendors hide them.
- `approvals.Enrich` and `/requests/form` skip budget flags before phase 3, but `Finalize` still writes `budget_spend`.
- `invoices`: before phase 2, `Held` and `ZohoConnected` are not wired, mark-paid accepts PAY_APPROVED, and payment approval says "Payment approved."
- `access.User.Phase` carries the phase to the frontend through `/me` and sign-in.

**Where it is shown (frontend):** the menu items in `Shell.tsx` carry a `phase`, `App.tsx` wraps later screens in `PhaseGate`, and these screens trim their phase 2 to 4 parts with `usePhase()`: Payments, Invoice, Document, Approvals, Vendors and the vendor form, Properties, Item Setup, Flow Settings and Message Templates.

**Adding a feature:** decide its phase. Mount its routes inside the matching `releasephase.On(...)` block, give its menu item a `phase`, and wrap its route in `PhaseGate`. Before phase 5, the home page is `mywork/MyWorkPage`.

## Domain model

**Rights and scope**
- Rights: raise, approve, issue_po, receive, match, pay, masters, upload.
- Each `user_role` row has a flow step and a scope: all, clusters or properties, optionally limited to categories.
- Steps: staff, l1, l2, cc, purchase, store, ap, finance, wh, preop, creq, dh, capp, admin.
- A user can hold several roles. Everything is enforced on the server; the UI only hides.

**Documents**
- One `document` table holds four kinds: `purchase`, `bill` (utility or AMC), `corp_purchase` and `corp_bill`. A purchase from vendor `WH` is a warehouse transfer (`is_transfer`).
- Lines are in `document_line`. Corporate lines have no SKU; they carry a description, unit and `expense_head_id`.
- Statuses: PENDING, RETURNED, REJECTED, CANCELLED, APPROVED, PO_ISSUED, RECEIVED, CLOSED. Transfers also use WH_SENT, WH_DONE and WH_BACK.
- `Doc.Owner()` says whose step is next, and `CanAct` checks the user may act on it.

**Approval routing**
- Purchases use `approvals.Chain`. A warehouse or pre-opening property goes to `preop`. A warehouse transfer needs no approval unless the setting is on. Otherwise L2 is added for events or above a threshold, and cc is added if no L2 step or if "cc on all" is set.
- Bills go to the Cluster Manager or the Cost Controller, per Flow Settings.
- Corporate uses `CorpSteps`: department L1 (`cl1`), then L2 (`cl2`). Below a threshold L1 alone is enough. If the requester is the L1, it goes straight to L2. Approvers are fixed when the request is raised.
- Final approval calls `Finalize`, which writes `budget_spend`.

**Invoices**
- One `invoice` row is one payable and becomes one Zoho bill.
- Statuses: AWAITING (bill waiting for approval), RECEIVED (after goods receipt), MATCHED, PAY_APPROVED, PUSHED, PAID, VOID.
- `CloseIfPaid` closes the document when all its invoices are paid.

**Numbering and history**
- Numbers come from `NextNumber(seq, prefix)`: PR-, PO-, GRN-, BR-, WT-, CR- (`cr_seq`) and CB- (`cb_seq`). A short dispatch kept open becomes `<PR>-B1` with a new WT number and `parent_id`.
- `activity_log` and `document_log` are immutable, enforced by database triggers. Write to them with `auditlog.Write` and `auditlog.Doc`.

**Budgets**
- Budgets are per property, sub-category and month, counted including GST.
- The day budget is the month budget divided by days in the month. Weeks are 7-day blocks from the 1st.
- F&B day budgets up to today come from sales × the property cost %, shared between F&B sub-categories in the ratio of their month budgets.
- Budget checks apply only to live properties (`Doc.NoBudget()`).

**Warehouse**
- Ledger: opening + purchased − issued − pilferage − in transit = closing.
- Dispatch requires a scheduled delivery and cannot exceed stock.
- A shortage at receipt is booked as pilferage. "Cannot supply" releases the budget.

**TDS (services)**
- Brand is required. Two fixed brands (`brand.kind`): "Other brand" (`other`) for unbranded goods and "Service" (`service`). They cannot be renamed or made inactive.
- A service item must carry a TDS section from `indiantax.TDSSections` (194C 1% or 2%, 194J 2% or 10%, 194I 2% or 10%, 194H 2%, or NONE). `sku.tds_section` and `sku.tds_pct` are copied onto each `document_line` when the request is raised.
- TDS on an invoice = sum of received qty × rate × tds_pct, on the value before GST, rounded to the rupee (section 288B). `Invoice.TDS`, `TDSSections` and `NetPayable` (amount − TDS) show on the invoice, in Payments and in the payables report.
- Zoho: the bill still goes at full value with the TDS written in its notes and a warning in the preview, until TDS on a bill (`tds_tax_id`) is tested in a test organisation. Yearly thresholds and lower-deduction certificates are not applied.

**Masters**
- Nothing is deleted. Masters carry `active` plus who, when and why they went inactive.
- Duplicate names are blocked with `norm_key()`, which compares ignoring case, spaces and punctuation.

**Tables, by migration**
- 001: app_user, role, user_role, otp_code, city, cluster, property, category, sub_category, brand, pack_size, sku, vendor, vendor_sub_category, bill_type, vendor_bill_type, rate_card, vendor_mapping, vendor_mapping_property, file_object, budget, fb_sale, document, document_line, document_log, budget_spend, goods_receipt, goods_receipt_line, invoice, setting, pick_list, message_template, user_pref, activity_log, message_log.
- 001 Zoho tables: zoho_connection, zoho_account, zoho_tax, zoho_tag, zoho_tag_option, zoho_location, zoho_contact, zoho_map_property, zoho_map_account, zoho_map_tax, zoho_map_vendor.
- 003: warehouse_stock, delivery_partner, delivery, expense_head, corp_department, vendor_sheet_log, zoho_journal.

## Zoho Books

- India data centre, `https://www.zohoapis.in/books/v3`. Every call carries `organization_id`.
- Push in 3 steps, resuming from `invoice.zoho_step`:
  1. `PUT /bills` with headers `X-Unique-Identifier-Key: cf_purchase_desk_id`, `X-Unique-Identifier-Value`, `X-Upsert: true`. If the custom field is missing, it falls back to `FindBill` then `POST /bills`.
  2. `POST /bills/{id}/attachment`.
  3. `POST /bills/{id}/status/open`.
- A bill voided in Zoho reopens the invoice, and `zoho_voids` changes the next unique key.
- A reporting tag set in Zoho for line items only (or the whole transaction only) refuses the tag at the other level ("do not have the access to be associated with a transaction or line item"). Zoho's tag list does not show the preference, so push sends the tag on bill and lines, and on that refusal retries with lines only, then bill only (`tagLevelRefused`, `tagsAt`). Tags created by "Create in Zoho" are line-level for purchases and journals.
- `zohopush.Held` lists the reasons an invoice can't push yet: a missing property tag, account, tax or vendor mapping. The UI shows them.
- Learned live:
  - `has_more_page` on the chart of accounts is wrong, so the client reads pages until one is empty.
  - `/settings/tags` is retired; use `/reportingtags` with the key `tags`. Options come from `GET /reportingtags/options?tag_id=` (key `options`, fields `option_id`/`option_name`, nested `children`); `GET /reportingtags/{id}` has no options any more. `TagOption` decodes both field shapes.
  - Properties are not under one "Property" tag: Saltstayz has one tag per region with hotels as options (43). A property maps to an option of any tag; push and the warehouse journal send that option's own `tag_id`. `zoho_connection.property_tag_id` now only says which tag a new option is created under.
  - `/zoho/lists` gives each location the `tag_id` of its region: the tag with the location's name, or its state's name (`indiantax.StateName`; Bangalore KA gives Karnataka, Head Office HR gives Haryana). On the Properties tab a chosen location narrows the option list to that tag; with no location, or no matching tag, every tag is offered.
  - "Create in Zoho" on the Items tab (`POST /zoho/items/{id}/create`, one or many ticked) creates the SKU in Zoho and links it; an active Zoho item with the same SKU code or name is linked instead. It needs the sub-category account and both GST taxes mapped first.
  - "Create in Zoho" on the Properties tab adds the property's name as an option under its location's region tag; if Zoho has no such tag it first creates one named after the location (`CreateReportingTag`), then links the option. Without a location it falls back to `property_tag_id`.
  - Contacts: send `contact_type=vendor` alone. Adding `filter_by=Status.All` makes Zoho return customers too (10,382 contacts instead of 962 vendors). `Vendors()` also skips any non-vendor contact.
  - Taxes come as `tax_group` (intra) and `tax` igst (inter).
  - Name matching, on every sync and from "Match by name": `autoLinkAccounts` fills unmapped sub-categories, bill types and expense heads with the one active expense account of the same name; `autoLinkItems` links unmapped SKUs to the one active Zoho item named like the item (or brand + item) via `zoho_map_item`. A name shared by two Zoho records is never guessed. Bill lines send `item_id` for a linked active item. "Create in Zoho" for an account selects an existing account of that name instead of creating it.
- The test org had no reporting tag at first; on 8 Oct 2026 it showed 6 reporting tags, 6 locations, 575 accounts, 124 taxes and 962 vendors. Check `cf_purchase_desk_id` before switching pushing on.
- MSG91 WhatsApp template names: `pd_ok`, `pd_mod`, `pd_ret`, `pd_rej`, `pd_dlv`, `pd_vsw`. Parameters follow the placeholder order of the app's message template.

## Code patterns

**Go handler**
```go
func (s *Service) thing(w http.ResponseWriter, r *http.Request) error {
    u := access.From(r)
    id, err := httpjson.ID(r, "id"); if err != nil { return err }
    var in struct{ Note string `json:"note"` }
    if err := httpjson.Decode(r, &in); err != nil { return err }
    errs := []string{}                       // collect every field error, plain-English, then:
    if len(errs) > 0 { return httpjson.Invalid(errs) }
    var msg string
    err = pgx.BeginFunc(r.Context(), s.DB, func(tx pgx.Tx) error {
        d, err := documents.Load(r.Context(), tx, id, true)   // lock the row
        if err != nil { return err }
        if documents.CanAct(u, d, d.Owner(), access.Receive) == nil { return httpjson.Forbidden("...") }
        // ... update, then auditlog.Doc(...) / auditlog.Write(...)
        msg = d.Number + " done"
        return nil
    })
    if err != nil { return err }
    return httpjson.OK(w, map[string]any{"message": msg})
}
```
- Register routes in the package's `Routes(r chi.Router)` with `httpjson.Wrap`. Mount new services in `router.go`.
- Uploads are multipart with a JSON `data` field plus files, saved with `uploadedfiles.Save`.
- Error and success messages are written for the end user ("Schedule the delivery first, so the property knows when to expect it."). The frontend shows `message` as a toast and `errors[]` inline.

**React screen**
- Load with `const { data, reload } = useApi<T>(path)`. Act with `const act = useAction()`, then `act.run(() => post(...), () => reload())`, and show `<Errors list={act.errors} />`.
- Lists use `DataTable`, and forms use `Field` and `Select`.

## Gotchas already hit (don't repeat them)

- Don't write `\u` escapes inside shell heredocs; it once produced a literal BOM. Write `"﻿"` through the Write or Edit tools.
- `go vet` flags a stray `%` in the format strings of `httpjson.Bad`, `Forbidden` and `Conflict`; escape it as `%%`. Plain strings appended to `errs` don't need escaping.
- An expression index in `ON CONFLICT` needs double parentheses: `ON CONFLICT ((norm_key(name)))`.
- pgx can't encode an int where SQL concatenates text (`number||'-B'||$2`). Pass `fmt.Sprint(n)`.
- `gofmt` aligns struct fields, so an exact-match edit of a struct can miss. Re-read before editing.
- Don't use `omitempty` on JSON fields the UI expects, even when the value is zero or empty.
- Convert `pgtype.Numeric` to float64 before returning JSON.
- Migrations run once each, recorded in `schema_migrations`. Add `004_*.sql` for schema changes; never edit 001 to 003 once deployed. Demo data changes go in `demodata.go` and need a DB reset.
- A property's role scope matters. A user can only act on properties inside their grant, so demo scopes were widened where flows cross properties (vikas is `all`).
- A phase 1 screen must never call a later-phase endpoint: the 404 shows as an error. The shared lookups once did this with `/bill-types`. Check with the headless sweep in phase 1.
- The `NewRequestPage` property list follows the user's roles (server: `purchaserequests.raiseGrant`). Live: staff, l1 and purchase. Pre-opening: wh, or staff, l1 and purchase assigned to it. Warehouse: wh only. Corporate is never listed. All of it is limited to the raise scope.
- The session re-reads `/me` and the shared lookups when the window regains focus (at most every 30 s) and on every sign-in, so role and master changes show without a reload.

## Open items (waiting on Saltstayz)

- Create `cf_purchase_desk_id` and the "Property" tag in Zoho. Map everything, test pushes in a test org, then set `ZOHO_PUSH_ENABLED=true`.
- Sandbox questions: can two vendors share a bill number, and can TDS go on a bill? For now bills go gross.
- Are Zoho locations on? MSG91 sender and template approval, SMTP sender, Supabase credentials and hosting are also pending.
- The Zoho credentials in `.env` are dummy and development-only. The user will delete or rotate them after the project.

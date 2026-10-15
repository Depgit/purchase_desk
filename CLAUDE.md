# CLAUDE.md: Purchase Desk

Read this first. It is enough to work on the project without exploring the whole tree. `README.md` is the human guide (setup, settings, integrations). `PROGRESS.md` is the build log. `docs/ZOHO_API.md` lists every Zoho call.

## What this is

Purchase Desk is Saltstayz's procurement app: request → approval → PO → goods receipt → three-way match → payment approval → push to Zoho Books. It also covers utility and AMC bills, budgets, corporate purchases, the central warehouse, vendor order sheets, 14 reports and role dashboards. It was built from the handover pack: the prototype `purchase-desk-prototype-v26.html`, the Developer Specification and the Zoho Books Integration Module. Screens follow the prototype; rules follow the specification.

**Status:** everything is built and tested end to end, and always on (the old RELEASE_PHASE rollout switch was removed on 15 Oct 2026). Pushing to Zoho stays off (`ZOHO_PUSH_ENABLED=false`) until the Zoho org has the bill custom field `cf_purchase_desk_id` and a "Property" reporting tag. Read calls to Zoho are verified live; write calls are not yet.

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
- The automated tests are `go test ./internal/messaging/` (the MSG91 reply check), `go test ./internal/photoshrink/` and `go test ./internal/documents/` (document number helpers). Otherwise verify with the commands above and with API calls (sign in, then call with `Authorization: Bearer <token>`).
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
| vikas | wh | all (may also act as Warehouse Approver L2) |
| manoj | whl1 | all (every sub-category) |
| seema | whl2 | all |
| shalini | preop | all |
| meera | creq | CO1 |
| rakesh, vivek | dh | CO1 (Admin and IT department L1) |
| anil | capp | CO1 (L2 of both departments) |

Demo properties (document codes SA, SB, DL, SK, HK, SX, WH, CO): P01 to P05 live, WH Central Warehouse, X01 pre-opening, CO1 corporate office. There are 16 SKUs (LN-, EL-, GA-, ST-, DY-... codes), vendors V001 to V021, and the internal vendor `WH`.

## Folder rule (the user asked for this)

**Every folder does exactly one job, and its name says which.** A new feature gets a new folder named after what it does. Every Go package starts with a `// Package x ...` doc comment saying its job. Do not create `utils`, `common`, `helpers` or `misc`.

## Backend: `backend/internal/<package>` with its routes (all under `/api`)

Wiring lives in `router/router.go`. Startup, migrations and background jobs live in `cmd/server/main.go`. The jobs are zoho-sync (hourly), zoho-payments (every 15 min; the only way an invoice becomes PAID: there is no manual mark-paid), zoho-send (every minute and at once when `zohowake.Now()` is called: masters, POs, bills, advances; see zohopush), warehouse-journals (hourly; posts the rest of each month once it is over) and vendor-sheets (every minute, sends at the set time).

**Test data set:** `go run ./cmd/testdata ../testdata` (in `backend/`) writes `testdata/00-test-plan.xlsx` (load order, manual setup, logins, scenarios, things that must be refused) and the upload files `01-…` to `14-…` for a fresh test database (SEED_DEMO=false): 10 properties, 26 users (password Test@1234), 48 items, 17 vendors, rates, mappings, budgets, F&B sales, warehouse stock. Codes the app assigns (P01, V001, LN-0001) are predictable on an empty database. Rerun before a test: sales dates and rate validity follow today.

- **access**: the signed-in user, rights, steps, scope. Exports `From(r)`, `Guard.Middleware`, `Guard.Visible`, `Guard.SeesProperty`, `User.HasRight`, `User.HasStep`, `User.Can`, `NeedRight`, `NeedAdmin`. Each grant carries its role's effective screen levels (`Grant.Screens`); `User.Level(screen, prop, cluster)` is the highest level among grants covering that property, `User.Overall(screen)` across all; `SeesReport`, `AddsUsers()` (role `can_users`), `CreatableRoles` (role `user_role_ids`), `UserScopeAny`, `ManagesRoles()` (role `can_roles`). `/me` sends `screens`, `reports` (null = all), `adds_users`, `user_roles`, `user_scope_any`, `manages_roles`.
- **activestatus**: mark masters active or inactive, with an impact preview. `POST /masters/{kind}/{id}/active`, `GET /masters/{kind}/{id}/impact`.
- **advances**: vendor advances on a PO with **Advance payment terms** (`money.Advance`; purchase or corporate, not transfers). The first advance is asked while issuing the PO (`purchaseorders.issue` takes `advance` and calls `advances.Create` in the same transaction), and **the PO is not raised until the advance is PAID** (changed 15 Oct: approval alone no longer releases it): the request stays APPROVED with `document.advance_hold_po` (migration 015) = the PO number it will get, owner Finance, label "PO waiting for advance approval", then "PO waiting for advance payment" once approved (`Doc.AdvanceReady`, JSON `advance_approved`); no goods receipt meanwhile. Marking it paid with the UTR (`paid`) or reading it paid from Zoho (`PaidInZoho`) runs `releaseHeld` → `documents.ReleaseHeldPO` (PO_ISSUED under that number, then `POReleased` = `purchaseorders.Release`; `SendPO` accepts a nil user for the Zoho read); rejecting or cancelling it (pending or approved-unpaid) runs `documents.ClearHeldPO` and Purchase issues the PO again. A further instalment on the PO page while it is PO_ISSUED. Invoice payment approval waits while any advance on its PO is pending or approved-but-unpaid (`advances.Waiting`, `Invoice.AdvanceWait`, 409 in `invoices.approveOne`); a goods receipt never cancels an unpaid advance (`CancelUnpaid` only when a balance reject cancels a PO with nothing delivered). A Net/15th-terms PO takes no advance (`requestable`). Asked by whoever can issue the PO (`CanAct(u,d,"purchase",IssuePO)`) as an amount or % (`Ask`, `Ask.Check`) with a reason (`advance_reasons`) and optional proforma (`ReadProforma`); approved, rejected and marked paid (UTR, date; vendor emailed with template `advpaid`) by the same Finance grant as invoice payments (`u.Can("finance",Pay,...)`); cancelled by the requester/Purchase. Number `documents.AdvanceNumber`: ADV_HZ_0001_01, _02 for PO_HZ_0001; ADV_HZ_0001_01_01 for PO_HZ_0001_01; ADV-2001_01 for older/corporate POs. Total advances ≤ PO value. TDS in proportion to the PO's service lines. At payment approval `invoices.approveOne` calls `advances.Apply` (picks from the body, or the `Plan` suggestion: this PO's paid advances, then the vendor's carried-forward ones, oldest first, capped at amount − TDS); `advance_use` rows (kind invoice/refund) hold amount and TDS share. Net payable = amount − TDS − (advance − its TDS). Left over: `refund` or `carry-forward` (only once the PO is no longer open). `CancelUnpaid` runs only when a balance reject cancels a PO with nothing delivered; `Release` when a bill is voided in Zoho. Zoho gets the bill at full value with the advance in its notes and a preview warning (applying a vendor advance in Zoho is untested). `GET /advances`, `GET /advances/{id}`, `GET|POST /documents/{id}/advances`, `GET /invoices/{id}/advances`, `POST /advances/{id}/approve|reject|cancel|paid|refund|carry-forward`. Advances waiting for Finance count in the payments badge.
- **approvals**: routing (`Chain`, `CorpSteps`), the act on one or many documents, the workbench, `BillFlags`, `Enrich`, `Finalize`, and WhatsApp notify. `GET /approvals`, `POST /approvals/bulk`, `POST /documents/{id}/approve|return|reject|route`.
- **auditlog**: `Write(...)` for the activity log and `Doc(...)` for the document history. `GET /activity`.
- **balancereject**: rejects the undelivered balance of a PO (per line) when the vendor will not deliver it. Purchase, the receiving store (or wh) or the Admin; never the requester alone (`Who`). The quantity comes off `document_line.qty` into `rejected_qty` (migration 009), its value is released from `budget_spend`, the vendor is emailed (`pobal`) and the requester gets WhatsApp (`bal`, MSG91 `pd_bal`). Nothing left due: RECEIVED + `po_closed_short` (then `CloseIfPaid`), or CANCELLED ("PO cancelled, nothing delivered") if nothing was received. `POST /documents/{id}/reject-balance` with `{quantities, reason, note}`; reasons from the `balance_reasons` pick list.
- **billtypes**: `GET|POST /bill-types`, `PUT /bill-types/{id}`.
- **budgets**: month budget, week and day split, F&B sales, spend ledger, pace and flags. `GET /budgets/overview|property|export`, `PUT /budgets`, `GET /fb-sales`, `/fb-sales/export`.
- **bulkupload**: CSV check then import, with kinds registered through `Register(...)`. `GET /uploads/{kind}/sample`, `POST /uploads/{kind}/check|import`. Edit round trip (`roundtrip.go`, `workbook.go`): `GET /uploads/{kind}/workbook` gives an .xlsx with a Read me sheet, the page's list plus the masters it depends on as editable sheets (named after each kind's title, applied in order: Rates = Vendors, Items, Vendor rates; Vendor Mapping = the mapping sheet plus read-only master sheets Ref - Vendors, Ref - Items, Ref - Vendor rates, Ref - Properties, Ref - Sub-categories, Ref - Clusters, which upload ignores), and read-only "Ref - ..." sheets. Check and import take `{text}` (CSV, one list) or `{xlsx}` (base64 workbook). A workbook check is a dry run in a rolled-back transaction, sheet by sheet with savepoints, so a later sheet sees earlier sheets' changes. `GET /uploads/{kind}/export` still gives one list as CSV. A row with its key updates, one without adds, an unchanged row is skipped (`keyOf`); users also use `onSame` and `after` so a deleted role row removes that role. Vendor bank details, GSTIN and PAN are locked on update. The workbook carries Excel rules (`sheetrules.go`): dropdowns that read from other sheets (a mapping's vendor_code from the Vendors sheet; sub-category and SKU lists filtered by the row's category through the hidden `Lists` sheet and OFFSET/MATCH/COUNTIF), stop messages naming the sheet to change first, and protected sheets where existing rows' keys and vendor bank/GST/PAN cells are locked (grey). Reference sheets are read-only. `spreadsheet.Sheet` has `Validations`, `LockedCols`, `Protect` and `Hidden`; `{col}` and `{Sheet.col}` placeholders resolve in rule formulas. A `spreadsheet.Formula` cell value is written as a formula (full recalculation on open). `sheetRules.formulas` (`cellFormula`, `{r}` = row) and `spare` rows fill cells from the row: the mapping sheet's vendor_name, item → category/sub_category (only on rows with an item), and item_available_at (the SKU's property scope) are VLOOKUPs into the hidden Lists sheet (columns I to N). Properties are one tick column per ordering property, headed "P01 · name" (`mappingProperties`, `withPropertyTicks`): on every row they show the saved mapping until the row's item changes (`{orig}` / `{orig:item}` in `cellFormula`), then tick themselves from item_available_at; category and sub_category likewise follow the item. The mapping download has no Read me sheet (`bundle.noReadme`) (code or cluster written into each formula, since excelize's calculator cannot nest a lookup in SEARCH), and are greyed by conditional format (`spreadsheet.Shade`) where the item is not available. Upload reads ticks with `tickedProperties`: all ticked = ALL; a CSV without tick columns still uses the `properties` cell. Its item column is a label "brand item (pack) · SKU code" (`itemLabelSQL`, read back by `skuOfItem`); level is derived (item given = sku); an item may only be mapped inside its scope (`outsideItem`, ALL allowed). `kind.computed` and `kind.derived` columns are ignored by the unchanged-row check. A mapping's vendor, level and item may change in place. Updates go through `items.Update`, `geography.UpdateProperty`, `vendors.Update`, `vendormapping.CheckMapping` and `SaveMapping`. Small masters (`smallmasters.go`): Cities & Clusters (bundle `clusters`: Cities sheet then Clusters, plus Ref - Cluster Managers) and Item Setup (bundle `sub_categories`: Categories then Sub-categories; bundle `pack_sizes`: new pack sizes only, an existing name in any spelling is refused, via `items.CheckNamed`/`AddNamed`). Cities, clusters and categories match by name (rename on the screen); sub-categories by their 2-letter code. They use `geography.ValidateCity/SaveCity`, `ValidateCluster/SaveCluster` and `items.ValidateCategory/SaveCategory`, `ValidateSubCategory/SaveSubCategory`, the same checks as the screens (a city's state code must be a real one, `indiantax.StateCodes`). An unchanged row still counts as seen (`onSame`), so a changed second copy of it is refused.
- **clock**: IST helpers `Now`, `Today`, `ParseDate`, `ParseMonth`, `MonthStart`, `DaysInMonth`.
- **config**: env vars, see `.env.example`.
- **corporate**: departments, expense heads, corporate purchases and bills. `ValidateDepartment`/`SaveDepartment` and `ValidateHead`/`SaveHead` serve the screen and bulk upload (`bulkupload/corporatesetup.go`: workbook `corp_departments` = Expense heads sheet then Departments, approvers by email, mobile or employee ID; `expense_heads` only adds, new heads pending Finance; `corp_lines` checks the lines of a new corporate purchase (sku_code, expense_head, quantity, optional rate; `?vendor_id=` for its rate card) and hands them to the form, like `request_lines`). `GET|POST /corporate/departments`, `PUT .../{id}`, `GET|POST /corporate/expense-heads`, `GET /corporate/form`, `POST /corporate/requests`.
- **csvfile**: reads and writes CSV.
- **dashboard**: tiles, sections and alerts per role view (ops, cc, fin, mgmt, my). `GET /dashboard`.
- **database**: `Connect`, `Migrate`, `Tx`, and the `Q` interface (pool or tx). Holds `migrations/001_core.sql`, `002_seed.sql`, `003_warehouse_corporate_sheets.sql`.
- **demodata**: the sample company when `SEED_DEMO=true`, in parts (`core`, `billsBudgetsCorporate`, `warehouse`). The setting `demo_loaded_phase` records how far it loaded (1, 3, 5) and missing parts load on the next start. A database with properties but no such setting is treated as real data and left alone.
- **documents**: `Load(ctx,q,id,lock)`, `LoadMany`, `Doc.Owner()` (whose step is next), `CanAct(u,d,step,right)`, `StatusLabel`, `NextNumber(seq,prefix)`, `RequestNumbers`, `NextSplits`, `RequestBase`, `Derived` (numbers.go, tested), `CloseIfPaid`, plus the status constants.
- **encryption**: AES-256-GCM `Box` for Zoho secrets and bank account numbers.
- **filestorage**: the `Store` interface, with local and Supabase drivers.
- **firstrun**: creates the first System Admin from env.
- **flowsettings**: `Load` gives a `Settings` struct (thresholds, toggles). Also pick lists, message templates, `CheckReason`, `Template`, `Render` (returns text and MSG91 params in placeholder order) and `Fill`. `GET|PUT /settings`, `GET /pick-lists`, `PUT /pick-lists/{name}`, `GET /templates`, `PUT /templates/{code}`.
- **formdrafts**: "Save draft" for forms (`form` request, grn with `ref_id` = the PO, bill, corporate), private to the person who saved it. `GET /drafts?form=&ref=`, `POST /drafts` (multipart: data `{id, form, ref_id, title, fields, keep}` plus files by form field; `keep` lists draft files that stay, so a re-save does not upload them again; replies with the stored `files`), `DELETE /drafts/{id}`. Fields are checked only when the form is submitted. Table `form_draft` (migration 014). Frontend `formdrafts/drafts.tsx`: `useDraft`, `DraftControls`, `KeptFiles`, `DraftList`; restored files are fetched back as File objects, so submitting needs no change on the server.
- **geography**: cities, clusters, properties (with `doc_code`, checked by `checkDocCode`: exactly 2 letters/digits, unique, required on create, blank on edit keeps it, fixed once used), F&B cost %. `GET|POST /cities|/clusters|/properties`, `PUT .../{id}`, `PUT /properties/{id}/pct`.
- **goodsreceipts**: receipts with part deliveries, invoice and photos, as multipart with a `data` JSON field. `POST /documents/{id}/receipts`. Who receives: `documents.Receiver` / `TransferReceiver` (anyone with the Receiving / Store role at the property, the Warehouse Manager at a warehouse or pre-opening property, and the requester on their own PO when their role has Goods Receipts = Edit there; raise roles get that by default). Whose step it is (`Doc.Owner`, badges, "next step"): the requester when Flow Settings `receipt_by_requester` (migration 021, default on; `Doc.ReceiptByRequester` is read in `selectDoc`), else "store". `balancereject.Who` never lets the requester reject alone. Less than due needs `balance` keep/close (no default); the reply's `closed` text drives the "PO closed" popup (`usePopup`). A vendor portal invoice with the same number is marked used. After an AP send-back the form refills from the voided GRN; otherwise from the newest unused portal invoice.
- **httpjson**: `Wrap(handler returning error)`, `OK`, `JSON`, `Decode`, `ID(r,"id")`, `QInt`, `QInts` / `QStrings` (a filter with several values, `property_id=1,2`; empty = all: reports' property/cluster/city, budgets' cluster, activity modules, rate format categories). Every filter dropdown in the app lets several values be ticked; form fields that take one value stay single. Errors: `Bad`, `Invalid([]string)`, `Forbidden`, `NotFound`, `Conflict`, `Unauthorized`. Bad, Forbidden and Conflict take a format string.
- **indiantax**: GSTIN, PAN, IFSC, Udyam and HSN checks; GST state codes.
- **invoices**: three-way match, send back, payment approval (MSME 45-day limit), no manual mark-paid: paid status comes only from Zoho via `zohopush.ReadPayments`. `Held` and `ZohoConnected` are injected from zohopush and zohosync. `GET /invoices`, `GET /invoices/{id}`, `POST /invoices/{id}/match|send-back|approve-payment|mark-paid`, `POST /invoices/bulk` (action `match`, in-tolerance only, or `approve-payment`; one transaction each, results per invoice).
- **items**: categories, sub-categories, brands, pack sizes, SKUs. `GET|POST /categories|/sub-categories|/brands|/pack-sizes|/skus`, `PUT /sub-categories/{id}`, `PUT /skus/{id}`. `sub_category.auto_po`: a request of only such items gets its PO at the final approval (`purchaseorders.Auto`, called from `approvals.Finalize`; vendor terms, mapped rate, deliver by today + Flow Settings `auto_po_days`; estimated rates and Advance terms still go to Purchase). `sku.daily_use` (Daily consumption): false lets several vendors hold the SKU at a property (`vendormapping.CheckMapping`) and the requester picks one per line (`lineIn.vendor_id`, `Source.Options`); back to true only with one SKU vendor.
- **login**: sign-in with email, mobile or employee ID (`findUser`, case-blind ID; if one person's mobile is another's employee ID, email wins, then mobile), by password or one-time code (sent by SMS when signed in by mobile or when the user has no email) (12 h session; throttled after 8 failures in 15 min), plus per-screen prefs. `POST /auth/login|/auth/otp/request|/auth/otp/verify`, `GET /me`, `POST /me/password`, `GET|PUT /prefs/{screen}`.
- **masterapproval**: Finance's approval of new masters, and of **edits** (`edits.go`, migration 028): editing an approved category, sub-category or item (screen or upload) puts it back to pending with `fin_change` (what changed, `Changed`) and `fin_before` (the values before, `Snapshot`/`Edited`); approving clears them (an item already in Zoho gets `zoho_update_due`), rejecting an edit restores the old values (`restore`). Editing a vendor (any field or what it supplies) sets it unverified with `vendor.fin_change`; Finance's verify clears it (and `zoho_update_due` sends it to Zoho). Vendors' own requests (`vendor_change`, `vendors.Propose`/`DecideChange`, kind `vendor_change` here) are approved or rejected in the same list. Then, as before, Finance's approval of new masters that go to Zoho (migration 023: `fin_status` pending/approved/rejected, `fin_by`, `fin_at`, `fin_note` on sku, category, sub_category, property, bill_type, expense_head; new rows start pending, older ones approved; demo masters are approved by the demo loader). Until approved: not orderable (`vendormapping` resolve gives "Waits for Finance approval"), no requests at a pending property (`purchaserequests.property`), no bills on a pending bill type or property, no corporate line on a pending head, no budgets (`budgets.PendingFinance`), and nothing sent to Zoho (`zohosync.SyncMasters` and the buttons check it). Set-up is allowed while pending (sub-categories, items, rates, mappings). Approve parents first (`decide`; bulk sorts category → sub-category → item). On approval `zohowake.Now()`: items and properties go to Zoho as before, and a sub-category, bill type or expense head gets its expense account (`accountFor`: the active account of exactly its name, else a new one). Reject = rejected + inactive with the reason (categories have no active flag). Vendors are listed too; their approval stays the bank verification. Finance (or Admin) only. `GET /master-approvals`, `POST /master-approvals/{kind}/{id}/approve|reject`, `POST /master-approvals/bulk`. Screen `master_approvals` (Edit for finance, View for masters), menu "Masters to approve" with a badge.
- **messaging**: SMTP email, MSG91 WhatsApp and SMS, all recorded in `message_log`. Without credentials it logs with status `logged`. `msg91Result` reads MSG91's reply body, because MSG91 can answer HTTP 200 with `{"type":"error"}` or `{"hasError":true}`.
- **money**: `Round2`, `LineValue(qty,rate,gst)`, `INR`, `TermsDays`, `PaymentTerms`.
- **passwords**: bcrypt and one-time code hashes.
- **photoshrink**: `Fit(data,name,type,max)` makes a JPEG or PNG fit a size limit (turned upright from EXIF, scaled down, saved as JPEG). Used before attaching an invoice to a Zoho bill (kept under 5 MB; Zoho's limit is about 10 MB). Tested in `go test ./internal/photoshrink/`.
- **purchaseorders**: `POST /documents/{id}/po` issues the PO and emails the vendor; JSON, or multipart (data + `proforma`) on Advance terms, where `advance` `{pct|amount, reason, note}` is required and saved with the PO. `POST /purchase-orders/bulk` issues several at their current rates with one delivery date (terms "" = each vendor's own); a request with an estimated rate, or on Advance terms, is refused.
- **purchaserequests**: raise (split per vendor), edit, list, detail with allowed actions, resubmit, cancel, search, and `MyTurn`. `prepare` checks a new or edited request the same way. `PUT /requests/{id}` edits everything (property, items, event) while `Editable`: only the requester themself, and only while PENDING at step 0 or RETURNED; the route restarts, the first-step approvers are emailed (`edit`), and items from other vendors become new PRs with the same `created_at`. Clone is frontend only (`/requests/new?clone=<id>`, own PRs). `GET|POST /requests`, `PUT /requests/{id}`, `GET /requests/form` (also `often`: SKU ids most ordered at the property in 45 days, and `last`: its latest submission, for Repeat last order), `GET /documents/{id}`, `POST /documents/{id}/resubmit|cancel|preop-approver`, `GET /search`. The detail returns `raised_with`: the other PRs split off the same submission (same requester, property and `created_at`).
- **reports**: 14 reports and the Excel pack (`List()` for the role editor; a role's `reports` limits the list, `one` returns 403 for others, the pack skips them). The purchase register is one row per vendor purchase line with PR No., PO No., GRN No., Invoice No., Voucher (PV) and invoice date (a line received over several deliveries lists each, in delivery order) and the received quantity; warehouse transfers are left out. `GET /reports`, `GET /reports/{key}` (`?format=xlsx`), `GET /reports/pack`.
- **router**: mounts everything, CORS for dev, `GET /health`.
- **screens**: the screen list (`All`: key, label, group, `ViewOnly` for flow, templates, activity, reports, where a role gets at most View), levels None/View/Edit, `Defaults(rights, step)` (the starting levels, matching the old menu and buttons: e.g. requests edit with raise, purchase_orders/payments view for all, approvals only for l2/cc/preop), `Effective(set, ...)` (set levels over defaults; can_users gives users=edit, can_roles users≥view).
- **screenguard**: middleware after `Guard.Middleware`, a rule table of method + path → screens, level and target. For document, invoice and advance routes it loads the record's property and checks `Level` there; list GETs such as `/requests?view=gr` map by query. Reference GET lists (SKUs, vendors...) stay open as lookups. Refusals: "Your role can only view X..., not change it." / "Your role has no access to X." Handlers check the same where the property is in the body (`purchaserequests.prepare`, `utilitybills`, `approvals.Act`, `purchaseorders.issueOne`, `invoices.lockInvoice`, `advances`), detail actions are filtered by `purchaserequests.onScreens`, the approvals workbench lists only documents the user may edit, and worklist badges drop where `Overall` < edit.
- **scheduler**: background jobs; a `Job.Wake` channel also runs one 2 s after a signal.
- **sessiontoken**: JWT sign and verify.
- **spreadsheet**: writes .xlsx through excelize; `Send` writes it as a download.
- **textclean**: `Clean`, `Key` (duplicate-name key; SQL `norm_key()` is the same idea), `Upper`, `Contains`, `Uniq`, `Default`.
- **uploadedfiles**: `Save` and serve files. Bank proofs are visible only to Finance and Admin. `GET /files/{id}`.
- **users**: users, roles and grants (`ValidateUser`, `SaveUser`). Each user has an Employee ID (`app_user.employee_id`, migration 025): required when a user is added, stored in capitals, unique ignoring case (refusal names who has it), blank on an edit keeps it; older users may have none. It is in the People list (column and search), the access grid search and the users bulk file (`employee_id` column). `GET /people`, `GET|POST /users`, `PUT /users/{id}`. Roles (`roles.go`): `GET /roles` (with effective `screens`, `reports`, `can_users`, `user_role_ids`, `user_scope`, `can_roles`, `group`), `GET /roles/catalogue` (every screen, report and right), `POST /roles`, `PUT /roles/{id}` (partial; screens stores only the levels someone set), `POST /roles/{id}/copy` (never copies can_roles), `GET|POST /role-groups`, `PUT /role-groups/{id}`. Who manages roles: the Admin, or a role the Admin gave `can_roles`; `withinOwn` stops them giving rights, levels, reports, create-users or any-property scope above their own, and only the Admin changes `can_roles`; the System Admin role cannot change. Adding users is on the role (`can_users`, `user_role_ids`, `user_scope` own/any): such a user sees and edits only users they added (`created_by`; `Manages`, also used by `activestatus`), gives only those roles and, unless scope is any, only property access inside their own (`delegateLimits`). Access grid (`accessgrid.go`): `GET /users/access-grid`, `PUT /users/{id}/property-access {property_id, role_id}` (0 = no access); only cells from named-property grants change there, never all-property or cluster grants, and a user can't be left with no role.
- **utilitybills**: utility and AMC bills with no PO or receipt; blocks a duplicate period or invoice number. `GET /bills/form`, `POST /bills`.
- **vendormapping**: who supplies what where (SKU beats sub-category), rate cards (`CurrentRate`, `CheckRate`, `AddRate`), property check. `GET|POST /rates`, `GET /rates/format`, `GET|POST /mappings`, `PUT /mappings/{id}`, `PUT /mappings/{id}/estimate`, `GET /mappings/check|check-all`.
- **vendorportal**: the vendors' own sign-in at `/vendor`. One sign-in (`vendor_account`: email, password, must_change; migration 026) can open several vendors: `vendor_login` links each goods vendor to one account (`active` per vendor). After signing in the person picks a vendor (`PickVendor`, the last one used is remembered) and switches from the header (`POST /vendor-portal/switch`); the token (kind "vendor", `sessiontoken.IssueVendor(account, vendor)`) carries both and is never accepted by the staff guard and vice versa. Giving an email that already signs in adds the vendor to it without changing its password; the same email again resets that account's password (for all its vendors); turning a vendor off leaves the email's other vendors. Vendor routes (outside the staff group): `POST /vendor-portal/login|switch|details`, `GET /vendor-portal/me|dashboard|details|orders|orders/{id}|payments|files/{id}`, `POST /vendor-portal/password`, `POST /vendor-portal/orders/{id}/accept|decline|dispatch|invoice`. `document.vendor_status` '' (label "Sent to vendor") → accepted ("Awaiting delivery[, dispatch <date>]") or declined ("Declined by vendor", counts in Purchase's PO badge); `dispatch_date`; `vendor_invoice` (uploaded invoices, `goods_receipt_id` once used). Staff: `GET|POST /vendors/{id}/portal` (give / reset: one-time password shown once and emailed, must change at first sign-in; also offered on the new-vendor form, `VendorForm` calls it right after the vendor is saved and shows the password in a popup), `POST /vendors/{id}/portal/off`, `POST /documents/{id}/vendor-confirm` (Purchase confirms for a vendor by phone, or after a decline with what was agreed). The PO email names the portal when the vendor has a sign-in. Vendor actions are logged with `auditlog.ByVendor` (role "Vendor").
- **vendors**: onboarding (`Input`, `Validate`, `Insert`), bank verification, sending preferences, and vendors' own change requests (`changes.go`: `Propose` checks them like a staff edit and keeps a new bank account only encrypted; `DecideChange` saves an approved one as an edit and marks the vendor verified). `GET|POST /vendors`, `GET|PUT /vendors/{id}`, `POST /vendors/{id}/verify`, `PUT /vendors/{id}/sending`.
- **vendorsheets**: `Build`, `Send`, `RunAuto`, with sends logged in `vendor_sheet_log`. `GET /vendor-sheets`, `GET /vendor-sheets/{id}`, `GET /vendor-sheets/{id}/xlsx`, `POST /vendor-sheets/{id}/send`.
- **warehouse**: stock ledger (`Ledger`), transfers, delivery scheduling, dispatch, cannot-supply, receive with pilferage, and the opening-stock upload (`UploadCheck`, `UploadCommit`). `GET /warehouse/stock|transfers|movements`, `PUT /warehouse/stock/{sku}`, `GET|POST /delivery-partners`, `PUT /delivery-partners/{id}`, `POST /warehouse/schedule`, `POST /documents/{id}/wait|dispatch|cannot-supply|receive-transfer`.
- **warehousejournal**: Zoho journals per property for warehouse issues, at ex-GST value. A journal covers a period (`zoho_journal.period_start/end`, migration 008): "Post now" posts what was received since the last journal up to that moment; after the month ends the hourly `warehouse-journals` job (`PostMonthEnds`) posts the rest of the month, dated its last day. `GET /zoho/journals`, `GET /zoho/journals/preview`, `POST /zoho/journals`.
- **worklist**: menu badge counts. `GET /worklist`.
- **zohowake**: `Now()` asks the zoho-send job to run at once (called by `QueueZohoPO`, `ReleaseHeldPO`, invoice payment approval, advance paid, vendor verify, SKU or property created, a Zoho mapping or sending setting changed).
- **zohoapi**: the Zoho HTTP client with token refresh, 429 handling and pagination. Calls are listed in `docs/ZOHO_API.md`.
- **zohoexport**: the "Download from Zoho (Excel)" button on Settings → Zoho Books only. On each click it reads the tab's list live from Zoho (every page) and writes every field Zoho returns; nothing is saved. `GET /zoho/export/{all|properties|accounts|taxes|vendors|items|bills|journals}` (Push queue tab = bills, Warehouse journal tab = journals).
- **zohopush**: builds the bill, pushes it in 3 steps, reads payments back, `Held` reasons. `GET /zoho/queue`, `GET /invoices/{id}/zoho-preview`, `POST /zoho/push`, `POST /zoho/read-payments`. Purchase orders (`purchaseorders.go`): every vendor PO (not transfers) is kept in step with a Zoho purchase order under the same number. `documents.QueueZohoPO` sets `document.zoho_po_sync` (create on issue or advance release, update on balance reject or close short, cancel when nothing delivered; migration 016); the `zoho-purchase-orders` job (`SyncPOs`, every minute) sends it with the same mappings and Held checks as bills, keeping `zoho_po_id` and each line's `zoho_po_line_id`. The `zoho-bills` job (`AutoPushBills`) pushes bills approved for payment without the button; a bill quotes `purchaseorder_ids` and `purchaseorder_item_id`; before saving it, `alignToPO` reads the Zoho PO and copies each PO line's item and account onto the bill line (Zoho links them only through common items, error 4197), and on a 4197 the bill goes unlinked (logged). Held = retried every run; a Zoho refusal waits 30 min (`zoho_po_at`, `invoice.zoho_tried_at`). Approved edits go to Zoho too (`ItemUpdateToZoho` PUT /items/{id}, `VendorUpdateToZoho` PUT /contacts/{id}: name, terms, GST, state), as kinds item_update and vendor_update in `SyncMasters`. `SendAll` (job zoho-send) runs `zohosync.SyncMasters`, `SyncPOs`, `AutoPushBills`, `SyncAdvances` in that order, and waits while `zohoapi.LowOnCalls()` (under a fifth of the daily allowance Zoho reports in its X-Rate-Limit headers; calls are spaced 750 ms). Advances (`advances.go`): **read back from Zoho** (`ReadAdvances`, button "Read advance payments from Zoho" = `POST /zoho/read-advance-payments` on Payments › Advances, and the 15-minute zoho-payments job): for each APPROVED advance of a linked vendor, the vendor's Zoho payments (`GET /vendorpayments`) are searched for the advance number as a whole word in Reference# or Notes (`carryingAll`); the one for the right amount (±₹1, not already linked) marks it PAID with Zoho's date and reference and links `zoho_payment_id` (`advances.PaidInZoho`, vendor emailed as usual; a PO held for that advance is raised now); each advance not found gets its reason in `advance.zoho_error` and in the button's `not_found` list (vendor not linked, no payment carrying the number, a right-amount payment without it, wrong amount; a vendor-payments list refused with filters is read again without them); a wrong amount is noted in `advance.zoho_error`. Mark paid in Purchase Desk still works: before creating a vendor payment for it, `SyncAdvances` links one Finance already recorded in Zoho with that number. Applying to bills keeps the Zoho payment's own account, date, amount and reference (`GET /vendorpayments/{id}`). A PAID advance becomes a Zoho vendor payment (`POST /vendorpayments`, from `zoho_connection.advance_account_id`, `advance.zoho_payment_id`), applied to its bill once that bill is open (`PUT /vendorpayments/{id}` with all its bills, `advance_use.zoho_applied`); untested live. Nothing is sent while `ZOHO_PUSH_ENABLED=false`. `GET /zoho/purchase-orders`, `POST /zoho/purchase-orders/{id}/sync` (Sync now: Finance, Purchase, Admin). PRs and goods receipts have no Zoho Books object.
- **zohosync**: the connection (encrypted, tested before save), list sync (accounts, taxes, tags, locations, vendors, and items into `zoho_item`, read only, shown on the Items tab via `GET /zoho/items`), the four mappings, vendor link and create. `GET|PUT /zoho/connection`, `POST /zoho/test|connected|sync`, `GET /zoho/lists`, `GET|PUT /zoho/mappings` (PUT takes `key` or `keys[]`), `POST /zoho/properties/match`, `POST /zoho/properties/{id}/create-option`, `POST /zoho/accounts`, `POST /zoho/vendors/{id}/link|unlink|create`. Masters (`masters.go`): `VendorToZoho` (link by GSTIN, or the one same-named unregistered contact, else create; needs bank verified), `ItemToZoho`, `PropertyToZoho` (link a same-named option, else add it under the location's region tag) serve both the buttons and `SyncMasters` (automatic when `zoho_connection.auto_masters`); a `Held` error (no Zoho call needed to re-check) is retried every run, others after 30 min; last problem per master in `zoho_master_sync`. `PUT /zoho/sending` (`auto_masters`, `advance_account_id`: a bank or cash account), `GET /zoho/masters/problems`, `GET /zoho/masters/{vendor|item|property}/{id}` (the Zoho card on those pages, `ZohoMasterCard`), `GET /zoho/usage`.

## Frontend: `frontend/src/<folder>`

Routes are in `app/App.tsx`. The left menu, top bar, badges and search are in `layout/Shell.tsx`. Styles are in `styles/app.css` and use the prototype's CSS variables, light and dark, in the olive palette (accent #556B2F; dark mode #A9C267).

- **Shared:**
  - `api/client.ts` has `get`, `post`, `put`, `upload(path,data,files)`, `download`, `fileUrl` and `ApiError` (`.errors[]`).
  - `api/useApi.ts` is `useApi<T>(path)`, returning `{data,error,loading,reload,setData}`.
  - `api/types.ts` has Me, Doc, Line, Delivery, ListRow and Invoice. `api/lookups.ts` has `useLookups()` and `refreshLookups()`.
  - `auth/` holds the sign-in page and `session.tsx` (the current user).
  - `format/format.ts` has `inr`, `qty`, `day`, `dateTime`, `month`, `today`, `thisMonth`, `addDays` and `plural`.
- **ui/:**
  - `DataTable` gives `defaultSort` (until the user clicks a heading; the SKU rates list sorts by SKU code), search, filters (every filter is a tick-box `MultiSelect` by default, rows pass if any ticked value passes; `single: true` keeps a plain dropdown), and a list whose rows have a boolean `active` gets a Status filter (Active / Inactive / both) by itself (`noStatus` leaves it out; pages with their own tables use `useActiveFilter` from `ui/ActiveFilter`), a date range (`date={{label, value, onFrom?}}`; `onFrom` lets `/requests` lists widen `days` with `daysBack`), paging, the column chooser saved to `/prefs` and CSV export.
  - Widgets: `Modal`, `Field`, `Select`, `Checks` (several choices: the tick-several `MultiSelect` dropdown with search over 8 options, Select all and Clear; give it a `placeholder` like "Choose properties"), `Seg`, `Pill` and `statusTone`, `Tabs`, `Errors`, `ActiveToggle`. Escape in a dropdown closes only the dropdown, never the Modal around it.
  - `Toast` provides `useToast`, and `useAction` provides `{busy,errors,run}`. `BulkResults` lists the per-item outcome of a bulk action.
- **Screens:**

| Folder | Routes |
|---|---|
| dashboard | `/` (the home page for everyone) |
| approvals | `/approvals` |
| requests | `/requests`, `/requests/new` (also `?clone=<id>`, `?draft=<id>`), `/requests/:id/edit`. The new request is a tile picker as in prototype v27: category tabs (Often ordered, All items, one per category), sub-category chips, tap to add, + / − on the tile and in the vendor-grouped cart (`.pos`, `.mgrid`, `.mt`, `.cline`), Repeat last order, Save draft |
| documents | `/documents/:id`, the hub that shows the next action for any kind; it also holds BudgetBox. Clone on a utility/AMC bill opens `/bills/new/:section?clone=<id>` |
| balancereject | RejectBalanceForm (inline, no modal): inside the store's ReceivePanel behind the "Vendor won't deliver the rest" switch, or BalanceDuePanel for Purchase/Admin who do not receive |
| purchaseorders | `/purchase-orders` |
| goodsreceipts | `/goods-receipts` |
| invoices | `/invoices`, `/invoices/:id` |
| payments | `/payments` (tabs: invoices and bills, advances) |
| advances | AdvanceFields (shared: % picks or amount, reason, note, proforma, preview; used in IssuePOPanel on Advance terms and for a further advance), AdvancesCard (document page; summary stats, one row per advance with a usage bar, the request form inline with quick % picks and a TDS/vendor-gets preview; hidden from users with neither pay nor issue_po and no step on an advance), AdvancesList (Payments tab), AdvanceActions |
| bills | `/bills/new/:section` (no menu item: raised from the top bar's + Bill; utility and AMC bills are listed on `/requests` with a Type filter, and `/bills/:section` redirects there; returned bills count in the requests badge) (`?clone=<id>`: same property, type, vendor, duration and remark, next period) |
| vendors | `/vendors`, `/vendors/new`, `/vendors/:id/edit` (with the Vendor portal and Zoho Books cards) |
| vendorportal | `/vendor/*` outside the staff session (both sign-in pages carry `auth/SignInDoor`: Saltstayz staff / Vendor; the accounts are separate, so one email can be a staff sign-in and a vendor sign-in with different passwords) (own token `pd-vendor-token`, `portalApi.ts`): sign-in, Dashboard (home `/vendor`, `VendorDashboard`: tiles for new orders, to deliver, bills being checked, approved to pay, paid this month, balance; new orders list; the statement from `GET /vendor-portal/dashboard` (`dashboard.go`): date, bill/payment, bill amount, payment amount, running balance over the whole account (bills add; TDS held, invoice payments = net payable, advances paid net of their TDS take; refunds add back), with search, type, property and period filters, opening/closing balance and CSV), Orders (`/vendor/orders`), Order, Payments, Password. Staff side: `PortalAccess` (vendor page), `VendorResponse` (PO page: vendor's answer, uploaded invoices, Confirm for the vendor) |
| items | `/items`, `/settings/item-setup` |
| vendormapping | `/vendor-mapping` |
| vendorsheets | `/vendor-sheets` |
| corporate | `/corporate`, `/corporate/new/:kind`, `/settings/corporate` |
| warehouse | `/warehouse`, with DispatchPanel and ReceiveTransferPanel |
| reports | `/reports` |
| properties | `/settings/properties`, `/settings/cities` |
| budgets | `/settings/budgets` |
| billtypes | `/settings/bill-types` |
| users | `/settings/users`: tabs People, Roles (RolesTab: groups, editor with rights, screen levels, reports, special rights, copy) and Access by property (AccessGrid) |
| screenaccess | `useScreen(key)` → `{level, view, edit}` and `<ScreenGate screen>`; every route in `App.tsx` is wrapped, the menu (`Shell.tsx`, `NavItem.screen`) follows `me.screens`, pages AND their change buttons with `useScreen(...).edit` |
| flowsettings | `/settings/flow`, `/settings/templates` |
| zohobooks | `/settings/zoho` |
| activitylog | `/settings/activity` |
| bulkupload | the upload dialog |
| appversion | `NewVersionBanner`: an open tab notices a newer deploy (on focus, at most once a minute) and offers Reload. nginx sends `no-cache` for the page and a year's cache for the hashed `/assets/` |
| masterapproval | `/settings/master-approvals`: Finance approves or rejects new masters (one or ticked many) |

## Adding a feature

A new screen needs an entry in `screens.All` (and `Defaults`), the `ScreenKey` type, a `ScreenGate` around its route, a `screen` on its menu item and `screenguard` rules for its writes.

## Domain model

**Rights and scope**
- Rights: raise, approve, issue_po, receive, match, pay, masters, catalog, upload. **catalog** (migration 028, Accounts Payable by default) creates and edits categories, sub-categories, brands, pack sizes, items and vendors (handlers, `activestatus.catalogKinds`, the items/vendors/item-setup uploads, and the screen defaults for items, item_setup and vendors); **masters** keeps properties, cities, vendor mapping, rates, bill types and corporate setup. Purchase can still give a vendor its portal sign-in (screenguard lets purchase_orders edit do that).
- Each `user_role` row has a flow step and a scope: all, clusters or properties, optionally limited to categories.
- Steps: staff, l1, l2, cc, purchase, store, ap, finance, wh, whs, whl1, whl2, preop, chef, hchef, creq, dh, capp, admin. Roles added in migration 022: **Property Chef** (`chef`, raise + receive at their property; the Users form pre-ticks the F&B categories, untick for all items), **Head Chef** (`hchef`, no rights: views everything at their properties, approves nothing), **Warehouse Staff** (`whs`, raise + receive at the warehouse). The Warehouse Manager got `approve`.
- A user can hold several roles. Everything is enforced on the server; the UI only hides.

**Documents**
- One `document` table holds four kinds: `purchase`, `bill` (utility or AMC), `corp_purchase` and `corp_bill`. A purchase from vendor `WH` is a warehouse transfer (`is_transfer`).
- Lines are in `document_line`. Corporate purchase lines use existing items only (since 15 Oct): `sku_id` from the items available at the corporate office (`corporate.officeItems`/`OfficeItem`: active, Finance-approved, Available at = all, the office's cluster or the office), plus `expense_head_id`; description and unit are the item's name and pack size; the rate comes from the vendor's current rate card (`vendormapping.CurrentRate`), else the typed rate with the item's GST. Older corporate lines have no SKU. Zoho books any line with an expense head to the head's account (`zohopush`, bill and PO lines), with or without a SKU.
- Statuses: PENDING, RETURNED, REJECTED, CANCELLED, APPROVED, PO_ISSUED, RECEIVED, CLOSED. Transfers also use WH_SENT, WH_DONE and WH_BACK.
- `Doc.Owner()` says whose step is next, and `CanAct` checks the user may act on it.

**Approval routing**
- Purchases use `approvals.Chain`. A warehouse property's own purchases go to **Warehouse Approver L1** (`whl1`) then **Warehouse Approver L2** (`whl2`; the Warehouse Manager `wh` may also act there, `documents.ApproverSteps`) (migration 027). L1 is chosen by sub-category: the role is given at the warehouse with sub-categories ticked (`user_role.sub_category_ids`, none = all; `access.User.CanSub`, `approvals.WarehouseL1s`). `purchaserequests.prepare` splits a request into one part per vendor and L1 group, refuses items whose sub-category has no L1 there, and starts at L2 when the requester is the only L1. Nobody approves their own warehouse request at either step (`documents.SelfBarred`). The L2 (or WM) sends the vendor order sheets. Warehouse Staff and the WM raise there (`raiseGrant`). A pre-opening property goes to `preop` (the WM or staff assigned there raise). A warehouse transfer needs no approval unless the setting is on. Otherwise L2 is added for events or above a threshold, and cc is added if no L2 step or if "cc on all" is set.
- Bills go to the Cluster Manager or the Cost Controller, per Flow Settings.
- Corporate uses `CorpSteps`: department L1 (`cl1`), then L2 (`cl2`). Below a threshold L1 alone is enough. If the requester is the L1, it goes straight to L2. Approvers are fixed when the request is raised.
- Final approval calls `Finalize`, which writes `budget_spend` and raises the PO (`purchaseorders.Auto`). **There is no Purchase Team step** (Flow Settings `po_on_approval`, migration 019, on by default): the last approver's approval issues the PO on the vendor's terms, deliver by today + `auto_po_days`; the Approve box (`raises_po` in the document detail, so only the last approver) asks the PO's **payment terms** (`AtApproval.Terms`, the vendor's usual by default; it applies to this PO only, the vendor master is unchanged), the confirmed rate of each estimated line (`Decision.rates`) and, when the terms are Advance, the advance (`Decision.advance`, then `advances.CreateAtApproval`; the PO is held until Finance approves and pays the advance). Bulk approval refuses a request that needs either. Setting off: only sub-categories with `auto_po` get their PO at approval, the rest wait for Purchase (`POST /documents/{id}/po` still works). `documents.POKeeper` (Purchase, or any approver on the route) confirms a PO for the vendor and sees declined POs in the badge; vendor order sheets are sent by l2, cc, preop or purchase. **Push to vendor:** `document.sent_to_vendor_at` (migration 020) is set when the PO reaches the vendor: by a sent vendor order sheet (`vendorsheets.Send` marks every PO in it, logged "Sent to vendor" with the sheet version) or by the PO email (`purchaseorders.SendPO`, used when Purchase issues by hand). A PO raised at approval or released by Finance's advance approval goes through `purchaseorders.Release`: a purchase PO is not emailed and shows "Push to vendor" (badge on Vendor Order Sheets for the senders) until its sheet goes; a corporate PO is emailed at once. The vendor portal shows only POs already sent.

**Invoices**
- One `invoice` row is one payable and becomes one Zoho bill.
- Statuses: AWAITING (bill waiting for approval), RECEIVED (after goods receipt), MATCHED, PAY_APPROVED, PUSHED, PAID, VOID.
- `CloseIfPaid` closes the document when all its invoices are paid.

**Numbering and history**
- Purchase numbers carry the property's 2-character document code (`property.doc_code`, asked when the property is created; existing properties start blank and cannot raise until the admin sets it). `documents.RequestNumbers` counts per property (`property.doc_seq`, row-locked by the UPDATE): PR_HZ_0001, or PR_HZ_0001_01, _02 ... when one submission splits per vendor. Once `doc_seq` > 0 the code is fixed.
- The PO, warehouse transfer and GRN copy the request number (`documents.Derived`): PO_HZ_0001_01, WT_HZ_0001, GRN_HZ_0001. Gaps are expected (a rejected PR has no PO). One PO stays open across part deliveries; each GRN then gets the delivery number: GRN_HZ_0001_01, _02 (a single delivery that leaves nothing due has no suffix). A short dispatch kept open becomes `<PR>_B1` with WT `<WT>_B1` and `parent_id`.
- Edit (`purchaserequests.editNumbers`): a move to another property takes a fresh number there; a split renames PR_HZ_0001 to _01 and adds _02 ... (a request already _01 keeps it, new ones follow the highest suffix); each rename is logged "Renumbered from ...". The base is therefore never both plain and suffixed, so GRN numbers cannot collide.
- Older numbers (PR-1001) and corporate, bill numbers keep `NextNumber(seq, prefix)`: PO-, GRN-, WT-, BR-, CR- (`cr_seq`), CB- (`cb_seq`).
- Every invoice row gets a purchase voucher number from one global series, `invoice.voucher_number` PV_0001 (column default `next_voucher()`, `pv_seq`), at goods receipt, utility/AMC bill or corporate bill entry. Search finds PR, PO, GRN, invoice and PV numbers (input is LIKE-escaped, since `_` is a wildcard).
- `activity_log` and `document_log` are immutable, enforced by database triggers. Write to them with `auditlog.Write` and `auditlog.Doc`.

**Budgets**
- Budgets are per property, sub-category and month, counted including GST.
- The day budget is the month budget divided by days in the month. Weeks are 7-day blocks from the 1st.
- F&B day budgets up to today come from sales × the property cost %, shared between F&B sub-categories in the ratio of their month budgets.
- Budget checks apply only to live properties (`Doc.NoBudget()`).

**Warehouse**
- Ledger: opening + purchased − issued − pilferage − in transit = closing.
- **Stocked at warehouse** (`sku.wh_stocked`, migration 024; Items form, bulk column `stocked_at_warehouse`): a property's request for such an item always resolves to the Central Warehouse (`vendormapping.fromWarehouse`, at the warehouse rate card, else the mapped vendor's rate, else an estimate), so approval makes it a transfer. Not at the warehouse itself or the corporate office. The stock ledger also gives `due` (approved transfers not yet sent), `on_order` (the warehouse's own open POs) and `short`; the Warehouse page lists short items and "Raise purchase for shortfall" opens `/requests/new?property=<WH>&lines=sku:qty,...`. The Warehouse Manager may raise at the warehouse (his own requests route to the Cost Controller, `approvals.Chain` requesterStep); Warehouse Staff requests go to him. What is in stock is sent now (part dispatch keeps the balance open as `<PR>_B1`).
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
- 009: `document_line.rejected_qty`, the `balance_reasons` pick list, templates `bal`, `pobal`, `edit`.
- 012: `advance`, `advance_use`, pick list `advance_reasons`, template `advpaid`.
- 011: `app_user.creatable_role_ids` (dropped again in 013).
- 013: `role_group` (Head office, Property, Warehouse, Corporate office); role `group_id`, `screens` JSONB (only levels someone set), `reports` TEXT[] (null = all), `can_users`, `user_role_ids`, `user_scope`, `can_roles`.
- 016: `document.zoho_po_id`, `zoho_po_sync`, `zoho_po_error`, `zoho_po_at`; `document_line.zoho_po_line_id`; `invoice.zoho_tried_at`.
- 017: `sub_category.auto_po`, setting `auto_po_days`; `sku.daily_use`; `vendor_login`; `document.vendor_status`, `vendor_acted_at`, `vendor_note`, `dispatch_date`; `vendor_invoice`; `zoho_connection.auto_masters`, `advance_account_id`; `zoho_master_sync`; `advance.zoho_payment_id`, `zoho_error`, `zoho_at`; `advance_use.zoho_applied`.
- 019: setting `po_on_approval` = true.
- 020: `document.sent_to_vendor_at` (existing POs counted as sent).
- 021: setting `receipt_by_requester` = true.
- 022: roles Property Chef, Head Chef, Warehouse Staff; `approve` for the Warehouse Manager.
- 024: `sku.wh_stocked`.
- 025: `app_user.employee_id` (unique, case-blind, when given).
- 026: `vendor_account` (one portal sign-in for several vendors); `vendor_login` keeps only the vendor → account link and `active`.
- 028: right `catalog` for Accounts Payable; `fin_change`/`fin_before` on category, sub_category, sku; `fin_change` on vendor; `zoho_update_due` on sku and vendor; `vendor_change`; `zoho_master_sync.kind` also item_update, vendor_update.
- 027: roles Warehouse Approver L1 (`whl1`) and L2 (`whl2`); `user_role.sub_category_ids` (the L1's sub-categories; users bulk column `sub_categories`, names or codes).
- 023: `fin_status` / `fin_by` / `fin_at` / `fin_note` on sku, category, sub_category, property, bill_type, expense_head (Finance approval); `zoho_master_sync.kind` also subcat, billtype, head.
- 018: repeats 017 with IF NOT EXISTS (a database had recorded 017 while it was still being written). Never append to a migration once its file exists; add the next number.
- 015: `document.advance_hold_po`.
- 014: `form_draft`.
- 010: `property.doc_code` (unique, case-blind) and `doc_seq`; `invoice.voucher_number` with `pv_seq` and `next_voucher()`; existing invoices numbered in entry order.

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
- MSG91 WhatsApp template names: `pd_ok`, `pd_mod`, `pd_ret`, `pd_rej`, `pd_dlv`, `pd_vsw`, `pd_bal`. Parameters follow the placeholder order of the app's message template.

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
- Migrations run once each, recorded in `schema_migrations`. Add the next numbered file (`009_*.sql`) for schema changes; never edit one that is deployed. Demo data changes go in `demodata.go` and need a DB reset.
- A property's role scope matters. A user can only act on properties inside their grant, so demo scopes were widened where flows cross properties (vikas is `all`).
- The `NewRequestPage` property list follows the user's roles (server: `purchaserequests.raiseGrant`). Live: staff, l1 and purchase. Pre-opening: wh, or staff, l1 and purchase assigned to it. Warehouse: wh only. Corporate is never listed. All of it is limited to the raise scope.
- The session re-reads `/me` and the shared lookups when the window regains focus (at most every 30 s) and on every sign-in, so role and master changes show without a reload.

## Open items (waiting on Saltstayz)

- Create `cf_purchase_desk_id` and the "Property" tag in Zoho. Map everything, test pushes in a test org, then set `ZOHO_PUSH_ENABLED=true`.
- Sandbox questions: can two vendors share a bill number, and can TDS go on a bill? For now bills go gross.
- Are Zoho locations on? MSG91 sender and template approval, SMTP sender, Supabase credentials and hosting are also pending.
- The Zoho credentials in `.env` are dummy and development-only. The user will delete or rotate them after the project.

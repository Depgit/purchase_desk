# Purchase Desk

Purchase Desk runs the whole purchase cycle for Saltstayz: request, approval, purchase order, goods receipt, three-way match, payment approval and the push to Zoho Books. It also handles utility and AMC bills, budgets, corporate purchases, the central warehouse, vendor order sheets, reports and dashboards.

It is built from the handover pack dated 8 October 2026: the clickable prototype (version 26), the Developer Specification and the Zoho Books Integration Module. Where the prototype and the specification differ, the screens follow the prototype and the rules follow the specification.

- Backend: Go 1.27, Postgres 16, chi router, pgx.
- Frontend: React 19, TypeScript, Vite; plain CSS that follows the prototype's look, with light and dark themes.
- Runs in Docker: `db` (Postgres), `api` (Go), `web` (nginx serving the React app and forwarding `/api`).
- Files: local disk, or Supabase Storage once its credentials are set.

## Quick start

```bash
cp .env.example .env          # then fill in the values (see "Settings" below)
docker compose up -d --build
open http://localhost:3000
```

On first start the API creates the database tables and the first System Admin from `ADMIN_EMAIL` and `ADMIN_PASSWORD`. With `SEED_DEMO=true` it also loads a sample company.

| Service  | Address                   | Notes                                                                |
| -------- | ------------------------- | -------------------------------------------------------------------- |
| Web app  | http://localhost:3000     | `WEB_HOST_PORT`                                                    |
| API      | http://localhost:8080/api | `API_HOST_PORT`; health check at `/api/health`                   |
| Postgres | localhost:5544            | `DB_HOST_PORT`; for inspection only, remove the port in production |

To start again from an empty database: `docker compose down -v && docker compose up -d --build`.

### Sample company (SEED_DEMO=true)

Every sample user signs in with the password `Demo@1234`. Emails end in `@demo.saltstayz.com`.

| User              | Role                    | Access                                      |
| ----------------- | ----------------------- | ------------------------------------------- |
| sunil             | Property Staff          | Sector 45 (P01)                             |
| ravi              | Property Manager (L1)   | P01                                         |
| neha, arjun, sana | Cluster Manager (L2)    | Gurugram North, Gurugram South, Delhi South |
| amit              | Cost Controller         | All properties                              |
| pooja             | Purchase Team           | All                                         |
| deepak            | Receiving / Store       | P01, P02                                    |
| kavita            | Accounts Payable        | All                                         |
| rohit             | Finance                 | All                                         |
| vikas             | Warehouse Manager       | All                                         |
| shalini           | Pre-opening Team        | All                                         |
| meera             | Corporate Requester     | Corporate Office                            |
| rakesh, vivek     | Department Head (L1)    | Admin and IT departments                    |
| anil              | Corporate Approver (L2) | Corporate Office                            |

The sample has 5 live properties, the Central Warehouse, a pre-opening property, a corporate office, 16 SKUs with rates, 13 vendors (goods, utility, AMC and corporate) plus the internal warehouse vendor, budgets and F&B sales for the current month, warehouse opening stock and two delivery partners.

## What is built

| Area                  | What it does                                                                                                                                                                                                                                                                                                                                                                                                                                                                                    |
| --------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Sign-in and rights    | Email or mobile, with a password or a one-time code (MSG91 SMS or email). Eight rights, standard and custom roles, several roles per user, each with its own property and category scope. The System Admin can let a user add users of chosen roles (for example Finance adds Property and Cluster Managers): they see and edit only the users they added, inside their own property access. Rights and scope are enforced on the server.                                                                                                                                                                                                                                                          |
| Masters               | Cities, clusters, properties (2-letter document code, eZee code, F&B cost %), categories, sub-categories, brands, pack sizes, SKUs (generated codes, HSN), vendors (GSTIN, PAN, IFSC, Udyam checks, bank proof, Finance verification), bill types. Nothing is deleted; records are marked inactive, with an impact message first.                                                                                                                                                                                       |
| Vendor mapping        | One vendor per item per property. A SKU mapping wins over a sub-category mapping. Rate cards with validity and history, a property check screen, rate format download and upload.                                                                                                                                                                                                                                                                                                               |
| Purchase flow         | Request form (vendor and rate from the mapping, split per vendor, event tag, budget position) and approval routing (Cost Controller; Cluster Manager for events or above an amount; send to the Cluster Manager; Pre-opening Team). Approvals workbench with grouping and bulk actions, quantity changes shown to the requester, PO issue with email to the vendor, goods receipt on a phone with part deliveries, three-way match with tolerance, payment approval with the MSME 45-day limit. |
| Document numbers      | Each property has a 2-letter document code (HZ). Requests count per property: PR_HZ_0001, or PR_HZ_0001_01, _02 when one request splits per vendor. The PO, transfer and GRN copy it (PO_HZ_0001, WT_HZ_0001, GRN_HZ_0001; GRN_HZ_0001_01, _02 for part deliveries). Every bill entered gets a purchase voucher number from one series: PV_0001. The code is fixed once used; a property without one cannot raise requests. |
| Vendor advances       | After a PO is issued, Purchase can ask for an advance (amount or %, reason, proforma). Finance approves and marks it paid (the vendor is emailed). It is adjusted on the vendor's invoices at payment approval (net payable drops), and anything left is refunded by the vendor or carried forward to their next invoices. Numbered from the PO: ADV_HZ_0001_01. |
| TDS on services | Items with the brand "Service" carry a TDS section and rate (194C, 194J, 194I, 194H, or No TDS). TDS is worked out on the value before GST, rounded to the rupee, and shown with the net payable on invoices, in Payments and in the payables report. Zoho bills go at full value with the TDS in the notes until TDS on a bill is tested. |
| Utility and AMC bills | No PO or receipt. Duplicate period and invoice checks, a flag when a bill jumps above the previous one, a warning when an AMC is near its end.                                                                                                                                                                                                                                                                                                                                                  |
| Budgets               | One amount per property, sub-category and month, split into 7-day weeks and days. F&B day budgets come from uploaded sales and each property's cost %. Pace, week and month flags, history.                                                                                                                                                                                                                                                                                                     |
| Corporate             | Purchases with free-text lines and bills, each line under an expense head. Department L1 then L2 with a backup approver; approvers are fixed when a request is raised. Then the same PO, receipt, match, payment and Zoho flow.                                                                                                                                                                                                                                                                 |
| Warehouse             | Stock ledger (opening + purchased − issued − pilferage − in transit). Transfer orders from approved requests, delivery scheduling with partners and slots, dispatch limited to stock, short dispatch kept open or closed, receipt at the property with pilferage, "cannot supply" with the budget released.                                                                                                                                                                                  |
| Zoho Books            | Encrypted connection settings with a test before saving. Hourly sync of accounts, taxes, reporting tags, locations and vendors. The four mappings, with held reasons. Push with a unique key (or find then create), attachment, open status, and resume from the failed step. Payment read-back every two hours, void handling, and a monthly warehouse journal.                                                                                                                                |
| Vendor order sheets   | One Excel per vendor: summary, property status, SKU totals, property × SKU (confirmed and approved), and one tab per property. Sent by email or WhatsApp, by hand or automatically at a set time. Every send is logged.                                                                                                                                                                                                                                                                        |
| Reports               | 14 reports with date range, basis, property, city and cluster filters. Excel per report and one Excel pack.                                                                                                                                                                                                                                                                                                                                                                                     |
| Dashboards            | Property operations, Cost control, Finance, Management and My view (up to 6 tiles and any sections, saved per user), plus "Needs attention" alerts.                                                                                                                                                                                                                                                                                                                                             |
| Everywhere            | Activity log (cannot be edited), CSV export of every list, column chooser saved per user, global search, bulk uploads with row-by-row checks and only clean rows imported.                                                                                                                                                                                                                                                                                                                      |

## Folder map

Every folder does one job, and its name says which.

```
Purchase_Desk/
├── docker-compose.yml        the three services
├── .env.example              every setting, with placeholders
├── CLAUDE.md                 working context for Claude Code sessions
├── PROGRESS.md               build notes and decisions
├── backups/                  database backups (not in git)
├── docs/ZOHO_API.md          every Zoho call with a curl to test it
├── scripts/zoho-readonly-check.sh   runs every read-only Zoho call with your .env
├── backend/
│   ├── cmd/server            starts the API, the migrations and the background jobs
│   └── internal/
│       ├── access            who is signed in, their roles, rights and property scope
│       ├── activestatus      mark master records active or inactive, and say what that affects
│       ├── approvals         approval routes, approve / return / reject / send to Cluster Manager, the workbench
│       ├── auditlog          the activity log and each document's history
│       ├── billtypes         utility and AMC bill types
│       ├── budgets           month budgets, week and day split, F&B sales, spend, pace and flags
│       ├── bulkupload        CSV uploads with row-by-row checks
│       ├── clock             today, this month, in India Standard Time
│       ├── config            settings from environment variables
│       ├── corporate         corporate departments, expense heads, corporate purchases and bills
│       ├── csvfile           read and write CSV
│       ├── dashboard         tiles, sections and alerts for each person's dashboard
│       ├── database          Postgres connection and the SQL migrations (migrations/)
│       ├── demodata          the sample company
│       ├── documents         loads a request or bill, says who owns the next step, numbers documents
│       ├── encryption        AES-GCM for Zoho secrets and bank account numbers
│       ├── filestorage       local disk or Supabase Storage
│       ├── firstrun          creates the first System Admin
│       ├── flowsettings      flow settings, reason lists, message templates
│       ├── geography         cities, clusters, properties
│       ├── goodsreceipts     goods receipts on a phone, part deliveries
│       ├── httpjson          JSON in and out, user-facing errors
│       ├── indiantax         GSTIN, PAN, IFSC, Udyam, HSN checks and GST state codes
│       ├── invoices          three-way match, payment approval, mark paid
│       ├── items             categories, sub-categories, brands, pack sizes, SKUs
│       ├── login             sign-in, one-time codes, screen preferences
│       ├── messaging         email (SMTP), WhatsApp and SMS (MSG91), message log
│       ├── money             rupee rounding, GST line values, rupee formatting
│       ├── passwords         password and one-time code hashing
│       ├── purchaseorders    issue POs and email them
│       ├── purchaserequests  raise requests, lists, detail, resubmit, cancel, search
│       ├── reports           the 14 reports and the Excel pack
│       ├── router            mounts every service under /api
│       ├── scheduler         background jobs
│       ├── sessiontoken      signed session tokens
│       ├── spreadsheet       writes .xlsx files
│       ├── textclean         tidy text and duplicate-name keys
│       ├── uploadedfiles     saves and serves invoices, photos, bank proofs
│       ├── users             users and roles
│       ├── utilitybills      raise utility and AMC bills
│       ├── vendormapping     who supplies what where, rate cards
│       ├── vendors           vendor onboarding and bank verification
│       ├── vendorsheets      vendor order sheets and their sending
│       ├── warehouse         stock ledger, transfers, delivery scheduling, dispatch, pilferage
│       ├── warehousejournal  monthly Zoho journal for warehouse issues
│       ├── worklist          counts for the menu badges
│       ├── zohoapi           the Zoho Books API client
│       ├── zohopush          push bills to Zoho, read payments back
│       └── zohosync          Zoho connection, list sync, the four mappings, vendor links
└── frontend/src/
    ├── activitylog   the activity log screen
    ├── api           talks to the API; shared types and lookup lists
    ├── app           the list of screens
    ├── approvals     approvals workbench and the reason dialog
    ├── auth          sign-in screen and the signed-in user
    ├── bills         utility and AMC bill lists and form
    ├── billtypes     bill types screen
    ├── budgets       budgets, week and day views, F&B sales
    ├── bulkupload    the upload dialog
    ├── corporate     corporate list, new request, corporate setup
    ├── dashboard     the role dashboards
    ├── documents     one request or bill and its next action
    ├── flowsettings  flow settings and message templates
    ├── format        rupees, dates, quantities
    ├── goodsreceipts receipt list and the phone receipt panel
    ├── invoices      invoice list and the match screen
    ├── items         SKUs and item setup
    ├── layout        left menu and top bar
    ├── payments      payments, push to Zoho, preview
    ├── properties    properties, cities and clusters
    ├── purchaseorders PO list and the issue PO panel
    ├── reports       reports screen
    ├── requests      request list and form
    ├── styles        the stylesheet
    ├── ui            shared widgets: table, dialog, fields, pills, tabs, messages
    ├── users         users and rights
    ├── vendormapping vendor mapping, rates, property check
    ├── vendors       vendor list and onboarding form
    ├── vendorsheets  vendor order sheets
    ├── warehouse     warehouse screen, dispatch and receive-transfer panels
    └── zohobooks     Zoho Books settings, mappings, push queue, journals
```

## Settings (.env)

| Variable                                                                            | What it is                                                                                                                                      |
| ----------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------- |
| `APP_ENV`                                                                         | `development` shows one-time codes on screen when no SMS or email is set up, and allows the Vite dev server. Use `production` on servers.   |
| `APP_BASE_URL`                                                                    | Address of the web app, for CORS in development.                                                                                                |
| `POSTGRES_USER`, `POSTGRES_PASSWORD`, `POSTGRES_DB`, `DATABASE_URL`         | Database. Docker Compose builds the API's`DATABASE_URL` from the first three.                                                                 |
| `JWT_SECRET`                                                                      | At least 32 random characters. Signs sessions (12 hours).                                                                                       |
| `ENCRYPTION_KEY`                                                                  | Long random key. Encrypts Zoho secrets and bank account numbers.**If lost, those values cannot be read**: keep it in a password manager.  |
| `ADMIN_EMAIL`, `ADMIN_PASSWORD`, `ADMIN_NAME`                                 | The first System Admin, created only when the database has no users.                                                                            |
| `SEED_DEMO`                                                                       | `true` loads the sample company into an empty database. Set `false` for real data.                                                          |
| `STORAGE_DRIVER`                                                                  | `local` (Docker volume `files`) or `supabase`.                                                                                            |
| `SUPABASE_URL`, `SUPABASE_SERVICE_KEY`, `SUPABASE_BUCKET`                     | Supabase Storage. Create a**private** bucket; the API uses the service key, and files are served only through the API with rights checks. |
| `ZOHO_ACCOUNTS_URL`, `ZOHO_API_URL`                                             | India data centre by default.                                                                                                                   |
| `ZOHO_ORG_ID`, `ZOHO_CLIENT_ID`, `ZOHO_CLIENT_SECRET`, `ZOHO_REFRESH_TOKEN` | Copied into the database (secrets encrypted) on first start. Change them later under Settings › Zoho Books.                                    |
| `ZOHO_PUSH_ENABLED`                                                               | `false` blocks every write to Zoho: bills, journals and the "Create in Zoho" buttons. Sync, mappings and previews still work, because they only read.                                                 |
| `SMTP_HOST`, `SMTP_PORT`, `SMTP_USER`, `SMTP_PASSWORD`, `SMTP_FROM`       | Email for POs, vendor sheets and sign-in codes. Ports 587 (STARTTLS) and 465 (TLS) work.                                                        |
| `MSG91_AUTH_KEY`, `MSG91_WHATSAPP_NUMBER`, `MSG91_OTP_TEMPLATE_ID`            | WhatsApp and SMS sign-in codes through MSG91.                                                                                                   |

Without SMTP or MSG91 the app still works: each message is written to the `message_log` table with status `logged`.

## Setting up the integrations

### Zoho Books

See [docs/ZOHO_API.md](docs/ZOHO_API.md) for every call and a test script. In short:

1. In the Zoho API Console, create a server-based client and generate a refresh token with Zoho Books scopes (`ZohoBooks.fullaccess.all` works). Put the four values in `.env`, or enter them under Settings › Zoho Books › Change credentials. Saving runs a test call and refuses values that fail.
2. In Zoho Books, create the bill custom field `cf_purchase_desk_id` with "Do not allow duplicate values". Then create a reporting tag named "Property".
3. Click Sync now. Then map properties to tag options, sub-categories, bill types and expense heads to accounts, the warehouse, pre-opening and stock loss accounts, GST rates to taxes, and vendors to Zoho vendors (GSTIN matches link automatically).
4. Test pushes in a Zoho test organisation, then set `ZOHO_PUSH_ENABLED=true`.

### WhatsApp (MSG91)

Get these templates approved on the MSG91 WhatsApp sender. Their body variables are filled in the order the placeholders appear in the matching message template under Settings › Message Templates.

| MSG91 template name | Message                       |
| ------------------- | ----------------------------- |
| `pd_ok`           | Request approved              |
| `pd_mod`          | Request approved with changes |
| `pd_ret`          | Request returned              |
| `pd_rej`          | Request rejected              |
| `pd_dlv`          | Warehouse delivery scheduled  |
| `pd_vsw`          | Vendor order sheet summary    |

The vendor order sheet Excel goes by email. On WhatsApp the vendor gets the summary, because WhatsApp documents need a public file URL.

### Supabase Storage

Create a private bucket (default name `purchase-desk`), set `STORAGE_DRIVER=supabase`, `SUPABASE_URL` and `SUPABASE_SERVICE_KEY`, and restart the API. Files already saved on local disk stay there. Copy the `files` volume into the bucket with the same keys if you switch after go-live.

## Running without Docker (development)

```bash
# database
docker compose up -d db
# API
cd backend && set -a && . ../.env && set +a && DATABASE_URL=postgres://purchasedesk:<password>@localhost:5544/purchasedesk?sslmode=disable STORAGE_LOCAL_DIR=../data/files go run ./cmd/server
# web (http://localhost:5173, forwards /api to :8080)
cd frontend && npm install && npm run dev
```

Checks: `cd backend && go vet ./...` and `cd frontend && npx tsc -b && npx vite build`.

## Database

- Migrations live in `backend/internal/database/migrations` and run in order on every start, each once, recorded in `schema_migrations`. Add a new numbered file for every change; never edit one that has already run in production.
- Nothing is deleted. Masters carry `active`, `inactive_by`, `inactive_at` and `inactive_reason`. `activity_log` and `document_log` reject updates and deletes at the database level.
- One `document` table holds purchase requests, utility and AMC bills, warehouse transfers and corporate requests (`kind`). One `invoice` row is one payable and one Zoho bill.

## Security notes

- Rights and property scope are checked on the server for every action. Hiding buttons on screen is only a convenience.
- Zoho client secret, refresh token and bank account numbers are encrypted with `ENCRYPTION_KEY`. Bank numbers show masked; bank proofs are visible only to Finance and the System Admin.
- Sign-in is throttled after 8 failures in 15 minutes per login.
- Put the web container behind HTTPS (a load balancer or Caddy or nginx with a certificate) before go-live, and remove the database port from `docker-compose.yml`.
- Rotate the Zoho credentials used during development.

## Backups

Invoices, files and logs are kept for all time. A suggested setup:

- Database: a nightly `pg_dump` from the `db` container to off-site storage, kept for at least 35 days, plus monthly copies kept for a year. Example: `docker compose exec -T db pg_dump -U purchasedesk -Fc purchasedesk > backup-$(date +%F).dump`.
- Files: Supabase Storage keeps them off the server. With local storage, back up the `files` volume with the database.
- Keep `ENCRYPTION_KEY` and `.env` in a password manager; a backup cannot be read without them.

## Still open with Saltstayz

- Whether Zoho locations (branches) are switched on for the Haryana and Delhi GST registrations. The app works either way; location is optional on a bill.
- To test in a Zoho sandbox: whether two vendors may use the same bill number, and whether TDS can be sent on a bill. For now the bill goes gross and the accountant applies TDS in Zoho.
- MSG91 sender number and template approval; the sending email address.
- Hosting: any server that runs Docker. Two small VMs or one 4 GB VM is plenty for 40 properties, with managed Postgres if preferred.

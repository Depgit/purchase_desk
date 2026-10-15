// Package router builds every service and mounts its routes under /api.
package router

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"

	"purchasedesk/internal/access"
	"purchasedesk/internal/activestatus"
	"purchasedesk/internal/advances"
	"purchasedesk/internal/approvals"
	"purchasedesk/internal/auditlog"
	"purchasedesk/internal/balancereject"
	"purchasedesk/internal/billtypes"
	"purchasedesk/internal/budgets"
	"purchasedesk/internal/bulkupload"
	"purchasedesk/internal/corporate"
	"purchasedesk/internal/dashboard"
	"purchasedesk/internal/encryption"
	"purchasedesk/internal/filestorage"
	"purchasedesk/internal/flowsettings"
	"purchasedesk/internal/formdrafts"
	"purchasedesk/internal/geography"
	"purchasedesk/internal/goodsreceipts"
	"purchasedesk/internal/httpjson"
	"purchasedesk/internal/invoices"
	"purchasedesk/internal/items"
	"purchasedesk/internal/login"
	"purchasedesk/internal/masterapproval"
	"purchasedesk/internal/messaging"
	"purchasedesk/internal/purchaseorders"
	"purchasedesk/internal/purchaserequests"
	"purchasedesk/internal/reports"
	"purchasedesk/internal/screenguard"
	"purchasedesk/internal/sessiontoken"
	"purchasedesk/internal/uploadedfiles"
	"purchasedesk/internal/users"
	"purchasedesk/internal/utilitybills"
	"purchasedesk/internal/vendormapping"
	"purchasedesk/internal/vendorportal"
	"purchasedesk/internal/vendors"
	"purchasedesk/internal/vendorsheets"
	"purchasedesk/internal/warehouse"
	"purchasedesk/internal/warehousejournal"
	"purchasedesk/internal/worklist"
	"purchasedesk/internal/zohoexport"
	"purchasedesk/internal/zohopush"
	"purchasedesk/internal/zohosync"
)

type Deps struct {
	DB          *pgxpool.Pool
	Tokens      sessiontoken.Tokens
	Box         *encryption.Box
	Store       filestorage.Store
	Messages    *messaging.Sender
	DevMode     bool
	PushEnabled bool
	CORSOrigin  string
	BaseURL     string // the app's address, for links in emails (the vendor portal sign-in)
}

// Built holds the services other parts of the program use, such as the scheduler.
type Built struct {
	Handler  http.Handler
	ZohoSync *zohosync.Service
	ZohoPush *zohopush.Service
	Uploads  *bulkupload.Service
	Files    *uploadedfiles.Service
	Guard    *access.Guard
	Sheets   *vendorsheets.Service
	Journals *warehousejournal.Service // nil until the Zoho and Warehouse phases are on
}

func New(d Deps) *Built {
	guard := &access.Guard{DB: d.DB, Tokens: d.Tokens}
	files := &uploadedfiles.Service{DB: d.DB, Store: d.Store}
	zsync := &zohosync.Service{DB: d.DB, Box: d.Box, PushEnabled: d.PushEnabled}
	zpush := &zohopush.Service{DB: d.DB, Sync: zsync, Files: files, PushEnabled: d.PushEnabled}
	vendorSvc := &vendors.Service{DB: d.DB, Box: d.Box, Files: files}
	uploads := &bulkupload.Service{DB: d.DB, Vendors: vendorSvc}
	journals := &warehousejournal.Service{DB: d.DB, Sync: zsync, PushEnabled: d.PushEnabled}
	inv := &invoices.Service{DB: d.DB, Guard: guard, Held: zpush.Held}
	loginSvc := &login.Service{DB: d.DB, Tokens: d.Tokens, Guard: guard, Messages: d.Messages, DevMode: d.DevMode}
	sheets := &vendorsheets.Service{DB: d.DB, Messages: d.Messages}
	portal := &vendorportal.Service{DB: d.DB, Tokens: d.Tokens, Files: files, Messages: d.Messages, BaseURL: d.BaseURL, Vendors: vendorSvc}
	masterapproval.VendorChange = vendorSvc.DecideChange // Finance approves a vendor's own change under Masters to approve

	r := chi.NewRouter()
	r.Use(middleware.RealIP, middleware.RequestID, middleware.Logger, middleware.Recoverer, middleware.Timeout(120*time.Second))
	r.Use(cors(d.CORSOrigin))
	r.Route("/api", func(r chi.Router) {
		r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
			defer cancel()
			if err := d.DB.Ping(ctx); err != nil {
				httpjson.JSON(w, http.StatusServiceUnavailable, map[string]string{"status": "database unavailable"})
				return
			}
			httpjson.JSON(w, http.StatusOK, map[string]any{"status": "ok", "storage": d.Store.Name()})
		})
		loginSvc.PublicRoutes(r)
		// The vendor portal has its own sign-in; a vendor session opens nothing else.
		portal.PublicRoutes(r)
		portal.VendorRoutes(r)
		r.Group(func(r chi.Router) {
			r.Use(guard.Middleware)
			r.Use((&screenguard.Guard{DB: d.DB}).Middleware) // screen levels from the user's roles, on every request
			loginSvc.Routes(r)
			(&auditlog.Service{DB: d.DB}).Routes(r)
			(&flowsettings.Service{DB: d.DB}).Routes(r)
			(&geography.Service{DB: d.DB}).Routes(r)
			(&activestatus.Service{DB: d.DB}).Routes(r)
			(&items.Service{DB: d.DB}).Routes(r)
			vendorSvc.Routes(r)
			(&vendormapping.Service{DB: d.DB}).Routes(r)
			(&users.Service{DB: d.DB, Guard: guard}).Routes(r)
			(&purchaserequests.Service{DB: d.DB, Guard: guard, Messages: d.Messages}).Routes(r)
			poSvc := &purchaseorders.Service{DB: d.DB, Messages: d.Messages, Files: files, PortalURL: portal.URL()}
			poSvc.Routes(r)
			portal.StaffRoutes(r)
			(&approvals.Service{DB: d.DB, Guard: guard, Messages: d.Messages, POSent: poSvc.Release}).Routes(r)
			(&goodsreceipts.Service{DB: d.DB, Files: files}).Routes(r)
			(&balancereject.Service{DB: d.DB, Messages: d.Messages}).Routes(r)
			advSvc := &advances.Service{DB: d.DB, Guard: guard, Files: files, Messages: d.Messages, POReleased: poSvc.Release}
			advSvc.Routes(r)
			zpush.Advances = advSvc // advances paid in Zoho are read back
			inv.Routes(r)
			files.Routes(r)
			(&formdrafts.Service{DB: d.DB, Files: files}).Routes(r)
			uploads.Routes(r)
			(&worklist.Service{DB: d.DB, Guard: guard}).Routes(r)
			(&masterapproval.Service{DB: d.DB}).Routes(r)

			zsync.Routes(r)
			zpush.Routes(r)
			(&zohoexport.Service{Sync: zsync}).Routes(r)
			(&billtypes.Service{DB: d.DB}).Routes(r)
			(&utilitybills.Service{DB: d.DB, Files: files}).Routes(r)
			(&budgets.Service{DB: d.DB, Guard: guard}).Routes(r)
			(&corporate.Service{DB: d.DB, Files: files}).Routes(r)
			uploads.Register("warehouse_stock", "Warehouse opening stock", "Warehouse", []string{"sku_code", "opening_qty"},
				"sku_code,item_name,opening_qty,reorder_level\nLN-0001,Bed sheet double,450,150\n",
				func(u *access.User) bool { return u.HasRight(access.Upload) && u.HasStep("wh") },
				warehouse.UploadCheck, warehouse.UploadCommit)
			(&warehouse.Service{DB: d.DB, Guard: guard, Files: files, Messages: d.Messages}).Routes(r)
			sheets.Routes(r)
			journals.Routes(r)
			(&reports.Service{DB: d.DB, Guard: guard}).Routes(r)
			(&dashboard.Service{DB: d.DB, Guard: guard, Invoices: inv}).Routes(r)
		})
	})
	return &Built{Handler: r, ZohoSync: zsync, ZohoPush: zpush, Uploads: uploads, Files: files, Guard: guard, Sheets: sheets, Journals: journals}
}

// cors allows the Vite dev server in development. In Docker the frontend is served from the same origin.
func cors(origin string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if o := r.Header.Get("Origin"); origin != "" && strings.EqualFold(o, origin) {
				w.Header().Set("Access-Control-Allow-Origin", o)
				w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
				if r.Method == http.MethodOptions {
					w.WriteHeader(http.StatusNoContent)
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

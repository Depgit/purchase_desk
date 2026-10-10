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
	"purchasedesk/internal/approvals"
	"purchasedesk/internal/auditlog"
	"purchasedesk/internal/billtypes"
	"purchasedesk/internal/budgets"
	"purchasedesk/internal/bulkupload"
	"purchasedesk/internal/corporate"
	"purchasedesk/internal/dashboard"
	"purchasedesk/internal/encryption"
	"purchasedesk/internal/filestorage"
	"purchasedesk/internal/flowsettings"
	"purchasedesk/internal/geography"
	"purchasedesk/internal/goodsreceipts"
	"purchasedesk/internal/httpjson"
	"purchasedesk/internal/invoices"
	"purchasedesk/internal/items"
	"purchasedesk/internal/login"
	"purchasedesk/internal/messaging"
	"purchasedesk/internal/purchaseorders"
	"purchasedesk/internal/purchaserequests"
	"purchasedesk/internal/releasephase"
	"purchasedesk/internal/reports"
	"purchasedesk/internal/sessiontoken"
	"purchasedesk/internal/uploadedfiles"
	"purchasedesk/internal/users"
	"purchasedesk/internal/utilitybills"
	"purchasedesk/internal/vendormapping"
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
	var journals *warehousejournal.Service
	inv := &invoices.Service{DB: d.DB, Guard: guard}
	if releasephase.On(releasephase.Zoho) {
		inv.Held, inv.ZohoConnected = zpush.Held, zsync.Connected
	}
	loginSvc := &login.Service{DB: d.DB, Tokens: d.Tokens, Guard: guard, Messages: d.Messages, DevMode: d.DevMode}
	sheets := &vendorsheets.Service{DB: d.DB, Messages: d.Messages}

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
			httpjson.JSON(w, http.StatusOK, map[string]any{"status": "ok", "storage": d.Store.Name(), "phase": releasephase.Current()})
		})
		loginSvc.PublicRoutes(r)
		r.Group(func(r chi.Router) {
			r.Use(guard.Middleware)
			loginSvc.Routes(r)
			(&auditlog.Service{DB: d.DB}).Routes(r)
			(&flowsettings.Service{DB: d.DB}).Routes(r)
			(&geography.Service{DB: d.DB}).Routes(r)
			(&activestatus.Service{DB: d.DB}).Routes(r)
			(&items.Service{DB: d.DB}).Routes(r)
			vendorSvc.Routes(r)
			(&vendormapping.Service{DB: d.DB}).Routes(r)
			(&users.Service{DB: d.DB}).Routes(r)
			(&approvals.Service{DB: d.DB, Guard: guard, Messages: d.Messages}).Routes(r)
			(&purchaserequests.Service{DB: d.DB, Guard: guard}).Routes(r)
			(&purchaseorders.Service{DB: d.DB, Messages: d.Messages}).Routes(r)
			(&goodsreceipts.Service{DB: d.DB, Files: files}).Routes(r)
			inv.Routes(r)
			files.Routes(r)
			uploads.Routes(r)
			(&worklist.Service{DB: d.DB, Guard: guard}).Routes(r)

			// Later phases mount only when RELEASE_PHASE in .env reaches them.
			if releasephase.On(releasephase.Zoho) {
				zsync.Routes(r)
				zpush.Routes(r)
				(&zohoexport.Service{Sync: zsync}).Routes(r)
			}
			if releasephase.On(releasephase.Bills) {
				(&billtypes.Service{DB: d.DB}).Routes(r)
				(&utilitybills.Service{DB: d.DB, Files: files}).Routes(r)
				(&budgets.Service{DB: d.DB, Guard: guard}).Routes(r)
				(&corporate.Service{DB: d.DB, Files: files}).Routes(r)
			}
			if releasephase.On(releasephase.Warehouse) {
				uploads.Register("warehouse_stock", "Warehouse opening stock", "Warehouse", []string{"sku_code", "opening_qty"},
					"sku_code,item_name,opening_qty,reorder_level\nLN-0001,Bed sheet double,450,150\n",
					func(u *access.User) bool { return u.HasRight(access.Upload) && u.HasStep("wh") },
					warehouse.UploadCheck, warehouse.UploadCommit)
				(&warehouse.Service{DB: d.DB, Guard: guard, Files: files, Messages: d.Messages}).Routes(r)
				sheets.Routes(r)
			}
			if releasephase.On(releasephase.Zoho) && releasephase.On(releasephase.Warehouse) {
				journals = &warehousejournal.Service{DB: d.DB, Sync: zsync, PushEnabled: d.PushEnabled}
				journals.Routes(r)
			}
			if releasephase.On(releasephase.Reports) {
				(&reports.Service{DB: d.DB, Guard: guard}).Routes(r)
				(&dashboard.Service{DB: d.DB, Guard: guard, Invoices: inv}).Routes(r)
			}
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

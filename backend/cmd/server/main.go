// Command server runs the Purchase Desk API.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "time/tzdata"

	"github.com/jackc/pgx/v5/pgxpool"

	"purchasedesk/internal/config"
	"purchasedesk/internal/database"
	"purchasedesk/internal/demodata"
	"purchasedesk/internal/encryption"
	"purchasedesk/internal/filestorage"
	"purchasedesk/internal/firstrun"
	"purchasedesk/internal/messaging"
	"purchasedesk/internal/releasephase"
	"purchasedesk/internal/router"
	"purchasedesk/internal/scheduler"
	"purchasedesk/internal/sessiontoken"
	"purchasedesk/internal/zohoapi"
)

func main() {
	cfg := config.Load()
	if len(cfg.JWTSecret) < 32 {
		log.Fatal("JWT_SECRET must be at least 32 characters")
	}
	box, err := encryption.New(cfg.EncKey)
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := connect(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		log.Fatal(err)
	}
	releasephase.Set(cfg.ReleasePhase)
	if err := firstrun.EnsureAdmin(ctx, db, cfg.AdminEmail, cfg.AdminPassword, cfg.AdminName); err != nil {
		log.Fatal(err)
	}
	if cfg.SeedDemo {
		if err := demodata.Load(ctx, db, box); err != nil {
			log.Fatal(err)
		}
	}

	var store filestorage.Store = filestorage.Local{Dir: cfg.StorageLocalDir}
	if cfg.StorageDriver == "supabase" {
		if cfg.SupabaseURL == "" || cfg.SupabaseKey == "" {
			log.Fatal("STORAGE_DRIVER=supabase needs SUPABASE_URL and SUPABASE_SERVICE_KEY")
		}
		store = filestorage.NewSupabase(cfg.SupabaseURL, cfg.SupabaseKey, cfg.SupabaseBucket)
	}
	msgs := messaging.New(db, messaging.Config{SMTPHost: cfg.SMTPHost, SMTPPort: cfg.SMTPPort, SMTPUser: cfg.SMTPUser, SMTPPassword: cfg.SMTPPassword,
		SMTPFrom: cfg.SMTPFrom, MSG91AuthKey: cfg.MSG91AuthKey, MSG91WhatsAppNumber: cfg.MSG91WhatsAppNumber, MSG91OTPTemplate: cfg.MSG91OTPTemplate})

	cors := ""
	if cfg.Env == "development" {
		cors = cfg.BaseURL
	}
	app := router.New(router.Deps{DB: db, Tokens: sessiontoken.Tokens{Secret: []byte(cfg.JWTSecret)}, Box: box, Store: store, Messages: msgs,
		DevMode: cfg.Env == "development", PushEnabled: cfg.ZohoPushEnabled, CORSOrigin: cors})
	if err := app.ZohoSync.SeedFromEnv(ctx, zohoapi.Config{AccountsURL: cfg.ZohoAccountsURL, APIURL: cfg.ZohoAPIURL, OrgID: cfg.ZohoOrgID,
		ClientID: cfg.ZohoClientID, ClientSecret: cfg.ZohoClientSecret, RefreshToken: cfg.ZohoRefreshToken}); err != nil {
		log.Fatal(err)
	}

	jobs := []scheduler.Job{}
	if releasephase.On(releasephase.Zoho) {
		jobs = append(jobs,
			scheduler.Job{Name: "zoho-sync", Every: time.Hour, Run: func(ctx context.Context) error {
				if !app.ZohoSync.Connected(ctx) {
					return nil
				}
				_, err := app.ZohoSync.Sync(ctx)
				return err
			}},
			scheduler.Job{Name: "zoho-payments", Every: 2 * time.Hour, Run: func(ctx context.Context) error {
				if !app.ZohoSync.Connected(ctx) {
					return nil
				}
				_, err := app.ZohoPush.ReadPayments(ctx)
				return err
			}})
	}
	if releasephase.On(releasephase.Sheets) {
		jobs = append(jobs, scheduler.Job{Name: "vendor-sheets", Every: time.Minute, Run: app.Sheets.RunAuto})
	}
	scheduler.Start(ctx, jobs...)

	srv := &http.Server{Addr: ":" + cfg.Port, Handler: app.Handler, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		log.Printf("Purchase Desk API listening on :%s (release phase: %d, storage: %s, Zoho push enabled: %v)", cfg.Port, releasephase.Current(), store.Name(), cfg.ZohoPushEnabled)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()
	<-ctx.Done()
	sctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = srv.Shutdown(sctx)
}

// connect waits for the database, which may still be starting in Docker.
func connect(ctx context.Context, url string) (*pgxpool.Pool, error) {
	var last error
	for i := range 30 {
		db, err := database.Connect(ctx, url)
		if err == nil {
			if err = db.Ping(ctx); err == nil {
				return db, nil
			}
			db.Close()
		}
		last = err
		log.Printf("[Attempt %d/30] waiting for database: %v", i+1, err)

		// Use time.NewTimer to prevent memory leaks in select loops
		t := time.NewTimer(2 * time.Second)
		select {
		case <-t.C:
			// 2 seconds passed, proceed to next iteration
		case <-ctx.Done():
			t.Stop() // Explicitly release timer resources immediately
			return nil, ctx.Err()
		}
	}
	return nil, fmt.Errorf("database connection failed after 30 attempts: %w", last)
}

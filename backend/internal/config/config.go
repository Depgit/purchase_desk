// Package config reads the settings the server needs from environment variables (.env in Docker).
package config

import (
	"os"
	"strconv"
	"strings"
)

type Config struct {
	Env         string
	Port        string
	BaseURL     string
	DatabaseURL string
	JWTSecret   string
	EncKey      string

	AdminEmail, AdminPassword, AdminName string
	SeedDemo                             bool

	StorageDriver   string
	StorageLocalDir string
	SupabaseURL     string
	SupabaseKey     string
	SupabaseBucket  string

	ZohoAccountsURL, ZohoAPIURL                                 string
	ZohoOrgID, ZohoClientID, ZohoClientSecret, ZohoRefreshToken string
	ZohoPushEnabled                                             bool

	SMTPHost, SMTPPort, SMTPUser, SMTPPassword, SMTPFrom string
	MSG91AuthKey, MSG91WhatsAppNumber, MSG91OTPTemplate  string
}

func env(k, def string) string {
	if v, ok := os.LookupEnv(k); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return def
}

func envBool(k string, def bool) bool {
	v, err := strconv.ParseBool(env(k, strconv.FormatBool(def)))
	if err != nil {
		return def
	}
	return v
}

func envInt(k string, def int) int {
	v, err := strconv.Atoi(env(k, strconv.Itoa(def)))
	if err != nil {
		return def
	}
	return v
}

func Load() Config {
	return Config{
		Env:                 env("APP_ENV", "development"),
		Port:                env("APP_PORT", "8080"),
		BaseURL:             env("APP_BASE_URL", "http://localhost:5173"),
		DatabaseURL:         env("DATABASE_URL", "postgres://purchasedesk@localhost:5432/purchasedesk?sslmode=disable"),
		JWTSecret:           env("JWT_SECRET", ""),
		EncKey:              env("ENCRYPTION_KEY", ""),
		AdminEmail:          env("ADMIN_EMAIL", "admin@saltstayz.com"),
		AdminPassword:       env("ADMIN_PASSWORD", ""),
		AdminName:           strings.Trim(env("ADMIN_NAME", "System Admin"), `"`),
		SeedDemo:            envBool("SEED_DEMO", false),
		StorageDriver:       env("STORAGE_DRIVER", "local"),
		StorageLocalDir:     env("STORAGE_LOCAL_DIR", "./data/files"),
		SupabaseURL:         strings.TrimRight(env("SUPABASE_URL", ""), "/"),
		SupabaseKey:         env("SUPABASE_SERVICE_KEY", ""),
		SupabaseBucket:      env("SUPABASE_BUCKET", "purchase-desk"),
		ZohoAccountsURL:     strings.TrimRight(env("ZOHO_ACCOUNTS_URL", "https://accounts.zoho.in"), "/"),
		ZohoAPIURL:          strings.TrimRight(env("ZOHO_API_URL", "https://www.zohoapis.in"), "/"),
		ZohoOrgID:           env("ZOHO_ORG_ID", ""),
		ZohoClientID:        env("ZOHO_CLIENT_ID", ""),
		ZohoClientSecret:    env("ZOHO_CLIENT_SECRET", ""),
		ZohoRefreshToken:    env("ZOHO_REFRESH_TOKEN", ""),
		ZohoPushEnabled:     envBool("ZOHO_PUSH_ENABLED", false),
		SMTPHost:            env("SMTP_HOST", ""),
		SMTPPort:            env("SMTP_PORT", "587"),
		SMTPUser:            env("SMTP_USER", ""),
		SMTPPassword:        env("SMTP_PASSWORD", ""),
		SMTPFrom:            env("SMTP_FROM", ""),
		MSG91AuthKey:        env("MSG91_AUTH_KEY", ""),
		MSG91WhatsAppNumber: env("MSG91_WHATSAPP_NUMBER", ""),
		MSG91OTPTemplate:    env("MSG91_OTP_TEMPLATE_ID", ""),
	}
}

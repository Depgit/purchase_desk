// Package firstrun prepares an empty database: it creates the first System Admin from the environment.
package firstrun

import (
	"context"
	"log"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"purchasedesk/internal/passwords"
)

func EnsureAdmin(ctx context.Context, db *pgxpool.Pool, email, password, name string) error {
	var n int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM app_user`).Scan(&n); err != nil || n > 0 {
		return err
	}
	if len(password) < 8 {
		log.Printf("first run: ADMIN_PASSWORD is shorter than 8 characters; no admin created")
		return nil
	}
	h, err := passwords.Hash(password)
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
		var id int64
		if err := tx.QueryRow(ctx, `INSERT INTO app_user(name,email,department,password_hash) VALUES ($1,$2,'Admin',$3) RETURNING id`, name, email, h).Scan(&id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO user_role(user_id,role_id,scope) SELECT $1, id, 'all' FROM role WHERE code='admin'`, id); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO activity_log(user_name,role_name,module,action,record,details) VALUES ('System','System','Users','First System Admin created',$1,'')`, email)
		log.Printf("first run: System Admin %s created", email)
		return err
	})
}

// Package sessiontoken issues and checks the signed token a browser sends after sign-in.
package sessiontoken

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type Tokens struct{ Secret []byte }

type claims struct {
	UserID int64 `json:"uid"`
	jwt.RegisteredClaims
}

func (t Tokens) Issue(userID int64, ttl time.Duration) (string, error) {
	c := claims{UserID: userID, RegisteredClaims: jwt.RegisteredClaims{
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(ttl)),
		IssuedAt:  jwt.NewNumericDate(time.Now()),
		Issuer:    "purchase-desk",
	}}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString(t.Secret)
}

func (t Tokens) Parse(s string) (int64, error) {
	c := &claims{}
	tok, err := jwt.ParseWithClaims(s, c, func(tk *jwt.Token) (any, error) {
		if _, ok := tk.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method")
		}
		return t.Secret, nil
	})
	if err != nil || !tok.Valid {
		return 0, errors.New("invalid session")
	}
	return c.UserID, nil
}

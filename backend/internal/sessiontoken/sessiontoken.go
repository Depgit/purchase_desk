// Package sessiontoken issues and checks the signed token a browser sends after sign-in.
package sessiontoken

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type Tokens struct{ Secret []byte }

// claims carry a staff user (uid) or, on the vendor portal, a vendor (vid with kind "vendor"). Each kind of token is
// accepted only by its own side, so a vendor's session can never open the staff app.
type claims struct {
	UserID   int64  `json:"uid,omitempty"`
	VendorID int64  `json:"vid,omitempty"`
	Kind     string `json:"knd,omitempty"`
	jwt.RegisteredClaims
}

func (t Tokens) Issue(userID int64, ttl time.Duration) (string, error) {
	return t.sign(claims{UserID: userID}, ttl)
}

// IssueVendor signs a vendor portal session.
func (t Tokens) IssueVendor(vendorID int64, ttl time.Duration) (string, error) {
	return t.sign(claims{VendorID: vendorID, Kind: "vendor"}, ttl)
}

func (t Tokens) sign(c claims, ttl time.Duration) (string, error) {
	c.RegisteredClaims = jwt.RegisteredClaims{
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(ttl)),
		IssuedAt:  jwt.NewNumericDate(time.Now()),
		Issuer:    "purchase-desk",
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString(t.Secret)
}

// Parse checks a staff session and returns its user.
func (t Tokens) Parse(s string) (int64, error) {
	c, err := t.parse(s)
	if err != nil || c.Kind != "" || c.UserID == 0 {
		return 0, errors.New("invalid session")
	}
	return c.UserID, nil
}

// ParseVendor checks a vendor portal session and returns its vendor.
func (t Tokens) ParseVendor(s string) (int64, error) {
	c, err := t.parse(s)
	if err != nil || c.Kind != "vendor" || c.VendorID == 0 {
		return 0, errors.New("invalid session")
	}
	return c.VendorID, nil
}

func (t Tokens) parse(s string) (*claims, error) {
	c := &claims{}
	tok, err := jwt.ParseWithClaims(s, c, func(tk *jwt.Token) (any, error) {
		if _, ok := tk.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method")
		}
		return t.Secret, nil
	})
	if err != nil || !tok.Valid {
		return nil, errors.New("invalid session")
	}
	return c, nil
}

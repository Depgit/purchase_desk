package money

import (
	"testing"
	"time"
)

func TestDueDate(t *testing.T) {
	d := func(s string) time.Time { x, _ := time.Parse("2006-01-02", s); return x }
	cases := []struct{ invoice, terms, want string }{
		{"2026-10-01", Next15th, "2026-11-15"},
		{"2026-10-29", Next15th, "2026-11-15"},
		{"2026-10-31", Next15th, "2026-11-15"},
		{"2026-11-01", Next15th, "2026-12-15"},
		{"2026-12-29", Next15th, "2027-01-15"},
		{"2026-10-29", "Net 30", "2026-11-28"},
		{"2026-10-29", "Advance", "2026-10-29"},
	}
	for _, c := range cases {
		if got := DueDate(d(c.invoice), c.terms).Format("2006-01-02"); got != c.want {
			t.Errorf("%s with %q: got %s, want %s", c.invoice, c.terms, got, c.want)
		}
	}
}

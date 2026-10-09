// Package money rounds amounts to paise, computes line values with GST and formats rupees.
package money

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

func Round2(v float64) float64 { return math.Round(v*100) / 100 }

// LineValue is quantity × rate plus GST.
func LineValue(qty, rate, gstPct float64) float64 { return qty * rate * (1 + gstPct/100) }

// INR formats a whole-rupee amount in the Indian grouping, for example ₹1,23,456.
func INR(v float64) string {
	neg := v < 0
	s := strconv.FormatInt(int64(math.Round(math.Abs(v))), 10)
	if len(s) > 3 {
		head, tail := s[:len(s)-3], s[len(s)-3:]
		parts := []string{}
		for len(head) > 2 {
			parts = append([]string{head[len(head)-2:]}, parts...)
			head = head[:len(head)-2]
		}
		if head != "" {
			parts = append([]string{head}, parts...)
		}
		s = strings.Join(parts, ",") + "," + tail
	}
	if neg {
		return "-₹" + s
	}
	return "₹" + s
}

// TermsDays reads "Net 30" as 30. "Advance" and anything else is 0.
func TermsDays(t string) int {
	var n int
	if _, err := fmt.Sscanf(t, "Net %d", &n); err == nil {
		return n
	}
	return 0
}

// Next15th is a payment term: due on the 15th of the month after the invoice date, so anything dated in
// October (1st to 31st) is due on 15 November.
const Next15th = "15th of next month"

var PaymentTerms = []string{"Net 15", "Net 30", "Net 45", Next15th, "Advance"}

// DueDate is when an invoice dated on day falls due under the payment terms. "Net N" adds N days,
// "15th of next month" gives the 15th of the following month, and "Advance" or anything else is the invoice date.
func DueDate(day time.Time, terms string) time.Time {
	if terms == Next15th {
		return time.Date(day.Year(), day.Month()+1, 15, 0, 0, 0, 0, day.Location())
	}
	return day.AddDate(0, 0, TermsDays(terms))
}

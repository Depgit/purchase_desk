// Package clock gives the current time, day and month in India Standard Time.
package clock

import (
	"strings"
	"time"
)

var IST = load()

func load() *time.Location {
	l, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		return time.FixedZone("IST", 5*3600+1800)
	}
	return l
}

func Now() time.Time { return time.Now().In(IST) }

func Today() time.Time { return DayOf(Now()) }

func DayOf(t time.Time) time.Time {
	t = t.In(IST)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, IST)
}

func YMD(t time.Time) string { return t.In(IST).Format("2006-01-02") }

func MonthStart(t time.Time) time.Time {
	t = t.In(IST)
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, IST)
}

func DaysInMonth(t time.Time) int {
	t = t.In(IST)
	return time.Date(t.Year(), t.Month()+1, 0, 0, 0, 0, 0, IST).Day()
}

// ParseDate reads YYYY-MM-DD.
func ParseDate(s string) (time.Time, bool) {
	t, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(s), IST)
	return t, err == nil
}

// ParseMonth reads YYYY-MM and returns the first day of that month.
func ParseMonth(s string) (time.Time, bool) {
	t, err := time.ParseInLocation("2006-01", strings.TrimSpace(s), IST)
	return t, err == nil
}

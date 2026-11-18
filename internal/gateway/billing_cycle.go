package gateway

import (
	"strings"
	"time"
)

// Clamp each month's boundary independently so a February boundary does not
// change an account's configured day for March.
func billingCycleRange(now time.Time, day int) (time.Time, time.Time) {
	boundary := func(month time.Time) time.Time {
		last := month.AddDate(0, 1, -1).Day()
		return time.Date(month.Year(), month.Month(), min(day, last), 0, 0, 0, 0, month.Location())
	}
	month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	if now.Before(boundary(month)) {
		month = month.AddDate(0, -1, 0)
	}
	return boundary(month), boundary(month.AddDate(0, 1, 0))
}

// Used by both credential lists; return local midnight as a timestamptz.
func (a *App) billingTimezoneSQL() string {
	return "'" + strings.ReplaceAll(a.reportLocation().String(), "'", "''") + "'"
}

func (a *App) billingCycleBoundarySQL(day, month string) string {
	return "(" + month + "+(least(" + day + ",extract(day FROM " + month + "+interval '1 month - 1 day')::int)-1)*interval '1 day') AT TIME ZONE " + a.billingTimezoneSQL()
}

func (a *App) billingCycleStartSQL(day, at string) string {
	zone := a.billingTimezoneSQL()
	month := "date_trunc('month',(" + at + ") AT TIME ZONE " + zone + ")"
	current := a.billingCycleBoundarySQL(day, month)
	return "CASE WHEN (" + at + ")>=(" + current + ") THEN (" + current + ") ELSE (" + a.billingCycleBoundarySQL(day, "("+month+"-interval '1 month')") + ") END"
}

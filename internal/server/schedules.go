package server

import (
	"time"
	"vibexui/internal/model"
)

// Calendar schedules use the panel's local timezone; intervals remain exact durations.
// Find the first valid instant of a calendar date. A binary search handles
// skipped/repeated midnights without relying on time.Date's ambiguous DST choice.
func calendarStart(date time.Time, loc *time.Location) (time.Time, bool) {
	date = time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, time.UTC)
	key := func(t time.Time) int { return t.Year()*10000 + int(t.Month())*100 + t.Day() }
	target := key(date)
	lo, hi := date.Add(-36*time.Hour).Unix(), date.Add(36*time.Hour).Unix()
	for lo < hi {
		mid := lo + (hi-lo)/2
		if key(time.Unix(mid, 0).In(loc)) >= target {
			hi = mid
		} else {
			lo = mid + 1
		}
	}
	first := time.Unix(lo, 0).In(loc)
	return first, key(first) == target
}
func monthlyDate(year int, month time.Month, day int, loc *time.Location) time.Time {
	first := time.Date(year, month, 1, 0, 0, 0, 0, time.UTC)
	last := first.AddDate(0, 1, -1).Day()
	if day > last {
		day = last
	}
	start, ok := calendarStart(first.AddDate(0, 0, day-1), loc)
	if !ok {
		return time.Time{}
	}
	return start
}
func nextMonthly(after time.Time, day int) time.Time {
	local := after.In(time.Local)
	for offset := 0; ; offset++ {
		next := monthlyDate(local.Year(), local.Month()+time.Month(offset), day, time.Local)
		if !next.IsZero() && next.After(after) {
			return next
		}
	}
}
func nextWeekly(after time.Time, weekday int) time.Time {
	local := after.In(time.Local)
	today := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)
	offset := (weekday%7 - int(today.Weekday()) + 7) % 7
	for date := today.AddDate(0, 0, offset); ; date = date.AddDate(0, 0, 7) {
		next, ok := calendarStart(date, time.Local)
		if ok && next.After(after) {
			return next
		}
	}
}
func nextDaily(after time.Time) time.Time {
	local := after.In(time.Local)
	today := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)
	for date := today.AddDate(0, 0, 1); ; date = date.AddDate(0, 0, 1) {
		next, ok := calendarStart(date, time.Local)
		if ok && next.After(after) {
			return next
		}
	}
}
func nextTrafficReset(after time.Time, mode string, day int) time.Time {
	local := after.In(time.Local)
	switch mode {
	case "hourly":
		// Advancing an absolute hour also handles a repeated local hour at DST fall-back.
		return local.Add(time.Hour - time.Duration(local.Minute())*time.Minute - time.Duration(local.Second())*time.Second - time.Duration(local.Nanosecond()))
	case "daily":
		return nextDaily(after)
	case "weekly":
		return nextWeekly(after, 1)
	case "monthly":
		return nextMonthly(after, day)
	default:
		return time.Time{}
	}
}
func validTrafficReset(mode string, day int) bool {
	return (mode == "" || mode == "never" || mode == "hourly" || mode == "daily" || mode == "weekly" || mode == "monthly") && day >= 1 && day <= 31
}
func renewClient(c *model.Client, now time.Time) {
	if !c.Enabled || c.ExpiresAt.IsZero() || now.Before(c.ExpiresAt) || (c.ResetDays == 0 && c.ResetDay == 0 && c.ResetWeekday == 0) {
		return
	}
	next := c.ExpiresAt
	periods := 0
	if c.ResetDays > 0 {
		step := time.Duration(c.ResetDays) * 24 * time.Hour
		periods = int(now.Sub(next)/step) + 1
		next = next.Add(time.Duration(periods) * step)
	} else {
		// Jump to a future boundary, counting elapsed renewals without one loop per day.
		if c.ResetDay > 0 {
			old := next.In(time.Local)
			next = nextMonthly(now, c.ResetDay)
			future := next.In(time.Local)
			periods = (future.Year()-old.Year())*12 + int(future.Month()-old.Month())
			if monthlyDate(old.Year(), old.Month(), c.ResetDay, time.Local).After(c.ExpiresAt) {
				periods++
			}
		} else {
			first := nextWeekly(next, c.ResetWeekday)
			next = nextWeekly(now, c.ResetWeekday)
			// Calendar dates, rather than durations, count weeks across DST.
			a := time.Date(first.Year(), first.Month(), first.Day(), 0, 0, 0, 0, time.UTC)
			b := time.Date(next.Year(), next.Month(), next.Day(), 0, 0, 0, 0, time.UTC)
			periods = int(b.Sub(a)/(7*24*time.Hour)) + 1
		}
	}
	if periods < 1 {
		periods = 1
	}
	if c.ResetMax > 0 && periods > c.ResetMax-c.RenewalCount {
		return
	}
	c.ExpiresAt = next
	c.RenewalCount += periods
	c.QuotaBaseline = model.Traffic{Upload: c.Upload, Download: c.Download}
}
func reconcileNodes(st *model.State, now time.Time, affected map[string]bool) {
	for i := range st.Nodes {
		n := &st.Nodes[i]
		if n.TrafficReset != "" && n.TrafficReset != "never" {
			if n.QuotaResetsAt.IsZero() {
				n.QuotaResetsAt = nextTrafficReset(now, n.TrafficReset, n.TrafficResetDay)
			}
			if !now.Before(n.QuotaResetsAt) {
				n.QuotaBaseline = model.Traffic{Upload: n.Upload, Download: n.Download}
				n.QuotaResetsAt = nextTrafficReset(now, n.TrafficReset, n.TrafficResetDay)
			}
		}
		old := n.AccessState
		if old == "" {
			if n.Enabled {
				old = "active"
			} else {
				old = "disabled"
			}
		}
		status := model.NodeStatus(*n, now)
		if (old == "active") != (status == "active") {
			affected[n.ServerID] = true
		}
		n.AccessState = status
	}
}

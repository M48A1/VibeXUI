package server

import (
	"context"
	"log"
	"net/http"
	"time"
	"vibexui/internal/model"
)

// Reconcile once per transition, including deadlines while no Agent is polling.
func reconcileClients(st *model.State, now time.Time) {
	affected := map[string]bool{}
	reconcileNodes(st, now, affected)
	for i := range st.Clients {
		c := &st.Clients[i]
		renewClient(c, now)
		if c.TrafficReset != "" && c.TrafficReset != "never" {
			if c.QuotaResetsAt.IsZero() {
				c.QuotaResetsAt = nextTrafficReset(now, c.TrafficReset, c.TrafficResetDay)
			}
			if !now.Before(c.QuotaResetsAt) {
				c.QuotaBaseline = model.Traffic{Upload: c.Upload, Download: c.Download}
				c.QuotaResetsAt = nextTrafficReset(now, c.TrafficReset, c.TrafficResetDay)
			}
		}
		if c.QuotaPeriodMonths > 0 && !c.QuotaResetsAt.IsZero() && !now.Before(c.QuotaResetsAt) {
			c.QuotaBaseline = model.Traffic{Upload: c.Upload, Download: c.Download}
			anchor := c.QuotaCycleAnchor
			months := (now.UTC().Year()-anchor.Year())*12 + int(now.UTC().Month()-anchor.Month())
			steps := months / c.QuotaPeriodMonths
			if steps < 1 {
				steps = 1
			}
			for next := quotaCycleDate(anchor, steps*c.QuotaPeriodMonths); ; next = quotaCycleDate(anchor, steps*c.QuotaPeriodMonths) {
				if now.Before(next) {
					c.QuotaResetsAt = next
					break
				}
				steps++
			}
		}
		status := model.ClientStatus(*c, now)
		old := c.AccessState
		if old == "" {
			if c.Enabled {
				old = "active"
			} else {
				old = "disabled"
			}
		}
		if c.NodeIPStates == nil {
			c.NodeIPStates = map[string]model.NodeIPState{}
		}
		for _, n := range st.Nodes {
			if !model.Contains(c.NodeIDs, n.ID) {
				continue
			}
			previous := c.NodeIPStates[n.ID]
			// Use the recorded applied policy, rather than the wall clock, to detect
			// the end of a cooldown and restore the user in the Agent configuration.
			wasAllowed := old == "active" && previous.BlockedUntil.IsZero()
			current := sampleNodeIPs(st, *c, n, now)
			limit := model.ClientIPLimit(*c, n.ID)
			if limit == 0 {
				current.BlockedUntil = time.Time{}
			} else {
				current.BlockedUntil = previous.BlockedUntil
				if !now.Before(current.BlockedUntil) {
					current.BlockedUntil = time.Time{}
				}
				if status == "active" && current.BlockedUntil.IsZero() && current.StatsState == "ok" && len(current.OnlineIPs) > limit {
					current.BlockedUntil = now.Add(time.Minute)
				}
			}
			if limit > 0 || current.StatsState != "pending" || !current.BlockedUntil.IsZero() {
				c.NodeIPStates[n.ID] = current
			} else {
				delete(c.NodeIPStates, n.ID)
			}
			allowed := status == "active" && current.BlockedUntil.IsZero()
			if wasAllowed != allowed {
				affected[n.ServerID] = true
			}
		}
		for nid := range c.NodeIPLimits {
			if !model.Contains(c.NodeIDs, nid) {
				delete(c.NodeIPLimits, nid)
			}
		}
		for nid := range c.NodeIPStates {
			if !model.Contains(c.NodeIDs, nid) {
				delete(c.NodeIPStates, nid)
			}
		}
		c.AccessState = status
	}
	for sid := range affected {
		bump(st, sid)
	}
}

func (s *Server) RunMaintenance(ctx context.Context) {
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		if err := s.store.Update(func(st *model.State) error { reconcileClients(st, time.Now()); return nil }); err != nil {
			log.Printf("用户策略检查失败：%v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (s *Server) resetQuota(w http.ResponseWriter, r *http.Request) {
	var in struct{}
	if !decode(w, r, &in) {
		return
	}
	var out model.Client
	err := s.store.Update(func(st *model.State) error {
		for i := range st.Clients {
			c := &st.Clients[i]
			if c.ID != r.PathValue("id") {
				continue
			}
			c.QuotaBaseline = model.Traffic{Upload: c.Upload, Download: c.Download}
			reconcileClients(st, time.Now())
			out = *c
			return nil
		}
		return errNotFound
	})
	s.result(w, err, out)
}

// Keep the original day across short months (January 31 -> February 28 -> March 31).
func quotaCycleDate(anchor time.Time, months int) time.Time {
	first := time.Date(anchor.Year(), anchor.Month()+time.Month(months), 1, anchor.Hour(), anchor.Minute(), anchor.Second(), anchor.Nanosecond(), time.UTC)
	last := first.AddDate(0, 1, -1).Day()
	day := anchor.Day()
	if day > last {
		day = last
	}
	return first.AddDate(0, 0, day-1)
}

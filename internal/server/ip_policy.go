package server

import (
	"fmt"
	"net/netip"
	"sort"
	"strings"
	"time"
	"vibexui/internal/model"
)

const onlineSampleLifetime = 35 * time.Second

func validateOnlineIPs(sample map[string][]string) error {
	if len(sample) > 5000 {
		return fmt.Errorf("在线用户统计过长")
	}
	count := 0
	for key, ips := range sample {
		if len(key) > 100 || len(ips) > 129 {
			return fmt.Errorf("在线 IP 统计过长")
		}
		count += len(ips)
		for _, ip := range ips {
			if _, err := netip.ParseAddr(strings.Trim(ip, "[]")); err != nil {
				return fmt.Errorf("在线统计包含无效 IP")
			}
		}
	}
	if count > 5000 {
		return fmt.Errorf("在线 IP 统计过长")
	}
	return nil
}

func accountOnlineIPs(st *model.State, s *model.Server, r model.Report, now time.Time) {
	s.IPStatsError = r.IPStatsError
	if r.OnlineIPs == nil {
		if s.IPStatsError == "" {
			s.IPStatsError = "Agent 未提供在线 IP 统计，请升级"
		}
		return
	}
	if r.IPStatsError != "" {
		return
	}
	s.OnlineIPs = map[string][]string{}
	for _, c := range st.Clients {
		for _, n := range st.Nodes {
			if n.ServerID != s.ID || !model.Contains(c.NodeIDs, n.ID) {
				continue
			}
			key := model.BindingKey(c.ID, n.ID)
			ips, present := r.OnlineIPs[key]
			if !present {
				continue
			}
			seen := map[string]bool{}
			normalized := []string{}
			for _, ip := range ips {
				addr, _ := netip.ParseAddr(strings.Trim(ip, "[]"))
				text := addr.Unmap().String()
				if !seen[text] {
					seen[text] = true
					normalized = append(normalized, text)
				}
			}
			sort.Strings(normalized)
			s.OnlineIPs[key] = normalized
		}
	}
	s.IPStatsAt = now
	for i := range st.Clients {
		c := &st.Clients[i]
		for _, n := range st.Nodes {
			if s.Running && n.ServerID == s.ID && len(s.OnlineIPs[model.BindingKey(c.ID, n.ID)]) > 0 {
				c.LastOnlineAt = now
				break
			}
		}
	}
}

func sampleNodeIPs(st *model.State, c model.Client, n model.Node, now time.Time) model.NodeIPState {
	old := c.NodeIPStates[n.ID]
	out := model.NodeIPState{OnlineIPs: []string{}, UpdatedAt: old.UpdatedAt, BlockedUntil: old.BlockedUntil, StatsState: "pending"}
	s := serverAt(st, n.ServerID)
	if s == nil {
		return out
	}
	ips, known := s.OnlineIPs[model.BindingKey(c.ID, n.ID)]
	if known {
		out.UpdatedAt = s.IPStatsAt
	}
	switch {
	case s.IPStatsError != "":
		out.StatsState = "error"
	case !known:
		out.StatsState = "pending"
	case now.Sub(s.IPStatsAt) > onlineSampleLifetime:
		out.StatsState = "stale"
	default:
		out.StatsState = "ok"
	}
	// A failed RPC does not erase a recent known snapshot or imply zero IPs.
	if known && now.Sub(s.IPStatsAt) <= onlineSampleLifetime && s.Running {
		out.OnlineIPs = ips
	}
	return out
}

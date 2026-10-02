package server

import (
	"fmt"
	"net/http"
	"strings"
	"time"
	"vibexui/internal/model"
)

type inboundExport struct {
	Format   string         `json:"format"`
	Node     model.Node     `json:"node"`
	Clients  []model.Client `json:"clients"`
	ServerID string         `json:"serverId,omitempty"`
}

func (s *Server) exportNode(w http.ResponseWriter, r *http.Request) {
	st, err := s.store.View()
	if err != nil {
		s.result(w, err, nil)
		return
	}
	for _, n := range st.Nodes {
		if n.ID != r.PathValue("id") {
			continue
		}
		out := inboundExport{Format: "vibexui-inbound-v1", Node: n, Clients: []model.Client{}}
		for _, c := range st.Clients {
			if model.Contains(c.NodeIDs, n.ID) {
				out.Clients = append(out.Clients, c)
			}
		}
		respond(w, 200, out)
		return
	}
	s.result(w, errNotFound, nil)
}
func clearClientUsage(c *model.Client, now time.Time) {
	c.Upload, c.Download = 0, 0
	c.QuotaBaseline = model.Traffic{}
	c.ServerTraffic = nil
	c.NodeIPStates = nil
	c.TrafficUpdatedAt = time.Time{}
	c.FirstUsedAt = time.Time{}
	c.RenewalCount = 0
	if c.FirstUseDays > 0 {
		c.ExpiresAt = time.Time{}
	}
	c.QuotaResetsAt = nextTrafficReset(now, c.TrafficReset, c.TrafficResetDay)
	if c.QuotaPeriodMonths > 0 {
		c.QuotaCycleAnchor = now.UTC()
		c.QuotaResetsAt = quotaCycleDate(c.QuotaCycleAnchor, c.QuotaPeriodMonths)
	}
	c.CreatedAt, c.UpdatedAt, c.LastOnlineAt = now, now, time.Time{}
	c.AccessState = model.ClientStatus(*c, now)
}
func (s *Server) importNode(w http.ResponseWriter, r *http.Request) {
	var in inboundExport
	if !decode(w, r, &in) {
		return
	}
	if in.Format != "vibexui-inbound-v1" || len(in.Clients) > 1000 {
		fail(w, 400, "入站配置格式无效或客户端超过 1000 个")
		return
	}
	var out model.Node
	err := s.store.Update(func(st *model.State) error {
		now := time.Now()
		out = in.Node
		originalID := out.ID
		out.ID = model.ID()
		out.ServerID = in.ServerID
		if serverAt(st, out.ServerID) == nil {
			return fmt.Errorf("请选择目标服务器")
		}
		if err := model.CheckSnellPort(*st, out.ServerID, out.Port); err != nil {
			return err
		}
		if len(st.Nodes) >= 1000 {
			return fmt.Errorf("入站数量已达上限")
		}
		if err := model.ValidateNode(out); err != nil {
			return err
		}
		for _, n := range st.Nodes {
			if n.ServerID == out.ServerID && n.Port == out.Port {
				return fmt.Errorf("目标服务器的监听端口已被占用")
			}
		}
		if out.PrivateKey == "" {
			private, public, err := model.Keys()
			if err != nil {
				return err
			}
			out.PrivateKey, out.PublicKey = private, public
		}
		key := out.PrivateKey
		if err := (nodeSettings{PrivateKey: &key, QuotaBytes: &out.QuotaBytes}).apply(&out, now); err != nil {
			return err
		}
		out.Upload, out.Download = 0, 0
		out.QuotaBaseline = model.Traffic{}
		out.TrafficUpdatedAt = time.Time{}
		out.QuotaResetsAt = nextTrafficReset(now, out.TrafficReset, out.TrafficResetDay)
		out.AccessState = model.NodeStatus(out, now)
		st.Nodes = append(st.Nodes, out)
		for _, incoming := range in.Clients {
			found := -1
			for i, c := range st.Clients {
				if strings.EqualFold(c.UUID, incoming.UUID) {
					found = i
					break
				}
			}
			if found >= 0 {
				c := &st.Clients[found]
				c.NodeIDs = append(c.NodeIDs, out.ID)
				if limit, ok := incoming.NodeIPLimits[originalID]; ok {
					if limit < 0 || limit > 128 {
						return fmt.Errorf("IP 上限无效")
					}
					if c.NodeIPLimits == nil {
						c.NodeIPLimits = map[string]int{}
					}
					c.NodeIPLimits[out.ID] = limit
				}
				if flow, ok := incoming.NodeFlows[originalID]; ok {
					if flow != "" && flow != "xtls-rprx-vision" {
						return fmt.Errorf("Flow 覆盖无效")
					}
					if c.NodeFlows == nil {
						c.NodeFlows = map[string]string{}
					}
					c.NodeFlows[out.ID] = flow
				}
				continue
			}
			if len(st.Clients) >= 5000 {
				return fmt.Errorf("用户数量已达上限")
			}
			c := incoming
			c.ID = model.ID()
			c.NodeIDs = []string{out.ID}
			c.NodeIPLimits = map[string]int{}
			if limit, ok := incoming.NodeIPLimits[originalID]; ok {
				if limit < 0 || limit > 128 {
					return fmt.Errorf("IP 上限无效")
				}
				c.NodeIPLimits[out.ID] = limit
			}
			c.NodeFlows = map[string]string{}
			if flow, ok := incoming.NodeFlows[originalID]; ok {
				if flow != "" && flow != "xtls-rprx-vision" {
					return fmt.Errorf("Flow 覆盖无效")
				}
				c.NodeFlows[out.ID] = flow
			}
			clearClientUsage(&c, now)
			if strings.TrimSpace(c.Name) == "" || len(c.Name) > 100 {
				return fmt.Errorf("客户端名称无效")
			}
			if c.QuotaBytes > 9007199254740991 || (c.QuotaPeriodMonths != 0 && c.QuotaPeriodMonths != 1 && c.QuotaPeriodMonths != 3 && c.QuotaPeriodMonths != 6 && c.QuotaPeriodMonths != 12) {
				return fmt.Errorf("客户端流量参数无效")
			}
			if c.Token == "" {
				c.Token = model.Secret()
			}
			if !tokenPattern.MatchString(c.Token) {
				return fmt.Errorf("订阅标识无效")
			}
			uuid, flow := c.UUID, model.ClientFlow(c)
			if err := (clientSettings{UUID: &uuid, Flow: &flow, LimitIP: &c.LimitIP, ResetDays: &c.ResetDays, ResetDay: &c.ResetDay, ResetWeekday: &c.ResetWeekday, ResetMax: &c.ResetMax, FirstUseDays: &c.FirstUseDays, ExternalLinks: &c.ExternalLinks}).apply(st, &c, now); err != nil {
				return err
			}
			st.Clients = append(st.Clients, c)
		}
		bump(st, out.ServerID)
		reconcileClients(st, now)
		return nil
	})
	out.PrivateKey = ""
	s.result(w, err, out)
}

package server

import (
	"fmt"
	"net/http"
	"strings"
	"time"
	"vibexui/internal/model"
)

func (s *Server) bulkClients(w http.ResponseWriter, r *http.Request) {
	var in struct {
		IDs     []string `json:"ids"`
		Action  string   `json:"action"`
		NodeIDs []string `json:"nodeIds"`
		Days    int      `json:"days"`
		Bytes   uint64   `json:"bytes"`
		Group   string   `json:"group"`
		Flow    string   `json:"flow"`
	}
	if !decode(w, r, &in) {
		return
	}
	if len(in.IDs) == 0 || len(in.IDs) > 5000 {
		fail(w, 400, "请选择客户端")
		return
	}
	now := time.Now()
	err := s.store.Update(func(st *model.State) error {
		selected := map[string]bool{}
		for _, id := range in.IDs {
			selected[id] = true
		}
		for _, c := range st.Clients {
			if selected[c.ID] {
				delete(selected, c.ID)
			}
		}
		if len(selected) > 0 {
			return errNotFound
		}
		for _, id := range in.IDs {
			selected[id] = true
		}
		nodes := map[string]bool{}
		for _, n := range st.Nodes {
			nodes[n.ID] = true
		}
		for _, id := range in.NodeIDs {
			if !nodes[id] {
				return fmt.Errorf("入站选择无效")
			}
		}
		if in.Days < 1 || in.Days > 3650 {
			if in.Action == "add-days" {
				return fmt.Errorf("增加天数须为 1–3650")
			}
		}
		if in.Bytes > 9007199254740991 {
			return fmt.Errorf("增加流量额度过大")
		}
		if in.Action == "flow" && in.Flow != "" && in.Flow != "xtls-rprx-vision" {
			return fmt.Errorf("Flow 无效")
		}
		if len(in.Group) > 100 {
			return fmt.Errorf("分组最多 100 字节")
		}
		affected := map[string]bool{}
		clients := []model.Client{}
		for _, c := range st.Clients {
			if !selected[c.ID] {
				clients = append(clients, c)
				continue
			}
			c.UpdatedAt = now
			oldNodes := append([]string(nil), c.NodeIDs...)
			switch in.Action {
			case "enable":
				c.Enabled = true
			case "disable":
				c.Enabled = false
			case "reset":
				c.QuotaBaseline = model.Traffic{Upload: c.Upload, Download: c.Download}
			case "delete":
			case "add-days":
				if c.FirstUseDays > 0 && c.FirstUsedAt.IsZero() {
					if c.FirstUseDays+in.Days > 3650 {
						return fmt.Errorf("首次使用有效期不能超过 3650 天")
					}
					c.FirstUseDays += in.Days
				} else {
					base := c.ExpiresAt
					if base.IsZero() || base.Before(now) {
						base = now
					}
					c.ExpiresAt = base.Add(time.Duration(in.Days) * 24 * time.Hour)
				}
			case "add-bytes":
				if c.QuotaBytes == 0 {
					return fmt.Errorf("不限流量客户端不能增加额度，请先设置限额")
				}
				if in.Bytes == 0 || in.Bytes > 9007199254740991-c.QuotaBytes {
					return fmt.Errorf("增加流量额度无效或超过上限")
				}
				c.QuotaBytes += in.Bytes
			case "group":
				c.Group = strings.TrimSpace(in.Group)
			case "flow":
				flow := in.Flow
				c.Flow = &flow
			case "attach":
				for _, id := range in.NodeIDs {
					if !model.Contains(c.NodeIDs, id) {
						c.NodeIDs = append(c.NodeIDs, id)
					}
				}
			case "detach":
				ids := []string{}
				for _, id := range c.NodeIDs {
					if !model.Contains(in.NodeIDs, id) {
						ids = append(ids, id)
					}
				}
				c.NodeIDs = ids
			default:
				return fmt.Errorf("批量操作无效")
			}
			for _, n := range st.Nodes {
				if model.Contains(oldNodes, n.ID) || model.Contains(c.NodeIDs, n.ID) {
					affected[n.ServerID] = true
				}
			}
			if in.Action != "delete" {
				clients = append(clients, c)
			}
		}
		for i := range clients {
			for id := range clients[i].NodeFlows {
				if !model.Contains(clients[i].NodeIDs, id) {
					delete(clients[i].NodeFlows, id)
				}
			}
		}
		st.Clients = clients
		for sid := range affected {
			bump(st, sid)
		}
		reconcileClients(st, now)
		return nil
	})
	s.result(w, err, map[string]bool{"ok": true})
}
func (s *Server) batchCreateClients(w http.ResponseWriter, r *http.Request) {
	var in struct {
		TemplateID string   `json:"templateId"`
		Prefix     string   `json:"prefix"`
		Count      int      `json:"count"`
		NodeIDs    []string `json:"nodeIds"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Count < 1 || in.Count > 100 || len(in.Prefix) > 80 || strings.TrimSpace(in.Prefix) == "" {
		fail(w, 400, "数量须为 1–100，名称前缀须为 1–80 字节")
		return
	}
	created := []model.Client{}
	err := s.store.Update(func(st *model.State) error {
		if len(st.Clients)+in.Count > 5000 {
			return fmt.Errorf("用户数量已达上限")
		}
		template := model.Client{Enabled: true}
		if in.TemplateID != "" {
			found := false
			for _, c := range st.Clients {
				if c.ID == in.TemplateID {
					template = c
					found = true
					break
				}
			}
			if !found {
				return errNotFound
			}
		}
		ids := template.NodeIDs
		if in.NodeIDs != nil {
			ids = in.NodeIDs
		}
		seen := map[string]bool{}
		affected := map[string]bool{}
		for _, id := range ids {
			found := false
			for _, n := range st.Nodes {
				if n.ID == id {
					found = true
					affected[n.ServerID] = true
				}
			}
			if !found || seen[id] {
				return fmt.Errorf("入站选择无效")
			}
			seen[id] = true
		}
		for i := 0; i < in.Count; i++ {
			c := template
			c.ID, c.UUID, c.Token = model.ID(), model.UUID(), model.Secret()
			c.CreatedAt, c.UpdatedAt = time.Now(), time.Now()
			c.LastOnlineAt = time.Time{}
			c.Name = fmt.Sprintf("%s-%03d", strings.TrimSpace(in.Prefix), i+1)
			c.Email = c.Name
			c.NodeIDs = append([]string{}, ids...)
			c.NodeFlows = map[string]string{}
			c.NodeIPLimits = map[string]int{}
			for _, id := range ids {
				if flow, ok := template.NodeFlows[id]; ok {
					c.NodeFlows[id] = flow
				}
			}
			for _, id := range ids {
				if _, ok := template.NodeIPLimits[id]; ok {
					c.NodeIPLimits[id] = template.NodeIPLimits[id]
				}
			}
			c.NodeIPStates = map[string]model.NodeIPState{}
			c.Upload, c.Download = 0, 0
			c.QuotaBaseline = model.Traffic{}
			c.ServerTraffic = nil
			c.TrafficUpdatedAt = time.Time{}
			c.FirstUsedAt = time.Time{}
			c.RenewalCount = 0
			c.TelegramID = ""
			if c.FirstUseDays > 0 {
				c.ExpiresAt = time.Time{}
			}
			now := time.Now()
			c.QuotaResetsAt = nextTrafficReset(now, c.TrafficReset, c.TrafficResetDay)
			if c.QuotaPeriodMonths > 0 {
				c.QuotaCycleAnchor = now.UTC()
				c.QuotaResetsAt = quotaCycleDate(c.QuotaCycleAnchor, c.QuotaPeriodMonths)
			}
			if err := (clientSettings{}).apply(st, &c, now); err != nil {
				return err
			}
			c.AccessState = model.ClientStatus(c, now)
			st.Clients = append(st.Clients, c)
			created = append(created, c)
		}
		for sid := range affected {
			bump(st, sid)
		}
		reconcileClients(st, time.Now())
		return nil
	})
	s.result(w, err, created)
}
func (s *Server) bulkNodes(w http.ResponseWriter, r *http.Request) {
	var in struct {
		IDs    []string `json:"ids"`
		Action string   `json:"action"`
	}
	if !decode(w, r, &in) {
		return
	}
	if len(in.IDs) == 0 || len(in.IDs) > 1000 {
		fail(w, 400, "请选择入站")
		return
	}
	err := s.store.Update(func(st *model.State) error {
		selected := map[string]bool{}
		for _, id := range in.IDs {
			selected[id] = true
		}
		for _, n := range st.Nodes {
			if selected[n.ID] {
				delete(selected, n.ID)
			}
		}
		if len(selected) > 0 {
			return errNotFound
		}
		for _, id := range in.IDs {
			selected[id] = true
		}
		affected := map[string]bool{}
		nodes := []model.Node{}
		for _, n := range st.Nodes {
			if !selected[n.ID] {
				nodes = append(nodes, n)
				continue
			}
			affected[n.ServerID] = true
			switch in.Action {
			case "enable":
				n.Enabled = true
			case "disable":
				n.Enabled = false
			case "reset":
				n.QuotaBaseline = model.Traffic{Upload: n.Upload, Download: n.Download}
			case "delete":
			default:
				return fmt.Errorf("批量操作无效")
			}
			if in.Action != "delete" {
				nodes = append(nodes, n)
			}
		}
		st.Nodes = nodes
		cleanAssignments(st)
		for sid := range affected {
			bump(st, sid)
		}
		reconcileClients(st, time.Now())
		return nil
	})
	s.result(w, err, map[string]bool{"ok": true})
}

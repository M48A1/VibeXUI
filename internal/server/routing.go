package server

import (
	"fmt"
	"net/http"
	"strings"
	"time"
	"vibexui/internal/model"
)

func (s *Server) saveOutbound(w http.ResponseWriter, r *http.Request) {
	var in model.Outbound
	if !decode(w, r, &in) {
		return
	}
	err := s.store.Update(func(st *model.State) error {
		if serverAt(st, in.ServerID) == nil {
			return errNotFound
		}
		idx := -1
		if id := r.PathValue("id"); id != "" {
			for i, o := range st.Outbounds {
				if o.ID == id {
					idx = i
					break
				}
			}
			if idx < 0 {
				return errNotFound
			}
			old := st.Outbounds[idx]
			if old.ServerID != in.ServerID {
				return fmt.Errorf("不能更改出口所属服务器")
			}
			in.ID = id
			if old.Protocol == in.Protocol {
				if in.Password == "" {
					in.Password = old.Password
				}
				if in.UUID == "" {
					in.UUID = old.UUID
				}
			}
		} else {
			in.ID = model.ID()
		}
		in.Name = strings.TrimSpace(in.Name)
		in.Address = strings.TrimSpace(in.Address)
		if in.Protocol == "shadowsocks" {
			in.UUID = ""
			in.Flow = ""
			in.Security = ""
			in.ServerName = ""
			in.PublicKey = ""
			in.ShortID = ""
			in.Fingerprint = ""
		} else {
			in.Password = ""
			in.Method = ""
		}
		if idx < 0 {
			st.Outbounds = append(st.Outbounds, in)
		} else {
			st.Outbounds[idx] = in
		}
		if err := model.ValidateRouting(*st, in.ServerID); err != nil {
			return err
		}
		bump(st, in.ServerID)
		return nil
	})
	s.result(w, err, map[string]string{"id": in.ID})
}
func (s *Server) deleteOutbound(w http.ResponseWriter, r *http.Request) {
	err := s.store.Update(func(st *model.State) error {
		for i, o := range st.Outbounds {
			if o.ID == r.PathValue("id") {
				st.Outbounds = append(st.Outbounds[:i], st.Outbounds[i+1:]...)
				if err := model.ValidateRouting(*st, o.ServerID); err != nil {
					return fmt.Errorf("出口仍被默认出口或分流规则引用：%w", err)
				}
				bump(st, o.ServerID)
				return nil
			}
		}
		return errNotFound
	})
	s.result(w, err, map[string]bool{"ok": true})
}
func (s *Server) outboundFromNode(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ServerID string `json:"serverId"`
		NodeID   string `json:"nodeId"`
		ClientID string `json:"clientId"`
		Name     string `json:"name"`
	}
	if !decode(w, r, &in) {
		return
	}
	id := model.ID()
	err := s.store.Update(func(st *model.State) error {
		if serverAt(st, in.ServerID) == nil {
			return errNotFound
		}
		for _, n := range st.Nodes {
			if n.ID != in.NodeID {
				continue
			}
			if n.ServerID == in.ServerID {
				return fmt.Errorf("请选择另一台服务器的入站")
			}
			if model.NodeStatus(n, time.Now()) != "active" {
				return fmt.Errorf("落地入站未启用或已受限")
			}
			target := serverAt(st, n.ServerID)
			if target == nil {
				return errNotFound
			}
			for _, c := range st.Clients {
				if c.ID != in.ClientID {
					continue
				}
				if !model.Contains(c.NodeIDs, n.ID) || !model.ClientNodeActive(c, n.ID, time.Now()) {
					return fmt.Errorf("请选择已分配此入站且可用的客户端")
				}
				name := strings.TrimSpace(in.Name)
				if name == "" {
					name = target.Name + " / " + n.Name
				}
				o := model.Outbound{ID: id, ServerID: in.ServerID, Name: name, Enabled: true, Protocol: "vless", Address: target.Host, Port: n.Port, UUID: c.UUID, Flow: model.ClientNodeFlow(c, n.ID), Security: "reality", ServerName: n.SNI, PublicKey: n.PublicKey, ShortID: n.ShortID, Fingerprint: "chrome"}
				st.Outbounds = append(st.Outbounds, o)
				if err := model.ValidateRouting(*st, in.ServerID); err != nil {
					return err
				}
				bump(st, in.ServerID)
				return nil
			}
			return errNotFound
		}
		return errNotFound
	})
	s.result(w, err, map[string]string{"id": id})
}
func (s *Server) saveRouteRule(w http.ResponseWriter, r *http.Request) {
	var in model.RouteRule
	if !decode(w, r, &in) {
		return
	}
	err := s.store.Update(func(st *model.State) error {
		if serverAt(st, in.ServerID) == nil {
			return errNotFound
		}
		idx := -1
		if id := r.PathValue("id"); id != "" {
			for i, v := range st.Rules {
				if v.ID == id {
					idx = i
					break
				}
			}
			if idx < 0 {
				return errNotFound
			}
			if st.Rules[idx].ServerID != in.ServerID {
				return fmt.Errorf("不能更改规则所属服务器")
			}
			in.ID = id
		} else {
			in.ID = model.ID()
		}
		in.Name = strings.TrimSpace(in.Name)
		in.Port = strings.TrimSpace(in.Port)
		if idx < 0 {
			st.Rules = append(st.Rules, in)
		} else {
			st.Rules[idx] = in
		}
		if err := model.ValidateRouting(*st, in.ServerID); err != nil {
			return err
		}
		bump(st, in.ServerID)
		return nil
	})
	s.result(w, err, map[string]string{"id": in.ID})
}
func (s *Server) deleteRouteRule(w http.ResponseWriter, r *http.Request) {
	err := s.store.Update(func(st *model.State) error {
		for i, v := range st.Rules {
			if v.ID == r.PathValue("id") {
				st.Rules = append(st.Rules[:i], st.Rules[i+1:]...)
				bump(st, v.ServerID)
				return nil
			}
		}
		return errNotFound
	})
	s.result(w, err, map[string]bool{"ok": true})
}
func (s *Server) saveRouting(w http.ResponseWriter, r *http.Request) {
	var in model.RoutingSettings
	if !decode(w, r, &in) {
		return
	}
	id := r.PathValue("id")
	err := s.store.Update(func(st *model.State) error {
		v := serverAt(st, id)
		if v == nil {
			return errNotFound
		}
		v.Routing = model.RoutingDefaults(in)
		if err := model.ValidateRouting(*st, id); err != nil {
			return err
		}
		bump(st, id)
		return nil
	})
	s.result(w, err, map[string]bool{"ok": true})
}
func (s *Server) orderRouteRules(w http.ResponseWriter, r *http.Request) {
	var in struct {
		IDs []string `json:"ids"`
	}
	if !decode(w, r, &in) {
		return
	}
	id := r.PathValue("id")
	err := s.store.Update(func(st *model.State) error {
		if serverAt(st, id) == nil {
			return errNotFound
		}
		rules := map[string]model.RouteRule{}
		for _, v := range st.Rules {
			if v.ServerID == id {
				rules[v.ID] = v
			}
		}
		if len(in.IDs) != len(rules) {
			return fmt.Errorf("规则列表已变化，请刷新后重试")
		}
		ordered := []model.RouteRule{}
		for _, rid := range in.IDs {
			v, ok := rules[rid]
			if !ok {
				return fmt.Errorf("排序包含重复或其他服务器的规则")
			}
			ordered = append(ordered, v)
			delete(rules, rid)
		}
		i := 0
		for j := range st.Rules {
			if st.Rules[j].ServerID == id {
				st.Rules[j] = ordered[i]
				i++
			}
		}
		bump(st, id)
		return nil
	})
	s.result(w, err, map[string]bool{"ok": true})
}

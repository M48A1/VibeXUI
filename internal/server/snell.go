package server

import (
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"vibexui/internal/model"
)

func (s *Server) saveSnell(w http.ResponseWriter, r *http.Request) {
	var n model.SnellInbound
	if !decode(w, r, &n) {
		return
	}
	err := s.store.Update(func(st *model.State) error {
		srv := serverAt(st, n.ServerID)
		if srv == nil {
			return errNotFound
		}
		if !srv.SnellSupported {
			return fmt.Errorf("请先升级此服务器的 Agent 以支持 Snell")
		}
		idx := -1
		if id := r.PathValue("id"); id != "" {
			for i, v := range st.Snell {
				if v.ID == id {
					idx = i
					break
				}
			}
			if idx < 0 {
				return errNotFound
			}
			old := st.Snell[idx]
			if old.ServerID != n.ServerID {
				return fmt.Errorf("不能迁移 Snell 入站")
			}
			n.ID = id
			n.Revision = old.Revision + 1
			if n.PSK == "" {
				n.PSK = old.PSK
			}
		} else {
			n.ID = model.ID()
			n.Revision = 1
			if n.PSK == "" {
				n.PSK = model.Secret()
			}
		}
		n.Name = strings.TrimSpace(n.Name)
		if n.Listen == "" {
			n.Listen = "0.0.0.0"
		}
		if err := model.ValidateSnell(n); err != nil {
			return err
		}
		count := 0
		for _, v := range st.Snell {
			if v.ServerID == n.ServerID {
				count++
				if v.ID != n.ID && v.Port == n.Port {
					return fmt.Errorf("端口已被 Snell 入站占用")
				}
			}
		}
		if idx < 0 && count >= 16 {
			return fmt.Errorf("每台服务器最多 16 个 Snell 入站")
		}
		for _, v := range st.Nodes {
			if v.ServerID == n.ServerID && v.Port == n.Port {
				return fmt.Errorf("端口已被 Xray 入站占用")
			}
		}
		if idx < 0 {
			st.Snell = append(st.Snell, n)
		} else {
			st.Snell[idx] = n
		}
		return nil
	})
	s.result(w, err, map[string]string{"id": n.ID})
}
func (s *Server) deleteSnell(w http.ResponseWriter, r *http.Request) {
	err := s.store.Update(func(st *model.State) error {
		for i, n := range st.Snell {
			if n.ID == r.PathValue("id") {
				st.Snell = append(st.Snell[:i], st.Snell[i+1:]...)
				return nil
			}
		}
		return errNotFound
	})
	s.result(w, err, map[string]bool{"ok": true})
}
func (s *Server) exportSnell(w http.ResponseWriter, r *http.Request) {
	st, ok := s.view(w)
	if !ok {
		return
	}
	for _, n := range st.Snell {
		if n.ID == r.PathValue("id") {
			srv := serverAt(&st, n.ServerID)
			if srv == nil {
				s.result(w, errNotFound, nil)
				return
			}
			host := srv.Host
			if net.ParseIP(host) != nil && strings.Contains(host, ":") {
				host = "[" + strings.Trim(host, "[]") + "]"
			}
			respond(w, 200, map[string]string{"config": "Snell-" + n.ID + " = snell, " + host + ", " + strconv.Itoa(n.Port) + ", psk=" + n.PSK + ", version=5, tfo=" + strconv.FormatBool(n.TFO)})
			return
		}
	}
	s.result(w, errNotFound, nil)
}

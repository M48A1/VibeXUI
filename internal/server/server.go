package server

import (
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/skip2/go-qrcode"
	"golang.org/x/crypto/bcrypt"
	"vibexui/internal/model"
	"vibexui/internal/store"
	"vibexui/internal/web"
)

type Options struct{ Username, Password, PublicURL, DownloadsDir string }
type Server struct {
	telegram                      *telegramNotifier
	credentialVersion             uint64
	credentialAttempts            []time.Time
	store                         *store.Store
	username, password, publicURL string
	secure                        bool
	downloads                     string
	mu                            sync.Mutex
	sessions                      map[string]time.Time
	attempts                      []time.Time
	handler                       http.Handler
}

var errNotFound = errors.New("记录不存在")

func New(db *store.Store, o Options) (*Server, error) {
	u, err := url.Parse(o.PublicURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return nil, fmt.Errorf("public-url 须为 http(s)://域名[:端口]，不含路径")
	}
	s := &Server{store: db, publicURL: strings.TrimRight(o.PublicURL, "/"), secure: u.Scheme == "https", sessions: map[string]time.Time{}}
	s.downloads = o.DownloadsDir
	s.telegram = newTelegram(db, s.publicURL)
	s.username, err = db.Setting("username")
	if err != nil {
		return nil, err
	}
	s.password, err = db.Setting("password")
	if err != nil {
		return nil, err
	}
	if s.password == "" {
		if o.Username == "" {
			o.Username = "admin"
		}
		if len(o.Password) < 12 || len(o.Password) > 72 {
			return nil, fmt.Errorf("首次启动须通过 VIBEXUI_PASSWORD 提供 12–72 字节密码")
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(o.Password), bcrypt.DefaultCost)
		if err != nil {
			return nil, err
		}
		if err = db.SetCredentials(o.Username, string(hash)); err != nil {
			return nil, err
		}
		s.username = o.Username
		s.password = string(hash)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/login", s.login)
	mux.HandleFunc("POST /api/logout", s.admin(s.logout))
	mux.HandleFunc("GET /api/me", s.admin(s.accountInfo))
	mux.HandleFunc("PUT /api/settings/account", s.admin(s.updateAccount))
	mux.HandleFunc("GET /api/state", s.admin(s.state))
	mux.HandleFunc("GET /api/notifications/telegram", s.admin(s.telegramGet))
	mux.HandleFunc("PUT /api/notifications/telegram", s.admin(s.telegramSave))
	mux.HandleFunc("POST /api/notifications/telegram/test", s.admin(s.telegramTest))
	mux.HandleFunc("POST /api/servers", s.admin(s.createServer))
	mux.HandleFunc("PATCH /api/servers/{id}", s.admin(s.editServer))
	mux.HandleFunc("DELETE /api/servers/{id}", s.admin(s.deleteServer))
	mux.HandleFunc("POST /api/servers/{id}/registration", s.admin(s.registration))
	mux.HandleFunc("POST /api/nodes", s.admin(s.saveNode))
	mux.HandleFunc("PATCH /api/nodes/{id}", s.admin(s.saveNode))
	mux.HandleFunc("DELETE /api/nodes/{id}", s.admin(s.deleteNode))
	mux.HandleFunc("POST /api/clients", s.admin(s.saveClient))
	mux.HandleFunc("PATCH /api/clients/{id}", s.admin(s.saveClient))
	mux.HandleFunc("DELETE /api/clients/{id}", s.admin(s.deleteClient))
	mux.HandleFunc("POST /api/clients/bulk", s.admin(s.bulkClients))
	mux.HandleFunc("POST /api/clients/renewal-preview", s.admin(s.previewRenewal))
	mux.HandleFunc("POST /api/clients/batch", s.admin(s.batchCreateClients))
	mux.HandleFunc("GET /api/nodes/{id}/export", s.admin(s.exportNode))
	mux.HandleFunc("POST /api/nodes/import", s.admin(s.importNode))
	mux.HandleFunc("POST /api/nodes/keys", s.admin(s.nodeKeys))
	mux.HandleFunc("POST /api/nodes/bulk", s.admin(s.bulkNodes))
	mux.HandleFunc("POST /api/nodes/{id}/reset-quota", s.admin(s.resetNodeQuota))
	mux.HandleFunc("POST /api/clients/{id}/reset-quota", s.admin(s.resetQuota))
	mux.HandleFunc("POST /api/clients/{id}/clear-ips", s.admin(s.clearClientIPs))
	mux.HandleFunc("GET /api/clients/{id}/links", s.admin(s.links))
	mux.HandleFunc("GET /api/clients/{id}/qr", s.admin(s.qr))
	mux.HandleFunc("GET /api/backup", s.admin(s.backup))
	mux.HandleFunc("POST /api/agent/register", s.register)
	mux.HandleFunc("POST /api/agent/poll", s.poll)
	mux.HandleFunc("GET /install/agent.sh", s.installer)
	mux.HandleFunc("GET /api/agent/download/{arch}", s.downloadAgent)
	mux.HandleFunc("GET /api/agent/download/{arch}/sha256", s.downloadAgent)
	mux.HandleFunc("GET /sub/{token}", s.subscription)
	mux.Handle("/", web.Handler())
	s.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		w.Header().Set("Cache-Control", "no-store")
		mux.ServeHTTP(w, r)
	})
	return s, nil
}
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.handler.ServeHTTP(w, r) }
func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, status int, msg string) {
	respond(w, status, map[string]string{"error": msg})
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		fail(w, 400, "请求内容无效")
		return false
	}
	if err := d.Decode(&struct{}{}); err != io.EOF {
		fail(w, 400, "请求包含多余内容")
		return false
	}
	return true
}
func mutationOK(w http.ResponseWriter, r *http.Request, origin string) bool {
	if r.Header.Get("X-VibeXUI") != "1" || (r.Header.Get("Origin") != "" && r.Header.Get("Origin") != origin) {
		fail(w, 403, "请求来源无效")
		return false
	}
	return true
}
func (s *Server) admin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && !mutationOK(w, r, s.publicURL) {
			return
		}
		c, err := r.Cookie("vibexui_session")
		if err != nil {
			fail(w, 401, "请先登录")
			return
		}
		s.mu.Lock()
		expiry, ok := s.sessions[model.Hash(c.Value)]
		s.mu.Unlock()
		if !ok || time.Now().After(expiry) {
			fail(w, 401, "登录已过期")
			return
		}
		next(w, r)
	}
}
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if !mutationOK(w, r, s.publicURL) {
		return
	}
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decode(w, r, &in) {
		return
	}
	s.mu.Lock()
	now := time.Now()
	recent := s.attempts[:0]
	for _, t := range s.attempts {
		if now.Sub(t) < time.Minute {
			recent = append(recent, t)
		}
	}
	s.attempts = recent
	if len(recent) >= 10 {
		s.mu.Unlock()
		fail(w, 429, "登录尝试过多，请稍后重试")
		return
	}
	s.attempts = append(s.attempts, now)
	username, password, version := s.username, s.password, s.credentialVersion
	s.mu.Unlock()
	err := bcrypt.CompareHashAndPassword([]byte(password), []byte(in.Password))
	if subtle.ConstantTimeCompare([]byte(in.Username), []byte(username)) != 1 || err != nil {
		s.notifyLogin(r, in.Username, false)
		fail(w, 401, "用户名或密码错误")
		return
	}
	token := model.Secret()
	s.mu.Lock()
	if s.credentialVersion != version {
		s.mu.Unlock()
		fail(w, 401, "账号已更新，请使用新账号密码登录")
		return
	}
	for k, v := range s.sessions {
		if now.After(v) {
			delete(s.sessions, k)
		}
	}
	if len(s.sessions) >= 100 {
		s.mu.Unlock()
		fail(w, 429, "会话数量已达上限")
		return
	}
	s.sessions[model.Hash(token)] = now.Add(12 * time.Hour)
	s.mu.Unlock()
	s.notifyLogin(r, in.Username, true)
	http.SetCookie(w, &http.Cookie{Name: "vibexui_session", Value: token, Path: "/", HttpOnly: true, Secure: s.secure, SameSite: http.SameSiteStrictMode, MaxAge: 43200})
	respond(w, 200, map[string]bool{"ok": true})
}
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	c, _ := r.Cookie("vibexui_session")
	if c != nil {
		s.mu.Lock()
		delete(s.sessions, model.Hash(c.Value))
		s.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: "vibexui_session", Value: "", Path: "/", HttpOnly: true, Secure: s.secure, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	respond(w, 200, map[string]bool{"ok": true})
}
func (s *Server) result(w http.ResponseWriter, err error, v any) {
	if err != nil {
		if errors.Is(err, errNotFound) {
			fail(w, 404, err.Error())
		} else {
			fail(w, 400, err.Error())
		}
		return
	}
	respond(w, 200, v)
}
func (s *Server) view(w http.ResponseWriter) (model.State, bool) {
	st, err := s.store.View()
	if err != nil {
		log.Printf("database: %v", err)
		fail(w, 500, "数据库读取失败")
		return st, false
	}
	return st, true
}
func redact(st *model.State, keys bool) {
	for i := range st.Servers {
		st.Servers[i].TokenHash = ""
		st.Servers[i].RegistrationHash = ""
	}
	if keys {
		for i := range st.Nodes {
			st.Nodes[i].PrivateKey = ""
		}
	}
}
func (s *Server) state(w http.ResponseWriter, r *http.Request) {
	st, ok := s.view(w)
	if !ok {
		return
	}
	for i := range st.Servers {
		v := &st.Servers[i]
		switch {
		case v.TokenHash != "":
			v.RegistrationState = "registered"
		case v.RegistrationHash != "" && time.Now().Before(v.RegistrationExpires):
			v.RegistrationState = "pending"
		default:
			v.RegistrationState = "expired"
		}
	}
	redact(&st, true)
	respond(w, 200, st)
}
func (s *Server) backup(w http.ResponseWriter, r *http.Request) {
	st, ok := s.view(w)
	if !ok {
		return
	}
	redact(&st, false)
	w.Header().Set("Content-Disposition", `attachment; filename="vibexui-config-backup.json"`)
	respond(w, 200, st)
}
func (s *Server) createServer(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
		Host string `json:"host"`
	}
	if !decode(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.Name) == "" || len(in.Name) > 100 || !model.ValidHost(in.Host) {
		fail(w, 400, "请填写服务器名称和有效公网 IP 或域名")
		return
	}
	token := model.Secret()
	v := model.Server{ID: model.ID(), Name: in.Name, Host: in.Host, RegistrationHash: model.Hash(token), RegistrationExpires: time.Now().Add(30 * time.Minute), Version: 1, DesiredRunning: true}
	err := s.store.Update(func(st *model.State) error {
		if len(st.Servers) >= 100 {
			return fmt.Errorf("最多支持 100 台服务器")
		}
		st.Servers = append(st.Servers, v)
		return nil
	})
	s.result(w, err, s.registrationResult(v.ID, token))
}
func serverAt(st *model.State, id string) *model.Server {
	for i := range st.Servers {
		if st.Servers[i].ID == id {
			return &st.Servers[i]
		}
	}
	return nil
}
func bump(st *model.State, id string) {
	if v := serverAt(st, id); v != nil {
		v.Version++
	}
}
func (s *Server) editServer(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name   *string `json:"name"`
		Host   *string `json:"host"`
		Action string  `json:"action"`
	}
	if !decode(w, r, &in) {
		return
	}
	err := s.store.Update(func(st *model.State) error {
		v := serverAt(st, r.PathValue("id"))
		if v == nil {
			return errNotFound
		}
		if in.Name != nil {
			if strings.TrimSpace(*in.Name) == "" || len(*in.Name) > 100 {
				return fmt.Errorf("服务器名称无效")
			}
			v.Name = *in.Name
		}
		if in.Host != nil {
			if !model.ValidHost(*in.Host) {
				return fmt.Errorf("公网地址无效")
			}
			v.Host = *in.Host
		}
		switch in.Action {
		case "":
		case "start":
			v.DesiredRunning = true
		case "stop":
			v.DesiredRunning = false
		case "restart":
			v.DesiredRunning = true
			v.Version++
		default:
			return fmt.Errorf("操作无效")
		}
		return nil
	})
	s.result(w, err, map[string]bool{"ok": true})
}
func (s *Server) registration(w http.ResponseWriter, r *http.Request) {
	token := model.Secret()
	err := s.store.Update(func(st *model.State) error {
		v := serverAt(st, r.PathValue("id"))
		if v == nil {
			return errNotFound
		}
		v.TokenHash = ""
		v.RegistrationHash = model.Hash(token)
		v.RegistrationExpires = time.Now().Add(30 * time.Minute)
		v.LastSeen = time.Time{}
		return nil
	})
	s.result(w, err, s.registrationResult(r.PathValue("id"), token))
}
func (s *Server) deleteServer(w http.ResponseWriter, r *http.Request) {
	err := s.store.Update(func(st *model.State) error {
		if serverAt(st, r.PathValue("id")) == nil {
			return errNotFound
		}
		servers := []model.Server{}
		for _, v := range st.Servers {
			if v.ID != r.PathValue("id") {
				servers = append(servers, v)
			}
		}
		st.Servers = servers
		nodes := []model.Node{}
		for _, v := range st.Nodes {
			if v.ServerID != r.PathValue("id") {
				nodes = append(nodes, v)
			}
		}
		st.Nodes = nodes
		cleanAssignments(st)
		return nil
	})
	s.result(w, err, map[string]bool{"ok": true})
}
func cleanAssignments(st *model.State) {
	for i := range st.Clients {
		ids := []string{}
		for _, id := range st.Clients[i].NodeIDs {
			for _, n := range st.Nodes {
				if n.ID == id {
					ids = append(ids, id)
					break
				}
			}
		}
		st.Clients[i].NodeIDs = ids
		for id := range st.Clients[i].NodeIPLimits {
			if !model.Contains(ids, id) {
				delete(st.Clients[i].NodeIPLimits, id)
			}
		}
		for id := range st.Clients[i].NodeIPStates {
			if !model.Contains(ids, id) {
				delete(st.Clients[i].NodeIPStates, id)
			}
		}
		for id := range st.Clients[i].NodeFlows {
			if !model.Contains(ids, id) {
				delete(st.Clients[i].NodeFlows, id)
			}
		}
	}
}
func (s *Server) saveNode(w http.ResponseWriter, r *http.Request) {
	var in struct {
		nodeSettings
		SourceNodeID string `json:"sourceNodeId"`
		ServerID     string `json:"serverId"`
		Name         string `json:"name"`
		Port         int    `json:"port"`
		SNI          string `json:"sni"`
		Target       string `json:"target"`
		Enabled      bool   `json:"enabled"`
	}
	if !decode(w, r, &in) {
		return
	}
	n := model.Node{ServerID: in.ServerID, Name: in.Name, Port: in.Port, SNI: in.SNI, Target: in.Target, Enabled: in.Enabled}
	if err := model.ValidateNode(n); err != nil {
		fail(w, 400, err.Error())
		return
	}
	err := s.store.Update(func(st *model.State) error {
		if serverAt(st, n.ServerID) == nil {
			return fmt.Errorf("请先选择服务器")
		}
		id := r.PathValue("id")
		if in.SourceNodeID != "" {
			if id != "" {
				return fmt.Errorf("只有新增入站可以使用复制源")
			}
			found := false
			for _, source := range st.Nodes {
				if source.ID == in.SourceNodeID {
					found = true
					break
				}
			}
			if !found {
				return errNotFound
			}
		}
		for _, v := range st.Nodes {
			if v.ID != id && v.ServerID == n.ServerID && v.Port == n.Port {
				return fmt.Errorf("同一服务器上的端口已被占用")
			}
		}
		if id != "" {
			for i, v := range st.Nodes {
				if v.ID == id {
					if v.ServerID != n.ServerID {
						return fmt.Errorf("修改节点不能迁移服务器")
					}
					base := n
					n = v
					n.ServerID, n.Name, n.Port, n.SNI, n.Target, n.Enabled = base.ServerID, base.Name, base.Port, base.SNI, base.Target, base.Enabled
					if err := in.nodeSettings.apply(&n, time.Now()); err != nil {
						return err
					}
					st.Nodes[i] = n
					bump(st, n.ServerID)
					return nil
				}
			}
			return errNotFound
		}
		if len(st.Nodes) >= 1000 {
			return fmt.Errorf("节点数量已达上限")
		}
		var err error
		n.ID = model.ID()
		n.PrivateKey, n.PublicKey, err = model.Keys()
		if err != nil {
			return err
		}
		n.ShortID = model.Hash(model.Secret())[:16]
		if err := in.nodeSettings.apply(&n, time.Now()); err != nil {
			return err
		}
		st.Nodes = append(st.Nodes, n)
		if in.SourceNodeID != "" {
			for i := range st.Clients {
				c := &st.Clients[i]
				if !model.Contains(c.NodeIDs, in.SourceNodeID) {
					continue
				}
				c.NodeIDs = append(c.NodeIDs, n.ID)
				if value, ok := c.NodeIPLimits[in.SourceNodeID]; ok {
					if c.NodeIPLimits == nil {
						c.NodeIPLimits = map[string]int{}
					}
					c.NodeIPLimits[n.ID] = value
				}
				if value, ok := c.NodeFlows[in.SourceNodeID]; ok {
					if c.NodeFlows == nil {
						c.NodeFlows = map[string]string{}
					}
					c.NodeFlows[n.ID] = value
				}
			}
		}
		bump(st, n.ServerID)
		return nil
	})
	n.PrivateKey = ""
	s.result(w, err, n)
}
func (s *Server) deleteNode(w http.ResponseWriter, r *http.Request) {
	err := s.store.Update(func(st *model.State) error {
		for i, n := range st.Nodes {
			if n.ID == r.PathValue("id") {
				bump(st, n.ServerID)
				st.Nodes = append(st.Nodes[:i], st.Nodes[i+1:]...)
				cleanAssignments(st)
				return nil
			}
		}
		return errNotFound
	})
	s.result(w, err, map[string]bool{"ok": true})
}
func (s *Server) saveClient(w http.ResponseWriter, r *http.Request) {
	var in struct {
		clientSettings
		Name              string         `json:"name"`
		Enabled           bool           `json:"enabled"`
		NodeIDs           []string       `json:"nodeIds"`
		Rotate            bool           `json:"rotate"`
		QuotaPeriodMonths *int           `json:"quotaPeriodMonths"`
		QuotaBytes        *uint64        `json:"quotaBytes"`
		ExpiresAt         *time.Time     `json:"expiresAt"`
		NodeIPLimits      map[string]int `json:"nodeIPLimits"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.NodeIDs == nil {
		in.NodeIDs = []string{}
	}
	if strings.TrimSpace(in.Name) == "" || len(in.Name) > 100 {
		fail(w, 400, "用户名称无效")
		return
	}
	var out model.Client
	err := s.store.Update(func(st *model.State) error {
		seen := map[string]bool{}
		for _, id := range in.NodeIDs {
			exists := false
			for _, n := range st.Nodes {
				if n.ID == id {
					exists = true
				}
			}
			if !exists || seen[id] {
				return fmt.Errorf("节点选择无效")
			}
			seen[id] = true
		}
		id := r.PathValue("id")
		index := -1
		for i, c := range st.Clients {
			if c.ID == id {
				index = i
				out = c
				break
			}
		}
		if id != "" && index < 0 {
			return errNotFound
		}
		if index < 0 {
			if len(st.Clients) >= 5000 {
				return fmt.Errorf("用户数量已达上限")
			}
			out = model.Client{ID: model.ID(), UUID: model.UUID(), Token: model.Secret(), CreatedAt: time.Now()}
		}
		old := out.NodeIDs
		if in.NodeIPLimits != nil {
			for nid, limit := range in.NodeIPLimits {
				if !model.Contains(in.NodeIDs, nid) || limit < 0 || limit > 128 {
					return fmt.Errorf("在线 IP 上限须为 0–128，且对应已分配入站")
				}
			}
			for nid, limit := range in.NodeIPLimits {
				if limit != out.NodeIPLimits[nid] && out.NodeIPStates != nil {
					value := out.NodeIPStates[nid]
					value.BlockedUntil = time.Time{}
					out.NodeIPStates[nid] = value
				}
			}
			out.NodeIPLimits = in.NodeIPLimits
		}
		for nid := range out.NodeIPLimits {
			if !model.Contains(in.NodeIDs, nid) {
				delete(out.NodeIPLimits, nid)
			}
		}

		out.UpdatedAt = time.Now()
		out.Name = in.Name
		out.Enabled = in.Enabled
		if in.QuotaBytes != nil {
			if *in.QuotaBytes > 9007199254740991 {
				return fmt.Errorf("流量额度过大")
			}
			out.QuotaBytes = *in.QuotaBytes
		}
		if in.QuotaPeriodMonths != nil {
			months := *in.QuotaPeriodMonths
			if months != 0 && months != 1 && months != 3 && months != 6 && months != 12 {
				return fmt.Errorf("流量周期须为不自动重置、月、季度、半年或年")
			}
			if months != out.QuotaPeriodMonths {
				out.QuotaPeriodMonths = months
				out.QuotaCycleAnchor = time.Time{}
				out.QuotaResetsAt = time.Time{}
				if months > 0 {
					out.QuotaCycleAnchor = time.Now().UTC()
					out.QuotaResetsAt = quotaCycleDate(out.QuotaCycleAnchor, months)
				}
			}
		}
		if in.ExpiresAt != nil {
			out.ExpiresAt = *in.ExpiresAt
		}
		out.NodeIDs = in.NodeIDs
		if in.Rotate {
			out.Token = model.Secret()
			in.SubscriptionID = nil
		}
		if err := in.clientSettings.apply(st, &out, time.Now()); err != nil {
			return err
		}
		if in.QuotaPeriodMonths != nil && *in.QuotaPeriodMonths > 0 {
			if in.TrafficReset != nil && *in.TrafficReset != "" && *in.TrafficReset != "never" {
				return fmt.Errorf("不能同时配置两种流量重置周期")
			}
			out.TrafficReset = "never"
		}
		out.AccessState = model.ClientStatus(out, time.Now())
		out.NodeIDs = in.NodeIDs
		affected := map[string]bool{}
		for _, n := range st.Nodes {
			if model.Contains(old, n.ID) || model.Contains(out.NodeIDs, n.ID) {
				affected[n.ServerID] = true
			}
		}
		for sid := range affected {
			bump(st, sid)
			v := serverAt(st, sid)
			if v.ClientTraffic == nil {
				v.ClientTraffic = map[string]model.Traffic{}
			}
			if _, exists := v.ClientTraffic[out.ID]; !exists {
				v.ClientTraffic[out.ID] = model.Traffic{}
			}
		}
		if index < 0 {
			st.Clients = append(st.Clients, out)
		} else {
			st.Clients[index] = out
		}
		reconcileClients(st, time.Now())
		for _, c := range st.Clients {
			if c.ID == out.ID {
				out = c
				break
			}
		}
		return nil
	})
	s.result(w, err, out)
}
func (s *Server) deleteClient(w http.ResponseWriter, r *http.Request) {
	err := s.store.Update(func(st *model.State) error {
		for i, c := range st.Clients {
			if c.ID == r.PathValue("id") {
				affected := map[string]bool{}
				for _, n := range st.Nodes {
					if model.Contains(c.NodeIDs, n.ID) {
						affected[n.ServerID] = true
					}
				}
				for sid := range affected {
					bump(st, sid)
				}
				st.Clients = append(st.Clients[:i], st.Clients[i+1:]...)
				return nil
			}
		}
		return errNotFound
	})
	s.result(w, err, map[string]bool{"ok": true})
}
func (s *Server) links(w http.ResponseWriter, r *http.Request) {
	st, ok := s.view(w)
	if !ok {
		return
	}
	for _, c := range st.Clients {
		if c.ID == r.PathValue("id") {
			respond(w, 200, map[string]any{"links": model.ShareLinks(st, c), "subscription": s.publicURL + "/sub/" + c.Token})
			return
		}
	}
	fail(w, 404, "用户不存在")
}
func (s *Server) qr(w http.ResponseWriter, r *http.Request) {
	st, ok := s.view(w)
	if !ok {
		return
	}
	for _, c := range st.Clients {
		if c.ID == r.PathValue("id") {
			png, err := qrcode.Encode(s.publicURL+"/sub/"+c.Token, qrcode.Medium, 280)
			if err != nil {
				fail(w, 500, "二维码生成失败")
				return
			}
			w.Header().Set("Content-Type", "image/png")
			w.Write(png)
			return
		}
	}
	fail(w, 404, "用户不存在")
}
func (s *Server) subscription(w http.ResponseWriter, r *http.Request) {
	st, ok := s.view(w)
	if !ok {
		return
	}
	for _, c := range st.Clients {
		if subtle.ConstantTimeCompare([]byte(c.Token), []byte(r.PathValue("token"))) == 1 && model.ClientStatus(c, time.Now()) == "active" {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			setSubscriptionUsage(w, st, c.Token)
			io.WriteString(w, base64.StdEncoding.EncodeToString([]byte(strings.Join(model.SubscriptionLinks(st, c.Token), "\n"))))
			return
		}
	}
	http.NotFound(w, r)
}
func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		return ""
	}
	return strings.TrimPrefix(h, "Bearer ")
}
func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	token := bearer(r)
	if token == "" {
		fail(w, 401, "注册令牌无效")
		return
	}
	var in struct {
		ID string `json:"id"`
	}
	if !decode(w, r, &in) {
		return
	}
	credential := model.Secret()
	err := s.store.Update(func(st *model.State) error {
		v := serverAt(st, in.ID)
		if v == nil || v.RegistrationHash == "" || time.Now().After(v.RegistrationExpires) || subtle.ConstantTimeCompare([]byte(v.RegistrationHash), []byte(model.Hash(token))) != 1 {
			return fmt.Errorf("注册令牌无效或已过期")
		}
		v.TokenHash = model.Hash(credential)
		v.RegistrationHash = ""
		v.RegistrationExpires = time.Time{}
		return nil
	})
	if err != nil {
		fail(w, 401, "注册令牌无效或已过期")
		return
	}
	respond(w, 200, map[string]string{"token": credential})
}
func (s *Server) poll(w http.ResponseWriter, r *http.Request) {
	token := bearer(r)
	if token == "" {
		fail(w, 401, "Agent 凭据无效")
		return
	}
	var in struct {
		ID string `json:"id"`
		model.Report
	}
	if !decode(w, r, &in) {
		return
	}
	if len(in.Error) > 4000 || len(in.StatsError) > 1000 || len(in.XrayVersion) > 200 || len(in.StatsEpoch) > 64 || len(in.ClientTraffic) > 5000 || len(in.IPStatsError) > 1000 {
		fail(w, 400, "上报数据过长")
		return
	}
	if err := validateOnlineIPs(in.OnlineIPs); err != nil {
		fail(w, 400, err.Error())
		return
	}
	var task model.Task
	err := s.store.Update(func(st *model.State) error {
		v := serverAt(st, in.ID)
		if v == nil || v.TokenHash == "" || subtle.ConstantTimeCompare([]byte(v.TokenHash), []byte(model.Hash(token))) != 1 {
			return fmt.Errorf("Agent 凭据无效")
		}
		if in.AppliedVersion < 0 || in.AppliedVersion > v.Version {
			return fmt.Errorf("配置版本无效")
		}
		v.LastSeen = time.Now()
		v.AppliedVersion = in.AppliedVersion
		v.Running = in.Running
		v.XrayVersion = in.XrayVersion
		v.Error = in.Error
		v.Upload = in.Upload
		v.Download = in.Download
		v.StatsError = in.StatsError
		reconcileClients(st, time.Now())
		accountTraffic(st, v, in.Report)
		accountOnlineIPs(st, v, in.Report, time.Now())
		reconcileClients(st, time.Now())
		task.Version = v.Version
		task.Running = v.DesiredRunning
		task.NodeIDs = []string{}
		for _, n := range st.Nodes {
			if n.ServerID == v.ID {
				task.NodeIDs = append(task.NodeIDs, n.ID)
			}
		}
		task.IPBindings = []string{}
		task.ClientIDs = []string{}
		for _, c := range st.Clients {
			task.ClientIDs = append(task.ClientIDs, c.ID)
			for _, n := range st.Nodes {
				if n.ServerID == v.ID && model.Contains(c.NodeIDs, n.ID) {
					task.IPBindings = append(task.IPBindings, model.BindingKey(c.ID, n.ID))
				}
			}

		}
		if v.Version != in.AppliedVersion {
			var err error
			task.Config, err = model.Config(*st, v.ID)
			return err
		}
		return nil
	})
	if err != nil {
		fail(w, 401, "Agent 认证或上报失败")
		return
	}
	respond(w, 200, task)
}

func IsLoopback(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	h := u.Hostname()
	return h == "localhost" || net.ParseIP(h) != nil && net.ParseIP(h).IsLoopback()
}

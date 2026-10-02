package model

import (
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Server struct {
	SnellSupported bool                   `json:"snellSupported"`
	SnellStatus    map[string]SnellStatus `json:"snellStatus,omitempty"`
	RestartVersion int64                  `json:"restartVersion"`
	Routing        RoutingSettings        `json:"routing"`
	KernelTask     *KernelTask            `json:"kernelTask,omitempty"`
	Kernel         KernelReport           `json:"kernel"`
	OnlineIPs      map[string][]string    `json:"onlineIPs,omitempty"`
	IPStatsAt      time.Time              `json:"ipStatsAt"`
	IPStatsError   string                 `json:"ipStatsError"`

	ID                  string             `json:"id"`
	Name                string             `json:"name"`
	Host                string             `json:"host"`
	TokenHash           string             `json:"tokenHash,omitempty"`
	RegistrationHash    string             `json:"registrationHash,omitempty"`
	RegistrationExpires time.Time          `json:"registrationExpires"`
	LastSeen            time.Time          `json:"lastSeen"`
	Version             int64              `json:"version"`
	AppliedVersion      int64              `json:"appliedVersion"`
	DesiredRunning      bool               `json:"desiredRunning"`
	Running             bool               `json:"running"`
	XrayVersion         string             `json:"xrayVersion"`
	Error               string             `json:"error"`
	Upload              uint64             `json:"upload"`
	Download            uint64             `json:"download"`
	StatsError          string             `json:"statsError"`
	StatsEpoch          string             `json:"statsEpoch,omitempty"`
	NodeTraffic         map[string]Traffic `json:"nodeTraffic,omitempty"`
	ClientTraffic       map[string]Traffic `json:"clientTraffic,omitempty"`
	StatsUpdatedAt      time.Time          `json:"statsUpdatedAt"`
	RegistrationState   string             `json:"registrationState,omitempty"`
}
type Traffic struct {
	Upload   uint64 `json:"upload"`
	Download uint64 `json:"download"`
}
type Node struct {
	Listen            string    `json:"listen"`
	Fingerprint       string    `json:"fingerprint"`
	SpiderX           string    `json:"spiderX"`
	ServerNames       []string  `json:"serverNames"`
	MinClientVersion  string    `json:"minClientVersion"`
	MaxClientVersion  string    `json:"maxClientVersion"`
	MaxTimeDiff       int64     `json:"maxTimeDiff"`
	RealityShow       bool      `json:"realityShow"`
	Sniffing          bool      `json:"sniffing"`
	SniffingRouteOnly bool      `json:"sniffingRouteOnly"`
	SubSortIndex      int       `json:"subSortIndex"`
	ExcludeFromSub    bool      `json:"excludeFromSub"`
	QuotaBytes        uint64    `json:"quotaBytes"`
	QuotaBaseline     Traffic   `json:"quotaBaseline"`
	Upload            uint64    `json:"upload"`
	Download          uint64    `json:"download"`
	TrafficUpdatedAt  time.Time `json:"trafficUpdatedAt"`
	TrafficReset      string    `json:"trafficReset"`
	TrafficResetDay   int       `json:"trafficResetDay"`
	QuotaResetsAt     time.Time `json:"quotaResetsAt"`
	ExpiresAt         time.Time `json:"expiresAt"`
	AccessState       string    `json:"accessState"`
	ID                string    `json:"id"`
	ServerID          string    `json:"serverId"`
	Name              string    `json:"name"`
	Port              int       `json:"port"`
	SNI               string    `json:"sni"`
	Target            string    `json:"target"`
	PrivateKey        string    `json:"privateKey,omitempty"`
	PublicKey         string    `json:"publicKey"`
	ShortID           string    `json:"shortId"`
	Enabled           bool      `json:"enabled"`
}
type NodeIPState struct {
	OnlineIPs    []string  `json:"onlineIPs"`
	UpdatedAt    time.Time `json:"updatedAt"`
	BlockedUntil time.Time `json:"blockedUntil"`
	StatsState   string    `json:"statsState"`
}
type Client struct {
	CreatedAt       time.Time              `json:"createdAt"`
	UpdatedAt       time.Time              `json:"updatedAt"`
	LastOnlineAt    time.Time              `json:"lastOnlineAt"`
	LimitIP         int                    `json:"limitIp"`
	NodeFlows       map[string]string      `json:"nodeFlows"`
	Email           string                 `json:"email"`
	Flow            *string                `json:"flow,omitempty"`
	ReverseTag      string                 `json:"reverseTag"`
	TelegramID      string                 `json:"telegramId"`
	Group           string                 `json:"group"`
	Comment         string                 `json:"comment"`
	ExternalLinks   []string               `json:"externalLinks"`
	TrafficReset    string                 `json:"trafficReset"`
	TrafficResetDay int                    `json:"trafficResetDay"`
	ResetDays       int                    `json:"resetDays"`
	ResetDay        int                    `json:"resetDay"`
	ResetWeekday    int                    `json:"resetWeekday"`
	ResetMax        int                    `json:"resetMax"`
	RenewalCount    int                    `json:"renewalCount"`
	FirstUseDays    int                    `json:"firstUseDays"`
	FirstUsedAt     time.Time              `json:"firstUsedAt"`
	NodeIPLimits    map[string]int         `json:"nodeIPLimits"`
	NodeIPStates    map[string]NodeIPState `json:"nodeIPStates"`

	QuotaPeriodMonths int                `json:"quotaPeriodMonths"`
	QuotaCycleAnchor  time.Time          `json:"quotaCycleAnchor"`
	QuotaResetsAt     time.Time          `json:"quotaResetsAt"`
	QuotaBytes        uint64             `json:"quotaBytes"`
	QuotaBaseline     Traffic            `json:"quotaBaseline"`
	ExpiresAt         time.Time          `json:"expiresAt"`
	AccessState       string             `json:"accessState"`
	ID                string             `json:"id"`
	Name              string             `json:"name"`
	UUID              string             `json:"uuid"`
	Token             string             `json:"token"`
	Enabled           bool               `json:"enabled"`
	NodeIDs           []string           `json:"nodeIds"`
	Upload            uint64             `json:"upload"`
	Download          uint64             `json:"download"`
	TrafficUpdatedAt  time.Time          `json:"trafficUpdatedAt"`
	ServerTraffic     map[string]Traffic `json:"serverTraffic,omitempty"`
}
type State struct {
	Snell     []SnellInbound `json:"snell"`
	Outbounds []Outbound     `json:"outbounds"`
	Rules     []RouteRule    `json:"rules"`
	Servers   []Server       `json:"servers"`
	Nodes     []Node         `json:"nodes"`
	Clients   []Client       `json:"clients"`
}
type Report struct {
	SnellSupported   bool                   `json:"snellSupported"`
	SnellStatus      map[string]SnellStatus `json:"snellStatus,omitempty"`
	StatsCollectedAt time.Time              `json:"statsCollectedAt,omitempty"`
	IPCollectedAt    time.Time              `json:"ipCollectedAt,omitempty"`
	Kernel           KernelReport           `json:"kernel"`
	NodeTraffic      map[string]Traffic     `json:"nodeTraffic,omitempty"`
	OnlineIPs        map[string][]string    `json:"onlineIPs"`
	IPStatsError     string                 `json:"ipStatsError"`

	AppliedVersion int64              `json:"appliedVersion"`
	Running        bool               `json:"running"`
	XrayVersion    string             `json:"xrayVersion"`
	Error          string             `json:"error"`
	Upload         uint64             `json:"upload"`
	Download       uint64             `json:"download"`
	StatsError     string             `json:"statsError"`
	StatsEpoch     string             `json:"statsEpoch,omitempty"`
	ClientTraffic  map[string]Traffic `json:"clientTraffic,omitempty"`
}
type Task struct {
	Snell          []SnellInbound  `json:"snell"`
	RestartVersion int64           `json:"restartVersion"`
	Kernel         *KernelTask     `json:"kernel,omitempty"`
	NodeIDs        []string        `json:"nodeIds"`
	IPBindings     []string        `json:"ipBindings"`
	Version        int64           `json:"version"`
	Running        bool            `json:"running"`
	Config         json.RawMessage `json:"config,omitempty"`
	ClientIDs      []string        `json:"clientIds"`
}

func Secret() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
func ID() string           { return Secret()[:16] }
func Hash(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func UUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}
func Keys() (string, string, error) {
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	return base64.RawURLEncoding.EncodeToString(k.Bytes()), base64.RawURLEncoding.EncodeToString(k.PublicKey().Bytes()), nil
}
func ValidHost(host string) bool {
	if net.ParseIP(host) != nil {
		return true
	}
	if len(host) == 0 || len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}
func ValidateNode(n Node) error {
	if strings.TrimSpace(n.Name) == "" || len(n.Name) > 100 {
		return fmt.Errorf("节点名称须为 1–100 个字符")
	}
	if n.Port < 1 || n.Port > 65535 || n.Port == 10085 {
		return fmt.Errorf("端口须为 1–65535，10085 为统计接口保留端口")
	}
	if !ValidHost(n.SNI) || net.ParseIP(n.SNI) != nil {
		return fmt.Errorf("SNI 必须为域名")
	}
	h, p, err := net.SplitHostPort(n.Target)
	if err != nil || !ValidHost(h) {
		return fmt.Errorf("目标地址须为 域名:端口")
	}
	port, err := strconv.Atoi(p)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("目标端口无效")
	}
	return nil
}
func Contains(ids []string, id string) bool {
	for _, v := range ids {
		if v == id {
			return true
		}
	}
	return false
}

func Config(state State, serverID string) (json.RawMessage, error) {
	outbounds, routing, err := routingConfig(state, serverID)
	if err != nil {
		return nil, err
	}
	inbounds := []any{}
	for _, n := range state.Nodes {
		if n.ServerID != serverID || NodeStatus(n, time.Now()) != "active" {
			continue
		}
		clients := []any{}
		for _, c := range state.Clients {
			if ClientNodeActive(c, n.ID, time.Now()) && Contains(c.NodeIDs, n.ID) {
				entry := map[string]any{"id": c.UUID, "flow": ClientNodeFlow(c, n.ID), "email": BindingKey(c.ID, n.ID), "level": 0}
				if c.ReverseTag != "" {
					entry["reverse"] = map[string]string{"tag": c.ReverseTag}
				}
				clients = append(clients, entry)
			}
		}
		listen := n.Listen
		if listen == "" {
			listen = "0.0.0.0"
		}
		serverNames := append([]string{n.SNI}, n.ServerNames...)
		reality := map[string]any{"show": n.RealityShow, "target": n.Target, "xver": 0, "serverNames": serverNames, "privateKey": n.PrivateKey, "shortIds": []string{n.ShortID}}
		if n.MinClientVersion != "" {
			reality["minClientVer"] = n.MinClientVersion
		}
		if n.MaxClientVersion != "" {
			reality["maxClientVer"] = n.MaxClientVersion
		}
		if n.MaxTimeDiff > 0 {
			reality["maxTimeDiff"] = n.MaxTimeDiff
		}
		entry := map[string]any{"tag": n.ID, "listen": listen, "port": n.Port, "protocol": "vless", "settings": map[string]any{"clients": clients, "decryption": "none"}, "streamSettings": map[string]any{"network": "raw", "security": "reality", "realitySettings": reality}}
		if n.Sniffing {
			entry["sniffing"] = map[string]any{"enabled": true, "destOverride": []string{"http", "tls", "quic"}, "routeOnly": n.SniffingRouteOnly}
		}
		inbounds = append(inbounds, entry)
	}
	return json.MarshalIndent(map[string]any{"log": map[string]any{"loglevel": "warning"}, "api": map[string]any{"tag": "api", "listen": "127.0.0.1:10085", "services": []string{"StatsService"}}, "stats": map[string]any{}, "policy": map[string]any{"levels": map[string]any{"0": map[string]any{"statsUserUplink": true, "statsUserDownlink": true, "statsUserOnline": true}}, "system": map[string]any{"statsInboundUplink": true, "statsInboundDownlink": true}}, "inbounds": inbounds, "outbounds": outbounds, "routing": routing}, "", "  ")
}
func Link(c Client, n Node, s Server) string {
	q := url.Values{"type": {"tcp"}, "security": {"reality"}, "encryption": {"none"}, "flow": {ClientNodeFlow(c, n.ID)}, "sni": {n.SNI}, "fp": {"chrome"}, "pbk": {n.PublicKey}, "sid": {n.ShortID}}
	if n.Fingerprint != "" {
		q.Set("fp", n.Fingerprint)
	}
	if n.SpiderX != "" {
		q.Set("spx", n.SpiderX)
	}
	return "vless://" + c.UUID + "@" + net.JoinHostPort(s.Host, strconv.Itoa(n.Port)) + "?" + q.Encode() + "#" + url.PathEscape(n.Name)
}
func Links(state State, c Client) []string {
	links := []string{}
	if ClientStatus(c, time.Now()) != "active" {
		return links
	}
	nodes := append([]Node(nil), state.Nodes...)
	sort.SliceStable(nodes, func(i, j int) bool {
		a, b := nodes[i].SubSortIndex, nodes[j].SubSortIndex
		if a == 0 {
			a = 1
		}
		if b == 0 {
			b = 1
		}
		return a < b
	})
	for _, n := range nodes {
		if NodeStatus(n, time.Now()) != "active" || n.ExcludeFromSub || !Contains(c.NodeIDs, n.ID) || !ClientNodeActive(c, n.ID, time.Now()) {
			continue
		}
		for _, s := range state.Servers {
			if s.ID == n.ServerID {
				links = append(links, Link(c, n, s))
			}
		}
	}
	return append(links, c.ExternalLinks...)
}

// QuotaUsage preserves lifetime accounting when a new allowance begins.
func QuotaUsage(c Client) uint64 {
	var up, down uint64
	if c.Upload > c.QuotaBaseline.Upload {
		up = c.Upload - c.QuotaBaseline.Upload
	}
	if c.Download > c.QuotaBaseline.Download {
		down = c.Download - c.QuotaBaseline.Download
	}
	if ^uint64(0)-up < down {
		return ^uint64(0)
	}
	return up + down
}
func ClientStatus(c Client, now time.Time) string {
	if !c.Enabled {
		return "disabled"
	}
	if !c.ExpiresAt.IsZero() && !now.Before(c.ExpiresAt) {
		return "expired"
	}
	if c.QuotaBytes > 0 && QuotaUsage(c) >= c.QuotaBytes {
		return "quota"
	}
	return "active"
}

// IDs generated by ID contain no dots, so this is unambiguous.
func BindingKey(clientID, nodeID string) string { return clientID + "." + nodeID }
func ClientNodeActive(c Client, nodeID string, now time.Time) bool {
	return ClientStatus(c, now) == "active" && (ClientIPLimit(c, nodeID) == 0 || !now.Before(c.NodeIPStates[nodeID].BlockedUntil))
}
func ClientIDFromEmail(email string) string { id, _, _ := strings.Cut(email, "."); return id }

func ClientFlow(c Client) string {
	if c.Flow == nil {
		return "xtls-rprx-vision"
	}
	return *c.Flow
}
func NodeUsage(n Node) uint64 {
	return QuotaUsage(Client{Upload: n.Upload, Download: n.Download, QuotaBaseline: n.QuotaBaseline})
}
func NodeStatus(n Node, now time.Time) string {
	if !n.Enabled {
		return "disabled"
	}
	if !n.ExpiresAt.IsZero() && !now.Before(n.ExpiresAt) {
		return "expired"
	}
	if n.QuotaBytes > 0 && NodeUsage(n) >= n.QuotaBytes {
		return "quota"
	}
	return "active"
}

// A shared subscription ID groups active clients without sharing credentials or quotas.
func SubscriptionLinks(st State, token string) []string {
	links := []string{}
	seen := map[string]bool{}
	for _, c := range st.Clients {
		if c.Token != token {
			continue
		}
		for _, link := range Links(st, c) {
			if !seen[link] {
				seen[link] = true
				links = append(links, link)
			}
		}
	}
	return links
}

func ClientIPLimit(c Client, nodeID string) int {
	if limit, ok := c.NodeIPLimits[nodeID]; ok {
		return limit
	}
	return c.LimitIP
}
func ClientNodeFlow(c Client, nodeID string) string {
	if flow, ok := c.NodeFlows[nodeID]; ok {
		return flow
	}
	return ClientFlow(c)
}

// Hiding an inbound from subscriptions does not hide its individual share link.
func ShareLinks(st State, c Client) []string {
	st.Nodes = append([]Node(nil), st.Nodes...)
	for i := range st.Nodes {
		st.Nodes[i].ExcludeFromSub = false
	}
	return Links(st, c)
}

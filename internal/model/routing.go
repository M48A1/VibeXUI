package model

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
)

// RoutingSettings defaults preserve the direct-only behavior of older databases.
type RoutingSettings struct {
	DefaultOutbound string `json:"defaultOutbound"`
	DomainStrategy  string `json:"domainStrategy"`
}
type Outbound struct {
	ID          string `json:"id"`
	ServerID    string `json:"serverId"`
	Name        string `json:"name"`
	Enabled     bool   `json:"enabled"`
	Protocol    string `json:"protocol"`
	Address     string `json:"address"`
	Port        int    `json:"port"`
	UUID        string `json:"uuid,omitempty"`
	Flow        string `json:"flow"`
	Security    string `json:"security"`
	ServerName  string `json:"serverName"`
	PublicKey   string `json:"publicKey"`
	ShortID     string `json:"shortId"`
	Fingerprint string `json:"fingerprint"`
	Method      string `json:"method"`
	Password    string `json:"password,omitempty"`
}
type RouteRule struct {
	ID         string   `json:"id"`
	ServerID   string   `json:"serverId"`
	Name       string   `json:"name"`
	Enabled    bool     `json:"enabled"`
	OutboundID string   `json:"outboundId"`
	Domains    []string `json:"domains"`
	IPs        []string `json:"ips"`
	InboundIDs []string `json:"inboundIds"`
	Port       string   `json:"port"`
	Network    string   `json:"network"`
}

func RoutingDefaults(v RoutingSettings) RoutingSettings {
	if v.DefaultOutbound == "" {
		v.DefaultOutbound = "direct"
	}
	if v.DomainStrategy == "" {
		v.DomainStrategy = "AsIs"
	}
	return v
}
func endpointHost(v string) bool {
	if len(v) == 0 || len(v) > 253 {
		return false
	}
	if net.ParseIP(v) != nil {
		return true
	}
	for _, label := range strings.Split(strings.TrimSuffix(v, "."), ".") {
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
func ValidateOutbound(o Outbound) error {
	if strings.TrimSpace(o.Name) == "" || len(o.Name) > 100 {
		return fmt.Errorf("出口名称须为 1–100 字节")
	}
	if !endpointHost(o.Address) || o.Port < 1 || o.Port > 65535 {
		return fmt.Errorf("请填写有效的出口主机和端口")
	}
	switch o.Protocol {
	case "vless":
		raw := strings.ReplaceAll(o.UUID, "-", "")
		b, e := hex.DecodeString(raw)
		if e != nil || len(b) != 16 || len(o.UUID) != 36 || o.UUID[8] != '-' || o.UUID[13] != '-' || o.UUID[18] != '-' || o.UUID[23] != '-' {
			return fmt.Errorf("VLESS UUID 无效")
		}
		if o.Flow != "" && o.Flow != "xtls-rprx-vision" && o.Flow != "xtls-rprx-vision-udp443" {
			return fmt.Errorf("不支持的 VLESS Flow")
		}
		if !endpointHost(o.ServerName) {
			return fmt.Errorf("请填写有效的 TLS/REALITY SNI")
		}
		switch o.Fingerprint {
		case "", "chrome", "firefox", "safari", "ios", "android", "edge", "random", "randomized":
		default:
			return fmt.Errorf("不支持的 TLS 指纹")
		}
		if o.Security == "reality" {
			key, e := base64.RawURLEncoding.DecodeString(o.PublicKey)
			if e != nil || len(key) != 32 {
				return fmt.Errorf("REALITY 公钥无效")
			}
			if len(o.ShortID) > 16 || len(o.ShortID)%2 != 0 {
				return fmt.Errorf("REALITY Short ID 无效")
			}
			if _, e := hex.DecodeString(o.ShortID); e != nil {
				return fmt.Errorf("REALITY Short ID 无效")
			}
		} else if o.Security != "tls" {
			return fmt.Errorf("VLESS 出口仅支持 RAW TCP + REALITY/TLS")
		}
	case "shadowsocks":
		keyLen := 0
		switch o.Method {
		case "2022-blake3-aes-128-gcm":
			keyLen = 16
		case "2022-blake3-aes-256-gcm", "2022-blake3-chacha20-poly1305":
			keyLen = 32
		case "aes-128-gcm", "aes-256-gcm", "chacha20-ietf-poly1305":
		default:
			return fmt.Errorf("不支持的 Shadowsocks 加密方式")
		}
		if len(o.Password) == 0 || len(o.Password) > 512 {
			return fmt.Errorf("Shadowsocks 密码须为 1–512 字节")
		}
		if keyLen > 0 {
			for _, part := range strings.Split(o.Password, ":") {
				b, e := base64.StdEncoding.DecodeString(part)
				if e != nil || len(b) != keyLen {
					return fmt.Errorf("SS2022 密钥须为 %d 字节的 Base64 编码", keyLen)
				}
			}
		}
	default:
		return fmt.Errorf("不支持的出口协议")
	}
	return nil
}
func ValidateRouting(st State, serverID string) error {
	var srv *Server
	for i := range st.Servers {
		if st.Servers[i].ID == serverID {
			srv = &st.Servers[i]
			break
		}
	}
	if srv == nil {
		return fmt.Errorf("服务器不存在")
	}
	settings := RoutingDefaults(srv.Routing)
	switch settings.DomainStrategy {
	case "AsIs", "IPIfNonMatch", "IPOnDemand":
	default:
		return fmt.Errorf("无效的域名解析策略")
	}
	targets := map[string]bool{"direct": true, "block": true}
	count := 0
	for _, o := range st.Outbounds {
		if o.ServerID != serverID {
			continue
		}
		count++
		if count > 100 {
			return fmt.Errorf("每台服务器最多 100 个出口")
		}
		if _, ok := targets[o.ID]; ok || o.ID == "" {
			return fmt.Errorf("出口 ID 重复或无效")
		}
		if err := ValidateOutbound(o); err != nil {
			return err
		}
		targets[o.ID] = o.Enabled
		if strings.EqualFold(strings.Trim(o.Address, "[]"), strings.Trim(srv.Host, "[]")) {
			for _, n := range st.Nodes {
				if n.ServerID == serverID && n.Port == o.Port {
					return fmt.Errorf("出口不能指向本机同端口的入站")
				}
			}
		}
	}
	if enabled := targets[settings.DefaultOutbound]; !enabled {
		return fmt.Errorf("默认出口必须是本服务器已启用的出口")
	}
	nodes := map[string]bool{}
	for _, n := range st.Nodes {
		if n.ServerID == serverID {
			nodes[n.ID] = true
		}
	}
	count = 0
	size := 0
	ids := map[string]bool{}
	for _, r := range st.Rules {
		if r.ServerID != serverID {
			continue
		}
		count++
		if count > 100 {
			return fmt.Errorf("每台服务器最多 100 条分流规则")
		}
		if r.ID == "" || ids[r.ID] {
			return fmt.Errorf("规则 ID 重复或无效")
		}
		ids[r.ID] = true
		if strings.TrimSpace(r.Name) == "" || len(r.Name) > 100 {
			return fmt.Errorf("规则名称须为 1–100 字节")
		}
		enabled, exists := targets[r.OutboundID]
		if !exists || r.Enabled && !enabled {
			return fmt.Errorf("规则目标必须是本服务器的出口，启用规则不能使用停用出口")
		}
		if len(r.Domains)+len(r.IPs)+len(r.InboundIDs) == 0 && r.Port == "" && r.Network == "" {
			return fmt.Errorf("规则至少需要一个匹配条件；全部流量请设置默认出口")
		}
		if len(r.Domains) > 100 || len(r.IPs) > 100 || len(r.InboundIDs) > 100 {
			return fmt.Errorf("每种匹配条件最多 100 项")
		}
		for _, id := range r.InboundIDs {
			if !nodes[id] {
				return fmt.Errorf("规则只能引用本服务器的入站")
			}
		}
		for _, d := range r.Domains {
			size += len(d)
			if len(d) > 1024 || strings.TrimSpace(d) != d || d == "" {
				return fmt.Errorf("域名匹配条件无效")
			}
			kind, value, has := strings.Cut(d, ":")
			if !has {
				kind = "domain"
				value = d
			}
			switch kind {
			case "domain", "full":
				if !endpointHost(value) {
					return fmt.Errorf("域名无效：%s", d)
				}
			case "keyword":
				if value == "" {
					return fmt.Errorf("关键词不能为空")
				}
			case "regexp":
				if _, err := regexp.Compile(value); err != nil {
					return fmt.Errorf("域名正则表达式无效")
				}
			default:
				return fmt.Errorf("仅支持 domain/full/keyword/regexp；暂不支持 geosite 等外部规则库")
			}
		}
		for _, ip := range r.IPs {
			size += len(ip)
			if _, e := netip.ParseAddr(ip); e != nil {
				if _, e := netip.ParsePrefix(ip); e != nil {
					return fmt.Errorf("IP 须为地址或 CIDR，暂不支持 geoip 规则库")
				}
			}
		}
		if r.Port != "" {
			if len(r.Port) > 512 {
				return fmt.Errorf("端口列表过长")
			}
			for _, p := range strings.Split(r.Port, ",") {
				ends := strings.Split(p, "-")
				if len(ends) > 2 {
					return fmt.Errorf("端口格式无效")
				}
				last := 0
				for _, v := range ends {
					n, e := strconv.Atoi(v)
					if e != nil || n < 1 || n > 65535 || n < last {
						return fmt.Errorf("端口或端口范围无效")
					}
					last = n
				}
			}
		}
		if r.Network != "" && r.Network != "tcp" && r.Network != "udp" && r.Network != "tcp,udp" {
			return fmt.Errorf("网络类型须为 TCP、UDP 或两者")
		}
	}
	if size > 128*1024 {
		return fmt.Errorf("服务器的路由匹配条件总长度超过限制")
	}
	return nil
}
func outboundTag(id string) string {
	if id == "direct" || id == "block" {
		return id
	}
	return "out-" + id
}
func routingConfig(st State, serverID string) ([]any, map[string]any, error) {
	if err := ValidateRouting(st, serverID); err != nil {
		return nil, nil, err
	}
	var settings RoutingSettings
	for _, s := range st.Servers {
		if s.ID == serverID {
			settings = RoutingDefaults(s.Routing)
		}
	}
	outs := []any{map[string]any{"tag": "direct", "protocol": "freedom"}, map[string]any{"tag": "block", "protocol": "blackhole"}}
	for _, o := range st.Outbounds {
		if o.ServerID != serverID || !o.Enabled {
			continue
		}
		cfg := map[string]any{"tag": outboundTag(o.ID), "protocol": o.Protocol}
		if o.Protocol == "shadowsocks" {
			cfg["settings"] = map[string]any{"servers": []any{map[string]any{"address": o.Address, "port": o.Port, "method": o.Method, "password": o.Password}}}
		} else {
			cfg["settings"] = map[string]any{"vnext": []any{map[string]any{"address": o.Address, "port": o.Port, "users": []any{map[string]any{"id": o.UUID, "encryption": "none", "flow": o.Flow}}}}}
			fp := o.Fingerprint
			if fp == "" {
				fp = "chrome"
			}
			stream := map[string]any{"network": "raw", "security": o.Security}
			if o.Security == "reality" {
				stream["realitySettings"] = map[string]any{"serverName": o.ServerName, "fingerprint": fp, "publicKey": o.PublicKey, "shortId": o.ShortID}
			} else {
				stream["tlsSettings"] = map[string]any{"serverName": o.ServerName, "fingerprint": fp}
			}
			cfg["streamSettings"] = stream
		}
		outs = append(outs, cfg)
	}
	// Xray falls back to the first outbound. A catch-all rule would suppress IPIfNonMatch's DNS pass.
	for i, o := range outs {
		if o.(map[string]any)["tag"] == outboundTag(settings.DefaultOutbound) {
			outs[0], outs[i] = outs[i], outs[0]
			break
		}
	}
	rules := []any{}
	for _, r := range st.Rules {
		if r.ServerID != serverID || !r.Enabled {
			continue
		}
		v := map[string]any{"type": "field", "outboundTag": outboundTag(r.OutboundID)}
		if len(r.Domains) > 0 {
			ds := []string{}
			for _, d := range r.Domains {
				if !strings.Contains(d, ":") {
					d = "domain:" + d
				}
				ds = append(ds, d)
			}
			v["domain"] = ds
		}
		if len(r.IPs) > 0 {
			v["ip"] = r.IPs
		}
		if len(r.InboundIDs) > 0 {
			v["inboundTag"] = r.InboundIDs
		}
		if r.Port != "" {
			v["port"] = r.Port
		}
		if r.Network != "" {
			v["network"] = r.Network
		}
		rules = append(rules, v)
	}
	return outs, map[string]any{"domainStrategy": settings.DomainStrategy, "rules": rules}, nil
}

// Referenced in disabled rules too: deleting an inbound must never broaden a saved rule.
func CheckNodeRoutingDelete(st State, id string) error {
	for _, r := range st.Rules {
		if Contains(r.InboundIDs, id) {
			return fmt.Errorf("入站被分流规则 %q 引用，请先修改或删除规则", r.Name)
		}
	}
	return nil
}

package server

import (
	"crypto/ecdh"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"
	"vibexui/internal/model"
)

type nodeSettings struct {
	Listen            *string    `json:"listen"`
	Fingerprint       *string    `json:"fingerprint"`
	SpiderX           *string    `json:"spiderX"`
	ServerNames       *[]string  `json:"serverNames"`
	PrivateKey        *string    `json:"privateKey"`
	ShortID           *string    `json:"shortId"`
	MinClientVersion  *string    `json:"minClientVersion"`
	MaxClientVersion  *string    `json:"maxClientVersion"`
	MaxTimeDiff       *int64     `json:"maxTimeDiff"`
	RealityShow       *bool      `json:"realityShow"`
	Sniffing          *bool      `json:"sniffing"`
	SniffingRouteOnly *bool      `json:"sniffingRouteOnly"`
	SubSortIndex      *int       `json:"subSortIndex"`
	ExcludeFromSub    *bool      `json:"excludeFromSub"`
	QuotaBytes        *uint64    `json:"quotaBytes"`
	ExpiresAt         *time.Time `json:"expiresAt"`
	TrafficReset      *string    `json:"trafficReset"`
	TrafficResetDay   *int       `json:"trafficResetDay"`
}

func (in nodeSettings) apply(n *model.Node, now time.Time) error {
	if in.Listen != nil {
		n.Listen = strings.TrimSpace(*in.Listen)
	}
	if n.Listen != "" && net.ParseIP(n.Listen) == nil {
		return fmt.Errorf("监听地址须为 IP，留空监听全部 IPv4 地址")
	}
	if in.Fingerprint != nil {
		n.Fingerprint = *in.Fingerprint
	}
	switch n.Fingerprint {
	case "", "chrome", "firefox", "safari", "ios", "android", "edge", "360", "qq", "random", "randomized":
	default:
		return fmt.Errorf("指纹无效")
	}
	if in.SpiderX != nil {
		n.SpiderX = *in.SpiderX
	}
	if len(n.SpiderX) > 200 || strings.ContainsAny(n.SpiderX, "\r\n") {
		return fmt.Errorf("SpiderX 无效")
	}
	if in.ServerNames != nil {
		n.ServerNames = *in.ServerNames
	}
	if len(n.ServerNames) > 20 {
		return fmt.Errorf("附加 SNI 最多 20 个")
	}
	for _, name := range n.ServerNames {
		if !model.ValidHost(name) || net.ParseIP(name) != nil {
			return fmt.Errorf("附加 SNI 须为域名")
		}
	}
	if in.PrivateKey != nil && *in.PrivateKey != "" {
		raw, err := base64.RawURLEncoding.DecodeString(*in.PrivateKey)
		if err != nil {
			return fmt.Errorf("REALITY 私钥无效")
		}
		key, err := ecdh.X25519().NewPrivateKey(raw)
		if err != nil {
			return fmt.Errorf("REALITY 私钥须为 32 字节 X25519 密钥")
		}
		n.PrivateKey = *in.PrivateKey
		n.PublicKey = base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes())
	}
	if in.ShortID != nil {
		n.ShortID = *in.ShortID
	}
	if _, err := hex.DecodeString(n.ShortID); err != nil || len(n.ShortID) > 16 {
		return fmt.Errorf("Short ID 须为最多 16 位且长度为偶数的十六进制字符串")
	}
	if in.MinClientVersion != nil {
		n.MinClientVersion = *in.MinClientVersion
	}
	if in.MaxClientVersion != nil {
		n.MaxClientVersion = *in.MaxClientVersion
	}
	version := regexp.MustCompile(`^([0-9]{1,3}\.){2}[0-9]{1,3}$`)
	for _, v := range []string{n.MinClientVersion, n.MaxClientVersion} {
		if v != "" && !version.MatchString(v) {
			return fmt.Errorf("Xray 版本须为 x.y.z")
		}
	}
	if in.MaxTimeDiff != nil {
		n.MaxTimeDiff = *in.MaxTimeDiff
	}
	if n.MaxTimeDiff < 0 || n.MaxTimeDiff > 86400000 {
		return fmt.Errorf("最大时间差须为 0–86400000 毫秒")
	}
	if in.RealityShow != nil {
		n.RealityShow = *in.RealityShow
	}
	if in.Sniffing != nil {
		n.Sniffing = *in.Sniffing
	}
	if in.SniffingRouteOnly != nil {
		n.SniffingRouteOnly = *in.SniffingRouteOnly
	}
	if in.SubSortIndex != nil {
		n.SubSortIndex = *in.SubSortIndex
	}
	if n.SubSortIndex < -100000 || n.SubSortIndex > 100000 {
		return fmt.Errorf("订阅排序须为 -100000–100000")
	}
	if in.ExcludeFromSub != nil {
		n.ExcludeFromSub = *in.ExcludeFromSub
	}

	if in.QuotaBytes != nil {
		if *in.QuotaBytes > 9007199254740991 {
			return fmt.Errorf("流量额度过大")
		}
		n.QuotaBytes = *in.QuotaBytes
	}
	if in.ExpiresAt != nil {
		n.ExpiresAt = *in.ExpiresAt
	}
	if !n.ExpiresAt.IsZero() && (n.ExpiresAt.Year() < 2000 || n.ExpiresAt.Year() > 9999) {
		return fmt.Errorf("到期时间无效")
	}
	oldMode, oldDay := n.TrafficReset, n.TrafficResetDay
	if n.TrafficResetDay == 0 {
		n.TrafficResetDay = 1
	}
	if in.TrafficReset != nil {
		n.TrafficReset = *in.TrafficReset
	}
	if in.TrafficResetDay != nil {
		n.TrafficResetDay = *in.TrafficResetDay
	}
	if !validTrafficReset(n.TrafficReset, n.TrafficResetDay) {
		return fmt.Errorf("流量重置须为不重置、每小时、每天、每周或每月；重置日须为 1–31")
	}
	if n.TrafficReset != oldMode || n.TrafficResetDay != oldDay {
		n.QuotaResetsAt = nextTrafficReset(now, n.TrafficReset, n.TrafficResetDay)
	}
	n.AccessState = model.NodeStatus(*n, now)
	return nil
}
func (s *Server) resetNodeQuota(w http.ResponseWriter, r *http.Request) {
	var in struct{}
	if !decode(w, r, &in) {
		return
	}
	var out model.Node
	err := s.store.Update(func(st *model.State) error {
		for i := range st.Nodes {
			n := &st.Nodes[i]
			if n.ID != r.PathValue("id") {
				continue
			}
			n.QuotaBaseline = model.Traffic{Upload: n.Upload, Download: n.Download}
			reconcileClients(st, time.Now())
			out = *n
			out.PrivateKey = ""
			return nil
		}
		return errNotFound
	})
	s.result(w, err, out)
}

func (s *Server) nodeKeys(w http.ResponseWriter, r *http.Request) {
	var in struct{}
	if !decode(w, r, &in) {
		return
	}
	private, public, err := model.Keys()
	s.result(w, err, map[string]string{"privateKey": private, "publicKey": public, "shortId": model.Hash(model.Secret())[:16]})
}

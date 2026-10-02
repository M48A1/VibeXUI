package server

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"sort"
	"strings"
	"time"

	"vibexui/internal/model"
)

type cdnResolver interface {
	LookupCNAME(context.Context, string) (string, error)
	LookupIPAddr(context.Context, string) ([]net.IPAddr, error)
}
type cdnChecker struct {
	resolver cdnResolver
	slots    chan struct{}
}
type cdnResult struct {
	Domain        string    `json:"domain"`
	Status        string    `json:"status"`
	Providers     []string  `json:"providers"`
	CanonicalName string    `json:"canonicalName"`
	Addresses     []string  `json:"addresses"`
	Evidence      []string  `json:"evidence"`
	Warnings      []string  `json:"warnings"`
	CheckedAt     time.Time `json:"checkedAt"`
}

// Official Cloudflare ranges, verified 2026-10-02:
// https://www.cloudflare.com/ips-v4 and https://www.cloudflare.com/ips-v6.
// These signatures indicate provider infrastructure, not whether content is cached.
var cloudflarePrefixes = func() []netip.Prefix {
	var prefixes []netip.Prefix
	for _, cidr := range strings.Fields(`173.245.48.0/20 103.21.244.0/22 103.22.200.0/22 103.31.4.0/22 141.101.64.0/18 108.162.192.0/18 190.93.240.0/20 188.114.96.0/20 197.234.240.0/22 198.41.128.0/17 162.158.0.0/15 104.16.0.0/13 104.24.0.0/14 172.64.0.0/13 131.0.72.0/22 2400:cb00::/32 2606:4700::/32 2803:f800::/32 2405:b500::/32 2405:8100::/32 2a06:98c0::/29 2c0f:f248::/32`) {
		prefixes = append(prefixes, netip.MustParsePrefix(cidr))
	}
	return prefixes
}()

func (s *Server) checkCDN(w http.ResponseWriter, r *http.Request) {
	var input struct {
		SNI string `json:"sni"`
	}
	if !decode(w, r, &input) {
		return
	}
	domain := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(input.SNI)), ".")
	if !model.ValidHost(domain) || net.ParseIP(domain) != nil || !strings.Contains(domain, ".") {
		fail(w, http.StatusBadRequest, "请输入完整的 SNI 域名，不含协议、端口或路径")
		return
	}
	select {
	case s.cdn.slots <- struct{}{}:
		defer func() { <-s.cdn.slots }()
	default:
		fail(w, http.StatusTooManyRequests, "检测繁忙，请稍后重试")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()
	respond(w, http.StatusOK, s.cdn.check(ctx, domain))
}

func (c *cdnChecker) check(ctx context.Context, domain string) cdnResult {
	result := cdnResult{Domain: domain, Status: "unknown", Providers: []string{}, Addresses: []string{}, Evidence: []string{}, Warnings: []string{}, CheckedAt: time.Now().UTC()}
	type cnameAnswer struct {
		name string
		err  error
	}
	type ipAnswer struct {
		ips []net.IPAddr
		err error
	}
	cnames := make(chan cnameAnswer, 1)
	ips := make(chan ipAnswer, 1)
	// Absolute DNS names avoid local search suffixes. No connection to the supplied host is made.
	go func() { name, err := c.resolver.LookupCNAME(ctx, domain+"."); cnames <- cnameAnswer{name, err} }()
	go func() { addrs, err := c.resolver.LookupIPAddr(ctx, domain+"."); ips <- ipAnswer{addrs, err} }()
	var cn cnameAnswer
	var ip ipAnswer
	select {
	case cn = <-cnames:
	case <-ctx.Done():
		cn.err = ctx.Err()
	}
	select {
	case ip = <-ips:
	case <-ctx.Done():
		ip.err = ctx.Err()
	}
	if cn.err != nil {
		result.Warnings = append(result.Warnings, "CNAME 查询失败或超时，检测结果不完整")
	} else {
		result.CanonicalName = strings.TrimSuffix(strings.ToLower(cn.name), ".")
	}
	if ip.err != nil || len(ip.ips) == 0 {
		result.Warnings = append(result.Warnings, "未能解析 IP 地址，无法确认域名可连接")
	}
	providers := map[string]bool{}
	// CDN edge hostname suffixes documented by AWS, Fastly and Akamai.
	for _, rule := range []struct{ suffix, provider string }{{"cloudfront.net", "CloudFront"}, {"fastly.net", "Fastly"}, {"edgesuite.net", "Akamai"}, {"edgekey.net", "Akamai"}, {"akamaized.net", "Akamai"}} {
		if result.CanonicalName == rule.suffix || strings.HasSuffix(result.CanonicalName, "."+rule.suffix) {
			providers[rule.provider] = true
			result.Evidence = append(result.Evidence, "DNS 名称匹配 "+rule.suffix)
		}
	}
	seen := map[string]bool{}
	for _, item := range ip.ips {
		addr, ok := netip.AddrFromSlice(item.IP)
		if !ok {
			continue
		}
		addr = addr.Unmap()
		if seen[addr.String()] {
			continue
		}
		seen[addr.String()] = true
		result.Addresses = append(result.Addresses, addr.String())
		for _, prefix := range cloudflarePrefixes {
			if prefix.Contains(addr) {
				providers["Cloudflare"] = true
				result.Evidence = append(result.Evidence, addr.String()+" 属于 Cloudflare 公布网段 "+prefix.String())
				break
			}
		}
	}
	for provider := range providers {
		result.Providers = append(result.Providers, provider)
	}
	sort.Strings(result.Providers)
	sort.Strings(result.Addresses)
	sort.Strings(result.Evidence)
	if len(providers) > 0 {
		result.Status = "detected"
	} else if len(result.Addresses) == 0 {
		result.Status = "inconclusive"
	}
	return result
}

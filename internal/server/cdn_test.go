package server

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fakeCDNDNS struct {
	cname           string
	addresses       []net.IPAddr
	cnameErr, ipErr error
	block           bool
}

func (f fakeCDNDNS) LookupCNAME(ctx context.Context, host string) (string, error) {
	if f.block {
		<-ctx.Done()
		return "", ctx.Err()
	}
	return f.cname, f.cnameErr
}
func (f fakeCDNDNS) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	if f.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return f.addresses, f.ipErr
}
func TestCDNSignatures(t *testing.T) {
	for _, tc := range []struct{ name, cname, ip, provider string }{
		{"cloudfront", "d123.cloudfront.net.", "192.0.2.1", "CloudFront"},
		{"fastly", "example.global.prod.fastly.net.", "192.0.2.1", "Fastly"},
		{"akamai", "example.edgekey.net.", "192.0.2.1", "Akamai"},
		{"spoof", "cloudfront.net.evil.example.", "192.0.2.1", ""},
		{"boundary", "evilcloudfront.net.", "192.0.2.1", ""},
		{"cf4", "example.com.", "104.16.0.1", "Cloudflare"},
		{"cf6", "example.com.", "2606:4700::1", "Cloudflare"},
		{"unknown", "example.com.", "192.0.2.1", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := cdnChecker{resolver: fakeCDNDNS{cname: tc.cname, addresses: []net.IPAddr{{IP: net.ParseIP(tc.ip)}}}}
			result := c.check(context.Background(), "example.com")
			if strings.Join(result.Providers, ",") != tc.provider {
				t.Fatalf("%+v", result)
			}
			if tc.provider == "" && result.Status != "unknown" {
				t.Fatal(result.Status)
			}
			if tc.provider != "" && (result.Status != "detected" || len(result.Evidence) == 0) {
				t.Fatal(result)
			}
		})
	}
}
func TestCDNIncompleteDNS(t *testing.T) {
	for _, tc := range []struct{ name, cname, status string }{{"failed", "", "inconclusive"}, {"partial", "d.cloudfront.net.", "detected"}} {
		t.Run(tc.name, func(t *testing.T) {
			f := fakeCDNDNS{cname: tc.cname, ipErr: errors.New("DNS failure")}
			if tc.cname == "" {
				f.cnameErr = f.ipErr
			}
			result := (&cdnChecker{resolver: f}).check(context.Background(), "example.com")
			if result.Status != tc.status || len(result.Warnings) == 0 {
				t.Fatal(result)
			}
		})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	result := (&cdnChecker{resolver: fakeCDNDNS{block: true}}).check(ctx, "example.com")
	if result.Status != "inconclusive" {
		t.Fatal(result)
	}
}
func TestCDNEndpoint(t *testing.T) {
	app, _ := setup(t)
	app.cdn.resolver = fakeCDNDNS{cname: "d.cloudfront.net.", addresses: []net.IPAddr{{IP: net.ParseIP("192.0.2.1")}}}
	path := "/api/nodes/check-cdn"
	if got := call(app, "POST", path, map[string]string{"sni": "example.com"}, nil, ""); got.Code != 401 {
		t.Fatal(got.Code)
	}
	cookie := login(t, app)
	req := httptest.NewRequest("POST", path, strings.NewReader(`{"sni":"example.com"}`))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatal(rec.Code)
	}
	before := call(app, "GET", "/api/state", nil, cookie, "").Body.String()
	for _, domain := range []string{"", "localhost", "1.2.3.4", "https://example.com", "example.com:443", "a..com"} {
		got := call(app, "POST", path, map[string]string{"sni": domain}, cookie, "")
		if got.Code != 400 {
			t.Fatalf("%s: %d", domain, got.Code)
		}
	}
	got := readJSON[cdnResult](t, call(app, "POST", path, map[string]string{"sni": " EXAMPLE.COM. "}, cookie, ""))
	if got.Domain != "example.com" || got.Status != "detected" {
		t.Fatal(got)
	}
	after := call(app, "GET", "/api/state", nil, cookie, "").Body.String()
	if before != after {
		t.Fatal("CDN check mutated state")
	}
	for i := 0; i < cap(app.cdn.slots); i++ {
		app.cdn.slots <- struct{}{}
	}
	if got := call(app, "POST", path, map[string]string{"sni": "example.com"}, cookie, ""); got.Code != 429 {
		t.Fatal(got.Code)
	}
}

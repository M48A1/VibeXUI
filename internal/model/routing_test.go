package model

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func routingFixture() State {
	return State{Servers: []Server{{ID: "a", Host: "a.example", Routing: RoutingSettings{DefaultOutbound: "exit", DomainStrategy: "IPIfNonMatch"}}, {ID: "b", Host: "b.example"}}, Nodes: []Node{{ID: "in-a", ServerID: "a"}, {ID: "in-b", ServerID: "b"}}, Outbounds: []Outbound{{ID: "exit", ServerID: "a", Name: "SS2022", Enabled: true, Protocol: "shadowsocks", Address: "127.0.0.1", Port: 8443, Method: "2022-blake3-aes-128-gcm", Password: base64.StdEncoding.EncodeToString([]byte("0123456789abcdef"))}}, Rules: []RouteRule{{ID: "one", ServerID: "a", Name: "domain", Enabled: true, OutboundID: "exit", Domains: []string{"example.org"}}, {ID: "two", ServerID: "a", Name: "ip", Enabled: true, OutboundID: "direct", IPs: []string{"127.0.0.0/8"}}}}
}
func TestRoutingGeneration(t *testing.T) {
	st := routingFixture()
	out, r, err := routingConfig(st, "a")
	if err != nil {
		t.Fatal(err)
	}
	if out[0].(map[string]any)["tag"] != "out-exit" || r["domainStrategy"] != "IPIfNonMatch" {
		t.Fatal("default outbound or strategy lost")
	}
	rules := r["rules"].([]any)
	if len(rules) != 2 {
		t.Fatal("catch-all rule must not suppress DNS fallback")
	}
	if rules[0].(map[string]any)["domain"].([]string)[0] != "domain:example.org" {
		t.Fatal("domain suffix matching lost")
	}
	st.Rules[0].InboundIDs = []string{"in-a"}
	_, r, err = routingConfig(st, "a")
	if err != nil || r["rules"].([]any)[0].(map[string]any)["inboundTag"].([]string)[0] != "in-a" {
		t.Fatal("inbound tag mismatch", err)
	}
	out, r, err = routingConfig(st, "b")
	if err != nil || len(out) != 2 || len(r["rules"].([]any)) != 0 || out[0].(map[string]any)["tag"] != "direct" {
		t.Fatal("cross-server routing leak", err)
	}
}
func TestRoutingRejectsBrokenReferences(t *testing.T) {
	cases := map[string]func(*State){"disabled default": func(s *State) { s.Outbounds[0].Enabled = false }, "missing target": func(s *State) { s.Rules[0].OutboundID = "missing" }, "cross-server inbound": func(s *State) { s.Rules[0].InboundIDs = []string{"in-b"} }, "empty rule": func(s *State) { s.Rules[0].Domains = nil }, "geoip unavailable": func(s *State) { s.Rules[1].IPs = []string{"geoip:cn"} }, "geosite unavailable": func(s *State) { s.Rules[0].Domains = []string{"geosite:cn"} }, "bad regex": func(s *State) { s.Rules[0].Domains = []string{"regexp:["} }, "bad port": func(s *State) { s.Rules[0].Port = "443-1" }, "bad key": func(s *State) { s.Outbounds[0].Password = "password" }, "self loop": func(s *State) { s.Outbounds[0].Address = "a.example"; s.Nodes[0].Port = 8443 }}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			st := routingFixture()
			mutate(&st)
			if ValidateRouting(st, "a") == nil {
				t.Fatal("invalid routing accepted")
			}
		})
	}
	st := routingFixture()
	st.Rules[0].Enabled = false
	st.Rules[0].InboundIDs = []string{"in-a"}
	if CheckNodeRoutingDelete(st, "in-a") == nil {
		t.Fatal("disabled rule reference discarded")
	}
}
func xrayBinary(t *testing.T) string {
	t.Helper()
	p := os.Getenv("XRAY_TEST_BINARY")
	if p == "" {
		t.Skip("set XRAY_TEST_BINARY to run real Xray validation / forwarding")
	}
	return p
}
func TestRoutingRealXrayConfig(t *testing.T) {
	bin := xrayBinary(t)
	st := routingFixture()
	_, pub, err := Keys()
	if err != nil {
		t.Fatal(err)
	}
	st.Outbounds = append(st.Outbounds, Outbound{ID: "vless", ServerID: "a", Name: "VLESS REALITY", Enabled: true, Protocol: "vless", Address: "b.example", Port: 443, UUID: UUID(), Flow: "xtls-rprx-vision", Security: "reality", ServerName: "example.com", PublicKey: pub, ShortID: "aabb", Fingerprint: "chrome"})
	for _, security := range []string{"reality", "tls"} {
		st.Outbounds[1].Security = security
		cfg, err := Config(st, "a")
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, cfg, 0600); err != nil {
			t.Fatal(err)
		}
		if output, err := exec.Command(bin, "run", "-test", "-config", path).CombinedOutput(); err != nil {
			t.Fatalf("%s: %v\n%s", security, err, output)
		}
	}
}
func freePort(t *testing.T) int {
	t.Helper()
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	p := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return p
}
func startRoutingXray(t *testing.T, bin string, config any, port int) {
	t.Helper()
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	log, err := os.Create(filepath.Join(dir, "xray.log"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "run", "-config", path)
	cmd.Stdout = log
	cmd.Stderr = log
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait(); log.Close() })
	for i := 0; i < 100; i++ {
		c, e := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 30*time.Millisecond)
		if e == nil {
			c.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	output, _ := os.ReadFile(log.Name())
	t.Fatalf("Xray did not listen: %s", output)
}

// Tests generated routing/outbounds unchanged, with a SOCKS test ingress in place of a client TLS connection.
func TestRoutingRealChainAndSplit(t *testing.T) {
	bin := xrayBinary(t)
	blocked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "SHOULD_NOT_REACH") }))
	defer blocked.Close()
	_, blockedPort, _ := net.SplitHostPort(strings.TrimPrefix(blocked.URL, "http://"))
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		fmt.Fprint(w, host)
	}))
	defer target.Close()
	_, targetPort, _ := net.SplitHostPort(strings.TrimPrefix(target.URL, "http://"))
	st := routingFixture()
	landingPort, entryPort := freePort(t), freePort(t)
	st.Outbounds[0].Port = landingPort
	startRoutingXray(t, bin, map[string]any{"inbounds": []any{map[string]any{"listen": "127.0.0.1", "port": landingPort, "protocol": "shadowsocks", "settings": map[string]any{"method": st.Outbounds[0].Method, "password": st.Outbounds[0].Password, "network": "tcp,udp"}}}, "outbounds": []any{map[string]any{"protocol": "freedom", "sendThrough": "127.0.0.2"}}}, landingPort)
	// Domain rule -> B; IP rule -> A direct; unmatched -> B; port rule -> block.
	st.Rules = []RouteRule{{ID: "domain", ServerID: "a", Name: "domain", Enabled: true, OutboundID: "exit", Domains: []string{"full:localhost"}}, {ID: "block", ServerID: "a", Name: "block", Enabled: true, OutboundID: "block", Port: blockedPort}, {ID: "ip", ServerID: "a", Name: "ip", Enabled: true, OutboundID: "direct", IPs: []string{"127.0.0.1"}}}
	raw, err := Config(st, "a")
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err = json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	delete(cfg, "api")
	cfg["inbounds"] = []any{map[string]any{"tag": "test-socks", "listen": "127.0.0.1", "port": entryPort, "protocol": "socks", "settings": map[string]any{"auth": "noauth", "udp": true}}}
	startRoutingXray(t, bin, cfg, entryPort)
	fetch := func(host, port string) (string, error) {
		c, e := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", entryPort), time.Second)
		if e != nil {
			return "", e
		}
		defer c.Close()
		c.SetDeadline(time.Now().Add(3 * time.Second))
		if _, e = c.Write([]byte{5, 1, 0}); e != nil {
			return "", e
		}
		reply := make([]byte, 2)
		if _, e = io.ReadFull(c, reply); e != nil {
			return "", e
		}
		p, _ := strconv.Atoi(port)
		request := append([]byte{5, 1, 0, 3, byte(len(host))}, []byte(host)...)
		request = append(request, byte(p>>8), byte(p))
		if _, e = c.Write(request); e != nil {
			return "", e
		}
		header := make([]byte, 4)
		if _, e = io.ReadFull(c, header); e != nil {
			return "", e
		}
		if header[1] != 0 {
			return "", fmt.Errorf("SOCKS connect rejected %d", header[1])
		}
		n := 0
		switch header[3] {
		case 1:
			n = 4
		case 4:
			n = 16
		case 3:
			if _, e = io.ReadFull(c, reply[:1]); e != nil {
				return "", e
			}
			n = int(reply[0])
		default:
			return "", fmt.Errorf("SOCKS invalid reply")
		}
		if _, e = io.CopyN(io.Discard, c, int64(n+2)); e != nil {
			return "", e
		}
		fmt.Fprintf(c, "GET / HTTP/1.0\r\nHost: %s\r\n\r\n", host)
		response, e := http.ReadResponse(bufio.NewReader(c), nil)
		if e != nil {
			return "", e
		}
		defer response.Body.Close()
		b, e := io.ReadAll(response.Body)
		return string(b), e
	}
	for _, tc := range []struct{ host, want string }{{"localhost", "127.0.0.2"}, {"127.0.0.1", "127.0.0.1"}} {
		body, err := fetch(tc.host, targetPort)
		if err != nil || !strings.HasSuffix(body, tc.want) {
			t.Fatalf("%s wanted exit %s, got %q %v", tc.host, tc.want, body, err)
		}
	}
	if body, _ := fetch("127.0.0.1", blockedPort); strings.Contains(body, "SHOULD_NOT_REACH") {
		t.Fatal("blocked request reached destination")
	}
	// No rule matches localhost.localdomain reliably; use a second target at .2 to exercise first-outbound fallback.
	l, err := net.Listen("tcp", "127.0.0.2:0")
	if err != nil {
		t.Fatal(err)
	}
	fallback := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h, _, _ := net.SplitHostPort(r.RemoteAddr)
		fmt.Fprint(w, h)
	}))
	fallback.Listener = l
	fallback.Start()
	defer fallback.Close()
	_, p, _ := net.SplitHostPort(l.Addr().String())
	body, err := fetch("127.0.0.2", p)
	if err != nil || !strings.HasSuffix(body, "127.0.0.2") {
		t.Fatalf("default route did not use B: %q %v", body, err)
	}
}

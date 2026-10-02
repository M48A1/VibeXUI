package agent

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
	"vibexui/internal/model"
)

// Exercise actual VLESS connections and Xray online stats. Only the transport
// is made plaintext for this local test; user identities, policy, inbounds and
// generated per-inbound revocation remain the production configuration.
func TestRealVLESSOnlineIPsAndScopedRevocation(t *testing.T) {
	binary := os.Getenv("XRAY_TEST_BINARY")
	if binary == "" {
		t.Skip("set XRAY_TEST_BINARY for VLESS online IP integration")
	}
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatal(err)
	}
	var source net.IP
	for _, address := range addresses {
		ip, _, e := net.ParseCIDR(address.String())
		if e == nil && ip.To4() != nil && !ip.IsLoopback() {
			source = ip.To4()
			break
		}
	}
	if source == nil {
		t.Skip("no local non-loopback IPv4; Xray excludes localhost from online stats")
	}
	echo, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	connections := make(chan net.Conn, 16)
	go func() {
		for {
			c, e := echo.Accept()
			if e != nil {
				return
			}
			connections <- c
			go func() { defer c.Close(); io.Copy(c, c) }()
		}
	}()
	defer func() {
		for {
			select {
			case c := <-connections:
				c.Close()
			default:
				return
			}
		}
	}()
	port := func() int {
		ln, e := net.Listen("tcp4", "127.0.0.1:0")
		if e != nil {
			t.Fatal(e)
		}
		p := ln.Addr().(*net.TCPAddr).Port
		ln.Close()
		return p
	}
	private, public, err := model.Keys()
	if err != nil {
		t.Fatal(err)
	}
	st := model.State{Servers: []model.Server{{ID: "s"}}, Nodes: []model.Node{
		{ID: "n", ServerID: "s", Name: "first", Port: port(), SNI: "example.com", Target: "example.com:443", PrivateKey: private, PublicKey: public, ShortID: "1234567890abcdef", Enabled: true},
		{ID: "m", ServerID: "s", Name: "second", Port: port(), SNI: "example.com", Target: "example.com:443", PrivateKey: private, PublicKey: public, ShortID: "1234567890abcdef", Enabled: true},
	}, Clients: []model.Client{
		{ID: "alice", UUID: model.UUID(), Enabled: true, NodeIDs: []string{"n", "m"}, NodeIPLimits: map[string]int{"n": 1, "m": 1}},
		{ID: "bob", UUID: model.UUID(), Enabled: true, NodeIDs: []string{"n"}},
	}}
	config := func() json.RawMessage {
		raw, e := model.Config(st, "s")
		if e != nil {
			t.Fatal(e)
		}
		var value map[string]any
		if e = json.Unmarshal(raw, &value); e != nil {
			t.Fatal(e)
		}
		for _, entry := range value["inbounds"].([]any) {
			in := entry.(map[string]any)
			in["listen"] = "127.0.0.1"
			in["streamSettings"] = map[string]any{"network": "raw", "security": "none"}
			for _, client := range in["settings"].(map[string]any)["clients"].([]any) {
				delete(client.(map[string]any), "flow")
			}
		}
		raw, e = json.Marshal(value)
		if e != nil {
			t.Fatal(e)
		}
		return raw
	}
	a, err := New(Options{Panel: "http://localhost:8080", ID: "s", Directory: t.TempDir(), Xray: binary})
	if err != nil {
		t.Fatal(err)
	}
	defer a.stop()
	apply := func(version int64) {
		t.Helper()
		if e := a.apply(context.Background(), model.Task{Version: version, Running: true, Config: config()}); e != nil {
			t.Fatal(e)
		}
	}
	apply(1)
	a.state.IPBindings = []string{"alice.n", "alice.m"}
	dial := func(client int, node int) (net.Conn, error) {
		d := net.Dialer{LocalAddr: &net.TCPAddr{IP: source}, Timeout: 2 * time.Second}
		c, e := d.Dial("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(st.Nodes[node].Port)))
		if e != nil {
			return nil, e
		}
		c.SetDeadline(time.Now().Add(2 * time.Second))
		id, e := hex.DecodeString(strings.ReplaceAll(st.Clients[client].UUID, "-", ""))
		if e != nil {
			c.Close()
			return nil, e
		}
		targetPort := echo.Addr().(*net.TCPAddr).Port
		request := append([]byte{0}, id...)
		request = append(request, 0, 1, byte(targetPort>>8), byte(targetPort), 1, 127, 0, 0, 1, 'Z')
		if _, e = c.Write(request); e != nil {
			c.Close()
			return nil, e
		}
		response := make([]byte, 3)
		if _, e = io.ReadFull(c, response); e != nil {
			c.Close()
			return nil, e
		}
		if string(response) != string([]byte{0, 0, 'Z'}) {
			c.Close()
			return nil, fmt.Errorf("unexpected VLESS response %x", response)
		}
		c.SetDeadline(time.Time{})
		return c, nil
	}
	first, err := dial(0, 0)
	if err != nil {
		t.Fatal("local VLESS connection failed", err)
	}
	defer first.Close()
	another, err := dial(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer another.Close()
	second, err := dial(0, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	sample, message := a.onlineIPs(context.Background())
	if message != "" || len(sample["alice.n"]) != 1 || len(sample["alice.m"]) != 1 || sample["alice.n"][0] != source.String() {
		t.Fatal("live connection sample did not separate or deduplicate IPs", sample, message)
	}
	st.Clients[0].NodeIPStates = map[string]model.NodeIPState{"n": {BlockedUntil: time.Now().Add(time.Minute)}}
	apply(2)
	if c, e := dial(0, 0); e == nil {
		c.Close()
		t.Fatal("blocked client still connected to first inbound")
	}
	allowed, e := dial(0, 1)
	if e != nil {
		t.Fatal("other inbound was revoked", e)
	}
	defer allowed.Close()
	other, e := dial(1, 0)
	if e != nil {
		t.Fatal("other client was revoked", e)
	}
	defer other.Close()
	sample, message = a.onlineIPs(context.Background())
	if message != "" || len(sample["alice.n"]) != 0 || len(sample["alice.m"]) != 1 {
		t.Fatal("post-revocation online sample wrong", sample, message)
	}
	st.Clients[0].NodeIPStates = nil
	apply(3)
	restored, e := dial(0, 0)
	if e != nil {
		t.Fatal("client not restored after cooldown", e)
	}
	restored.Close()
}

package agent

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
	"vibexui/internal/model"
	"vibexui/internal/server"
	"vibexui/internal/store"
)

func fake(t *testing.T) *Agent {
	t.Helper()
	dir := t.TempDir()
	binary := filepath.Join(dir, "xray")
	script := `#!/bin/sh
if [ "$1" = "version" ]; then echo 'Xray test'; exit 0; fi
if [ "$1" = "api" ]; then
  if [ -f "$0.stats" ]; then cat "$0.stats"; else echo '{"stat":[{"name":"inbound>>>node>>>traffic>>>uplink","value":"100"},{"name":"inbound>>>node>>>traffic>>>downlink","value":"200"}]}'; fi
  exit 0
fi
if [ "$2" = "-test" ]; then
  if grep -q reject "$4"; then echo 'invalid config'; exit 1; fi
  exit 0
fi
if grep -q crash "$3"; then exit 1; fi
trap 'exit 0' INT TERM
while true; do sleep 1; done
`
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	a, err := New(Options{Panel: "http://localhost:8080", ID: "test", Directory: filepath.Join(dir, "data"), Xray: binary})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.stop)
	return a
}

func TestRealPanelAgentRoundTrip(t *testing.T) {
	binary := os.Getenv("XRAY_TEST_BINARY")
	if binary == "" {
		t.Skip("set XRAY_TEST_BINARY for panel/agent integration")
	}
	db, err := store.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	app, err := server.New(db, server.Options{Username: "admin", Password: "integration-password", PublicURL: "http://localhost:8080"})
	if err != nil {
		t.Fatal(err)
	}
	panel := httptest.NewServer(app)
	defer panel.Close()
	token := model.Secret()
	private, public, err := model.Keys()
	if err != nil {
		t.Fatal(err)
	}
	err = db.Update(func(st *model.State) error {
		st.Servers = []model.Server{{ID: "server", Name: "local", Host: "127.0.0.1", RegistrationHash: model.Hash(token), RegistrationExpires: time.Now().Add(time.Minute), Version: 1, DesiredRunning: true}}
		st.Nodes = []model.Node{{ID: "node", ServerID: "server", Name: "test", Port: 19444, SNI: "example.com", Target: "example.com:443", PrivateKey: private, PublicKey: public, ShortID: "1234567890abcdef", Enabled: true}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(Options{Panel: panel.URL, ID: "server", RegistrationToken: token, Directory: t.TempDir(), Xray: binary, Interval: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err = a.Register(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	if a.running() {
		cancel()
		t.Fatal("registration-only started Xray")
	}
	a, err = New(Options{Panel: panel.URL, ID: "server", Directory: a.opts.Directory, Xray: binary, Interval: time.Second})
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	wait := func(check func(model.Server) bool) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			st, err := db.View()
			if err != nil {
				t.Fatal(err)
			}
			if check(st.Servers[0]) {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		st, _ := db.View()
		t.Fatalf("Agent state did not converge: %+v", st.Servers[0])
	}
	wait(func(s model.Server) bool {
		return s.Running && s.AppliedVersion == 1 && s.Error == "" && s.StatsError == ""
	})
	if err = db.Update(func(st *model.State) error { st.Servers[0].DesiredRunning = false; return nil }); err != nil {
		t.Fatal(err)
	}
	wait(func(s model.Server) bool { return !s.Running })
	if err = db.Update(func(st *model.State) error { st.Servers[0].DesiredRunning = true; st.Servers[0].Version++; return nil }); err != nil {
		t.Fatal(err)
	}
	wait(func(s model.Server) bool { return s.Running && s.AppliedVersion == 2 && s.Error == "" })
}
func TestApplyRollbackAndStats(t *testing.T) {
	a := fake(t)
	ctx := context.Background()
	good := json.RawMessage(`{"log":{}}`)
	if err := a.apply(ctx, model.Task{Version: 1, Running: true, Config: good}); err != nil {
		t.Fatal(err)
	}
	if !a.running() || a.state.Version != 1 {
		t.Fatal("initial config not running")
	}
	a.stats(ctx)
	a.stats(ctx)
	if a.state.Upload != 100 || a.state.Download != 200 || a.statsError != "" {
		t.Fatal("stats duplicated or parse failed", a.state, a.statsError)
	}
	if err := a.apply(ctx, model.Task{Version: 2, Running: true, Config: json.RawMessage(`{"reject":true}`)}); err == nil {
		t.Fatal("invalid config accepted")
	}
	if !a.running() || a.state.Version != 1 {
		t.Fatal("validation failure interrupted working process")
	}
	if err := a.apply(ctx, model.Task{Version: 2, Running: true, Config: json.RawMessage(`{"crash":true}`)}); err == nil {
		t.Fatal("crashed config accepted")
	}
	data, _ := os.ReadFile(filepath.Join(a.opts.Directory, "config.json"))
	if string(data) != string(good) || a.state.Version != 1 || !a.running() {
		t.Fatal("startup failure did not restore previous config")
	}
	a.stop()
	if a.running() {
		t.Fatal("stop failed")
	}
}
func TestAgentRefusesPublicPlaintext(t *testing.T) {
	_, err := New(Options{Panel: "http://panel.example.com", ID: "test", Directory: t.TempDir(), Xray: "not-needed"})
	if err == nil {
		t.Fatal("public plaintext accepted")
	}
}
func TestRealXrayConfiguration(t *testing.T) {
	binary := os.Getenv("XRAY_TEST_BINARY")
	if binary == "" {
		t.Skip("set XRAY_TEST_BINARY to validate with official Xray-core")
	}
	private, public, err := model.Keys()
	if err != nil {
		t.Fatal(err)
	}
	st := model.State{Nodes: []model.Node{{Listen: "127.0.0.1", Sniffing: true, SniffingRouteOnly: true, ServerNames: []string{"www.example.com"}, MinClientVersion: "1.0.0", MaxTimeDiff: 60000, ID: "node", ServerID: "server", Name: "node", Port: 19443, SNI: "example.com", Target: "example.com:443", PrivateKey: private, PublicKey: public, ShortID: "1234567890abcdef", Enabled: true}}, Clients: []model.Client{{ReverseTag: "reverse-test", ID: "client", UUID: model.UUID(), Enabled: true, NodeIDs: []string{"node"}}}}
	conf, err := model.Config(st, "server")
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(Options{Panel: "http://localhost:8080", ID: "server", Directory: t.TempDir(), Xray: binary})
	if err != nil {
		t.Fatal(err)
	}
	defer a.stop()
	if err = a.apply(context.Background(), model.Task{Version: 1, Running: true, Config: conf}); err != nil {
		t.Fatal(err)
	}
	a.stats(context.Background())
	if a.statsError != "" {
		t.Fatal(a.statsError)
	}
}

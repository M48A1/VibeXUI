package agent

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"vibexui/internal/model"
)

func onlineFake(t *testing.T) *Agent {
	t.Helper()
	a := fake(t)
	script, err := os.ReadFile(a.opts.Xray)
	if err != nil {
		t.Fatal(err)
	}
	extra := `if [ "$1" = "api" ]; then
 case "$2" in
 statsgetallonlineusers) cat "$0.users"; exit $? ;;
 statsonlineiplist) for arg in "$@"; do case "$arg" in -email=*) key="${arg#-email=}" ;; esac; done; cat "$0.online-$key"; exit $? ;;
 esac
`
	script = []byte(strings.Replace(string(script), `if [ "$1" = "api" ]; then`, extra, 1))
	if err = os.WriteFile(a.opts.Xray, script, 0700); err != nil {
		t.Fatal(err)
	}
	if err = a.apply(context.Background(), model.Task{Version: 1, Running: true, Config: json.RawMessage(`{"log":{}}`)}); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestOnlineCollectorSeparatesInboundBindings(t *testing.T) {
	a := onlineFake(t)
	a.state.IPBindings = []string{"alice.na", "alice.nb", "bob.na"}
	os.WriteFile(a.opts.Xray+".users", []byte(`{"users":["user>>>alice.na>>>online","user>>>alice.nb>>>online","user>>>unlimited.na>>>online"]}`), 0600)
	os.WriteFile(a.opts.Xray+".online-alice.na", []byte(`{"name":"user>>>alice.na>>>online","ips":{"203.0.113.1":"100","::ffff:203.0.113.1":"101","203.0.113.2":"102"}}`), 0600)
	os.WriteFile(a.opts.Xray+".online-alice.nb", []byte(`{"name":"user>>>alice.nb>>>online","ips":{"[2001:db8::1]":"103"}}`), 0600)
	got, err := a.onlineIPs(context.Background())
	if err != "" {
		t.Fatal(err)
	}
	want := map[string][]string{"alice.na": {"203.0.113.1", "203.0.113.2"}, "alice.nb": {"2001:db8::1"}, "bob.na": {}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("bad sample: %+v", got)
	}
	// Traffic must still aggregate the new per-inbound emails to the same user.
	os.WriteFile(a.opts.Xray+".stats", []byte(`{"stat":[{"name":"user>>>alice.na>>>traffic>>>uplink","value":"30"},{"name":"user>>>alice.nb>>>traffic>>>uplink","value":"20"},{"name":"user>>>bob.na>>>traffic>>>uplink","value":"5"}]}`), 0600)
	a.stats(context.Background())
	a.stats(context.Background())
	if a.state.ClientTraffic["alice"].Upload != 50 || a.state.ClientTraffic["bob"].Upload != 5 {
		t.Fatal("binding names broke user accounting", a.state.ClientTraffic)
	}
}

func TestOnlineCollectionFailureIsUnknown(t *testing.T) {
	a := onlineFake(t)
	a.state.IPBindings = []string{"alice.na"}
	// Missing fixture simulates an unsupported or failed API.
	sample, err := a.onlineIPs(context.Background())
	if sample != nil || err == "" {
		t.Fatal("failed API reported zero online IPs")
	}
	os.WriteFile(a.opts.Xray+".users", []byte(`{"users":["user>>>alice.na>>>online"]}`), 0600)
	os.WriteFile(a.opts.Xray+".online-alice.na", []byte(`{"ips":{"bad-ip":"123"}}`), 0600)
	sample, err = a.onlineIPs(context.Background())
	if sample != nil || err == "" {
		t.Fatal("invalid IP sample accepted")
	}
	a.stop()
	sample, err = a.onlineIPs(context.Background())
	if sample == nil || err != "" || len(sample) != 0 {
		t.Fatal("stopped Xray should report no connected IPs")
	}
}

func TestRealXrayOnlineAPI(t *testing.T) {
	binary := os.Getenv("XRAY_TEST_BINARY")
	if binary == "" {
		t.Skip("set XRAY_TEST_BINARY for online API integration")
	}
	private, public, err := model.Keys()
	if err != nil {
		t.Fatal(err)
	}
	st := model.State{Nodes: []model.Node{{ID: "n", ServerID: "s", Name: "test", Port: 19446, SNI: "example.com", Target: "example.com:443", PrivateKey: private, PublicKey: public, ShortID: "1234567890abcdef", Enabled: true}}, Clients: []model.Client{{ID: "alice", UUID: model.UUID(), Enabled: true, NodeIDs: []string{"n"}, NodeIPLimits: map[string]int{"n": 1}}}}
	config, err := model.Config(st, "s")
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(Options{Panel: "http://localhost:8080", ID: "s", Directory: t.TempDir(), Xray: binary})
	if err != nil {
		t.Fatal(err)
	}
	defer a.stop()
	if err = a.apply(context.Background(), model.Task{Version: 1, Running: true, Config: config}); err != nil {
		t.Fatal(err)
	}
	a.state.IPBindings = []string{"alice.n"}
	sample, message := a.onlineIPs(context.Background())
	if message != "" || sample == nil || len(sample["alice.n"]) != 0 {
		t.Fatal("official Xray online API not supported", message, sample)
	}
}

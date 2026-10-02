package server

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"vibexui/internal/model"
)

func TestRoutingAPILifecycle(t *testing.T) {
	app, db := setup(t)
	cookie := login(t, app)
	if err := db.Update(func(s *model.State) error {
		s.Servers = []model.Server{{ID: "a", Host: "a.example", Version: 1}, {ID: "b", Host: "b.example", Version: 1}}
		s.Nodes = []model.Node{{ID: "in-a", ServerID: "a"}, {ID: "in-b", ServerID: "b"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	o := model.Outbound{ServerID: "a", Name: "Landing", Enabled: true, Protocol: "shadowsocks", Address: "b.example", Port: 443, Method: "2022-blake3-aes-128-gcm", Password: base64.StdEncoding.EncodeToString([]byte("0123456789abcdef"))}
	if r := call(app, "POST", "/api/outbounds", o, nil, ""); r.Code != 401 {
		t.Fatal("missing auth", r.Code)
	}
	req := httptest.NewRequest("POST", "/api/outbounds", strings.NewReader(`{}`))
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	app.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatal("missing CSRF protection", w.Code)
	}
	result := readJSON[map[string]string](t, call(app, "POST", "/api/outbounds", o, cookie, ""))
	o.ID = result["id"]
	saved, err := db.View()
	if err != nil {
		t.Fatal(err)
	}
	if saved.Servers[0].Version != 2 || saved.Servers[1].Version != 1 {
		t.Fatal("version isolation failed")
	}
	public := call(app, "GET", "/api/state", nil, cookie, "")
	if strings.Contains(public.Body.String(), o.Password) {
		t.Fatal("secret leaked into state")
	}
	copy := readJSON[model.State](t, public).Outbounds[0]
	copy.Name = "renamed"
	readJSON[map[string]string](t, call(app, "PUT", "/api/outbounds/"+o.ID, copy, cookie, ""))
	saved, _ = db.View()
	if saved.Outbounds[0].Password != o.Password {
		t.Fatal("blank edit erased secret")
	}
	r := model.RouteRule{ServerID: "a", Name: "rule", Enabled: true, OutboundID: o.ID, Domains: []string{"example.com"}, InboundIDs: []string{"in-a"}}
	r.ID = readJSON[map[string]string](t, call(app, "POST", "/api/routing/rules", r, cookie, ""))["id"]
	before, _ := db.View()
	o.Enabled = false
	for _, res := range []*httptest.ResponseRecorder{call(app, "PUT", "/api/outbounds/"+o.ID, o, cookie, ""), call(app, "DELETE", "/api/outbounds/"+o.ID, nil, cookie, ""), call(app, "DELETE", "/api/nodes/in-a", nil, cookie, ""), call(app, "POST", "/api/nodes/bulk", map[string]any{"ids": []string{"in-a"}, "action": "delete"}, cookie, "")} {
		if res.Code != 400 {
			t.Fatalf("unsafe change accepted: %d %s", res.Code, res.Body.String())
		}
	}
	after, _ := db.View()
	a, _ := json.Marshal(before)
	b, _ := json.Marshal(after)
	if string(a) != string(b) {
		t.Fatal("failed mutation changed state")
	}
	r.ServerID = "b"
	if res := call(app, "POST", "/api/routing/rules", r, cookie, ""); res.Code != 400 {
		t.Fatal("cross-server rule accepted")
	}
	r.ServerID = "a"
	second := r
	second.ID = ""
	second.Name = "second"
	second.ID = readJSON[map[string]string](t, call(app, "POST", "/api/routing/rules", second, cookie, ""))["id"]
	readJSON[map[string]bool](t, call(app, "PUT", "/api/servers/a/routing/rules/order", map[string]any{"ids": []string{second.ID, r.ID}}, cookie, ""))
	saved, _ = db.View()
	if saved.Rules[0].ID != second.ID {
		t.Fatal("rule order lost")
	}
	for _, ids := range [][]string{{r.ID}, {r.ID, r.ID}, {r.ID, "unknown"}} {
		if res := call(app, "PUT", "/api/servers/a/routing/rules/order", map[string]any{"ids": ids}, cookie, ""); res.Code != 400 {
			t.Fatal("invalid order accepted")
		}
	}
	readJSON[map[string]bool](t, call(app, "PUT", "/api/servers/a/routing", model.RoutingSettings{DefaultOutbound: o.ID, DomainStrategy: "IPIfNonMatch"}, cookie, ""))
	saved, _ = db.View()
	cfg, err := model.Config(saved, "a")
	if err != nil || !strings.Contains(string(cfg), "out-"+o.ID) {
		t.Fatal("saved routes not generated", err)
	}
	// Delete rules; the default alone must still protect its outbound.
	for _, id := range []string{r.ID, second.ID} {
		readJSON[map[string]bool](t, call(app, "DELETE", "/api/routing/rules/"+id, nil, cookie, ""))
	}
	if res := call(app, "DELETE", "/api/outbounds/"+o.ID, nil, cookie, ""); res.Code != 400 {
		t.Fatal("default reference not protected")
	}
	readJSON[map[string]bool](t, call(app, "DELETE", "/api/servers/a", nil, cookie, ""))
	saved, _ = db.View()
	if len(saved.Outbounds) != 0 || len(saved.Rules) != 0 || len(saved.Servers) != 1 || len(saved.Nodes) != 1 {
		t.Fatal("server deletion left orphan routing")
	}
}
func TestOutboundFromManagedNode(t *testing.T) {
	app, db := setup(t)
	cookie := login(t, app)
	private, public, err := model.Keys()
	if err != nil {
		t.Fatal(err)
	}
	uuid := model.UUID()
	if err = db.Update(func(st *model.State) error {
		st.Servers = []model.Server{{ID: "a", Host: "a.example"}, {ID: "b", Host: "b.example"}}
		st.Nodes = []model.Node{{ID: "b-node", ServerID: "b", Name: "Landing", Enabled: true, Port: 443, SNI: "example.com", PublicKey: public, PrivateKey: private, ShortID: "abcd"}}
		st.Clients = []model.Client{{ID: "client", Enabled: true, UUID: uuid, NodeIDs: []string{"b-node"}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	body := map[string]string{"serverId": "a", "nodeId": "b-node", "clientId": "client"}
	readJSON[map[string]string](t, call(app, "POST", "/api/outbounds/from-node", body, cookie, ""))
	st, _ := db.View()
	if len(st.Outbounds) != 1 || st.Outbounds[0].UUID != uuid || st.Outbounds[0].PublicKey != public || st.Outbounds[0].Address != "b.example" {
		t.Fatal("copy lost credentials")
	}
	visible := call(app, "GET", "/api/state", nil, cookie, "")
	var state model.State
	json.Unmarshal(visible.Body.Bytes(), &state)
	if state.Outbounds[0].UUID != "" {
		t.Fatal("outbound UUID leaked")
	}
	body["serverId"] = "b"
	if res := call(app, "POST", "/api/outbounds/from-node", body, cookie, ""); res.Code != 400 {
		t.Fatal("self chain accepted")
	}
	body["serverId"] = "a"
	if err = db.Update(func(st *model.State) error { st.Clients[0].Enabled = false; return nil }); err != nil {
		t.Fatal(err)
	}
	if res := call(app, "POST", "/api/outbounds/from-node", body, cookie, ""); res.Code != 400 {
		t.Fatal("disabled landing client accepted")
	}
}

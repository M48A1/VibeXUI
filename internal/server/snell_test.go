package server

import (
	"strings"
	"testing"
	"vibexui/internal/model"
)

func TestSnellAPIAndCoexistence(t *testing.T) {
	app, db := setup(t)
	cookie := login(t, app)
	token := model.Secret()
	if err := db.Update(func(st *model.State) error {
		st.Servers = []model.Server{{ID: "a", Host: "example.com", SnellSupported: true, TokenHash: model.Hash(token), Version: 1}, {ID: "old", Host: "old.example"}}
		st.Nodes = []model.Node{{ID: "vless", ServerID: "a", Port: 443}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	n := model.SnellInbound{ServerID: "a", Name: "Snell", Listen: "0.0.0.0", Port: 6160, Enabled: true, TFO: true}
	if res := call(app, "POST", "/api/snell", n, nil, ""); res.Code != 401 {
		t.Fatal("missing auth")
	}
	conflict := n
	conflict.Port = 443
	if res := call(app, "POST", "/api/snell", conflict, cookie, ""); res.Code != 400 {
		t.Fatal("Xray port conflict accepted")
	}
	legacy := n
	legacy.ServerID = "old"
	if res := call(app, "POST", "/api/snell", legacy, cookie, ""); res.Code != 400 {
		t.Fatal("old Agent accepted")
	}
	id := readJSON[map[string]string](t, call(app, "POST", "/api/snell", n, cookie, ""))["id"]
	st, _ := db.View()
	secret := st.Snell[0].PSK
	if secret == "" || st.Servers[0].Version != 1 {
		t.Fatal("missing PSK or unnecessary Xray restart")
	}
	public := readJSON[model.State](t, call(app, "GET", "/api/state", nil, cookie, ""))
	if public.Snell[0].PSK != "" {
		t.Fatal("secret leaked")
	}
	exported := readJSON[map[string]string](t, call(app, "GET", "/api/snell/"+id+"/export", nil, cookie, ""))
	if !strings.Contains(exported["config"], secret) || !strings.Contains(exported["config"], "version=5") {
		t.Fatal("export missing credentials")
	}
	n.Enabled = false
	readJSON[map[string]string](t, call(app, "PUT", "/api/snell/"+id, n, cookie, ""))
	st, _ = db.View()
	if st.Snell[0].PSK != secret || st.Snell[0].Revision != 2 {
		t.Fatal("blank edit lost PSK")
	}
	task := readJSON[model.Task](t, call(app, "POST", "/api/agent/poll", map[string]any{"id": "a", "appliedVersion": 1, "snellSupported": true}, nil, token))
	if len(task.Snell) != 1 || task.Snell[0].PSK != secret || task.Snell[0].Enabled {
		t.Fatal("task missing Snell desired state")
	}
	if err := model.CheckSnellPort(st, "a", 6160); err == nil {
		t.Fatal("Snell reservation missing")
	}
	readJSON[map[string]bool](t, call(app, "DELETE", "/api/snell/"+id, nil, cookie, ""))
	task = readJSON[model.Task](t, call(app, "POST", "/api/agent/poll", map[string]any{"id": "a", "appliedVersion": 1, "snellSupported": true}, nil, token))
	if task.Snell == nil || len(task.Snell) != 0 {
		t.Fatal("empty deletion snapshot omitted")
	}
}

package server

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"vibexui/internal/model"
	"vibexui/internal/store"
)

func setup(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	app, err := New(db, Options{Username: "admin", Password: "test-password-123", PublicURL: "http://localhost:8080"})
	if err != nil {
		t.Fatal(err)
	}
	return app, db
}
func call(app *Server, method, path string, body any, cookie *http.Cookie, token string) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest(method, path, strings.NewReader(string(raw)))
	r.Header.Set("X-VibeXUI", "1")
	if cookie != nil {
		r.AddCookie(cookie)
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	app.ServeHTTP(w, r)
	return w
}
func login(t *testing.T, app *Server) *http.Cookie {
	t.Helper()
	r := call(app, "POST", "/api/login", map[string]string{"username": "admin", "password": "test-password-123"}, nil, "")
	if r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	return r.Result().Cookies()[0]
}
func readJSON[T any](t *testing.T, r *httptest.ResponseRecorder) T {
	t.Helper()
	if r.Code != 200 {
		t.Fatalf("HTTP %d: %s", r.Code, r.Body.String())
	}
	var v T
	if err := json.Unmarshal(r.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestAuthorizationAndCSRF(t *testing.T) {
	app, _ := setup(t)
	if r := call(app, "GET", "/api/state", nil, nil, ""); r.Code != 401 {
		t.Fatal(r.Code)
	}
	cookie := login(t, app)
	r := httptest.NewRequest("POST", "/api/servers", strings.NewReader(`{"name":"a","host":"example.com"}`))
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	app.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("missing CSRF header accepted")
	}
	r.Header.Set("X-VibeXUI", "1")
	r.Header.Set("Origin", "https://evil.example")
	w = httptest.NewRecorder()
	app.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("foreign origin accepted")
	}
	call(app, "POST", "/api/logout", nil, cookie, "")
	if r := call(app, "GET", "/api/state", nil, cookie, ""); r.Code != 401 {
		t.Fatal("logout did not revoke session")
	}
}

func TestMultiServerLifecycle(t *testing.T) {
	app, db := setup(t)
	cookie := login(t, app)
	a := readJSON[map[string]string](t, call(app, "POST", "/api/servers", map[string]string{"name": "Hong Kong", "host": "hk.example.com"}, cookie, ""))
	b := readJSON[map[string]string](t, call(app, "POST", "/api/servers", map[string]string{"name": "Tokyo", "host": "jp.example.com"}, cookie, ""))
	registered := readJSON[map[string]string](t, call(app, "POST", "/api/agent/register", map[string]string{"id": a["id"]}, nil, a["token"]))
	if call(app, "POST", "/api/agent/register", map[string]string{"id": a["id"]}, nil, a["token"]).Code != 401 {
		t.Fatal("registration token replay accepted")
	}
	if call(app, "POST", "/api/agent/poll", map[string]any{"id": b["id"], "appliedVersion": 0}, nil, registered["token"]).Code != 401 {
		t.Fatal("cross-server credentials accepted")
	}
	makeNode := func(sid, name string) model.Node {
		return readJSON[model.Node](t, call(app, "POST", "/api/nodes", map[string]any{"serverId": sid, "name": name, "port": 443, "sni": "example.com", "target": "example.com:443", "enabled": true}, cookie, ""))
	}
	n1 := makeNode(a["id"], "HK")
	n2 := makeNode(b["id"], "JP")
	if n1.PrivateKey != "" || n1.PublicKey == "" {
		t.Fatal("private key leaked or public key absent")
	}
	if call(app, "POST", "/api/nodes", map[string]any{"serverId": a["id"], "name": "duplicate", "port": 443, "sni": "example.com", "target": "example.com:443", "enabled": true}, cookie, "").Code != 400 {
		t.Fatal("duplicate port accepted")
	}
	c := readJSON[model.Client](t, call(app, "POST", "/api/clients", map[string]any{"name": "Alice", "enabled": true, "nodeIds": []string{n1.ID, n2.ID}}, cookie, ""))
	task := readJSON[model.Task](t, call(app, "POST", "/api/agent/poll", map[string]any{"id": a["id"], "appliedVersion": 0, "running": false}, nil, registered["token"]))
	if !strings.Contains(string(task.Config), c.UUID) || !task.Running {
		t.Fatal("missing client in generated config")
	}
	var conf map[string]any
	json.Unmarshal(task.Config, &conf)
	if len(conf["inbounds"].([]any)) != 1 {
		t.Fatal("foreign node included")
	}
	view := readJSON[model.State](t, call(app, "GET", "/api/state", nil, cookie, ""))
	if view.Servers[0].TokenHash != "" || view.Servers[0].RegistrationHash != "" || view.Nodes[0].PrivateKey != "" {
		t.Fatal("secrets leaked in state")
	}
	sub := call(app, "GET", "/sub/"+c.Token, nil, nil, "")
	decoded, err := base64.StdEncoding.DecodeString(sub.Body.String())
	if err != nil || len(strings.Split(string(decoded), "\n")) != 2 || !strings.Contains(string(decoded), "hk.example.com:443") {
		t.Fatal("invalid cross-server subscription", string(decoded), err)
	}
	qr := call(app, "GET", "/api/clients/"+c.ID+"/qr", nil, cookie, "")
	if qr.Code != 200 || !strings.HasPrefix(qr.Body.String(), "\x89PNG") {
		t.Fatal("invalid QR image")
	}
	synced := readJSON[model.Task](t, call(app, "POST", "/api/agent/poll", map[string]any{"id": a["id"], "appliedVersion": task.Version, "running": true, "upload": 123}, nil, registered["token"]))
	if len(synced.Config) != 0 {
		t.Fatal("unchanged config retransmitted")
	}
	readJSON[map[string]bool](t, call(app, "PATCH", "/api/servers/"+a["id"], map[string]string{"action": "stop"}, cookie, ""))
	stopped := readJSON[model.Task](t, call(app, "POST", "/api/agent/poll", map[string]any{"id": a["id"], "appliedVersion": task.Version}, nil, registered["token"]))
	if stopped.Running {
		t.Fatal("stop not delivered")
	}
	readJSON[model.Client](t, call(app, "PATCH", "/api/clients/"+c.ID, map[string]any{"name": "Alice", "enabled": false, "nodeIds": []string{n1.ID, n2.ID}}, cookie, ""))
	if call(app, "GET", "/sub/"+c.Token, nil, nil, "").Code != 404 {
		t.Fatal("disabled subscription accessible")
	}
	disabled := readJSON[model.Task](t, call(app, "POST", "/api/agent/poll", map[string]any{"id": a["id"], "appliedVersion": task.Version}, nil, registered["token"]))
	if strings.Contains(string(disabled.Config), c.UUID) {
		t.Fatal("disabled client still in config")
	}
	readJSON[map[string]string](t, call(app, "POST", "/api/servers/"+a["id"]+"/registration", nil, cookie, ""))
	if call(app, "POST", "/api/agent/poll", map[string]any{"id": a["id"]}, nil, registered["token"]).Code != 401 {
		t.Fatal("old credentials not revoked")
	}
	readJSON[map[string]bool](t, call(app, "DELETE", "/api/servers/"+a["id"], nil, cookie, ""))
	st, err := db.View()
	if err != nil || len(st.Nodes) != 1 || len(st.Clients[0].NodeIDs) != 1 {
		t.Fatal("cascaded deletion failed", err)
	}
}
func TestExpiredRegistrationAndAtomicValidation(t *testing.T) {
	app, db := setup(t)
	cookie := login(t, app)
	v := readJSON[map[string]string](t, call(app, "POST", "/api/servers", map[string]string{"name": "a", "host": "example.com"}, cookie, ""))
	db.Update(func(st *model.State) error {
		st.Servers[0].RegistrationExpires = time.Now().Add(-time.Second)
		return nil
	})
	if call(app, "POST", "/api/agent/register", map[string]string{"id": v["id"]}, nil, v["token"]).Code != 401 {
		t.Fatal("expired registration accepted")
	}
	before, _ := db.View()
	db.Update(func(st *model.State) error { st.Servers[0].Name = "corrupt"; return fmt.Errorf("invalid") })
	after, _ := db.View()
	if before.Servers[0].Name != after.Servers[0].Name {
		t.Fatal("failed mutation changed stored state")
	}
}

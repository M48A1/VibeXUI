package server

import (
	"testing"
	"time"
	"vibexui/internal/model"
)

func TestUserTrafficAggregationAndIdempotency(t *testing.T) {
	app, db := setup(t)
	token := model.Secret()
	if err := db.Update(func(st *model.State) error {
		st.Servers = []model.Server{{ID: "a", Name: "A", TokenHash: model.Hash(token), Version: 1}, {ID: "b", Name: "B", TokenHash: model.Hash(token), Version: 1}, {ID: "foreign", TokenHash: model.Hash(token), Version: 1}}
		st.Nodes = []model.Node{{ID: "na", ServerID: "a"}, {ID: "nb", ServerID: "b"}}
		st.Clients = []model.Client{{ID: "alice", Name: "Alice", Enabled: true, NodeIDs: []string{"na", "nb"}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	post := func(sid, epoch string, up, down uint64) {
		t.Helper()
		body := struct {
			ID string `json:"id"`
			model.Report
		}{ID: sid, Report: model.Report{AppliedVersion: 1, Running: true, StatsEpoch: epoch, ClientTraffic: map[string]model.Traffic{"alice": {Upload: up, Download: down}, "unrelated": {Upload: 100000}}}}
		readJSON[model.Task](t, call(app, "POST", "/api/agent/poll", body, nil, token))
	}
	check := func(up, down uint64) {
		t.Helper()
		st, err := db.View()
		if err != nil {
			t.Fatal(err)
		}
		c := st.Clients[0]
		if c.Upload != up || c.Download != down {
			t.Fatalf("wrong cumulative totals: got %d/%d want %d/%d", c.Upload, c.Download, up, down)
		}
		if c.TrafficUpdatedAt.IsZero() {
			t.Fatal("missing accounting timestamp")
		}
	}
	post("a", "epoch-a", 100, 50)
	post("a", "epoch-a", 100, 50)
	post("a", "epoch-a", 90, 30)
	check(100, 50)
	post("b", "epoch-b", 20, 30)
	post("foreign", "epoch-f", 99999, 99999)
	check(120, 80)
	post("a", "epoch-a", 130, 70)
	check(150, 100)
	post("a", "new-agent-epoch", 5, 7)
	post("a", "new-agent-epoch", 5, 7)
	check(155, 107)
	st, _ := db.View()
	if st.Clients[0].ServerTraffic["a"] != (model.Traffic{Upload: 135, Download: 77}) {
		t.Fatal("wrong server breakdown")
	}
	cookie := login(t, app)
	readJSON[map[string]bool](t, call(app, "DELETE", "/api/servers/a", nil, cookie, ""))
	check(155, 107)
}

func TestRegistrationStatesExposeNoSecrets(t *testing.T) {
	app, db := setup(t)
	db.Update(func(st *model.State) error {
		st.Servers = []model.Server{{ID: "pending", RegistrationHash: "secret", RegistrationExpires: time.Now().Add(time.Minute)}, {ID: "expired", RegistrationHash: "secret", RegistrationExpires: time.Now().Add(-time.Second)}, {ID: "registered", TokenHash: "secret"}}
		return nil
	})
	st := readJSON[model.State](t, call(app, "GET", "/api/state", nil, login(t, app), ""))
	for i, expected := range []string{"pending", "expired", "registered"} {
		if st.Servers[i].RegistrationState != expected || st.Servers[i].TokenHash != "" || st.Servers[i].RegistrationHash != "" {
			t.Fatal("incorrect or unsafe registration state")
		}
	}
}

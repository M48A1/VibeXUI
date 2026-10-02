package server

import (
	"net/http"
	"testing"
	"time"
	"vibexui/internal/model"
)

func TestPollScopingAndErrorClassification(t *testing.T) {
	app, db := setup(t)
	token := "agent-token"
	err := db.Update(func(st *model.State) error {
		st.Servers = []model.Server{{ID: "a", Version: 1, TokenHash: model.Hash(token), ClientTraffic: map[string]model.Traffic{"historical": {Upload: 10}}}, {ID: "b", Version: 1}}
		st.Nodes = []model.Node{{ID: "local", ServerID: "a"}, {ID: "other", ServerID: "b"}}
		st.Clients = []model.Client{{ID: "assigned", NodeIDs: []string{"local"}}, {ID: "foreign", NodeIDs: []string{"other"}}, {ID: "historical"}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"id": "a", "appliedVersion": 1}
	task := readJSON[model.Task](t, call(app, "POST", "/api/agent/poll", body, nil, token))
	if !model.Contains(task.ClientIDs, "assigned") || !model.Contains(task.ClientIDs, "historical") || model.Contains(task.ClientIDs, "foreign") {
		t.Fatal("incorrect scoped client IDs", task.ClientIDs)
	}
	if len(task.IPBindings) != 1 || task.IPBindings[0] != model.BindingKey("assigned", "local") {
		t.Fatal("incorrect scoped bindings", task.IPBindings)
	}
	if r := call(app, "POST", "/api/agent/poll", body, nil, "wrong"); r.Code != http.StatusUnauthorized {
		t.Fatal(r.Code)
	}
	body["appliedVersion"] = 999
	if r := call(app, "POST", "/api/agent/poll", body, nil, token); r.Code != 400 {
		t.Fatal("bad version misclassified", r.Code)
	}
	if err = db.Update(func(st *model.State) error { st.Servers[0].Routing.DefaultOutbound = "missing"; return nil }); err != nil {
		t.Fatal(err)
	}
	body["appliedVersion"] = 0
	start := time.Now()
	if r := call(app, "POST", "/api/agent/poll", body, nil, token); r.Code != 500 {
		t.Fatal("config failure misclassified", r.Code)
	}
	st, _ := db.View()
	if st.Servers[0].LastSeen.Before(start) {
		t.Fatal("config generation failure discarded heartbeat")
	}
	db.Close()
	if r := call(app, "POST", "/api/agent/poll", body, nil, token); r.Code != 500 {
		t.Fatal("database failure misclassified", r.Code)
	}
}
func TestCollectedSampleAge(t *testing.T) {
	now := time.Now()
	old := now.Add(-time.Minute)
	st := model.State{Servers: []model.Server{{ID: "s"}}, Nodes: []model.Node{{ID: "n", ServerID: "s"}}, Clients: []model.Client{{ID: "c", NodeIDs: []string{"n"}}}}
	srv := &st.Servers[0]
	srv.Running = true
	r := model.Report{Running: true, StatsEpoch: "e", StatsCollectedAt: old, IPCollectedAt: old, OnlineIPs: map[string][]string{model.BindingKey("c", "n"): {"1.2.3.4"}}}
	accountTraffic(&st, srv, r)
	accountOnlineIPs(&st, srv, r, now)
	if !srv.StatsUpdatedAt.Equal(old) {
		t.Fatal("old stats relabeled as current")
	}
	if srv.IPStatsError == "" || len(srv.OnlineIPs) != 0 {
		t.Fatal("stale IP sample accepted")
	}
	r.IPCollectedAt = now.Add(-time.Second)
	accountOnlineIPs(&st, srv, r, now)
	if !srv.IPStatsAt.Equal(r.IPCollectedAt) || srv.IPStatsError != "" {
		t.Fatal("valid sample timestamp lost")
	}
}

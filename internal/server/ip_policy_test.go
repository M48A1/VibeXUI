package server

import (
	"encoding/json"
	"testing"
	"time"
	"vibexui/internal/model"
)

func ipFixture(now time.Time) model.State {
	return model.State{
		Servers: []model.Server{{ID: "a", Host: "a.example", Version: 1, Running: true, IPStatsAt: now, OnlineIPs: map[string][]string{"alice.na": {"203.0.113.1", "203.0.113.2"}, "alice.nb": {"203.0.113.3"}, "bob.na": {"203.0.113.4"}}}, {ID: "b", Host: "b.example", Version: 1, Running: true, IPStatsAt: now, OnlineIPs: map[string][]string{"alice.nc": {"203.0.113.5"}}}},
		Nodes:   []model.Node{{ID: "na", ServerID: "a", Enabled: true}, {ID: "nb", ServerID: "a", Enabled: true}, {ID: "nc", ServerID: "b", Enabled: true}},
		Clients: []model.Client{{ID: "alice", UUID: model.UUID(), Enabled: true, NodeIDs: []string{"na", "nb", "nc"}, NodeIPLimits: map[string]int{"na": 1, "nb": 2, "nc": 1}}, {ID: "bob", UUID: model.UUID(), Enabled: true, NodeIDs: []string{"na"}, NodeIPLimits: map[string]int{"na": 1}}},
	}
}

func TestIPLimitIsScopedToClientAndInbound(t *testing.T) {
	now := time.Now()
	st := ipFixture(now)
	reconcileClients(&st, now)
	c := st.Clients[0]
	if !c.NodeIPStates["na"].BlockedUntil.Equal(now.Add(time.Minute)) {
		t.Fatal("missing one minute cooldown")
	}
	if model.ClientNodeActive(c, "na", now) || !model.ClientNodeActive(c, "nb", now) || !model.ClientNodeActive(c, "nc", now) || !model.ClientNodeActive(st.Clients[1], "na", now) {
		t.Fatal("block leaked to another inbound or user")
	}
	if st.Servers[0].Version != 2 || st.Servers[1].Version != 1 {
		t.Fatal("unrelated server version changed")
	}
	if len(model.Links(st, c)) != 2 {
		t.Fatal("blocked inbound still shared")
	}
	config, _ := model.Config(st, "a")
	var raw struct {
		Inbounds []struct {
			Tag      string
			Settings struct{ Clients []struct{ Email string } }
		}
	}
	if err := json.Unmarshal(config, &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw.Inbounds[0].Settings.Clients) != 1 || raw.Inbounds[0].Settings.Clients[0].Email != "bob.na" || raw.Inbounds[1].Settings.Clients[0].Email != "alice.nb" {
		t.Fatal("generated config did not enforce per-client per-inbound isolation", string(config))
	}
	reconcileClients(&st, now.Add(10*time.Second))
	if st.Servers[0].Version != 2 || !st.Clients[0].NodeIPStates["na"].BlockedUntil.Equal(now.Add(time.Minute)) {
		t.Fatal("poll extended cooldown or caused repeated restart")
	}
	reconcileClients(&st, now.Add(time.Minute))
	if st.Servers[0].Version != 3 || !st.Clients[0].NodeIPStates["na"].BlockedUntil.IsZero() {
		t.Fatal("cooldown did not automatically restore access")
	}
	st.Servers[0].IPStatsAt = now.Add(time.Minute)
	reconcileClients(&st, now.Add(time.Minute))
	if st.Servers[0].Version != 4 {
		t.Fatal("fresh sustained excess did not cause another cooldown")
	}
}

func TestIPSampleNormalizationUnknownAndExpiry(t *testing.T) {
	now := time.Now()
	st := ipFixture(now)
	s := &st.Servers[0]
	accountOnlineIPs(&st, s, model.Report{OnlineIPs: map[string][]string{"alice.na": {"203.0.113.1", "::ffff:203.0.113.1"}, "alice.nc": {"203.0.113.99"}, "foreign.na": {"203.0.113.100"}}}, now)
	if len(s.OnlineIPs) != 1 || len(s.OnlineIPs["alice.na"]) != 1 {
		t.Fatal("duplicate IP or another server's binding accepted")
	}
	reconcileClients(&st, now)
	if !st.Clients[0].NodeIPStates["na"].BlockedUntil.IsZero() {
		t.Fatal("IPv4 mapped IPv6 counted twice")
	}
	accountOnlineIPs(&st, s, model.Report{IPStatsError: "RPC failed"}, now.Add(time.Second))
	sample := sampleNodeIPs(&st, st.Clients[0], st.Nodes[0], now.Add(time.Second))
	if len(sample.OnlineIPs) != 1 || sample.StatsState != "error" || !sample.UpdatedAt.Equal(now) {
		t.Fatal("failed RPC erased recent known sample")
	}
	sample = sampleNodeIPs(&st, st.Clients[0], st.Nodes[0], now.Add(36*time.Second))
	if len(sample.OnlineIPs) != 0 {
		t.Fatal("stale sample still considered online")
	}
	accountOnlineIPs(&st, s, model.Report{OnlineIPs: map[string][]string{"alice.na": {}}}, now.Add(time.Minute))
	sample = sampleNodeIPs(&st, st.Clients[0], st.Nodes[0], now.Add(time.Minute))
	if sample.StatsState != "ok" || len(sample.OnlineIPs) != 0 {
		t.Fatal("successful empty sample must mean zero IPs")
	}
	for _, bad := range []map[string][]string{{"alice.na": {"not-an-ip"}}, {"alice.na": {"203.0.113.1/24"}}} {
		if validateOnlineIPs(bad) == nil {
			t.Fatal("invalid IP accepted")
		}
	}
}

func TestSaveIPLimitsValidationAndPreservation(t *testing.T) {
	app, db := setup(t)
	cookie := login(t, app)
	db.Update(func(st *model.State) error {
		st.Servers = []model.Server{{ID: "a", Version: 1}}
		st.Nodes = []model.Node{{ID: "na", ServerID: "a"}, {ID: "nb", ServerID: "a"}}
		return nil
	})
	c := readJSON[model.Client](t, call(app, "POST", "/api/clients", map[string]any{"name": "Alice", "enabled": true, "nodeIds": []string{"na", "nb"}, "nodeIPLimits": map[string]int{"na": 1, "nb": 2}}, cookie, ""))
	for _, limits := range []map[string]int{{"na": -1}, {"na": 129}, {"foreign": 1}} {
		w := call(app, "PATCH", "/api/clients/"+c.ID, map[string]any{"name": "Alice", "enabled": true, "nodeIds": c.NodeIDs, "nodeIPLimits": limits}, cookie, "")
		if w.Code != 400 {
			t.Fatal("invalid limit accepted", w.Code)
		}
	}
	c = readJSON[model.Client](t, call(app, "PATCH", "/api/clients/"+c.ID, map[string]any{"name": "Alice", "enabled": false, "nodeIds": c.NodeIDs}, cookie, ""))
	if c.NodeIPLimits["na"] != 1 || c.NodeIPLimits["nb"] != 2 {
		t.Fatal("toggle erased IP limits")
	}
	c = readJSON[model.Client](t, call(app, "PATCH", "/api/clients/"+c.ID, map[string]any{"name": "Alice", "enabled": true, "nodeIds": []string{"na"}, "nodeIPLimits": map[string]int{"na": 0}}, cookie, ""))
	if len(c.NodeIPLimits) != 1 || c.NodeIPLimits["na"] != 0 {
		t.Fatal("limits not cleared correctly")
	}
}

func TestIPPollBlocksOnlyItsInbound(t *testing.T) {
	app, db := setup(t)
	token := model.Secret()
	now := time.Now()
	st := ipFixture(now)
	st.Servers[0].TokenHash = model.Hash(token)
	db.Update(func(state *model.State) error { *state = st; return nil })
	task := readJSON[model.Task](t, call(app, "POST", "/api/agent/poll", map[string]any{"id": "a", "appliedVersion": 1, "running": true, "onlineIPs": map[string][]string{"alice.na": {"203.0.113.1", "203.0.113.2"}, "alice.nb": {"203.0.113.3"}, "bob.na": {"203.0.113.4"}}}, nil, token))
	if task.Version != 2 || len(task.IPBindings) != 3 {
		t.Fatal("poll did not return scoped enforcement task", task)
	}
	stored, _ := db.View()
	if stored.Servers[1].Version != 1 {
		t.Fatal("poll changed another VPS")
	}
	if model.ClientNodeActive(stored.Clients[0], "na", time.Now()) {
		t.Fatal("over-limit user still active on inbound")
	}
	if !model.ClientNodeActive(stored.Clients[0], "nb", time.Now()) {
		t.Fatal("other inbound disabled")
	}
}

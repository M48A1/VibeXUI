package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
	"vibexui/internal/model"
)

func TestPolicyTransitionsAcrossServers(t *testing.T) {
	now := time.Now()
	st := model.State{Servers: []model.Server{{ID: "a", Version: 1}, {ID: "b", Version: 1}}, Nodes: []model.Node{{ID: "na", ServerID: "a"}, {ID: "nb", ServerID: "b"}}, Clients: []model.Client{{ID: "alice", Enabled: true, NodeIDs: []string{"na", "nb"}, QuotaBytes: 100, Upload: 60, Download: 40}}}
	reconcileClients(&st, now)
	for _, s := range st.Servers {
		if s.Version != 2 {
			t.Fatal("quota did not revoke all servers")
		}
	}
	reconcileClients(&st, now)
	if st.Servers[0].Version != 2 {
		t.Fatal("repeated reconciliation bumped version")
	}
	c := &st.Clients[0]
	c.QuotaBaseline = model.Traffic{Upload: 60, Download: 40}
	reconcileClients(&st, now)
	if c.AccessState != "active" || st.Servers[1].Version != 3 || c.Upload != 60 {
		t.Fatal("reset failed to restore access preserving history")
	}
	c.ExpiresAt = now
	reconcileClients(&st, now)
	if c.AccessState != "expired" || st.Servers[0].Version != 4 {
		t.Fatal("deadline boundary was not enforced")
	}
	if len(model.Links(st, *c)) != 0 {
		t.Fatal("expired links exposed")
	}
	c.Enabled = false
	c.ExpiresAt = time.Time{}
	reconcileClients(&st, now)
	if c.AccessState != "disabled" || st.Servers[0].Version != 4 {
		t.Fatal("blocked to blocked must not resync")
	}
}

func TestQuotaPollRevokesConfigAndSubscription(t *testing.T) {
	app, db := setup(t)
	token := model.Secret()
	db.Update(func(st *model.State) error {
		st.Servers = []model.Server{{ID: "a", Version: 1, TokenHash: model.Hash(token)}}
		st.Nodes = []model.Node{{ID: "n", ServerID: "a", Enabled: true}}
		st.Clients = []model.Client{{ID: "alice", UUID: "test-uuid", Token: "subscription", Enabled: true, NodeIDs: []string{"n"}, QuotaBytes: 100}}
		return nil
	})
	body := map[string]any{"id": "a", "appliedVersion": 1, "running": true, "statsEpoch": "e", "clientTraffic": map[string]model.Traffic{"alice": {Upload: 100}}}
	task := readJSON[model.Task](t, call(app, "POST", "/api/agent/poll", body, nil, token))
	if task.Version != 2 || strings.Contains(string(task.Config), "test-uuid") {
		t.Fatal("quota report did not immediately generate revoked config")
	}
	if call(app, "GET", "/sub/subscription", nil, nil, "").Code != 404 {
		t.Fatal("subscription still active")
	}
	st, _ := db.View()
	st.Clients[0].QuotaBaseline = model.Traffic{Upload: 100}
	reconcileClients(&st, time.Now())
	// Identical high-water report after reset must not consume the new allowance.
	accountTraffic(&st, &st.Servers[0], model.Report{StatsEpoch: "e", ClientTraffic: map[string]model.Traffic{"alice": {Upload: 100}}})
	if model.QuotaUsage(st.Clients[0]) != 0 {
		t.Fatal("duplicate report consumed renewed allowance")
	}
	config, _ := model.Config(st, "a")
	var parsed map[string]any
	if json.Unmarshal(config, &parsed) != nil || !strings.Contains(string(config), "test-uuid") {
		t.Fatal("reset did not restore configuration")
	}
}

func TestMaintenanceEnforcesWithoutPoll(t *testing.T) {
	app, db := setup(t)
	db.Update(func(st *model.State) error {
		st.Servers = []model.Server{{ID: "a", Version: 1}}
		st.Nodes = []model.Node{{ID: "n", ServerID: "a"}}
		st.Clients = []model.Client{{ID: "c", Enabled: true, NodeIDs: []string{"n"}, ExpiresAt: time.Now().Add(-time.Second)}}
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	app.RunMaintenance(ctx)
	st, _ := db.View()
	if st.Servers[0].Version != 2 || st.Clients[0].AccessState != "expired" {
		t.Fatal("background expiry failed")
	}
}

func TestAutomaticQuotaCycles(t *testing.T) {
	anchor := time.Date(2024, 1, 31, 12, 0, 0, 0, time.UTC)
	for _, months := range []int{1, 3, 6, 12} {
		t.Run(time.Duration(months).String(), func(t *testing.T) {
			reset := quotaCycleDate(anchor, months)
			st := model.State{Servers: []model.Server{{ID: "s", Version: 1}}, Nodes: []model.Node{{ID: "n", ServerID: "s"}}, Clients: []model.Client{{ID: "c", Enabled: true, NodeIDs: []string{"n"}, AccessState: "quota", QuotaBytes: 100, Upload: 60, Download: 40, QuotaPeriodMonths: months, QuotaCycleAnchor: anchor, QuotaResetsAt: reset}}}
			reconcileClients(&st, reset.Add(-time.Nanosecond))
			if model.QuotaUsage(st.Clients[0]) != 100 {
				t.Fatal("reset before deadline")
			}
			reconcileClients(&st, reset)
			c := st.Clients[0]
			if model.QuotaUsage(c) != 0 || c.Upload != 60 || c.Download != 40 || c.AccessState != "active" || st.Servers[0].Version != 2 {
				t.Fatal("reset must preserve history and restore access")
			}
			reconcileClients(&st, reset)
			if st.Servers[0].Version != 2 {
				t.Fatal("duplicate reset")
			}
			reconcileClients(&st, anchor.AddDate(5, 0, 0))
			if !st.Clients[0].QuotaResetsAt.After(anchor.AddDate(5, 0, 0)) {
				t.Fatal("missed cycles not advanced")
			}
		})
	}
	if quotaCycleDate(anchor, 1).Day() != 29 || quotaCycleDate(anchor, 2).Day() != 31 {
		t.Fatal("month end drift")
	}
	for _, disabled := range []bool{false, true} {
		c := model.Client{Enabled: !disabled, ExpiresAt: anchor, QuotaPeriodMonths: 1, QuotaCycleAnchor: anchor, QuotaResetsAt: quotaCycleDate(anchor, 1), Upload: 100, QuotaBytes: 100}
		st := model.State{Clients: []model.Client{c}}
		reconcileClients(&st, quotaCycleDate(anchor, 1))
		if st.Clients[0].AccessState == "active" {
			t.Fatal("reset bypassed expiry or manual disable")
		}
	}
}

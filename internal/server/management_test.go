package server

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
	"vibexui/internal/model"
)

func TestCompleteClientSettingsAffectConfigAndSurviveToggle(t *testing.T) {
	app, db := setup(t)
	cookie := login(t, app)
	db.Update(func(st *model.State) error {
		st.Servers = []model.Server{{ID: "s", Version: 1}}
		st.Nodes = []model.Node{{ID: "n", ServerID: "s", Enabled: true}}
		return nil
	})
	uuid := model.UUID()
	body := map[string]any{"name": "Alice", "email": "alice@example.com", "uuid": uuid, "flow": "", "reverseTag": "reverse-alice", "enabled": true, "nodeIds": []string{"n"}, "quotaBytes": 1024, "trafficReset": "monthly", "trafficResetDay": 31, "resetDays": 30, "resetMax": 3, "expiresAt": time.Now().Add(24 * time.Hour), "subscriptionId": "shared_subscription", "telegramId": "123456789012345", "group": "premium", "comment": "a note", "externalLinks": []string{"trojan://secret@example.com:443#external"}, "limitIp": 2, "nodeFlows": map[string]string{"n": "xtls-rprx-vision"}}
	c := readJSON[model.Client](t, call(app, "POST", "/api/clients", body, cookie, ""))
	if c.Email != "alice@example.com" || c.TelegramID != "123456789012345" || c.ResetDays != 30 || c.ResetMax != 3 || c.QuotaResetsAt.IsZero() || c.Group != "premium" || c.Comment != "a note" || c.LimitIP != 2 {
		t.Fatal("settings lost", c)
	}
	st, _ := db.View()
	config, _ := model.Config(st, "s")
	var parsed struct {
		Inbounds []struct {
			Settings struct {
				Clients []struct {
					ID, Flow, Email string
					Reverse         struct{ Tag string }
				}
			}
		}
	}
	if err := json.Unmarshal(config, &parsed); err != nil {
		t.Fatal(err)
	}
	entry := parsed.Inbounds[0].Settings.Clients[0]
	if entry.ID != uuid || entry.Flow != "xtls-rprx-vision" || entry.Reverse.Tag != "reverse-alice" || entry.Email != model.BindingKey(c.ID, "n") {
		t.Fatal("settings not applied", string(config))
	}
	link := model.Links(st, c)[0]
	if !strings.Contains(link, "flow=xtls-rprx-vision") {
		t.Fatal("share flow differs from config", link)
	}
	disabled := readJSON[model.Client](t, call(app, "PATCH", "/api/clients/"+c.ID, map[string]any{"name": c.Name, "enabled": false, "nodeIds": c.NodeIDs}, cookie, ""))
	if disabled.Email != c.Email || disabled.Token != c.Token || disabled.ResetMax != 3 || disabled.Flow == nil || *disabled.Flow != "" || disabled.NodeFlows["n"] != "xtls-rprx-vision" || disabled.LimitIP != 2 || len(disabled.ExternalLinks) != 1 {
		t.Fatal("toggle erased settings", disabled)
	}
	if call(app, "GET", "/sub/"+c.Token, nil, nil, "").Code != 404 {
		t.Fatal("disabled subscription accessible")
	}
	for key, value := range map[string]any{"uuid": "invalid", "flow": "unsupported", "limitIp": 129, "resetWeekday": 8, "resetDays": -1, "trafficReset": "yearly", "trafficResetDay": 32, "telegramId": "bad", "nodeFlows": map[string]string{"foreign": ""}} {
		invalid := map[string]any{"name": c.Name, "enabled": true, "nodeIds": c.NodeIDs, key: value}
		if w := call(app, "PATCH", "/api/clients/"+c.ID, invalid, cookie, ""); w.Code != 400 {
			t.Fatalf("invalid %s accepted: %d %s", key, w.Code, w.Body.String())
		}
	}
}

func TestNodeQuotaAccountingResetAndConfigIsolation(t *testing.T) {
	app, db := setup(t)
	cookie := login(t, app)
	token := model.Secret()
	db.Update(func(st *model.State) error {
		st.Servers = []model.Server{{ID: "s", Version: 1, TokenHash: model.Hash(token)}}
		st.Nodes = []model.Node{{ID: "n", ServerID: "s", Enabled: true, QuotaBytes: 100, AccessState: "active"}, {ID: "other", ServerID: "s", Enabled: true}, {ID: "foreign", ServerID: "else", Enabled: true}}
		st.Clients = []model.Client{{ID: "c", Enabled: true, UUID: model.UUID(), NodeIDs: []string{"n", "other"}, Upload: 20, QuotaBytes: 50}}
		return nil
	})
	report := func(epoch string, up uint64) model.Task {
		return readJSON[model.Task](t, call(app, "POST", "/api/agent/poll", map[string]any{"id": "s", "appliedVersion": 1, "statsEpoch": epoch, "running": true, "nodeTraffic": map[string]model.Traffic{"n": {Upload: up}, "foreign": {Upload: 1000}}}, nil, token))
	}
	task := report("e", 100)
	if strings.Contains(string(task.Config), `"tag":"n"`) || !strings.Contains(string(task.Config), `"tag":"other"`) {
		t.Fatal("node quota disabled wrong inbounds", string(task.Config))
	}
	report("e", 100)
	report("e", 90)
	st, _ := db.View()
	if st.Nodes[0].Upload != 100 || st.Nodes[2].Upload != 0 || st.Servers[0].Version != 2 {
		t.Fatal("duplicate or foreign node report counted", st.Nodes)
	}
	n := readJSON[model.Node](t, call(app, "POST", "/api/nodes/n/reset-quota", map[string]any{}, cookie, ""))
	if model.NodeUsage(n) != 0 || n.Upload != 100 || n.AccessState != "active" {
		t.Fatal("manual node reset failed", n)
	}
	report("e", 100)
	report("e", 105)
	report("new", 3)
	st, _ = db.View()
	if st.Nodes[0].Upload != 108 || model.NodeUsage(st.Nodes[0]) != 8 || model.QuotaUsage(st.Clients[0]) != 20 {
		t.Fatal("node reset lost isolation or epoch accounting", st)
	}
	config, _ := model.Config(st, "s")
	if !strings.Contains(string(config), `"tag": "n"`) {
		t.Fatal("node reset did not restore inbound")
	}
}

func TestCalendarResetsAndRenewalCatchUp(t *testing.T) {
	loc := time.Local
	anchor := time.Date(2024, 1, 31, 0, 0, 0, 0, loc)
	feb := nextMonthly(anchor, 31)
	march := nextMonthly(feb, 31)
	if feb.Day() != 29 || march.Day() != 31 {
		t.Fatal("monthly schedule drift", feb, march)
	}
	for _, mode := range []string{"hourly", "daily", "weekly", "monthly"} {
		reset := nextTrafficReset(anchor, mode, 31)
		st := model.State{Nodes: []model.Node{{ID: "n", Enabled: true, Upload: 100, QuotaBytes: 100, TrafficReset: mode, TrafficResetDay: 31, QuotaResetsAt: reset}}, Clients: []model.Client{{Enabled: true, Upload: 100, QuotaBytes: 100, TrafficReset: mode, TrafficResetDay: 31, QuotaResetsAt: reset}}}
		reconcileClients(&st, reset.Add(-time.Nanosecond))
		if model.NodeUsage(st.Nodes[0]) != 100 || model.QuotaUsage(st.Clients[0]) != 100 {
			t.Fatal("early reset", mode)
		}
		reconcileClients(&st, reset)
		if model.NodeUsage(st.Nodes[0]) != 0 || model.QuotaUsage(st.Clients[0]) != 0 || st.Nodes[0].Upload != 100 {
			t.Fatal("reset failed", mode)
		}
		next := st.Clients[0].QuotaResetsAt
		reconcileClients(&st, reset)
		if !st.Clients[0].QuotaResetsAt.Equal(next) {
			t.Fatal("duplicate reset", mode)
		}
	}
	now := anchor.Add(90 * 24 * time.Hour)
	for _, limit := range []int{0, 3, 4} {
		c := model.Client{Enabled: true, Upload: 100, ExpiresAt: anchor, ResetDays: 30, ResetMax: limit}
		renewClient(&c, now)
		if limit == 3 {
			if c.ExpiresAt != anchor || c.RenewalCount != 0 || model.QuotaUsage(c) != 100 {
				t.Fatal("renewal limit bypassed", c)
			}
		} else if !c.ExpiresAt.After(now) || c.RenewalCount != 4 || model.QuotaUsage(c) != 0 {
			t.Fatal("incorrect catch up", c)
		}
	}
	c := model.Client{Enabled: true, ExpiresAt: anchor, ResetDay: 31}
	renewClient(&c, time.Date(2024, 4, 1, 0, 0, 0, 0, loc))
	if c.ExpiresAt.Day() != 30 || c.ExpiresAt.Month() != time.April || c.RenewalCount != 3 {
		t.Fatal("monthly catch up", c)
	}
	weekly := model.Client{Enabled: true, ExpiresAt: time.Date(2024, 1, 1, 0, 0, 0, 0, loc), ResetWeekday: 1}
	renewClient(&weekly, time.Date(2024, 1, 15, 0, 0, 0, 0, loc))
	if weekly.RenewalCount != 3 || weekly.ExpiresAt.Day() != 22 {
		t.Fatal("weekly catch up", weekly)
	}
	disabled := model.Client{Enabled: false, ExpiresAt: anchor, ResetDays: 30}
	renewClient(&disabled, now)
	if disabled.ExpiresAt != anchor {
		t.Fatal("operator disabled client renewed")
	}
}

func TestFirstUseAndManualResetPreserveExpiryAndSchedule(t *testing.T) {
	app, db := setup(t)
	cookie := login(t, app)
	db.Update(func(st *model.State) error {
		st.Servers = []model.Server{{ID: "s", Version: 1}}
		st.Nodes = []model.Node{{ID: "n", ServerID: "s", Enabled: true}}
		return nil
	})
	c := readJSON[model.Client](t, call(app, "POST", "/api/clients", map[string]any{"name": "first-use", "enabled": true, "nodeIds": []string{"n"}, "firstUseDays": 7, "trafficReset": "daily", "quotaBytes": 50}, cookie, ""))
	if !c.ExpiresAt.IsZero() || !c.FirstUsedAt.IsZero() {
		t.Fatal("started before first use")
	}
	db.Update(func(st *model.State) error {
		accountTraffic(st, &st.Servers[0], model.Report{StatsEpoch: "e", ClientTraffic: map[string]model.Traffic{c.ID: {Upload: 50}}})
		reconcileClients(st, time.Now())
		return nil
	})
	st, _ := db.View()
	active := st.Clients[0]
	if active.FirstUsedAt.IsZero() || active.ExpiresAt.Sub(active.FirstUsedAt) != 7*24*time.Hour || active.AccessState != "quota" {
		t.Fatal("first use failed", active)
	}
	reset := readJSON[model.Client](t, call(app, "POST", "/api/clients/"+c.ID+"/reset-quota", map[string]any{}, cookie, ""))
	if !reset.ExpiresAt.Equal(active.ExpiresAt) || !reset.QuotaResetsAt.Equal(active.QuotaResetsAt) || reset.AccessState != "active" || reset.Upload != 50 {
		t.Fatal("reset changed expiry or schedule", reset)
	}
	db.Update(func(st *model.State) error {
		accountTraffic(st, &st.Servers[0], model.Report{StatsEpoch: "e", ClientTraffic: map[string]model.Traffic{c.ID: {Upload: 50}}})
		return nil
	})
	st, _ = db.View()
	if model.QuotaUsage(st.Clients[0]) != 0 || !st.Clients[0].ExpiresAt.Equal(active.ExpiresAt) {
		t.Fatal("duplicate report renewed first use")
	}
}

func TestBulkAndBatchOperationsAreAtomic(t *testing.T) {
	app, db := setup(t)
	cookie := login(t, app)
	db.Update(func(st *model.State) error {
		st.Servers = []model.Server{{ID: "s", Version: 1}}
		st.Nodes = []model.Node{{ID: "n", ServerID: "s", Enabled: true}}
		return nil
	})
	clients := readJSON[[]model.Client](t, call(app, "POST", "/api/clients/batch", map[string]any{"prefix": "user", "count": 3, "nodeIds": []string{"n"}}, cookie, ""))
	if len(clients) != 3 || clients[0].UUID == clients[1].UUID || clients[0].Token == clients[1].Token {
		t.Fatal("batch credentials not independent")
	}
	before, _ := db.View()
	if call(app, "POST", "/api/clients/batch", map[string]any{"prefix": "user", "count": 2}, cookie, "").Code != 400 {
		t.Fatal("batch conflict accepted")
	}
	if call(app, "POST", "/api/clients/bulk", map[string]any{"ids": []string{clients[0].ID, "missing"}, "action": "disable"}, cookie, "").Code != 404 {
		t.Fatal("unknown ID accepted")
	}
	after, _ := db.View()
	if len(after.Clients) != 3 || after.Servers[0].Version != before.Servers[0].Version || !after.Clients[0].Enabled {
		t.Fatal("partial changes persisted")
	}
	readJSON[map[string]bool](t, call(app, "POST", "/api/clients/bulk", map[string]any{"ids": []string{clients[0].ID, clients[1].ID}, "action": "disable"}, cookie, ""))
	after, _ = db.View()
	if after.Clients[0].Enabled || after.Clients[1].Enabled || !after.Clients[2].Enabled {
		t.Fatal("bulk selection isolation failed")
	}
}

func TestSharedSubscriptionGroupsOnlyActiveClients(t *testing.T) {
	app, db := setup(t)
	token := "shared-subscription"
	db.Update(func(st *model.State) error {
		st.Servers = []model.Server{{ID: "s", Host: "example.com"}}
		st.Nodes = []model.Node{{ID: "n", ServerID: "s", Enabled: true}}
		st.Clients = []model.Client{{ID: "a", UUID: "uuid-a", Enabled: true, Token: token, NodeIDs: []string{"n"}, QuotaBytes: 100, Upload: 10}, {ID: "b", UUID: "uuid-b", Enabled: true, Token: token, NodeIDs: []string{"n"}, QuotaBytes: 200, Download: 20}, {ID: "c", UUID: "uuid-c", Enabled: false, Token: token, NodeIDs: []string{"n"}}}
		return nil
	})
	w := call(app, "GET", "/sub/"+token, nil, nil, "")
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	decoded, _ := base64.StdEncoding.DecodeString(w.Body.String())
	if !strings.Contains(string(decoded), "uuid-a") || !strings.Contains(string(decoded), "uuid-b") || strings.Contains(string(decoded), "uuid-c") {
		t.Fatal("wrong grouped subscription", string(decoded))
	}
	if w.Header().Get("Subscription-Userinfo") != "upload=10; download=20; total=300" {
		t.Fatal("wrong subscription quota header", w.Header())
	}
}

func TestInboundSettingsAndTransferPreserveCredentialsAndIsolateHistory(t *testing.T) {
	app, db := setup(t)
	cookie := login(t, app)
	db.Update(func(st *model.State) error {
		st.Servers = []model.Server{{ID: "a", Version: 1}, {ID: "b", Version: 1}}
		return nil
	})
	keys := readJSON[map[string]string](t, call(app, "POST", "/api/nodes/keys", map[string]any{}, cookie, ""))
	body := map[string]any{"serverId": "a", "name": "Inbound", "port": 443, "sni": "example.com", "target": "example.com:443", "enabled": true, "privateKey": keys["privateKey"], "shortId": "0123456789abcdef", "listen": "127.0.0.1", "fingerprint": "firefox", "spiderX": "/probe", "serverNames": []string{"www.example.com"}, "sniffing": true, "sniffingRouteOnly": true, "quotaBytes": 1000, "trafficReset": "monthly", "trafficResetDay": 31, "subSortIndex": 2}
	node := readJSON[model.Node](t, call(app, "POST", "/api/nodes", body, cookie, ""))
	if node.PrivateKey != "" || node.PublicKey != keys["publicKey"] || !node.Sniffing || node.TrafficResetDay != 31 {
		t.Fatal("node settings lost or private key leaked", node)
	}
	client := readJSON[model.Client](t, call(app, "POST", "/api/clients", map[string]any{"name": "Alice", "enabled": true, "nodeIds": []string{node.ID}, "nodeIPLimits": map[string]int{node.ID: 2}, "nodeFlows": map[string]string{node.ID: ""}}, cookie, ""))
	st, _ := db.View()
	conf, _ := model.Config(st, "a")
	if !strings.Contains(string(conf), `"listen": "127.0.0.1"`) || !strings.Contains(string(conf), `"routeOnly": true`) {
		t.Fatal("listen or sniffing not applied", string(conf))
	}
	if link := model.Links(st, client)[0]; !strings.Contains(link, "fp=firefox") || !strings.Contains(link, "spx=%2Fprobe") {
		t.Fatal("share parameters not applied", link)
	}
	exported := readJSON[inboundExport](t, call(app, "GET", "/api/nodes/"+node.ID+"/export", nil, cookie, ""))
	exported.Node.Port = 8443
	exported.ServerID = "b"
	exported.Node.Upload = 999
	exported.Clients[0].Upload = 500
	imported := readJSON[model.Node](t, call(app, "POST", "/api/nodes/import", exported, cookie, ""))
	st, _ = db.View()
	if len(st.Nodes) != 2 || len(st.Clients) != 1 || imported.Upload != 0 || imported.PublicKey != node.PublicKey || st.Clients[0].Upload != 0 || !model.Contains(st.Clients[0].NodeIDs, imported.ID) || st.Clients[0].NodeIPLimits[imported.ID] != 2 {
		t.Fatal("import changed credentials or history", st)
	}
	if value, ok := st.Clients[0].NodeFlows[imported.ID]; !ok || value != "" {
		t.Fatal("import lost binding flow override")
	}
	// Toggle using the old API shape must retain every new inbound setting.
	body = map[string]any{"serverId": "a", "name": "Inbound", "port": 443, "sni": "example.com", "target": "example.com:443", "enabled": false}
	changed := readJSON[model.Node](t, call(app, "PATCH", "/api/nodes/"+node.ID, body, cookie, ""))
	if changed.QuotaBytes != 1000 || changed.TrafficReset != "monthly" || changed.Listen != "127.0.0.1" || changed.Fingerprint != "firefox" || !changed.Sniffing || changed.SubSortIndex != 2 {
		t.Fatal("toggle erased inbound settings", changed)
	}
	exported.Node.Port = 8444
	exported.Node.QuotaBytes = 9007199254740992
	if call(app, "POST", "/api/nodes/import", exported, cookie, "").Code != 400 {
		t.Fatal("invalid imported quota accepted")
	}
	st, _ = db.View()
	if len(st.Nodes) != 2 {
		t.Fatal("failed import persisted partial inbound")
	}
}

func TestDefaultIPLimitAndClearIPsKeepOtherClientsIntact(t *testing.T) {
	app, db := setup(t)
	cookie := login(t, app)
	now := time.Now()
	st := ipFixture(now)
	st.Clients[0].LimitIP = 1
	delete(st.Clients[0].NodeIPLimits, "na")
	reconcileClients(&st, now)
	if st.Clients[0].NodeIPStates["na"].BlockedUntil.IsZero() {
		t.Fatal("default IP limit not enforced")
	}
	db.Update(func(current *model.State) error { *current = st; return nil })
	c := readJSON[model.Client](t, call(app, "POST", "/api/clients/alice/clear-ips", map[string]any{}, cookie, ""))
	if !c.NodeIPStates["na"].BlockedUntil.IsZero() {
		t.Fatal("clear IP did not restore scoped access")
	}
	after, _ := db.View()
	if len(after.Servers[0].OnlineIPs["bob.na"]) != 1 {
		t.Fatal("clear IP erased another user's sample")
	}
}

func TestCalendarMidnightHandlesTimezoneTransitions(t *testing.T) {
	for _, name := range []string{"America/Santiago", "America/Havana", "Pacific/Apia", "Asia/Shanghai", "Asia/Kolkata"} {
		loc, err := time.LoadLocation(name)
		if err != nil {
			t.Fatal(err)
		}
		date := time.Date(2011, 12, 30, 0, 0, 0, 0, time.UTC)
		got, ok := calendarStart(date, loc)
		if name == "Pacific/Apia" {
			if ok {
				t.Fatal("skipped date must have no calendar start", got)
			}
			continue
		}
		if !ok || got.In(loc).Day() != 30 {
			t.Fatal("wrong calendar date", name, got)
		}
		before := got.Add(-time.Second).In(loc)
		if before.Day() == 30 {
			t.Fatal("did not choose first instant", name, got)
		}
	}
	loc, err := time.LoadLocation("America/Havana")
	if err != nil {
		t.Fatal(err)
	}
	repeated, ok := calendarStart(time.Date(2024, 11, 3, 0, 0, 0, 0, time.UTC), loc)
	if !ok || repeated.Add(-time.Second).In(loc).Day() == 3 {
		t.Fatal("repeated midnight picked later instant", repeated)
	}
}

func TestRenewalPreviewDoesNotMutateClient(t *testing.T) {
	app, db := setup(t)
	cookie := login(t, app)
	cutoff := time.Now().Add(-65 * 24 * time.Hour)
	db.Update(func(st *model.State) error {
		st.Clients = []model.Client{{ID: "c", Name: "Alice", Enabled: true, ResetDays: 30, ResetMax: 2, ExpiresAt: cutoff, Upload: 100}}
		return nil
	})
	preview := readJSON[map[string]any](t, call(app, "POST", "/api/clients/renewal-preview", map[string]any{"clientId": "c", "expiresAt": cutoff, "resetDays": 30, "resetMax": 2}, cookie, ""))
	if preview["canRenew"] != false || preview["renewalsNeeded"] != float64(3) {
		t.Fatal("preview ignored renewal limit", preview)
	}
	st, _ := db.View()
	if !st.Clients[0].ExpiresAt.Equal(cutoff) || st.Clients[0].RenewalCount != 0 || model.QuotaUsage(st.Clients[0]) != 100 {
		t.Fatal("preview mutated client")
	}
}

func TestLegacyDuplicateNamesCanStillBeToggled(t *testing.T) {
	app, db := setup(t)
	cookie := login(t, app)
	db.Update(func(st *model.State) error {
		st.Clients = []model.Client{{ID: "a", Name: "same", UUID: model.UUID(), Token: model.Secret(), Enabled: true}, {ID: "b", Name: "same", UUID: model.UUID(), Token: model.Secret(), Enabled: true}}
		return nil
	})
	changed := readJSON[model.Client](t, call(app, "PATCH", "/api/clients/a", map[string]any{"name": "same", "enabled": false, "nodeIds": []string{}}, cookie, ""))
	if changed.Enabled || changed.Email == "same" {
		t.Fatal("legacy duplicate name prevented safe toggle", changed)
	}
}

func TestSubscriptionHidingAndOrderDoNotDisableInbound(t *testing.T) {
	st := model.State{Servers: []model.Server{{ID: "s", Host: "example.com"}}, Nodes: []model.Node{{ID: "late", Name: "late", ServerID: "s", Enabled: true, SubSortIndex: 3}, {ID: "hidden", Name: "hidden", ServerID: "s", Enabled: true, ExcludeFromSub: true}, {ID: "first", Name: "first", ServerID: "s", Enabled: true, SubSortIndex: -1}}}
	c := model.Client{ID: "c", UUID: model.UUID(), Enabled: true, NodeIDs: []string{"late", "hidden", "first"}}
	links := model.Links(st, c)
	if len(links) != 2 || !strings.HasSuffix(links[0], "#first") {
		t.Fatal("subscription hiding or order ignored", links)
	}
	if len(model.ShareLinks(st, c)) != 3 {
		t.Fatal("hidden inbound lost direct share link")
	}
	config, _ := model.Config(st, "s")
	if !strings.Contains(string(config), `"tag": "hidden"`) {
		t.Fatal("subscription hiding disabled Xray inbound")
	}
}

func TestCopyInboundAssignsSourceClientsWithoutResettingTheirUsage(t *testing.T) {
	app, db := setup(t)
	cookie := login(t, app)
	db.Update(func(st *model.State) error {
		st.Servers = []model.Server{{ID: "s", Version: 1}}
		st.Nodes = []model.Node{{ID: "source", ServerID: "s", Enabled: true}}
		st.Clients = []model.Client{{ID: "c", Name: "Alice", UUID: model.UUID(), Enabled: true, NodeIDs: []string{"source"}, Upload: 123, NodeIPLimits: map[string]int{"source": 2}, NodeFlows: map[string]string{"source": ""}}}
		return nil
	})
	n := readJSON[model.Node](t, call(app, "POST", "/api/nodes", map[string]any{"sourceNodeId": "source", "serverId": "s", "name": "copy", "port": 8443, "sni": "example.com", "target": "example.com:443", "enabled": true}, cookie, ""))
	st, _ := db.View()
	c := st.Clients[0]
	if c.Upload != 123 || len(c.NodeIDs) != 2 || !model.Contains(c.NodeIDs, n.ID) || c.NodeIPLimits[n.ID] != 2 {
		t.Fatal("copy did not preserve shared client usage or binding settings", c)
	}
	if flow, ok := c.NodeFlows[n.ID]; !ok || flow != "" {
		t.Fatal("copy lost empty flow override")
	}
	readJSON[map[string]bool](t, call(app, "DELETE", "/api/nodes/"+n.ID, nil, cookie, ""))
	st, _ = db.View()
	if _, ok := st.Clients[0].NodeFlows[n.ID]; ok {
		t.Fatal("deleted inbound's flow override retained")
	}
}

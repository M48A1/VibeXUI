package agent

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"vibexui/internal/model"
)

func TestUserTrafficSurvivesXrayAndAgentRestarts(t *testing.T) {
	a := fake(t)
	ctx := context.Background()
	config := json.RawMessage(`{"log":{}}`)
	fixture := func(up, down, aliceUp, aliceDown uint64) {
		t.Helper()
		stats := []map[string]any{}
		for name, value := range map[string]uint64{"inbound>>>node>>>traffic>>>uplink": up, "inbound>>>node>>>traffic>>>downlink": down, "user>>>alice>>>traffic>>>uplink": aliceUp, "user>>>alice>>>traffic>>>downlink": aliceDown, "outbound>>>direct>>>traffic>>>uplink": up, "outbound>>>direct>>>traffic>>>downlink": down} {
			stats = append(stats, map[string]any{"name": name, "value": value})
		}
		raw, _ := json.Marshal(map[string]any{"stat": stats})
		if err := os.WriteFile(a.opts.Xray+".stats", raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	fixture(100, 200, 60, 120)
	if err := a.apply(ctx, model.Task{Version: 1, Running: true, Config: config}); err != nil {
		t.Fatal(err)
	}
	a.stats(ctx)
	a.stats(ctx)
	if a.state.NodeTraffic["node"] != (model.Traffic{Upload: 100, Download: 200}) || a.state.Upload != 100 || a.state.Download != 200 || a.state.ClientTraffic["alice"] != (model.Traffic{Upload: 60, Download: 120}) {
		t.Fatal("duplicated user/inbound/outbound traffic")
	}
	epoch := a.state.StatsEpoch
	directory := a.opts.Directory
	binary := a.opts.Xray
	a.stop()
	next, err := New(Options{Panel: "http://localhost:8080", ID: "test", Directory: directory, Xray: binary})
	if err != nil {
		t.Fatal(err)
	}
	defer next.stop()
	if next.state.NodeTraffic["node"] != (model.Traffic{Upload: 100, Download: 200}) || next.state.StatsEpoch != epoch || next.state.ClientTraffic["alice"].Upload != 60 {
		t.Fatal("lost persisted accounting")
	}
	fixture(10, 20, 5, 10)
	if err = next.start(ctx); err != nil {
		t.Fatal(err)
	}
	next.stats(ctx)
	next.stats(ctx)
	if next.state.NodeTraffic["node"] != (model.Traffic{Upload: 110, Download: 220}) || next.state.Upload != 110 || next.state.Download != 220 || next.state.ClientTraffic["alice"] != (model.Traffic{Upload: 65, Download: 130}) {
		t.Fatal("process restart lost or duplicated traffic", next.state.ClientTraffic)
	}
}

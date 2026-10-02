package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
	"vibexui/internal/model"
)

func TestIdenticalConfigurationAndExplicitRestart(t *testing.T) {
	a := fake(t)
	ctx := context.Background()
	cfg := json.RawMessage(`{"log":{"loglevel":"warning"}}`)
	if err := a.apply(ctx, model.Task{Version: 1, Running: true, Config: cfg}); err != nil {
		t.Fatal(err)
	}
	pid := a.process.Process.Pid
	if err := a.apply(ctx, model.Task{Version: 2, Running: true, Config: json.RawMessage(`{ "log" : { "loglevel" : "warning" } }`)}); err != nil {
		t.Fatal(err)
	}
	if a.process.Process.Pid != pid || a.state.Version != 2 {
		t.Fatal("unchanged config restarted process or failed to acknowledge")
	}
	if err := a.apply(ctx, model.Task{Version: 3, RestartVersion: 3, Running: true, Config: cfg}); err != nil {
		t.Fatal(err)
	}
	if a.process.Process.Pid == pid || a.state.RestartVersion != 3 {
		t.Fatal("explicit restart was skipped")
	}
	// Saving an identical checkpoint must preserve the underlying file inode/mtime.
	path := filepath.Join(a.opts.Directory, "agent.json")
	before, _ := os.Stat(path)
	if err := a.save(); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(path)
	if !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("identical checkpoint was rewritten")
	}
	if sameConfig([]byte(`{"v":9007199254740992}`), []byte(`{"v":9007199254740993}`)) {
		t.Fatal("config comparison lost integer precision")
	}
}
func TestFinalCountersAndStaleCollector(t *testing.T) {
	a := fake(t)
	ctx := context.Background()
	if err := a.apply(ctx, model.Task{Version: 1, Running: true, Config: json.RawMessage(`{"log":{}}`)}); err != nil {
		t.Fatal(err)
	}
	a.stats(ctx)
	if a.state.Upload != 100 {
		t.Fatal(a.state.Upload)
	}
	if err := os.WriteFile(a.opts.Xray+".stats", []byte(`{"stat":[{"name":"inbound>>>node>>>traffic>>>uplink","value":"150"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	oldGeneration := a.generation
	if err := a.apply(ctx, model.Task{Version: 2, Running: false, Config: json.RawMessage(`{"log":{"loglevel":"error"}}`)}); err != nil {
		t.Fatal(err)
	}
	if a.state.Upload != 150 {
		t.Fatal("tail counters lost", a.state.Upload)
	}
	if err := a.start(ctx); err != nil {
		t.Fatal(err)
	}
	a.collection = make(chan collectedStats, 1)
	a.collection <- collectedStats{generation: oldGeneration, raw: []byte(`{"stat":[{"name":"inbound>>>node>>>traffic>>>uplink","value":"999"}]}`)}
	a.consumeCollection()
	if a.state.Upload != 150 {
		t.Fatal("old process sample applied to new process")
	}
}
func TestSlowCollectionDoesNotDelayHeartbeat(t *testing.T) {
	a := fake(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer a.invalidateCollection()
	if err := a.apply(ctx, model.Task{Version: 1, Running: true, Config: json.RawMessage(`{"log":{}}`)}); err != nil {
		t.Fatal(err)
	}
	// A pending worker is represented by its empty channel. Heartbeat must never wait for it.
	a.collection = make(chan collectedStats, 1)
	seen := make(chan struct{}, 1)
	panel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- struct{}{}
		json.NewEncoder(w).Encode(model.Task{Version: 1, Running: true})
	}))
	defer panel.Close()
	a.opts.Panel = panel.URL
	start := time.Now()
	if err := a.cycle(ctx); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || time.Since(start) > time.Second {
		t.Fatal("heartbeat waited for collection")
	}
}
func TestApplyBackoffNewVersionAndStop(t *testing.T) {
	a := fake(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer a.invalidateCollection()
	if err := a.apply(ctx, model.Task{Version: 1, Running: true, Config: json.RawMessage(`{"log":{}}`)}); err != nil {
		t.Fatal(err)
	}
	var taskMu sync.Mutex
	task := model.Task{Version: 2, Running: true, Config: json.RawMessage(`{"reject":true}`)}
	panel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		taskMu.Lock()
		snapshot := task
		taskMu.Unlock()
		json.NewEncoder(w).Encode(snapshot)
	}))
	defer panel.Close()
	a.opts.Panel = panel.URL
	if err := a.cycle(ctx); err == nil {
		t.Fatal("invalid config accepted")
	}
	retry := a.retryAt
	if err := a.cycle(ctx); err != nil || a.retryAt != retry {
		t.Fatal("failed version not backed off", err)
	}
	taskMu.Lock()
	task.Running = false
	taskMu.Unlock()
	if err := a.cycle(ctx); err != nil {
		t.Fatal(err)
	}
	if a.running() {
		t.Fatal("backoff delayed operator stop")
	}
	taskMu.Lock()
	task = model.Task{Version: 3, Running: true, Config: json.RawMessage(`{"log":{}}`)}
	taskMu.Unlock()
	if err := a.cycle(ctx); err != nil {
		t.Fatal(err)
	}
	if a.state.Version != 3 || !a.running() || a.failedVersion != 0 {
		t.Fatal("new version did not bypass retry delay")
	}
	for i := 0; i < 20; i++ {
		a.noteApplyFailure(4)
	}
	if a.retryDelay != 5*time.Minute {
		t.Fatal("unbounded backoff")
	}
}

func TestCollectionWorkerSnapshot(t *testing.T) {
	a := fake(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer a.invalidateCollection()
	if err := a.apply(ctx, model.Task{Version: 1, Running: true, Config: json.RawMessage(`{"log":{}}`)}); err != nil {
		t.Fatal(err)
	}
	a.state.IPBindings = []string{"alice.node"}
	a.beginCollection(ctx)
	select {
	case sample := <-a.collection:
		a.collection <- sample
	case <-time.After(3 * time.Second):
		t.Fatal("collector failed to finish")
	}
	ips, err := a.consumeCollection()
	if err != "" || ips == nil || a.state.Upload != 100 || a.statsAt.IsZero() || a.ipAt.IsZero() {
		t.Fatal("worker result missing", err, a.state.Upload)
	}
	a.beginCollection(ctx)
	a.state.IPBindings[0] = "changed.node"
	a.opts.Xray = "changed-path"
	a.invalidateCollection()
	// Wait for cancellation to complete: workers only use captured values.
}

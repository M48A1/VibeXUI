package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
	"vibexui/internal/model"
)

func TestOfflineStartupAndCrashRecovery(t *testing.T) {
	a := fake(t)
	script, err := os.ReadFile(a.opts.Xray)
	if err != nil {
		t.Fatal(err)
	}
	script = []byte(strings.Replace(string(script), "trap 'exit 0' INT TERM", "echo $$ > \"$0.pid\"\ntrap 'exit 0' INT TERM", 1))
	if err = os.WriteFile(a.opts.Xray, script, 0700); err != nil {
		t.Fatal(err)
	}
	reports := make(chan model.Report, 20)
	panel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var report model.Report
		json.NewDecoder(r.Body).Decode(&report)
		select {
		case reports <- report:
		default:
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer panel.Close()
	a.opts.Panel = panel.URL
	a.state.Panel = panel.URL
	a.state.Token = "saved-token"
	a.opts.Interval = time.Second
	if err = a.apply(context.Background(), model.Task{Version: 1, Running: true, Config: json.RawMessage(`{"log":{}}`)}); err != nil {
		t.Fatal(err)
	}
	a.stop()
	next, err := New(a.opts)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- next.Run(ctx) }()
	defer func() { cancel(); <-done }()
	select {
	case r := <-reports:
		if !r.Running || r.AppliedVersion != 1 {
			t.Fatal("offline startup did not recover saved configuration", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no heartbeat")
	}
	readPID := func() int {
		raw, e := os.ReadFile(a.opts.Xray + ".pid")
		if e != nil {
			t.Fatal(e)
		}
		pid, e := strconv.Atoi(strings.TrimSpace(string(raw)))
		if e != nil {
			t.Fatal(e)
		}
		return pid
	}
	oldPID := readPID()
	process, err := os.FindProcess(oldPID)
	if err != nil {
		t.Fatal(err)
	}
	if err = process.Kill(); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(6 * time.Second)
	for {
		select {
		case r := <-reports:
			if r.Running && readPID() != oldPID {
				return
			}
		case <-deadline:
			t.Fatal("panel outage prevented process recovery")
		}
	}
}
func TestPendingConfigBackoffDoesNotBlockRecovery(t *testing.T) {
	a := fake(t)
	ctx := context.Background()
	if err := a.apply(ctx, model.Task{Version: 1, Running: true, Config: json.RawMessage(`{"log":{}}`)}); err != nil {
		t.Fatal(err)
	}
	a.stop()
	a.failedVersion = 2
	a.retryAt = time.Now().Add(5 * time.Minute)
	retry := a.retryAt
	panel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(model.Task{Version: 2, Running: true, Config: json.RawMessage(`{"reject":true}`)})
	}))
	defer panel.Close()
	a.opts.Panel = panel.URL
	defer a.invalidateCollection()
	if err := a.cycle(ctx); err != nil {
		t.Fatal(err)
	}
	if !a.running() || a.state.Version != 1 || a.retryAt != retry {
		t.Fatal("old process recovery mixed with candidate retry")
	}
}
func TestLocalRecoveryGatesAndBackoff(t *testing.T) {
	a := fake(t)
	ctx := context.Background()
	if err := a.apply(ctx, model.Task{Version: 1, Running: false, Config: json.RawMessage(`{"log":{}}`)}); err != nil {
		t.Fatal(err)
	}
	if err := a.recoverLocal(ctx); err != nil || a.running() {
		t.Fatal("operator stop ignored")
	}
	a.state.DesiredRunning = true
	a.state.Version = 0
	if err := a.recoverLocal(ctx); err != nil || a.running() {
		t.Fatal("unapplied config started")
	}
	a.state.Version = 1
	if err := os.WriteFile(a.opts.Directory+"/config.json", []byte(`{"reject":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.recoverLocal(ctx); err == nil || a.running() {
		t.Fatal("invalid local configuration started")
	}
	retry := a.recoveryAt
	if err := a.recoverLocal(ctx); err != nil || a.recoveryAt != retry {
		t.Fatal("recovery failure retry was not throttled")
	}
	a.recoveryAt = time.Time{}
	a.recoveryDelay = time.Minute
	if err := a.recoverLocal(ctx); err == nil || a.recoveryDelay != time.Minute {
		t.Fatal("recovery backoff cap missing")
	}
}
func TestStopDuringConfigBackoffPersists(t *testing.T) {
	a := fake(t)
	ctx := context.Background()
	if err := a.apply(ctx, model.Task{Version: 1, Running: true, Config: json.RawMessage(`{"log":{}}`)}); err != nil {
		t.Fatal(err)
	}
	a.failedVersion = 2
	a.retryAt = time.Now().Add(time.Minute)
	panel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(model.Task{Version: 2, Running: false, Config: json.RawMessage(`{"reject":true}`)})
	}))
	defer panel.Close()
	a.opts.Panel = panel.URL
	a.state.Panel = panel.URL
	if err := a.cycle(ctx); err != nil {
		t.Fatal(err)
	}
	if a.running() {
		t.Fatal("stop delayed by config backoff")
	}
	next, err := New(a.opts)
	if err != nil {
		t.Fatal(err)
	}
	defer next.stop()
	if next.state.DesiredRunning {
		t.Fatal("stop was not persisted")
	}
	if err = next.recoverLocal(ctx); err != nil || next.running() {
		t.Fatal("offline recovery undid stop")
	}
}

func TestStopCheckpointWriteRetriedDuringBackoff(t *testing.T) {
	a := fake(t)
	ctx := context.Background()
	if err := a.apply(ctx, model.Task{Version: 1, Running: true, Config: json.RawMessage(`{"log":{}}`)}); err != nil {
		t.Fatal(err)
	}
	panel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(model.Task{Version: 2, Running: false, Config: json.RawMessage(`{"reject":true}`)})
	}))
	defer panel.Close()
	a.opts.Panel = panel.URL
	a.state.Panel = panel.URL
	a.failedVersion = 2
	a.retryAt = time.Now().Add(time.Minute)
	path := a.opts.Directory + "/agent.json"
	if err := os.Rename(path, path+".test-backup"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := a.cycle(ctx); err == nil {
		t.Fatal("expected checkpoint failure")
	}
	if a.running() {
		t.Fatal("failed persistence prevented stop")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".test-backup", path); err != nil {
		t.Fatal(err)
	}
	if err := a.cycle(ctx); err != nil {
		t.Fatal(err)
	}
	next, err := New(a.opts)
	if err != nil {
		t.Fatal(err)
	}
	if next.state.DesiredRunning {
		t.Fatal("dirty stop checkpoint not retried during config backoff")
	}
}

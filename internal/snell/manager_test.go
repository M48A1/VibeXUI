package snell

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
	"vibexui/internal/model"
)

func waitStatus(t *testing.T, m *Manager, id string, predicate func(model.SnellStatus) bool) model.SnellStatus {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		s := m.Status()[id]
		if predicate(s) {
			return s
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatalf("status did not converge: %+v", m.Status())
	return model.SnellStatus{}
}
func port(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}
func TestConfigRejectsInjection(t *testing.T) {
	n := model.SnellInbound{ID: model.ID(), Revision: 1, Name: "test", Listen: "127.0.0.1", Port: 6160, PSK: model.Secret()}
	if _, err := model.SnellConfig(n); err != nil {
		t.Fatal(err)
	}
	n.PSK = "secret\nlisten = 0.0.0.0:22"
	if _, err := model.SnellConfig(n); err == nil {
		t.Fatal("INI injection accepted")
	}
	n.PSK = model.Secret()
	n.ID = "../../injection"
	if _, err := model.SnellConfig(n); err == nil {
		t.Fatal("path traversal accepted")
	}
}
func TestRealSnellLifecycle(t *testing.T) {
	binary := os.Getenv("SNELL_TEST_BINARY")
	if binary == "" {
		t.Skip("set SNELL_TEST_BINARY to validate official Snell server")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := t.TempDir()
	download := func(context.Context, string) (string, error) { return binary, nil }
	m := newManager(ctx, dir, download)
	defer func() {
		if m != nil {
			m.Close()
		}
	}()
	n := model.SnellInbound{ID: model.ID(), ServerID: "server", Name: "Snell", Listen: "127.0.0.1", Port: port(t), PSK: model.Secret(), Enabled: true, TFO: true, Revision: 1}
	m.Submit([]model.SnellInbound{n})
	waitStatus(t, m, n.ID, func(s model.SnellStatus) bool { return s.Running && s.Revision == 1 })
	info, err := os.Stat(filepath.Join(dir, "snell", n.ID+".conf"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("config permissions", err)
	}
	// Editing to an occupied port must restore the previous listener.
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	oldPort := n.Port
	n.Port = occupied.Addr().(*net.TCPAddr).Port
	n.Revision = 2
	m.Submit([]model.SnellInbound{n})
	waitStatus(t, m, n.ID, func(s model.SnellStatus) bool { return s.Error != "" && s.Running && s.Revision == 1 })
	m.Close()
	m = nil
	m = newManager(ctx, dir, download)
	waitStatus(t, m, n.ID, func(s model.SnellStatus) bool { return s.Error != "" && s.Running && s.Revision == 1 })
	n.Port = oldPort
	n.Revision = 3
	n.PSK = model.Secret()
	m.Submit([]model.SnellInbound{n})
	waitStatus(t, m, n.ID, func(s model.SnellStatus) bool { return s.Running && s.Revision == 3 })
	m.Close()
	m = nil
	// An Agent restart recovers the last saved desired listener without a panel request.
	m = newManager(ctx, dir, download)
	waitStatus(t, m, n.ID, func(s model.SnellStatus) bool { return s.Running && s.Revision == 3 })
	n.Enabled = false
	n.Revision = 4
	m.Submit([]model.SnellInbound{n})
	waitStatus(t, m, n.ID, func(s model.SnellStatus) bool { return !s.Running && s.State == "stopped" && s.Revision == 4 })
	n.Enabled = true
	n.Revision = 5
	m.Submit([]model.SnellInbound{n})
	waitStatus(t, m, n.ID, func(s model.SnellStatus) bool { return s.Running && s.Revision == 5 })
	m.Submit([]model.SnellInbound{})
	waitStatus(t, m, n.ID, func(s model.SnellStatus) bool { return s.State == "" })
	if _, err = os.Stat(filepath.Join(dir, "snell", n.ID+".conf")); !os.IsNotExist(err) {
		t.Fatal("deleted listener config retained")
	}
}

func TestOfficialDownload(t *testing.T) {
	if os.Getenv("SNELL_DOWNLOAD_TEST") == "" {
		t.Skip("set SNELL_DOWNLOAD_TEST=1 for official download verification")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
	defer cancel()
	p, err := install(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(p)
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatal("installed file invalid", err)
	}
	if cached, err := install(ctx, filepath.Dir(p)); err != nil || cached != p {
		t.Fatal("cached binary not reused", err)
	}
}

package snell

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
	"vibexui/internal/model"
)

type process struct {
	cmd               *exec.Cmd
	done              chan error
	config            model.SnellInbound
	retryAt           time.Time
	delay             time.Duration
	attemptedRevision int64
}
type Manager struct {
	dir       string
	jobs      chan []model.SnellInbound
	done      chan struct{}
	cancel    context.CancelFunc
	mu        sync.Mutex
	status    map[string]model.SnellStatus
	processes map[string]*process
	download  func(context.Context, string) (string, error)
}

func New(ctx context.Context, dir string) *Manager {
	return newManager(ctx, dir, install)
}
func newManager(ctx context.Context, dir string, download func(context.Context, string) (string, error)) *Manager {
	ctx, cancel := context.WithCancel(ctx)
	m := &Manager{dir: filepath.Join(dir, "snell"), jobs: make(chan []model.SnellInbound, 1), done: make(chan struct{}), cancel: cancel, status: map[string]model.SnellStatus{}, processes: map[string]*process{}, download: download}
	go m.run(ctx)
	return m
}
func (m *Manager) Close() { m.cancel(); <-m.done }
func (m *Manager) Submit(nodes []model.SnellInbound) {
	if nodes == nil {
		return
	}
	copy := append([]model.SnellInbound{}, nodes...)
	select {
	case m.jobs <- copy:
	default:
		select {
		case <-m.jobs:
		default:
		}
		select {
		case m.jobs <- copy:
		default:
		}
	}
}
func (m *Manager) Status() map[string]model.SnellStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	copy := map[string]model.SnellStatus{}
	for id, v := range m.status {
		copy[id] = v
	}
	return copy
}
func (m *Manager) set(id string, s model.SnellStatus) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status[id] = s
}
func (m *Manager) stop(p *process) {
	if p == nil || p.cmd == nil {
		return
	}
	p.cmd.Process.Signal(os.Interrupt)
	select {
	case <-p.done:
	case <-time.After(2 * time.Second):
		p.cmd.Process.Kill()
		<-p.done
	}
	p.cmd = nil
}
func (m *Manager) launch(ctx context.Context, path string, n model.SnellInbound) (*process, error) {
	cfg, err := model.SnellConfig(n)
	if err != nil {
		return nil, err
	}
	name := filepath.Join(m.dir, n.ID+".conf")
	if err = writeFile(name, cfg, 0600); err != nil {
		return nil, err
	}
	cmd := exec.Command(path, "-c", name)
	cmd.WaitDelay = time.Second
	// Do not forward Snell's raw logs: startup messages may contain sensitive configuration.
	if err = cmd.Start(); err != nil {
		return nil, fmt.Errorf("Snell 启动失败，请检查运行环境")
	}
	p := &process{cmd: cmd, done: make(chan error, 1), config: n}
	go func() { p.done <- cmd.Wait() }()
	timer := time.NewTimer(700 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-p.done:
		p.cmd = nil
		return nil, fmt.Errorf("Snell 启动后退出，请检查端口占用或系统兼容性")
	case <-ctx.Done():
		m.stop(p)
		return nil, ctx.Err()
	case <-timer.C:
	}
	host := n.Listen
	if host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	if host == "::" {
		host = "::1"
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, fmt.Sprint(n.Port)), time.Second)
	if err != nil {
		m.stop(p)
		return nil, fmt.Errorf("Snell 监听检查失败")
	}
	conn.Close()
	return p, nil
}
func (m *Manager) run(ctx context.Context) {
	defer close(m.done)
	defer func() {
		for _, p := range m.processes {
			m.stop(p)
		}
	}()
	if err := os.MkdirAll(m.dir, 0700); err != nil {
		return
	}
	var desired []model.SnellInbound
	// Persisted desired state allows offline recovery after an Agent restart.
	if raw, err := os.ReadFile(filepath.Join(m.dir, "desired.json")); err == nil {
		json.Unmarshal(raw, &desired)
	}
	if len(desired) > 16 {
		desired = nil
	}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case nodes := <-m.jobs:
			if len(nodes) > 16 {
				continue
			}
			valid := true
			for _, n := range nodes {
				if model.ValidateSnell(n) != nil {
					valid = false
					break
				}
			}
			if !valid {
				continue
			}
			raw, _ := json.Marshal(nodes)
			old, _ := json.Marshal(desired)
			if string(raw) != string(old) {
				if err := writeFile(filepath.Join(m.dir, "desired.json"), raw, 0600); err != nil {
					for _, n := range nodes {
						m.set(n.ID, model.SnellStatus{State: "error", Error: "Snell 配置保存失败"})
					}
					continue
				}
			}
			kept := map[string]bool{}
			for _, n := range nodes {
				kept[n.ID] = true
			}
			for _, n := range desired {
				if !kept[n.ID] && model.ValidateSnell(n) == nil {
					os.Remove(filepath.Join(m.dir, n.ID+".conf"))
					os.Remove(filepath.Join(m.dir, n.ID+".last.json"))
				}
			}
			desired = nodes
		case <-ticker.C:
		}
		wanted := map[string]bool{}
		for _, n := range desired {
			wanted[n.ID] = true
		}
		for id, p := range m.processes {
			if !wanted[id] {
				m.stop(p)
				os.Remove(filepath.Join(m.dir, id+".conf"))
				os.Remove(filepath.Join(m.dir, id+".last.json"))
				delete(m.processes, id)
			}
		}
		m.mu.Lock()
		for id := range m.status {
			if !wanted[id] {
				delete(m.status, id)
			}
		}
		m.mu.Unlock()
		for _, n := range desired {
			if ctx.Err() != nil {
				return
			}
			if model.ValidateSnell(n) != nil {
				continue
			}
			p := m.processes[n.ID]
			if !n.Enabled {
				m.stop(p)
				delete(m.processes, n.ID)
				m.set(n.ID, model.SnellStatus{Revision: n.Revision, State: "stopped"})
				continue
			}
			if p != nil && p.cmd != nil {
				select {
				case <-p.done:
					p.cmd = nil
					m.set(n.ID, model.SnellStatus{Revision: p.config.Revision, State: "error", Error: "Snell 进程退出，等待重试"})
				default:
				}
			}
			if p != nil && p.config.Revision == n.Revision && p.cmd != nil {
				m.set(n.ID, model.SnellStatus{Revision: n.Revision, Running: true, State: "running"})
				continue
			}
			if p != nil && p.attemptedRevision == n.Revision && time.Now().Before(p.retryAt) {
				continue
			}
			m.set(n.ID, model.SnellStatus{State: "installing"})
			binary, err := m.download(ctx, m.dir)
			var next *process
			if err == nil {
				m.stop(p)
				next, err = m.launch(ctx, binary, n)
			}
			if err == nil {
				raw, _ := json.Marshal(n)
				if saveErr := writeFile(filepath.Join(m.dir, n.ID+".last.json"), raw, 0600); saveErr != nil {
					m.stop(next)
					err = fmt.Errorf("Snell 已应用配置保存失败")
				}
			}
			if err == nil {
				m.processes[n.ID] = next
				m.set(n.ID, model.SnellStatus{Revision: n.Revision, Running: true, State: "running"})
				continue
			}
			if p == nil {
				if raw, e := os.ReadFile(filepath.Join(m.dir, n.ID+".last.json")); e == nil {
					var previous model.SnellInbound
					if json.Unmarshal(raw, &previous) == nil && previous.ID == n.ID && model.ValidateSnell(previous) == nil {
						p = &process{config: previous}
					}
				}
			}
			status := model.SnellStatus{State: "error", Error: err.Error()}
			delay := 10 * time.Second
			if p != nil && p.attemptedRevision == n.Revision {
				delay = p.delay * 2
				if delay < 10*time.Second {
					delay = 10 * time.Second
				}
			}
			if delay > 5*time.Minute {
				delay = 5 * time.Minute
			}
			// Restore the previous listener after a failed edit; report its actual revision.
			failed := &process{config: n, delay: delay, retryAt: time.Now().Add(delay)}
			if p != nil && p.cmd != nil {
				status.Running = true
				status.Revision = p.config.Revision
				failed = p
			} else if p != nil && p.config.Revision != n.Revision && binary != "" {
				if restored, e := m.launch(ctx, binary, p.config); e == nil {
					status.Running = true
					status.Revision = p.config.Revision
					failed = restored
				}
			}
			failed.attemptedRevision = n.Revision
			failed.retryAt = time.Now().Add(delay)
			failed.delay = delay
			m.processes[n.ID] = failed
			m.set(n.ID, status)
		}
	}
}

package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"vibexui/internal/kernel"
	"vibexui/internal/model"
)

type Options struct {
	Panel, ID, RegistrationToken, Directory, Xray string
	Interval                                      time.Duration
}
type checkpoint struct {
	RestartVersion        int64                    `json:"restartVersion"`
	Kernel                model.KernelReport       `json:"kernel"`
	KernelPath            string                   `json:"kernelPath"`
	PreviousKernelPath    string                   `json:"previousKernelPath"`
	PreviousKernelVersion string                   `json:"previousKernelVersion"`
	IPBindings            []string                 `json:"ipBindings"`
	Token                 string                   `json:"token"`
	Panel                 string                   `json:"panel"`
	ID                    string                   `json:"id"`
	Version               int64                    `json:"version"`
	DesiredRunning        bool                     `json:"desiredRunning"`
	Upload                uint64                   `json:"upload"`
	Download              uint64                   `json:"download"`
	LastUpload            uint64                   `json:"lastUpload"`
	LastDownload          uint64                   `json:"lastDownload"`
	StatsEpoch            string                   `json:"statsEpoch"`
	NodeTraffic           map[string]model.Traffic `json:"nodeTraffic"`
	LastNodeTraffic       map[string]model.Traffic `json:"lastNodeTraffic"`
	ClientTraffic         map[string]model.Traffic `json:"clientTraffic"`
	LastClientTraffic     map[string]model.Traffic `json:"lastClientTraffic"`
}
type Agent struct {
	saved          []byte
	failedVersion  int64
	retryAt        time.Time
	retryDelay     time.Duration
	collection     chan collectedStats
	collectCancel  context.CancelFunc
	generation     uint64
	statsAt        time.Time
	ipAt           time.Time
	kernelDownload chan kernelResult
	kernelCancel   context.CancelFunc
	downloadKernel func(context.Context, string, string, string) (string, error)
	opts           Options
	client         *http.Client
	state          checkpoint
	process        *exec.Cmd
	done           chan error
	lastError      string
	statsError     string
	version        string
}

func New(o Options) (*Agent, error) {
	u, err := url.Parse(o.Panel)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return nil, fmt.Errorf("panel 地址无效")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1")) {
		return nil, fmt.Errorf("Agent 必须通过 HTTPS 连接主面板（本机调试除外）")
	}
	if o.ID == "" {
		return nil, fmt.Errorf("请提供 server-id")
	}
	if o.Interval < time.Second {
		o.Interval = 10 * time.Second
	}
	if o.Xray == "" {
		o.Xray = "xray"
	}
	path, err := exec.LookPath(o.Xray)
	if err != nil {
		return nil, fmt.Errorf("找不到 Xray-core：%w", err)
	}
	o.Xray, err = filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	o.Directory, err = filepath.Abs(o.Directory)
	if err != nil {
		return nil, err
	}
	o.Panel = strings.TrimRight(o.Panel, "/")
	if err = os.MkdirAll(o.Directory, 0700); err != nil {
		return nil, err
	}
	a := &Agent{opts: o, client: &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	raw, err := os.ReadFile(filepath.Join(o.Directory, "agent.json"))
	if err == nil {
		if err = json.Unmarshal(raw, &a.state); err != nil {
			return nil, err
		}
		if a.state.Panel != o.Panel || a.state.ID != o.ID {
			return nil, fmt.Errorf("本地凭据属于其他面板或服务器，请使用独立 data-dir")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	a.state.Panel = o.Panel
	a.state.ID = o.ID
	if a.state.StatsEpoch == "" {
		a.state.StatsEpoch = model.Secret()
	}
	if a.state.NodeTraffic == nil {
		a.state.NodeTraffic = map[string]model.Traffic{}
	}
	if a.state.LastNodeTraffic == nil {
		a.state.LastNodeTraffic = map[string]model.Traffic{}
	}
	if a.state.ClientTraffic == nil {
		a.state.ClientTraffic = map[string]model.Traffic{}
	}
	if a.state.LastClientTraffic == nil {
		a.state.LastClientTraffic = map[string]model.Traffic{}
	}
	a.downloadKernel = kernel.NewCatalog().Download
	if a.state.KernelPath != "" {
		if _, err = os.Stat(a.state.KernelPath); err != nil {
			return nil, fmt.Errorf("已保存的内核文件不可用：%w", err)
		}
		a.opts.Xray = a.state.KernelPath
	}
	if a.state.Kernel.State == "downloading" || a.state.Kernel.State == "switching" {
		a.kernelFailed(fmt.Errorf("上次切换因 Agent 重启而中断，已保留原内核，请重试"))
	}
	return a, nil
}
func atomicWrite(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".vibexui-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
func (a *Agent) save() error {
	raw, err := json.MarshalIndent(a.state, "", "  ")
	if err != nil {
		return err
	}
	if bytes.Equal(raw, a.saved) {
		return nil
	}
	if err = atomicWrite(filepath.Join(a.opts.Directory, "agent.json"), raw); err != nil {
		return err
	}
	a.saved = append(a.saved[:0], raw...)
	return nil
}
func (a *Agent) request(ctx context.Context, path, token string, in, out any) error {
	raw, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", a.opts.Panel+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	res, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("面板返回 HTTP %d", res.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(out)
}
func (a *Agent) running() bool {
	if a.process == nil {
		return false
	}
	select {
	case err := <-a.done:
		a.process = nil
		if err != nil {
			a.lastError = "Xray 退出：" + err.Error()
		}
		return false
	default:
		return true
	}
}
func (a *Agent) start(ctx context.Context) error {
	if a.running() {
		return nil
	}
	a.invalidateCollection()
	a.state.LastUpload = 0
	a.state.LastDownload = 0
	a.state.LastClientTraffic = map[string]model.Traffic{}
	a.state.LastNodeTraffic = map[string]model.Traffic{}
	cmd := exec.Command(a.opts.Xray, "run", "-config", filepath.Join(a.opts.Directory, "config.json"))
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	a.process = cmd
	a.done = make(chan error, 1)
	done := a.done
	go func() { done <- cmd.Wait() }()
	timer := time.NewTimer(600 * time.Millisecond)
	defer timer.Stop()
	select {
	case err := <-done:
		a.process = nil
		return fmt.Errorf("Xray 启动后退出：%v", err)
	case <-ctx.Done():
		a.stop()
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
func (a *Agent) stop() {
	a.invalidateCollection()
	if !a.running() {
		return
	}
	a.process.Process.Signal(os.Interrupt)
	select {
	case <-a.done:
	case <-time.After(5 * time.Second):
		a.process.Process.Kill()
		<-a.done
	}
	a.process = nil
}
func (a *Agent) command(ctx context.Context, args ...string) ([]byte, error) {
	c, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(c, a.opts.Xray, args...)
	cmd.WaitDelay = time.Second
	out, err := cmd.CombinedOutput()
	if err != nil && len(out) > 3000 {
		out = out[:3000]
	}
	if err != nil {
		return nil, fmt.Errorf("Xray 命令失败：%s (%w)", strings.TrimSpace(string(out)), err)
	}
	return out, nil
}
func (a *Agent) apply(ctx context.Context, t model.Task) error {
	if len(t.Config) == 0 || !json.Valid(t.Config) {
		return fmt.Errorf("配置内容无效")
	}
	if current, err := os.ReadFile(filepath.Join(a.opts.Directory, "config.json")); err == nil && sameConfig(current, t.Config) && t.RestartVersion <= a.state.RestartVersion {
		old := a.state
		if t.Running {
			if err := a.start(ctx); err != nil {
				return err
			}
		} else {
			a.invalidateCollection()
			a.stats(ctx)
			a.stop()
		}
		a.state.Version = t.Version
		a.state.DesiredRunning = t.Running
		if err := a.save(); err != nil {
			a.state.Version = old.Version
			a.state.DesiredRunning = old.DesiredRunning
			return err
		}
		return nil
	}
	candidate := filepath.Join(a.opts.Directory, "candidate.json")
	if err := atomicWrite(candidate, t.Config); err != nil {
		return err
	}
	defer os.Remove(candidate)
	if _, err := a.command(ctx, "run", "-test", "-config", candidate); err != nil {
		return err
	}
	path := filepath.Join(a.opts.Directory, "config.json")
	previous, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if len(previous) > 0 {
		if err = atomicWrite(filepath.Join(a.opts.Directory, "config.previous.json"), previous); err != nil {
			return err
		}
	}
	// Capture the final counters before stopping or replacing the process.
	a.invalidateCollection()
	a.stats(ctx)
	oldState := a.state
	wasRunning := a.running()
	a.stop()
	if err = atomicWrite(path, t.Config); err == nil && t.Running {
		err = a.start(ctx)
	}
	if err == nil {
		a.state.Version = t.Version
		a.state.RestartVersion = t.RestartVersion
		a.state.DesiredRunning = t.Running
		a.state.LastUpload = 0
		a.state.LastDownload = 0
		err = a.save()
	}
	if err != nil {
		a.stop()
		a.state = oldState
		var rollback error
		if len(previous) > 0 {
			rollback = atomicWrite(path, previous)
			if rollback == nil && wasRunning {
				a.state.LastUpload = 0
				a.state.LastDownload = 0
				rollback = a.start(ctx)
			}
		} else {
			rollback = os.Remove(path)
			if errors.Is(rollback, os.ErrNotExist) {
				rollback = nil
			}
		}
		if rollback != nil {
			return fmt.Errorf("应用失败：%v；回退失败：%v", err, rollback)
		}
		return fmt.Errorf("应用失败，已恢复上一配置：%w", err)
	}
	return nil
}
func (a *Agent) stats(ctx context.Context) {
	if !a.running() {
		a.statsError = ""
		return
	}
	out, err := a.command(ctx, "api", "statsquery", "-s=127.0.0.1:10085", "-pattern=", "-reset=false")
	if err != nil {
		a.statsError = err.Error()
		return
	}
	a.ingestStats(out)
}
func (a *Agent) ingestStats(out []byte) {
	var err error
	var raw struct {
		Stat []struct {
			Name  string      `json:"name"`
			Value json.Number `json:"value"`
		} `json:"stat"`
	}
	if err = json.Unmarshal(out, &raw); err != nil {
		a.statsError = "无法解析 Xray 统计结果"
		return
	}
	var up, down uint64
	users := map[string]model.Traffic{}
	nodes := map[string]model.Traffic{}
	for _, v := range raw.Stat {
		value, e := v.Value.Int64()
		if e != nil || value < 0 {
			continue
		}
		parts := strings.Split(v.Name, ">>>")
		if len(parts) != 4 || parts[2] != "traffic" {
			continue
		}
		switch parts[0] {
		case "inbound":
			t := nodes[parts[1]]
			if parts[3] == "uplink" {
				t.Upload += uint64(value)
			}
			if parts[3] == "downlink" {
				t.Download += uint64(value)
			}
			nodes[parts[1]] = t
			if parts[3] == "uplink" {
				up += uint64(value)
			}
			if parts[3] == "downlink" {
				down += uint64(value)
			}
		case "user":
			t := users[model.ClientIDFromEmail(parts[1])]
			if parts[3] == "uplink" {
				t.Upload += uint64(value)
			}
			if parts[3] == "downlink" {
				t.Download += uint64(value)
			}
			users[model.ClientIDFromEmail(parts[1])] = t
		}
	}
	for id, current := range nodes {
		previous := a.state.LastNodeTraffic[id]
		total := a.state.NodeTraffic[id]
		total.Upload += counterDelta(current.Upload, previous.Upload)
		total.Download += counterDelta(current.Download, previous.Download)
		a.state.NodeTraffic[id] = total
		a.state.LastNodeTraffic[id] = current
	}
	for id, current := range users {
		previous := a.state.LastClientTraffic[id]
		total := a.state.ClientTraffic[id]
		total.Upload += counterDelta(current.Upload, previous.Upload)
		total.Download += counterDelta(current.Download, previous.Download)
		a.state.ClientTraffic[id] = total
		a.state.LastClientTraffic[id] = current
	}
	if up >= a.state.LastUpload {
		a.state.Upload += up - a.state.LastUpload
	} else {
		a.state.Upload += up
	}
	if down >= a.state.LastDownload {
		a.state.Download += down - a.state.LastDownload
	} else {
		a.state.Download += down
	}
	a.state.LastUpload = up
	a.state.LastDownload = down
	a.statsError = ""
	a.statsAt = time.Now()
	if err = a.save(); err != nil {
		a.statsError = "统计持久化失败：" + err.Error()
	}
}
func (a *Agent) cycle(ctx context.Context) error {
	onlineIPs, ipError := a.consumeCollection()
	defer a.beginCollection(ctx)
	in := struct {
		ID string `json:"id"`
		model.Report
	}{ID: a.opts.ID, Report: model.Report{StatsCollectedAt: a.statsAt, IPCollectedAt: a.ipAt, Kernel: a.kernelReport(), OnlineIPs: onlineIPs, IPStatsError: limit(ipError, 900), AppliedVersion: a.state.Version, Running: a.running(), XrayVersion: limit(a.version, 180), Error: limit(a.lastError, 3500), Upload: a.state.Upload, Download: a.state.Download, StatsError: limit(a.statsError, 900), StatsEpoch: a.state.StatsEpoch, ClientTraffic: a.state.ClientTraffic, NodeTraffic: a.state.NodeTraffic}}
	var task model.Task
	if err := a.request(ctx, "/api/agent/poll", a.state.Token, in, &task); err != nil {
		return err
	}
	defer func() { a.state.DesiredRunning = task.Running; a.handleKernel(ctx, task.Kernel) }()
	if task.NodeIDs != nil {
		valid := map[string]bool{}
		for _, id := range task.NodeIDs {
			valid[id] = true
		}
		for id := range a.state.NodeTraffic {
			if !valid[id] {
				delete(a.state.NodeTraffic, id)
				delete(a.state.LastNodeTraffic, id)
			}
		}
	}
	if task.IPBindings != nil {
		a.state.IPBindings = task.IPBindings
	}
	if task.ClientIDs != nil {
		valid := map[string]bool{}
		for _, id := range task.ClientIDs {
			valid[id] = true
		}
		for id := range a.state.ClientTraffic {
			if !valid[id] {
				delete(a.state.ClientTraffic, id)
				delete(a.state.LastClientTraffic, id)
			}
		}
	}
	if task.Version != a.state.Version {
		if !task.Running && a.running() {
			a.invalidateCollection()
			a.stats(ctx)
			a.stop()
			a.state.DesiredRunning = false
			if err := a.save(); err != nil {
				return err
			}
		}
		if a.failedVersion == task.Version && time.Now().Before(a.retryAt) {
			return nil
		}
		if err := a.apply(ctx, task); err != nil {
			a.noteApplyFailure(task.Version)
			a.lastError = err.Error()
			return err
		}
		a.lastError = ""
		a.failedVersion = 0
		a.retryDelay = 0
		a.retryAt = time.Time{}
	} else {
		if task.Running {
			if !a.running() {
				a.state.LastUpload = 0
				a.state.LastDownload = 0
			}
			if err := a.start(ctx); err != nil {
				a.lastError = err.Error()
				return err
			}
		} else {
			a.invalidateCollection()
			a.stats(ctx)
			a.stop()
		}
		a.state.DesiredRunning = task.Running
		if err := a.save(); err != nil {
			a.lastError = err.Error()
			return err
		}
		a.lastError = ""
	}
	return nil
}

func counterDelta(current, previous uint64) uint64 {
	if current >= previous {
		return current - previous
	}
	return current
}

func limit(value string, size int) string {
	if len(value) > size {
		return string([]rune(value[:size-4])) + "…"
	}
	return value
}
func (a *Agent) Register(ctx context.Context) error {
	if a.state.Token == "" {
		if a.opts.RegistrationToken == "" {
			return fmt.Errorf("首次启动需要 registration-token")
		}
		var out struct {
			Token string `json:"token"`
		}
		if err := a.request(ctx, "/api/agent/register", a.opts.RegistrationToken, map[string]string{"id": a.opts.ID}, &out); err != nil {
			return err
		}
		if out.Token == "" {
			return fmt.Errorf("面板未返回凭据")
		}
		a.state.Token = out.Token
		if err := a.save(); err != nil {
			return fmt.Errorf("保存凭据失败，请重新生成注册令牌：%w", err)
		}
	}
	a.opts.RegistrationToken = ""
	return nil
}

func (a *Agent) Run(ctx context.Context) error {
	defer a.invalidateCollection()
	defer a.stop()
	defer func() {
		if a.kernelCancel != nil {
			a.kernelCancel()
		}
	}()
	if err := a.Register(ctx); err != nil {
		return err
	}
	out, err := a.command(ctx, "version")
	if err == nil {
		a.version = strings.Split(string(out), "\n")[0]
	}
	a.state.LastUpload = 0
	a.state.LastDownload = 0
	ticker := time.NewTicker(a.opts.Interval)
	defer ticker.Stop()
	for {
		if err := a.cycle(ctx); err != nil {
			log.Printf("Agent: %v", err)
		}
		select {
		case <-ctx.Done():
			a.stats(context.Background())
			return nil
		case <-ticker.C:
		}
	}
}

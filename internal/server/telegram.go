package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	_ "time/tzdata"
	"vibexui/internal/model"
	"vibexui/internal/store"
)

const telegramSettingsKey = "telegram.settings"
const telegramRuntimeKey = "telegram.runtime"

type telegramSettings struct {
	Enabled     bool     `json:"enabled"`
	Token       string   `json:"token,omitempty"`
	ChatIDs     []string `json:"chatIds"`
	Login       bool     `json:"login"`
	Servers     bool     `json:"servers"`
	Xray        bool     `json:"xray"`
	Expiry      bool     `json:"expiry"`
	ExpiryDays  int      `json:"expiryDays"`
	Traffic     bool     `json:"traffic"`
	RemainingGB float64  `json:"remainingGB"`
	IPLimit     bool     `json:"ipLimit"`
	Daily       bool     `json:"daily"`
	ReportTime  string   `json:"reportTime"`
	Timezone    string   `json:"timezone"`
}
type telegramStatus struct {
	LastAttempt time.Time `json:"lastAttempt"`
	LastSuccess time.Time `json:"lastSuccess"`
	LastError   string    `json:"lastError"`
}
type telegramReceipt struct {
	Fingerprint string    `json:"fingerprint"`
	RetryAt     time.Time `json:"retryAt"`
}
type telegramEvent struct {
	Key         string    `json:"key"`
	Fingerprint string    `json:"fingerprint"`
	Text        string    `json:"text"`
	Created     time.Time `json:"created"`
}
type telegramRuntime struct {
	Config   string                     `json:"config"`
	Receipts map[string]telegramReceipt `json:"receipts"`
	Health   map[string]string          `json:"health"`
	Pending  []telegramEvent            `json:"pending"`
	Retry    map[string]time.Time       `json:"retry"`
	Errors   map[string]string          `json:"errors"`
	Status   telegramStatus             `json:"status"`
}
type telegramNotifier struct {
	db        *store.Store
	publicURL string
	configMu  sync.Mutex
	runMu     sync.Mutex
	client    *http.Client
	logins    chan telegramEvent
	wake      chan struct{}
}

func newTelegram(db *store.Store, publicURL string) *telegramNotifier {
	return &telegramNotifier{db: db, publicURL: publicURL, client: &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, logins: make(chan telegramEvent, 64), wake: make(chan struct{}, 1)}
}
func defaultTelegramSettings() telegramSettings {
	return telegramSettings{ChatIDs: []string{}, Login: true, Servers: true, Xray: true, Expiry: true, ExpiryDays: 3, Traffic: true, RemainingGB: 1, IPLimit: true, Daily: true, ReportTime: "09:00", Timezone: "Asia/Shanghai"}
}
func (t *telegramNotifier) settings() (telegramSettings, error) {
	c := defaultTelegramSettings()
	raw, err := t.db.Setting(telegramSettingsKey)
	if err == nil && raw != "" {
		err = json.Unmarshal([]byte(raw), &c)
	}
	return c, err
}
func (t *telegramNotifier) runtime() (telegramRuntime, error) {
	r := telegramRuntime{Receipts: map[string]telegramReceipt{}, Health: map[string]string{}}
	raw, err := t.db.Setting(telegramRuntimeKey)
	if err == nil && raw != "" {
		err = json.Unmarshal([]byte(raw), &r)
	}
	if r.Receipts == nil {
		r.Receipts = map[string]telegramReceipt{}
	}
	if r.Health == nil {
		r.Health = map[string]string{}
	}
	if r.Retry == nil {
		r.Retry = map[string]time.Time{}
	}
	if r.Errors == nil {
		r.Errors = map[string]string{}
	}
	return r, err
}

var telegramTokenPattern = regexp.MustCompile(`^[0-9]{1,20}:[A-Za-z0-9_-]{20,100}$`)

func validateTelegram(c *telegramSettings) error {
	c.Token = strings.TrimSpace(c.Token)
	if c.Token != "" && !telegramTokenPattern.MatchString(c.Token) {
		return errors.New("Bot Token 格式无效")
	}
	if len(c.ChatIDs) > 10 {
		return errors.New("最多设置 10 个 Chat ID")
	}
	ids := []string{}
	seen := map[string]bool{}
	for _, v := range c.ChatIDs {
		id, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		if err != nil || id == 0 {
			return errors.New("Chat ID 必须为非零整数，群组 ID 可为负数")
		}
		v = strconv.FormatInt(id, 10)
		if !seen[v] {
			ids = append(ids, v)
			seen[v] = true
		}
	}
	c.ChatIDs = ids
	if c.Enabled && (c.Token == "" || len(ids) == 0) {
		return errors.New("启用通知前请填写 Bot Token 和 Chat ID")
	}
	if c.ExpiryDays < 0 || c.ExpiryDays > 365 {
		return errors.New("到期预警天数须为 0–365")
	}
	if math.IsNaN(c.RemainingGB) || math.IsInf(c.RemainingGB, 0) || c.RemainingGB < 0 || c.RemainingGB > 1048576 {
		return errors.New("剩余流量预警须为 0–1048576 GB")
	}
	if _, err := time.Parse("15:04", c.ReportTime); err != nil {
		return errors.New("报告时间须为 HH:MM")
	}
	if _, err := time.LoadLocation(c.Timezone); err != nil {
		return errors.New("时区无效，例如 Asia/Shanghai")
	}
	return nil
}
func (s *Server) telegramGet(w http.ResponseWriter, r *http.Request) {
	c, err := s.telegram.settings()
	if err != nil {
		fail(w, 500, "无法读取通知设置")
		return
	}
	rt, err := s.telegram.runtime()
	if err != nil {
		fail(w, 500, "无法读取通知状态")
		return
	}
	hasToken := c.Token != ""
	c.Token = ""
	respond(w, 200, map[string]any{"settings": c, "hasToken": hasToken, "status": rt.Status})
}
func (s *Server) telegramSave(w http.ResponseWriter, r *http.Request) {
	var in struct {
		telegramSettings
		ClearToken bool `json:"clearToken"`
	}
	if !decode(w, r, &in) {
		return
	}
	t := s.telegram
	t.configMu.Lock()
	defer t.configMu.Unlock()
	old, err := t.settings()
	if err != nil {
		fail(w, 500, "无法读取通知设置")
		return
	}
	if in.ClearToken {
		in.Token = ""
	} else if strings.TrimSpace(in.Token) == "" {
		in.Token = old.Token
	}
	if err = validateTelegram(&in.telegramSettings); err != nil {
		fail(w, 400, err.Error())
		return
	}
	raw, err := json.Marshal(in.telegramSettings)
	if err == nil {
		err = t.db.SetSetting(telegramSettingsKey, string(raw))
	}
	if err != nil {
		fail(w, 500, "无法保存通知设置")
		return
	}
	select {
	case t.wake <- struct{}{}:
	default:
	}
	respond(w, 200, map[string]bool{"ok": true})
}

// All messages use plain text: user names cannot inject Telegram markup or links.
// Never return the transport error: net/http errors can include the token-bearing URL.
func (t *telegramNotifier) send(ctx context.Context, c telegramSettings, chat, text string) (time.Duration, error) {
	raw, _ := json.Marshal(map[string]any{"chat_id": chat, "text": text, "link_preview_options": map[string]bool{"is_disabled": true}})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.telegram.org/bot"+c.Token+"/sendMessage", bytes.NewReader(raw))
	if err != nil {
		return time.Minute, errors.New("无法创建 Telegram 请求")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.client.Do(req)
	if err != nil {
		return time.Minute, errors.New("Telegram 连接失败，请检查网络")
	}
	defer resp.Body.Close()
	var result struct {
		OK         bool `json:"ok"`
		Parameters struct {
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
	}
	err = json.NewDecoder(io.LimitReader(resp.Body, 65536)).Decode(&result)
	if resp.StatusCode == http.StatusTooManyRequests {
		delay := time.Duration(result.Parameters.RetryAfter) * time.Second
		if delay < time.Minute {
			delay = time.Minute
		}
		if delay > 24*time.Hour {
			delay = 24 * time.Hour
		}
		return delay, errors.New("Telegram 限流，稍后自动重试")
	}
	if resp.StatusCode != 200 || err != nil || !result.OK {
		return time.Minute, fmt.Errorf("Telegram 发送失败（HTTP %d），请检查 Token、Chat ID 及机器人聊天权限", resp.StatusCode)
	}
	return 0, nil
}
func (s *Server) telegramTest(w http.ResponseWriter, r *http.Request) {
	var in struct{}
	if !decode(w, r, &in) {
		return
	}
	t := s.telegram
	// Serialize test sends and worker delivery, without blocking login or Agent polling.
	if !t.runMu.TryLock() {
		fail(w, 409, "通知正在发送，请稍后重试")
		return
	}
	defer t.runMu.Unlock()
	c, err := t.settings()
	if err != nil || c.Token == "" || len(c.ChatIDs) == 0 {
		fail(w, 400, "请先保存 Bot Token 和 Chat ID")
		return
	}
	rt, err := t.runtime()
	if err != nil {
		fail(w, 500, "无法读取通知状态")
		return
	}
	if time.Since(rt.Status.LastAttempt) < 5*time.Second {
		fail(w, 429, "请等待 5 秒后重试")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	results := []map[string]string{}
	for _, chat := range c.ChatIDs {
		rt.Status.LastAttempt = time.Now()
		_, err = t.send(ctx, c, chat, "✅ VibeXUI · 测试通知\nTelegram 通知连接成功。\n面板："+t.publicURL)
		message := "发送成功"
		if err != nil {
			message = err.Error()
			rt.Errors[chat] = message
		} else {
			rt.Status.LastSuccess = time.Now()
			delete(rt.Errors, chat)
		}
		rt.Status.LastError = telegramErrors(c.ChatIDs, rt.Errors)
		results = append(results, map[string]string{"chatId": chat, "result": message})
	}
	raw, _ := json.Marshal(rt)
	if err = t.db.SetSetting(telegramRuntimeKey, string(raw)); err != nil {
		fail(w, 500, "无法保存通知状态")
		return
	}
	respond(w, 200, map[string]any{"results": results})
}
func shortTelegram(v string) string {
	v = strings.Join(strings.Fields(v), " ")
	r := []rune(v)
	if len(r) > 100 {
		v = string(r[:100]) + "…"
	}
	return v
}
func (s *Server) notifyLogin(r *http.Request, username string, success bool) {
	c, err := s.telegram.settings()
	if err != nil || !c.Enabled || !c.Login {
		return
	}
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		ip = r.RemoteAddr
	}
	// Only trust Caddy's forwarding header when the immediate peer is loopback.
	if parsed := net.ParseIP(ip); parsed != nil && parsed.IsLoopback() {
		forwarded := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-For"), ",")[0])
		if parsed = net.ParseIP(forwarded); parsed != nil {
			ip = parsed.String()
		}
	}
	status := "失败"
	if success {
		status = "成功"
	}
	now := time.Now()
	e := telegramEvent{Key: "login/" + strconv.FormatInt(now.UnixNano(), 10), Fingerprint: "login", Created: now, Text: fmt.Sprintf("🔐 面板登录%s\n账号：%s\n来源 IP：%s", status, shortTelegram(username), shortTelegram(ip))}
	select {
	case s.telegram.logins <- e:
	default:
	}
}
func (t *telegramNotifier) run(ctx context.Context) {
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	for {
		t.runMu.Lock()
		t.process(ctx, time.Now())
		t.runMu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-t.wake:
		}
	}
}
func (t *telegramNotifier) process(ctx context.Context, now time.Time) {
	c, err := t.settings()
	if err != nil {
		return
	}
	rt, err := t.runtime()
	if err != nil {
		return
	}
	// New destinations must not inherit delivery receipts from a previous bot/chat list.
	raw, _ := json.Marshal(c)
	config := model.Hash(string(raw))
	identity := model.Hash(c.Token + "/" + strings.Join(c.ChatIDs, ","))
	if rt.Config != identity {
		rt = telegramRuntime{Config: identity, Receipts: map[string]telegramReceipt{}, Health: map[string]string{}, Retry: map[string]time.Time{}, Errors: map[string]string{}, Status: rt.Status}
	}
	for len(t.logins) > 0 {
		e := <-t.logins
		if len(rt.Pending) < 64 && c.Enabled && c.Login {
			rt.Pending = append(rt.Pending, e)
		}
	}
	if !c.Enabled {
		if len(rt.Pending) == 0 && len(rt.Receipts) == 0 && len(rt.Health) == 0 && len(rt.Retry) == 0 {
			return
		}
		rt.Pending = nil
		rt.Receipts = map[string]telegramReceipt{}
		rt.Health = map[string]string{}
		rt.Retry = map[string]time.Time{}
		rt.Errors = map[string]string{}
		raw, _ = json.Marshal(rt)
		_ = t.db.SetSetting(telegramRuntimeKey, string(raw))
		return
	}
	st, err := t.db.View()
	if err != nil {
		return
	}
	events := telegramEvents(c, st, now, &rt)
	pending := rt.Pending[:0]
	for _, e := range rt.Pending {
		if c.Login && now.Sub(e.Created) < time.Hour {
			pending = append(pending, e)
			events = append(events, e)
		}
	}
	rt.Pending = pending
	active := map[string]bool{}
	for _, e := range events {
		for _, chat := range c.ChatIDs {
			active[chat+"/"+e.Key] = true
		}
	}
	for key := range rt.Receipts {
		if !active[key] {
			delete(rt.Receipts, key)
		}
	}
	loc, _ := time.LoadLocation(c.Timezone)
	// Digest at most eight alerts per message; remaining alerts are picked up next tick.
	// Bound a cycle to one message per chat, and retry each failed chat independently.
	for _, chat := range c.ChatIDs {
		if now.Before(rt.Retry[chat]) {
			continue
		}
		selected := []telegramEvent{}
		text := "VibeXUI · 外部通知\n面板：" + t.publicURL + "\n时间：" + now.In(loc).Format("2006-01-02 15:04:05 MST")
		for _, e := range events {
			receipt := rt.Receipts[chat+"/"+e.Key]
			if receipt.Fingerprint == e.Fingerprint || now.Before(receipt.RetryAt) {
				continue
			}
			if len(selected) >= 8 || len([]rune(text+e.Text)) > 1800 {
				break
			}
			selected = append(selected, e)
			text += "\n\n" + e.Text
		}
		if len(selected) == 0 {
			continue
		}
		// Recheck settings so disabling or changing recipients stops the next send.
		current, e := t.settings()
		if e != nil {
			return
		}
		raw, _ := json.Marshal(current)
		if model.Hash(string(raw)) != config {
			return
		}
		rt.Status.LastAttempt = now
		retry, sendErr := t.send(ctx, c, chat, text)
		if sendErr == nil {
			rt.Status.LastSuccess = now
			delete(rt.Errors, chat)
			delete(rt.Retry, chat)
		} else {
			rt.Errors[chat] = sendErr.Error()
			rt.Retry[chat] = now.Add(retry)
		}
		rt.Status.LastError = telegramErrors(c.ChatIDs, rt.Errors)
		for _, e := range selected {
			key := chat + "/" + e.Key
			if sendErr == nil {
				rt.Receipts[key] = telegramReceipt{Fingerprint: e.Fingerprint}
			} else {
				rt.Receipts[key] = telegramReceipt{RetryAt: now.Add(retry)}
			}
		}
		// Persist after each recipient, so successful recipients are not resent on a later failure.
		raw, _ = json.Marshal(rt)
		if t.db.SetSetting(telegramRuntimeKey, string(raw)) != nil {
			return
		}
	}
	remaining := rt.Pending[:0]
	for _, e := range rt.Pending {
		done := true
		for _, chat := range c.ChatIDs {
			if rt.Receipts[chat+"/"+e.Key].Fingerprint != e.Fingerprint {
				done = false
			}
		}
		if !done {
			remaining = append(remaining, e)
		}
	}
	rt.Pending = remaining
	raw, _ = json.Marshal(rt)
	_ = t.db.SetSetting(telegramRuntimeKey, string(raw))
}

func telegramErrors(chats []string, failures map[string]string) string {
	parts := []string{}
	for _, chat := range chats {
		if e := failures[chat]; e != "" {
			parts = append(parts, chat+": "+e)
		}
	}
	return strings.Join(parts, "；")
}

package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
	"vibexui/internal/model"
)

const testTelegramToken = "123456:abcdefghijklmnopqrstuvwxyz123456789"

type telegramTransport func(*http.Request) (*http.Response, error)

func (f telegramTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func telegramResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}
func saveTestTelegram(t *testing.T, s *Server, c telegramSettings) {
	t.Helper()
	raw, _ := json.Marshal(c)
	if err := s.store.SetSetting(telegramSettingsKey, string(raw)); err != nil {
		t.Fatal(err)
	}
}
func testTelegramSettings() telegramSettings {
	c := defaultTelegramSettings()
	c.Enabled = true
	c.Token = testTelegramToken
	c.ChatIDs = []string{"123", "-456"}
	c.Daily = false
	return c
}
func TestTelegramSettingsAuthorizationAndSecrets(t *testing.T) {
	s, _ := setup(t)
	for _, method := range []string{"GET", "PUT"} {
		if w := call(s, method, "/api/notifications/telegram", nil, nil, ""); w.Code != 401 {
			t.Fatalf("unauthenticated %s: %d", method, w.Code)
		}
	}
	if w := call(s, "POST", "/api/notifications/telegram/test", nil, nil, ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	cookie := login(t, s)
	c := testTelegramSettings()
	req := httptest.NewRequest("PUT", "/api/notifications/telegram", strings.NewReader(`{}`))
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != 403 {
		t.Fatal("missing CSRF accepted")
	}
	if w = call(s, "PUT", "/api/notifications/telegram", c, cookie, ""); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	w = call(s, "GET", "/api/notifications/telegram", nil, cookie, "")
	if w.Code != 200 || strings.Contains(w.Body.String(), testTelegramToken) || !strings.Contains(w.Body.String(), `"hasToken":true`) {
		t.Fatal("token redaction failed", w.Body.String())
	}
	c.Token = ""
	c.ExpiryDays = 7
	if w = call(s, "PUT", "/api/notifications/telegram", c, cookie, ""); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	saved, _ := s.telegram.settings()
	if saved.Token != testTelegramToken || saved.ExpiryDays != 7 {
		t.Fatal("blank token did not preserve secret")
	}
	c.ChatIDs = []string{"oops"}
	if w = call(s, "PUT", "/api/notifications/telegram", c, cookie, ""); w.Code != 400 {
		t.Fatal("invalid chat accepted")
	}
	c = testTelegramSettings()
	c.Timezone = "not/a/timezone"
	if w = call(s, "PUT", "/api/notifications/telegram", c, cookie, ""); w.Code != 400 {
		t.Fatal("invalid timezone accepted")
	}
	c = testTelegramSettings()
	c.ReportTime = "99:99"
	if w = call(s, "PUT", "/api/notifications/telegram", c, cookie, ""); w.Code != 400 {
		t.Fatal("invalid time accepted")
	}
	c = testTelegramSettings()
	c.Enabled = false
	c.Token = ""
	raw, _ := json.Marshal(c)
	var clear map[string]any
	json.Unmarshal(raw, &clear)
	clear["clearToken"] = true
	if w = call(s, "PUT", "/api/notifications/telegram", clear, cookie, ""); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	saved, _ = s.telegram.settings()
	if saved.Token != "" {
		t.Fatal("token not cleared")
	}
}
func TestTelegramDeliveryRetryPersistsPerRecipient(t *testing.T) {
	s, db := setup(t)
	c := testTelegramSettings()
	saveTestTelegram(t, s, c)
	now := time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC)
	db.Update(func(st *model.State) error {
		st.Clients = []model.Client{{ID: "c", Name: "alice", Enabled: true, ExpiresAt: now.Add(time.Hour), UUID: "secret-uuid", Token: "secret-subscription"}}
		return nil
	})
	attempts := map[string]int{}
	transport := telegramTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "api.telegram.org" || r.URL.Scheme != "https" {
			t.Fatal("unexpected destination")
		}
		var payload struct {
			ChatID string `json:"chat_id"`
			Text   string `json:"text"`
		}
		json.NewDecoder(r.Body).Decode(&payload)
		if strings.Contains(payload.Text, "secret-") || len(utf16.Encode([]rune(payload.Text))) > 4096 {
			t.Fatal("unsafe payload")
		}
		attempts[payload.ChatID]++
		if payload.ChatID == "-456" && attempts[payload.ChatID] == 1 {
			return telegramResponse(429, `{"ok":false,"parameters":{"retry_after":120}}`), nil
		}
		return telegramResponse(200, `{"ok":true}`), nil
	})
	s.telegram.client.Transport = transport
	s.telegram.process(context.Background(), now)
	if attempts["123"] != 1 || attempts["-456"] != 1 {
		t.Fatal(attempts)
	}
	rt, _ := s.telegram.runtime()
	if !strings.Contains(rt.Status.LastError, "-456") {
		t.Fatal("partial failure hidden")
	}
	// Simulate a panel restart: receipts must come from SQLite, not process memory.
	next := newTelegram(db, s.publicURL)
	next.client.Transport = transport
	next.process(context.Background(), now.Add(30*time.Second))
	if attempts["123"] != 1 || attempts["-456"] != 1 {
		t.Fatal("retry ignored backoff")
	}
	next.process(context.Background(), now.Add(121*time.Second))
	next.process(context.Background(), now.Add(150*time.Second))
	if attempts["123"] != 1 || attempts["-456"] != 2 {
		t.Fatal("successful destination duplicated", attempts)
	}
	rt, _ = next.runtime()
	if rt.Status.LastError != "" {
		t.Fatal("failure not cleared")
	}
	// Renewal leaves the warning window, then a later expiry must alert again.
	db.Update(func(st *model.State) error { st.Clients[0].ExpiresAt = now.Add(30 * 24 * time.Hour); return nil })
	next.process(context.Background(), now.Add(180*time.Second))
	db.Update(func(st *model.State) error { st.Clients[0].ExpiresAt = now.Add(2 * time.Hour); return nil })
	next.process(context.Background(), now.Add(210*time.Second))
	if attempts["123"] != 2 {
		t.Fatal("renewal did not rearm notification")
	}
}
func TestTelegramServerRecoveryAndReportSchedule(t *testing.T) {
	c := testTelegramSettings()
	c.Daily = true
	c.ReportTime = "09:00"
	now := time.Date(2026, 10, 2, 0, 59, 0, 0, time.UTC)
	st := model.State{Servers: []model.Server{{ID: "s", Name: "Tokyo", LastSeen: now, Running: true, DesiredRunning: true}, {ID: "pending", Name: "Unregistered"}}}
	rt := telegramRuntime{Health: map[string]string{}}
	if events := telegramEvents(c, st, now, &rt); len(events) != 0 {
		t.Fatal("initial healthy/pending server alerted", events)
	}
	st.Servers[0].LastSeen = now.Add(-2 * time.Minute)
	events := telegramEvents(c, st, now, &rt)
	if len(events) != 1 || events[0].Fingerprint != "down" {
		t.Fatal(events)
	}
	st.Servers[0].LastSeen = now
	events = telegramEvents(c, st, now, &rt)
	if len(events) != 1 || events[0].Fingerprint != "up" {
		t.Fatal("missing recovery", events)
	}
	events = telegramEvents(c, st, now.Add(time.Minute), &rt)
	if events[0].Key != "daily" || events[0].Fingerprint != "2026-10-02" {
		t.Fatal("wrong local schedule", events)
	}
}
func TestTelegramQuotaWarningEscalationAndReset(t *testing.T) {
	c := testTelegramSettings()
	now := time.Now()
	rt := telegramRuntime{Health: map[string]string{}}
	st := model.State{Clients: []model.Client{{ID: "c", Name: "a", Enabled: true, QuotaBytes: 2 << 30, Upload: 1500 << 20}}}
	events := telegramEvents(c, st, now, &rt)
	if len(events) != 1 || !strings.Contains(events[0].Text, "剩余流量不足") {
		t.Fatal(events)
	}
	fp := events[0].Fingerprint
	st.Clients[0].Upload = 3 << 30
	events = telegramEvents(c, st, now, &rt)
	if len(events) != 1 || fp == events[0].Fingerprint || !strings.Contains(events[0].Text, "已用尽") {
		t.Fatal("quota escalation failed")
	}
	st.Clients[0].QuotaBaseline.Upload = st.Clients[0].Upload
	if events = telegramEvents(c, st, now, &rt); len(events) != 0 {
		t.Fatal("reset failed")
	}
}
func TestTelegramTransportErrorsNeverExposeToken(t *testing.T) {
	s, _ := setup(t)
	c := testTelegramSettings()
	s.telegram.client.Transport = telegramTransport(func(r *http.Request) (*http.Response, error) { return nil, errors.New(r.URL.String()) })
	_, err := s.telegram.send(context.Background(), c, "123", "test")
	if err == nil || strings.Contains(err.Error(), testTelegramToken) {
		t.Fatal("transport leaked token")
	}
	s.telegram.client.Transport = telegramTransport(func(r *http.Request) (*http.Response, error) {
		return telegramResponse(200, `{"ok":false,"description":"`+testTelegramToken+`"}`), nil
	})
	_, err = s.telegram.send(context.Background(), c, "123", "test")
	if err == nil || strings.Contains(err.Error(), testTelegramToken) {
		t.Fatal("API leaked token")
	}
}
func TestTelegramLoginIsQueuedAndContainsNoPassword(t *testing.T) {
	s, _ := setup(t)
	c := testTelegramSettings()
	saveTestTelegram(t, s, c)
	w := call(s, "POST", "/api/login", map[string]string{"username": "wrong", "password": "very-secret-password"}, nil, "")
	if w.Code != 401 || len(s.telegram.logins) != 1 {
		t.Fatal("missing failed login event")
	}
	event := <-s.telegram.logins
	if strings.Contains(event.Text, "very-secret-password") || !strings.Contains(event.Text, "失败") {
		t.Fatal(event.Text)
	}
	login(t, s)
	if len(s.telegram.logins) != 1 {
		t.Fatal("missing successful login")
	}
}

package server

import (
	"golang.org/x/crypto/bcrypt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"vibexui/internal/model"
)

func accountChange(username, password, newPassword string) map[string]string {
	return map[string]string{"username": username, "currentPassword": password, "newPassword": newPassword}
}
func TestAccountUpdateInvalidatesSessionsAndPersists(t *testing.T) {
	s, db := setup(t)
	first, second := login(t, s), login(t, s)
	w := call(s, "PUT", "/api/settings/account", accountChange("new-admin", "test-password-123", "new-password-456"), first, "")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if cookies := w.Result().Cookies(); len(cookies) != 1 || cookies[0].MaxAge != -1 {
		t.Fatal("session cookie was not expired")
	}
	for _, c := range []*http.Cookie{first, second} {
		if w = call(s, "GET", "/api/me", nil, c, ""); w.Code != 401 {
			t.Fatal("old session survived")
		}
	}
	if w = call(s, "POST", "/api/login", map[string]string{"username": "admin", "password": "test-password-123"}, nil, ""); w.Code != 401 {
		t.Fatal("old credentials accepted")
	}
	w = call(s, "POST", "/api/login", map[string]string{"username": "new-admin", "password": "new-password-456"}, nil, "")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	username, _ := db.Setting("username")
	hash, _ := db.Setting("password")
	if username != "new-admin" || hash == "new-password-456" || bcrypt.CompareHashAndPassword([]byte(hash), []byte("new-password-456")) != nil {
		t.Fatal("credentials not persisted safely")
	}
	// Startup environment is only for initialization and must not restore old credentials.
	restarted, err := New(db, Options{Username: "admin", Password: "test-password-123", PublicURL: "http://localhost:8080"})
	if err != nil {
		t.Fatal(err)
	}
	w = call(restarted, "POST", "/api/login", map[string]string{"username": "new-admin", "password": "new-password-456"}, nil, "")
	if w.Code != 200 {
		t.Fatal("new credentials lost after restart")
	}
}
func TestAccountUsernameOnlyAndValidation(t *testing.T) {
	s, _ := setup(t)
	cookie := login(t, s)
	bad := []map[string]string{
		accountChange("new", "wrong-password", "new-password-456"),
		accountChange("bad name", "test-password-123", ""),
		accountChange("", "test-password-123", ""),
		accountChange("new", "test-password-123", "short"),
		accountChange("new", "test-password-123", strings.Repeat("中", 25)),
	}
	for _, in := range bad {
		w := call(s, "PUT", "/api/settings/account", in, cookie, "")
		if w.Code != 400 && w.Code != 403 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	w := call(s, "PUT", "/api/settings/account", accountChange("renamed", "test-password-123", ""), cookie, "")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	w = call(s, "POST", "/api/login", map[string]string{"username": "renamed", "password": "test-password-123"}, nil, "")
	if w.Code != 200 {
		t.Fatal("username-only change altered password")
	}
}
func TestAccountUpdateRequiresSessionCSRFAndRateLimits(t *testing.T) {
	s, _ := setup(t)
	in := accountChange("new", "wrong-password", "")
	if w := call(s, "PUT", "/api/settings/account", in, nil, ""); w.Code != 401 {
		t.Fatal("unauthenticated change accepted")
	}
	cookie := login(t, s)
	r := httptest.NewRequest("PUT", "/api/settings/account", strings.NewReader(`{}`))
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("missing CSRF accepted")
	}
	r = httptest.NewRequest("PUT", "/api/settings/account", strings.NewReader(`{}`))
	r.AddCookie(cookie)
	r.Header.Set("X-VibeXUI", "1")
	r.Header.Set("Origin", "https://evil.example")
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("cross-origin accepted")
	}
	for i := 0; i < 5; i++ {
		w = call(s, "PUT", "/api/settings/account", in, cookie, "")
		if w.Code != 403 {
			t.Fatal(w.Code)
		}
	}
	w = call(s, "PUT", "/api/settings/account", in, cookie, "")
	if w.Code != 429 {
		t.Fatal("no password attempt throttling")
	}
}
func TestAccountConcurrentChangesHaveOneWinner(t *testing.T) {
	s, _ := setup(t)
	cookie := login(t, s)
	var wg sync.WaitGroup
	results := make(chan int, 2)
	for _, name := range []string{"one", "two"} {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			results <- call(s, "PUT", "/api/settings/account", accountChange(name, "test-password-123", "new-password-456"), cookie, "").Code
		}(name)
	}
	wg.Wait()
	close(results)
	wins := 0
	for code := range results {
		if code == 200 {
			wins++
		} else if code != 401 && code != 403 {
			t.Fatal(code)
		}
	}
	if wins != 1 {
		t.Fatalf("expected one change, got %d", wins)
	}
}
func TestConcurrentLoginCannotReviveOldCredentials(t *testing.T) {
	s, _ := setup(t)
	cookie := login(t, s)
	var wg sync.WaitGroup
	results := make(chan *http.Cookie, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := call(s, "POST", "/api/login", map[string]string{"username": "admin", "password": "test-password-123"}, nil, "")
			if w.Code == 200 {
				results <- w.Result().Cookies()[0]
			}
		}()
	}
	w := call(s, "PUT", "/api/settings/account", accountChange("new", "test-password-123", "new-password-456"), cookie, "")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	wg.Wait()
	close(results)
	for c := range results {
		if w = call(s, "GET", "/api/me", nil, c, ""); w.Code != 401 {
			t.Fatal("in-flight old login survived account change")
		}
	}
}
func TestAccountStorageFailureKeepsOldCredentials(t *testing.T) {
	s, db := setup(t)
	cookie := login(t, s)
	db.Close()
	w := call(s, "PUT", "/api/settings/account", accountChange("new", "test-password-123", "new-password-456"), cookie, "")
	if w.Code != 500 {
		t.Fatal(w.Code)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.username != "admin" || bcrypt.CompareHashAndPassword([]byte(s.password), []byte("test-password-123")) != nil {
		t.Fatal("memory credentials changed after storage error")
	}
	if expiry := s.sessions[model.Hash(cookie.Value)]; !expiry.After(time.Now()) {
		t.Fatal("session cleared on failed save")
	}
}

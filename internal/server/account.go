package server

import (
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"
	"vibexui/internal/model"
)

func (s *Server) accountInfo(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	username := s.username
	s.mu.Unlock()
	respond(w, 200, map[string]any{"username": username, "publicURL": s.publicURL})
}

func (s *Server) updateAccount(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Username        string `json:"username"`
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
	}
	if !decode(w, r, &in) {
		return
	}
	in.Username = strings.TrimSpace(in.Username)
	if !utf8.ValidString(in.Username) || len(in.Username) == 0 || len(in.Username) > 64 || strings.IndexFunc(in.Username, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		fail(w, 400, "管理员账号须为 1–64 字节，不能包含空白或控制字符")
		return
	}
	if in.NewPassword != "" && (len(in.NewPassword) < 12 || len(in.NewPassword) > 72) {
		fail(w, 400, "新密码须为 12–72 字节")
		return
	}
	if in.CurrentPassword == "" || len(in.CurrentPassword) > 72 {
		fail(w, 400, "请填写当前密码（最多 72 字节）")
		return
	}
	now := time.Now()
	s.mu.Lock()
	recent := s.credentialAttempts[:0]
	for _, at := range s.credentialAttempts {
		if now.Sub(at) < time.Minute {
			recent = append(recent, at)
		}
	}
	s.credentialAttempts = recent
	if len(recent) >= 5 {
		s.mu.Unlock()
		fail(w, 429, "当前密码验证过于频繁，请一分钟后重试")
		return
	}
	s.credentialAttempts = append(s.credentialAttempts, now)
	oldUsername, oldHash, version := s.username, s.password, s.credentialVersion
	s.mu.Unlock()
	if bcrypt.CompareHashAndPassword([]byte(oldHash), []byte(in.CurrentPassword)) != nil {
		fail(w, 403, "当前密码不正确")
		return
	}
	if oldUsername == in.Username && in.NewPassword == "" {
		fail(w, 400, "账号和密码均未修改")
		return
	}
	newHash := oldHash
	if in.NewPassword != "" {
		hash, err := bcrypt.GenerateFromPassword([]byte(in.NewPassword), bcrypt.DefaultCost)
		if err != nil {
			fail(w, 500, "无法更新密码，请稍后重试")
			return
		}
		newHash = string(hash)
	}
	// Recheck the session and credential generation after expensive password work.
	// An in-flight login using old credentials must not recreate a session afterwards.
	cookie, _ := r.Cookie("vibexui_session")
	s.mu.Lock()
	defer s.mu.Unlock()
	if cookie == nil {
		fail(w, 401, "请重新登录")
		return
	}
	expiry, ok := s.sessions[model.Hash(cookie.Value)]
	if !ok || time.Now().After(expiry) || s.credentialVersion != version {
		fail(w, 401, "登录已过期，请重新登录")
		return
	}
	if err := s.store.SetCredentials(in.Username, newHash); err != nil {
		fail(w, 500, "保存账号失败，原账号和密码保持不变")
		return
	}
	s.username, s.password = in.Username, newHash
	s.credentialVersion++
	s.sessions = map[string]time.Time{}
	s.credentialAttempts = nil
	http.SetCookie(w, &http.Cookie{Name: "vibexui_session", Value: "", Path: "/", HttpOnly: true, Secure: s.secure, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	respond(w, 200, map[string]bool{"ok": true, "reauthenticate": true})
}

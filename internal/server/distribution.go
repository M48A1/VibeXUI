package server

import (
	"crypto/subtle"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"vibexui/internal/bootstrap"
	"vibexui/internal/model"
)

func (s *Server) installer(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write(bootstrap.Script)
}
func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }
func (s *Server) registrationResult(id, token string) map[string]string {
	result := map[string]string{"id": id, "token": token, "panel": s.publicURL}
	if s.secure {
		result["installCommand"] = "sudo env VIBEXUI_PANEL=" + shellQuote(s.publicURL) + " VIBEXUI_SERVER_ID=" + shellQuote(id) + " VIBEXUI_REGISTRATION_TOKEN=" + shellQuote(token) + " bash -c " + shellQuote(`set -e; installer=$(mktemp); trap 'rm -f "$installer"' EXIT; curl -q -fsSL --proto '=https' --proto-redir '=https' "$VIBEXUI_PANEL/install/agent.sh" -o "$installer"; bash "$installer"`)
	}
	return result
}
func (s *Server) downloadAgent(w http.ResponseWriter, r *http.Request) {
	st, ok := s.view(w)
	if !ok {
		return
	}
	v := serverAt(&st, r.Header.Get("X-VibeXUI-Server"))
	token := bearer(r)
	if token == "" || v == nil || v.RegistrationHash == "" || time.Now().After(v.RegistrationExpires) || subtle.ConstantTimeCompare([]byte(v.RegistrationHash), []byte(model.Hash(token))) != 1 {
		fail(w, 401, "注册令牌无效或已过期")
		return
	}
	arch := r.PathValue("arch")
	if arch != "amd64" && arch != "arm64" {
		fail(w, 400, "不支持该服务器架构")
		return
	}
	if s.downloads == "" {
		fail(w, 503, "尚未配置 Agent 发行目录")
		return
	}
	data, err := os.ReadFile(filepath.Join(s.downloads, "linux-"+arch, "vibexui-agent"))
	if err != nil || len(data) == 0 {
		fail(w, 503, "主面板缺少该架构的 Agent 发行文件，请运行 make linux 并配置 downloads-dir")
		return
	}
	if strings.HasSuffix(r.URL.Path, "/sha256") {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintln(w, model.Hash(string(data)))
		return
	}
	http.NewResponseController(w).SetWriteDeadline(time.Now().Add(5 * time.Minute))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="vibexui-agent"`)
	w.Write(data)
}

package server

import (
	"crypto/sha256"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"vibexui/internal/model"
)

func TestAgentDistribution(t *testing.T) {
	app, db := setup(t)
	app.secure = true
	app.publicURL = "https://panel.example.com"
	app.downloads = t.TempDir()
	path := filepath.Join(app.downloads, "linux-amd64")
	if err := os.MkdirAll(path, 0755); err != nil {
		t.Fatal(err)
	}
	data := []byte("agent-release-fixture")
	if err := os.WriteFile(filepath.Join(path, "vibexui-agent"), data, 0600); err != nil {
		t.Fatal(err)
	}
	token := model.Secret()
	id := model.ID()
	if err := db.Update(func(st *model.State) error {
		st.Servers = append(st.Servers, model.Server{ID: id, RegistrationHash: model.Hash(token), RegistrationExpires: time.Now().Add(time.Minute)})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	get := func(arch, token, serverID string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/api/agent/download/"+arch, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("X-VibeXUI-Server", serverID)
		w := httptest.NewRecorder()
		app.ServeHTTP(w, r)
		return w
	}
	if get("amd64", "", id).Code != 401 || get("amd64", token, "wrong").Code != 401 {
		t.Fatal("unauthorized release download")
	}
	if r := get("amd64", token, id); r.Code != 200 || r.Body.String() != string(data) {
		t.Fatal("release download failed", r.Code)
	}
	if r := get("amd64/sha256", token, id); r.Code != 200 || strings.TrimSpace(r.Body.String()) != fmt.Sprintf("%x", sha256.Sum256(data)) {
		t.Fatal("wrong release checksum")
	}
	if get("arm64", token, id).Code != 503 || get("mips", token, id).Code != 400 {
		t.Fatal("missing or unsupported release accepted")
	}
	if r := call(app, "GET", "/install/agent.sh", nil, nil, ""); r.Code != 200 || !strings.Contains(r.Body.String(), "verify_sha256") {
		t.Fatal("bootstrap script not served")
	}
	readJSON[map[string]string](t, call(app, "POST", "/api/agent/register", map[string]string{"id": id}, nil, token))
	if get("amd64", token, id).Code != 401 {
		t.Fatal("consumed registration token still downloads releases")
	}
}

func TestInstallCommandCarriesParametersAndPropagatesFailure(t *testing.T) {
	app, _ := setup(t)
	app.secure = true
	app.publicURL = "https://panel.example.com"
	dir := t.TempDir()
	mock := filepath.Join(dir, "curl")
	writeMock := func(content string) {
		t.Helper()
		if err := os.WriteFile(mock, []byte(content), 0700); err != nil {
			t.Fatal(err)
		}
	}
	writeMock(`#!/bin/bash
if [[ ${FAIL_DOWNLOAD:-0} == 1 ]]; then exit 22; fi
while [[ $# -gt 0 ]]; do
  if [[ $1 == -o ]]; then output=$2; shift; fi
  shift
done
printf '%s\n' 'printf "%s|%s|%s" "$VIBEXUI_PANEL" "$VIBEXUI_SERVER_ID" "$VIBEXUI_REGISTRATION_TOKEN"' > "$output"
`)
	command := strings.TrimPrefix(app.registrationResult("server-id", "registration-token")["installCommand"], "sudo ")
	run := func(fail bool) (string, error) {
		cmd := exec.Command("bash", "-c", command)
		cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"))
		if fail {
			cmd.Env = append(cmd.Env, "FAIL_DOWNLOAD=1")
		}
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	out, err := run(false)
	if err != nil || out != "https://panel.example.com|server-id|registration-token" {
		t.Fatal("command lost bootstrap parameters", err)
	}
	if _, err = run(true); err == nil {
		t.Fatal("failed script download returned success")
	}
	app.secure = false
	if app.registrationResult("id", "token")["installCommand"] != "" {
		t.Fatal("plaintext bootstrap generated")
	}
}

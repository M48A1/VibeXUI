package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
	"vibexui/internal/model"
)

func setupKernelServer(t *testing.T) (*Server, *http.Cookie) {
	s, db := setup(t)
	cookie := login(t, s)
	db.Update(func(st *model.State) error {
		st.Servers = []model.Server{{ID: "one", Name: "Tokyo", LastSeen: time.Now(), Version: 1, TokenHash: model.Hash("agent-token"), Kernel: model.KernelReport{Supported: true, Arch: "amd64"}}}
		return nil
	})
	s.kernels.Client.Transport = telegramTransport(func(r *http.Request) (*http.Response, error) {
		body := `{"tag_name":"v26.3.27","assets":[{"name":"Xray-linux-64.zip","digest":"sha256:` + strings.Repeat("a", 64) + `"}]}`
		if strings.Contains(r.URL.RawQuery, "per_page") {
			body = `[{"tag_name":"v26.3.27"}]`
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})
	return s, cookie
}
func TestKernelJobAuthorizationCapabilitiesAndLifecycle(t *testing.T) {
	s, cookie := setupKernelServer(t)
	job := map[string]string{"action": "install", "version": "v26.3.27"}
	if w := call(s, "GET", "/api/xray/versions", nil, nil, ""); w.Code != 401 {
		t.Fatal("public versions endpoint")
	}
	if w := call(s, "POST", "/api/servers/one/kernel", job, nil, ""); w.Code != 401 {
		t.Fatal("unauthorized kernel update")
	}
	w := call(s, "POST", "/api/servers/one/kernel", job, cookie, "")
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = call(s, "POST", "/api/servers/one/kernel", job, cookie, ""); w.Code != 409 {
		t.Fatal("overlapping update accepted")
	}
	st, _ := s.store.View()
	id := st.Servers[0].KernelTask.ID
	report := struct {
		ID string `json:"id"`
		model.Report
	}{ID: "one", Report: model.Report{Kernel: model.KernelReport{ID: id, State: "succeeded", Supported: true, Arch: "amd64", PreviousVersion: "Xray 26.1.1"}}}
	w = call(s, "POST", "/api/agent/poll", report, nil, "agent-token")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	task := readJSON[model.Task](t, w)
	if task.Kernel == nil || task.Kernel.ID != id {
		t.Fatal("kernel task not delivered")
	}
	w = call(s, "POST", "/api/servers/one/kernel", map[string]string{"action": "rollback"}, cookie, "")
	if w.Code != 202 {
		t.Fatal("completed job blocked rollback", w.Body.String())
	}
	s.store.Update(func(st *model.State) error {
		st.Servers[0].KernelTask = nil
		st.Servers[0].Kernel.Supported = false
		return nil
	})
	if w = call(s, "POST", "/api/servers/one/kernel", job, cookie, ""); w.Code != 409 {
		t.Fatal("legacy agent accepted")
	}
	s.store.Update(func(st *model.State) error {
		st.Servers[0].Kernel.Supported = true
		st.Servers[0].LastSeen = time.Now().Add(-time.Hour)
		return nil
	})
	if w = call(s, "POST", "/api/servers/one/kernel", job, cookie, ""); w.Code != 409 {
		t.Fatal("offline agent accepted")
	}
	if w = call(s, "POST", "/api/servers/one/kernel", map[string]string{"action": "install", "version": "../../tmp"}, cookie, ""); w.Code != 400 {
		t.Fatal("invalid tag accepted")
	}
}
func TestKernelConcurrentRequestsOnlyQueueOneJob(t *testing.T) {
	s, cookie := setupKernelServer(t)
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes <- call(s, "POST", "/api/servers/one/kernel", map[string]string{"action": "install", "version": "v26.3.27"}, cookie, "").Code
		}()
	}
	wg.Wait()
	close(codes)
	count := 0
	for code := range codes {
		if code == 202 {
			count++
		} else if code != 409 {
			t.Fatal(code)
		}
	}
	if count != 1 {
		t.Fatal("duplicate jobs")
	}
}
func TestKernelVersionServiceFailsClosed(t *testing.T) {
	s, cookie := setupKernelServer(t)
	if w := call(s, "GET", "/api/xray/versions", nil, cookie, ""); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	s.kernels.Client.Transport = telegramTransport(func(*http.Request) (*http.Response, error) { return nil, fmt.Errorf("unavailable") })
	w := call(s, "POST", "/api/servers/one/kernel", map[string]string{"action": "install", "version": "v26.3.27"}, cookie, "")
	if w.Code != 502 {
		t.Fatal(w.Code)
	}
	st, _ := s.store.View()
	if st.Servers[0].KernelTask != nil {
		t.Fatal("unverified release queued")
	}
	_, _ = s.kernels.List(context.Background())
}

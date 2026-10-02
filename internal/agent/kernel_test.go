package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"vibexui/internal/model"
)

func candidateKernel(t *testing.T, dir, version string, invalid, crash bool) string {
	t.Helper()
	d, err := os.MkdirTemp(dir, "candidate-")
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(d, "xray")
	script := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = version ]; then echo 'Xray %s'; exit 0; fi\nif [ \"$1\" = api ]; then echo '{}'; exit 0; fi\nif [ \"$2\" = -test ]; then exit %d; fi\n%s\ntrap 'exit 0' INT TERM\nwhile true; do sleep 1; done\n", version, map[bool]int{true: 1, false: 0}[invalid], map[bool]string{true: "exit 1", false: ""}[crash])
	if err = os.WriteFile(p, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return p
}
func prepareKernelAgent(t *testing.T) *Agent {
	t.Helper()
	a := fake(t)
	a.version = "Xray old"
	if err := os.WriteFile(filepath.Join(a.opts.Directory, "config.json"), []byte(`{"inbounds":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	return a
}
func TestKernelSwitchAndRollbackPersistWithoutReplacingSystemBinary(t *testing.T) {
	a := prepareKernelAgent(t)
	ctx := context.Background()
	original := a.opts.Xray
	originalBytes, _ := os.ReadFile(original)
	a.state.DesiredRunning = true
	if err := a.start(ctx); err != nil {
		t.Fatal(err)
	}
	target := candidateKernel(t, a.opts.Directory, "26.3.27", false, false)
	a.state.Kernel = model.KernelReport{ID: "one", Target: "v26.3.27"}
	if err := a.switchKernel(ctx, target, "v26.3.27"); err != nil {
		t.Fatal(err)
	}
	if !a.running() || a.opts.Xray != target || a.state.PreviousKernelPath != original || a.state.Kernel.State != "succeeded" {
		t.Fatal("switch not applied")
	}
	data, _ := os.ReadFile(original)
	if string(data) != string(originalBytes) {
		t.Fatal("system binary overwritten")
	}
	a.stop()
	restarted, err := New(Options{Panel: a.opts.Panel, ID: a.opts.ID, Directory: a.opts.Directory, Xray: original})
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.stop()
	if restarted.opts.Xray != target {
		t.Fatal("managed kernel not persisted")
	}
	restarted.version = "Xray 26.3.27"
	restarted.handleKernel(ctx, &model.KernelTask{ID: "two", Action: "rollback"})
	if restarted.opts.Xray != original || restarted.state.Kernel.State != "succeeded" || !restarted.running() {
		t.Fatal("rollback failed", restarted.state.Kernel)
	}
}
func TestKernelValidationAndStartupFailuresPreserveOldProcess(t *testing.T) {
	a := prepareKernelAgent(t)
	ctx := context.Background()
	original := a.opts.Xray
	a.state.DesiredRunning = true
	a.start(ctx)
	for _, kind := range []string{"config", "version", "crash"} {
		p := candidateKernel(t, a.opts.Directory, "26.3.27", kind == "config", kind == "crash")
		tag := "v26.3.27"
		if kind == "version" {
			tag = "v26.1.1"
		}
		if err := a.switchKernel(ctx, p, tag); err == nil {
			t.Fatal("expected failure", kind)
		}
		if a.opts.Xray != original || !a.running() {
			t.Fatal("old service not preserved", kind)
		}
	}
}
func TestKernelDownloadDoesNotBlockAndTaskIsNotRepeated(t *testing.T) {
	a := prepareKernelAgent(t)
	release := make(chan struct{})
	calls := 0
	p := candidateKernel(t, a.opts.Directory, "26.3.27", false, false)
	a.downloadKernel = func(ctx context.Context, version, arch, dir string) (string, error) { <-release; return p, nil }
	job := &model.KernelTask{ID: "test-job", Action: "install", Version: "v26.3.27"}
	start := time.Now()
	a.handleKernel(context.Background(), job)
	calls++
	if time.Since(start) > time.Second || a.state.Kernel.State != "downloading" {
		t.Fatal("download blocked Agent loop")
	}
	a.handleKernel(context.Background(), job)
	close(release)
	for i := 0; i < 100 && a.state.Kernel.State == "downloading"; i++ {
		a.handleKernel(context.Background(), job)
		time.Sleep(time.Millisecond)
	}
	if a.state.Kernel.State != "succeeded" {
		t.Fatal(a.state.Kernel)
	}
	a.downloadKernel = func(context.Context, string, string, string) (string, error) {
		calls++
		return "", fmt.Errorf("unexpected duplicate")
	}
	a.handleKernel(context.Background(), job)
	if calls != 1 {
		t.Fatal("job repeated")
	}
}
func TestKernelInterruptedDownloadAndStoppedService(t *testing.T) {
	a := prepareKernelAgent(t)
	a.state.Kernel = model.KernelReport{ID: "interrupted", State: "downloading"}
	a.save()
	restarted, err := New(Options{Panel: a.opts.Panel, ID: a.opts.ID, Directory: a.opts.Directory, Xray: a.opts.Xray})
	if err != nil {
		t.Fatal(err)
	}
	if restarted.state.Kernel.State != "failed" || !strings.Contains(restarted.state.Kernel.Error, "中断") {
		t.Fatal("interrupted task was not finalized")
	}
	p := candidateKernel(t, a.opts.Directory, "26.3.27", false, false)
	if err = restarted.switchKernel(context.Background(), p, "v26.3.27"); err != nil {
		t.Fatal(err)
	}
	if restarted.running() {
		t.Fatal("stopped service was started by upgrade")
	}
}

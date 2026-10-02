package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"vibexui/internal/kernel"
	"vibexui/internal/model"
)

type kernelResult struct {
	path string
	err  error
}

func (a *Agent) kernelReport() model.KernelReport {
	r := a.state.Kernel
	r.Supported = runtime.GOOS == "linux" && kernel.AssetName(runtime.GOARCH) != ""
	r.Arch = runtime.GOARCH
	r.PreviousVersion = a.state.PreviousKernelVersion
	return r
}
func (a *Agent) kernelFailed(err error) {
	a.state.Kernel.State = "failed"
	a.state.Kernel.Error = limit(err.Error(), 900)
	a.state.Kernel.UpdatedAt = time.Now()
	_ = a.save()
}
func kernelVersion(ctx context.Context, path string) (string, error) {
	c, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(c, path, "version").Output()
	if err != nil {
		return "", fmt.Errorf("无法运行目标内核")
	}
	line := strings.TrimSpace(strings.Split(string(out), "\n")[0])
	if !strings.HasPrefix(line, "Xray ") || len(line) > 180 {
		return "", fmt.Errorf("下载文件不是有效的 Xray 内核")
	}
	return line, nil
}
func kernelConfigValid(ctx context.Context, path, config string) error {
	c, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	// Do not echo stderr: configuration errors can contain private keys.
	if err := exec.CommandContext(c, path, "run", "-test", "-config", config).Run(); err != nil {
		return fmt.Errorf("目标内核不兼容当前配置，已保留原内核")
	}
	return nil
}
func (a *Agent) handleKernel(ctx context.Context, job *model.KernelTask) {
	if a.kernelDownload != nil {
		select {
		case result := <-a.kernelDownload:
			a.kernelDownload = nil
			if a.kernelCancel != nil {
				a.kernelCancel()
				a.kernelCancel = nil
			}
			if result.err != nil {
				a.kernelFailed(result.err)
				return
			}
			if err := a.switchKernel(ctx, result.path, a.state.Kernel.Target); err != nil {
				os.RemoveAll(filepath.Dir(result.path))
				a.kernelFailed(err)
			}
		default:
		}
		return
	}
	if job == nil || job.ID == a.state.Kernel.ID {
		return
	}
	a.state.Kernel = model.KernelReport{ID: job.ID, Target: job.Version, State: "downloading", UpdatedAt: time.Now()}
	if len(job.ID) > 64 || job.ID == "" || !a.kernelReport().Supported {
		a.kernelFailed(fmt.Errorf("不支持的内核任务或平台"))
		return
	}
	if job.Action == "rollback" {
		a.state.Kernel.State = "switching"
		a.state.Kernel.Target = a.state.PreviousKernelVersion
		if err := a.save(); err != nil {
			a.kernelFailed(err)
			return
		}
		if a.state.PreviousKernelPath == "" {
			a.kernelFailed(fmt.Errorf("没有可回滚的上一内核"))
			return
		}
		if err := a.switchKernel(ctx, a.state.PreviousKernelPath, ""); err != nil {
			a.kernelFailed(err)
		}
		return
	}
	if job.Action != "install" || !kernel.ValidVersion(job.Version) {
		a.kernelFailed(fmt.Errorf("版本或内核操作无效"))
		return
	}
	if err := a.save(); err != nil {
		a.kernelFailed(err)
		return
	}
	workCtx, cancel := context.WithCancel(ctx)
	a.kernelCancel = cancel
	result := make(chan kernelResult, 1)
	a.kernelDownload = result
	download := a.downloadKernel
	version, dir := job.Version, a.opts.Directory
	go func() {
		path, err := download(workCtx, version, runtime.GOARCH, dir)
		result <- kernelResult{path, err}
	}()
}

// switchKernel runs only on the Agent loop, so configuration, process and traffic
// updates cannot race. Its checkpoint remains pointed at the old binary until
// the new binary has validated and started successfully.
func (a *Agent) switchKernel(ctx context.Context, path, tag string) error {
	version, err := kernelVersion(ctx, path)
	if err != nil {
		return err
	}
	fields := strings.Fields(version)
	if tag != "" && (len(fields) < 2 || "v"+fields[1] != tag) {
		return fmt.Errorf("下载内核版本与所选版本不一致")
	}
	if err = kernelConfigValid(ctx, path, filepath.Join(a.opts.Directory, "config.json")); err != nil {
		return err
	}
	a.stats(ctx)
	oldPath, oldVersion, oldState := a.opts.Xray, a.version, a.state
	if oldVersion == "" {
		oldVersion = "原内核（版本未知）"
	}
	wasRunning := a.running()
	a.state.Kernel.State = "switching"
	a.state.Kernel.UpdatedAt = time.Now()
	if err = a.save(); err != nil {
		return err
	}
	a.stop()
	a.opts.Xray = path
	if a.state.DesiredRunning {
		err = a.start(ctx)
	}
	if err == nil {
		a.version = version
		a.state.KernelPath = path
		a.state.PreviousKernelPath = oldPath
		a.state.PreviousKernelVersion = oldVersion
		a.state.Kernel.State = "succeeded"
		a.state.Kernel.Error = ""
		a.state.Kernel.UpdatedAt = time.Now()
		err = a.save()
	}
	if err != nil {
		a.stop()
		a.opts.Xray = oldPath
		a.version = oldVersion
		a.state = oldState
		recoveryCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if wasRunning {
			if recoveryErr := a.start(recoveryCtx); recoveryErr != nil {
				return fmt.Errorf("切换失败且旧内核未能启动，请查看 Agent 日志")
			}
		}
		return fmt.Errorf("切换失败，已恢复原内核")
	}
	a.cleanupKernels()
	return nil
}

// Retain only the active and previous managed binaries. System binaries live
// outside this directory and are never deleted or overwritten.
func (a *Agent) cleanupKernels() {
	root := filepath.Join(a.opts.Directory, "kernels")
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "kernel-") {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		if dir == filepath.Dir(a.opts.Xray) || dir == filepath.Dir(a.state.PreviousKernelPath) {
			continue
		}
		_ = os.RemoveAll(dir)
	}
}

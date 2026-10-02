package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Recover only a previously applied, locally enabled configuration. New task
// retries and local process restarts intentionally have separate retry timers.
func (a *Agent) recoverLocal(ctx context.Context) error {
	if ctx.Err() != nil || !a.state.DesiredRunning || a.state.Version <= 0 || a.running() {
		return nil
	}
	if time.Now().Before(a.recoveryAt) {
		return nil
	}
	path := filepath.Join(a.opts.Directory, "config.json")
	raw, err := os.ReadFile(path)
	if err == nil && !json.Valid(raw) {
		err = fmt.Errorf("本地配置 JSON 无效")
	}
	if err == nil {
		err = kernelConfigValid(ctx, a.opts.Xray, path)
	}
	if err == nil {
		err = a.start(ctx)
	}
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if a.recoveryDelay == 0 {
			a.recoveryDelay = time.Second
		} else {
			a.recoveryDelay *= 2
		}
		if a.recoveryDelay > time.Minute {
			a.recoveryDelay = time.Minute
		}
		a.recoveryAt = time.Now().Add(a.recoveryDelay)
		a.lastError = "本地 Xray 恢复失败，请检查已保存配置和运行环境"
		return fmt.Errorf("%s", a.lastError)
	}
	a.recoveryAt = time.Time{}
	a.recoveryDelay = 0
	// Retain a pending configuration failure while the old configuration runs.
	if a.failedVersion == 0 {
		a.lastError = ""
	}
	return nil
}

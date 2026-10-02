package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"time"
)

type collectedStats struct {
	generation    uint64
	raw           []byte
	err           error
	ips           map[string][]string
	ipError       string
	statsAt, ipAt time.Time
}

func sameConfig(a, b []byte) bool {
	var x, y any
	leftDecoder := json.NewDecoder(bytes.NewReader(a))
	leftDecoder.UseNumber()
	rightDecoder := json.NewDecoder(bytes.NewReader(b))
	rightDecoder.UseNumber()
	if leftDecoder.Decode(&x) != nil || rightDecoder.Decode(&y) != nil {
		return false
	}
	left, _ := json.Marshal(x)
	right, _ := json.Marshal(y)
	return bytes.Equal(left, right)
}
func (a *Agent) noteApplyFailure(version int64) {
	if a.failedVersion != version {
		a.retryDelay = 10 * time.Second
	} else {
		a.retryDelay *= 2
	}
	if a.retryDelay > 5*time.Minute {
		a.retryDelay = 5 * time.Minute
	}
	a.failedVersion = version
	a.retryAt = time.Now().Add(a.retryDelay)
}

// Only the main loop mutates Agent state. The worker owns an immutable command
// snapshot and returns results through a buffered channel, even after cancellation.
func (a *Agent) beginCollection(ctx context.Context) {
	if a.collection != nil {
		return
	}
	running := a.running()
	if !running {
		return
	}
	worker := &Agent{opts: a.opts}
	bindings := append([]string(nil), a.state.IPBindings...)
	generation := a.generation
	ch := make(chan collectedStats, 1)
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	a.collectCancel = cancel
	a.collection = ch
	go func() {
		defer cancel()
		r := collectedStats{generation: generation}
		r.raw, r.err = worker.command(ctx, "api", "statsquery", "-s=127.0.0.1:10085", "-pattern=", "-reset=false")
		r.statsAt = time.Now()
		r.ips, r.ipError = worker.collectOnlineIPs(ctx, bindings, running)
		r.ipAt = time.Now()
		ch <- r
	}()
}
func (a *Agent) invalidateCollection() {
	a.generation++
	if a.collectCancel != nil {
		a.collectCancel()
		a.collectCancel = nil
	}
	a.collection = nil
}
func (a *Agent) consumeCollection() (map[string][]string, string) {
	if !a.running() {
		a.statsError = ""
		a.ipAt = time.Now()
		return map[string][]string{}, ""
	}
	if a.collection != nil {
		select {
		case r := <-a.collection:
			a.collection = nil
			a.collectCancel = nil
			if r.generation == a.generation {
				if r.err != nil {
					a.statsError = r.err.Error()
				} else {
					a.ingestStats(r.raw)
					if a.statsError == "" {
						a.statsAt = r.statsAt
					}
				}
				a.ipAt = r.ipAt
				return r.ips, r.ipError
			}
		default:
		}
	}
	a.statsError = "统计采集中，当前上报保留上次累计值"
	return nil, "在线 IP 采集中，保留上次采样"
}

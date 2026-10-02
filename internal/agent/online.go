package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"sort"
	"strings"
	"time"
)

// Online IPs are sampled for assigned client/inbound pairs. RPC failures
// return nil (unknown), never an empty successful sample.
func (a *Agent) onlineIPs(ctx context.Context) (map[string][]string, string) {
	return a.collectOnlineIPs(ctx, append([]string(nil), a.state.IPBindings...), a.running())
}
func (a *Agent) collectOnlineIPs(ctx context.Context, bindings []string, running bool) (map[string][]string, string) {
	sample := map[string][]string{}
	if len(bindings) == 0 || !running {
		return sample, ""
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	raw, err := a.command(ctx, "api", "statsgetallonlineusers", "-s=127.0.0.1:10085", "-t=2")
	if err != nil {
		return nil, err.Error()
	}
	var users struct {
		Users []string `json:"users"`
	}
	if err = json.Unmarshal(raw, &users); err != nil {
		return nil, "无法解析在线用户统计"
	}
	active := map[string]bool{}
	for _, name := range users.Users {
		if strings.HasPrefix(name, "user>>>") && strings.HasSuffix(name, ">>>online") {
			active[strings.TrimSuffix(strings.TrimPrefix(name, "user>>>"), ">>>online")] = true
		}
	}
	type result struct {
		key string
		ips []string
		err error
	}
	jobs := make(chan string)
	results := make(chan result, len(bindings))
	// Bound child processes and total collection time even for large panels.
	for i := 0; i < 8; i++ {
		go func() {
			for key := range jobs {
				raw, e := a.command(ctx, "api", "statsonlineiplist", "-s=127.0.0.1:10085", "-t=2", "-email="+key)
				var value struct {
					IPs map[string]json.Number `json:"ips"`
				}
				if e == nil {
					e = json.Unmarshal(raw, &value)
				}
				ips := []string{}
				seen := map[string]bool{}
				if e == nil {
					for ip := range value.IPs {
						addr, parseErr := netip.ParseAddr(strings.Trim(ip, "[]"))
						if parseErr != nil {
							e = fmt.Errorf("在线统计包含无效 IP")
							break
						}
						text := addr.Unmap().String()
						if !seen[text] {
							seen[text] = true
							ips = append(ips, text)
						}
					}
					sort.Strings(ips)
					// Limits are at most 128. A 129-IP sample is sufficient to detect excess.
					if len(ips) > 129 {
						ips = ips[:129]
					}
				}
				results <- result{key: key, ips: ips, err: e}
			}
		}()
	}
	count := 0
	for _, key := range bindings {
		sample[key] = []string{}
		if active[key] {
			count++
		}
	}
	go func() {
		defer close(jobs)
		for _, key := range bindings {
			if active[key] {
				jobs <- key
			}
		}
	}()
	total := 0
	firstError := ""
	for i := 0; i < count; i++ {
		r := <-results
		if r.err != nil {
			if firstError == "" {
				firstError = r.err.Error()
				cancel()
			}
			continue
		}
		sample[r.key] = r.ips
		total += len(r.ips)
	}
	if firstError != "" {
		return nil, firstError
	}
	if total > 5000 {
		return nil, "在线 IP 统计超过单次采集上限"
	}
	return sample, ""
}

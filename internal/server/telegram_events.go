package server

import (
	"fmt"
	"strings"
	"time"
	"vibexui/internal/model"
)

func telegramBytes(n uint64) string {
	units := []string{"B", "KiB", "MiB", "GiB", "TiB", "PiB"}
	v := float64(n)
	i := 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	return fmt.Sprintf("%.2f %s", v, units[i])
}
func telegramEvents(c telegramSettings, st model.State, now time.Time, rt *telegramRuntime) []telegramEvent {
	events := []telegramEvent{}
	add := func(key, fingerprint, text string) {
		events = append(events, telegramEvent{Key: key, Fingerprint: fingerprint, Text: text, Created: now})
	}
	loc, _ := time.LoadLocation(c.Timezone)
	healthKeys := map[string]bool{}
	transition := func(key string, bad bool, down, up string) {
		healthKeys[key] = true
		old := rt.Health[key]
		if bad {
			rt.Health[key] = "down"
			add(key, "down", down)
		} else if old == "down" || old == "up" {
			rt.Health[key] = "up"
			add(key, "up", up)
		} else {
			rt.Health[key] = "initial"
		}
	}
	var upload, download uint64
	online := 0
	serverNames := map[string]string{}
	nodeNames := map[string]string{}
	for _, s := range st.Servers {
		serverNames[s.ID] = shortTelegram(s.Name)
		upload = addBytes(upload, s.Upload)
		download = addBytes(download, s.Download)
		live := !s.LastSeen.IsZero() && now.Sub(s.LastSeen) < 35*time.Second
		if live {
			online++
		}
		// Pending registrations are not offline servers. Use 60 s to tolerate jitter.
		if c.Servers && !s.LastSeen.IsZero() {
			label := fmt.Sprintf("服务器：%s\n地址：%s", shortTelegram(s.Name), shortTelegram(s.Host))
			key := "server/" + s.ID
			if live {
				transition(key, false, "", "✅ 服务器恢复在线\n"+label)
			} else if now.Sub(s.LastSeen) >= 60*time.Second {
				transition(key, true, "🔴 服务器离线\n"+label+"\n最后上报："+s.LastSeen.In(loc).Format("2006-01-02 15:04:05"), "")
			} else {
				// Preserve the last event during the hysteresis interval.
				healthKeys[key] = true
				if rt.Health[key] == "down" {
					add(key, "down", "🔴 服务器离线\n"+label)
				} else if rt.Health[key] == "up" {
					add(key, "up", "✅ 服务器恢复在线\n"+label)
				}
			}
		}
		if c.Xray {
			healthKeys["xray/"+s.ID] = true
		}
		if c.Xray && live {
			bad := s.Error != "" || (s.DesiredRunning && !s.Running)
			// Do not forward Xray stderr: it may contain configuration credentials.
			transition("xray/"+s.ID, bad, "⚠️ Xray 运行或配置异常\n服务器："+shortTelegram(s.Name)+"\n请在面板查看诊断详情。", "✅ Xray 异常已解除\n服务器："+shortTelegram(s.Name))
		}
	}
	policy := func(key, label string, enabled bool, expiry time.Time, quota, used uint64, baseline model.Traffic) {
		if !enabled {
			return
		}
		if c.Expiry && !expiry.IsZero() {
			status := ""
			if !now.Before(expiry) {
				status = "⛔ 已到期"
			} else if c.ExpiryDays > 0 && expiry.Sub(now) <= time.Duration(c.ExpiryDays)*24*time.Hour {
				status = "⏰ 即将到期"
			}
			if status != "" {
				add(key+"/expiry", status+expiry.Format(time.RFC3339Nano), status+"\n"+label+"\n到期时间："+expiry.In(loc).Format("2006-01-02 15:04:05"))
			}
		}
		if c.Traffic && quota > 0 {
			status := ""
			remaining := uint64(0)
			if used >= quota {
				status = "⛔ 流量额度已用尽"
			} else {
				remaining = quota - used
				if c.RemainingGB > 0 && float64(remaining) <= c.RemainingGB*1024*1024*1024 {
					status = "⚠️ 剩余流量不足"
				}
			}
			if status != "" {
				fp := fmt.Sprintf("%s/%d/%d/%d", status, quota, baseline.Upload, baseline.Download)
				add(key+"/traffic", fp, fmt.Sprintf("%s\n%s\n已用：%s / %s\n剩余：%s", status, label, telegramBytes(used), telegramBytes(quota), telegramBytes(remaining)))
			}
		}
	}
	for _, n := range st.Nodes {
		nodeNames[n.ID] = shortTelegram(n.Name)
		policy("node/"+n.ID, fmt.Sprintf("入站：%s · :%d\n服务器：%s", shortTelegram(n.Name), n.Port, serverNames[n.ServerID]), n.Enabled, n.ExpiresAt, n.QuotaBytes, model.NodeUsage(n), n.QuotaBaseline)
	}
	activeClients := 0
	for _, v := range st.Clients {
		if model.ClientStatus(v, now) == "active" {
			activeClients++
		}
		label := "用户：" + shortTelegram(v.Name)
		if v.Email != "" {
			label += "\nEmail：" + shortTelegram(v.Email)
		}
		policy("client/"+v.ID, label, v.Enabled, v.ExpiresAt, v.QuotaBytes, model.QuotaUsage(v), v.QuotaBaseline)
		if c.IPLimit && v.Enabled {
			for _, nid := range v.NodeIDs {
				ip := v.NodeIPStates[nid]
				if now.Before(ip.BlockedUntil) {
					add("ip/"+v.ID+"/"+nid, ip.BlockedUntil.Format(time.RFC3339Nano), fmt.Sprintf("⚠️ 在线 IP 超限，临时限制访问\n%s\n入站：%s\nIP 数：%d / %d\n限制截止：%s", label, nodeNames[nid], len(ip.OnlineIPs), model.ClientIPLimit(v, nid), ip.BlockedUntil.In(loc).Format("15:04:05")))
				}
			}
		}
	}
	for key := range rt.Health {
		if !healthKeys[key] {
			delete(rt.Health, key)
		}
	}
	local := now.In(loc)
	if c.Daily && local.Format("15:04") >= c.ReportTime {
		add("daily", local.Format("2006-01-02"), fmt.Sprintf("📊 每日运行报告\n服务器在线：%d / %d\n入站：%d\n策略允许用户：%d / %d\n累计上传：%s\n累计下载：%s\n累计总流量：%s\n流量为累计值，不是当天增量。", online, len(st.Servers), len(st.Nodes), activeClients, len(st.Clients), telegramBytes(upload), telegramBytes(download), telegramBytes(addBytes(upload, download))))
	}
	// Keep reports ahead of large alert backlogs.
	if len(events) > 0 && strings.HasPrefix(events[len(events)-1].Key, "daily") {
		last := events[len(events)-1]
		copy(events[1:], events[:len(events)-1])
		events[0] = last
	}
	return events
}

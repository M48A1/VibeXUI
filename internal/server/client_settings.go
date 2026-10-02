package server

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"vibexui/internal/model"
)

// Pointers preserve existing settings when an older UI or API omits new fields.
type clientSettings struct {
	LimitIP         *int               `json:"limitIp"`
	NodeFlows       *map[string]string `json:"nodeFlows"`
	UUID            *string            `json:"uuid"`
	Email           *string            `json:"email"`
	Flow            *string            `json:"flow"`
	ReverseTag      *string            `json:"reverseTag"`
	SubscriptionID  *string            `json:"subscriptionId"`
	TelegramID      *string            `json:"telegramId"`
	Group           *string            `json:"group"`
	Comment         *string            `json:"comment"`
	ExternalLinks   *[]string          `json:"externalLinks"`
	TrafficReset    *string            `json:"trafficReset"`
	TrafficResetDay *int               `json:"trafficResetDay"`
	ResetDays       *int               `json:"resetDays"`
	ResetDay        *int               `json:"resetDay"`
	ResetWeekday    *int               `json:"resetWeekday"`
	ResetMax        *int               `json:"resetMax"`
	FirstUseDays    *int               `json:"firstUseDays"`
}

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
var tokenPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{8,128}$`)
var tagPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,100}$`)

func (in clientSettings) apply(st *model.State, c *model.Client, now time.Time) error {
	if in.LimitIP != nil {
		if *in.LimitIP < 0 || *in.LimitIP > 128 {
			return fmt.Errorf("IP 上限须为 0–128")
		}
		if c.LimitIP != *in.LimitIP {
			for id, value := range c.NodeIPStates {
				value.BlockedUntil = time.Time{}
				c.NodeIPStates[id] = value
			}
		}
		c.LimitIP = *in.LimitIP
	}
	if in.NodeFlows != nil {
		for id, flow := range *in.NodeFlows {
			if !model.Contains(c.NodeIDs, id) || (flow != "" && flow != "xtls-rprx-vision") {
				return fmt.Errorf("Flow 覆盖须对应已分配的入站且为有效值")
			}
		}
		c.NodeFlows = *in.NodeFlows
	}
	for id := range c.NodeFlows {
		if !model.Contains(c.NodeIDs, id) {
			delete(c.NodeFlows, id)
		}
	}
	if in.UUID != nil {
		if !uuidPattern.MatchString(*in.UUID) {
			return fmt.Errorf("UUID 格式无效")
		}
		c.UUID = strings.ToLower(*in.UUID)
	}
	if in.Email != nil {
		c.Email = strings.TrimSpace(*in.Email)
	}
	if c.Email == "" {
		c.Email = c.Name
		// Older databases allowed duplicate display names. Give an existing
		// record an unambiguous identifier when an old API omits Email.
		if in.Email == nil {
			existing := false
			conflict := false
			for _, other := range st.Clients {
				if other.ID == c.ID {
					existing = true
					continue
				}
				email := other.Email
				if email == "" {
					email = other.Name
				}
				if strings.EqualFold(email, c.Email) {
					conflict = true
				}
			}
			if existing && conflict {
				c.Email = "client-" + c.ID
			}
		}
	}
	if len(c.Email) > 100 || strings.Contains(c.Email, ">>>") || strings.IndexFunc(c.Email, unicode.IsControl) >= 0 {
		return fmt.Errorf("Email 标识须为 1–100 个字符，不能含控制字符或 >>>")
	}
	if in.Flow != nil {
		if *in.Flow != "" && *in.Flow != "xtls-rprx-vision" {
			return fmt.Errorf("Flow 须为空或 xtls-rprx-vision")
		}
		flow := *in.Flow
		c.Flow = &flow
	}
	if in.ReverseTag != nil {
		c.ReverseTag = strings.TrimSpace(*in.ReverseTag)
	}
	if c.ReverseTag != "" {
		if !tagPattern.MatchString(c.ReverseTag) || c.ReverseTag == "direct" || c.ReverseTag == "api" {
			return fmt.Errorf("反向代理标签无效或使用了保留名称")
		}
		for _, n := range st.Nodes {
			if n.ID == c.ReverseTag {
				return fmt.Errorf("反向代理标签与入站冲突")
			}
		}
	}
	if in.SubscriptionID != nil && *in.SubscriptionID != "" {
		if !tokenPattern.MatchString(*in.SubscriptionID) {
			return fmt.Errorf("订阅标识须为 8–128 位字母、数字、下划线或连字符")
		}
		c.Token = *in.SubscriptionID
	}
	for _, other := range st.Clients {
		if other.ID == c.ID {
			continue
		}
		email := other.Email
		if email == "" {
			email = other.Name
		}
		if strings.EqualFold(email, c.Email) {
			return fmt.Errorf("Email 标识已被其他客户端使用")
		}
		if strings.EqualFold(other.UUID, c.UUID) {
			return fmt.Errorf("UUID 已被其他客户端使用")
		}
	}
	if in.TelegramID != nil {
		c.TelegramID = strings.TrimSpace(*in.TelegramID)
	}
	if c.TelegramID != "" {
		id, err := strconv.ParseInt(c.TelegramID, 10, 64)
		if err != nil || id <= 0 {
			return fmt.Errorf("Telegram ID 须为正整数")
		}
	}
	if in.Group != nil {
		c.Group = strings.TrimSpace(*in.Group)
	}
	if in.Comment != nil {
		c.Comment = *in.Comment
	}
	if len(c.Group) > 100 || len(c.Comment) > 2000 {
		return fmt.Errorf("分组最多 100 字节，备注最多 2000 字节")
	}
	if in.ExternalLinks != nil {
		if len(*in.ExternalLinks) > 100 {
			return fmt.Errorf("外部链接最多 100 条")
		}
		links := []string{}
		for _, raw := range *in.ExternalLinks {
			raw = strings.TrimSpace(raw)
			if raw == "" {
				continue
			}
			u, err := url.Parse(raw)
			if err != nil || len(raw) > 8192 || strings.IndexFunc(raw, unicode.IsControl) >= 0 {
				return fmt.Errorf("外部节点链接无效")
			}
			switch u.Scheme {
			case "vless", "vmess", "trojan", "ss", "ssr", "hysteria", "hysteria2", "tuic", "wireguard":
			default:
				return fmt.Errorf("外部链接须为代理节点分享链接")
			}
			if u.Host == "" && u.Opaque == "" {
				return fmt.Errorf("外部链接缺少节点信息")
			}
			links = append(links, raw)
		}
		c.ExternalLinks = links
	}
	oldMode, oldDay := c.TrafficReset, c.TrafficResetDay
	if c.TrafficResetDay == 0 {
		c.TrafficResetDay = 1
	}
	if in.TrafficReset != nil {
		c.TrafficReset = *in.TrafficReset
	}
	if in.TrafficResetDay != nil {
		c.TrafficResetDay = *in.TrafficResetDay
	}
	if !validTrafficReset(c.TrafficReset, c.TrafficResetDay) {
		return fmt.Errorf("流量重置须为不重置、每小时、每天、每周或每月；重置日须为 1–31")
	}
	if in.TrafficReset != nil {
		c.QuotaPeriodMonths = 0
		c.QuotaCycleAnchor = time.Time{}
	}
	if c.QuotaPeriodMonths == 0 && (c.TrafficReset != oldMode || c.TrafficResetDay != oldDay || in.TrafficReset != nil && c.QuotaResetsAt.IsZero()) {
		c.QuotaResetsAt = nextTrafficReset(now, c.TrafficReset, c.TrafficResetDay)
	}
	oldDays, oldMonthly, oldWeekly, oldMax := c.ResetDays, c.ResetDay, c.ResetWeekday, c.ResetMax
	if in.ResetDays != nil {
		c.ResetDays = *in.ResetDays
	}
	if in.ResetDay != nil {
		c.ResetDay = *in.ResetDay
	}
	if in.ResetWeekday != nil {
		c.ResetWeekday = *in.ResetWeekday
	}
	if in.ResetMax != nil {
		c.ResetMax = *in.ResetMax
	}
	if c.ResetDays < 0 || c.ResetDays > 3650 || c.ResetDay < 0 || c.ResetDay > 31 || c.ResetWeekday < 0 || c.ResetWeekday > 7 || c.ResetMax < 0 || c.ResetMax > 1000000 {
		return fmt.Errorf("自动续期参数无效")
	}
	modes := 0
	if c.ResetDays > 0 {
		modes++
	}
	if c.ResetDay > 0 {
		modes++
	}
	if c.ResetWeekday > 0 {
		modes++
	}
	if modes > 1 {
		return fmt.Errorf("自动续期只能选择一种模式")
	}
	if oldDays != c.ResetDays || oldMonthly != c.ResetDay || oldWeekly != c.ResetWeekday || oldMax != c.ResetMax {
		c.RenewalCount = 0
	}
	if in.FirstUseDays != nil {
		if *in.FirstUseDays < 0 || *in.FirstUseDays > 3650 {
			return fmt.Errorf("首次使用有效期须为 0–3650 天")
		}
		if *in.FirstUseDays > 0 && !c.FirstUsedAt.IsZero() && *in.FirstUseDays != c.FirstUseDays {
			return fmt.Errorf("已经激活的客户端请直接修改到期时间")
		}
		c.FirstUseDays = *in.FirstUseDays
	}
	if c.FirstUseDays > 0 && !c.FirstUsedAt.IsZero() && c.ExpiresAt.IsZero() {
		return fmt.Errorf("已经激活的客户端不能重新开始首次使用计时")
	}
	if c.FirstUseDays > 0 && c.FirstUsedAt.IsZero() && !c.ExpiresAt.IsZero() {
		return fmt.Errorf("首次使用计时与固定到期时间不能同时设置")
	}
	if !c.ExpiresAt.IsZero() && (c.ExpiresAt.Year() < 2000 || c.ExpiresAt.Year() > 9999) {
		return fmt.Errorf("到期时间无效")
	}
	return nil
}

func setSubscriptionUsage(w http.ResponseWriter, st model.State, token string) {
	var upload, download, total uint64
	var expiry time.Time
	unlimited := false
	for _, c := range st.Clients {
		if c.Token != token || model.ClientStatus(c, time.Now()) != "active" {
			continue
		}
		if c.Upload > c.QuotaBaseline.Upload {
			upload = addBytes(upload, c.Upload-c.QuotaBaseline.Upload)
		}
		if c.Download > c.QuotaBaseline.Download {
			download = addBytes(download, c.Download-c.QuotaBaseline.Download)
		}
		if c.QuotaBytes == 0 {
			unlimited = true
		}
		total = addBytes(total, c.QuotaBytes)
		if !c.ExpiresAt.IsZero() && (expiry.IsZero() || c.ExpiresAt.Before(expiry)) {
			expiry = c.ExpiresAt
		}
	}
	if unlimited {
		total = 0
	}
	value := fmt.Sprintf("upload=%d; download=%d; total=%d", upload, download, total)
	if !expiry.IsZero() {
		value += fmt.Sprintf("; expire=%d", expiry.Unix())
	}
	w.Header().Set("Subscription-Userinfo", value)
}

func (s *Server) clearClientIPs(w http.ResponseWriter, r *http.Request) {
	var in struct{}
	if !decode(w, r, &in) {
		return
	}
	var out model.Client
	err := s.store.Update(func(st *model.State) error {
		for i := range st.Clients {
			c := &st.Clients[i]
			if c.ID != r.PathValue("id") {
				continue
			}
			affected := map[string]bool{}
			for _, n := range st.Nodes {
				if !model.Contains(c.NodeIDs, n.ID) {
					continue
				}
				if !c.NodeIPStates[n.ID].BlockedUntil.IsZero() {
					affected[n.ServerID] = true
				}
				if srv := serverAt(st, n.ServerID); srv != nil {
					delete(srv.OnlineIPs, model.BindingKey(c.ID, n.ID))
				}
			}
			c.NodeIPStates = map[string]model.NodeIPState{}
			for sid := range affected {
				bump(st, sid)
			}
			reconcileClients(st, time.Now())
			out = *c
			return nil
		}
		return errNotFound
	})
	s.result(w, err, out)
}

func (s *Server) previewRenewal(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ClientID     string    `json:"clientId"`
		ExpiresAt    time.Time `json:"expiresAt"`
		ResetDays    int       `json:"resetDays"`
		ResetDay     int       `json:"resetDay"`
		ResetWeekday int       `json:"resetWeekday"`
		ResetMax     int       `json:"resetMax"`
	}
	if !decode(w, r, &in) {
		return
	}
	c := model.Client{Enabled: true, ExpiresAt: in.ExpiresAt, ResetDays: in.ResetDays, ResetDay: in.ResetDay, ResetWeekday: in.ResetWeekday, ResetMax: in.ResetMax}
	if in.ClientID != "" {
		st, ok := s.view(w)
		if !ok {
			return
		}
		found := false
		for _, existing := range st.Clients {
			if existing.ID == in.ClientID {
				found = true
				c.Enabled = existing.Enabled
				if existing.ResetDays == c.ResetDays && existing.ResetDay == c.ResetDay && existing.ResetWeekday == c.ResetWeekday && existing.ResetMax == c.ResetMax {
					c.RenewalCount = existing.RenewalCount
				}
				break
			}
		}
		if !found {
			s.result(w, errNotFound, nil)
			return
		}
	}
	days, day, week, max := c.ResetDays, c.ResetDay, c.ResetWeekday, c.ResetMax
	if err := (clientSettings{ResetDays: &days, ResetDay: &day, ResetWeekday: &week, ResetMax: &max}).apply(&model.State{}, &c, time.Now()); err != nil {
		s.result(w, err, nil)
		return
	}
	now := time.Now()
	suggested := time.Time{}
	if c.ResetDay > 0 {
		suggested = nextMonthly(now, c.ResetDay)
	} else if c.ResetWeekday > 0 {
		suggested = nextWeekly(now, c.ResetWeekday)
	} else if c.ResetDays > 0 {
		suggested = now.Add(time.Duration(c.ResetDays) * 24 * time.Hour)
	}
	if c.ExpiresAt.IsZero() {
		respond(w, 200, map[string]any{"suggestedExpiresAt": suggested, "timezone": now.Format("MST -07:00"), "canRenew": false})
		return
	}
	preview := c
	preview.Enabled = true
	preview.ResetMax = 0
	at := now
	if at.Before(c.ExpiresAt) {
		at = c.ExpiresAt
	}
	renewClient(&preview, at)
	needed := preview.RenewalCount - c.RenewalCount
	canRenew := c.Enabled && needed > 0 && (c.ResetMax == 0 || needed <= c.ResetMax-c.RenewalCount)
	respond(w, 200, map[string]any{"expiresAt": c.ExpiresAt, "lastValidAt": c.ExpiresAt.Add(-time.Second), "nextExpiresAt": preview.ExpiresAt, "renewalsNeeded": needed, "canRenew": canRenew, "renewalCount": c.RenewalCount, "suggestedExpiresAt": suggested, "timezone": now.Format("MST -07:00")})
}

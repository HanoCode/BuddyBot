package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// ============================================================
// 官方口径用量对账
//
// 数据源：官方 get-user-request-usage（国内/国际同构，国际版无 /v2 前缀；
// 与社区 workbuddy-switch 的 official_usage 实测协议一致）。
// 用途：把「网关本地记录的真实用量」与「官方计费口径」并排展示对账；
// 官方接口不可用时按账号返回 Error，由前端明确提示回退本地统计。
// ============================================================

// officialUsagePageSize 官方分页大小（社区实测安全值 3000）
const officialUsagePageSize = 3000

// officialUsageMaxPages 分页安全上限（3000 × 100 = 30 万行，足够 30 天）
const officialUsageMaxPages = 100

// officialUsageCacheTTL 拉取结果缓存时长：官方接口按天分页较重，30 分钟内复用
const officialUsageCacheTTL = 30 * time.Minute

// OfficialUsageRow 官方请求明细行（解析后关心的字段）
type OfficialUsageRow struct {
	RequestID   string  `json:"requestId"`
	Credit      float64 `json:"credit"`
	Model       string  `json:"model"`
	Client      string  `json:"client"`
	RequestTime string  `json:"requestTime"`
}

// OfficialUsageModel 单模型官方用量
type OfficialUsageModel struct {
	Model    string  `json:"model"`
	Requests int     `json:"requests"`
	Credits  float64 `json:"credits"`
}

// OfficialUsageDay 单日官方用量
type OfficialUsageDay struct {
	Date    string  `json:"date"` // YYYY-MM-DD
	Requests int    `json:"requests"`
	Credits float64 `json:"credits"`
}

// OfficialUsageAccount 单账号官方用量聚合
type OfficialUsageAccount struct {
	UID      string             `json:"uid"`
	Nickname string             `json:"nickname"`
	Realm    string             `json:"realm"`
	Today    float64            `json:"today"`
	Total    float64            `json:"total"`
	Requests int                `json:"requests"`
	Daily    []OfficialUsageDay `json:"daily"`
	Models   []OfficialUsageModel `json:"models"`
	Error    string             `json:"error,omitempty"` // 该账号拉取失败原因（不影响其他账号）
}

// OfficialUsageReport 官方用量对账报告
type OfficialUsageReport struct {
	FetchedAt string                 `json:"fetchedAt"`
	Days      int                    `json:"days"`
	Source    string                 `json:"source"` // official = 官方接口；partial = 部分账号失败
	Accounts  []OfficialUsageAccount `json:"accounts"`
}

// FetchRequestUsage 拉取单账号 [from, to] 闭区间内的官方请求用量明细（分页聚合）。
func (c *upstreamClient) FetchRequestUsage(cred *UpstreamCred, from, to time.Time) ([]OfficialUsageRow, error) {
	// 国内/国际同构路径；CN 若 404 则回退无 /v2（对齐 FetchBalanceDetail 的 fallback 语义）
	paths := []string{"/billing/meter/get-user-request-usage"}
	if cred.Realm != "global" {
		paths = []string{"/v2/billing/meter/get-user-request-usage", "/billing/meter/get-user-request-usage"}
	}
	layout := "2006-01-02 15:04:05"
	bodyBase := map[string]any{
		"startTime": from.Format(layout),
		"endTime":   to.Format("2006-01-02") + " 23:59:59",
		"pageSize":  officialUsagePageSize,
	}
	var rows []OfficialUsageRow
	seen := map[string]bool{}
	reportedTotal := 0
	for page := 1; page <= officialUsageMaxPages; page++ {
		body := map[string]any{}
		for k, v := range bodyBase {
			body[k] = v
		}
		body["pageNum"] = page
		var lastErr error
		var data json.RawMessage
		for i, p := range paths {
			req, err := http.NewRequest(http.MethodPost, c.billingBaseOf(cred.Realm)+p, bytes.NewReader(mustJSON(body)))
			if err != nil {
				return nil, err
			}
			c.setBillingHeaders(req, cred)
			data, lastErr = c.doEnvelopeLoose(req)
			if lastErr == nil {
				break
			}
			if ue, ok := lastErr.(*upstreamAPIError); ok && ue.Status == http.StatusNotFound && i < len(paths)-1 {
				continue // 404 尝试下一路径
			}
			return nil, lastErr
		}
		if lastErr != nil {
			return nil, lastErr
		}
		var pageResp struct {
			Data struct {
				Rows  []OfficialUsageRow `json:"data"`
				Total any                `json:"total"`
			} `json:"data"`
		}
		if err := json.Unmarshal(data, &pageResp); err != nil {
			return nil, fmt.Errorf("官方用量响应解析失败: %w", err)
		}
		fetched := 0
		for _, r := range pageResp.Data.Rows {
			fetched++
			key := r.RequestID + "|" + r.RequestTime
			if r.RequestID != "" && seen[key] {
				continue
			}
			seen[key] = true
			rows = append(rows, r)
		}
		switch t := pageResp.Data.Total.(type) {
		case float64:
			if int(t) > reportedTotal {
				reportedTotal = int(t)
			}
		case string:
			var n int
			if _, err := fmt.Sscanf(strings.TrimSpace(t), "%d", &n); err == nil && n > reportedTotal {
				reportedTotal = n
			}
		}
		if fetched == 0 || len(rows) >= reportedTotal {
			break
		}
	}
	return rows, nil
}

// doEnvelopeLoose 宽松信封解析：code 0 或 200 均视为成功（官方用量接口实测两种返回）。
func (c *upstreamClient) doEnvelopeLoose(req *http.Request) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	resp, err := c.http.Do(req.WithContext(ctx))
	if err != nil {
		return nil, fmt.Errorf("请求网络异常: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %w", err)
	}
	if resp.StatusCode >= 400 {
		return nil, &upstreamAPIError{Status: resp.StatusCode,
			Msg: fmt.Sprintf("HTTP %d: %s", resp.StatusCode, truncateBody(raw))}
	}
	var env struct {
		Code int             `json:"code"`
		Msg  string          `json:"msg"`
		Data json.RawMessage `json:"data"`
	}
	if json.Unmarshal(raw, &env) != nil {
		return nil, fmt.Errorf("响应信封解析失败: %s", truncateBody(raw))
	}
	if env.Code != 0 && env.Code != 200 {
		return nil, &upstreamAPIError{Status: resp.StatusCode,
			Msg: fmt.Sprintf("code=%d msg=%s", env.Code, env.Msg)}
	}
	return env.Data, nil
}

// AggregateOfficialUsage 把官方明细行聚合为按日/按模型视图。
func AggregateOfficialUsage(rows []OfficialUsageRow, days int) (daily []OfficialUsageDay, models []OfficialUsageModel, today, total float64, requests int) {
	byDay := map[string]*OfficialUsageDay{}
	byModel := map[string]*OfficialUsageModel{}
	todayKey := time.Now().Format("2006-01-02")
	for _, r := range rows {
		if r.Credit < 0 {
			r.Credit = 0
		}
		date := r.RequestTime
		if len(date) >= 10 {
			date = date[:10]
		} else {
			continue
		}
		d := byDay[date]
		if d == nil {
			d = &OfficialUsageDay{Date: date}
			byDay[date] = d
		}
		d.Requests++
		d.Credits += r.Credit
		total += r.Credit
		requests++
		if date == todayKey {
			today += r.Credit
		}
		name := strings.TrimSpace(r.Model)
		if name == "" || name == "—" {
			name = "(未知模型)"
		}
		m := byModel[name]
		if m == nil {
			m = &OfficialUsageModel{Model: name}
			byModel[name] = m
		}
		m.Requests++
		m.Credits += r.Credit
	}
	daily = make([]OfficialUsageDay, 0, len(byDay))
	for _, d := range byDay {
		daily = append(daily, *d)
	}
	sort.Slice(daily, func(i, j int) bool { return daily[i].Date < daily[j].Date })
	models = make([]OfficialUsageModel, 0, len(byModel))
	for _, m := range byModel {
		models = append(models, *m)
	}
	sort.Slice(models, func(i, j int) bool { return models[i].Credits > models[j].Credits })
	if len(models) > 10 {
		models = models[:10]
	}
	return
}

// ---------- Service 层聚合入口（带缓存） ----------

type officialUsageCache struct {
	mu       sync.Mutex
	report   *OfficialUsageReport
	at       time.Time
	fetching bool
}

var ouCache officialUsageCache

// OfficialUsage 拉取全部在线账号最近 days 天的官方口径用量（带 30 分钟缓存）。
// force = 跳过缓存重新拉取。单账号失败不影响其他账号，失败原因写入该账号 Error。
func (s *Service) OfficialUsage(days int, force bool) (*OfficialUsageReport, error) {
	if days <= 0 {
		days = 7
	}
	ouCache.mu.Lock()
	if !force && ouCache.report != nil && ouCache.report.Days == days &&
		time.Since(ouCache.at) < officialUsageCacheTTL {
		rep := ouCache.report
		ouCache.mu.Unlock()
		return rep, nil
	}
	ouCache.mu.Unlock()

	to := time.Now()
	from := to.AddDate(0, 0, -(days - 1))
	startOfDay := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, from.Location())

	rep := &OfficialUsageReport{FetchedAt: Now(), Days: days, Source: "official", Accounts: []OfficialUsageAccount{}}
	for _, a := range s.Accounts() {
		item := OfficialUsageAccount{UID: a.UID, Nickname: a.Nickname, Realm: a.Realm}
		if a.Status != "online" {
			item.Error = "账号不在线，跳过"
			rep.Accounts = append(rep.Accounts, item)
			continue
		}
		cred, err := LoadUpstreamCred(s.AuthDir(), a.Credential)
		if err != nil {
			item.Error = "凭证读取失败: " + err.Error()
			rep.Accounts = append(rep.Accounts, item)
			continue
		}
		rows, err := s.Scheduler().up.FetchRequestUsage(cred, startOfDay, to)
		if err != nil {
			item.Error = err.Error()
			rep.Accounts = append(rep.Accounts, item)
			continue
		}
		daily, models, today, total, requests := AggregateOfficialUsage(rows, days)
		item.Daily, item.Models, item.Today, item.Total, item.Requests = daily, models, today, total, requests
		rep.Accounts = append(rep.Accounts, item)
		time.Sleep(300 * time.Millisecond) // 账号间隔防风控
	}
	failed := 0
	for _, a := range rep.Accounts {
		if a.Error != "" {
			failed++
		}
	}
	if failed == len(rep.Accounts) && len(rep.Accounts) > 0 {
		rep.Source = "unavailable" // 全部失败：前端应回退本地统计并明示
	} else if failed > 0 {
		rep.Source = "partial"
	}

	ouCache.mu.Lock()
	ouCache.report = rep
	ouCache.at = time.Now()
	ouCache.mu.Unlock()
	return rep, nil
}

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"workbuddy-desktop/internal/core"
)

// LogsAPI 日志查询API
type LogsAPI struct {
	service *core.Service
}

// NewLogsAPI 创建日志API
func NewLogsAPI(service *core.Service) *LogsAPI {
	return &LogsAPI{service: service}
}

// RequestLog 请求日志
type RequestLog = core.RequestLog

// TaskLog 任务日志
type TaskLog = core.TaskLog

// LogQuery 日志查询参数（时间范围按 Unix 秒，服务端过滤 + 分页）
type LogQuery struct {
	From     int64  `json:"from,omitempty"` // Unix 秒，含
	To       int64  `json:"to,omitempty"`   // Unix 秒，含
	Keyword  string `json:"keyword,omitempty"`
	Key      string `json:"key,omitempty"`
	Model    string `json:"model,omitempty"`
	Status   int    `json:"status,omitempty"`
	StatusIn []int  `json:"statusIn,omitempty"`
	Type     string `json:"type,omitempty"` // 任务日志类型
	// UID 账号精确匹配。只有带账号维度的日志（任务日志）认这个字段，
	// 请求日志 / 积分流水 / 审计日志会忽略它——它们没有 uid 归属，硬凑等于假过滤。
	UID      string `json:"uid,omitempty"`
	Page     int    `json:"page,omitempty"`
	PageSize int    `json:"pageSize,omitempty"`
}

// RequestLogPage 请求日志分页结果
type RequestLogPage struct {
	Items    []RequestLog `json:"items"`
	Total    int          `json:"total"`
	Page     int          `json:"page"`
	PageSize int          `json:"pageSize"`
	Tokens   int          `json:"tokens"`
	Errors   int          `json:"errors"`
}

// TaskLogPage 任务日志分页结果
type TaskLogPage struct {
	Items    []TaskLog `json:"items"`
	Total    int       `json:"total"`
	Page     int       `json:"page"`
	PageSize int       `json:"pageSize"`
}

// GetRequestLogs 请求日志（时间范围 / 关键字 / 状态码过滤 + 分页）
func (l *LogsAPI) GetRequestLogs(ctx context.Context, q LogQuery) (*RequestLogPage, error) {
	all := l.service.Store().ListRequestLogs() // 新 → 旧
	kw := strings.ToLower(strings.TrimSpace(q.Keyword))
	statusSet := map[int]bool{}
	for _, s := range q.StatusIn {
		statusSet[s] = true
	}

	filtered := make([]RequestLog, 0, len(all))
	tokens, errors := 0, 0
	for _, lg := range all {
		if q.From > 0 && lg.TS < q.From {
			continue
		}
		if q.To > 0 && lg.TS > q.To {
			continue
		}
		if q.Status != 0 && lg.Status != q.Status {
			continue
		}
		if len(statusSet) > 0 && !statusSet[lg.Status] {
			continue
		}
		if q.Key != "" && !strings.Contains(strings.ToLower(lg.KeyName+lg.KeyID), strings.ToLower(q.Key)) {
			continue
		}
		if q.Model != "" && !strings.Contains(strings.ToLower(lg.Model), strings.ToLower(q.Model)) {
			continue
		}
		if kw != "" {
			hit := false
			for _, f := range []string{lg.KeyName, lg.Model, lg.IP, lg.SessionID, lg.ID, lg.Error} {
				if strings.Contains(strings.ToLower(f), kw) {
					hit = true
					break
				}
			}
			if !hit {
				continue
			}
		}
		filtered = append(filtered, lg)
		tokens += lg.Tokens
		if lg.Status >= 400 {
			errors++
		}
	}

	page, size, items := paginate(filtered, q.Page, q.PageSize)
	return &RequestLogPage{Items: items, Total: len(filtered), Page: page, PageSize: size, Tokens: tokens, Errors: errors}, nil
}

// GetTaskLogs 任务日志（时间范围 / 类型 / 状态过滤 + 分页）
func (l *LogsAPI) GetTaskLogs(ctx context.Context, q LogQuery) (*TaskLogPage, error) {
	all := l.service.Store().ListTaskLogs()
	kw := strings.ToLower(strings.TrimSpace(q.Keyword))
	statusSet := map[int]bool{}
	for _, s := range q.StatusIn {
		statusSet[s] = true
	}
	filtered := make([]TaskLog, 0, len(all))
	for _, lg := range all {
		if q.From > 0 && lg.TS < q.From {
			continue
		}
		if q.To > 0 && lg.TS > q.To {
			continue
		}
		if q.Type != "" && lg.Type != q.Type {
			continue
		}
		if q.UID != "" && lg.UID != q.UID {
			continue
		}
		if q.Status != 0 && !matchTaskStatus(lg.Status, q.Status) {
			continue
		}
		if len(statusSet) > 0 && !statusSet[taskStatusIndex(lg.Status)] {
			continue
		}
		if kw != "" {
			hit := false
			for _, f := range []string{lg.UID, lg.Type, lg.Message, lg.Trigger} {
				if strings.Contains(strings.ToLower(f), kw) {
					hit = true
					break
				}
			}
			if !hit {
				continue
			}
		}
		filtered = append(filtered, lg)
	}
	page, size, items := paginate(filtered, q.Page, q.PageSize)
	return &TaskLogPage{Items: items, Total: len(filtered), Page: page, PageSize: size}, nil
}

// matchTaskStatus 状态过滤：1=成功 2=失败 3=跳过（与前端选项一致）
func matchTaskStatus(status string, code int) bool {
	switch code {
	case 1:
		return status == core.TaskSuccess
	case 2:
		return status == core.TaskFailed
	case 3:
		return status == core.TaskSkipped
	}
	return true
}

// taskStatusIndex 任务状态 → 过滤码（导出复用）
func taskStatusIndex(status string) int {
	switch status {
	case core.TaskSuccess:
		return 1
	case core.TaskFailed:
		return 2
	default:
		return 3
	}
}

func paginate[T any](all []T, page, size int) (int, int, []T) {
	if page <= 0 {
		page = 1
	}
	if size <= 0 {
		size = 50
	}
	start := (page - 1) * size
	if start > len(all) {
		start = len(all)
	}
	end := start + size
	if end > len(all) {
		end = len(all)
	}
	return page, size, all[start:end]
}

// LogExportResult 日志导出结果（含落盘路径与条数）
type LogExportResult struct {
	Path  string `json:"path"`
	Count int    `json:"count"`
}

// Export 弹出系统保存对话框，把日志导出到用户选择的位置，返回文件绝对路径与条数
func (l *LogsAPI) Export(ctx context.Context, format string, scope string, q LogQuery) (*LogExportResult, error) {
	format = strings.ToLower(strings.TrimSpace(format))
	if format != "csv" && format != "json" {
		return nil, fmt.Errorf("不支持的导出格式: %s", format)
	}
	defaultName := fmt.Sprintf("workbuddy-%s-logs-%s.%s", scope, time.Now().Format("20060102-150405"), format)
	filter, filterPattern := "JSON 文件", "*.json"
	if format == "csv" {
		filter, filterPattern = "CSV 文件", "*.csv"
	}
	path, err := saveExportDialog(defaultName, filter, filterPattern)
	if err != nil {
		return nil, err
	}
	if path == "" {
		return &LogExportResult{}, nil // 用户取消
	}

	var content []byte
	count := 0
	if scope == "task" {
		res, _ := l.GetTaskLogs(ctx, LogQuery{From: q.From, To: q.To, Keyword: q.Keyword, Type: q.Type, UID: q.UID, Status: q.Status, Page: 1, PageSize: 100000})
		count = len(res.Items)
		content = marshalExport(format, taskHeaders(), toTaskRows(res.Items))
	} else {
		res, _ := l.GetRequestLogs(ctx, LogQuery{From: q.From, To: q.To, Keyword: q.Keyword, Key: q.Key, Model: q.Model, Status: q.Status, StatusIn: q.StatusIn, Page: 1, PageSize: 100000})
		count = len(res.Items)
		content = marshalExport(format, reqHeaders(), toReqRows(res.Items))
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		return nil, err
	}
	revealInFileManager(path)
	return &LogExportResult{Path: path, Count: count}, nil
}

// ClearLogs 清空日志（scope: request / task / credit / audit / all），返回清除条数
func (l *LogsAPI) ClearLogs(ctx context.Context, scope string) (int, error) {
	if err := ensureWritable(l.service); err != nil {
		return 0, err
	}
	switch scope {
	case "request", "task", "credit", "audit", "all":
	default:
		return 0, fmt.Errorf("未知范围: %s", scope)
	}
	n := l.service.Store().ClearLogs(scope)
	audit(l.service, "logs.clear", scope, fmt.Sprintf("清除 %d 条", n))
	return n, nil
}

// CreditLogPage 积分流水分页结果
type CreditLogPage struct {
	Items    []core.CreditLog `json:"items"`
	Total    int              `json:"total"`
	Page     int              `json:"page"`
	PageSize int              `json:"pageSize"`
}

// GetCreditLogs 积分变动流水（真实余额观测差值，时间/账号过滤 + 分页）
func (l *LogsAPI) GetCreditLogs(ctx context.Context, q LogQuery) (*CreditLogPage, error) {
	all := l.service.Store().ListCreditLogs()
	kw := strings.ToLower(strings.TrimSpace(q.Keyword))
	filtered := make([]core.CreditLog, 0, len(all))
	for _, lg := range all {
		if q.From > 0 && lg.TS < q.From {
			continue
		}
		if q.To > 0 && lg.TS > q.To {
			continue
		}
		if kw != "" && !strings.Contains(strings.ToLower(lg.UID+lg.Note), kw) {
			continue
		}
		filtered = append(filtered, lg)
	}
	page, size, items := paginate(filtered, q.Page, q.PageSize)
	return &CreditLogPage{Items: items, Total: len(filtered), Page: page, PageSize: size}, nil
}

// CreditDetailTask 按任务类型的领取积分汇总
type CreditDetailTask struct {
	Type    string `json:"type"`
	Count   int    `json:"count"`   // 有领取积分的执行次数
	Credits int    `json:"credits"` // 累计领取积分
}

// CreditDetailDay 按天的领取积分汇总（本地时区，日期格式 2006-01-02）
type CreditDetailDay struct {
	Date    string `json:"date"`
	Count   int    `json:"count"`
	Credits int    `json:"credits"`
}

// CreditDetail 任务领取积分明细
//
// 口径分两本账，都展示在本页：
//   - **确认领取**（Credits / ByTask / ByDay / Items）：只统计上游**明确返回了积分数值**
//     的领取动作（成长任务 reward_credit / 连登档位 credit / 抽奖 credit 奖品 / 礼包补偿
//     credit）。签到本金、开学季领奖、盲盒物品、trial 加油包等上游不返回数值的动作不写 0
//     充数，由 Runs / NoAmount 如实计数。
//   - **观测入账**（Observed / ObservedCredits / ObservedToday / ObservedLast7d）：积分流水
//     里余额上升的差值条目（delta<0，含上游异步发放的签到/任务计分）。到账是事实、来源
//     无法从响应确证，故单列且标注「来源未确证」，不与确认领取混算，也不写进 ByTask。
//
// 筛选分两层，Today / Last7d / ObservedToday / ObservedLast7d 只吃第一层：
//   - 维度筛选（UID / Type / Keyword）：确认领取三卡按它收窄；观测入账只吃 UID / Keyword
//     （它没有任务类型，选了 Type 筛选时 Observed 返回空，前端对应隐藏）；
//   - 时间范围（From / To）：只作用于 Credits / Runs / NoAmount / ByTask / ByDay / Items /
//     Observed / ObservedCredits。
type CreditDetail struct {
	Today    int                `json:"today"`    // 今日领取（不受查询范围影响）
	Last7d   int                `json:"last7d"`   // 近 7 天领取（含今日）
	Credits  int                `json:"credits"`  // 查询范围内领取合计
	Runs     int                `json:"runs"`     // 查询范围内执行记录条数
	NoAmount int                `json:"noAmount"` // 其中未返回积分数值的条数
	ByTask   []CreditDetailTask `json:"byTask"`
	ByDay    []CreditDetailDay  `json:"byDay"`
	Items    []core.TaskLog     `json:"items"` // 仅 credits > 0，新 → 旧，分页
	ItemHits int                `json:"itemHits"`
	Page     int                `json:"page"`
	PageSize int                `json:"pageSize"`

	Observed        []core.CreditLog `json:"observed"`        // 观测入账条目（余额上升，新 → 旧，截断 50 条）
	ObservedCredits int              `json:"observedCredits"` // 查询范围内观测入账合计（正数，分）
	ObservedToday   int              `json:"observedToday"`   // 今日观测入账（不受查询范围影响）
	ObservedLast7d  int              `json:"observedLast7d"`  // 近 7 天观测入账（含今日）
}

// GetCreditDetail 任务领取积分明细：汇总（今日 / 近 7 天 / 范围内）+ 按任务 / 按天 + 逐条明细。
// 账号（UID 精确）/ 任务类型 / 关键字（账号 / 任务 / 说明）过滤与任务日志同口径；
// 今日与近 7 天跟随这三个维度收窄、忽略时间范围，详见 CreditDetail 的注释。
func (l *LogsAPI) GetCreditDetail(ctx context.Context, q LogQuery) (*CreditDetail, error) {
	all := l.service.Store().ListTaskLogs() // 新 → 旧
	kw := strings.ToLower(strings.TrimSpace(q.Keyword))
	localDay := func(ts int64) string { return time.Unix(ts, 0).Format("2006-01-02") }
	today := time.Now().Format("2006-01-02")
	weekStart := time.Now().AddDate(0, 0, -6).Format("2006-01-02")

	// 维度筛选：账号 / 任务类型 / 关键字。今日与近 7 天也走这一层。
	dimMatch := func(lg core.TaskLog) bool {
		if q.UID != "" && lg.UID != q.UID {
			return false
		}
		if q.Type != "" && lg.Type != q.Type {
			return false
		}
		if kw != "" {
			hit := false
			for _, f := range []string{lg.UID, lg.Type, lg.Message, lg.Trigger} {
				if strings.Contains(strings.ToLower(f), kw) {
					hit = true
					break
				}
			}
			if !hit {
				return false
			}
		}
		return true
	}

	out := &CreditDetail{ByTask: []CreditDetailTask{}, ByDay: []CreditDetailDay{}, Items: []core.TaskLog{}}
	byTask := map[string]*CreditDetailTask{}
	byDay := map[string]*CreditDetailDay{}
	earned := make([]core.TaskLog, 0)

	// 今日 / 近 7 天：跟随维度筛选，但不随时间范围变化（选了「近 1 小时」也该看到今日总量）
	for _, lg := range all {
		if lg.Credits <= 0 || !dimMatch(lg) {
			continue
		}
		day := localDay(lg.TS)
		if day == today {
			out.Today += lg.Credits
		}
		if day >= weekStart {
			out.Last7d += lg.Credits
		}
	}

	for _, lg := range all {
		if q.From > 0 && lg.TS < q.From {
			continue
		}
		if q.To > 0 && lg.TS > q.To {
			continue
		}
		if !dimMatch(lg) {
			continue
		}
		out.Runs++
		if lg.Credits <= 0 {
			out.NoAmount++
			continue
		}
		out.Credits += lg.Credits
		if t := byTask[lg.Type]; t == nil {
			byTask[lg.Type] = &CreditDetailTask{Type: lg.Type, Count: 1, Credits: lg.Credits}
		} else {
			t.Count++
			t.Credits += lg.Credits
		}
		day := localDay(lg.TS)
		if d := byDay[day]; d == nil {
			byDay[day] = &CreditDetailDay{Date: day, Count: 1, Credits: lg.Credits}
		} else {
			d.Count++
			d.Credits += lg.Credits
		}
		earned = append(earned, lg)
	}

	for _, t := range byTask {
		out.ByTask = append(out.ByTask, *t)
	}
	// 领取多的排前面，同额按类型名稳定排序（结果可复现，不依赖 map 遍历顺序）
	sort.Slice(out.ByTask, func(i, j int) bool {
		if out.ByTask[i].Credits != out.ByTask[j].Credits {
			return out.ByTask[i].Credits > out.ByTask[j].Credits
		}
		return out.ByTask[i].Type < out.ByTask[j].Type
	})
	for _, d := range byDay {
		out.ByDay = append(out.ByDay, *d)
	}
	sort.Slice(out.ByDay, func(i, j int) bool { return out.ByDay[i].Date > out.ByDay[j].Date })

	out.ItemHits = len(earned)
	page, size, items := paginate(earned, q.Page, q.PageSize)
	out.Page, out.PageSize, out.Items = page, size, items

	// 观测入账：积分流水里余额上升的条目（delta<0）。没有任务类型维度——选了 Type
	// 筛选时返回空（前端隐藏该区），其余维度只吃 UID / Keyword + 时间范围。
	if q.Type == "" {
		const maxObserved = 50
		observed := make([]core.CreditLog, 0)
		for _, lg := range l.service.Store().ListCreditLogs() { // 新 → 旧
			if lg.Delta >= 0 {
				continue // 0 与正 delta 是消耗，不是入账
			}
			if q.UID != "" && lg.UID != q.UID {
				continue
			}
			if kw != "" {
				hit := false
				for _, f := range []string{lg.UID, lg.Note} {
					if strings.Contains(strings.ToLower(f), kw) {
						hit = true
						break
					}
				}
				if !hit {
					continue
				}
			}
			day := localDay(lg.TS)
			if day == today {
				out.ObservedToday += -int(lg.Delta)
			}
			if day >= weekStart {
				out.ObservedLast7d += -int(lg.Delta)
			}
			if q.From > 0 && lg.TS < q.From {
				continue
			}
			if q.To > 0 && lg.TS > q.To {
				continue
			}
			out.ObservedCredits += -int(lg.Delta)
			if len(observed) < maxObserved {
				observed = append(observed, lg)
			}
		}
		out.Observed = observed
	} else {
		out.Observed = []core.CreditLog{}
	}
	return out, nil
}

// AuditLogPage 审计日志分页结果
type AuditLogPage struct {
	Items    []core.AuditLog `json:"items"`
	Total    int             `json:"total"`
	Page     int             `json:"page"`
	PageSize int             `json:"pageSize"`
}

// GetAuditLogs 审计日志（管理侧敏感操作记录，时间/关键字过滤 + 分页）
func (l *LogsAPI) GetAuditLogs(ctx context.Context, q LogQuery) (*AuditLogPage, error) {
	all := l.service.Store().ListAuditLogs()
	kw := strings.ToLower(strings.TrimSpace(q.Keyword))
	filtered := make([]core.AuditLog, 0, len(all))
	for _, lg := range all {
		if q.From > 0 && lg.TS < q.From {
			continue
		}
		if q.To > 0 && lg.TS > q.To {
			continue
		}
		if q.Type != "" && lg.Action != q.Type {
			continue
		}
		if kw != "" {
			hit := false
			for _, f := range []string{lg.Action, lg.Target, lg.Detail} {
				if strings.Contains(strings.ToLower(f), kw) {
					hit = true
					break
				}
			}
			if !hit {
				continue
			}
		}
		filtered = append(filtered, lg)
	}
	page, size, items := paginate(filtered, q.Page, q.PageSize)
	return &AuditLogPage{Items: items, Total: len(filtered), Page: page, PageSize: size}, nil
}

// OpenExportDir 打开导出目录
func (l *LogsAPI) OpenExportDir(ctx context.Context) (string, error) {
	dir, err := exportDir()
	if err != nil {
		return "", err
	}
	return dir, nil
}

func exportDir() (string, error) {
	dir := filepath.Join(core.AppDir(), "exports")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

func reqHeaders() []string {
	return []string{"time", "key", "model", "status", "tokens", "input_tokens", "output_tokens", "latency_ms", "ip", "session_id", "stream", "error"}
}

func toReqRows(logs []RequestLog) [][]string {
	rows := make([][]string, 0, len(logs))
	for _, l := range logs {
		rows = append(rows, []string{
			l.Time, l.KeyName, l.Model, fmt.Sprint(l.Status), fmt.Sprint(l.Tokens),
			fmt.Sprint(l.InputTokens), fmt.Sprint(l.OutputTokens),
			fmt.Sprintf("%.0f", l.Latency), l.IP, l.SessionID,
			fmt.Sprint(l.Stream), l.Error,
		})
	}
	return rows
}

func taskHeaders() []string {
	return []string{"time", "type", "trigger", "uid", "status", "credits", "message", "duration_ms"}
}

func toTaskRows(logs []TaskLog) [][]string {
	rows := make([][]string, 0, len(logs))
	for _, l := range logs {
		rows = append(rows, []string{
			l.Time, l.Type, l.Trigger, l.UID, l.Status, fmt.Sprint(l.Credits), l.Message, fmt.Sprintf("%.0f", l.Duration),
		})
	}
	return rows
}

func marshalExport(format string, headers []string, rows [][]string) []byte {
	if format == "json" {
		items := make([]map[string]string, 0, len(rows))
		for _, r := range rows {
			m := map[string]string{}
			for i, h := range headers {
				m[h] = r[i]
			}
			items = append(items, m)
		}
		b, _ := json.MarshalIndent(items, "", "  ")
		return b
	}
	var sb strings.Builder
	sb.WriteString(strings.Join(headers, ",") + "\n")
	for _, r := range rows {
		escaped := make([]string, len(r))
		for i, c := range r {
			if strings.ContainsAny(c, ",\"\n") {
				c = "\"" + strings.ReplaceAll(c, "\"", "\"\"") + "\""
			}
			escaped[i] = c
		}
		sb.WriteString(strings.Join(escaped, ",") + "\n")
	}
	return []byte(sb.String())
}

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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

// Export 导出日志到备份目录，返回文件绝对路径与条数
func (l *LogsAPI) Export(ctx context.Context, format string, scope string, q LogQuery) (*LogExportResult, error) {
	format = strings.ToLower(strings.TrimSpace(format))
	if format != "csv" && format != "json" {
		return nil, fmt.Errorf("不支持的导出格式: %s", format)
	}
	dir, err := exportDir()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, fmt.Sprintf("%s-logs-%s.%s", scope, time.Now().Format("20060102-150405"), format))

	var content []byte
	count := 0
	if scope == "task" {
		res, _ := l.GetTaskLogs(ctx, LogQuery{From: q.From, To: q.To, Keyword: q.Keyword, Type: q.Type, Status: q.Status, Page: 1, PageSize: 100000})
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
	return []string{"time", "type", "trigger", "uid", "status", "message", "duration_ms"}
}

func toTaskRows(logs []TaskLog) [][]string {
	rows := make([][]string, 0, len(logs))
	for _, l := range logs {
		rows = append(rows, []string{
			l.Time, l.Type, l.Trigger, l.UID, l.Status, l.Message, fmt.Sprintf("%.0f", l.Duration),
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

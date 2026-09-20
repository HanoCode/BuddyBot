package core

import (
	"bufio"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ============================================================
// 本机会话日志全文检索（MCP search_sessions 工具的数据层）
//
// 数据源与 clientstats.go 同根（~/.workbuddy / ~/.workbuddy-ai 的
// projects|sessions 下 *.jsonl），但这里是显式的用户发起检索：
// 读取消息正文、命中即返回 ± 上下文片段，其余字段（时间/角色/项目）
// 与统计口径一致。只在内存中匹配，不落盘、不外发。
// ============================================================

// 检索硬预算：MCP 是单线程串行服务，一次调用不能把整个 server 卡住
const (
	sessionSearchMaxFiles = 20000
	sessionSearchBudget   = 5 * time.Second
)

// SessionSearchHit 单条命中
type SessionSearchHit struct {
	SessionID string `json:"sessionId"`
	Source    string `json:"source"` // workbuddy / workbuddy-ai
	Title     string `json:"title"`
	Project   string `json:"project"` // 目录名（路径段），非 cwd 全路径
	Role      string `json:"role"`    // user / assistant / 其他原始 role
	Timestamp int64  `json:"ts"`      // Unix 秒
	Snippet   string `json:"snippet"` // 命中位置 ± 上下文（换行折叠为空格）
}

// SessionSearchResult 一次检索的完整结果
type SessionSearchResult struct {
	Query        string            `json:"query"`
	Days         int               `json:"days"`
	FilesScanned int               `json:"filesScanned"`
	Hits         []SessionSearchHit `json:"hits"`
	Sessions     int               `json:"sessions"` // 命中覆盖的去重会话数
	Truncated    bool              `json:"truncated"`
}

// searchRecord JSONL 行的检索视角解析（只需时间/角色/正文；正文先取原始 JSON 再按形态展开）
type searchRecord struct {
	Timestamp json.Number `json:"timestamp"`
	Type      string      `json:"type"`
	Role      string      `json:"role"`
	SessionID string      `json:"sessionId"`
	Content   json.RawMessage `json:"content"`
}

// sessionTextContent 展开 content 的两种形态：纯字符串 / 块数组 [{type,text}]。
// 其他形态（工具参数等非文本块）如实跳过，不猜。
func sessionTextContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var b strings.Builder
	for _, blk := range blocks {
		if blk.Text != "" {
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(blk.Text)
		}
	}
	return b.String()
}

// snippetOf 取命中位置前 after 后 before 的片段，换行折叠为空格，两端补省略号
func snippetOf(text string, idx, qlen int) string {
	const before, after = 60, 140
	start := idx - before
	if start < 0 {
		start = 0
	}
	end := idx + qlen + after
	if end > len(text) {
		end = len(text)
	}
	out := strings.ReplaceAll(text[start:end], "\n", " ")
	out = strings.Join(strings.Fields(out), " ") // 折叠连续空白
	if start > 0 {
		out = "…" + out
	}
	if end < len(text) {
		out += "…"
	}
	return out
}

// SearchClientSessions 在本机会话日志中检索 query（大小写不敏感的子串匹配），
// 返回最近 days 天内至多 limit 条命中。目录缺失返回空集，不报错。
func SearchClientSessions(query, project string, days, limit int) *SessionSearchResult {
	q := strings.ToLower(strings.TrimSpace(query))
	res := &SessionSearchResult{Query: query, Hits: []SessionSearchHit{}}
	if q == "" {
		return res
	}
	if days <= 0 {
		days = 30
	}
	if days > 365 {
		days = 365
	}
	res.Days = days
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}

	now := time.Now()
	cutoffMs := now.AddDate(0, 0, -days).UnixMilli()
	projLower := strings.ToLower(strings.TrimSpace(project))
	// 硬预算：MCP 是单线程串行服务，一次检索不能把整个 server 卡住
	deadline := now.Add(sessionSearchBudget)

	seenSession := map[string]struct{}{}
	for _, src := range clientStatsRoots() {
		titles := map[string]string{}
		for _, root := range src.roots {
			err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
				if err != nil {
					return nil // 单个不可读路径跳过
				}
				if d.IsDir() {
					if d.Name() == "subagents" {
						return filepath.SkipDir // 子代理日志重复父会话内容，与统计口径一致
					}
					return nil
				}
				if !strings.HasSuffix(d.Name(), ".jsonl") {
					return nil
				}
				if res.FilesScanned >= sessionSearchMaxFiles || time.Now().After(deadline) {
					res.Truncated = true
					return filepath.SkipAll
				}
				if info, statErr := d.Info(); statErr == nil && info.ModTime().Before(time.UnixMilli(cutoffMs)) {
					return nil // 最后修改早于窗口：文件内不可能有窗口内记录
				}
				if res.Truncated {
					return filepath.SkipAll
				}
				res.FilesScanned++
				hits := searchSessionFile(path, src.source, q, cutoffMs, res.Hits, limit)
				if len(hits) == 0 {
					return nil
				}
				if _, ok := titles[hits[0].SessionID]; !ok {
					titles[hits[0].SessionID] = firstTitleOfJSONL(path)
				}
				for i := range hits {
					if projLower != "" && !strings.Contains(strings.ToLower(hits[i].Project), projLower) {
						continue
					}
					hits[i].Title = titles[hits[i].SessionID]
					res.Hits = append(res.Hits, hits[i])
					sk := src.source + "|" + hits[i].SessionID
					seenSession[sk] = struct{}{}
					if len(res.Hits) >= limit {
						res.Truncated = true
						return filepath.SkipAll
					}
				}
				return nil
			})
			_ = err
			if res.Truncated {
				break
			}
		}
		if res.Truncated {
			break
		}
	}
	res.Sessions = len(seenSession)
	return res
}

// searchSessionFile 扫描单个会话文件，返回未回填 title 的命中（最多填满剩余空间）
func searchSessionFile(path, source, query string, cutoffMs int64, existing []SessionSearchHit, limit int) []SessionSearchHit {
	remaining := limit - len(existing)
	if remaining <= 0 {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	var hits []SessionSearchHit
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 32<<20) // 单行可能含大段正文
	sessID := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var r searchRecord
		if json.Unmarshal([]byte(line), &r) != nil {
			continue
		}
		if r.Type != "" && r.Type != "message" {
			continue // session-meta 等非消息行
		}
		text := sessionTextContent(r.Content)
		if text == "" {
			continue
		}
		lower := strings.ToLower(text)
		idx := strings.Index(lower, query)
		if idx < 0 {
			continue
		}
		ts := tsToMillis(r.Timestamp) / 1000
		if ts > 0 && ts*1000 < cutoffMs {
			continue
		}
		hits = append(hits, SessionSearchHit{
			SessionID: orElse(r.SessionID, sessID),
			Source:    source,
			Project:   projectOfPath(path),
			Role:      r.Role,
			Timestamp: ts,
			Snippet:   snippetOf(text, idx, len(query)),
		})
		if len(hits) >= remaining {
			break
		}
	}
	return hits
}

func orElse(v, fallback string) string {
	if v != "" {
		return v
	}
	return fallback
}

// projectOfPath 会话路径倒数第二段即项目目录名（…/projects/<项目>/<会话>.jsonl）
func projectOfPath(path string) string {
	dir := filepath.Base(filepath.Dir(path))
	if dir == "" || dir == "." || dir == string(filepath.Separator) {
		return "(未知项目)"
	}
	return dir
}

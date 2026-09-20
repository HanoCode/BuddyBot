package core

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// writeSessionJSONL 造一个会话文件（content 支持字符串与块数组两种形态）
func writeSessionJSONL(t *testing.T, home, project, sessID string, lines ...string) string {
	t.Helper()
	dir := filepath.Join(home, ".workbuddy", "projects", project)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建会话目录失败: %v", err)
	}
	p := filepath.Join(dir, sessID+".jsonl")
	if err := os.WriteFile(p, []byte(joinLines(lines)), 0o644); err != nil {
		t.Fatalf("写入会话文件失败: %v", err)
	}
	return p
}

func joinLines(lines []string) string {
	out := ""
	for _, l := range lines {
		out += l + "\n"
	}
	return out
}

func TestSearchClientSessionsHitAndSnippet(t *testing.T) {
	home := t.TempDir()
	old := userHomeDir
	userHomeDir = func() (string, error) { return home, nil }
	defer func() { userHomeDir = old }()

	now := time.Now().UnixMilli()
	writeSessionJSONL(t, home, "proj-a", "sess-1",
		`{"type":"session-meta","sessionId":"sess-1","timestamp":`+strconv.FormatInt(now, 10)+`}`,
		`{"type":"message","role":"user","timestamp":`+strconv.FormatInt(now, 10)+`,"content":[{"type":"input_text","text":"帮我看下 Nginx 反向代理的配置"}]}`,
		`{"type":"message","role":"assistant","timestamp":`+strconv.FormatInt(now, 10)+`,"content":[{"type":"text","text":"Nginx 反向代理需要在 location 里配置 proxy_pass，并补上 proxy_set_header"}]}`,
	)
	// 另一项目里的不相关会话
	writeSessionJSONL(t, home, "proj-b", "sess-2",
		`{"type":"message","role":"user","timestamp":`+strconv.FormatInt(now, 10)+`,"content":"完全无关的内容"}`,
	)

	res := SearchClientSessions("反向代理", "", 30, 20)
	if len(res.Hits) != 2 {
		t.Fatalf("应命中 2 条（用户+助手），得到 %d", len(res.Hits))
	}
	if res.Sessions != 1 {
		t.Fatalf("应覆盖 1 个会话，得到 %d", res.Sessions)
	}
	for _, h := range res.Hits {
		if h.SessionID != "sess-1" || h.Project != "proj-a" {
			t.Fatalf("归因错误: %+v", h)
		}
		if h.Snippet == "" {
			t.Fatalf("片段不应为空: %+v", h)
		}
	}
	if res.Hits[0].Role != "user" || res.Hits[1].Role != "assistant" {
		t.Fatalf("角色顺序不符: %+v", res.Hits)
	}
}

func TestSearchClientSessionsWindowLimitAndProject(t *testing.T) {
	home := t.TempDir()
	old := userHomeDir
	userHomeDir = func() (string, error) { return home, nil }
	defer func() { userHomeDir = old }()

	recent := time.Now().UnixMilli()
	oldTS := time.Now().AddDate(0, 0, -40).UnixMilli()
	writeSessionJSONL(t, home, "proj-a", "recent",
		`{"type":"message","role":"user","timestamp":`+strconv.FormatInt(recent, 10)+`,"content":"关键词 alpha 命中"}`,
	)
	writeSessionJSONL(t, home, "proj-a", "stale",
		`{"type":"message","role":"user","timestamp":`+strconv.FormatInt(oldTS, 10)+`,"content":"关键词 alpha 但时间在窗口外"}`,
	)

	// 窗口外记录被排除
	res := SearchClientSessions("alpha", "", 7, 20)
	if len(res.Hits) != 1 || res.Hits[0].SessionID != "recent" {
		t.Fatalf("窗口应排除过期记录，得到 %+v", res.Hits)
	}
	// 项目过滤
	if got := SearchClientSessions("alpha", "proj-b", 30, 20); len(got.Hits) != 0 {
		t.Fatalf("项目过滤应命中 0 条，得到 %d", len(got.Hits))
	}
	// limit 截断
	res = SearchClientSessions("alpha", "proj-a", 60, 1)
	if len(res.Hits) != 1 || !res.Truncated {
		t.Fatalf("应截断为 1 条并标记 Truncated，得到 %+v", res)
	}
	// 空查询不返回任何结果
	if got := SearchClientSessions("  ", "", 30, 20); len(got.Hits) != 0 {
		t.Fatalf("空查询应返回空集，得到 %d", len(got.Hits))
	}
}

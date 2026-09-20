package core

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// withFakeHome 在临时目录造出 .workbuddy/projects 结构并替换 userHomeDir
func withFakeHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	root := filepath.Join(home, ".workbuddy", "projects", "Users-tester-proj")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	old := userHomeDir
	userHomeDir = func() (string, error) { return home, nil }
	t.Cleanup(func() { userHomeDir = old })
	return root
}

func writeJSONL(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// now = 毫秒时间戳基点（今天）
func fixture(nowMs int64) string {
	return strings.Join([]string{
		// 用户消息：无 usage，应忽略
		`{"timestamp":` + i64(nowMs-60000) + `,"type":"message","role":"user","content":[{"type":"input_text","text":"正文不应被读取"}]}`,
		// assistant：snake 形态 usage，input 含 cacheRead
		`{"timestamp":` + i64(nowMs-30000) + `,"cwd":"/Users/tester/proj","providerData":{"model":"deepseek-v4.1-flash"},"message":{"usage":{"input_tokens":1000,"output_tokens":200,"total_tokens":1200,"cache_read_input_tokens":800}}}`,
		// assistant：camel 形态 usage
		`{"timestamp":` + i64(nowMs-20000) + `,"cwd":"/Users/tester/proj","providerData":{"model":"glm-5"},"message":{"usage":{"inputTokens":500,"outputTokens":100,"cacheReadInputTokens":300}}}`,
		// 无 input 字段：无效记录，跳过
		`{"timestamp":` + i64(nowMs-10000) + `,"message":{"usage":{"output_tokens":9}}}`,
		// 窗口外（10 天前）：排除
		`{"timestamp":` + i64(nowMs-10*86400000) + `,"cwd":"/Users/tester/proj","providerData":{"model":"glm-5"},"message":{"usage":{"input_tokens":999,"output_tokens":1}}}`,
		// 坏行
		`{broken json`,
		// subagents 目录单独放（见测试）
		""}, "\n")
}

func i64(v int64) string { return strconv.FormatInt(v, 10) }

func TestClientTokenStatsScanAndAggregate(t *testing.T) {
	root := withFakeHome(t)
	nowMs := time.Now().UnixMilli()
	writeJSONL(t, filepath.Join(root, "sess-a.jsonl"), fixture(nowMs))

	// subagents 目录应整体跳过（重复父会话上下文）
	sub := filepath.Join(root, "subagents")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	writeJSONL(t, filepath.Join(sub, "child.jsonl"),
		`{"timestamp":`+i64(nowMs-15000)+`,"cwd":"/Users/tester/proj","providerData":{"model":"glm-5"},"message":{"usage":{"input_tokens":8888,"output_tokens":1}}}`)

	// 第二个会话：与 sess-a 完全同指纹的一条（跨文件去重，归属先处理的文件）
	writeJSONL(t, filepath.Join(root, "sess-b.jsonl"),
		`{"timestamp":`+i64(nowMs-30000)+`,"cwd":"/Users/tester/proj","providerData":{"model":"deepseek-v4.1-flash"},"message":{"usage":{"input_tokens":1000,"output_tokens":200,"cache_read_input_tokens":800}}}`)

	rep, err := (&Service{}).ClientTokenStats(7, true)
	if err != nil {
		t.Fatal(err)
	}
	s := rep.Summary
	// 期望：3 条有效记录（sess-a 2 条 + sess-b 去重后 0 条新增），坏行 1
	if s.Records != 2 {
		t.Fatalf("records = %d, want 2", s.Records)
	}
	// total = input + output + cacheWrite；input 含 cacheRead
	// r1: 1000+200；r2: 500+100 → 1800
	if s.TotalTokens != 1800 {
		t.Fatalf("total = %d, want 1800", s.TotalTokens)
	}
	if s.InputTokens != 1500 || s.OutputTokens != 300 {
		t.Fatalf("in=%d out=%d, want 1500/300", s.InputTokens, s.OutputTokens)
	}
	if s.CacheRead != 1100 {
		t.Fatalf("cacheRead = %d, want 1100", s.CacheRead)
	}
	if s.CacheWrite != 0 {
		t.Fatalf("cacheWrite = %d, want 0", s.CacheWrite)
	}
	wantRate := round2(float64(1100) / float64(1500) * 100)
	if s.CacheHitRate != wantRate {
		t.Fatalf("cacheHitRate = %v, want %v", s.CacheHitRate, wantRate)
	}
	if len(rep.Models) != 2 {
		t.Fatalf("models = %d, want 2", len(rep.Models))
	}
	if rep.Models[0].Model != "deepseek-v4.1-flash" || rep.Models[0].TotalTokens != 1200 {
		t.Fatalf("top model = %+v", rep.Models[0])
	}
	if len(rep.Projects) != 1 || rep.Projects[0].Project != "proj" {
		t.Fatalf("projects = %+v, want [proj]", rep.Projects)
	}
	// sess-b 唯一记录与 sess-a 同指纹被全局去重 → 只有 1 个会话条目
	if len(rep.Sessions) != 1 {
		t.Fatalf("sessions = %d, want 1", len(rep.Sessions))
	}
	// 数据源：CN 存在、AI 缺失如实标注
	if len(rep.Sources) != 2 {
		t.Fatalf("sources = %d, want 2", len(rep.Sources))
	}
	if rep.Sources[0].Source != "workbuddy" || rep.Sources[0].Missing {
		t.Fatalf("cn source = %+v", rep.Sources[0])
	}
	if rep.Sources[1].Source != "workbuddy-ai" || !rep.Sources[1].Missing {
		t.Fatalf("ai source = %+v", rep.Sources[1])
	}
	// 坏行计入 ParseErrors
	if rep.Sources[0].ParseErrors != 1 {
		t.Fatalf("parseErrors = %d, want 1", rep.Sources[0].ParseErrors)
	}
	// subagents 未被扫描：FilesScanned = 2
	if rep.Sources[0].FilesScanned != 2 {
		t.Fatalf("filesScanned = %d, want 2", rep.Sources[0].FilesScanned)
	}
	// 逐日只有今天；日活：两条记录同会话同项目 → 会话 1、项目 1
	if len(rep.Daily) != 1 || rep.Daily[0].TotalTokens != 1800 {
		t.Fatalf("daily = %+v", rep.Daily)
	}
	if rep.Daily[0].ActiveSessions != 1 || rep.Daily[0].ActiveProjects != 1 {
		t.Fatalf("active = %d/%d, want 1/1", rep.Daily[0].ActiveSessions, rep.Daily[0].ActiveProjects)
	}
}

func TestClientTokenStatsCacheWriteFallback(t *testing.T) {
	root := withFakeHome(t)
	nowMs := time.Now().UnixMilli()
	// rawUsage 形态（prompt_tokens + cache_creation_input_tokens）
	writeJSONL(t, filepath.Join(root, "s.jsonl"),
		`{"timestamp":`+i64(nowMs-5000)+`,"cwd":"/home/tester/p2","providerData":{"model":"kimi"},"message":{"usage":{"prompt_tokens":300,"completion_tokens":50,"cache_creation_input_tokens":70}}}`)
	rep, err := (&Service{}).ClientTokenStats(1, true)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Summary.InputTokens != 300 || rep.Summary.OutputTokens != 50 || rep.Summary.CacheWrite != 70 {
		t.Fatalf("summary = %+v", rep.Summary)
	}
	// total = 300 + 50 + 70 = 420（input 已含 cacheRead=0）
	if rep.Summary.TotalTokens != 420 {
		t.Fatalf("total = %d, want 420", rep.Summary.TotalTokens)
	}
}

func TestClientTokenStatsDaysClamp(t *testing.T) {
	withFakeHome(t)
	rep, err := (&Service{}).ClientTokenStats(0, true)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Days != 7 {
		t.Fatalf("days = %d, want 7", rep.Days)
	}
	rep, err = (&Service{}).ClientTokenStats(500, true)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Days != 90 {
		t.Fatalf("days = %d, want 90", rep.Days)
	}
}

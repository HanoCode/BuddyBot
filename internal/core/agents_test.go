package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetTOMLTopLevel(t *testing.T) {
	// 空文件：追加
	got := setTOMLTopLevel("", "model", `"gpt-5"`)
	if got != "model = \"gpt-5\"\n" {
		t.Fatalf("空文件追加失败: %q", got)
	}
	// 已有顶层键：原位替换，不碰表内同名键
	src := "model = \"a\"\n" +
		"\n" +
		"[model_providers.x]\n" +
		"model = \"b\"\n"
	got = setTOMLTopLevel(src, "model", `"c"`)
	if !strings.Contains(got, "model = \"c\"\n") {
		t.Fatalf("顶层键未替换: %q", got)
	}
	if !strings.Contains(got, "model = \"b\"\n") {
		t.Fatalf("表内键被误改: %q", got)
	}
	// 已有表：新顶层键插到第一个表头之前
	src2 := "# 注释\n[model_providers.x]\nname = \"n\"\n"
	got = setTOMLTopLevel(src2, "model", `"c"`)
	idxModel := strings.Index(got, "model = \"c\"")
	idxTable := strings.Index(got, "[model_providers.x]")
	if idxModel < 0 || idxTable < 0 || idxModel > idxTable {
		t.Fatalf("新键应插到第一个表头之前: %q", got)
	}
}

func TestReplaceTOMLTable(t *testing.T) {
	src := "model = \"a\"\n" +
		"[providers.old]\n" +
		"type = \"x\"\n" +
		"base_url = \"http://old\"\n" +
		"[models.\"m1\"]\n" +
		"provider = \"old\"\n"
	body := "[providers.workbuddy]\ntype = \"openai\"\n"
	got := replaceTOMLTable(src, "[providers.old]", body)
	if !strings.Contains(got, body) {
		t.Fatalf("新表体未写入: %q", got)
	}
	if strings.Contains(got, "http://old") {
		t.Fatalf("旧表体未清除: %q", got)
	}
	if !strings.Contains(got, "[models.\"m1\"]\nprovider = \"old\"") {
		t.Fatalf("后续表被误删: %q", got)
	}
	// 表不存在：追加
	got = replaceTOMLTable("a = 1\n", "[models.\"m2\"]", "[models.\"m2\"]\nprovider = \"p\"\n")
	if !strings.Contains(got, "[models.\"m2\"]") {
		t.Fatalf("缺表时未追加: %q", got)
	}
}

func TestApplyAgentClaudeCode(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	backupRoot := filepath.Join(home, "backups")

	res, err := ApplyAgent("claude-code", "http://127.0.0.1:7863", "sk-test", []string{"glm-5", "deepseek-v4-flash"}, backupRoot)
	if err != nil {
		t.Fatalf("ApplyAgent 失败: %v", err)
	}
	if len(res.Models) != 2 {
		t.Fatalf("模型列表应原样回传: %v", res.Models)
	}
	data, err := os.ReadFile(claudeCodeSettingsPath())
	if err != nil {
		t.Fatalf("配置未写入: %v", err)
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatalf("写入的不是合法 JSON: %v", err)
	}
	env := root["env"].(map[string]any)
	if env["ANTHROPIC_BASE_URL"] != "http://127.0.0.1:7863" || env["ANTHROPIC_AUTH_TOKEN"] != "sk-test" {
		t.Fatalf("env 字段未写入: %v", env)
	}
	if env["ANTHROPIC_DEFAULT_SONNET_MODEL"] != "glm-5" || env["ANTHROPIC_DEFAULT_OPUS_MODEL"] != "deepseek-v4-flash" {
		t.Fatalf("槽位模型未写入: %v", env)
	}

	// 二次写入：配置已被识别为已接入
	targets := DetectAgents("http://127.0.0.1:7863", "sk-test")
	var found bool
	for _, tg := range targets {
		if tg.ID == "claude-code" {
			found = true
			if !tg.Installed || !tg.Configured {
				t.Fatalf("claude-code 应为已安装+已接入: %+v", tg)
			}
		}
	}
	if !found {
		t.Fatal("探测结果缺少 claude-code")
	}

	// 备份存在且回滚后文件被清掉（首次写入前无旧文件，先造一份再验证回滚）
	if err := os.WriteFile(claudeCodeSettingsPath(), []byte(`{"env":{"KEEP":1}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	res2, err := ApplyAgent("claude-code", "http://127.0.0.1:7863", "sk-test", nil, backupRoot)
	if err != nil {
		t.Fatalf("二次写入失败: %v", err)
	}
	backups := ListAgentBackups("claude-code", backupRoot)
	// 首次写入前无旧文件 → 不落备份目录；只有二次写入（覆盖前）有 1 份
	if len(backups) != 1 || len(backups[0].Files) != 1 {
		t.Fatalf("应恰有 1 份含 1 个文件的备份: %+v", backups)
	}
	n, err := RestoreAgentBackup("claude-code", res2.BackupDir[strings.LastIndex(res2.BackupDir, "/")+1:], backupRoot)
	if err != nil || n != 1 {
		t.Fatalf("回滚失败: n=%d err=%v", n, err)
	}
	kept, _ := os.ReadFile(claudeCodeSettingsPath())
	if string(kept) != `{"env":{"KEEP":1}}` {
		t.Fatalf("回滚后内容不符: %s", kept)
	}
}

func TestApplyAgentCodexAndKimi(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	t.Setenv("KIMI_CODE_HOME", filepath.Join(home, ".kimi-code"))
	backupRoot := filepath.Join(home, "backups")

	if _, err := ApplyAgent("codex", "http://127.0.0.1:7863", "sk-cx", []string{"glm-5"}, backupRoot); err != nil {
		t.Fatalf("codex 接入失败: %v", err)
	}
	cfg := readAgentFile(filepath.Join(home, ".codex", "config.toml"))
	for _, want := range []string{
		`model_provider = "workbuddy"`, `model = "glm-5"`,
		`[model_providers.workbuddy]`, `base_url = "http://127.0.0.1:7863/v1"`, `wire_api = "responses"`,
	} {
		if !strings.Contains(cfg, want) {
			t.Fatalf("codex config 缺少 %q: %s", want, cfg)
		}
	}
	auth := readAgentFile(filepath.Join(home, ".codex", "auth.json"))
	var authRoot map[string]any
	if json.Unmarshal([]byte(auth), &authRoot) != nil || authRoot["OPENAI_API_KEY"] != "sk-cx" {
		t.Fatalf("codex auth.json 未写入: %s", auth)
	}

	if _, err := ApplyAgent("kimi-code", "http://127.0.0.1:7863", "sk-kc", []string{"glm-5", "deepseek-v4-flash"}, backupRoot); err != nil {
		t.Fatalf("kimi-code 接入失败: %v", err)
	}
	kcfg := readAgentFile(filepath.Join(home, ".kimi-code", "config.toml"))
	for _, want := range []string{
		`default_model = "glm-5"`, `[providers.workbuddy]`, `base_url = "http://127.0.0.1:7863/v1"`,
		`[models."glm-5"]`, `provider = "workbuddy"`, `[models."deepseek-v4-flash"]`,
	} {
		if !strings.Contains(kcfg, want) {
			t.Fatalf("kimi config 缺少 %q: %s", want, kcfg)
		}
	}
}

func TestApplyAgentUnsupported(t *testing.T) {
	if _, err := ApplyAgent("dream-skin", "http://x", "k", nil, t.TempDir()); err == nil {
		t.Fatal("不支持的客户端应报错")
	}
}

func TestUpsertModelListEntry(t *testing.T) {
	dir := t.TempDir()

	// 裸数组形态（WorkBuddy 空文件/裸数组）：只追加自家条目
	p1 := filepath.Join(dir, "workbuddy-models.json")
	if err := os.WriteFile(p1, []byte("[]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := upsertModelListEntry(p1, "http://127.0.0.1:7863", "sk-wb", []string{"glm-5", "deepseek-v4-flash"}); err != nil {
		t.Fatalf("裸数组写入失败: %v", err)
	}
	var bare []map[string]any
	if json.Unmarshal(mustRead(t, p1), &bare) != nil || len(bare) != 2 {
		t.Fatalf("裸数组形态应输出 2 条: %s", mustRead(t, p1))
	}
	if bare[0]["url"] != "http://127.0.0.1:7863/v1/chat/completions" || bare[0]["vendor"] != "WorkBuddy" {
		t.Fatalf("条目字段不符: %v", bare[0])
	}

	// 对象形态（CodeBuddy）：保留其它厂商条目，自家条目替换
	p2 := filepath.Join(dir, "codebuddy-models.json")
	existing := `{"models":[` +
		`{"id":"glm-5","vendor":"GLM Coding Plan","apiKey":"keep-me"},` +
		`{"id":"old","vendor":"WorkBuddy","apiKey":"stale"}]}`
	if err := os.WriteFile(p2, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := upsertModelListEntry(p2, "http://127.0.0.1:7863", "sk-cb", []string{"glm-5"}); err != nil {
		t.Fatalf("对象形态写入失败: %v", err)
	}
	var obj struct {
		Models []map[string]any `json:"models"`
	}
	if json.Unmarshal(mustRead(t, p2), &obj) != nil || len(obj.Models) != 2 {
		t.Fatalf("应保留 1 条外部 + 新写 1 条: %s", mustRead(t, p2))
	}
	if obj.Models[0]["apiKey"] != "keep-me" {
		t.Fatalf("外部厂商条目被篡改: %v", obj.Models[0])
	}
	if obj.Models[1]["apiKey"] != "sk-cb" {
		t.Fatalf("自家条目未更新: %v", obj.Models[1])
	}
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

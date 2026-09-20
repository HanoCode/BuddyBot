package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newPluginTestEnv 造一个隔离的家目录，并配置各客户端探测目录存在
func newPluginTestEnv(t *testing.T) (*Service, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	t.Setenv("OPENCODE_CONFIG", filepath.Join(home, ".config", "opencode", "opencode.json"))
	for _, d := range []string{".claude", ".codex", ".config/opencode", ".cursor", ".qwen", ".config/crush"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o755); err != nil {
			t.Fatalf("创建目录失败: %v", err)
		}
	}
	return NewServiceAt(t.TempDir()), home
}

func TestGuardDecision(t *testing.T) {
	allow := []string{"go test", "git status", "ls"}
	cases := []struct {
		cmd  string
		want bool
	}{
		{"go test ./...", true},
		{"go test", true},
		{"git status", true},
		{"ls -la", true},
		{"go testx", false},          // 前缀必须到词边界
		{"npm publish", false},       // 不在白名单
		{"go test; rm -rf /", false}, // 组合符号一律不放行
		{"go test && rm -rf /", false},
		{"go test > out.txt", false},
		{"go test $(date)", false},
		{"ls `whoami`", false},
		{"", false},
		{"ls | wc -l", false},
	}
	for _, c := range cases {
		got, _ := guardDecision(allow, c.cmd)
		if got != c.want {
			t.Errorf("guardDecision(%q) = %v，期望 %v", c.cmd, got, c.want)
		}
	}
}

// TestGuardBlocksDangerousUsage 锁定「必须交回用户确认」的命令：
// 白名单前缀命中不等于这个具体写法安全——参数可以指向任意文件，也可以触发删除/执行。
func TestGuardBlocksDangerousUsage(t *testing.T) {
	allow := DefaultSafetyAllowlist()
	blocked := []string{
		// 参数级破坏性用法
		"find . -name '*.log' -delete",
		"find /tmp -type f -exec rm {} +",
		"find . -execdir sh -c 'echo hi' +",
		"git branch -D main",
		"git push origin main",
		"git reset --hard HEAD~1",
		"git log --output=/tmp/x",
		"git remote add evil http://example.com",
		// 敏感路径（读取类命令的参数是自由的）
		"cat ~/.ssh/id_rsa",
		"cat ~/.buddybot/config.json",
		"grep -r token ~/.config",
		"ls /etc/passwd",
		"cat /dev/zero",
		// 执行项目代码：默认不放进白名单
		"go run ./cmd/evil",
		"go test ./...",
		"npm test",
		"npm run build",
		"make test",
		"pytest",
	}
	allowed := []string{
		"ls -la",
		"cat internal/core/service.go",
		"go build ./...",
		"go vet ./...",
		"grep -rn 'guardDecision' internal/core",
		"find . -name '*.go'",
		"git status",
		"git branch",
		"git branch -a",
		"git remote -v",
		"git log --oneline -5",
	}
	for _, cmd := range blocked {
		if got, _ := guardDecision(allow, cmd); got {
			t.Errorf("不应自动放行: %s", cmd)
		}
	}
	for _, cmd := range allowed {
		if got, _ := guardDecision(allow, cmd); !got {
			t.Errorf("应自动放行: %s", cmd)
		}
	}
	// 用户自行加回执行类命令后应生效（放行与否是用户取舍）
	if got, _ := guardDecision(append(append([]string{}, allow...), "go test"), "go test ./..."); !got {
		t.Error("用户加入白名单后应放行 go test")
	}
}

func TestMarkedBlockRoundTrip(t *testing.T) {
	user := "# 我自己的规则\n\n- 不要删我\n"
	block := pluginMarkerBegin + "\n# 编码规范\n- 手术后只跑必要测试\n" + pluginMarkerEnd

	first := replaceMarkedBlock(user, block)
	if !strings.Contains(first, "不要删我") || !strings.Contains(first, pluginMarkerBegin) {
		t.Fatalf("首次写入应同时保留用户内容与托管块: %s", first)
	}
	// 二次同步：块内替换，不叠加
	block2 := pluginMarkerBegin + "\n# 编码规范 v2\n" + pluginMarkerEnd
	second := replaceMarkedBlock(first, block2)
	if strings.Count(second, pluginMarkerBegin) != 1 || !strings.Contains(second, "v2") {
		t.Fatalf("二次同步应替换块内容: %s", second)
	}
	// 卸载：移除块，用户内容保留
	third := stripMarkedBlock(second)
	if strings.Contains(third, pluginMarkerBegin) || !strings.Contains(third, "不要删我") {
		t.Fatalf("卸载应只移除托管块: %s", third)
	}
	if markedBlock(second) != "\n# 编码规范 v2\n" {
		t.Fatalf("markedBlock 取值不符: %q", markedBlock(second))
	}
}

func TestApplyAndRemoveMCP(t *testing.T) {
	svc, home := newPluginTestEnv(t)

	res, err := svc.ApplyPlugin("mcp", nil)
	if err != nil {
		t.Fatalf("安装 MCP 失败: %v", err)
	}
	if len(res.Files) == 0 {
		t.Fatal("应写入若干客户端配置")
	}
	// Claude Code 用户级配置
	claudeCfg := filepath.Join(home, ".claude.json")
	raw, err := os.ReadFile(claudeCfg)
	if err != nil {
		t.Fatalf("Claude MCP 配置未写入: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("Claude 配置不是合法 JSON: %v", err)
	}
	entry := doc["mcpServers"].(map[string]any)["buddybot"].(map[string]any)
	if entry["command"] == "" || entry["args"].([]any)[0] != "mcp" {
		t.Fatalf("stdio server 条目不符: %v", entry)
	}
	// Codex TOML
	codexCfg := filepath.Join(home, ".codex", "config.toml")
	if body := readAgentFile(codexCfg); !strings.Contains(body, "[mcp_servers.buddybot]") || !strings.Contains(body, `args = ["mcp"]`) {
		t.Fatalf("Codex MCP 配置不符: %s", body)
	}
	// 状态探测应识别为已安装
	st := findPluginStatus(t, svc, "mcp")
	if !st.Installed {
		t.Fatal("状态探测应识别为已安装")
	}

	if _, err := svc.RemovePlugin("mcp", nil); err != nil {
		t.Fatalf("卸载 MCP 失败: %v", err)
	}
	if body := readAgentFile(claudeCfg); strings.Contains(body, "buddybot") {
		t.Fatalf("卸载后 Claude 配置仍残留: %s", body)
	}
	if body := readAgentFile(codexCfg); strings.Contains(body, "mcp_servers.buddybot") {
		t.Fatalf("卸载后 Codex 配置仍残留: %s", body)
	}
}

func TestApplyRulesPreservesUserContentAndRemove(t *testing.T) {
	svc, home := newPluginTestEnv(t)

	claudeRules := filepath.Join(home, ".claude", "CLAUDE.md")
	if err := os.WriteFile(claudeRules, []byte("# 用户自己的规则\n- 保留我\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := svc.GetConfig()
	cfg.Plugins.RulesText = "- 改动前先说明假设\n"
	if err := svc.UpdateConfig(cfg); err != nil {
		t.Fatalf("保存配置失败: %v", err)
	}

	if _, err := svc.ApplyPlugin("rules", nil); err != nil {
		t.Fatalf("同步规则失败: %v", err)
	}
	body := readAgentFile(claudeRules)
	if !strings.Contains(body, "保留我") || !strings.Contains(body, "改动前先说明假设") {
		t.Fatalf("规则文件应同时含用户内容与同步内容: %s", body)
	}
	// 未创建的客户端规则文件也应被写入
	if !strings.Contains(readAgentFile(filepath.Join(home, ".codex", "AGENTS.md")), "改动前先说明假设") {
		t.Fatal("Codex AGENTS.md 未同步")
	}
	// Cursor 需带 frontmatter
	if !strings.Contains(readAgentFile(filepath.Join(home, ".cursor", "rules", "buddybot.mdc")), "alwaysApply: true") {
		t.Fatal("Cursor 规则缺少 frontmatter")
	}
	// 源变更后状态应提示待同步
	cfg.Plugins.RulesText = "- 新规则\n"
	if err := svc.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	st := findPluginStatus(t, svc, "rules")
	for _, tg := range st.Targets {
		if tg.Applied && tg.Note != "源文本已变更，待重新同步" {
			t.Fatalf("变更后状态说明不符：%+v", tg)
		}
	}

	if _, err := svc.RemovePlugin("rules", nil); err != nil {
		t.Fatalf("卸载规则失败: %v", err)
	}
	if body := readAgentFile(claudeRules); strings.Contains(body, pluginMarkerBegin) || !strings.Contains(body, "保留我") {
		t.Fatalf("卸载后应只移除托管块: %s", body)
	}
}

func TestApplyAndRemoveSafetyHook(t *testing.T) {
	svc, home := newPluginTestEnv(t)

	if _, err := svc.ApplyPlugin("hook-safety", nil); err != nil {
		t.Fatalf("安装安全钩子失败: %v", err)
	}
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	var doc map[string]any
	if err := json.Unmarshal([]byte(readAgentFile(settingsPath)), &doc); err != nil {
		t.Fatalf("settings.json 不是合法 JSON: %v", err)
	}
	hooks := doc["hooks"].(map[string]any)
	pre := hooks["PreToolUse"].([]any)
	if len(pre) != 1 {
		t.Fatalf("PreToolUse 应有 1 条，得到 %d", len(pre))
	}
	entry := pre[0].(map[string]any)
	if entry["matcher"] != "Bash" {
		t.Fatalf("matcher 应为 Bash: %v", entry)
	}
	scriptPath := filepath.Join(home, ".claude", "hooks", "buddybot-safety.sh")
	if !pathExists(scriptPath) {
		t.Fatal("钩子脚本未落盘")
	}
	// 脚本注释里不得出现反引号/$( )：那是命令替换语法，挪出注释就会真的执行
	if body := readAgentFile(scriptPath); strings.ContainsAny(body, "`") || strings.Contains(body, "$(") {
		t.Fatalf("钩子脚本含命令替换语法: %s", body)
	}
	if !strings.HasPrefix(readAgentFile(scriptPath), "#!/bin/sh\n") {
		t.Fatalf("钩子脚本缺少 shebang: %s", readAgentFile(scriptPath))
	}
	if info, err := os.Stat(scriptPath); err != nil || info.Mode().Perm()&0o100 == 0 {
		t.Fatal("钩子脚本应可执行")
	}
	// 幂等重装不叠加条目
	if _, err := svc.ApplyPlugin("hook-safety", nil); err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal([]byte(readAgentFile(settingsPath)), &doc)
	if got := len(doc["hooks"].(map[string]any)["PreToolUse"].([]any)); got != 1 {
		t.Fatalf("重装后条目数应仍为 1，得到 %d", got)
	}
	// 用户已有其它 hook 时不得被清掉
	userHook := map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "echo user"}}}
	_ = json.Unmarshal([]byte(readAgentFile(settingsPath)), &doc)
	doc["hooks"].(map[string]any)["Stop"] = []any{userHook}
	out, _ := json.Marshal(doc)
	if err := os.WriteFile(settingsPath, out, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ApplyPlugin("hook-context", nil); err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal([]byte(readAgentFile(settingsPath)), &doc)
	if len(doc["hooks"].(map[string]any)["Stop"].([]any)) != 1 {
		t.Fatal("不应影响用户已有的 Stop 钩子")
	}

	if _, err := svc.RemovePlugin("hook-safety", nil); err != nil {
		t.Fatalf("卸载钩子失败: %v", err)
	}
	_ = json.Unmarshal([]byte(readAgentFile(settingsPath)), &doc)
	if _, ok := doc["hooks"].(map[string]any)["PreToolUse"]; ok {
		t.Fatal("卸载后 PreToolUse 应被清除")
	}
	if pathExists(scriptPath) {
		t.Fatal("卸载后脚本应被删除")
	}
}

func TestSlashCommandsInstallAndRemove(t *testing.T) {
	svc, home := newPluginTestEnv(t)
	SetEmbeddedPrompts([]byte(`[
	  {"id":7,"title":"深度调研：宠物食品行业","category":"deep-research","scene":"顾问","prompt":"请调研宠物食品行业并输出报告"},
	  {"id":9,"title":"周报汇总","category":"report","scene":"","prompt":"汇总本周进展"}
	]`))
	defer SetEmbeddedPrompts(nil)

	res, err := svc.ApplyPlugin("slash", nil)
	if err != nil {
		t.Fatalf("安装命令失败: %v", err)
	}
	if len(res.Files) != 2 {
		t.Fatalf("应安装 2 条命令，得到 %d", len(res.Files))
	}
	body := readAgentFile(filepath.Join(home, ".claude", "commands", "buddybot-7-deep-research.md"))
	if !strings.Contains(body, "description: \"深度调研：宠物食品行业\"") || !strings.Contains(body, "请调研宠物食品行业并输出报告") {
		t.Fatalf("命令文件内容不符: %s", body)
	}
	st := findPluginStatus(t, svc, "slash")
	if !st.Installed || len(st.Targets) != 1 {
		t.Fatalf("状态探测不符: %+v", st)
	}
	if !strings.Contains(st.Targets[0].Note, "2 / 2") {
		t.Fatalf("状态应汇总安装条数: %+v", st.Targets[0])
	}
	list := svc.SlashCommandList()
	if len(list) != 2 || !list[0].Installed || list[0].Name != "buddybot-7-deep-research" {
		t.Fatalf("指令清单不符: %+v", list)
	}
	// 按编号卸载
	if _, err := svc.RemovePlugin("slash", []int{7}); err != nil {
		t.Fatalf("卸载失败: %v", err)
	}
	if pathExists(filepath.Join(home, ".claude", "commands", "buddybot-7-deep-research.md")) {
		t.Fatal("编号 7 应被卸载")
	}
	if !pathExists(filepath.Join(home, ".claude", "commands", "buddybot-9-report.md")) {
		t.Fatal("编号 9 不应被卸载")
	}
}

func TestDropTOMLTableAndTomlQuote(t *testing.T) {
	text := "model = \"x\"\n\n[mcp_servers.buddybot]\ncommand = \"C:\\\\app\\\\b.exe\"\nargs = [\"mcp\"]\n\n[other]\nk = \"v\"\n"
	out := dropTOMLTable(text, "[mcp_servers.buddybot]")
	if strings.Contains(out, "mcp_servers.buddybot") || !strings.Contains(out, "[other]") || !strings.Contains(out, "model = \"x\"") {
		t.Fatalf("删表结果不符: %s", out)
	}
	if tomlQuote(`C:\app\b.exe`) != `"C:\\app\\b.exe"` {
		t.Fatalf("TOML 路径转义不符: %s", tomlQuote(`C:\app\b.exe`))
	}
}

func findPluginStatus(t *testing.T, svc *Service, id string) PluginStatus {
	t.Helper()
	for _, st := range svc.PluginStatuses() {
		if st.ID == id {
			return st
		}
	}
	t.Fatalf("未找到插件状态: %s", id)
	return PluginStatus{}
}

// TestPluginConfigEmptySemantics 锁定「缺键保留默认 / 显式清空即生效」的三种情形
// （这条语义此前注释与实现相反：清空规则文本会被静默还原成默认模板）
func TestPluginConfigEmptySemantics(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")

	// 1) 缺 plugins 键 → 保留默认
	if err := os.WriteFile(cfgPath, []byte(`{"listen":":7863"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	svc := NewServiceAt(dir)
	if svc.GetConfig().Plugins.RulesText == "" || len(svc.GetConfig().Plugins.SafetyAllowlist) == 0 {
		t.Fatal("缺 plugins 键时应保留默认值")
	}

	// 2) 显式清空 → 原样生效（不再回落默认）
	if err := os.WriteFile(cfgPath, []byte(`{"plugins":{"rules_text":"","safety_allowlist":[]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	svc2 := NewServiceAt(dir)
	if got := svc2.GetConfig().Plugins.RulesText; got != "" {
		t.Fatalf("显式清空的规则文本被回落成默认: %q", got)
	}
	if got := svc2.GetConfig().Plugins.SafetyAllowlist; len(got) != 0 {
		t.Fatalf("显式清空的白名单被回落成默认: %v", got)
	}
	// 规则文本为空时同步应给出明确错误，而不是悄悄用默认模板
	if _, err := svc2.ApplyPlugin("rules", nil); err == nil {
		t.Fatal("规则文本为空时应报错")
	}

	// 3) 保存 → 重载语义一致（白名单条目去空白）
	if err := svc2.SavePluginSettings(PluginSettings{RulesText: "- x\n", SafetyAllowlist: []string{" ls ", ""}}); err != nil {
		t.Fatal(err)
	}
	svc3 := NewServiceAt(dir)
	if got := svc3.GetConfig().Plugins; got.RulesText != "- x\n" || len(got.SafetyAllowlist) != 1 || got.SafetyAllowlist[0] != "ls" {
		t.Fatalf("保存后重载不符: %+v", got)
	}

	// 4) 只读模式拒绝保存
	cfg4 := svc3.GetConfig()
	cfg4.Security.ReadOnly = true
	if err := svc3.UpdateConfig(cfg4); err != nil {
		t.Fatal(err)
	}
	if err := svc3.SavePluginSettings(PluginSettings{RulesText: "y"}); err == nil {
		t.Fatal("只读模式应拒绝保存插件配置")
	}
}

// TestWriteAgentJSONPrecisionAndMode 重写客户端配置不得改变数值精度与文件权限
func TestWriteAgentJSONPrecisionAndMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cfg.json")
	orig := "{\n  \"ts\": 1758346795123456789,\n  \"cost\": 0.123456789012345678,\n  \"mcpServers\": {}\n}\n"
	if err := os.WriteFile(path, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeAgentJSON(path, func(root map[string]any) {
		root["mcpServers"] = map[string]any{"buddybot": map[string]any{"command": "/x"}}
	}); err != nil {
		t.Fatal(err)
	}
	body := readAgentFile(path)
	if !strings.Contains(body, "1758346795123456789") {
		t.Fatalf("大整数被 float64 抹平: %s", body)
	}
	if !strings.Contains(body, "0.123456789012345678") {
		t.Fatalf("高精度小数被抹平: %s", body)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("原有权限被改动: %v", info.Mode().Perm())
	}
	fresh := filepath.Join(dir, "new.json")
	if err := writeAgentJSON(fresh, func(root map[string]any) { root["a"] = 1 }); err != nil {
		t.Fatal(err)
	}
	if i2, err := os.Stat(fresh); err != nil || i2.Mode().Perm() != 0o600 {
		t.Fatalf("新建文件应为 0600: %v", i2.Mode().Perm())
	}
}

// TestTOMLHeaderDetection 表头判定：带尾随注释的表头要能识别（否则会追加重复表 → TOML 非法），
// 多行数组里的 [1, 2] 不能当表头
func TestTOMLHeaderDetection(t *testing.T) {
	text := "args = [\n  \"a\",\n  \"b\",\n]\n\n[mcp_servers.buddybot] # 注释\ncommand = \"x\"\n\n[other]\nk = 1\n"
	out := replaceTOMLTable(text, "[mcp_servers.buddybot]", "[mcp_servers.buddybot]\ncommand = \"y\"\n")
	if strings.Count(out, "[mcp_servers.buddybot]") != 1 || !strings.Contains(out, `command = "y"`) {
		t.Fatalf("带注释表头替换结果不符: %s", out)
	}
	if !strings.Contains(out, "[other]") || !strings.Contains(out, "k = 1") || !strings.Contains(out, "args = [") {
		t.Fatalf("替换不应影响其它内容: %s", out)
	}
	multi := "v = [\n[1, 2],\n[3, 4],\n]\n"
	if dropped := dropTOMLTable(multi, "[mcp_servers.buddybot]"); dropped != multi {
		t.Fatalf("多行数组被误判为表头并删除: %q", dropped)
	}
}

// TestMCPStatusParsing 状态探测必须解析配置结构，而不是「文件里出现 buddybot 字样」；
// 并且要能发现「条目还在、命令路径已失效」
func TestMCPStatusParsing(t *testing.T) {
	svc, home := newPluginTestEnv(t)
	specOf := func(id string) pluginSpec {
		for _, sp := range pluginSpecs() {
			if sp.id == id {
				return sp
			}
		}
		t.Fatalf("不存在的客户端: %s", id)
		return pluginSpec{}
	}
	// 无关位置出现 buddybot 字样 → 不得判为已接入
	claude := filepath.Join(home, ".claude.json")
	if err := os.WriteFile(claude, []byte(`{"projects":{"/x/buddybot/y":{}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tg := range findPluginStatus(t, svc, "mcp").Targets {
		if tg.ID == "claude-code" && tg.Applied {
			t.Fatal("无关位置的 buddybot 字样不应判为已接入")
		}
	}

	if _, err := svc.ApplyPlugin("mcp", nil); err != nil {
		t.Fatalf("接入失败: %v", err)
	}
	exe := selfExecutable()
	for _, id := range []string{"claude-code", "codex"} {
		cmd, ok := mcpStoredCommand(specOf(id))
		if !ok || cmd != exe {
			t.Fatalf("%s 应能解析出当前可执行文件: %q ok=%v", id, cmd, ok)
		}
	}

	// 命令路径失效 → 状态里必须提示重写
	raw := strings.ReplaceAll(readAgentFile(claude), exe, "/old/path/BuddyBot")
	if err := os.WriteFile(claude, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tg := range findPluginStatus(t, svc, "mcp").Targets {
		if tg.ID == "claude-code" && tg.Note != "命令路径已失效，请重新写入" {
			t.Fatalf("应提示命令路径失效: %+v", tg)
		}
	}
}

// TestNotifyThrottle Stop 钩子按会话节流：同一会话窗口内只推一次
func TestNotifyThrottle(t *testing.T) {
	t.Setenv("BUDDYBOT_HOME", t.TempDir())
	if !notifyAllowed("s1") {
		t.Fatal("首次应允许推送")
	}
	if notifyAllowed("s1") {
		t.Fatal("节流窗口内不应重复推送")
	}
	if !notifyAllowed("s2") {
		t.Fatal("不同会话不应互相节流")
	}
	if _, err := os.Stat(notifyStateFile()); err != nil {
		t.Fatalf("节流状态应落盘: %v", err)
	}
}

// TestRemoveRulesUnpairedMarker 标记不成对时不得假装卸载成功
func TestRemoveRulesUnpairedMarker(t *testing.T) {
	svc, home := newPluginTestEnv(t)
	path := filepath.Join(home, ".claude", "CLAUDE.md")
	broken := "# 用户规则\n" + pluginMarkerBegin + "\n# 半截块\n" // 缺结束标记
	if err := os.WriteFile(path, []byte(broken), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RemovePlugin("rules", nil); err == nil {
		t.Fatal("标记不成对时应报错，而不是静默返回成功")
	}
	if readAgentFile(path) != broken {
		t.Fatal("标记不成对时不应改动文件")
	}
}

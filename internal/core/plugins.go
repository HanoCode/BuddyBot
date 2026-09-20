package core

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ============================================================
// 插件中心：把 BuddyBot 的本机能力以「辅助插件」形式接入编程智能体。
//
// 定位：只做「辅助 agent 使用」的扩展，不驱动/编排 agent——
//   mcp          本机数据（账号池/单价表/会话日志）作为 MCP 工具暴露
//   rules        编码规范一源多写（CLAUDE.md / AGENTS.md / .cursor/rules）
//   hook-safety  安全命令自动放行（PreToolUse，减少确认弹窗）
//   hook-context 会话开始注入本机资源摘要（SessionStart）
//   hook-notify  任务结束推送（Stop，复用 Bark/PushPlus 通道）
//   slash        提效指令库安装为客户端 slash 命令
//
// 所有写入都走既有的备份/回滚机制（backupAgentFiles / RestoreAgentBackup），
// 状态一律由「磁盘上的真实内容」推导，不做独立状态记录，避免配置与文件漂移。
// ============================================================

// PluginsConfig 插件中心配置（只存用户意图，不存安装状态）
type PluginsConfig struct {
	// RulesText 编码规范源文本：同步到各客户端规则文件（标记块内），
	// 用户维护一份，多端一致。
	RulesText string `json:"rules_text"`
	// SafetyAllowlist 安全命令放行白名单（命令前缀）。命中且不含 shell 组合符号时，
	// PreToolUse 钩子直接放行，减少 agent 干活时的确认弹窗。
	SafetyAllowlist []string `json:"safety_allowlist"`
}

// DefaultSafetyAllowlist 默认放行前缀：只读检视 + 不执行项目代码的构建/静态检查。
//
// 刻意不含 go test / go run / npm test|run|build / make / pytest / cargo test ——
// 这些会执行项目里的代码或项目自定义脚本，等于把「免确认执行任意代码」交给智能体；
// 需要时在插件中心的白名单里自行加回（放行 = 跳过确认，属于用户自己的取舍）。
func DefaultSafetyAllowlist() []string {
	return []string{
		"go build", "go vet", "cargo check", "cargo build",
		"git status", "git diff", "git log", "git show", "git branch", "git remote",
		"git blame", "git grep", "git rev-parse", "git ls-files",
		"ls", "cat", "head", "tail", "wc", "grep", "rg", "find", "pwd", "which",
		"stat", "file", "tree", "du", "echo",
	}
}

// DefaultRulesText 规则源默认模板（新装即有内容，用户可改）
func DefaultRulesText() string {
	return strings.Join([]string{
		"# 编码规范",
		"",
		"- 改动要外科手术式：只碰必须碰的代码，不做顺手重构。",
		"- 提交前必须跑通构建与测试，失败就如实报告，不要静默跳过。",
		"- 不确定的地方先提问，不要猜测后默默实现。",
		"",
	}, "\n")
}

// pluginMarkerBegin / End 规则文件里的托管块标记：
// 只替换块内内容，用户自己的规则原样保留，删除插件即移除整块。
const (
	pluginMarkerBegin = "<!-- buddybot:rules:begin -->"
	pluginMarkerEnd   = "<!-- buddybot:rules:end -->"
)

// pluginMCPServerName MCP 配置里的 server 名（客户端侧显示名）
const pluginMCPServerName = "buddybot"

// pluginSpec 一个客户端的插件接入点定义
type pluginSpec struct {
	id          string
	name        string
	detectDir   string // 安装探测目录
	mcpPath     string // MCP 配置文件（"" = 该客户端不支持）
	mcpKind     string // claude-json / codex-toml / opencode-json / gemini-json / crush-json
	rulesPath   string // 规则文件（"" = 不支持）
	rulesKind   string // md / mdc
	hookSetting string // hooks 所在配置文件（目前仅 Claude Code）
}

// pluginSpecs 支持插件接入的客户端。
// 未列出的客户端不是遗漏：只写「格式已确认」的接入点，不猜配置结构。
func pluginSpecs() []pluginSpec {
	return []pluginSpec{
		{
			id: "claude-code", name: "Claude Code",
			detectDir:   agentEnvDir("CLAUDE_CONFIG_DIR", ".claude"),
			mcpPath:     filepath.Join(agentHome(), ".claude.json"),
			mcpKind:     "claude-json",
			rulesPath:   filepath.Join(agentEnvDir("CLAUDE_CONFIG_DIR", ".claude"), "CLAUDE.md"),
			rulesKind:   "md",
			hookSetting: claudeCodeSettingsPath(),
		},
		{
			id: "codex", name: "Codex CLI",
			detectDir: agentEnvDir("CODEX_HOME", ".codex"),
			mcpPath:   first(codexConfigPath()),
			mcpKind:   "codex-toml",
			rulesPath: filepath.Join(agentEnvDir("CODEX_HOME", ".codex"), "AGENTS.md"),
			rulesKind: "md",
		},
		{
			id: "opencode", name: "OpenCode",
			detectDir: filepath.Dir(opencodeConfigPath()),
			mcpPath:   opencodeConfigPath(),
			mcpKind:   "opencode-json",
			rulesPath: filepath.Join(filepath.Dir(opencodeConfigPath()), "AGENTS.md"),
			rulesKind: "md",
		},
		{
			id: "cursor", name: "Cursor",
			detectDir: agentHome() + "/.cursor",
			rulesPath: filepath.Join(agentHome(), ".cursor", "rules", "buddybot.mdc"),
			rulesKind: "mdc",
		},
		{
			id: "qwen", name: "Qwen Code",
			detectDir: filepath.Dir(qwenSettingsPath()),
			mcpPath:   qwenSettingsPath(),
			mcpKind:   "gemini-json",
		},
		{
			id: "crush", name: "Crush",
			detectDir: filepath.Dir(crushConfigPath()),
			mcpPath:   crushConfigPath(),
			mcpKind:   "crush-json",
		},
	}
}

// PluginTargetStatus 单个客户端上的接入状态
type PluginTargetStatus struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Path      string `json:"path"`
	Installed bool   `json:"installed"` // 客户端本身已安装（目录存在）
	Applied   bool   `json:"applied"`   // 插件已写入该客户端
	Note      string `json:"note,omitempty"`
}

// PluginStatus 单项插件总览
type PluginStatus struct {
	ID          string               `json:"id"`
	Name        string               `json:"name"`
	Description string               `json:"description"`
	Installed   bool                 `json:"installed"` // 任一目标已写入
	Targets     []PluginTargetStatus `json:"targets"`
	Detail      string               `json:"detail,omitempty"` // 补充说明（如已安装的命令数）
}

// PluginApplyResult 安装/卸载结果（复用智能体接入的备份语义）
type PluginApplyResult struct {
	Plugin    string   `json:"plugin"`
	BackupDir string   `json:"backupDir"`
	Files     []string `json:"files"`
	Removed   []string `json:"removed"`
	Detail    string   `json:"detail,omitempty"`
}

// selfExecutable 当前可执行文件路径：写的 hook 脚本与 MCP 配置都指向它。
// macOS 打包后即 .app/Contents/MacOS/BuddyBot，指针稳定。
func selfExecutable() string {
	p, err := os.Executable()
	if err != nil {
		return ""
	}
	if real, err := filepath.EvalSymlinks(p); err == nil {
		p = real
	}
	return p
}

// PluginSettings 用户可编辑的插件配置（对应 Config.Plugins，供前端读写）
type PluginSettings struct {
	RulesText       string   `json:"rulesText"`
	SafetyAllowlist []string `json:"safetyAllowlist"`
}

// PluginSettingsNow 当前插件配置（规则文本 + 放行白名单）
func (s *Service) PluginSettingsNow() PluginSettings {
	cfg := s.GetConfig()
	return PluginSettings{RulesText: cfg.Plugins.RulesText, SafetyAllowlist: cfg.Plugins.SafetyAllowlist}
}

// SavePluginSettings 保存插件配置：只改 plugins 段并落盘，不触发网关重启
// （SaveConfigWith 不做热重启，插件配置与网关运行无关）。
func (s *Service) SavePluginSettings(in PluginSettings) error {
	if s.ReadOnly() {
		return fmt.Errorf("当前为只读模式，插件配置未保存")
	}
	cfg := s.GetConfig()
	cfg.Plugins.RulesText = in.RulesText
	// 白名单去掉空行；结果为空的非 nil 切片会被序列化成 []，用户清空即生效
	allow := make([]string, 0, len(in.SafetyAllowlist))
	for _, a := range in.SafetyAllowlist {
		if t := strings.TrimSpace(a); t != "" {
			allow = append(allow, t)
		}
	}
	cfg.Plugins.SafetyAllowlist = allow
	return s.SaveConfigWith(cfg)
}

// PluginBackup 一条插件写入产生的备份（用于页面上的回滚入口）
type PluginBackup struct {
	Target    string   `json:"target"`    // 备份目录名，如 plugin-rules-claude-code
	Plugin    string   `json:"plugin"`    // 由 target 解析出的插件 ID
	ID        string   `json:"id"`        // 备份批次（目录名即时间戳）
	Files     []string `json:"files"`     // 该批次包含的原始文件名
	CreatedAt string   `json:"createdAt"` // = ID（20260102-150405 形式）
}

// ListPluginBackups 列出插件中心产生的备份（新→旧）。
// 只列有内容的批次：首次生成（目标文件原本不存在）没有旧文件可回滚。
func (s *Service) ListPluginBackups() []PluginBackup {
	root := s.AgentBackupRoot()
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []PluginBackup
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "plugin-") {
			continue
		}
		target := e.Name()
		for _, b := range ListAgentBackups(target, root) {
			if len(b.Files) == 0 {
				continue
			}
			out = append(out, PluginBackup{
				Target: target, Plugin: pluginIDOfTarget(target),
				ID: b.ID, Files: b.Files, CreatedAt: b.ID,
			})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	return out
}

// RestorePluginBackup 回滚一条插件备份
func (s *Service) RestorePluginBackup(target, backupID string) (int, error) {
	if s.ReadOnly() {
		return 0, fmt.Errorf("当前为只读模式，回滚被拒绝")
	}
	return RestoreAgentBackup(target, backupID, s.AgentBackupRoot())
}

// pluginIDOfTarget 备份目录名 → 插件 ID（plugin-mcp-claude-code → mcp）
func pluginIDOfTarget(target string) string {
	rest := strings.TrimPrefix(target, "plugin-")
	for _, id := range []string{"hook-safety", "hook-context", "hook-notify", "mcp", "rules"} {
		if rest == id || strings.HasPrefix(rest, id+"-") {
			return id
		}
	}
	return rest
}

// ---------- 状态探测 ----------

// PluginStatuses 汇总所有插件的接入状态（纯读盘推导，不做缓存）
func (s *Service) PluginStatuses() []PluginStatus {
	cfg := s.GetConfig()
	specs := pluginSpecs()
	return []PluginStatus{
		mcpPluginStatus(specs),
		rulesPluginStatus(specs, cfg.Plugins.RulesText),
		hookPluginStatus("hook-safety", "安全命令自动放行",
			"PreToolUse 钩子：白名单内的只读类命令直接放行（不触碰敏感路径、不带删除/执行类参数），减少确认弹窗",
			specs, hookPath("safety")),
		hookPluginStatus("hook-context", "会话上下文注入",
			"SessionStart 钩子：会话开始注入账号池与今日消耗摘要，让智能体感知本机资源",
			specs, hookPath("context")),
		hookPluginStatus("hook-notify", "任务完成推送",
			"Stop 钩子：整轮任务结束推送完成/失败摘要（走已配置的 Bark / PushPlus 通道）",
			specs, hookPath("notify")),
		slashPluginStatus(),
	}
}

// mcpPluginStatus MCP 接入状态：解析各客户端配置里的 mcpServers / mcp 段来判定。
// 不用「文件里出现过 buddybot 字样」——那既会把路径/历史里的同名文字误判成已接入，
// 也发现不了「条目还在、但命令路径已失效」（应用被移动过）。
func mcpPluginStatus(specs []pluginSpec) PluginStatus {
	st := PluginStatus{
		ID: "mcp", Name: "MCP 工具接入",
		Description: "把本机数据暴露为 MCP 工具：buddybot_quota（账号池余额）/ buddybot_estimate_cost（费用估算）/ buddybot_search_sessions（历史会话检索）/ buddybot_search_code（代码检索）",
	}
	exe := selfExecutable()
	for _, sp := range specs {
		t := PluginTargetStatus{ID: sp.id, Name: sp.name, Path: sp.mcpPath}
		if sp.mcpPath == "" {
			t.Note = "该客户端不支持 MCP 配置写入"
			st.Targets = append(st.Targets, t)
			continue
		}
		t.Installed = pathExists(sp.detectDir)
		cmd, applied := mcpStoredCommand(sp)
		t.Applied = applied
		switch {
		case !t.Installed:
			t.Note = "未检测到安装"
		case applied && exe != "" && cmd != exe:
			t.Note = "命令路径已失效，请重新写入"
		}
		if t.Applied {
			st.Installed = true
		}
		st.Targets = append(st.Targets, t)
	}
	return st
}

// mcpStoredCommand 该客户端配置里 buddybot 条目记录的命令（未写入时 ok=false）
func mcpStoredCommand(sp pluginSpec) (string, bool) {
	raw, err := os.ReadFile(sp.mcpPath)
	if err != nil {
		return "", false
	}
	switch sp.mcpKind {
	case "claude-json", "gemini-json":
		var doc map[string]json.RawMessage
		if json.Unmarshal(raw, &doc) != nil {
			return "", false
		}
		var servers map[string]struct {
			Command string `json:"command"`
		}
		if json.Unmarshal(doc["mcpServers"], &servers) != nil {
			return "", false
		}
		e, ok := servers[pluginMCPServerName]
		return e.Command, ok
	case "opencode-json", "crush-json":
		var doc map[string]json.RawMessage
		if json.Unmarshal(raw, &doc) != nil {
			return "", false
		}
		var mcp map[string]struct {
			Command json.RawMessage `json:"command"`
		}
		if json.Unmarshal(doc["mcp"], &mcp) != nil {
			return "", false
		}
		e, ok := mcp[pluginMCPServerName]
		if !ok {
			return "", false
		}
		// opencode 的 command 是数组（[可执行文件, "mcp"]），crush 是字符串
		var one string
		if json.Unmarshal(e.Command, &one) == nil {
			return one, true
		}
		var many []string
		if json.Unmarshal(e.Command, &many) == nil && len(many) > 0 {
			return many[0], true
		}
		return "", true
	case "codex-toml":
		return tomlTableCommand(string(raw), "[mcp_servers."+pluginMCPServerName+"]")
	}
	return "", false
}

// tomlTableCommand 取 TOML 表内 command 键的值（表不存在时 ok=false）
func tomlTableCommand(text, header string) (string, bool) {
	inTable := false
	for _, l := range strings.Split(text, "\n") {
		if h := tomlHeaderOf(l); h != "" {
			inTable = h == header
			continue
		}
		if !inTable {
			continue
		}
		k, v, ok := strings.Cut(strings.TrimSpace(l), "=")
		if !ok || strings.TrimSpace(k) != "command" {
			continue
		}
		value := strings.TrimSpace(v)
		var got string // tomlQuote 只转义反斜杠，按 JSON 字符串解即可
		if json.Unmarshal([]byte(value), &got) == nil {
			return got, true
		}
		return value, true
	}
	return "", false
}

func rulesPluginStatus(specs []pluginSpec, rulesText string) PluginStatus {
	st := PluginStatus{
		ID: "rules", Name: "规则一源多写",
		Description: "维护一份编码规范，同步写入各客户端的规则文件（托管块内，不影响你自己的规则）",
	}
	src := strings.TrimSpace(rulesText)
	for _, sp := range specs {
		if sp.rulesPath == "" {
			continue
		}
		t := PluginTargetStatus{ID: sp.id, Name: sp.name, Path: sp.rulesPath}
		t.Installed = pathExists(sp.detectDir)
		if !t.Installed {
			t.Note = "未检测到安装"
		}
		if raw, err := os.ReadFile(sp.rulesPath); err == nil {
			body := string(raw)
			t.Applied = strings.Contains(body, pluginMarkerBegin)
			if t.Applied {
				// 已写入但与当前源不一致 → 提示待同步（诚实反映差异）
				t.Note = rulesNote(body, src)
			}
		}
		if t.Applied {
			st.Installed = true
		}
		st.Targets = append(st.Targets, t)
	}
	return st
}

// rulesNote 已写入块的同步状态说明
func rulesNote(fileBody, src string) string {
	blk := markedBlock(fileBody)
	if strings.TrimSpace(blk) == strings.TrimSpace(src) {
		return "已同步"
	}
	return "源文本已变更，待重新同步"
}

func hookPluginStatus(id, name, desc string, specs []pluginSpec, scriptPath string) PluginStatus {
	st := PluginStatus{ID: id, Name: name, Description: desc}
	for _, sp := range specs {
		if sp.hookSetting == "" {
			continue
		}
		t := PluginTargetStatus{ID: sp.id, Name: sp.name, Path: sp.hookSetting}
		t.Installed = pathExists(sp.detectDir)
		if raw, err := os.ReadFile(sp.hookSetting); err == nil {
			t.Applied = strings.Contains(string(raw), filepath.Base(scriptPath))
		}
		if t.Applied {
			st.Installed = true
		}
		st.Targets = append(st.Targets, t)
	}
	if pathExists(scriptPath) && len(st.Targets) > 0 {
		st.Targets[0].Note = "脚本 " + scriptPath
	}
	return st
}

// ---------- 安装 ----------

// ApplyPlugin 安装/更新一个插件（写入前自动备份目标文件）。
// ids 仅对 slash 插件有意义（为空 = 全部指令）。
func (s *Service) ApplyPlugin(id string, ids []int) (*PluginApplyResult, error) {
	if err := s.ensureWritable(); err != nil {
		return nil, err
	}
	switch id {
	case "mcp":
		return s.applyMCP()
	case "rules":
		return s.applyRules()
	case "hook-safety", "hook-context", "hook-notify":
		return s.applyHook(id)
	case "slash":
		return s.ApplySlashCommands(ids)
	default:
		return nil, fmt.Errorf("未知插件: %s", id)
	}
}

// RemovePlugin 卸载插件：删除托管块/条目，用户原有内容保持不变
func (s *Service) RemovePlugin(id string, ids []int) (*PluginApplyResult, error) {
	if err := s.ensureWritable(); err != nil {
		return nil, err
	}
	switch id {
	case "mcp":
		return s.removeMCP()
	case "rules":
		return s.removeRules()
	case "hook-safety", "hook-context", "hook-notify":
		return s.removeHook(id)
	case "slash":
		return s.RemoveSlashCommands(ids)
	default:
		return nil, fmt.Errorf("未知插件: %s", id)
	}
}

// ensureWritable 只读模式下拒绝一切插件变更（与网关管理侧同一红线）
func (s *Service) ensureWritable() error {
	if s.ReadOnly() {
		return fmt.Errorf("当前为只读模式，插件变更被拒绝")
	}
	return nil
}

// ---------- 部分失败回滚 ----------

// pluginWrite 一次插件写入的目标记录（用于部分失败时回滚）
type pluginWrite struct {
	target   string // 备份目标名（plugin-mcp-claude-code 等）
	path     string // 被写入的文件
	backupID string // 备份批次；"" = 该文件原本不存在（回滚时应删除）
}

// pluginBackupID 从 backupAgentFiles 返回的目录取批次 ID（无旧文件时返回 ""）
func pluginBackupID(backupDir string, existed bool) string {
	if !existed || backupDir == "" {
		return ""
	}
	return filepath.Base(backupDir)
}

// rollbackPluginWrites 回滚已完成的写入：有备份的恢复备份，新建的删除；返回成功回滚的文件
func rollbackPluginWrites(writes []pluginWrite, root string) []string {
	var undone []string
	for _, w := range writes {
		if w.backupID != "" {
			if _, err := RestoreAgentBackup(w.target, w.backupID, root); err == nil {
				undone = append(undone, w.path)
			}
			continue
		}
		if err := os.Remove(w.path); err == nil {
			undone = append(undone, w.path)
		}
	}
	return undone
}

// pluginWriteFail 组装「部分失败」错误：把回滚结果写进错误里，
// 避免用户以为「报错了但前面写的没事」——那只是这次刚好没回滚成功。
func pluginWriteFail(writeErr error, undone []string) error {
	if len(undone) == 0 {
		return writeErr
	}
	return fmt.Errorf("%w（已回滚先前写入的 %d 个文件）", writeErr, len(undone))
}

// applyMCP 接入 MCP：逐个客户端写入，任一失败即回滚已写入的客户端
func (s *Service) applyMCP() (*PluginApplyResult, error) {
	exe := selfExecutable()
	if exe == "" {
		return nil, fmt.Errorf("无法定位 BuddyBot 可执行文件，MCP 配置未写入")
	}
	root := s.AgentBackupRoot()
	res := &PluginApplyResult{Plugin: "mcp"}
	var writes []pluginWrite
	for _, sp := range pluginSpecs() {
		if sp.mcpPath == "" || !pathExists(sp.detectDir) {
			continue
		}
		existed := pathExists(sp.mcpPath)
		target := "plugin-mcp-" + sp.id
		backupDir, err := backupAgentFiles(target, []string{sp.mcpPath}, root)
		if err != nil {
			return nil, pluginWriteFail(err, rollbackPluginWrites(writes, root))
		}
		res.BackupDir = backupDir
		if err := writeMCPEntry(sp, exe); err != nil {
			return nil, pluginWriteFail(
				fmt.Errorf("写入 %s 的 MCP 配置失败: %w", sp.name, err),
				rollbackPluginWrites(writes, root))
		}
		writes = append(writes, pluginWrite{target: target, path: sp.mcpPath, backupID: pluginBackupID(backupDir, existed)})
		res.Files = append(res.Files, sp.mcpPath)
	}
	if len(res.Files) == 0 {
		return nil, fmt.Errorf("未检测到支持 MCP 的客户端，未做任何写入")
	}
	res.Detail = fmt.Sprintf("已接入 %d 个客户端，MCP 命令：%s mcp", len(res.Files), exe)
	return res, nil
}

// writeMCPEntry 按客户端各自的 schema 写入 stdio server 条目
func writeMCPEntry(sp pluginSpec, exe string) error {
	args := []string{"mcp"}
	switch sp.mcpKind {
	case "claude-json", "gemini-json":
		// Claude Code 用户级配置与 Gemini CLI 系（Qwen Code）同为 mcpServers 映射
		return writeAgentJSON(sp.mcpPath, func(root map[string]any) {
			servers, _ := root["mcpServers"].(map[string]any)
			if servers == nil {
				servers = map[string]any{}
			}
			servers[pluginMCPServerName] = map[string]any{
				"type":    "stdio",
				"command": exe,
				"args":    args,
			}
			root["mcpServers"] = servers
		})
	case "codex-toml":
		existing := readAgentFile(sp.mcpPath)
		table := "[mcp_servers." + pluginMCPServerName + "]"
		body := table + "\ncommand = " + tomlQuote(exe) + "\nargs = [\"mcp\"]\n"
		return atomicWriteFile(sp.mcpPath, replaceTOMLTable(existing, table, body))
	case "opencode-json":
		return writeAgentJSON(sp.mcpPath, func(root map[string]any) {
			mcp, _ := root["mcp"].(map[string]any)
			if mcp == nil {
				mcp = map[string]any{}
			}
			mcp[pluginMCPServerName] = map[string]any{
				"type":    "local",
				"command": append([]string{exe}, args...),
				"enabled": true,
			}
			root["mcp"] = mcp
		})
	case "crush-json":
		return writeAgentJSON(sp.mcpPath, func(root map[string]any) {
			mcp, _ := root["mcp"].(map[string]any)
			if mcp == nil {
				mcp = map[string]any{}
			}
			mcp[pluginMCPServerName] = map[string]any{
				"type":    "stdio",
				"command": exe,
				"args":    args,
			}
			root["mcp"] = mcp
		})
	}
	return fmt.Errorf("未支持的 MCP 配置格式: %s", sp.mcpKind)
}

func (s *Service) removeMCP() (*PluginApplyResult, error) {
	res := &PluginApplyResult{Plugin: "mcp"}
	for _, sp := range pluginSpecs() {
		if sp.mcpPath == "" || !pathExists(sp.mcpPath) {
			continue
		}
		raw, err := os.ReadFile(sp.mcpPath)
		if err != nil || !strings.Contains(string(raw), pluginMCPServerName) {
			continue
		}
		backupDir, err := backupAgentFiles("plugin-mcp-"+sp.id, []string{sp.mcpPath}, s.AgentBackupRoot())
		if err != nil {
			return nil, err
		}
		res.BackupDir = backupDir
		if err := removeMCPEntry(sp); err != nil {
			return nil, fmt.Errorf("移除 %s 的 MCP 配置失败: %w", sp.name, err)
		}
		res.Removed = append(res.Removed, sp.mcpPath)
	}
	if len(res.Removed) == 0 {
		return nil, fmt.Errorf("没有需要移除的 MCP 配置")
	}
	return res, nil
}

func removeMCPEntry(sp pluginSpec) error {
	switch sp.mcpKind {
	case "claude-json", "gemini-json":
		return writeAgentJSON(sp.mcpPath, func(root map[string]any) {
			if servers, ok := root["mcpServers"].(map[string]any); ok {
				delete(servers, pluginMCPServerName)
			}
		})
	case "opencode-json", "crush-json":
		key := "mcp"
		return writeAgentJSON(sp.mcpPath, func(root map[string]any) {
			if mcp, ok := root[key].(map[string]any); ok {
				delete(mcp, pluginMCPServerName)
			}
		})
	case "codex-toml":
		existing := readAgentFile(sp.mcpPath)
		out := dropTOMLTable(existing, "[mcp_servers."+pluginMCPServerName+"]")
		return atomicWriteFile(sp.mcpPath, out)
	}
	return fmt.Errorf("未支持的 MCP 配置格式: %s", sp.mcpKind)
}

// applyRules 把规则源同步到各客户端规则文件（标记块内替换）；任一失败即回滚已写入的
func (s *Service) applyRules() (*PluginApplyResult, error) {
	src := s.GetConfig().Plugins.RulesText
	if strings.TrimSpace(src) == "" {
		return nil, fmt.Errorf("规则文本为空：请在插件中心填写编码规范")
	}
	root := s.AgentBackupRoot()
	res := &PluginApplyResult{Plugin: "rules"}
	var writes []pluginWrite
	for _, sp := range pluginSpecs() {
		if sp.rulesPath == "" || !pathExists(sp.detectDir) {
			continue
		}
		existed := pathExists(sp.rulesPath)
		target := "plugin-rules-" + sp.id
		backupDir, err := backupAgentFiles(target, []string{sp.rulesPath}, root)
		if err != nil {
			return nil, pluginWriteFail(err, rollbackPluginWrites(writes, root))
		}
		res.BackupDir = backupDir
		existing := readAgentFile(sp.rulesPath)
		block := pluginMarkerBegin + "\n" + strings.TrimRight(src, "\n") + "\n" + pluginMarkerEnd
		next := replaceMarkedBlock(existing, block)
		if sp.rulesKind == "mdc" && !strings.Contains(next, "alwaysApply:") {
			next = mdcFrontmatter() + next
		}
		if err := atomicWriteFile(sp.rulesPath, next); err != nil {
			return nil, pluginWriteFail(
				fmt.Errorf("写入 %s 规则文件失败: %w", sp.name, err),
				rollbackPluginWrites(writes, root))
		}
		writes = append(writes, pluginWrite{target: target, path: sp.rulesPath, backupID: pluginBackupID(backupDir, existed)})
		res.Files = append(res.Files, sp.rulesPath)
	}
	if len(res.Files) == 0 {
		return nil, fmt.Errorf("未检测到支持的客户端规则文件目标")
	}
	res.Detail = fmt.Sprintf("已同步到 %d 个客户端", len(res.Files))
	return res, nil
}

func (s *Service) removeRules() (*PluginApplyResult, error) {
	res := &PluginApplyResult{Plugin: "rules"}
	skipped := 0
	for _, sp := range pluginSpecs() {
		if sp.rulesPath == "" || !pathExists(sp.rulesPath) {
			continue
		}
		body := readAgentFile(sp.rulesPath)
		if !strings.Contains(body, pluginMarkerBegin) {
			continue
		}
		next := stripMarkedBlock(body)
		if next == body {
			// 标记不成对（只有开始或顺序颠倒）：不猜边界，交回用户手工处理
			skipped++
			continue
		}
		if _, err := backupAgentFiles("plugin-rules-"+sp.id, []string{sp.rulesPath}, s.AgentBackupRoot()); err != nil {
			return nil, err
		}
		if err := atomicWriteFile(sp.rulesPath, next); err != nil {
			return nil, fmt.Errorf("移除 %s 规则块失败: %w", sp.name, err)
		}
		res.Removed = append(res.Removed, sp.rulesPath)
	}
	if len(res.Removed) == 0 {
		if skipped > 0 {
			return nil, fmt.Errorf("有 %d 个规则文件的托管标记不成对（缺 %s），未做修改", skipped, pluginMarkerEnd)
		}
		return nil, fmt.Errorf("没有已同步的规则块")
	}
	if skipped > 0 {
		res.Detail = fmt.Sprintf("已移除 %d 个文件；%d 个文件标记不成对已跳过（需手工处理）", len(res.Removed), skipped)
	}
	return res, nil
}

// mdcFrontmatter Cursor 规则的必需 frontmatter（alwaysApply = 始终生效）
func mdcFrontmatter() string {
	return "---\ndescription: BuddyBot 同步的编码规范\nalwaysApply: true\n---\n\n"
}

// ---------- 钩子脚本 ----------

// hookPath 钩子脚本落盘路径（Claude Code 的 hooks 脚本目录）
func hookPath(kind string) string {
	return filepath.Join(agentEnvDir("CLAUDE_CONFIG_DIR", ".claude"), "hooks", "buddybot-"+kind+".sh")
}

// hookKindOf 钩子 ID → 脚本种类
func hookKindOf(id string) string {
	switch id {
	case "hook-safety":
		return "safety"
	case "hook-context":
		return "context"
	default:
		return "notify"
	}
}

// hookEventOf 钩子种类 → Claude Code 事件名
func hookEventOf(kind string) string {
	switch kind {
	case "safety":
		return "PreToolUse"
	case "context":
		return "SessionStart"
	default:
		return "Stop"
	}
}

func (s *Service) applyHook(id string) (*PluginApplyResult, error) {
	exe := selfExecutable()
	if exe == "" {
		return nil, fmt.Errorf("无法定位 BuddyBot 可执行文件，钩子未写入")
	}
	kind := hookKindOf(id)
	script := hookPath(kind)
	settings := claudeCodeSettingsPath()
	if err := os.MkdirAll(filepath.Dir(script), 0o755); err != nil {
		return nil, err
	}
	backupRoot := s.AgentBackupRoot()
	target := "plugin-" + id
	existedScript := pathExists(script)
	backupDir, err := backupAgentFiles(target, []string{script, settings}, backupRoot)
	if err != nil {
		return nil, err
	}
	// 脚本写完但配置写失败时，把脚本本身回滚掉，不留一个没人引用的钩子脚本
	rollbackScript := func() []string {
		return rollbackPluginWrites([]pluginWrite{{
			target: target, path: script, backupID: pluginBackupID(backupDir, existedScript),
		}}, backupRoot)
	}
	if err := atomicWriteFile(script, hookScript(kind, exe)); err != nil {
		return nil, err
	}
	if err := os.Chmod(script, 0o755); err != nil {
		return nil, pluginWriteFail(err, rollbackScript())
	}
	event := hookEventOf(kind)
	if err := writeAgentJSON(settings, func(root map[string]any) {
		hooks, _ := root["hooks"].(map[string]any)
		if hooks == nil {
			hooks = map[string]any{}
		}
		entries := toAnySlice(hooks[event])
		// 先剔除本插件的旧条目（幂等重装），再追加
		kept := make([]any, 0, len(entries)+1)
		for _, e := range entries {
			if !hookEntryRefersTo(e, filepath.Base(script)) {
				kept = append(kept, e)
			}
		}
		entry := map[string]any{
			"hooks": []any{map[string]any{"type": "command", "command": script}},
		}
		if kind == "safety" {
			entry["matcher"] = "Bash"
		}
		kept = append(kept, entry)
		hooks[event] = kept
		root["hooks"] = hooks
	}); err != nil {
		return nil, pluginWriteFail(
			fmt.Errorf("写入 Claude Code hooks 配置失败: %w", err), rollbackScript())
	}
	return &PluginApplyResult{
		Plugin: id,
		Files:  []string{script, settings},
		Detail: fmt.Sprintf("已注册 %s 钩子，脚本 %s", event, script),
	}, nil
}

func (s *Service) removeHook(id string) (*PluginApplyResult, error) {
	kind := hookKindOf(id)
	script := hookPath(kind)
	event := hookEventOf(kind)
	settings := claudeCodeSettingsPath()
	if pathExists(settings) {
		raw := readAgentFile(settings)
		if strings.Contains(raw, filepath.Base(script)) {
			if _, err := backupAgentFiles("plugin-"+id, []string{settings, script}, s.AgentBackupRoot()); err != nil {
				return nil, err
			}
			if err := writeAgentJSON(settings, func(root map[string]any) {
				hooks, _ := root["hooks"].(map[string]any)
				if hooks == nil {
					return
				}
				entries := toAnySlice(hooks[event])
				kept := make([]any, 0, len(entries))
				for _, e := range entries {
					if !hookEntryRefersTo(e, filepath.Base(script)) {
						kept = append(kept, e)
					}
				}
				if len(kept) == 0 {
					delete(hooks, event)
				} else {
					hooks[event] = kept
				}
				root["hooks"] = hooks
			}); err != nil {
				return nil, err
			}
		}
	}
	removed := []string{}
	if pathExists(script) {
		if err := os.Remove(script); err != nil {
			return nil, err
		}
		removed = append(removed, script)
	}
	if len(removed) == 0 {
		return nil, fmt.Errorf("未找到已安装的钩子脚本")
	}
	return &PluginApplyResult{Plugin: id, Removed: removed}, nil
}

// hookScript 生成钩子脚本：脚本只做转发，判定逻辑在 Go 侧（可用同一套测试覆盖）。
// 注释里不出现反引号 / $( )——那是命令替换语法，哪行被挪出注释就会真的执行。
func hookScript(kind, exe string) string {
	sub := map[string]string{"safety": "hook-guard", "context": "hook-context", "notify": "hook-notify"}[kind]
	return "#!/bin/sh\n" +
		"# BuddyBot 插件中心生成：重装会覆盖本文件\n" +
		"# 用途：" + hookEventOf(kind) + " 钩子，判定逻辑在「" + filepath.Base(exe) + " " + sub + "」子命令里\n" +
		"exec " + shellQuote(exe) + " " + sub + "\n"
}

func shellQuote(p string) string {
	if !strings.ContainsAny(p, " \t\"'$`\\") {
		return p
	}
	return "'" + strings.ReplaceAll(p, "'", `'\''`) + "'"
}

// toAnySlice 把 JSON 解出的任意值当数组用（缺失/nil → 空切片）
func toAnySlice(v any) []any {
	if list, ok := v.([]any); ok {
		return list
	}
	return nil
}

// hookEntryRefersTo 判断一条 hooks 配置项是否指向指定脚本
func hookEntryRefersTo(entry any, scriptName string) bool {
	m, ok := entry.(map[string]any)
	if !ok {
		return false
	}
	for _, h := range toAnySlice(m["hooks"]) {
		hm, ok := h.(map[string]any)
		if !ok {
			continue
		}
		if cmd, _ := hm["command"].(string); strings.Contains(cmd, scriptName) {
			return true
		}
	}
	return false
}

// ---------- Slash 命令 ----------

// PluginSlashCommand 一条可安装的提效指令
type PluginSlashCommand struct {
	ID        int    `json:"id"`
	Title     string `json:"title"`
	Category  string `json:"category"`
	Name      string `json:"name"` // 安装后的命令名（不含 /）
	Installed bool   `json:"installed"`
	Path      string `json:"path"`
}

// slashCommandDir 命令安装目录
func slashCommandDir() string {
	return filepath.Join(agentEnvDir("CLAUDE_CONFIG_DIR", ".claude"), "commands")
}

// slashName 命令名：ASCII 稳定（中文标题不适合做文件名），格式 buddybot-<id>-<category>
func slashName(p promptEntry) string {
	cat := strings.ToLower(strings.TrimSpace(p.Category))
	cat = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			return r
		}
		return '-'
	}, cat)
	cat = strings.Trim(strings.ReplaceAll(cat, "--", "-"), "-")
	if cat == "" {
		cat = "command"
	}
	return fmt.Sprintf("buddybot-%d-%s", p.ID, cat)
}

// promptEntry prompts.json 的条目结构（embedded 数据，字段与前端一致）
type promptEntry struct {
	ID       int    `json:"id"`
	Title    string `json:"title"`
	Category string `json:"category"`
	Scene    string `json:"scene"`
	Prompt   string `json:"prompt"`
}

// embeddedPromptsJSON 随包内嵌的提效指令库（main 启动时注入：
// 该文件在 frontend 目录下，go:embed 不允许跨目录，故由 main 传入原始字节）
var embeddedPromptsJSON []byte

// SetEmbeddedPrompts 注入提效指令库原始 JSON（main 启动时调用一次）
func SetEmbeddedPrompts(raw []byte) { embeddedPromptsJSON = raw }

// loadPromptEntries 读取随包内嵌的提效指令库
func loadPromptEntries() []promptEntry {
	if len(embeddedPromptsJSON) == 0 {
		return nil
	}
	var out []promptEntry
	if err := json.Unmarshal(embeddedPromptsJSON, &out); err != nil {
		return nil
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// slashCommandEntries 逐条列出提效指令及其安装状态（页面按条勾选用）
func slashCommandEntries() []PluginSlashCommand {
	dir := slashCommandDir()
	entries := loadPromptEntries()
	out := make([]PluginSlashCommand, 0, len(entries))
	for _, p := range entries {
		path := filepath.Join(dir, slashName(p)+".md")
		out = append(out, PluginSlashCommand{
			ID: p.ID, Title: p.Title, Category: p.Category,
			Name: slashName(p), Installed: pathExists(path), Path: path,
		})
	}
	return out
}

// SlashCommandList 提效指令清单（含逐条安装状态）
func (s *Service) SlashCommandList() []PluginSlashCommand { return slashCommandEntries() }

// slashPluginStatus 只给一行汇总（目标 = Claude Code 命令目录），
// 逐条明细走 SlashCommandList——否则 100 条指令会把卡片撑成 100 行。
func slashPluginStatus() PluginStatus {
	st := PluginStatus{
		ID: "slash", Name: "提效指令安装为命令",
		Description: "把提效指令库安装为客户端 slash 命令（/buddybot-<编号>-<分类>），在会话里直接调用",
	}
	entries := slashCommandEntries()
	installed := 0
	for _, e := range entries {
		if e.Installed {
			installed++
		}
	}
	dir := slashCommandDir()
	st.Targets = []PluginTargetStatus{{
		ID:        "claude-code",
		Name:      "Claude Code",
		Path:      dir,
		Installed: pathExists(agentEnvDir("CLAUDE_CONFIG_DIR", ".claude")),
		Applied:   installed > 0,
		Note:      fmt.Sprintf("已安装 %d / %d 条", installed, len(entries)),
	}}
	st.Installed = installed > 0
	st.Detail = fmt.Sprintf("命令目录 %s", dir)
	return st
}

// ApplySlashCommands 安装指定编号的指令（ids 为空 = 全部）
func (s *Service) ApplySlashCommands(ids []int) (*PluginApplyResult, error) {
	entries := loadPromptEntries()
	if len(entries) == 0 {
		return nil, fmt.Errorf("提效指令库为空，无内容可安装")
	}
	want := map[int]bool{}
	for _, id := range ids {
		want[id] = true
	}
	dir := slashCommandDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	res := &PluginApplyResult{Plugin: "slash"}
	for _, p := range entries {
		if len(want) > 0 && !want[p.ID] {
			continue
		}
		path := filepath.Join(dir, slashName(p)+".md")
		if err := atomicWriteFile(path, slashCommandBody(p)); err != nil {
			return nil, err
		}
		res.Files = append(res.Files, path)
	}
	if len(res.Files) == 0 {
		return nil, fmt.Errorf("没有匹配的指令可安装")
	}
	res.Detail = fmt.Sprintf("已安装 %d 条命令到 %s", len(res.Files), dir)
	return res, nil
}

// slashCommandBody 命令文件内容：frontmatter 描述 + 指令正文
func slashCommandBody(p promptEntry) string {
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "description: %s\n", yamlQuote(p.Title))
	if p.Scene != "" {
		fmt.Fprintf(&b, "argument-hint: %s\n", yamlQuote(p.Scene))
	}
	b.WriteString("---\n\n")
	b.WriteString(p.Prompt)
	b.WriteString("\n")
	return b.String()
}

// yamlQuote 简化 YAML 标量引号（标题可能含冒号等字符）
func yamlQuote(s string) string {
	return "\"" + strings.ReplaceAll(strings.ReplaceAll(s, "\\", "\\\\"), "\"", "\\\"") + "\""
}

// RemoveSlashCommands 卸载已安装的命令（ids 为空 = 全部）
func (s *Service) RemoveSlashCommands(ids []int) (*PluginApplyResult, error) {
	want := map[int]bool{}
	for _, id := range ids {
		want[id] = true
	}
	res := &PluginApplyResult{Plugin: "slash"}
	for _, p := range loadPromptEntries() {
		if len(want) > 0 && !want[p.ID] {
			continue
		}
		path := filepath.Join(slashCommandDir(), slashName(p)+".md")
		if !pathExists(path) {
			continue
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
		res.Removed = append(res.Removed, path)
	}
	if len(res.Removed) == 0 {
		return nil, fmt.Errorf("没有已安装的命令可卸载")
	}
	res.Detail = fmt.Sprintf("已卸载 %d 条命令", len(res.Removed))
	return res, nil
}

// ---------- 标记块工具 ----------

// replaceMarkedBlock 替换/追加托管块：块内内容整体替换，块外原样保留
func replaceMarkedBlock(body, block string) string {
	if i := strings.Index(body, pluginMarkerBegin); i >= 0 {
		if j := strings.Index(body, pluginMarkerEnd); j > i {
			end := j + len(pluginMarkerEnd)
			return body[:i] + block + body[end:]
		}
	}
	sep := ""
	if trimmed := strings.TrimRight(body, "\n"); trimmed != "" {
		sep = "\n\n"
		body = trimmed
	}
	return body + sep + block + "\n"
}

// stripMarkedBlock 移除托管块（含其前后多余空行），保留用户内容
func stripMarkedBlock(body string) string {
	i := strings.Index(body, pluginMarkerBegin)
	if i < 0 {
		return body
	}
	j := strings.Index(body, pluginMarkerEnd)
	if j < i {
		return body
	}
	out := body[:i] + body[j+len(pluginMarkerEnd):]
	out = strings.ReplaceAll(out, "\n\n\n", "\n\n")
	return strings.TrimLeft(out, "\n")
}

// markedBlock 取托管块内文本（未找到返回 ""）
func markedBlock(body string) string {
	i := strings.Index(body, pluginMarkerBegin)
	if i < 0 {
		return ""
	}
	j := strings.Index(body, pluginMarkerEnd)
	if j < i {
		return ""
	}
	return body[i+len(pluginMarkerBegin) : j]
}

// tomlQuote TOML 基本字符串（路径可能含反斜杠，Windows 下必须转义）
func tomlQuote(s string) string {
	return "\"" + strings.ReplaceAll(s, "\\", "\\\\") + "\""
}

// dropTOMLTable 删除指定 TOML 表（含其键值行，直到下一个表头）
func dropTOMLTable(text, header string) string {
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	skipping := false
	for _, l := range lines {
		if h := tomlHeaderOf(l); h != "" {
			skipping = h == header
			if skipping {
				continue
			}
		}
		if skipping {
			continue
		}
		out = append(out, l)
	}
	return strings.TrimRight(strings.Join(out, "\n"), "\n") + "\n"
}

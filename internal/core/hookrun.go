package core

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ============================================================
// 钩子运行入口（由插件中心写入的脚本回调，见 plugins.go）
//
// 三个子命令都从 stdin 读客户端传入的 hook JSON，向 stdout 输出
// 单个 JSON 决策对象；判定逻辑全在 Go 侧，脚本只做转发，
// 因此可以用普通 Go 测试完整覆盖（不依赖真实客户端）。
// ============================================================

// hookInput Claude Code 钩子入参（只取用到的字段，其余忽略）
type hookInput struct {
	HookEventName  string `json:"hook_event_name"`
	SessionID      string `json:"session_id"`
	CWD            string `json:"cwd"`
	ToolName       string `json:"tool_name"`
	StopHookActive bool   `json:"stop_hook_active"`
	ToolInput      struct {
		Command string `json:"command"`
	} `json:"tool_input"`
}

func readHookInput(r io.Reader) *hookInput {
	var in hookInput
	raw, err := io.ReadAll(io.LimitReader(r, 4<<20))
	if err == nil && len(raw) > 0 {
		_ = json.Unmarshal(raw, &in)
	}
	return &in
}

func writeHookJSON(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	_, _ = os.Stdout.Write(append(b, '\n'))
}

// RunHookGuard PreToolUse 决策：命令命中白名单且不含 shell 组合符号时放行，
// 否则不输出任何决策（交回客户端默认流程，仍会按原策略询问用户）。
func RunHookGuard(svc *Service, stdin io.Reader) {
	in := readHookInput(stdin)
	cmd := strings.TrimSpace(in.ToolInput.Command)
	allow, reason := guardDecision(svc.GetConfig().Plugins.SafetyAllowlist, cmd)
	if !allow {
		return // 静默：不干预客户端原有权限流程
	}
	writeHookJSON(map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":            "PreToolUse",
			"permissionDecision":       "allow",
			"permissionDecisionReason": reason,
		},
	})
}

// guardDecision 判定是否自动放行；返回 false 时不干预（交回客户端默认询问流程）。
//
// 三层判定依次通过才放行——白名单只约束命令前缀，光看前缀不足以判定「这个具体写法安全」：
//  1. 不含组合/替换/重定向符号：否则命令会串出白名单之外的行为（`;` `|` `&` `>` `<` 反引号 `$` 换行）
//  2. 不触碰敏感路径：读取类命令（cat/grep/find…）的参数是自由的，
//     一旦指向密钥、凭据或设备文件就必须由用户确认
//  3. 参数级用法不属破坏性动作：例如 find 的 -delete/-exec 会把「只读查找」变成任意删除/执行，
//     git 的 branch -D / push / reset 也不是「查看」
func guardDecision(allowlist []string, cmd string) (bool, string) {
	if cmd == "" {
		return false, ""
	}
	if strings.ContainsAny(cmd, ";|&><`$\n") {
		return false, ""
	}
	if guardTouchesProtectedPath(cmd) {
		return false, ""
	}
	if guardDestructiveArgs(strings.Fields(cmd)) {
		return false, ""
	}
	for _, prefix := range allowlist {
		p := strings.TrimSpace(prefix)
		if p == "" {
			continue
		}
		if cmd == p || strings.HasPrefix(cmd, p+" ") {
			return true, "命中 BuddyBot 安全命令白名单：" + p
		}
	}
	return false, ""
}

// guardProtectedPathSegments 命中即不自动放行的路径片段：密钥/凭据、设备与系统目录。
// 判断在整条命令上做子串匹配——宁可多弹一次确认，也不放过 `cat ~/.ssh/id_rsa`
// 这类「白名单命令 + 自由参数」组合（参数可以指向任意文件）。
var guardProtectedPathSegments = []string{
	"~",
	".ssh", ".aws", ".gnupg", ".netrc", ".buddybot", ".config", ".kube", ".docker",
	"keychains", "id_rsa", "id_ed25519",
	"/etc/", "/dev/", "/private/", "/var/db/", "/system/",
}

func guardTouchesProtectedPath(cmd string) bool {
	lower := strings.ToLower(cmd)
	for _, seg := range guardProtectedPathSegments {
		if strings.Contains(lower, seg) {
			return true
		}
	}
	return false
}

// guardDestructiveArgs 参数级的破坏性用法（fields[0] 为命令名）；命中返回 true
func guardDestructiveArgs(fields []string) bool {
	if len(fields) == 0 {
		return false
	}
	switch fields[0] {
	case "find":
		for _, a := range fields[1:] {
			switch a {
			case "-delete", "-exec", "-execdir", "-ok", "-okdir",
				"-fprint", "-fprint0", "-fprintf", "-fls":
				return true
			}
		}
	case "git":
		return guardGitArgs(fields[1:])
	}
	return false
}

// guardGitArgs git 只读子命令与参数白名单；命中写操作/网络操作返回 true（即不自动放行）
func guardGitArgs(args []string) bool {
	if len(args) == 0 {
		return false
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "status", "diff", "log", "show", "blame", "grep", "shortlog",
		"describe", "rev-parse", "ls-files", "show-ref", "cat-file", "rev-list", "whatchanged":
		for _, a := range rest {
			if strings.HasPrefix(a, "--output") { // 会被写文件
				return true
			}
		}
		return false
	case "branch":
		for _, a := range rest {
			switch a {
			case "-d", "-D", "--delete", "-m", "-M", "--move", "-c", "-C", "--copy":
				return true
			}
		}
		return false
	case "remote":
		if len(rest) == 0 {
			return false
		}
		switch rest[0] {
		case "-v", "--verbose", "show", "get-url":
			return false
		}
		return true // remote add/remove/rename/set-url 等写操作
	}
	return true // push/pull/commit/reset/clean/checkout/restore/rm/config 等一律不自动放行
}

// RunHookContext SessionStart 注入：把本机资源摘要写进会话上下文。
// 只用内存态数据（账号池 + 网关记账），不做全盘扫描，保证钩子快速返回。
func RunHookContext(svc *Service, stdin io.Reader) {
	_ = readHookInput(stdin)
	text := contextBriefing(svc)
	if strings.TrimSpace(text) == "" {
		return
	}
	writeHookJSON(map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":     "SessionStart",
			"additionalContext": text,
		},
	})
}

// contextBriefing 生成资源简报文本（无数据时如实说明，不编造）
func contextBriefing(svc *Service) string {
	accounts := svc.Accounts()
	if len(accounts) == 0 {
		return ""
	}
	online, known := 0, 0
	var credits, expiring float64
	for _, a := range accounts {
		if a.Status == "online" {
			online++
		}
		if a.CreditsKnown {
			known++
			credits += a.Credits
			expiring += a.CreditsExpiring
		}
	}
	dash := svc.Stats().Dashboard(1)
	var b strings.Builder
	b.WriteString("【BuddyBot 资源简报】")
	fmt.Fprintf(&b, "账号池 %d 个（在线 %d）", len(accounts), online)
	if known > 0 {
		fmt.Fprintf(&b, "；已知积分合计 %.0f", credits)
		if expiring > 0 {
			fmt.Fprintf(&b, "（72h 内到期 %.0f，优先使用临期账号）", expiring)
		}
	}
	fmt.Fprintf(&b, "；今日网关消耗 %d token（¥%.4f）", dash.Overview.Tokens, dash.Overview.TodayCost)
	if dash.Overview.UnpricedModels > 0 {
		fmt.Fprintf(&b, "，%d 个模型未定价", dash.Overview.UnpricedModels)
	}
	b.WriteString("。需要明细时调用 MCP 工具 buddybot_quota / buddybot_estimate_cost / buddybot_search_sessions / buddybot_search_code。")
	return b.String()
}

// notifyMinInterval 同一会话两次推送的最小间隔。Stop 钩子是「主 agent 每轮回答结束」都会触发，
// 不节流会变成一轮一条的通知轰炸；节流窗口内静默跳过，不影响会话本身。
const notifyMinInterval = 10 * time.Minute

// notifyStateFile 节流状态（会话 ID → 上次推送时间戳）
func notifyStateFile() string { return filepath.Join(AppDir(), "hook-notify.json") }

// RunHookNotify Stop 钩子：一轮回答结束时推送摘要（复用已配置的 Bark / PushPlus 通道）。
// 通知是不可靠通道，任何失败都不影响会话（只写进程日志）。
func RunHookNotify(svc *Service, stdin io.Reader) {
	in := readHookInput(stdin)
	if in.StopHookActive {
		return // 钩子自身触发的续跑：不再推送，避免循环
	}
	cfg := svc.GetConfig().Schedule.Notify
	if !cfg.Enabled || (cfg.PushPlusToken == "" && cfg.BarkURL == "") {
		return // 未配置推送通道：静默退出（桌面横幅由运行中的 BuddyBot 负责）
	}
	if !notifyAllowed(in.SessionID) {
		return // 同一会话刚推过：节流
	}
	proj := clientProjectName(in.CWD)
	title := "BuddyBot：本轮回答完成"
	body := fmt.Sprintf("项目 %s 完成一轮回答（%s）", proj, time.Now().Format("15:04"))
	svc.SendNotifyRemote(title, body)
}

// notifyAllowed 按会话节流：允许则记录时间戳并返回 true
func notifyAllowed(sessionID string) bool {
	if sessionID == "" {
		return true // 拿不到会话标识时不做节流：宁可多推一次，也不静默丢通知
	}
	state := map[string]int64{}
	if raw, err := os.ReadFile(notifyStateFile()); err == nil {
		_ = json.Unmarshal(raw, &state)
	}
	now := time.Now().Unix()
	if last, ok := state[sessionID]; ok && now-last < int64(notifyMinInterval/time.Second) {
		return false
	}
	for k, v := range state { // 顺带清理过期条目，避免文件无限增长
		if now-v > int64((24*time.Hour)/time.Second) {
			delete(state, k)
		}
	}
	state[sessionID] = now
	if b, err := json.Marshal(state); err == nil {
		if mkErr := os.MkdirAll(filepath.Dir(notifyStateFile()), 0o755); mkErr == nil {
			_ = os.WriteFile(notifyStateFile(), b, 0o600)
		}
	}
	return true
}

// SendNotifyRemote 走配置中的远端推送通道（Bark / PushPlus）
func (s *Service) SendNotifyRemote(title, body string) {
	cfg := s.GetConfig().Schedule.Notify
	client := &http.Client{Timeout: 10 * time.Second}
	if tok := strings.TrimSpace(cfg.PushPlusToken); tok != "" {
		pushPlus(client, tok, title, body)
	}
	if bu := strings.TrimSpace(cfg.BarkURL); bu != "" {
		barkPush(client, bu, title, body)
	}
}

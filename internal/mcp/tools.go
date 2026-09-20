package mcp

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"workbuddy-desktop/internal/core"
)

// toolDef MCP 工具描述（tools/list 返回项）
type toolDef struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema any    `json:"inputSchema"`
}

// toolDefs 三个工具，刻意少而精：MCP 工具定义常驻上下文，数量是成本
func toolDefs() []toolDef {
	return []toolDef{
		{
			Name: "buddybot_quota",
			Description: "查询本机 BuddyBot 账号池状态：各账号在线/冷却/过期状态、积分余额、72h 内到期积分、token 有效期。" +
				"在长任务开始前或中途自查剩余资源时使用。",
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
		{
			Name: "buddybot_estimate_cost",
			Description: "按 BuddyBot 的模型单价表（元/百万 token）估算一次调用的费用。" +
				"模型不在单价表时返回「未定价」。用于评估方案成本或向用户汇报花费。",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"model":             map[string]any{"type": "string", "description": "模型名，如 glm-5.3-flash"},
					"input_tokens":      map[string]any{"type": "number", "description": "输入 token 数（不含缓存读）"},
					"output_tokens":     map[string]any{"type": "number", "description": "输出 token 数"},
					"cache_read_tokens": map[string]any{"type": "number", "description": "缓存读 token 数（可省略）"},
					"cache_write_tokens": map[string]any{"type": "number", "description": "缓存写 token 数（可省略）"},
				},
				"required": []string{"model", "input_tokens", "output_tokens"},
			},
		},
		{
			Name: "buddybot_search_sessions",
			Description: "全文检索本机 WorkBuddy 客户端的历史会话日志（用户消息与助手回复正文），" +
				"返回命中片段及所属会话/项目/标题。用于回忆「之前某个问题是怎么解决的」等跨会话上下文。",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query":   map[string]any{"type": "string", "description": "检索关键词（大小写不敏感的子串匹配）"},
					"days":    map[string]any{"type": "number", "description": "回溯天数，默认 30，最大 365"},
					"limit":   map[string]any{"type": "number", "description": "最多返回命中数，默认 20，最大 100"},
					"project": map[string]any{"type": "string", "description": "按项目目录名过滤（子串匹配，可省略）"},
				},
				"required": []string{"query"},
			},
		},
		{
			Name: "buddybot_search_code",
			Description: "在指定项目目录内按关键词检索代码（返回文件/行号/上下文行）。" +
				"跳过 node_modules/.git/dist 等目录与二进制文件。用于快速定位符号、字符串或配置项在代码库中的位置。",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{"type": "string", "description": "检索关键词（大小写不敏感的子串匹配）"},
					"path":  map[string]any{"type": "string", "description": "项目根目录绝对路径；省略时用当前工作目录"},
					"limit": map[string]any{"type": "number", "description": "最多返回命中行数，默认 30，最大 100"},
				},
				"required": []string{"query"},
			},
		},
	}
}

// callTool 分发工具调用，返回展示给模型的文本
func callTool(svc *core.Service, name string, rawArgs json.RawMessage) (string, error) {
	var args map[string]any
	if len(rawArgs) > 0 {
		if err := json.Unmarshal(rawArgs, &args); err != nil {
			return "", fmt.Errorf("参数解析失败: %w", err)
		}
	}
	switch name {
	case "buddybot_quota":
		return quotaText(svc), nil
	case "buddybot_estimate_cost":
		return estimateCostText(svc, args)
	case "buddybot_search_sessions":
		return searchSessionsText(svc, args)
	case "buddybot_search_code":
		return searchCodeText(args)
	default:
		return "", fmt.Errorf("未知工具: %s", name)
	}
}

func argNumber(args map[string]any, key string) (float64, bool) {
	v, ok := args[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return n, true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	case string:
		var f float64
		if _, err := fmt.Sscanf(strings.TrimSpace(n), "%g", &f); err == nil {
			return f, true
		}
	}
	return 0, false
}

func argString(args map[string]any, key string) string {
	if v, ok := args[key].(string); ok {
		return v
	}
	return ""
}

// quotaText 账号池状态的人类可读汇总
func quotaText(svc *core.Service) string {
	accounts := svc.Accounts()
	if len(accounts) == 0 {
		return "账号池为空：未在凭证目录发现任何账号。"
	}
	counts := map[string]int{}
	statusOrder := []string{"online", "cooldown", "expired", "relogin", "disabled", "invalid", "unknown"}
	statusLabel := map[string]string{
		"online": "在线", "cooldown": "冷却", "expired": "过期",
		"relogin": "需重登", "disabled": "已禁用", "invalid": "损坏", "unknown": "未知",
	}
	var creditsSum float64
	creditsKnown := 0
	var expiringSum float64
	var expiringAccounts int
	for _, a := range accounts {
		counts[a.Status]++
		if a.CreditsKnown {
			creditsKnown++
			creditsSum += a.Credits
			if a.CreditsExpiring > 0 {
				expiringSum += a.CreditsExpiring
				expiringAccounts++
			}
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "账号池共 %d 个账号：", len(accounts))
	first := true
	for _, st := range statusOrder {
		if counts[st] == 0 {
			continue
		}
		if !first {
			b.WriteString("，")
		}
		first = false
		fmt.Fprintf(&b, "%s %d", statusLabel[st], counts[st])
	}
	b.WriteString("\n")
	if creditsKnown > 0 {
		fmt.Fprintf(&b, "已知积分余额 %d 个账号合计 %.2f", creditsKnown, creditsSum)
		if expiringSum > 0 {
			fmt.Fprintf(&b, "；72h 内到期 %.2f（%d 个账号）", expiringSum, expiringAccounts)
		}
		b.WriteString("\n")
	} else {
		b.WriteString("积分余额均未知（可在 BuddyBot 账号管理页刷新）\n")
	}

	b.WriteString("\n账号明细：\n")
	for _, a := range accounts {
		label := statusLabel[a.Status]
		if label == "" {
			label = a.Status
		}
		name := a.Nickname
		if name == "" {
			name = a.UID
		}
		uid := a.UID
		if len(uid) > 10 {
			uid = uid[:10] + "…"
		}
		fmt.Fprintf(&b, "- %s [%s]", name, label)
		if a.CreditsKnown {
			fmt.Fprintf(&b, " 积分 %.2f", a.Credits)
			if a.CreditsExpiring > 0 {
				fmt.Fprintf(&b, "（72h 内到期 %.2f）", a.CreditsExpiring)
			}
		} else {
			b.WriteString(" 积分未知")
		}
		if a.TokenExpiry != "" {
			fmt.Fprintf(&b, "；token 至 %s", a.TokenExpiry)
		}
		if a.Note != "" {
			fmt.Fprintf(&b, "；%s", a.Note)
		}
		fmt.Fprintf(&b, "（uid %s）\n", uid)
	}
	return b.String()
}

// maxEstimateTokens 单次估算的 token 上限：拦住明显的异常输入（10 亿 ×1000 不可能出现）
const maxEstimateTokens = 1e12

// estimateCostText 按单价表估算一次调用费用
func estimateCostText(svc *core.Service, args map[string]any) (string, error) {
	model := strings.TrimSpace(argString(args, "model"))
	if model == "" {
		return "", fmt.Errorf("缺少 model 参数")
	}
	in, okIn := argNumber(args, "input_tokens")
	out, okOut := argNumber(args, "output_tokens")
	if !okIn || !okOut {
		return "", fmt.Errorf("缺少 input_tokens / output_tokens 参数")
	}
	read, _ := argNumber(args, "cache_read_tokens")
	write, _ := argNumber(args, "cache_write_tokens")
	// 参数校验：负数会算出负费用；超大浮点转 int 是平台相关行为（可能溢出成负数）
	for _, c := range []struct {
		name string
		v    float64
	}{
		{"input_tokens", in}, {"output_tokens", out},
		{"cache_read_tokens", read}, {"cache_write_tokens", write},
	} {
		if c.v < 0 {
			return "", fmt.Errorf("%s 不能为负数", c.name)
		}
		if c.v > maxEstimateTokens {
			return "", fmt.Errorf("%s 超出合理范围（上限 %.0f）", c.name, maxEstimateTokens)
		}
	}

	table := core.NewPriceTable(svc.ModelPrices())
	cost, priced := table.Cost(model, int(in), int(out), int(read), int(write))
	if !priced {
		return fmt.Sprintf("模型 %s 在单价表中未配置单价（设置 → 模型单价 可补），无法估算。", model), nil
	}
	return fmt.Sprintf("模型 %s 本次调用估算费用：¥%.4f（输入 %d / 输出 %d / 缓存读 %d / 缓存写 %d token）",
		model, cost, int(in), int(out), int(read), int(write)), nil
}

// searchSessionsText 会话检索结果的文本化
func searchSessionsText(svc *core.Service, args map[string]any) (string, error) {
	query := argString(args, "query")
	if strings.TrimSpace(query) == "" {
		return "", fmt.Errorf("缺少 query 参数")
	}
	days := 30
	if v, ok := argNumber(args, "days"); ok && v > 0 {
		days = int(v)
	}
	limit := 20
	if v, ok := argNumber(args, "limit"); ok && v > 0 {
		limit = int(v)
	}
	res := core.SearchClientSessions(query, argString(args, "project"), days, limit)

	var b strings.Builder
	fmt.Fprintf(&b, "在最近 %d 天的本机会话中检索「%s」：%d 条命中，覆盖 %d 个会话（扫描 %d 个文件）。\n",
		res.Days, res.Query, len(res.Hits), res.Sessions, res.FilesScanned)
	if res.Truncated {
		b.WriteString("（结果已截断，可缩小关键词或减小 days）\n")
	}
	if len(res.Hits) == 0 {
		b.WriteString("没有命中。可尝试更短的关键词。")
		return b.String(), nil
	}
	for i, h := range res.Hits {
		title := h.Title
		if title == "" {
			title = "(无标题)"
		}
		ts := "时间未知"
		if h.Timestamp > 0 {
			ts = time.Unix(h.Timestamp, 0).Format("2006-01-02 15:04")
		}
		fmt.Fprintf(&b, "%d. [%s] %s（项目 %s，%s，%s）\n   %s\n",
			i+1, h.Source, title, h.Project, ts, h.Role, h.Snippet)
	}
	b.WriteString("\n如需完整上下文，可在 WorkBuddy 中按会话标题查找对应历史会话。")
	return b.String(), nil
}

// searchCodeText 代码检索结果文本化
func searchCodeText(args map[string]any) (string, error) {
	query := argString(args, "query")
	if strings.TrimSpace(query) == "" {
		return "", fmt.Errorf("缺少 query 参数")
	}
	root := argString(args, "path")
	if strings.TrimSpace(root) == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("未提供 path 且无法获取当前工作目录: %w", err)
		}
		root = wd
	}
	limit := 30
	if v, ok := argNumber(args, "limit"); ok && v > 0 {
		limit = int(v)
	}
	res, err := core.SearchProjectCode(root, query, limit)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "在 %s 中检索「%s」：%d 条命中，覆盖 %d 个文件（扫描 %d，跳过 %d）。\n",
		res.Root, res.Query, len(res.Hits), res.MatchedFiles, res.FilesScanned, res.FilesSkipped)
	switch res.StopReason {
	case core.CodeSearchStopBudget:
		b.WriteString("（已达扫描预算——文件数/时长上限，结果不完整；请指定更具体的目录或关键词）\n")
	case core.CodeSearchStopHits:
		b.WriteString("（结果已截断，可缩小关键词或减小 limit）\n")
	}
	if len(res.Hits) == 0 {
		b.WriteString("没有命中。")
		return b.String(), nil
	}
	for _, h := range res.Hits {
		fmt.Fprintf(&b, "%s:%d: %s\n", h.Path, h.Line, h.Snippet)
	}
	return b.String(), nil
}

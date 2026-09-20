package inject

import (
	"encoding/json"
	"time"
)

// ============================================================
// 面板数据通道：提效指令库（「指令」Tab）+ FAB 运行徽标
//
// 指令库单一数据源：frontend/src/data/prompts.json 由 main.go 内嵌传入，
// 桌面端「提效指令库」页与注入面板共用同一份；面板按需拉取一次。
// 徽标数据（在线账号数 / 今日 Token）随注入状态推送 + 每分钟随余额刷新。
// ============================================================

// panelPrompt 指令库条目（prompts.json 字段的投影，预期产出/技巧不下发面板）
type panelPrompt struct {
	ID       int    `json:"id"`
	Title    string `json:"title"`
	Category string `json:"category"`
	Scene    string `json:"scene"`
	Prompt   string `json:"prompt"`
}

var parsedPrompts []panelPrompt

// panelPrompts 解析内嵌指令库（进程内缓存，prompts.json 随二进制不可变）
func (m *Manager) panelPrompts() []panelPrompt {
	if parsedPrompts == nil {
		_ = json.Unmarshal(m.promptsJSON, &parsedPrompts)
		if parsedPrompts == nil {
			parsedPrompts = []panelPrompt{}
		}
	}
	return parsedPrompts
}

// pushPanelPromptsConn 面板请求指令库（binding 动作入口）
func (m *Manager) pushPanelPromptsConn() {
	m.mu.Lock()
	conn := m.conn
	m.mu.Unlock()
	if conn == nil {
		return
	}
	m.pushPanelPrompts(conn)
}

// pushPanelPrompts 推送指令库（约 80KB base64，仅在面板打开「指令」Tab 时拉取）
func (m *Manager) pushPanelPrompts(conn *cdpConn) {
	raw, _ := json.Marshal(m.panelPrompts())
	_, _ = conn.evaluate(`window.__wbdeskSetPrompts && window.__wbdeskSetPrompts(`+string(raw)+`)`, 15*time.Second)
}

// panelBadge FAB 运行徽标视图：在线账号数 / 池内总数 / 今日 Token 消耗
type panelBadge struct {
	Online int    `json:"online"`
	Total  int    `json:"total"`
	Tokens int    `json:"tokens"`
	Since  string `json:"since"` // 今日 0 点（本地时区），口径说明用
}

func (m *Manager) badgeSummary() panelBadge {
	out := panelBadge{}
	for _, a := range m.svc.Accounts() {
		out.Total++
		if a.Status == "online" {
			out.Online++
		}
	}
	now := time.Now()
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).Unix()
	out.Since = time.Unix(midnight, 0).Format("2006-01-02 15:04")
	for _, l := range m.svc.Store().ListRequestLogs() {
		if l.TS >= midnight {
			out.Tokens += l.Tokens
		}
	}
	return out
}

// pushPanelBadge 推送 FAB 徽标数据
func (m *Manager) pushPanelBadge(conn *cdpConn) {
	raw, _ := json.Marshal(m.badgeSummary())
	_, _ = conn.evaluate(`window.__wbdeskSetBadge && window.__wbdeskSetBadge(`+string(raw)+`)`, 5*time.Second)
}

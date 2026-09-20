package inject

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"workbuddy-desktop/internal/core"
)

// ============================================================
// 注入体检 + 验证截图（对齐 Codex Dream Skin 的 doctor / verify 理念）
//
// doctor：逐项自检客户端程序 → CDP 端口 → 注入连接 → 面板 DOM →
//         回传通道 → 主题标记 → 原生输入框，输出可读报告；
// verify：CDP Page.captureScreenshot 截取当前客户端画面存档，
//         主题/面板是否真实生效以截图为准，不靠口头确认。
// ============================================================

// DoctorCheck 体检单项
type DoctorCheck struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

// Doctor 逐项自检（不修改任何状态，可随时运行）
func (m *Manager) Doctor() []DoctorCheck {
	cfg := m.svc.GetConfig().Inject
	port := cfg.Port
	if port <= 0 {
		port = 9223
	}
	checks := []DoctorCheck{}

	// 1. 客户端程序可定位
	bin, err := findClientBin(cfg.ClientPath)
	if err != nil {
		checks = append(checks, DoctorCheck{Name: m.tt("客户端程序", "Client binary"), OK: false, Detail: err.Error()})
	} else {
		checks = append(checks, DoctorCheck{Name: m.tt("客户端程序", "Client binary"), OK: true, Detail: bin})
	}

	// 2. CDP 调试端口
	targets, err := listTargets(port)
	if err != nil {
		checks = append(checks, DoctorCheck{Name: m.tt("CDP 端口 ", "CDP port ") + fmt.Sprint(port), OK: false,
			Detail: m.tt("不可达，客户端未以调试模式运行", "unreachable; the client is not running in debug mode")})
	} else {
		checks = append(checks, DoctorCheck{Name: m.tt("CDP 端口 ", "CDP port ") + fmt.Sprint(port), OK: true,
			Detail: fmt.Sprintf(m.tt("可达，%d 个调试目标", "reachable, %d debug targets"), len(targets))})
	}

	// 3. 注入连接
	m.mu.Lock()
	conn := m.conn
	target := m.target
	m.mu.Unlock()
	if conn == nil || !conn.alive() {
		checks = append(checks, DoctorCheck{Name: m.tt("注入连接", "Injection"), OK: false, Detail: m.tt("未连接（页面刷新/客户端未重启为正常现象，等待自动重连）", "not connected (normal after page reload or client restart; waiting for auto-reconnect)")})
		return checks // 后续项都依赖连接，到此为止
	}
	checks = append(checks, DoctorCheck{Name: m.tt("注入连接", "Injection"), OK: true, Detail: target.Title})

	// 4. 面板 DOM + 回传通道（一次 evaluate 全查）
	// 注意：returnByValue 下表达式直接返回对象即可，此前 JSON.stringify 返回的
	// 是字符串，Unmarshal 进结构体必然失败且被吞掉，导致体检恒报缺失（误报）。
	present, err := conn.evaluate(`(function(){
		return {
			fab: !!document.getElementById('wbdesk-fab'),
			panel: !!document.getElementById('wbdesk-panel'),
			api: typeof window.__wbdesk === 'function',
			cleanup: typeof window.__wbdeskCleanup === 'function'
		};
	})()`, 3*time.Second)
	if err != nil {
		checks = append(checks, DoctorCheck{Name: m.tt("面板状态", "Panel"), OK: false, Detail: m.tt("页面执行失败: ", "page evaluation failed: ") + err.Error()})
		return checks
	}
	var dom struct {
		Fab     bool `json:"fab"`
		Panel   bool `json:"panel"`
		API     bool `json:"api"`
		Cleanup bool `json:"cleanup"`
	}
	if err := json.Unmarshal(present, &dom); err != nil {
		checks = append(checks, DoctorCheck{Name: m.tt("面板状态", "Panel"), OK: false, Detail: m.tt("结果解析失败: ", "failed to parse result: ") + err.Error()})
		return checks
	}
	switch {
	case dom.Fab && dom.Panel && dom.API && dom.Cleanup:
		checks = append(checks, DoctorCheck{Name: m.tt("面板状态", "Panel"), OK: true, Detail: m.tt("悬浮机器人与面板 DOM 完整，回传通道正常", "fab and panel DOM intact; callback channel OK")})
	default:
		missing := []string{}
		if !dom.Fab {
			missing = append(missing, m.tt("机器人", "fab"))
		}
		if !dom.Panel {
			missing = append(missing, m.tt("面板", "panel"))
		}
		if !dom.API || !dom.Cleanup {
			missing = append(missing, m.tt("回传通道", "callback channel"))
		}
		checks = append(checks, DoctorCheck{Name: m.tt("面板状态", "Panel"), OK: false,
			Detail: m.tt("缺失: ", "missing: ") + joinCN(missing, m.tt("、", ", ")) + m.tt("（自动重连后可恢复）", " (recovers after auto-reconnect)")})
	}

	// 5. 主题注入标记
	theme, err := conn.evaluate(`(function(){
		return {
			mark: document.documentElement.getAttribute('data-wbdesk-theme') || '',
			style: !!document.getElementById('wbdesk-theme-style'),
			id: document.documentElement.getAttribute('data-wbdesk-theme-id') || ''
		};
	})()`, 3*time.Second)
	if err != nil {
		checks = append(checks, DoctorCheck{Name: m.tt("主题标记", "Theme"), OK: false, Detail: m.tt("页面执行失败: ", "page evaluation failed: ") + err.Error()})
	} else {
		var th struct {
			Mark  string `json:"mark"`
			Style bool   `json:"style"`
			ID    string `json:"id"`
		}
		if err := json.Unmarshal(theme, &th); err != nil {
			checks = append(checks, DoctorCheck{Name: m.tt("主题标记", "Theme"), OK: false, Detail: m.tt("结果解析失败: ", "failed to parse result: ") + err.Error()})
		} else if th.ID == "" {
			checks = append(checks, DoctorCheck{Name: m.tt("主题标记", "Theme"), OK: true, Detail: m.tt("未设置自定义主题（官方外观，属正常）", "no custom theme set (official appearance; normal)")})
		} else if th.Mark != "" && th.Style {
			checks = append(checks, DoctorCheck{Name: m.tt("主题标记", "Theme"), OK: true, Detail: m.tt("主题 ", "theme ") + th.ID + m.tt(" 已注入（标记与样式层完整）", " injected (mark and style layer complete)")})
		} else {
			checks = append(checks, DoctorCheck{Name: m.tt("主题标记", "Theme"), OK: false,
				Detail: m.tt("主题 ", "theme ") + th.ID + m.tt(" 标记不完整（可能客户端更新导致，重开面板或重启注入可修复）", " mark incomplete (possibly caused by a client update; reopen the panel or restart injection to fix)")})
		}
	}

	// 6. 原生输入框可定位（面板直填/引用功能依赖）
	input, err := conn.evaluate(`(function(){
		var l = document.querySelectorAll('textarea,[contenteditable="true"]');
		var n = 0;
		for (var i = 0; i < l.length; i++) {
			var r = l[i].getBoundingClientRect();
			if (r.width > 0 && r.height > 0) n++;
		}
		return { input: n, list: !!document.querySelector('div.cr-message-list') };
	})()`, 3*time.Second)
	if err != nil {
		checks = append(checks, DoctorCheck{Name: m.tt("原生控件", "Native controls"), OK: false, Detail: m.tt("页面执行失败: ", "page evaluation failed: ") + err.Error()})
	} else {
		var nat struct {
			Input int  `json:"input"`
			List  bool `json:"list"`
		}
		if err := json.Unmarshal(input, &nat); err != nil {
			checks = append(checks, DoctorCheck{Name: m.tt("原生控件", "Native controls"), OK: false, Detail: m.tt("结果解析失败: ", "failed to parse result: ") + err.Error()})
		} else if nat.Input > 0 {
			detail := fmt.Sprintf(m.tt("可见输入框 %d 个", "%d visible input box(es)"), nat.Input)
			if !nat.List {
				detail += m.tt("；未找到会话消息列表（当前页面可能不是会话视图）", "; message list not found (current page may not be a conversation view)")
			}
			checks = append(checks, DoctorCheck{Name: m.tt("原生控件", "Native controls"), OK: true, Detail: detail})
		} else {
			checks = append(checks, DoctorCheck{Name: m.tt("原生控件", "Native controls"), OK: false,
				Detail: m.tt("未找到可见输入框（提示词直填/引用不可用，客户端更新可能改动了 DOM）", "no visible input box found (prompt fill / quote unavailable; a client update may have changed the DOM)")})
		}
	}
	return checks
}

// runPanelDoctor 面板触发体检，报告回推面板展示（同时记入桌面端事件日志）
func (m *Manager) runPanelDoctor() {
	report := m.Doctor()
	raw, _ := json.Marshal(report)
	m.mu.Lock()
	conn := m.conn
	m.mu.Unlock()
	if conn == nil {
		return
	}
	_, _ = conn.evaluate(`window.__wbdeskDoctorReport && window.__wbdeskDoctorReport(`+string(raw)+`)`, 5*time.Second)
	core.EmitEvent(core.EventInjectStatus, map[string]any{"kind": "doctor", "total": len(report)})
}

// VerifyScreenshot 用 CDP 截取客户端当前画面并存档（覆盖写 verify.png）
func (m *Manager) VerifyScreenshot() (string, error) {
	m.mu.Lock()
	conn := m.conn
	m.mu.Unlock()
	if conn == nil {
		return "", fmt.Errorf("%s", m.tt("注入未运行，无法截图", "injection not running; cannot capture"))
	}
	if _, err := conn.call("Page.enable", map[string]any{}, 3*time.Second); err != nil {
		return "", fmt.Errorf("%s: %w", m.tt("启用 Page 域失败", "failed to enable Page domain"), err)
	}
	res, err := conn.call("Page.captureScreenshot", map[string]any{"format": "png"}, 10*time.Second)
	if err != nil {
		return "", fmt.Errorf("%s: %w", m.tt("截图失败", "capture failed"), err)
	}
	var shot struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(res, &shot); err != nil || shot.Data == "" {
		return "", fmt.Errorf("%s", m.tt("截图数据无效", "invalid screenshot data"))
	}
	dir := filepath.Join(filepath.Dir(m.svc.ConfigPath()), "inject")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "verify.png")
	raw, err := base64.StdEncoding.DecodeString(shot.Data)
	if err != nil {
		return "", fmt.Errorf("%s: %w", m.tt("截图解码失败", "failed to decode screenshot"), err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return "", err
	}
	core.EmitEvent(core.EventInjectStatus, map[string]any{"kind": "verify", "path": path})
	return path, nil
}

// runPanelVerify 面板触发验证截图，成功后 toast 提示保存路径
func (m *Manager) runPanelVerify() {
	path, err := m.VerifyScreenshot()
	if err != nil {
		m.reportError(m.tt("验证截图失败", "Verify screenshot failed"), err)
		return
	}
	m.mu.Lock()
	conn := m.conn
	m.mu.Unlock()
	if conn == nil {
		return
	}
	msg, _ := json.Marshal(fmt.Sprintf(m.tt("验证截图已保存：%s", "Verify screenshot saved: %s"), path))
	_, _ = conn.evaluate(`window.__wbdeskToast && window.__wbdeskToast(`+string(msg)+`)`, 3*time.Second)
}

// joinCN 顿号/逗号连接（体检报告缺失项拼接，分隔符随面板语言）
func joinCN(items []string, sep string) string {
	out := ""
	for i, s := range items {
		if i > 0 {
			out += sep
		}
		out += s
	}
	return out
}

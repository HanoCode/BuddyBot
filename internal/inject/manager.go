package inject

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"embed"

	"workbuddy-desktop/internal/core"
)

//go:embed panel.js
var panelJS string

//go:embed wallpapers/*.webp
var wallFS embed.FS

// ============================================================
// 注入管理器：CDP 连接生命周期 + 面板注入 + 账号备份/切换
//
// 依赖 coder/websocket（wails 既有间接依赖），零新增第三方包。
// ============================================================

// AccountBackup 账号登录态备份（官方登录文件快照；legacy 条目为 localStorage 快照）
type AccountBackup struct {
	ID    string `json:"id"`             // 文件名（去扩展名）
	Name  string `json:"name"`           // 用户备注名
	UID   string `json:"uid"`            // 自动识别的账号标识（识别失败为空）
	Realm string `json:"realm,omitempty"` // 备份账号区域（cn/global）
	Time  string `json:"time"`           // 备份时间
	Size  int    `json:"size"`           // 字节数
}

// Manager 注入管理器
type Manager struct {
	svc *core.Service

	// promptsJSON 提效指令库数据（main.go 从 frontend/src/data/prompts.json
	// 内嵌传入，单一数据源：桌面端「提效指令库」页与注入面板共用）
	promptsJSON []byte

	// locale 注入面板语言（zh/en）：默认从客户端 URL ?locale= 识别（仅窗口创建
	// 时注入，实时切语言不更新）；面板「面板语言」开关上报后以此为准
	locale string

	mu      sync.Mutex
	conn    *cdpConn
	port    int
	target  Target
	started time.Time
}

// NewManager 创建注入管理器（promptsJSON 可为空，面板「指令」Tab 降级为空态）
func NewManager(svc *core.Service, promptsJSON []byte) *Manager {
	return &Manager{svc: svc, promptsJSON: promptsJSON}
}

// StartSupervisor 启动自动重连守护：
//   - 应用启动时若注入开关开启，先做一次完整恢复：客户端未以调试模式运行时
//     优雅退出并带 CDP 重启（quitClient 已保证会话保存），客户端未运行则直接拉起；
//   - 之后每 20 秒巡检：注入开关开启且 CDP 端口可达但连接已断（页面刷新 /
//     客户端重启）时自动重新附着；巡检不主动杀客户端（避免打扰正常使用）；
//   - 运行中每分钟重推一次账号池余额，保证面板积分准实时。
//
// 只依赖配置与连接状态，随时启动，重复调用无副作用。
func (m *Manager) StartSupervisor() {
	go func() {
		// 启动恢复：等应用与配置就绪后尝试一次完整恢复注入
		time.Sleep(5 * time.Second)
		if m.svc.GetConfig().Inject.Enabled {
			if err := m.Start(); err == nil {
				core.EmitEvent(core.EventInjectStatus, map[string]any{"kind": "restored"})
			} else {
				core.EmitEvent(core.EventInjectStatus, map[string]any{"kind": "restore_failed", "message": err.Error()})
			}
		}
		lastPush := time.Time{}
		for {
			time.Sleep(20 * time.Second)
			cfg := m.svc.GetConfig().Inject
			if !cfg.Enabled {
				continue
			}
			port := cfg.Port
			if port <= 0 {
				port = 9223
			}
			if st := m.Status(); st.Running {
				if time.Since(lastPush) >= time.Minute {
					m.mu.Lock()
					conn := m.conn
					m.mu.Unlock()
					if conn != nil {
						pool, _ := json.Marshal(m.poolSummary())
						_, _ = conn.evaluate(`window.__wbdeskSetPool && window.__wbdeskSetPool(`+string(pool)+`)`, 5*time.Second)
						m.pushPanelBadge(conn)
						lastPush = time.Now()
					}
				}
				continue
			}
			if !cdpAlive(port) {
				continue
			}
			if err := m.Start(); err == nil {
				lastPush = time.Now()
			}
		}
	}()
}

// ctx CDP 连接的根上下文（当前无应用级取消源，用 Background）
func (m *Manager) ctx() context.Context { return context.Background() }

// localeOf 从客户端页面 URL 提取语言：?locale=en-US → en，其余（含缺省）→ zh。
// 注意：主进程只在窗口创建时把 locale 写进 URL，运行中实时切语言不刷新页面，
// 该值不随之更新；面板语言以「面板语言」开关的 panel_lang 上报为准。
func localeOf(rawURL string) string {
	i := strings.Index(rawURL, "locale=")
	if i >= 0 {
		v := rawURL[i+len("locale="):]
		if j := strings.IndexByte(v, '&'); j >= 0 {
			v = v[:j]
		}
		if strings.HasPrefix(strings.ToLower(v), "en") {
			return "en"
		}
	}
	return "zh"
}

// tt 注入面板文案双语：面板语言为英文时返回 en 文案，否则 zh。
func (m *Manager) tt(zh, en string) string {
	if m.locale == "en" {
		return en
	}
	return zh
}

// Start 启动注入：确保客户端带 CDP 运行 → 附着主窗口 → 注入面板
func (m *Manager) Start() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.conn != nil {
		if m.conn.alive() {
			// WS 存活 ≠ 面板还在：DevTools WebSocket 附着的是页面目标，
			// 页面刷新/重登/账号切换（location.reload）不会断开连接，
			// 但注入的悬浮机器人 DOM 已随页面清空。校验面板存在性，
			// 缺失就在原连接上重注入（复用 WS，Runtime.addBinding 随会话存活）。
			present, err := m.conn.evaluate(
				`!!(window.__wbdeskCleanup&&document.getElementById('wbdesk-fab'))`, 3*time.Second)
			if err == nil && string(present) == "true" {
				return nil // 面板完好，已在运行
			}
			if err == nil {
				// 面板丢失（页面刷新过）：原连接重注入 + 重推状态
				if _, ierr := m.conn.evaluate(panelExpr(), 15*time.Second); ierr == nil {
					if ierr = m.pushPanelState(m.conn); ierr == nil {
						go m.restoreTheme()
						core.EmitEvent(core.EventInjectStatus, map[string]any{"kind": "recovered"})
						return nil
					}
				}
			}
			// 连接已不可用或重注入失败：关闭后走完整启动
			m.conn.close()
			m.conn, m.target, m.started = nil, Target{}, time.Time{}
		} else {
			// 连接已死（客户端关闭）：清理残留状态后重走启动流程
			m.conn, m.target, m.started = nil, Target{}, time.Time{}
		}
	}
	cfg := m.svc.GetConfig().Inject
	port := cfg.Port
	if port <= 0 {
		port = 9223
	}
	bin, err := findClientBin(cfg.ClientPath)
	if err != nil {
		return err
	}
	if err := ensureCDPPort(port, bin); err != nil {
		return err
	}
	target, err := pickPageTarget(port)
	if err != nil {
		return err
	}
	m.locale = localeOf(target.URL)
	conn, err := dialCDP(m.ctx(), port, target.ID, m.onBinding)
	if err != nil {
		return err
	}
	if _, err := conn.call("Runtime.enable", map[string]any{}, 5*time.Second); err != nil {
		conn.close()
		return err
	}
	if _, err := conn.call("Runtime.addBinding", map[string]any{"name": bindingName()}, 5*time.Second); err != nil {
		conn.close()
		return fmt.Errorf("注册面板回传通道失败: %w", err)
	}
	if _, err := conn.evaluate(panelExpr(), 15*time.Second); err != nil {
		conn.close()
		return fmt.Errorf("注入面板失败: %w", err)
	}
	if err := m.pushPanelState(conn); err != nil {
		conn.close()
		return err
	}
	// 恢复已保存的 WorkBuddy 主题（页面刷新/客户端重启后主题不丢）
	go m.restoreTheme()

	m.conn = conn
	m.port = port
	m.target = target
	m.started = time.Now()
	core.EmitEvent(core.EventInjectStatus, map[string]any{"kind": "started", "target": target.Title})
	return nil
}

// Stop 停止注入：清理页面内面板并断开
func (m *Manager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.conn == nil {
		return
	}
	_, _ = m.conn.evaluate(`window.__wbdeskCleanup && window.__wbdeskCleanup()`, 2*time.Second)
	m.conn.close()
	m.conn, m.target, m.started = nil, Target{}, time.Time{}
	core.EmitEvent(core.EventInjectStatus, map[string]any{"kind": "stopped"})
}

// Status 当前注入运行态
type Status struct {
	Running bool   `json:"running"`
	Target  string `json:"target,omitempty"`
	Port    int    `json:"port,omitempty"`
	Since   string `json:"since,omitempty"`
	DND     bool   `json:"dnd"`
}

// Status 返回运行态（连接已死的残留状态在此自愈，UI 不再显示假「运行中」）
func (m *Manager) Status() Status {
	m.mu.Lock()
	conn := m.conn
	if conn != nil && !conn.alive() {
		// 读循环已退出（页面刷新/客户端关闭/网络断开）：清理并广播
		m.conn, m.target, m.started = nil, Target{}, time.Time{}
		conn = nil
		m.mu.Unlock()
		core.EmitEvent(core.EventInjectStatus, map[string]any{"kind": "disconnected"})
	} else {
		m.mu.Unlock()
	}
	st := Status{
		Running: conn != nil,
		Port:    m.port,
		DND:     m.svc.GetConfig().Inject.DNDAutoConfirm,
	}
	if conn != nil {
		st.Target = m.target.Title
		st.Since = m.started.Format("2006-01-02 15:04:05")
	}
	return st
}

// ---------- 面板回传处理 ----------

func bindingName() string { return "__wbdesk" }

// panelExpr 以 base64 包装面板脚本，规避 evaluate 的转义问题。
// 注意：atob 只能产出 Latin-1 串，必须经 UTF-8 解码还原多字节中文，
// 否则面板内嵌中文全部变乱码。
func panelExpr() string {
	return `(0,eval)(new TextDecoder().decode(Uint8Array.from(atob("` +
		base64.StdEncoding.EncodeToString([]byte(panelJS)) +
		`"), function(c){return c.charCodeAt(0)})))`
}

// onBinding 面板动作回传（读循环 goroutine 调用）：动作放后台执行，绝不阻塞读循环。
// 关键：面板构建完成时会同步 send("panel_lang")，若在这里同步 m.mu.Lock()，
// 而 Start() 注入面板期间持有 m.mu，读循环会被卡死，evaluate 应答无法路由，
// 最终 Start 超时 → conn.close() 等读循环退出 → 死锁（UI 永久转圈）。
func (m *Manager) onBinding(name, payload string) {
	if name != bindingName() {
		return
	}
	go m.handleBinding(payload)
}

// handleBinding 真正的面板动作分发（独立 goroutine，与读循环解耦）
func (m *Manager) handleBinding(payload string) {
	var p struct {
		Action string `json:"action"`
		On     bool   `json:"on"`
		Name   string `json:"name"`
		ID     string `json:"id"`
		UID    string `json:"uid"`
		Text   string `json:"text"`
		Task   string `json:"task"`
		Key    string `json:"key"`
		Wall   string `json:"wall"`
		Mask   *int   `json:"mask"`
		Blur   *int   `json:"blur"`
		Data   string `json:"dataUrl"`
		Sprite string `json:"spriteUrl"` // pet_add：自定义宠物精灵图 dataURL
		Desc   string `json:"desc"`      // wall_ai：AI 生成壁纸的一句话描述
		Model  string `json:"model"`     // wall_ai：面板选定的网关模型
		Lang   string `json:"lang"`      // panel_lang：面板语言偏好（""=自动跟随客户端）
	}
	if json.Unmarshal([]byte(payload), &p) != nil {
		return
	}
	switch p.Action {
	case "dnd":
		_ = m.svc.UpdateInjectConfig(func(c *core.InjectConfig) { c.DNDAutoConfirm = p.On })
	case "enh":
		// 会话自动归档：配置在 Schedule.SessionArchive（非注入设置），单独落盘
		if p.Key == "arch" {
			_ = m.svc.UpdateSessionArchiveConfig(func(c *core.SessionArchiveConfig) { c.Enabled = p.On })
			return
		}
		// 增强开关（白名单键，防止任意写配置）
		_ = m.svc.UpdateInjectConfig(func(c *core.InjectConfig) {
			switch p.Key {
			case "dnd":
				c.DNDAutoConfirm = p.On
			case "file":
				c.AllowFileWrite = p.On
			case "cmd":
				c.AllowCommands = p.On
			case "del":
				c.AllowBatchDelete = p.On
			case "sys":
				c.AllowSystemTools = p.On
			case "resume":
				c.AutoResumeSession = p.On
			case "quote":
				c.QuoteMessage = p.On
			case "nav":
				c.MessageNav = p.On
			}
		})
	case "backup":
		if _, err := m.Backup(p.Name); err != nil {
			m.reportError(m.tt("备份登录态失败", "Failed to back up login"), err)
		} else {
			m.refreshPanelAccounts()
		}
	case "list":
		m.refreshPanelAccounts()
	case "pool":
		m.refreshPanelPool()
	case "client_import":
		// 面板「检测到官方客户端已登录」提示条的一键导入：复制发现的明文凭证进 auth_dir
		go func() {
			sess := m.svc.DetectClientSession()
			file, err := m.svc.ImportClientSession()
			if err != nil {
				m.reportError(m.tt("导入客户端登录账号失败", "Failed to import client login"), err)
				return
			}
			m.svc.Store().Audit("account.import_client_session", sess.UID, "from="+sess.CredFile)
			core.EmitEvent(core.EventAccountStatus, map[string]any{
				"uid": sess.UID, "status": "imported", "credential": file,
			})
			// 导入成功：面板内提示 + 重推池/会话（账号已在池中，提示条会自动消失）
			m.mu.Lock()
			conn := m.conn
			m.mu.Unlock()
			name := sess.Nickname
			if name == "" {
				name = sess.UID
			}
			if conn != nil {
				raw, _ := json.Marshal(fmt.Sprintf(m.tt("已导入 %s 到账号池", "Imported %s into the account pool"), name))
				_, _ = conn.evaluate(`window.__wbdeskToast && window.__wbdeskToast(`+string(raw)+`)`, 3*time.Second)
			}
			m.refreshPanelAccounts()
		}()
	case "switch":
		if err := m.Switch(p.ID); err != nil {
			// 页面会 reload，重连由用户重新点击「启动注入」触发（或后续自动重连）
			m.reportError(m.tt("切换账号失败", "Failed to switch account"), err)
		}
	case "switch_uid":
		// 面板直接切换登录账号：走客户端切换流水线（备份→退出→写入→重启），进度在桌面端可见
		go func() {
			if err := m.svc.ClientSwitchAccount(p.UID); err != nil {
				m.reportError(m.tt("切换登录账号失败", "Failed to switch login account"), err)
			}
		}()
	case "switch_next":
		// 面板一键换号：自动挑选下一个在线账号切换（客户端会重启，连接随之断开）
		go m.switchNextAccount()
	case "insert_text":
		// 把指令/引用文本写入对话输入框（Slate 编辑器必须走 CDP 受信任输入，见 chatinput.go）
		go func() {
			if err := m.insertChatText(p.Text); err != nil {
				m.reportError(m.tt("填入输入框失败", "Failed to fill into input box"), err)
			}
		}()
	case "prompts":
		// 面板「指令」Tab 请求提效指令库（按需推送一次）
		go m.pushPanelPromptsConn()
	case "del_backup":
		if err := m.Delete(p.ID); err != nil {
			m.reportError(m.tt("删除备份失败", "Failed to delete backup"), err)
		} else {
			m.refreshPanelAccounts()
		}
	case "task":
		m.runPanelTask(p.Task)
	case "task_all":
		m.runPanelTaskAll()
	case "tasks":
		m.refreshPanelTasks()
	case "models":
		m.refreshPanelModels()
	case "walls":
		// 面板主题页请求壁纸库（内置 + 自定义，按需推送，注入本身不携带图片数据）
		go m.pushPanelWalls()
	case "pets":
		// 面板请求内置宠物库（含 spritesheet dataURL，按需推送）
		go m.pushPanelPets()
	case "pet_apply":
		// 切换悬浮机器人皮肤（"" = 经典 CSS 机器人）
		go func() {
			if err := m.ApplyPet(p.ID); err != nil {
				m.reportError(m.tt("切换宠物失败", "Failed to switch pet"), err)
			}
		}()
	case "pet_add":
		// 上传自定义宠物（面板已裁好 preview 帧），成功后刷新宠物库并自动应用
		go func() {
			id, err := m.AddCustomPet(p.Name, p.Data, p.Sprite)
			if err != nil {
				m.reportError(m.tt("添加自定义宠物失败", "Failed to add custom pet"), err)
				return
			}
			if err := m.ApplyPet(id); err != nil {
				m.reportError(m.tt("应用自定义宠物失败", "Failed to apply custom pet"), err)
			}
			m.pushPanelPets()
		}()
	case "pet_del":
		// 删除自定义宠物；若正在使用则重置为经典机器人
		go func() {
			if err := m.DeleteCustomPet(p.ID); err != nil {
				m.reportError(m.tt("删除自定义宠物失败", "Failed to delete custom pet"), err)
				return
			}
			m.pushPanelPets()
			m.mu.Lock()
			conn := m.conn
			m.mu.Unlock()
			if conn != nil {
				m.pushPanelPet(conn)
			}
		}()
	case "theme_state":
		// 面板请求主题状态（注入后 pushPanelState 已推过一次，这里供手动刷新）
		m.mu.Lock()
		conn := m.conn
		m.mu.Unlock()
		if conn != nil {
			m.pushPanelTheme(conn)
		}
	case "theme_apply":
		// 切换主题外观（default/dark 仅联动官方外观，其余注入色板+补丁）
		go func() {
			th := m.svc.GetConfig().Inject.Theme
			th.ID = p.ID
			if err := m.ApplyTheme(th); err != nil {
				m.reportError(m.tt("应用主题失败", "Failed to apply theme"), err)
			}
		}()
	case "theme_wall":
		// 切换壁纸（"" = 无壁纸）
		go func() {
			th := m.svc.GetConfig().Inject.Theme
			th.Wallpaper = p.Wall
			if err := m.ApplyTheme(th); err != nil {
				m.reportError(m.tt("应用壁纸失败", "Failed to apply wallpaper"), err)
			}
		}()
	case "theme_cfg":
		// 蒙版 / 毛玻璃 / 文字阴影（指针字段区分「未传」与「传 0」）
		go func() {
			th := m.svc.GetConfig().Inject.Theme
			if p.Mask != nil {
				th.Mask = clampInt(*p.Mask, 0, 100)
			}
			if p.Blur != nil {
				th.Blur = clampInt(*p.Blur, 0, 100)
			}
			th.TextShadow = p.On
			if err := m.ApplyTheme(th); err != nil {
				m.reportError(m.tt("主题设置失败", "Failed to update theme"), err)
			}
		}()
	case "wall_add":
		// 自定义壁纸上传（面板已压缩为 webp dataURL）：落盘 → 选中 → 重应用
		go func() {
			name, err := m.addCustomWallpaper(p.Data)
			if err == nil {
				th := m.svc.GetConfig().Inject.Theme
				th.Wallpaper = "custom:" + name
				err = m.ApplyTheme(th)
			}
			if err != nil {
				m.reportError(m.tt("添加壁纸失败", "Failed to add wallpaper"), err)
			}
			m.pushPanelWalls()
		}()
	case "wall_del":
		// 删除自定义壁纸（面板传完整引用 custom:<name>）；若正在使用则清空引用并重应用
		go func() {
			name := strings.TrimPrefix(p.ID, "custom:")
			th := m.svc.GetConfig().Inject.Theme
			if th.Wallpaper == "custom:"+name {
				th.Wallpaper = ""
				_ = m.ApplyTheme(th)
			}
			if err := m.deleteCustomWallpaper(name); err != nil {
				m.reportError(m.tt("删除壁纸失败", "Failed to delete wallpaper"), err)
			}
			m.pushPanelWalls()
		}()
	case "panel_lang":
		// 面板语言偏好上报（面板构建时与切换时都会发）：""=自动跟随客户端 locale
		switch p.Lang {
		case "en", "zh":
			m.mu.Lock()
			m.locale = p.Lang
			m.mu.Unlock()
		case "":
			m.mu.Lock()
			m.locale = localeOf(m.target.URL)
			m.mu.Unlock()
		}
	case "awake":
		// 面板防休眠开关
		go func() {
			if err := m.svc.SetAwake(p.On); err != nil {
				m.reportError(m.tt("防休眠设置失败", "Failed to set keep-awake"), err)
			}
			m.mu.Lock()
			conn := m.conn
			m.mu.Unlock()
			if conn != nil {
				m.pushPanelSys(conn)
			}
		}()
	case "doctor":
		// 面板「注入体检」：CDP/面板/主题/原生控件逐项自检，结果回推面板展示
		go m.runPanelDoctor()
	case "verify":
		// 面板「验证截图」：CDP captureScreenshot 存档并在面板提示路径
		go m.runPanelVerify()
	case "wall_ai":
		// 面板「AI 生成壁纸」：一句话描述 → 本机网关对话模型产出 SVG banner
		go m.handleWallAI(p.Desc, p.Model)
	case "dnd_clicked":
		core.EmitEvent(core.EventInjectStatus, map[string]any{"kind": "dnd_click", "text": p.Text})
	case "enh_clicked":
		core.EmitEvent(core.EventInjectStatus, map[string]any{"kind": "enh_click", "key": p.Key, "text": p.Text})
	}
}

// ---------- 面板「任务」「模型」通道（参考 WorkDaddy 多 Tab 面板） ----------

// panelTaskRun 任务运行摘要（SchedulerStatus.History 的精简投影）
type panelTaskRun struct {
	Type      string  `json:"type"`
	StartedAt string  `json:"startedAt"`
	Duration  float64 `json:"duration"` // ms
	Total     int     `json:"total"`
	Success   int     `json:"success"`
	Failed    int     `json:"failed"`
	Skipped   int     `json:"skipped"`
}

// panelTasksView 任务中心视图：执行中的任务 + 最近运行记录
type panelTasksView struct {
	Busy    []string       `json:"busy"`
	History []panelTaskRun `json:"history"`
}

// panelTaskTypes 面板可执行的任务（与桌面端任务中心一致的全池口径）
var panelTaskTypes = []string{"checkin", "travel", "keepalive", "growth", "activity", "school", "cat"}

// panelTasks 汇总任务视图
func (m *Manager) panelTasks() panelTasksView {
	st := m.svc.Scheduler().Status()
	view := panelTasksView{Busy: st.Busy, History: []panelTaskRun{}}
	for i, r := range st.History {
		if i >= 6 {
			break // 面板只看最近 6 条
		}
		view.History = append(view.History, panelTaskRun{
			Type: r.Type, StartedAt: r.StartedAt, Duration: r.Duration,
			Total: r.Total, Success: r.Success, Failed: r.Failed, Skipped: r.Skipped,
		})
	}
	return view
}

// pushPanelTasks 把任务视图推给面板
func (m *Manager) pushPanelTasks(conn *cdpConn) {
	raw, _ := json.Marshal(m.panelTasks())
	_, _ = conn.evaluate(`window.__wbdeskSetTasks && window.__wbdeskSetTasks(`+string(raw)+`)`, 5*time.Second)
}

// runPanelTask 面板一键执行任务（全池口径，后台跑完推回结果）
func (m *Manager) runPanelTask(taskType string) {
	valid := false
	for _, t := range panelTaskTypes {
		if t == taskType {
			valid = true
			break
		}
	}
	if !valid {
		return
	}
	for _, b := range m.svc.Scheduler().Status().Busy {
		if b == taskType {
			return // 已在执行，忽略重复触发
		}
	}
	go func() {
		_ = m.svc.Scheduler().RunFor(taskType, "panel", nil)
		m.mu.Lock()
		conn := m.conn
		m.mu.Unlock()
		if conn != nil {
			m.pushPanelTasks(conn)
		}
	}()
}

// runPanelTaskAll 面板「一键全部执行」：按面板任务清单串行执行（提交时已在跑的任务跳过）
func (m *Manager) runPanelTaskAll() {
	busy := map[string]bool{}
	for _, b := range m.svc.Scheduler().Status().Busy {
		busy[b] = true
	}
	queue := make([]string, 0, len(panelTaskTypes))
	for _, t := range panelTaskTypes {
		if !busy[t] {
			queue = append(queue, t)
		}
	}
	if len(queue) == 0 {
		return
	}
	go func() {
		for _, taskType := range queue {
			_ = m.svc.Scheduler().RunFor(taskType, "panel", nil)
			m.mu.Lock()
			conn := m.conn
			m.mu.Unlock()
			if conn == nil {
				return // 连接已断，剩余任务不再推进
			}
			m.pushPanelTasks(conn)
		}
	}()
}

// pushPanelSys 推送系统状态（防休眠 / 网关地址）给面板
func (m *Manager) pushPanelSys(conn *cdpConn) {
	sys, _ := json.Marshal(map[string]any{
		"awake": m.svc.AwakeActive(),
		"url":   m.svc.GatewayBaseURL(),
	})
	_, _ = conn.evaluate(`window.__wbdeskSetSys && window.__wbdeskSetSys(`+string(sys)+`)`, 5*time.Second)
}

// panelModelView 模型视图（网关可用模型 + 本机用量）
type panelModelView struct {
	ID       string `json:"id"`
	Requests int    `json:"requests"`
	Tokens   int    `json:"tokens"`
	Observed bool   `json:"observed"`
}

// pushPanelModels 推送网关接入地址 + 模型目录
func (m *Manager) pushPanelModels(conn *cdpConn) {
	type gatewayView struct {
		URL    string           `json:"url"`
		Models []panelModelView `json:"models"`
	}
	out := gatewayView{URL: m.svc.GatewayBaseURL(), Models: []panelModelView{}}
	for _, mo := range core.ListModels(m.svc.Store()) {
		out.Models = append(out.Models, panelModelView{
			ID: mo.ID, Requests: mo.Requests, Tokens: mo.Tokens, Observed: mo.Observed,
		})
	}
	raw, _ := json.Marshal(out)
	_, _ = conn.evaluate(`window.__wbdeskSetModels && window.__wbdeskSetModels(`+string(raw)+`)`, 5*time.Second)
}

// refreshPanelTasks 面板请求刷新任务视图
func (m *Manager) refreshPanelTasks() {
	m.mu.Lock()
	conn := m.conn
	m.mu.Unlock()
	if conn != nil {
		m.pushPanelTasks(conn)
	}
}

// refreshPanelModels 面板请求刷新模型列表
func (m *Manager) refreshPanelModels() {
	m.mu.Lock()
	conn := m.conn
	m.mu.Unlock()
	if conn != nil {
		m.pushPanelModels(conn)
	}
}

// reportError 把面板动作失败上报到桌面端，并在注入页面内弹 toast（面板里立即可见）
func (m *Manager) reportError(what string, err error) {
	msg := what + ": " + err.Error()
	core.EmitEvent(core.EventInjectStatus, map[string]any{"kind": "error", "message": msg})
	m.mu.Lock()
	conn := m.conn
	m.mu.Unlock()
	if conn != nil {
		raw, _ := json.Marshal(msg)
		_, _ = conn.evaluate(`window.__wbdeskToast && window.__wbdeskToast(`+string(raw)+`)`, 3*time.Second)
	}
}

// panelEnhView 注入面板「设置」Tab 增强开关视图
type panelEnhView struct {
	Dnd    bool `json:"dnd"`
	File   bool `json:"file"`
	Cmd    bool `json:"cmd"`
	Del    bool `json:"del"`
	Sys    bool `json:"sys"`
	Resume bool `json:"resume"`
	Quote  bool `json:"quote"`
	Nav    bool `json:"nav"`
	// Arch 会话自动归档（配置在 Schedule.SessionArchive，与注入设置同面板展示）
	Arch bool `json:"arch"`
}

// pushPanelState 注入/刷新后把配置与账号列表同步给面板
func (m *Manager) pushPanelState(conn *cdpConn) error {
	c := m.svc.GetConfig()
	inj := c.Inject
	enh, _ := json.Marshal(panelEnhView{
		Dnd: inj.DNDAutoConfirm, File: inj.AllowFileWrite, Cmd: inj.AllowCommands,
		Del: inj.AllowBatchDelete, Sys: inj.AllowSystemTools,
		Resume: inj.AutoResumeSession, Quote: inj.QuoteMessage, Nav: inj.MessageNav,
		Arch: c.Schedule.SessionArchive.Enabled,
	})
	accs, _ := m.List()
	raw, _ := json.Marshal(accs)
	pool, _ := json.Marshal(m.poolSummary())
	expr := fmt.Sprintf(
		`window.__wbdeskSetEnh && window.__wbdeskSetEnh(%s);`+
			`window.__wbdeskSetAccounts && window.__wbdeskSetAccounts(%s);`+
			`window.__wbdeskSetPool && window.__wbdeskSetPool(%s);`,
		string(enh), string(raw), string(pool),
	)
	if _, err := conn.evaluate(expr, 5*time.Second); err != nil {
		return err
	}
	m.pushPanelTasks(conn)
	m.pushPanelModels(conn)
	m.pushPanelSys(conn)
	m.pushPanelTheme(conn)
	m.pushPanelPet(conn)
	m.pushPanelBadge(conn)
	m.pushPanelClientSession(conn)
	return nil
}

// poolSummary 账号池余额摘要（注入面板「账号池积分」区块的数据源）
type poolAccountView struct {
	UID             string  `json:"uid"`
	Name            string  `json:"name"`
	Credits         float64 `json:"credits"`
	CreditsKnown    bool    `json:"creditsKnown"`
	CreditsExpiring float64 `json:"creditsExpiring"`
	Current         bool    `json:"current"` // 是否为官方客户端当前登录账号
}

func (m *Manager) poolSummary() []poolAccountView {
	out := []poolAccountView{}
	// 官方当前登录账号：解析官方固定登录位（失败不阻断，仅失去「当前」标记）
	currentUID := ""
	if authFile, err := core.OfficialClientAuthFile(); err == nil {
		if raw, rerr := os.ReadFile(authFile); rerr == nil {
			if cred, perr := core.ParseCredential(authFile, raw); perr == nil && cred != nil {
				currentUID = cred.UID
			}
		}
	}
	for _, a := range m.svc.Accounts() {
		name := a.Nickname
		if name == "" {
			name = a.UID
		}
		out = append(out, poolAccountView{
			UID: a.UID, Name: name,
			Credits: a.Credits, CreditsKnown: a.CreditsKnown,
			CreditsExpiring: a.CreditsExpiring,
			Current:         a.UID != "" && a.UID == currentUID,
		})
	}
	return out
}

// panelClientSessionView 注入面板「检测到官方客户端已登录」提示条视图
type panelClientSessionView struct {
	Detected bool   `json:"detected"`         // 登录位存在且能读到 uid
	UID      string `json:"uid,omitempty"`    // 当前登录账号 uid
	Nickname string `json:"nickname"`         // 昵称（取自已发现的凭证，可能为空）
	HasCred  bool   `json:"hasCred"`          // 发现目录里找到同 uid 明文凭证（可一键导入）
	InPool   bool   `json:"inPool"`           // 该 uid 已在账号池（无需导入）
}

// pushPanelClientSession 推送客户端登录会话探测结果（只读磁盘，成本低）
func (m *Manager) pushPanelClientSession(conn *cdpConn) {
	sess := m.svc.DetectClientSession()
	v := panelClientSessionView{
		Detected: sess.Detected, UID: sess.UID, Nickname: sess.Nickname,
		HasCred: sess.CredFile != "", InPool: sess.InPool,
	}
	raw, _ := json.Marshal(v)
	_, _ = conn.evaluate(`window.__wbdeskSetClientSession && window.__wbdeskSetClientSession(`+string(raw)+`)`, 5*time.Second)
}

// refreshPanelPool 面板请求刷新账号池余额
func (m *Manager) refreshPanelPool() {
	m.mu.Lock()
	conn := m.conn
	m.mu.Unlock()
	if conn == nil {
		return
	}
	pool, _ := json.Marshal(m.poolSummary())
	_, _ = conn.evaluate(`window.__wbdeskSetPool && window.__wbdeskSetPool(`+string(pool)+`)`, 5*time.Second)
	m.pushPanelClientSession(conn)
}

// refreshPanelAccounts 面板主动刷新账号列表
func (m *Manager) refreshPanelAccounts() {
	m.mu.Lock()
	conn := m.conn
	m.mu.Unlock()
	if conn != nil {
		_ = m.pushPanelState(conn)
	}
}

// switchNextAccount 面板「一键换号」：从账号池挑下一个在线账号（当前登录除外，
// 已知积分高者优先），走客户端切换流水线。客户端会重启，CDP 连接随之断开，
// 由 StartSupervisor 自动重连恢复面板。
func (m *Manager) switchNextAccount() {
	currentUID := ""
	if authFile, err := core.OfficialClientAuthFile(); err == nil {
		if raw, rerr := os.ReadFile(authFile); rerr == nil {
			if cred, perr := core.ParseCredential(authFile, raw); perr == nil && cred != nil {
				currentUID = cred.UID
			}
		}
	}
	cands := []core.Account{}
	for _, a := range m.svc.Accounts() {
		if a.Status != "online" || a.UID == "" || a.UID == currentUID {
			continue
		}
		cands = append(cands, a)
	}
	if len(cands) == 0 {
		m.reportError(m.tt("一键换号失败", "Quick switch failed"), fmt.Errorf("%s", m.tt("没有其他在线账号可切换", "no other online account to switch to")))
		return
	}
	pick := cands[0]
	for _, a := range cands[1:] { // 已知积分高者优先，未知积分不参与比较
		if a.CreditsKnown && a.Credits > pick.Credits {
			pick = a
		}
	}
	if err := m.svc.ClientSwitchAccount(pick.UID); err != nil {
		m.reportError(m.tt("一键换号失败", "Quick switch failed"), err)
	}
}

// ---------- 账号备份 / 切换 ----------

// accountsDir 备份目录：<数据目录>/inject/accounts
func (m *Manager) accountsDir() string {
	return filepath.Join(filepath.Dir(m.svc.ConfigPath()), "inject", "accounts")
}

// Backup 备份当前登录态：复制官方客户端固定登录位文件（不依赖注入连接，未登录时报真实原因）。
// 历史版本的 localStorage 快照备份仍可切换（见 Switch 的 legacy 分支）。
func (m *Manager) Backup(name string) (AccountBackup, error) {
	authFile, err := core.OfficialClientAuthFile()
	if err != nil {
		return AccountBackup{}, err
	}
	raw, err := os.ReadFile(authFile)
	if err != nil {
		if os.IsNotExist(err) {
			return AccountBackup{}, fmt.Errorf("未找到官方登录文件，客户端可能未登录（%s）", authFile)
		}
		return AccountBackup{}, fmt.Errorf("读取官方登录文件失败: %w", err)
	}

	// 解析只为识别账号；解析失败也允许备份原文（切换时会再做校验）
	uid, nickname, realm := "", "", ""
	if cred, perr := core.ParseCredential(authFile, raw); perr == nil && cred != nil {
		uid, nickname, realm = cred.UID, cred.Nickname, cred.Realm
	}
	if name == "" {
		if nickname != "" {
			name = nickname
		} else {
			name = "官方登录 " + shortIDRaw(uid, raw)
		}
	}
	id := shortIDRaw(uid, raw)

	dir := m.accountsDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return AccountBackup{}, err
	}
	file := filepath.Join(dir, id+".json")
	meta := map[string]any{
		"id": id, "name": name, "uid": uid, "realm": realm,
		"time": time.Now().Format("2006-01-02 15:04"), "raw": string(raw),
	}
	out, _ := json.MarshalIndent(meta, "", "  ")
	if err := os.WriteFile(file, out, 0o600); err != nil {
		return AccountBackup{}, err
	}
	core.EmitEvent(core.EventInjectStatus, map[string]any{"kind": "backup", "id": id, "name": name})
	return AccountBackup{ID: id, Name: name, UID: uid, Realm: realm,
		Time: fmt.Sprintf("%v", meta["time"]), Size: len(out)}, nil
}

// List 列出全部备份
func (m *Manager) List() ([]AccountBackup, error) {
	entries, err := os.ReadDir(m.accountsDir())
	if err != nil {
		return []AccountBackup{}, nil // 目录不存在 = 无备份
	}
	out := []AccountBackup{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(m.accountsDir(), e.Name()))
		if err != nil {
			continue
		}
		var meta struct {
			ID    string `json:"id"`
			Name  string `json:"name"`
			UID   string `json:"uid"`
			Realm string `json:"realm"`
			Time  string `json:"time"`
		}
		_ = json.Unmarshal(raw, &meta)
		if meta.ID == "" {
			meta.ID = strings.TrimSuffix(e.Name(), ".json")
		}
		out = append(out, AccountBackup{ID: meta.ID, Name: meta.Name, UID: meta.UID, Realm: meta.Realm,
			Time: meta.Time, Size: len(raw)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time > out[j].Time })
	return out, nil
}

// Switch 切换到指定备份：
//   - 登录文件快照（raw 字段）：校验后写入官方登录位并重启客户端（对齐 WorkDaddy 的通道校验）；
//   - legacy localStorage 快照（storage 字段）：恢复页面 localStorage 并刷新（需注入连接仍在）。
func (m *Manager) Switch(id string) error {
	if id == "" || strings.ContainsAny(id, "/\\.") {
		return fmt.Errorf("非法的备份 ID")
	}
	raw, err := os.ReadFile(filepath.Join(m.accountsDir(), id+".json"))
	if err != nil {
		return fmt.Errorf("备份不存在: %s", id)
	}
	var meta struct {
		Raw     string            `json:"raw"`
		Storage map[string]string `json:"storage"`
	}
	if json.Unmarshal(raw, &meta) != nil {
		return fmt.Errorf("备份文件无效: %s", id)
	}

	// 登录文件快照：走核心恢复流水线（客户端会重启，CDP 连接随之断开）
	if meta.Raw != "" {
		if err := m.svc.ClientRestoreAuthBackup([]byte(meta.Raw)); err != nil {
			return err
		}
		m.Stop()
		core.EmitEvent(core.EventInjectStatus, map[string]any{"kind": "switch", "id": id})
		return nil
	}

	// legacy：localStorage 快照恢复
	if len(meta.Storage) == 0 {
		return fmt.Errorf("备份文件无效: %s", id)
	}
	m.mu.Lock()
	conn := m.conn
	m.mu.Unlock()
	if conn == nil {
		return fmt.Errorf("注入未运行，请先启动注入")
	}
	storageJSON, _ := json.Marshal(meta.Storage)
	expr := fmt.Sprintf(`(() => {
		const d = %s;
		localStorage.clear();
		for (const [k, v] of Object.entries(d)) localStorage.setItem(k, v);
		location.reload();
		return true;
	})()`, string(storageJSON))
	if _, err := conn.evaluate(expr, 10*time.Second); err != nil {
		return fmt.Errorf("恢复登录态失败: %w", err)
	}
	// 页面 reload 会断开 CDP 连接
	m.Stop()
	core.EmitEvent(core.EventInjectStatus, map[string]any{"kind": "switch", "id": id})
	return nil
}

// Delete 删除指定备份
func (m *Manager) Delete(id string) error {
	if id == "" || strings.ContainsAny(id, "/\\.") {
		return fmt.Errorf("非法的备份 ID")
	}
	return os.Remove(filepath.Join(m.accountsDir(), id+".json"))
}

// ---------- 辅助 ----------

// shortIDRaw 备份文件 ID：UID 可用则取其后 12 位，否则取内容哈希前 12 位
func shortIDRaw(uid string, raw []byte) string {
	if uid != "" {
		if len(uid) > 12 {
			return uid[len(uid)-12:]
		}
		return uid
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:6])
}

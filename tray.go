package main

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
	"github.com/wailsapp/wails/v3/pkg/icons"

	"workbuddy-desktop/internal/core"
)

// ============================================================
// 系统托盘（DESIGN.md Task 3.4）
//
// 托盘上的每个数字都取自 core 的真实运行态，菜单里的每个动作调用的都是
// 与界面完全相同的方法（Service.Start/Stop、Scheduler.RunFor、凭证目录解析）。
//
// 关于「托盘通知」：当前 Wails 版本没有跨平台的系统通知 API，
// 因此这里以托盘菜单项 + 悬停提示的真实更新来实现——任务结束后显示本次
// 真实结果（成功 / 失败 / 跳过 各多少），而不是伪造一条系统通知。
// ============================================================

type tray struct {
	svc    *core.Service
	window application.Window
	bar    *application.SystemTray

	mu      sync.Mutex
	gateway *application.MenuItem
	pool    *application.MenuItem
	awake   *application.MenuItem
	recent  *application.MenuItem
}

// setupTray 装配托盘：图标 + 右键菜单 + 关闭窗口隐藏到托盘
func setupTray(app *application.App, svc *core.Service, window application.Window) *tray {
	t := &tray{svc: svc, window: window}
	t.bar = app.SystemTray.New()

	switch runtime.GOOS {
	case "darwin":
		// 模板图标：由系统按亮/暗色自动反色
		t.bar.SetTemplateIcon(icons.SystrayMacTemplate)
	case "windows":
		t.bar.SetIcon(icons.DefaultWindowsIcon)
	default:
		t.bar.SetIcon(icons.SystrayLight)
	}

	menu := app.NewMenu()
	menu.Add("显示主窗口").OnClick(func(*application.Context) { t.show() })
	menu.AddSeparator()

	t.gateway = menu.Add("")
	t.gateway.OnClick(func(*application.Context) { t.toggleGateway() })

	t.pool = menu.Add("")
	t.pool.SetEnabled(false) // 纯展示项

	menu.Add("刷新状态").OnClick(func(*application.Context) { t.refresh() })
	menu.AddSeparator()

	menu.Add("执行每日签到").OnClick(func(*application.Context) { t.runTask(core.TaskCheckin) })
	menu.Add("执行保活任务").OnClick(func(*application.Context) { t.runTask(core.TaskKeepalive) })
	t.awake = menu.Add("")
	t.awake.OnClick(func(*application.Context) { t.toggleAwake() })
	menu.Add("打开凭证目录").OnClick(func(*application.Context) { t.openAuthDir() })
	menu.AddSeparator()

	t.recent = menu.Add("")
	t.recent.SetEnabled(false) // 纯展示项：最近一次真实动作 / 任务结果
	menu.AddSeparator()

	menu.Add("退出 BuddyBot").OnClick(func(*application.Context) { app.Quit() })

	t.bar.SetMenu(menu)
	t.bar.OnClick(func() { t.show() })

	// 关闭窗口 = 隐藏到托盘；真正退出走菜单里的「退出」
	window.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) {
		window.Hide()
		e.Cancel()
	})

	// macOS：点 Dock 图标回到窗口
	app.Event.OnApplicationEvent(events.Mac.ApplicationShouldHandleReopen, func(*application.ApplicationEvent) {
		t.show()
	})

	// 真实事件驱动刷新：任务执行完成、账号状态变化立即反映到托盘
	app.Event.On(core.EventTaskCompleted, func(*application.CustomEvent) {
		t.syncRecentFromScheduler()
		t.refresh()
	})
	app.Event.On(core.EventAccountStatus, func(*application.CustomEvent) { t.refresh() })

	// 低频兜底刷新：凭证文件可能被外部增删，账号池数量需要跟上
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			t.refresh()
		}
	}()

	t.refresh()
	t.setRecent("已启动到托盘")
	return t
}

// show 显示并聚焦主窗口
func (t *tray) show() {
	t.window.Show()
	t.window.Focus()
}

// refresh 用真实运行态重写托盘文案
func (t *tray) refresh() {
	accounts := t.svc.Accounts()

	t.mu.Lock()
	t.gateway.SetLabel(gatewayLabel(t.svc.IsRunning(), t.svc.GetConfig().Listen))
	t.pool.SetLabel(poolSummary(accounts))
	t.awake.SetLabel(awakeLabel(t.svc.AwakeActive()))
	t.mu.Unlock()

	t.updateTooltip("")
}

// gatewayLabel 网关菜单项文案：动作 + 真实监听地址
func gatewayLabel(running bool, listen string) string {
	if running {
		return fmt.Sprintf("停止网关（监听 %s）", listen)
	}
	return fmt.Sprintf("启动网关（%s）", listen)
}

// poolSummary 账号池菜单项文案：按真实派生状态统计
func poolSummary(accounts []core.Account) string {
	var online, cooldown, disabled, expired int
	for _, a := range accounts {
		switch a.Status {
		case "online":
			online++
		case "cooldown":
			cooldown++
		case "disabled":
			disabled++
		case "expired":
			expired++
		}
	}
	summary := fmt.Sprintf("账号池 %d/%d 在线", online, len(accounts))
	if cooldown > 0 {
		summary += fmt.Sprintf(" · 冷却 %d", cooldown)
	}
	if expired > 0 {
		summary += fmt.Sprintf(" · 过期 %d", expired)
	}
	if disabled > 0 {
		summary += fmt.Sprintf(" · 禁用 %d", disabled)
	}
	return summary
}

// toggleGateway 启停网关（与界面上的按钮同一路径）
func (t *tray) toggleGateway() {
	if t.svc.IsRunning() {
		t.svc.Stop()
		t.setRecent("网关已停止")
		return
	}
	t.svc.Start(context.Background())
	// Start 内部把监听失败写进网关日志；这里以「是否真的在监听」作为判定
	if t.svc.IsRunning() {
		t.setRecent("网关已启动，监听 " + t.svc.GetConfig().Listen)
	} else {
		t.setRecent("网关启动失败：请检查监听地址是否被占用")
	}
}

// toggleAwake 托盘切换防休眠（与设置页同一持久化路径）
func (t *tray) toggleAwake() {
	next := !t.svc.AwakeActive()
	if err := t.svc.SetAwake(next); err != nil {
		t.setRecent("防休眠设置失败：" + err.Error())
		return
	}
	if next {
		t.setRecent("防休眠已开启（" + t.svc.AwakeNote() + "）")
	} else {
		t.setRecent("防休眠已关闭，系统可正常休眠")
	}
	t.refresh()
}

// awakeLabel 防休眠菜单项文案
func awakeLabel(active bool) string {
	if active {
		return "关闭防休眠（当前阻止系统休眠）"
	}
	return "开启防休眠（任务期间不休眠）"
}

// runTask 手动触发一次真实任务（耗时操作放 goroutine，避免阻塞菜单线程）
func (t *tray) runTask(taskType string) {
	t.setRecent(taskName(taskType) + "执行中…")
	go func() {
		run := t.svc.Scheduler().RunFor(taskType, "manual", nil)
		t.setRecent(fmt.Sprintf("%s：成功 %d · 失败 %d · 跳过 %d",
			taskName(taskType), run.Success, run.Failed, run.Skipped))
		t.refresh()
	}()
}

// syncRecentFromScheduler 把「最近动态」同步为调度器记录里的真实结果
func (t *tray) syncRecentFromScheduler() {
	if last := t.svc.Scheduler().Status().LastRun; last != nil {
		t.setRecent(fmt.Sprintf("%s（%s）：成功 %d · 失败 %d · 跳过 %d",
			taskName(last.Type), triggerName(last.Trigger), last.Success, last.Failed, last.Skipped))
	}
}

// openAuthDir 在系统文件管理器中打开凭证目录
func (t *tray) openAuthDir() {
	dir := t.svc.AuthDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.setRecent("打开凭证目录失败：" + err.Error())
		return
	}
	if app := application.Get(); app != nil {
		app.Browser.OpenURL("file://" + dir)
	}
	t.setRecent("已打开凭证目录")
}

// setRecent 更新「最近动态」展示项，并刷新悬停提示
func (t *tray) setRecent(text string) {
	t.mu.Lock()
	t.recent.SetLabel(text)
	t.mu.Unlock()
	t.updateTooltip(text)
}

// updateTooltip 悬停提示：网关状态 + 账号数（+ 最近动态）
func (t *tray) updateTooltip(text string) {
	st := "已停止"
	if t.svc.IsRunning() {
		st = "运行中"
	}
	tooltip := fmt.Sprintf("BuddyBot · WorkBuddy 控制台\n网关 %s · 账号 %d 个", st, len(t.svc.Accounts()))
	if text != "" {
		tooltip += "\n" + text
	}
	t.bar.SetTooltip(tooltip)
}

// taskName 任务类型 → 中文名
func taskName(taskType string) string {
	switch taskType {
	case core.TaskCheckin:
		return "每日签到"
	case core.TaskTravel:
		return "猫猫旅行"
	case core.TaskKeepalive:
		return "保活任务"
	default:
		return taskType
	}
}

// triggerName 触发来源 → 中文名
func triggerName(trigger string) string {
	if trigger == "manual" {
		return "手动"
	}
	return "排程"
}

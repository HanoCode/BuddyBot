package main

import (
	"context"
	"embed"
	"log"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/services/notifications"

	"workbuddy-desktop/internal/api"
	"workbuddy-desktop/internal/core"
	"workbuddy-desktop/internal/inject"
)

//go:embed all:frontend/dist
var assets embed.FS

//go:embed frontend/src/data/prompts.json
var promptsJSON []byte

func main() {
	// 过滤标准库 log 的良性噪音（net/http 空闲连接上的迟到响应，见 logfilter.go）
	core.InstallStdLogFilter()

	// 初始化核心服务（配置加载 / 本地存储），网关在应用就绪后启动
	coreService := core.NewService()
	injectManager := inject.NewManager(coreService, promptsJSON)

	// 系统级桌面通知（Wails 内置服务）：macOS 裸二进制无 bundle identifier 时
	// 服务 Startup 会失败并中止启动，故先探测可用性，不可用则静默降级不注册
	services := []application.Service{
		application.NewService(api.NewAccountsAPI(coreService)),
		application.NewService(api.NewClientSwitchAPI(coreService)),
		application.NewService(api.NewDataMigrateAPI()),
		application.NewService(api.NewAgentsAPI(coreService)),
		application.NewService(api.NewConfigAPI(coreService)),
		application.NewService(api.NewGatewayAPI(coreService)),
		application.NewService(api.NewKeysAPI(coreService)),
		application.NewService(api.NewLogsAPI(coreService)),
		application.NewService(api.NewModelsAPI(coreService)),
		application.NewService(api.NewStatsAPI(coreService)),
		application.NewService(api.NewSkillsAPI(coreService)),
		application.NewService(api.NewSystemAPI(coreService)),
		application.NewService(api.NewChatAPI(coreService)),
		application.NewService(api.NewPowerAPI(coreService)),
		application.NewService(api.NewInjectAPI(coreService, injectManager)),
	}
	if core.DesktopNotificationsAvailable() {
		desktopNotifier := notifications.New()
		services = append(services, application.NewService(desktopNotifier))
		core.SetDesktopNotifier(desktopNotifier, coreService)
	}

	app := application.New(application.Options{
		Name: "BuddyBot",
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
		Services: services,
		// 退出前：停后台协程 + 把未落盘数据（记账/日志/状态）写盘
		OnShutdown: func() {
			coreService.Shutdown()
			injectManager.Stop()
		},
		Mac: application.MacOptions{
			// 关闭窗口只隐藏到托盘，真正退出走托盘菜单的「退出」
			ApplicationShouldTerminateAfterLastWindowClosed: false,
		},
	})

	// 应用启动后延迟拉起网关：保证事件循环就绪，gateway:log 可投递到前端
	coreService.SetAppReady()
	coreService.StartFlusher()
	injectManager.StartSupervisor() // 注入开关开启时自动恢复/重连（含启动时优雅重启客户端带 CDP）
	go func() {
		time.Sleep(600 * time.Millisecond)
		coreService.Start(context.Background())
	}()

	// 主窗口：无边框 + macOS 红绿灯内嵌（配合前端自绘标题栏）
	window := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:      "main", // 供 FocusMainWindow 等按名定位
		Title:     "BuddyBot",
		Width:     1380,
		Height:    860,
		MinWidth:  1080,
		MinHeight: 700,
		Frameless: true,
		Mac: application.MacWindow{
			TitleBar: application.MacTitleBarHiddenInset,
		},
	})

	// 系统托盘（DESIGN.md Task 3.4）：状态展示 + 快捷动作 + 关闭到托盘
	setupTray(app, coreService, window)

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}

package main

import (
	"context"
	"embed"
	"log"
	"os"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/services/notifications"

	"workbuddy-desktop/internal/api"
	"workbuddy-desktop/internal/core"
	"workbuddy-desktop/internal/inject"
	"workbuddy-desktop/internal/mcp"
)

//go:embed all:frontend/dist
var assets embed.FS

//go:embed frontend/src/data/prompts.json
var promptsJSON []byte

func main() {
	// 随包内嵌数据注入 core（go:embed 不能跨目录，故由 main 传入）
	core.SetEmbeddedPrompts(promptsJSON)

	// 无界面子命令：由插件中心写入的脚本 / 客户端配置回调。
	// 这些分支不启动 GUI / 网关 / 单实例锁，stdin/stdout 即协议通道。
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "mcp":
			// MCP stdio server：供编程智能体作为工具接入
			if err := mcp.Run(core.NewService()); err != nil {
				log.Fatal(err)
			}
			return
		case "hook-guard", "hook-context", "hook-notify":
			svc := core.NewService()
			switch os.Args[1] {
			case "hook-guard":
				core.RunHookGuard(svc, os.Stdin)
			case "hook-context":
				core.RunHookContext(svc, os.Stdin)
			default:
				core.RunHookNotify(svc, os.Stdin)
			}
			return
		}
	}

	// 单实例保护：关窗口默认隐藏到托盘，旧实例仍在运行；重复启动会造成
	// 网关端口 7863 冲突（bind WSAEADDRINUSE）。第二个实例弹提示后直接退出。
	if !core.AcquireSingleInstance() {
		return
	}

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
		application.NewService(api.NewPluginsAPI(coreService)),
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
	// 尺寸自适应屏幕：固定 1380×860 在小屏 / 高 DPI 缩放的 Windows 笔记本上
	// 会超出屏幕（连拖拽调大小的边缘都在屏外），这里按主屏工作区收缩，
	// 最小尺寸同样约束在屏幕内，保证窗口永远可完整显示、可正常调整大小。
	w, h, minW, minH := 1380, 860, 1080, 700
	if sm := app.Screen; sm != nil {
		if s := sm.GetPrimary(); s != nil && s.WorkArea.Width > 0 && s.WorkArea.Height > 0 {
			waW, waH := s.WorkArea.Width, s.WorkArea.Height
			w, h = min(w, waW*92/100), min(h, waH*92/100)
			minW, minH = min(minW, waW*96/100), min(minH, waH*96/100)
		}
	}
	window := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:      "main", // 供 FocusMainWindow 等按名定位
		Title:     "BuddyBot",
		Width:     w,
		Height:    h,
		MinWidth:  minW,
		MinHeight: minH,
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

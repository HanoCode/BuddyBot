package core

import (
	"github.com/wailsapp/wails/v3/pkg/application"
)

// Wails 事件名（与 API.md 事件系统章节一致）
const (
	EventGatewayLog    = "gateway:log"
	EventAccountStatus = "account:statusChanged"
	EventTaskProgress  = "task:progress"
	EventTaskCompleted = "task:completed"
	EventChatToken     = "chat:token"
	EventChatReasoning = "chat:reasoning"
	EventChatDone      = "chat:done"
	EventInjectStatus  = "inject:status"
	EventAppError      = "app:error" // 后端基础设施错误（落盘失败等），前端应醒目提示
	EventUpdateProgress  = "update:progress"  // 在线更新进度 {stage, downloaded, total, percent}
	EventUpdateAvailable = "update:available" // 定期检查发现新版本（负载为 UpdateInfo）
)

// EmitEvent 向前端发送事件；应用未创建时静默跳过（启动早期）。
// 同时作为桌面通知的转发出口（desktop_notify.go），未启用时为空操作。
func EmitEvent(name string, data any) {
	notifyDesktopEvent(name, data)
	app := application.Get()
	if app == nil {
		return
	}
	app.Event.Emit(name, data)
}

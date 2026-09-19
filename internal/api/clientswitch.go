package api

import (
	"context"

	"workbuddy-desktop/internal/core"
)

// ClientSwitchAPI 官方客户端登录账号切换
// （把账号池中的凭证写入官方登录位并重启客户端，进度经 clientswitch:progress 事件回报）
type ClientSwitchAPI struct {
	service *core.Service
}

// NewClientSwitchAPI 创建客户端切换API
func NewClientSwitchAPI(service *core.Service) *ClientSwitchAPI {
	return &ClientSwitchAPI{service: service}
}

// Precheck 切换前检查：目标账号 / 官方登录位状态 / 客户端探测 / 跨通道警告
func (a *ClientSwitchAPI) Precheck(ctx context.Context, uid string) (*core.ClientSwitchPrecheck, error) {
	return a.service.ClientSwitchPrecheck(uid)
}

// Switch 执行切换（备份 → 退出客户端 → 写入登录位 → 重启客户端）。
// 同步执行；每步进度经 clientswitch:progress 事件推送前端。
func (a *ClientSwitchAPI) Switch(ctx context.Context, uid string) error {
	return a.service.ClientSwitchAccount(uid)
}

// Backups 列出切换时自动备份的官方登录文件（可手工还原）
func (a *ClientSwitchAPI) Backups(ctx context.Context) []map[string]any {
	return a.service.ClientSwitchBackups()
}

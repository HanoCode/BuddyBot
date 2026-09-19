package api

import (
	"context"

	"workbuddy-desktop/internal/core"
	"workbuddy-desktop/internal/inject"
)

// InjectAPI 官方客户端注入（CDP 面板 / 免打扰 / 账号备份切换）
type InjectAPI struct {
	service *core.Service
	manager *inject.Manager
}

// NewInjectAPI 创建注入 API
func NewInjectAPI(service *core.Service, manager *inject.Manager) *InjectAPI {
	return &InjectAPI{service: service, manager: manager}
}

// Status 注入运行态
func (a *InjectAPI) Status(ctx context.Context) inject.Status {
	return a.manager.Status()
}

// Start 启动注入（确保客户端以调试模式运行并注入面板）
func (a *InjectAPI) Start(ctx context.Context) error {
	return a.manager.Start()
}

// Stop 停止注入
func (a *InjectAPI) Stop(ctx context.Context) error {
	a.manager.Stop()
	return nil
}

// UpdateConfig 局部更新注入设置（enabled/client_path/port/dnd_auto_confirm）
func (a *InjectAPI) UpdateConfig(ctx context.Context, enabled *bool, clientPath *string, port *int, dnd *bool) error {
	return a.service.UpdateInjectConfig(func(c *core.InjectConfig) {
		if enabled != nil {
			c.Enabled = *enabled
		}
		if clientPath != nil {
			c.ClientPath = *clientPath
		}
		if port != nil {
			c.Port = *port
		}
		if dnd != nil {
			c.DNDAutoConfirm = *dnd
		}
	})
}

// GetConfig 读取注入设置
func (a *InjectAPI) GetConfig(ctx context.Context) core.InjectConfig {
	return a.service.GetConfig().Inject
}

// Accounts 列出账号登录态备份
func (a *InjectAPI) Accounts(ctx context.Context) ([]inject.AccountBackup, error) {
	return a.manager.List()
}

// Backup 备份当前账号登录态（name 可空，自动生成）
func (a *InjectAPI) Backup(ctx context.Context, name string) (*inject.AccountBackup, error) {
	b, err := a.manager.Backup(name)
	if err != nil {
		return nil, err
	}
	return &b, nil
}

// Switch 切换到指定备份（恢复登录态并刷新客户端页面）
func (a *InjectAPI) Switch(ctx context.Context, id string) error {
	return a.manager.Switch(id)
}

// DeleteBackup 删除备份
func (a *InjectAPI) DeleteBackup(ctx context.Context, id string) error {
	return a.manager.Delete(id)
}

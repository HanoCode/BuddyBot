package api

import (
	"context"

	"workbuddy-desktop/internal/core"
)

// PluginsAPI 插件中心API：把 BuddyBot 能力以辅助插件形式接入编程智能体
// （MCP 工具 / 规则同步 / 钩子 / slash 命令），全部走备份+回滚。
type PluginsAPI struct {
	service *core.Service
}

// NewPluginsAPI 创建插件中心API
func NewPluginsAPI(service *core.Service) *PluginsAPI {
	return &PluginsAPI{service: service}
}

// Statuses 所有插件的接入状态（按磁盘真实内容推导）
func (p *PluginsAPI) Statuses(ctx context.Context) ([]core.PluginStatus, error) {
	return p.service.PluginStatuses(), nil
}

// Settings 读取插件配置（规则文本 + 安全命令放行白名单）
func (p *PluginsAPI) Settings(ctx context.Context) (*core.PluginSettings, error) {
	out := p.service.PluginSettingsNow()
	return &out, nil
}

// SaveSettings 保存插件配置（只落盘 plugins 段，不重启网关）
func (p *PluginsAPI) SaveSettings(ctx context.Context, in core.PluginSettings) error {
	return p.service.SavePluginSettings(in)
}

// SlashCommands 提效指令库的安装清单（供页面做按条勾选安装/卸载）
func (p *PluginsAPI) SlashCommands(ctx context.Context) ([]core.PluginSlashCommand, error) {
	return p.service.SlashCommandList(), nil
}

// DefaultSettings 插件配置的默认值（页面「恢复默认」用，避免前端再抄一份）
func (p *PluginsAPI) DefaultSettings(ctx context.Context) (*core.PluginSettings, error) {
	return &core.PluginSettings{
		RulesText:       core.DefaultRulesText(),
		SafetyAllowlist: core.DefaultSafetyAllowlist(),
	}, nil
}

// Backups 插件写入产生的备份列表（新→旧）
func (p *PluginsAPI) Backups(ctx context.Context) ([]core.PluginBackup, error) {
	return p.service.ListPluginBackups(), nil
}

// Restore 回滚指定插件备份，返回恢复的文件数
func (p *PluginsAPI) Restore(ctx context.Context, target string, backupID string) (int, error) {
	return p.service.RestorePluginBackup(target, backupID)
}

// PluginParams 安装/卸载参数（ids 仅 slash 插件使用）
type PluginParams struct {
	Plugin string `json:"plugin"`
	IDs    []int  `json:"ids"`
}

// Apply 安装/更新插件
func (p *PluginsAPI) Apply(ctx context.Context, params PluginParams) (*core.PluginApplyResult, error) {
	return p.service.ApplyPlugin(params.Plugin, params.IDs)
}

// Remove 卸载插件
func (p *PluginsAPI) Remove(ctx context.Context, params PluginParams) (*core.PluginApplyResult, error) {
	return p.service.RemovePlugin(params.Plugin, params.IDs)
}

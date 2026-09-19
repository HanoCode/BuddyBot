package api

import (
	"context"

	"workbuddy-desktop/internal/core"
)

// AgentsAPI 智能体一键接入API：探测本机 AI 编程客户端并写入网关配置
type AgentsAPI struct {
	service *core.Service
}

// NewAgentsAPI 创建智能体接入API
func NewAgentsAPI(service *core.Service) *AgentsAPI {
	return &AgentsAPI{service: service}
}

// List 探测全部支持的客户端
func (a *AgentsAPI) List(ctx context.Context) ([]core.AgentTarget, error) {
	return core.DetectAgents(a.service.GatewayBaseURL(), a.service.GetConfig().APIKey), nil
}

// ApplyParams 写入参数
type ApplyParams struct {
	Target string   `json:"target"`
	Models []string `json:"models"`
}

// Apply 接入指定客户端（写入前自动备份）
func (a *AgentsAPI) Apply(ctx context.Context, params ApplyParams) (*core.AgentApplyResult, error) {
	return core.ApplyAgent(
		params.Target,
		a.service.GatewayBaseURL(),
		a.service.GetConfig().APIKey,
		params.Models,
		a.service.AgentBackupRoot(),
	)
}

// Backups 列出某客户端的备份
func (a *AgentsAPI) Backups(ctx context.Context, target string) ([]core.AgentBackup, error) {
	return core.ListAgentBackups(target, a.service.AgentBackupRoot()), nil
}

// Restore 回滚指定备份，返回恢复的文件数
func (a *AgentsAPI) Restore(ctx context.Context, target, backupID string) (int, error) {
	return core.RestoreAgentBackup(target, backupID, a.service.AgentBackupRoot())
}

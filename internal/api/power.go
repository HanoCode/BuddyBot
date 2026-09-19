package api

import (
	"context"

	"workbuddy-desktop/internal/core"
)

// PowerAPI 防休眠控制
type PowerAPI struct {
	service *core.Service
}

// NewPowerAPI 创建防休眠 API
func NewPowerAPI(service *core.Service) *PowerAPI {
	return &PowerAPI{service: service}
}

// AwakeStatus 防休眠状态
type AwakeStatus struct {
	Active bool   `json:"active"`
	Note   string `json:"note,omitempty"`
	Config bool   `json:"config"` // 配置里的期望值
}

// SetKeepAwake 开启/关闭防休眠（同时持久化到配置，重启后保持）
func (p *PowerAPI) SetKeepAwake(ctx context.Context, on bool) error {
	return p.service.SetAwake(on)
}

// Status 查询防休眠状态
func (p *PowerAPI) Status(ctx context.Context) AwakeStatus {
	return AwakeStatus{
		Active: p.service.AwakeActive(),
		Note:   p.service.AwakeNote(),
		Config: p.service.GetConfig().KeepAwake,
	}
}

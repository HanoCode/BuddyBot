package api

import (
	"context"

	"workbuddy-desktop/internal/core"
	"workbuddy-desktop/internal/ferry"
)

// FerryAPI 免费额度摆渡 API：OpenCode 免费模型池的接入、状态与探测
type FerryAPI struct {
	service *core.Service
}

// NewFerryAPI 创建免费额度 API
func NewFerryAPI(service *core.Service) *FerryAPI {
	return &FerryAPI{service: service}
}

// Status 当前状态：阶段、隔离运行时版本、模型清单与判定
func (a *FerryAPI) Status(ctx context.Context) (ferry.Status, error) {
	return a.service.FerryManager().Status(), nil
}

// SetEnabled 启用/停用（启用会下载并托管隔离 OpenCode 运行时）
func (a *FerryAPI) SetEnabled(ctx context.Context, enabled bool) error {
	return a.service.SetFerryEnabled(enabled)
}

// Refresh 重读免费模型目录
func (a *FerryAPI) Refresh(ctx context.Context) (ferry.Status, error) {
	mgr := a.service.FerryManager()
	if err := mgr.Refresh(); err != nil {
		return mgr.Status(), err
	}
	mgr.ProbeAll(false)
	return mgr.Status(), nil
}

// Probe 检测：model 为空表示检测全部；force 忽略判定缓存（用户点「重新检测」时）
func (a *FerryAPI) Probe(ctx context.Context, model string, force bool) (ferry.Status, error) {
	mgr := a.service.FerryManager()
	if model == "" {
		mgr.ProbeAll(force)
	} else if err := mgr.ProbeOne(model, force); err != nil {
		return mgr.Status(), err
	}
	return mgr.Status(), nil
}

// SetSelected 勾选/取消一个模型（取消的模型不再对外提供）
func (a *FerryAPI) SetSelected(ctx context.Context, model string, selected bool) (ferry.Status, error) {
	mgr := a.service.FerryManager()
	if err := mgr.SetExcluded(model, !selected); err != nil {
		return mgr.Status(), err
	}
	return mgr.Status(), nil
}

// SetSystemProxy 是否跟随系统手动 HTTP/HTTPS 代理（切换会重启隔离运行时）
func (a *FerryAPI) SetSystemProxy(ctx context.Context, enabled bool) (ferry.Status, error) {
	mgr := a.service.FerryManager()
	if err := mgr.SetSystemProxy(enabled); err != nil {
		return mgr.Status(), err
	}
	return mgr.Status(), nil
}

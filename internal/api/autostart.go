package api

import (
	"context"

	"workbuddy-desktop/internal/core"
)

// AutoStartAPI 开机自启控制
type AutoStartAPI struct {
	service *core.Service
}

// NewAutoStartAPI 创建开机自启 API
func NewAutoStartAPI(service *core.Service) *AutoStartAPI {
	return &AutoStartAPI{service: service}
}

// Status 查询开机自启真实状态（由系统注册状态推导）
func (a *AutoStartAPI) Status(ctx context.Context) core.AutoStartStatus {
	return core.AutoStartState()
}

// Set 开启 / 关闭开机自启（下次登录系统时生效）
func (a *AutoStartAPI) Set(ctx context.Context, on bool) error {
	if err := ensureWritable(a.service); err != nil {
		return err
	}
	if err := core.SetAutoStart(on); err != nil {
		return err
	}
	if on {
		audit(a.service, "system.autostart", "autostart", "开机自启已开启")
	} else {
		audit(a.service, "system.autostart", "autostart", "开机自启已关闭")
	}
	return nil
}

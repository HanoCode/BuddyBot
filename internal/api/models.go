package api

import (
	"context"

	"workbuddy-desktop/internal/core"
)

// ModelsAPI 模型清单API（聊天测试的模型选择器数据源）
type ModelsAPI struct {
	service *core.Service
}

// NewModelsAPI 创建模型API
func NewModelsAPI(service *core.Service) *ModelsAPI {
	return &ModelsAPI{service: service}
}

// List 返回模型清单：目录收录（含 context_length / max_output_tokens）+ 本机真实用量
func (m *ModelsAPI) List(ctx context.Context) ([]core.ModelInfo, error) {
	return core.ListModels(m.service.Store()), nil
}

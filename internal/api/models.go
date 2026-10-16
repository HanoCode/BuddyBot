package api

import (
	"context"
	"sort"

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
// + 当前正在对外提供的免费额度模型。
//
// 免费额度模型必须出现在这里：客户端接入（智能体接入）与密钥范围都是从这份清单里选模型的，
// 清单里没有就等于「网关能提供但用户选不到」，功能等于没接上。
// 只列当前确实可用的（探测通过且未被取消勾选），避免选到一个会 404 的 id。
func (m *ModelsAPI) List(ctx context.Context) ([]core.ModelInfo, error) {
	list := core.ListModels(m.service.Store())
	free := m.service.FerryAvailableModels()
	if len(free) == 0 {
		return list, nil
	}
	seen := make(map[string]struct{}, len(list))
	for _, item := range list {
		seen[item.ID] = struct{}{}
	}
	for _, model := range free {
		if _, ok := seen[model.ID]; ok {
			continue
		}
		list = append(list, core.ModelInfo{
			ID:              model.ID,
			ContextLength:   model.Context,
			MaxOutputTokens: model.Output,
			Source:          "ferry",
		})
	}
	// 用与 ListModels 同一套排序（本机用过的在前，其余按 id），
	// 免得分流口插进来的模型永远掉在列表最末尾，用户翻不到。
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].Observed != list[j].Observed {
			return list[i].Observed
		}
		return list[i].ID < list[j].ID
	})
	return list, nil
}

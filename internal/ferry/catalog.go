package ferry

import (
	"encoding/json"
	"fmt"
	"sort"
)

// OpenCode 的模型目录（/provider）。
//
// 「免费」由价格判定，不由名字判定：输入/输出/缓存读写全为 0 才算，
// 并且排除已被标记 deprecated 的模型（它们仍会出现在目录里，但已经路由不到）。

type providerCatalog struct {
	All []providerEntry `json:"all"`
}

type providerEntry struct {
	ID     string                    `json:"id"`
	Models map[string]openCodeModel  `json:"models"`
}

type openCodeModel struct {
	Name  string `json:"name"`
	Cost  openCodeCost `json:"cost"`
	Caps  openCodeCapabilities `json:"capabilities"`
	Status string `json:"status"`
	Limit openCodeLimit `json:"limit"`
	Variants map[string]openCodeVariant `json:"variants"`
}

type openCodeCost struct {
	Input  float64 `json:"input"`
	Output float64 `json:"output"`
	Cache  struct {
		Read  float64 `json:"read"`
		Write float64 `json:"write"`
	} `json:"cache"`
}

type openCodeCapabilities struct {
	Input struct {
		Image bool `json:"image"`
	} `json:"input"`
	Output struct {
		Text *bool `json:"text"`
	} `json:"output"`
	Toolcall  bool `json:"toolcall"`
	Reasoning bool `json:"reasoning"`
}

type openCodeLimit struct {
	Context int64 `json:"context"`
	Input   int64 `json:"input"`
	Output  int64 `json:"output"`
}

type openCodeVariant struct {
	Disabled        bool   `json:"disabled"`
	ReasoningEffort string `json:"reasoningEffort"`
}

// freeModels 从目录里筛出免费模型
func freeModels(payload []byte) ([]Model, error) {
	var catalog providerCatalog
	if err := json.Unmarshal(payload, &catalog); err != nil {
		return nil, fmt.Errorf("模型目录无法解析: %w", err)
	}
	var models map[string]openCodeModel
	for _, provider := range catalog.All {
		if provider.ID == opencodeProviderID {
			models = provider.Models
			break
		}
	}
	if models == nil {
		return nil, fmt.Errorf("模型目录里没有 %s provider", opencodeProviderID)
	}
	out := make([]Model, 0, len(models))
	for id, entry := range models {
		if entry.Cost.Input != 0 || entry.Cost.Output != 0 || entry.Cost.Cache.Read != 0 || entry.Cost.Cache.Write != 0 {
			continue
		}
		if entry.Caps.Output.Text != nil && !*entry.Caps.Output.Text {
			continue
		}
		if entry.Status == "deprecated" {
			continue
		}
		name := entry.Name
		if name == "" {
			name = id
		}
		model := Model{
			ID:        gatewayModelPrefix + id,
			Name:      name,
			Context:   entry.Limit.Context,
			Input:     entry.Limit.Input,
			Output:    entry.Limit.Output,
			Images:    entry.Caps.Input.Image,
			Toolcall:  entry.Caps.Toolcall,
			Reasoning: entry.Caps.Reasoning,
		}
		if len(entry.Variants) > 0 {
			model.Variants = map[string]VariantOption{}
			for variant, option := range entry.Variants {
				model.Variants[variant] = VariantOption{Disabled: option.Disabled, ReasoningEffort: option.ReasoningEffort}
			}
		}
		out = append(out, model)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// upstreamModelID 网关可见 id → OpenCode 的上游 id
func upstreamModelID(gatewayID string) string {
	if len(gatewayID) > len(gatewayModelPrefix) && gatewayID[:len(gatewayModelPrefix)] == gatewayModelPrefix {
		return gatewayID[len(gatewayModelPrefix):]
	}
	return gatewayID
}

// workBuddyReasoning 把 OpenCode 的推理能力转成客户端认识的声明。
//
// 三种情况要分开：
//  1. 不支持推理 → 直接说不支持；
//  2. 支持推理但一个档位都映射不上 → 勾选推理，档位留空，关不掉思考；
//  3. 有档位 → 广告档位，默认优先 medium。
func workBuddyReasoning(model Model) map[string]any {
	if !model.Reasoning {
		return map[string]any{"supportsReasoning": false}
	}
	efforts := reasoningEfforts(model)
	supported := []string{}
	for _, effort := range reasoningEffortOrder {
		if _, ok := efforts[effort]; ok {
			supported = append(supported, effort)
		}
	}
	enabled := []string{}
	for _, effort := range supported {
		if effort != "none" {
			enabled = append(enabled, effort)
		}
	}
	if len(enabled) == 0 {
		return map[string]any{
			"supportsReasoning": true, "onlyReasoning": true,
			"reasoning": map[string]any{"supportedEfforts": []string{}, "canDisableThinking": false},
		}
	}
	defaultEffort := enabled[0]
	for _, effort := range enabled {
		if effort == "medium" {
			defaultEffort = effort
			break
		}
	}
	_, canDisable := efforts["none"]
	return map[string]any{
		"supportsReasoning": true,
		"onlyReasoning":     !canDisable,
		"reasoning": map[string]any{
			"supportedEfforts": supported, "defaultEffort": defaultEffort,
			"canDisableThinking": canDisable,
		},
	}
}

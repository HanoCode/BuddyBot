package core

import (
	"embed"
	"encoding/json"
	"sort"
)

// modelCatalogFS 模型上下文能力表（与 workbuddy2api 同源：internal/upstream/model.json 的
// 仓库种子版本，含 context_length / max_output_tokens）。它是模型清单的权威静态来源，
// 网关 /v1/models 与前端模型选择器都基于它，并叠加「本机真实观测到的模型」与
// 「密钥白名单里点名过的模型」。
//
//go:embed data/model.json
var modelCatalogFS embed.FS

// ModelCap 模型能力
type ModelCap struct {
	ContextLength   int64  `json:"context_length"`
	MaxOutputTokens int64  `json:"max_output_tokens"`
	Source          string `json:"source"`
}

// ModelInfo 模型清单条目（静态能力 + 本机真实用量）
type ModelInfo struct {
	ID              string `json:"id"`
	ContextLength   int64  `json:"contextLength"`
	MaxOutputTokens int64  `json:"maxOutputTokens"`
	Source          string `json:"source"`   // catalog = 目录收录；observed = 仅本机观测到
	Observed        bool   `json:"observed"` // 是否在本机请求日志中出现过
	Requests        int    `json:"requests"`
	Tokens          int    `json:"tokens"`
}

var seedCatalog = loadSeedCatalog()

func loadSeedCatalog() map[string]ModelCap {
	out := map[string]ModelCap{}
	b, err := modelCatalogFS.ReadFile("data/model.json")
	if err != nil {
		return out
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return map[string]ModelCap{}
	}
	return out
}

// ListModels 返回模型清单：目录收录的精简列表（去重、按 ID 排序）。
//
// 目录条数是固定的能力表，逐条下发没有意义；这里返回全部目录条目，
// 由前端按需搜索过滤。
func ListModels(store *Store) []ModelInfo {
	byID := map[string]*ModelInfo{}
	for id, cap := range seedCatalog {
		if id == "auto" {
			continue // auto 是路由别名，不是可请求模型
		}
		byID[id] = &ModelInfo{
			ID:              id,
			ContextLength:   cap.ContextLength,
			MaxOutputTokens: cap.MaxOutputTokens,
			Source:          "catalog",
		}
	}

	// 叠加上游动态目录（keepalive 周期刷新）：目录内同名条目覆盖种子表能力值，
	// 目录外条目以 upstream 来源新增（上游上新模型无需改代码即可见）
	if store != nil {
		dyn, _ := store.UpstreamModels()
		for id := range dyn {
			if m, ok := byID[id]; ok {
				m.Source = "upstream"
			} else {
				byID[id] = &ModelInfo{ID: id, Source: "upstream"}
			}
		}
	}

	// 叠加真实观测：本机请求日志里出现过的模型（含目录外的模型）
	if store != nil {
		for _, l := range store.ListRequestLogs() {
			if l.Model == "" {
				continue
			}
			m := byID[l.Model]
			if m == nil {
				m = &ModelInfo{ID: l.Model, Source: "observed"}
				byID[l.Model] = m
			}
			m.Observed = true
			m.Requests++
			m.Tokens += l.Tokens
		}
		// 密钥白名单里点名过的模型也要出现在清单里（否则无法在下拉里选中）
		for _, k := range store.ListKeys() {
			for _, name := range k.Models {
				if name == "" || name == "*" {
					continue
				}
				if _, ok := byID[name]; !ok {
					byID[name] = &ModelInfo{ID: name, Source: "whitelist"}
				}
			}
		}
	}

	out := make([]ModelInfo, 0, len(byID))
	for _, m := range byID {
		out = append(out, *m)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Observed != out[j].Observed {
			return out[i].Observed // 本机用过的排前面
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// ModelAllowed 校验模型是否命中密钥白名单（"*" 表示不限）
func ModelAllowed(models []string, model string) bool {
	if len(models) == 0 {
		return true
	}
	for _, m := range models {
		if m == "*" || m == model {
			return true
		}
	}
	return false
}

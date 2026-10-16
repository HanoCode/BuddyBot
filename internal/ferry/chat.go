package ferry

import (
	"context"
	"encoding/json"
	"net/http"
)

// ChatResult 一次转发的产出
type ChatResult struct {
	Body         map[string]any
	Stream       bool
	IncludeUsage bool
	// Usage 已按网关口径换算（缓存命中与未命中输入分开，见 TokenUsage）
	Usage TokenUsage
}

type streamFields struct {
	Stream        bool `json:"stream"`
	StreamOptions *struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options"`
}

// Chat 处理一次来自网关的转发请求。
//
// 并发上限 4：上游免费池对并发敏感，超出直接 429 而不是排队——
// 排队只会让客户端在看不到任何进展的情况下等更久。
func (m *Manager) Chat(ctx context.Context, body []byte) (*ChatResult, *BridgeError) {
	select {
	case m.sem <- struct{}{}:
		defer func() { <-m.sem }()
	default:
		return nil, newBridgeError(http.StatusTooManyRequests, "busy", "同时最多允许 4 个免费额度请求")
	}

	m.mu.Lock()
	backend := m.backend
	started := m.started
	m.mu.Unlock()
	if backend == nil || !started {
		return nil, newBridgeError(http.StatusServiceUnavailable, "ferry_unavailable", "免费额度服务未启动或未就绪")
	}
	models := m.AvailableModels()
	if len(models) == 0 {
		return nil, newBridgeError(http.StatusServiceUnavailable, "ferry_unavailable", "还没有已判定可用的免费模型")
	}
	request, perr := prepare(body, models)
	if perr != nil {
		return nil, perr
	}

	var fields streamFields
	_ = json.Unmarshal(body, &fields)

	meta := &requestMeta{Model: request.Model.ID}
	result, cerr := backend.Complete(ctx, request, meta)
	if cerr != nil {
		return nil, cerr
	}
	return &ChatResult{
		Body:         result,
		Stream:       fields.Stream,
		IncludeUsage: fields.StreamOptions != nil && fields.StreamOptions.IncludeUsage,
		Usage:        UsageTotals(result),
	}, nil
}

// WriteStream 把结果按 SSE 发给客户端（流式只是传输方式，不是信任方式）
func (m *Manager) WriteStream(w http.ResponseWriter, result *ChatResult) {
	if result == nil || result.Body == nil {
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	sendSSE(w, result.Body, result.IncludeUsage, false)
}

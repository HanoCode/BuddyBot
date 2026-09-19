package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"workbuddy-desktop/internal/core"
)

// ChatAPI 聊天测试API。
//
// 不是本地直造回复：它把请求**真实地**发到本机网关的 /v1/chat/completions，
// 因此「聊天测试」跑的是完整链路——鉴权、配额、账号路由、记账、日志一个不漏。
// 网关不可用或账号池为空时，错误原样返回给界面。
type ChatAPI struct {
	service *core.Service

	mu      sync.Mutex
	cancel  context.CancelFunc // 当前进行中对话的取消句柄（nil = 空闲）
	aborted bool
}

// NewChatAPI 创建聊天API
func NewChatAPI(service *core.Service) *ChatAPI {
	return &ChatAPI{service: service}
}

// ChatParams 请求参数
type ChatParams struct {
	Model       string             `json:"model"`
	System      string             `json:"system,omitempty"`
	Messages    []core.ChatMessage `json:"messages"`
	Temperature float64            `json:"temperature,omitempty"`
	MaxTokens   int                `json:"maxTokens,omitempty"`
	Stream      bool               `json:"stream"`
	SessionID   string             `json:"sessionId,omitempty"`
}

// ChatResult 结果（含真实用量与延迟）
type ChatResult struct {
	Content       string  `json:"content"`
	Reasoning     string  `json:"reasoning"`
	Tokens        int     `json:"tokens"`
	InputTokens   int     `json:"inputTokens"`
	OutputTokens  int     `json:"outputTokens"`
	FirstLatency  float64 `json:"latencyFirst"`
	TotalLatency  float64 `json:"latencyTotal"`
	SessionID     string  `json:"sessionId"`
	UpstreamModel string  `json:"upstreamModel"`
	Aborted       bool    `json:"aborted,omitempty"` // 用户手动停止：已生成内容有效，但未到自然结束
}

// Send 发送消息：流式时逐段通过 chat:token / chat:reasoning 事件推送，结束发 chat:done。
// 同一时刻只允许一条进行中的对话；Abort 可随时取消（已生成内容保留并随结果返回）。
func (c *ChatAPI) Send(ctx context.Context, params ChatParams) (*ChatResult, error) {
	cfg := c.service.GetConfig()
	if !c.service.IsRunning() {
		return nil, fmt.Errorf("网关未运行：请先在仪表盘启动网关")
	}

	// 注册可取消的请求上下文：Abort() 触发后本机请求与上游链路一并中断
	c.mu.Lock()
	if c.cancel != nil {
		c.mu.Unlock()
		return nil, fmt.Errorf("已有对话进行中，请等待完成或先停止")
	}
	reqCtx, cancel := context.WithCancel(ctx)
	c.cancel = cancel
	c.aborted = false
	c.mu.Unlock()
	defer func() {
		cancel()
		c.mu.Lock()
		c.cancel = nil
		c.mu.Unlock()
	}()

	body := map[string]any{
		"model":       params.Model,
		"messages":    params.Messages,
		"stream":      params.Stream,
		"temperature": params.Temperature,
		"system":      params.System,
		"session_id":  params.SessionID,
	}
	if params.MaxTokens > 0 {
		body["max_tokens"] = params.MaxTokens
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	url := "http://" + localHost(cfg.Listen) + "/v1/chat/completions"
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)

	client := &http.Client{Timeout: 5 * time.Minute}
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		if c.wasAborted() {
			return &ChatResult{SessionID: params.SessionID, Aborted: true}, nil
		}
		return nil, fmt.Errorf("请求本机网关失败（%s）: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("%s", apiErrorMessage(resp.Body, resp.StatusCode))
	}

	if params.Stream {
		return c.readStream(resp.Body, start, params.SessionID)
	}
	return c.readOnce(resp.Body, start, params.SessionID)
}

// Abort 停止当前进行中的对话；返回是否有对话被停止。
func (c *ChatAPI) Abort() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cancel == nil {
		return false
	}
	c.aborted = true
	c.cancel()
	return true
}

func (c *ChatAPI) wasAborted() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.aborted
}

// readStream 解析 SSE 流并通过 Wails 事件推送给前端
func (c *ChatAPI) readStream(body io.Reader, start time.Time, sessionID string) (*ChatResult, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 4<<20)

	var content, reasoning strings.Builder
	firstLatency := 0.0
	tokens := 0

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			break
		}
		var chunk struct {
			Model   string `json:"model"`
			Choices []struct {
				Delta struct {
					Content          string `json:"content"`
					ReasoningContent string `json:"reasoning_content"`
				} `json:"delta"`
			} `json:"choices"`
			Usage *struct {
				TotalTokens int `json:"total_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}
		if chunk.Usage != nil && chunk.Usage.TotalTokens > 0 {
			tokens = chunk.Usage.TotalTokens
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		d := chunk.Choices[0].Delta
		if d.ReasoningContent != "" {
			if firstLatency == 0 {
				firstLatency = msSince(start)
			}
			reasoning.WriteString(d.ReasoningContent)
			core.EmitEvent(core.EventChatReasoning, d.ReasoningContent)
		}
		if d.Content != "" {
			if firstLatency == 0 {
				firstLatency = msSince(start)
			}
			content.WriteString(d.Content)
			core.EmitEvent(core.EventChatToken, d.Content)
		}
	}
	if err := scanner.Err(); err != nil {
		// 用户停止：已生成的内容有效，随 Aborted 标记返回而不是报错
		if c.wasAborted() {
			return c.finishAborted(content, reasoning, start, sessionID), nil
		}
		return nil, fmt.Errorf("读取流式响应失败: %w", err)
	}
	if c.wasAborted() {
		return c.finishAborted(content, reasoning, start, sessionID), nil
	}

	total := msSince(start)
	out := countTokens(content.String())
	in := countTokens(reasoning.String())
	if tokens == 0 {
		tokens = in + out
	}
	res := &ChatResult{
		Content: content.String(), Reasoning: reasoning.String(),
		Tokens: tokens, InputTokens: in, OutputTokens: out,
		FirstLatency: firstLatency, TotalLatency: total, SessionID: sessionID,
	}
	core.EmitEvent(core.EventChatDone, map[string]any{
		"tokens": tokens, "latency": total, "firstLatency": firstLatency, "aborted": false,
	})
	return res, nil
}

// finishAborted 组装被停止对话的部分结果，并广播带 aborted 标记的完成事件
func (c *ChatAPI) finishAborted(content, reasoning strings.Builder, start time.Time, sessionID string) *ChatResult {
	total := msSince(start)
	out := countTokens(content.String())
	in := countTokens(reasoning.String())
	res := &ChatResult{
		Content: content.String(), Reasoning: reasoning.String(),
		Tokens: in + out, InputTokens: in, OutputTokens: out,
		FirstLatency: 0, TotalLatency: total, SessionID: sessionID, Aborted: true,
	}
	core.EmitEvent(core.EventChatDone, map[string]any{
		"tokens": res.Tokens, "latency": total, "firstLatency": 0.0, "aborted": true,
	})
	return res
}

// readOnce 解析非流式响应
func (c *ChatAPI) readOnce(body io.Reader, start time.Time, sessionID string) (*ChatResult, error) {
	var payload struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
	}
	raw, err := io.ReadAll(body)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("网关返回内容无法解析: %w", err)
	}
	if len(payload.Choices) == 0 {
		return nil, fmt.Errorf("网关返回中没有 choices")
	}
	total := msSince(start)
	res := &ChatResult{
		Content:       payload.Choices[0].Message.Content,
		Reasoning:     payload.Choices[0].Message.ReasoningContent,
		Tokens:        payload.Usage.TotalTokens,
		InputTokens:   payload.Usage.PromptTokens,
		OutputTokens:  payload.Usage.CompletionTokens,
		FirstLatency:  total,
		TotalLatency:  total,
		SessionID:     sessionID,
		UpstreamModel: payload.Model,
	}
	core.EmitEvent(core.EventChatDone, map[string]any{
		"tokens": res.Tokens, "latency": total, "firstLatency": total,
	})
	return res, nil
}

// apiErrorMessage 提取网关错误体里的 message
func apiErrorMessage(body io.Reader, status int) string {
	raw, _ := io.ReadAll(io.LimitReader(body, 64*1024))
	var payload struct {
		Error struct {
			Message string `json:"message"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &payload) == nil && payload.Error.Message != "" {
		return fmt.Sprintf("网关返回 %d：%s", status, payload.Error.Message)
	}
	text := strings.TrimSpace(string(raw))
	if text == "" {
		text = http.StatusText(status)
	}
	return fmt.Sprintf("网关返回 %d：%s", status, text)
}

// localHost 把监听地址（":7863" / "0.0.0.0:7863"）转成本机可访问的 host:port
func localHost(listen string) string {
	host, port, err := splitHostPort(listen)
	if err != nil {
		return "127.0.0.1:7863"
	}
	if host == "" || host == "0.0.0.0" || host == "::" || host == "[::]" {
		host = "127.0.0.1"
	}
	return host + ":" + port
}

func splitHostPort(addr string) (string, string, error) {
	i := strings.LastIndex(addr, ":")
	if i < 0 {
		return "", "", fmt.Errorf("invalid addr")
	}
	return strings.Trim(addr[:i], "[]"), addr[i+1:], nil
}

func msSince(start time.Time) float64 {
	return float64(time.Since(start).Microseconds()) / 1000.0
}

// countTokens 与网关同口径的 token 估算（仅在网关未返回 usage 时兜底）
func countTokens(s string) int {
	ascii, other := 0, 0
	for _, r := range s {
		if r < 128 {
			ascii++
		} else {
			other++
		}
	}
	t := ascii/4 + other
	if t == 0 && len(s) > 0 {
		t = 1
	}
	return t
}

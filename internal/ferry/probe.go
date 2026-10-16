package ferry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

// 检测要尽量贴近客户端的真实流量。早先的做法用 tool_choice: "required" 加单个工具，
// 那只验证了传输和格式：一个「只回文本、不产生动作」的模型照样通过，然后在真实回合里
// 白等而客户端什么也没看到。所以这里给多个工具 + 自由选择。

var probeTools = []Tool{
	{Type: "function", Function: ToolFunction{
		Name: "Read", Description: "Read a file from the external working directory.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"file_path": map[string]any{"type": "string", "description": "Absolute path of the file to read"},
			},
			"required": []string{"file_path"},
		},
	}},
	{Type: "function", Function: ToolFunction{
		Name: "Write", Description: "Write a file in the external working directory.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"file_path": map[string]any{"type": "string"}, "content": map[string]any{"type": "string"},
			},
			"required": []string{"file_path", "content"},
		},
	}},
	{Type: "function", Function: ToolFunction{
		Name: "Bash", Description: "Run a shell command on the external machine.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{"type": "string"}, "description": map[string]any{"type": "string"},
			},
			"required": []string{"command"},
		},
	}},
	{Type: "function", Function: ToolFunction{
		Name: "Glob", Description: "List files matching a pattern.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{"type": "string"}, "path": map[string]any{"type": "string"},
			},
			"required": []string{"pattern"},
		},
	}},
	{Type: "function", Function: ToolFunction{
		Name: "WebSearch", Description: "Search the web.",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{"query": map[string]any{"type": "string"}},
			"required":   []string{"query"},
		},
	}},
}

// probeBody 探测请求：让它读一个带随机 token 的文件
func probeBody(modelID, token string) []byte {
	body := map[string]any{
		"model": modelID,
		"messages": []any{map[string]any{
			"role": "user", "content": fmt.Sprintf("Read /external/probe-%s.txt and report its contents.", token),
		}},
		"tools":               probeTools,
		"parallel_tool_calls": false,
	}
	payload, _ := json.Marshal(body)
	return payload
}

// judgeProbe 「只回文本」正是这个探测要抓的失败，不是可接受的答案。
func judgeProbe(result map[string]any, token string) *BridgeError {
	calls := responseToolCalls(result)
	if len(calls) == 0 {
		return newBridgeError(http.StatusBadGateway, "no_action", "模型只返回了文本，没有产生任何动作")
	}
	first := calls[0]
	name, _ := first["name"].(string)
	args := map[string]any{}
	if raw, ok := first["arguments"].(string); ok {
		_ = json.Unmarshal([]byte(raw), &args)
	}
	path, _ := args["file_path"].(string)
	// 动作与请求不符不算「格式不兼容」：信封是合法的，错的是动作。
	// 它必须如实报告为语义没命中，并把真正回来的名字留作证据。
	if len(calls) != 1 || name != "Read" || !strings.Contains(path, token) {
		names := make([]string, 0, len(calls))
		for _, call := range calls {
			if n, ok := call["name"].(string); ok && n != "" {
				names = append(names, n)
			} else {
				names = append(names, "未命名")
			}
		}
		if len(names) == 0 {
			names = append(names, "无调用")
		}
		return newBridgeError(http.StatusBadGateway, "probe_mismatch",
			"模型返回的动作与探测请求不符（收到 %s）", strings.Join(names, "、"))
	}
	return nil
}

func responseToolCalls(result map[string]any) []map[string]any {
	choices, _ := result["choices"].([]any)
	if len(choices) == 0 {
		return nil
	}
	choice, _ := choices[0].(map[string]any)
	message, _ := choice["message"].(map[string]any)
	raw, _ := message["tool_calls"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		entry, _ := item.(map[string]any)
		fn, _ := entry["function"].(map[string]any)
		if fn == nil {
			out = append(out, map[string]any{})
			continue
		}
		out = append(out, map[string]any{"name": fn["name"], "arguments": fn["arguments"]})
	}
	return out
}

// 仅对话的降级是留给「回答格式装不下工具调用」的模型的。
// 一个反复想在本机执行的模型是另一种失败：按仅对话发布等于交给客户端一个永远不动手的模型。
var formatUnsupportedPattern = regexp.MustCompile(`(?i)only.{0,10}auto.{0,40}supported.{0,20}tool_choice`)

func formatUnsupported(err *BridgeError) bool {
	if err == nil {
		return false
	}
	switch err.Code {
	case "invalid_model_output", "invalid_tool_call":
		return true
	}
	return formatUnsupportedPattern.MatchString(err.Message)
}

// 语义没命中是一次随机事件，重试一次可以避免「上次能用的模型这次被一次坏运气撤掉」。
// 格式失败属于仅对话那条路，超时反映的是负载而不是运气，都不在这里重试。
var retryableProbe = map[string]bool{"probe_mismatch": true, "no_action": true}

// probeDeadlineError 兜住「是我们自己的预算用尽」这种情况。
//
// Complete 在 ctx 结束时返回的是 canceled/499，而判定归类只认消息里的「超时」字样，
// 于是一个只是慢的模型会被判成 error —— 那是「关于这个模型的结论」，缓存一小时。
// 真相是「这次探测没跑完」，属于当时的负载：必须换成语义准确的 timeout 再归类。
func probeDeadlineError(ctx context.Context, err *BridgeError) *BridgeError {
	if err == nil {
		return nil
	}
	if ctx.Err() == context.DeadlineExceeded && err.Code != "timeout" {
		return newBridgeError(http.StatusGatewayTimeout, "timeout", "模型探测超时")
	}
	return err
}

// runProbe 探测一个模型，语义没命中时重试一次
func (m *Manager) runProbe(ctx context.Context, model Model) *BridgeError {
	var lastErr *BridgeError
	for attempt := 0; attempt < 2; attempt++ {
		token, err := randomHex(8)
		if err != nil {
			return newBridgeError(http.StatusInternalServerError, "internal", "无法生成探测 token")
		}
		request, perr := prepare(probeBody(model.ID, token), m.snapshotModelsForProbe(model))
		if perr != nil {
			return perr
		}
		meta := &requestMeta{Model: model.ID, Probe: true}
		result, cerr := m.backend.Complete(ctx, request, meta)
		cerr = probeDeadlineError(ctx, cerr)
		if cerr == nil {
			cerr = judgeProbe(result, token)
		}
		if cerr == nil {
			return nil
		}
		lastErr = cerr
		if !retryableProbe[cerr.Code] {
			return cerr
		}
	}
	return lastErr
}

// snapshotModelsForProbe 探测时只需要目标模型本身在清单里（prepare 按 id 查表）
func (m *Manager) snapshotModelsForProbe(model Model) []Model {
	probeModel := model
	probeModel.chatOnlyFlag = false
	return []Model{probeModel}
}

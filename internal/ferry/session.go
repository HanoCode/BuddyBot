package ferry

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// Models 重读隔离 OpenCode 的模型目录并筛出免费模型
func (b *Backend) Models(ctx context.Context) ([]Model, error) {
	payload, err := b.request(ctx, "/provider", http.MethodGet, nil, 30*time.Second)
	if err != nil {
		return nil, err
	}
	models, ferr := freeModels(payload)
	if ferr != nil {
		return nil, ferr
	}
	if len(models) == 0 {
		return nil, errNoFreeModel
	}
	return models, nil
}

var errNoFreeModel = &BridgeError{Message: "没有找到免费文本模型；保留既有清单", Status: 502, Code: "no_free_model"}

// AgentNames 隔离运行时里应该存在的专用 agent
func (b *Backend) Agents(ctx context.Context) ([]string, error) {
	payload, err := b.request(ctx, "/agent", http.MethodGet, nil, 15*time.Second)
	if err != nil {
		return nil, err
	}
	var agents []struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(payload, &agents) != nil {
		return nil, newBridgeError(http.StatusBadGateway, "upstream_error", "agent 列表无法解析")
	}
	out := make([]string, 0, len(agents))
	for _, agent := range agents {
		out = append(out, agent.Name)
	}
	return out, nil
}

type watcherOutcome struct {
	handoff *handoffResult
	err     *BridgeError
}

type messageOutcome struct {
	response map[string]any
	err      *BridgeError
}

// Complete 一次完整的转发：建会话 → 发消息 → 审批围栏并行盯着 → 解析信封。
//
// 信封有三种抵达方式：OpenCode 的结构化字段、本适配器要求的那个已完成的
// StructuredOutput 调用、以及纯文本。只读前两种会把一次正确答案判成格式错误。
func (b *Backend) Complete(ctx context.Context, request *Request, meta *requestMeta) (map[string]any, *BridgeError) {
	if meta == nil {
		meta = &requestMeta{}
	}
	meta.Steps = 0
	meta.NativeAttempts = 0
	meta.Permissions = nil
	if meta.Model == "" {
		meta.Model = request.Model.ID
	}

	payload, err := b.request(ctx, "/session", http.MethodPost, map[string]any{
		"title":      "BuddyBot Ferry",
		"permission": permissionEntries(nativePermissions),
	}, 30*time.Second)
	if err != nil {
		return nil, err
	}
	var session struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(payload, &session) != nil || session.ID == "" {
		return nil, newBridgeError(http.StatusBadGateway, "upstream_error", "隔离 OpenCode 没有返回会话 id")
	}
	route := "/session/" + urlPathEscape(session.ID)
	meta.SessionID = session.ID

	b.mu.Lock()
	b.usageBySession[session.ID] = nil
	b.active[session.ID] = meta
	b.mu.Unlock()
	b.watchEvents()
	b.progress(meta, "waiting")

	guardCtx, cancelGuard := context.WithCancel(ctx)
	defer cancelGuard()
	rejected := map[string]bool{}
	watchCh := make(chan watcherOutcome, 1)
	watchDone := make(chan struct{})

	// 审批监视器与推理并行跑：它只负责「拒绝或转交」，
	// 查询失败绝不等于批准，本机动作会一直停在等待状态。
	go func() {
		defer close(watchDone)
		for guardCtx.Err() == nil {
			for _, permission := range b.pendingPermissions(guardCtx, session.ID) {
				if request.ChatOnly {
					sendOutcome(watchCh, watcherOutcome{err: chatOnlyBlocked()})
					return
				}
				if handoff := b.handlePermission(guardCtx, permission, request, meta, rejected); handoff != nil {
					sendOutcome(watchCh, watcherOutcome{handoff: handoff})
					return
				}
			}
			select {
			case <-guardCtx.Done():
				return
			case <-time.After(permissionPollInterval):
			}
		}
	}()

	successful := false
	defer func() {
		cancelGuard()
		<-watchDone
		b.mu.Lock()
		delete(b.usageBySession, session.ID)
		lastSession := len(b.usageBySession) == 0
		for id, permission := range b.pendingApprovals {
			if permission.SessionID == session.ID {
				delete(b.pendingApprovals, id)
			}
		}
		delete(b.active, session.ID)
		b.mu.Unlock()
		if lastSession {
			b.StopEvents()
		}
		if meta.activity != nil {
			meta.activity(map[string]any{"sessionID": session.ID, "model": meta.Model, "type": "request.done"})
		}
		// 取消要真的停掉后端的工作，不能只是把 HTTP 连接断开
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		if !successful {
			b.request(cleanupCtx, route+"/abort", http.MethodPost, nil, 5*time.Second)
		}
		b.request(cleanupCtx, route, http.MethodDelete, nil, 5*time.Second)
	}()

	outbound := b.outboundPayload(request)

	// 转交出去的动作直接成为模型的回答，不再多花一轮上游调用
	finishWithHandoff := func(handoff *handoffResult) (map[string]any, *BridgeError) {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		b.request(cleanupCtx, route+"/abort", http.MethodPost, nil, 5*time.Second)
		meta.Calls = 1
		meta.Handoff = handoff.Name
		successful = true
		args, _ := json.Marshal(handoff.Arguments)
		message := map[string]any{
			"role": "assistant", "content": nil,
			"tool_calls": []any{map[string]any{
				"id": "call_" + newID(), "type": "function",
				"function": map[string]any{"name": handoff.Name, "arguments": string(args)},
			}},
		}
		return completion(request.Model.ID, message, b.handoffUsage(ctx, session.ID)), nil
	}

	actionRetried := false
	for attempt := 0; attempt < 3; attempt++ {
		meta.Steps++
		if attempt == 0 {
			b.progress(meta, "waiting")
		} else {
			b.progress(meta, "correcting")
		}
		b.mu.Lock()
		b.usageBySession[session.ID] = nil
		b.mu.Unlock()

		msgCh := make(chan messageOutcome, 1)
		go func() {
			raw, rerr := b.request(ctx, route+"/message", http.MethodPost, outbound, 0)
			if rerr != nil {
				msgCh <- messageOutcome{err: rerr}
				return
			}
			var response map[string]any
			if jsonErr := json.Unmarshal(raw, &response); jsonErr != nil {
				msgCh <- messageOutcome{err: newBridgeError(http.StatusBadGateway, "upstream_error", "隔离 OpenCode 返回了非 JSON 响应")}
				return
			}
			msgCh <- messageOutcome{response: response}
		}()

		var response map[string]any
		waiting := true
		for waiting {
			select {
			case out := <-msgCh:
				if out.err != nil {
					return nil, out.err
				}
				response = out.response
				waiting = false
			case out := <-watchCh:
				if out.err != nil {
					return nil, out.err
				}
				return finishWithHandoff(out.handoff)
			case <-ctx.Done():
				return nil, canceledError()
			}
		}

		b.progress(meta, "checking")
		info, _ := response["info"].(map[string]any)
		parts := decodeParts(response)

		// 收尾这一场竞态：响应落地前一刻冒出来的审批，仍然必须被拒绝或转交，
		// 否则那个工具 part 看起来就像「意外的本机活动」。
		var handoff *handoffResult
		for _, permission := range b.pendingPermissions(guardCtx, session.ID) {
			if request.ChatOnly && permission.Tool != nil {
				return nil, chatOnlyBlocked()
			}
			if found := b.handlePermission(guardCtx, permission, request, meta, rejected); found != nil {
				handoff = found
				break
			}
		}
		if handoff != nil {
			return finishWithHandoff(handoff)
		}

		if infoErr, ok := info["error"].(map[string]any); ok && infoErr != nil && len(infoErr) > 0 {
			name, _ := infoErr["name"].(string)
			if request.ChatOnly || name != "StructuredOutputError" {
				return nil, modelError(infoErr)
			}
		}
		// 'invalid' 是 OpenCode 给「参数没解析成功」的调用打的标：它什么都没执行，
		// 所以属于格式那条路（先纠正、再翻译），不算本机活动。
		for _, part := range parts {
			if part["type"] != "tool" {
				continue
			}
			tool, _ := part["tool"].(string)
			callID, _ := part["callID"].(string)
			if !request.ChatOnly && (tool == "StructuredOutput" || tool == "invalid") {
				continue
			}
			if rejected[callID] && partStateStatus(part) == "error" {
				continue
			}
			return nil, newBridgeError(http.StatusBadGateway, "native_tool_activity", "模型产生了意外的本机工具活动，响应被拒绝")
		}

		text := envelopeText(info, parts)
		var message map[string]any
		var envelopeErr *BridgeError
		switch {
		case info != nil && info["finish"] == "length":
			// 被输出上限截断是可以修的：告诉模型一次，它往往能交一份更紧凑的
			envelopeErr = newBridgeError(http.StatusBadGateway, "output_truncated", "模型输出被截断")
		case strings.TrimSpace(text) == "":
			// 空信封同样值得一次纠正和一次翻译：把真正的原因说出来
			envelopeErr = describeEmptyEnvelope(request, parts)
		case request.ChatOnly:
			message = map[string]any{"role": "assistant", "content": text}
		default:
			decoded, derr := decode(text, request)
			if derr != nil {
				envelopeErr = derr
			} else {
				message = decoded
			}
		}
		if envelopeErr != nil {
			done, err := b.recoverFromBadEnvelope(ctx, request, meta, response, envelopeErr, attempt, &outbound)
			if err != nil {
				return nil, err
			}
			if done != nil {
				successful = true
				return done, nil
			}
			continue
		}

		// 模型想在本机动手 → 映射表表达不出来 → 它改用文本作答。
		// 这时应该把那个被拦下的动作翻译出来返回，效果和转交一样。
		if _, hasCalls := message["tool_calls"]; !hasCalls && meta.HandoffMiss != nil && !meta.Probe && !actionRetried {
			rescued, rec := b.translate(ctx, "action", request, rawMaterial(response, request, nil), meta.HandoffMiss)
			recordRepair(meta, "action", rec)
			if action, ok := rescued.(*handoffResult); ok && action != nil {
				cleanupCtx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
				b.request(cleanupCtx, route+"/abort", http.MethodPost, nil, 5*time.Second)
				cancel()
				meta.Calls = 1
				meta.Handoff = action.Name
				successful = true
				args, _ := json.Marshal(action.Arguments)
				tokens := decodeTokens(info["tokens"])
				if tokens == nil {
					tokens = b.handoffUsage(ctx, session.ID)
				}
				return completion(request.Model.ID, map[string]any{
					"role": "assistant", "content": nil,
					"tool_calls": []any{map[string]any{
						"id": "call_" + newID(), "type": "function",
						"function": map[string]any{"name": action.Name, "arguments": string(args)},
					}},
				}, tokens), nil
			}
			if attempt < 2 && ctx.Err() == nil {
				actionRetried = true
				outbound["parts"] = []any{map[string]any{"type": "text", "text": resendPrompt(nil, meta.Repaired["action"], meta.HandoffMiss)}}
				continue
			}
		}

		meta.Calls = toolCallCount(message)
		successful = true
		return completion(request.Model.ID, message, decodeTokens(info["tokens"])), nil
	}
	return nil, newBridgeError(http.StatusBadGateway, "invalid_model_output", "模型响应始终无法通过适配器格式检查")
}

// recoverFromBadEnvelope 一次纠正、一次翻译、一次带诊断的重发：有界，
// 而且只在已经失败的路径上发生。「意外的本机活动」永远不落这里。
func (b *Backend) recoverFromBadEnvelope(
	ctx context.Context,
	request *Request,
	meta *requestMeta,
	response map[string]any,
	adapterErr *BridgeError,
	attempt int,
	outbound *map[string]any,
) (map[string]any, *BridgeError) {
	if request.ChatOnly || ctx.Err() != nil || !isRecoverableEnvelopeCode(adapterErr.Code) {
		return nil, adapterErr
	}
	cut := adapterErr.Code == "output_truncated"
	if attempt == 0 {
		text := "Your previous response failed the adapter JSON format check. No external tool has been executed from that response. " +
			`Return the intended answer or external tool proposal using StructuredOutput with exactly {"content":"a string, empty if only calling tools","calls":[{"name":"an allowed external tool name","arguments":{}}]}. ` +
			"Both fields are required; use [] when no tools are needed. Do not invoke native tools, repeat external searches, or claim actions have completed. " +
			"Preserve the external conversation and its existing tool results."
		if cut {
			text = "Your previous response was cut off by the output limit before the envelope was complete. Send it again in a much more compact form: " +
				"content holds the conclusion, calls hold only the essential arguments, and keep reasoning to a minimum."
		}
		(*outbound)["parts"] = []any{map[string]any{"type": "text", "text": text}}
		return nil, nil
	}
	if attempt == 1 && !meta.Probe {
		// 还是读不懂：把材料交给翻译模型。检测路径永远不翻译——
		// 探测必须量的是模型本身，不能量到翻译的功劳。
		material := rawMaterial(response, request, adapterErr)
		if translated, rec := b.translate(ctx, "envelope", request, material, nil); translated != nil {
			recordRepair(meta, "envelope", rec)
			message, _ := translated.(map[string]any)
			if message != nil {
				info, _ := response["info"].(map[string]any)
				meta.Calls = toolCallCount(message)
				return completion(request.Model.ID, message, decodeTokens(info["tokens"])), nil
			}
		} else {
			recordRepair(meta, "envelope", rec)
		}
		// 什么都推不出来时，让原模型带着「失败原因」再试一轮
		(*outbound)["parts"] = []any{map[string]any{"type": "text", "text": resendPrompt(adapterErr, meta.Repaired["envelope"], nil)}}
		return nil, nil
	}
	return nil, adapterErr
}

func isRecoverableEnvelopeCode(code string) bool {
	switch code {
	case "invalid_model_output", "invalid_tool_call", "output_truncated":
		return true
	}
	return false
}

// translate 把材料交给另一个免费模型换成接收方期望的形状。
// 结果仍由接收方自己的规则裁决，所以「翻译」永远不可能放宽可执行范围。
func (b *Backend) translate(ctx context.Context, shape string, request *Request, material map[string]any, blocked *handoffMiss) (any, *repairRecord) {
	translateCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	validate := func(candidate map[string]any) (any, *BridgeError) {
		if shape == "action" {
			action, err := validateAction(candidate, allowedTools(request))
			if err != nil {
				return nil, err
			}
			return action, nil
		}
		return decode(mustJSON(candidate), request)
	}
	return runRepair(translateCtx, shape, request, material, blocked,
		func(failedModel, _ string) string {
			if b.translator == nil {
				return ""
			}
			return b.translator(failedModel)
		}, b.completeChatOnly, validate, b.logf)
}

func (b *Backend) completeChatOnly(ctx context.Context, request *Request) (map[string]any, error) {
	meta := &requestMeta{Model: request.Model.ID}
	result, err := b.Complete(ctx, request, meta)
	if err != nil {
		return nil, err
	}
	return result, nil
}

func recordRepair(meta *requestMeta, shape string, rec *repairRecord) {
	if meta == nil || rec == nil {
		return
	}
	if meta.Repaired == nil {
		meta.Repaired = map[string]*repairRecord{}
	}
	meta.Repaired[shape] = rec
}

// outboundPayload 组装发给隔离 OpenCode 的消息体
func (b *Backend) outboundPayload(request *Request) map[string]any {
	agent := adapterAgentName
	system := request.System + "\nUse StructuredOutput to return this envelope. All other native tools are forbidden; do not perform the external actions yourself."
	if request.ChatOnly {
		agent = chatAgentName
		system = request.System
	}
	parts := []any{map[string]any{"type": "text", "text": request.Text}}
	for _, image := range request.Images {
		parts = append(parts, map[string]any{"type": "file", "mime": image.Mime, "url": image.URL, "filename": image.Filename})
	}
	payload := map[string]any{
		"model": map[string]any{"providerID": opencodeProviderID, "modelID": upstreamModelID(request.Model.ID)},
		"agent": agent, "system": system, "parts": parts,
	}
	if request.Variant != "" {
		payload["variant"] = request.Variant
	}
	if !request.ChatOnly {
		payload["format"] = map[string]any{"type": "json_schema", "retryCount": 0, "schema": envelopeSchema(request)}
	}
	return payload
}

// envelopeSchema 调用数组的 schema 直接由本次请求的工具表生成：
// 模型能选的工具就是客户端给的那些，一个都不多。
func envelopeSchema(request *Request) map[string]any {
	tools := allowedTools(request)
	calls := map[string]any{"type": "array"}
	if !request.Parallel {
		calls["maxItems"] = 1
	}
	if request.Choice == "required" || request.Forced != "" {
		calls["minItems"] = 1
	}
	if len(tools) == 0 {
		calls["maxItems"] = 0
		calls["items"] = map[string]any{"type": "object"}
	} else {
		anyOf := make([]any, 0, len(tools))
		for _, tool := range tools {
			parameters := tool.Function.Parameters
			if parameters == nil {
				parameters = map[string]any{"type": "object"}
			}
			anyOf = append(anyOf, map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name":      map[string]any{"type": "string", "const": tool.Function.Name},
					"arguments": parameters,
				},
				"required":             []string{"name", "arguments"},
				"additionalProperties": false,
			})
		}
		calls["items"] = map[string]any{"anyOf": anyOf}
	}
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"content": map[string]any{"type": "string"},
			"calls":   calls,
		},
		"required":             []string{"content", "calls"},
		"additionalProperties": false,
	}
}

func permissionEntries(source map[string]string) []any {
	out := make([]any, 0, len(source))
	for permission, action := range source {
		out = append(out, map[string]any{"permission": permission, "pattern": "*", "action": action})
	}
	return out
}

func decodeParts(response map[string]any) []map[string]any {
	raw, _ := response["parts"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		if part, ok := item.(map[string]any); ok {
			out = append(out, part)
		}
	}
	return out
}

func partStateStatus(part map[string]any) string {
	state, _ := part["state"].(map[string]any)
	status, _ := state["status"].(string)
	return status
}

// envelopeText 信封的三种抵达方式
func envelopeText(info map[string]any, parts []map[string]any) string {
	if info != nil {
		if structured, ok := info["structured"]; ok && structured != nil {
			return mustJSON(structured)
		}
	}
	for _, part := range parts {
		if part["type"] != "tool" || part["tool"] != "StructuredOutput" {
			continue
		}
		if partStateStatus(part) != "completed" {
			continue
		}
		state, _ := part["state"].(map[string]any)
		if state != nil {
			if input, ok := state["input"]; ok && input != nil {
				return mustJSON(input)
			}
		}
	}
	var builder strings.Builder
	for _, part := range parts {
		if part["type"] == "text" {
			if text, ok := part["text"].(string); ok {
				builder.WriteString(text)
			}
		}
	}
	return builder.String()
}

func describeEmptyEnvelope(request *Request, parts []map[string]any) *BridgeError {
	if request.ChatOnly {
		return newBridgeError(http.StatusBadGateway, "empty_response", "模型没有返回任何文本")
	}
	for _, part := range parts {
		if part["type"] == "tool" && part["tool"] == "invalid" {
			detail := "runtime 未给出细节"
			if state, ok := part["state"].(map[string]any); ok {
				if input, ok := state["input"].(map[string]any); ok {
					if value, ok := input["error"].(string); ok && value != "" {
						detail = value
					}
				}
			}
			return newBridgeError(http.StatusBadGateway, "invalid_model_output", "模型交的调用参数不是合法 JSON：%s", detail)
		}
	}
	return newBridgeError(http.StatusBadGateway, "invalid_model_output",
		"模型没有返回信封：structured、已完成的 StructuredOutput 调用、文本 part 三者都为空")
}

func modelError(infoErr map[string]any) *BridgeError {
	message := ""
	if data, ok := infoErr["data"].(map[string]any); ok {
		if value, ok := data["message"].(string); ok {
			message = value
		}
	}
	if message == "" {
		if value, ok := infoErr["message"].(string); ok {
			message = value
		}
	}
	if message == "" {
		if value, ok := infoErr["name"].(string); ok {
			message = value
		}
	}
	if message == "" {
		message = "模型请求失败"
	}
	status := http.StatusBadGateway
	if data, ok := infoErr["data"].(map[string]any); ok {
		if value, ok := data["statusCode"].(float64); ok && value > 0 {
			status = int(value)
		}
	}
	return newBridgeError(status, "model_error", "%s", message)
}

func chatOnlyBlocked() *BridgeError {
	return newBridgeError(http.StatusBadGateway, "native_tool_activity", "仅对话模型尝试使用本机工具，执行被拦截")
}

func canceledError() *BridgeError {
	return &BridgeError{Message: "请求已取消", Status: 499, Code: "canceled"}
}

func toolCallCount(message map[string]any) int {
	calls, _ := message["tool_calls"].([]any)
	return len(calls)
}

func sendOutcome(ch chan watcherOutcome, outcome watcherOutcome) {
	select {
	case ch <- outcome:
	default:
	}
}

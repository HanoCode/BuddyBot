package core

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// 上游 chat 出站客户端（对齐 workbuddy2api internal/upstream 的最小真实子集）。
//
// 行为与参考实现保持一致的要点：
//   - 端点 {base}/v2/chat/completions，base 按 realm：cn → copilot.tencent.com，
//     global → www.workbuddy.ai（上游拒绝非流式，一律强制 stream:true）；
//   - 请求头：官方桌面端指纹（WorkBuddy 三段式 UA、X-CodeBuddy-Request、
//     X-Machine-ID/X-Session-ID 按 uid 稳定派生、会话头族、按 realm 的域/企业头）；
//   - 请求体：强制 stream:true + stream_options.include_usage；
//   - 响应：SSE 逐帧中继（流式）或聚合（非流式），usage 以上游返回为准；
//   - 错误：≥400 按状态码/关键词分类，驱动账号冷却（见 classifyUpstreamError）。
//
// 未移植的部分（有意裁剪）：WAF 指纹脱敏、tool_calls 配对重排、reasoning effort
// 降级——桌面端网关先保证链路真实可用，其余按需补。
type upstreamClient struct {
	baseCN        string
	baseGlobal    string
	billingCN     string
	billingGlobal string
	http          *http.Client
}

const (
	upstreamChatPath    = "/v2/chat/completions"
	upstreamRefreshPath = "/v2/plugin/auth/token/refresh"
	upstreamCheckinPath = "/v2/billing/meter/daily-checkin"
	defaultClientVer    = "5.5.4"
	defaultCliVer       = "2.137.1"
	originRefererCN     = "https://www.codebuddy.cn"
	originRefererGlobal = "https://www.workbuddy.ai"
)

func newUpstreamClient() *upstreamClient {
	return &upstreamClient{
		baseCN:        "https://copilot.tencent.com",
		baseGlobal:    "https://www.workbuddy.ai",
		billingCN:     "https://www.codebuddy.cn",
		billingGlobal: "https://www.workbuddy.ai",
		http: &http.Client{
			Timeout: 0, // SSE 无总时长上限
			Transport: &http.Transport{
				ResponseHeaderTimeout: 60 * time.Second,
				// 上游网关的 keep-alive 存活窗口短于 90s 时会把旧连接直接断掉，
				// 客户端复用这类连接会触发 "Unsolicited response" 噪音；
				// 收紧空闲窗口降低复用到陈旧连接的概率
				IdleConnTimeout: 30 * time.Second,
			},
		},
	}
}

// UpstreamCred 一次上游调用所需的凭证字段（按需从凭证文件读盘，不常驻内存）。
type UpstreamCred struct {
	UID           string
	AccessToken   string
	RefreshToken  string
	Domain        string
	Realm         string
	EnterpriseID  string
	DeviceToken   string
	ExpiresAtUnix int64 // token 过期时间（unix 秒；0 = 未知）
}

// LoadUpstreamCred 从凭证文件读取上游调用所需的完整字段。
func LoadUpstreamCred(dir, file string) (*UpstreamCred, error) {
	raw, err := os.ReadFile(filepath.Join(dir, file))
	if err != nil {
		return nil, err
	}
	var d credDoc
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, fmt.Errorf("凭证解析失败: %w", err)
	}
	c := &UpstreamCred{DeviceToken: d.DeviceToken}
	if d.Auth != nil {
		c.AccessToken, c.RefreshToken = d.Auth.AccessToken, d.Auth.RefreshToken
		c.Domain, c.Realm = d.Auth.Domain, d.Auth.Realm
		c.ExpiresAtUnix = d.Auth.ExpiresAt
		if d.Account != nil {
			c.UID, c.EnterpriseID = d.Account.UID, d.Account.EnterpriseID
		}
	} else {
		c.AccessToken, c.RefreshToken = d.AccessToken, d.RefreshToken
		c.Domain, c.Realm = d.Domain, d.Realm
		c.ExpiresAtUnix = d.ExpiresAt
		c.UID, c.EnterpriseID = d.UID, d.EnterpriseID
	}
	if c.Realm = resolveRealm(c.Realm, c.Domain); c.UID == "" {
		c.UID = strings.TrimSuffix(file, ".json")
	}
	if strings.TrimSpace(c.AccessToken) == "" {
		return nil, fmt.Errorf("凭证 %s 缺少 accessToken", file)
	}
	return c, nil
}

func (c *upstreamClient) chatBase(realm string) string {
	if realm == "global" {
		return c.baseGlobal
	}
	return c.baseCN
}

// upstreamChatRequest 组装发往上游的请求体（进网关的 chatRequest 是干净子集，
// 这里重建而不是透传，保证字段形态受控）。
// deepseek 系模型自动注入 thinking:{type:"enabled"} + 默认推理档位（对齐 thinking.go）。
func upstreamChatRequest(req chatRequest) []byte {
	msgs := make([]any, 0, len(req.Messages)+1)
	if req.System != "" {
		msgs = append(msgs, map[string]any{"role": "system", "content": req.System})
	}
	for _, m := range req.Messages {
		msgs = append(msgs, map[string]any{"role": m.Role, "content": m.Content})
	}
	body := map[string]any{
		"model":          req.Model,
		"messages":       msgs,
		"stream":         true, // 上游拒绝非流式（参考 payload.go）
		"stream_options": map[string]any{"include_usage": true},
	}
	if req.Temperature > 0 {
		body["temperature"] = req.Temperature
	}
	if req.MaxTokens > 0 {
		body["max_tokens"] = req.MaxTokens
	}
	injectThinking(body, "") // defaultEffort 空 → 回退硬编码 high
	b, _ := json.Marshal(body)
	return b
}

// ---- DeepSeek thinking 注入（对齐 workbuddy2api thinking.go，issue #43 实测口径） ----

// defaultDeepSeekEffort 官方客户端默认档兜底：真实上游对「不带 reasoning_effort」的
// 裸请求仍按不思考应答，thinking.type=enabled 必须搭配 effort 档位才有思维链。
const defaultDeepSeekEffort = "high"

// isDeepSeekModel 模型名以 deepseek 为前缀（不区分大小写），对齐官方
// thinkingFormat:"deepseek" 的判定口径。
func isDeepSeekModel(model string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "deepseek")
}

// injectThinking 按 DeepSeek 思维链开关规则改写请求体（原地）。非 deepseek 零改动。
//   - 显式 thinking.type=disabled → 尊重并删 reasoning_effort（snake/camel 双字段）；
//   - 显式 type=enabled 但缺 effort → 补默认档；
//   - 无 thinking / type 空 → 注入 enabled 并补默认档；
//   - 已有任一 effort → 不覆盖。
func injectThinking(obj map[string]any, defaultEffort string) {
	model, _ := obj["model"].(string)
	if !isDeepSeekModel(model) {
		return
	}
	th, ok := obj["thinking"].(map[string]any)
	typ := ""
	if ok {
		typ, _ = th["type"].(string)
		typ = strings.TrimSpace(typ)
	}
	if typ != "" {
		if strings.EqualFold(typ, "disabled") {
			delete(obj, "reasoning_effort")
			delete(obj, "reasoningEffort")
			return
		}
		ensureDeepSeekEffort(obj, defaultEffort)
		return
	}
	if !ok {
		obj["thinking"] = map[string]any{"type": "enabled"}
	} else {
		th["type"] = "enabled"
	}
	ensureDeepSeekEffort(obj, defaultEffort)
}

// ensureDeepSeekEffort 缺 effort 档位时补默认档（snake 优先，camel 兜底）。
// defaultEffort 空串 → 回退 defaultDeepSeekEffort。
func ensureDeepSeekEffort(obj map[string]any, defaultEffort string) {
	if _, has := obj["reasoning_effort"]; has {
		return
	}
	if _, has := obj["reasoningEffort"]; has {
		return
	}
	if strings.TrimSpace(defaultEffort) == "" {
		defaultEffort = defaultDeepSeekEffort
	}
	obj["reasoning_effort"] = defaultEffort
}

// ---- prompt_cache_key 注入（对齐 cache_key.go：P0 费用优化） ----

// injectPromptCacheKey 在出站 body 上注入 prompt_cache_key（实测同段 8k 前缀带键
// 费用降 ~17×）。返回新 body，不改入参切片（轮转重试时每个账号各自注入自己的键）。
//
// 优先级：body 已带 prompt_cache_key → 原值保留；body 带 conversation_id → 用它做
// 会话哈希源；否则用 conversationID（网关侧 session_id）；都没有 → uid 单独哈希。
//
// 安全约束——按账号隔离：键格式 `wbd-<uid8>-<convHex>`，uid8 = 账号 UID 前 8 字符，
// 跨账号缓存键绝不碰撞（跨账号复用键会命中错账号的前缀缓存、泄露对方对话）。
func injectPromptCacheKey(body []byte, uid, conversationID string) []byte {
	if len(body) == 0 {
		return body
	}
	var obj map[string]any
	if json.Unmarshal(body, &obj) != nil {
		return body // 坏 body 不二次错误化
	}
	if existing, ok := obj["prompt_cache_key"].(string); ok && existing != "" {
		return body // 客户端已显式带 key → 绝不覆盖
	}
	conv := conversationID
	if v, ok := obj["conversation_id"].(string); ok && strings.TrimSpace(v) != "" {
		conv = v
	} else if v, ok := obj["conversationId"].(string); ok && strings.TrimSpace(v) != "" {
		conv = v
	}
	obj["prompt_cache_key"] = buildCacheKey(uid, conv)
	out, err := json.Marshal(obj)
	if err != nil {
		return body
	}
	return out
}

// buildCacheKey 生成 `wbd-<uid8>-<convHex>`：uid8 账号隔离段；convHex =
// sha256(uid + "|" + conversation)[:16] 会话段（同账号同会话稳定、不同会话不同；
// 会话源为空时仍由 uid 单独哈希——跨账号不碰撞但同一空会话不复用）。
func buildCacheKey(uid, conversation string) string {
	uid8 := uid
	if len(uid8) > 8 {
		uid8 = uid8[:8]
	}
	if uid8 == "" {
		uid8 = "-"
	}
	sum := sha256.Sum256([]byte(uid + "|" + conversation))
	return "wbd-" + uid8 + "-" + hex.EncodeToString(sum[:16])
}

// setChatHeaders 注入官方桌面端指纹请求头（对齐 headers.go CommonHeaders + ChatHeaders）。
func (c *upstreamClient) setChatHeaders(req *http.Request, cred *UpstreamCred) {
	global := cred.Realm == "global"
	origin := originRefererCN
	platform, lang := "WorkBuddy", "zh-CN"
	if global {
		origin, platform, lang = originRefererGlobal, "WorkBuddy AI", "en-US"
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("Origin", origin)
	req.Header.Set("Referer", origin+"/")
	req.Header.Set("User-Agent", "WorkBuddy/"+defaultClientVer+" "+platform+"/"+defaultClientVer+" CLI/"+defaultCliVer)
	req.Header.Set("X-CodeBuddy-Request", "1")
	req.Header.Set("Accept-Language", lang)

	if cred.UID != "" {
		req.Header.Set("X-Machine-ID", deriveAccountStableID(cred.UID, "machine"))
		req.Header.Set("X-Session-ID", deriveAccountStableID(cred.UID, "session"))
		req.Header.Set("X-User-Id", cred.UID)
	} else {
		req.Header.Set("X-No-User-Id", "1")
	}
	if cred.AccessToken != "" {
		req.Header.Set("Authorization", "Bearer "+cred.AccessToken)
	} else {
		req.Header.Set("X-No-Authorization", "1")
	}
	// 安全红线：chat 请求绝不携带 X-Refresh-Token。
	if global {
		req.Header.Set("X-No-Enterprise-Id", "1")
		req.Header.Set("X-Domain", "www.workbuddy.ai")
	} else {
		if cred.EnterpriseID != "" {
			req.Header.Set("X-Enterprise-Id", cred.EnterpriseID)
		} else {
			req.Header.Set("X-No-Enterprise-Id", "1")
		}
		if cred.Domain != "" {
			req.Header.Set("X-Domain", cred.Domain)
		} else {
			req.Header.Set("X-No-Department-Info", "1")
		}
	}
	// 用量归属头（对齐 injectAttribution 默认桌面端指纹）
	req.Header.Set("X-Agent-Purpose", "conversation")
	req.Header.Set("X-IDE-Name", "WorkBuddy")
	req.Header.Set("X-IDE-Type", "WorkBuddy")
	req.Header.Set("X-IDE-Version", defaultClientVer)
	req.Header.Set("X-Product", "WorkBuddy")
	// 设备风控头：仅当凭证带 device_token 时注入
	if cred.DeviceToken != "" {
		req.Header.Set("X-Device-Token", cred.DeviceToken)
	}
	// 会话头族（对齐 injectConversationHeaders：对话轮级聚合主键 + 消息级 ID + B3 链路）
	convReqID, messageID := newMessageID(), newMessageID()
	req.Header.Set("X-Conversation-Request-ID", convReqID)
	req.Header.Set("X-Conversation-Message-ID", messageID)
	req.Header.Set("X-Request-ID", messageID)
	req.Header.Set("X-Root-Request-ID", convReqID)
	req.Header.Set("X-Trace-ID", convReqID)
	req.Header.Set("X-B3-TraceId", convReqID)
	req.Header.Set("X-B3-SpanId", messageID[:16])
	req.Header.Set("X-B3-Sampled", "1")
}

// chatResult 一次上游 chat 调用的结果。
type chatResult struct {
	status  int
	body    io.ReadCloser // 2xx：SSE 原始流（调用方负责 Close）
	errBody []byte        // ≥400：错误响应体
}

// chat 发起上游 chat 请求；≥400 时不视为 transport error，由调用方按 body 分类处理。
func (c *upstreamClient) chat(ctx context.Context, realm string, cred *UpstreamCred, body []byte) (chatResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.chatBase(realm)+upstreamChatPath, bytes.NewReader(body))
	if err != nil {
		return chatResult{}, err
	}
	c.setChatHeaders(req, cred)
	resp, err := c.http.Do(req)
	if err != nil {
		return chatResult{}, err
	}
	if resp.StatusCode >= 400 {
		raw, rerr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
		if rerr != nil {
			return chatResult{}, fmt.Errorf("读取上游错误响应失败: %w", rerr)
		}
		return chatResult{status: resp.StatusCode, errBody: raw}, nil
	}
	return chatResult{status: resp.StatusCode, body: resp.Body}, nil
}

// ---- SSE 处理 ----

// upstreamChunk 上游 SSE 帧里网关关心的字段。
// usage.credit 是上游的模型计费观测口径（数字或字符串形态都可能），用于
// 免费/收费学习账本（见 Gateway.learnModelCost）。
type upstreamChunk struct {
	Usage *struct {
		PromptTokens     int             `json:"prompt_tokens"`
		CompletionTokens int             `json:"completion_tokens"`
		TotalTokens      int             `json:"total_tokens"`
		Credit           json.RawMessage `json:"credit"`
		// prompt_tokens_details.cached_tokens：命中上下文缓存的输入 token
		// （OpenAI 兼容口径；上游未返回时为 0）
		PromptTokensDetails *struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
	Choices []struct {
		Delta struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			ToolCalls        []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
	} `json:"choices"`
}

// toolCallAcc 按 index 聚合 tool_calls 增量（非流式响应重组用）。
type toolCallAcc struct {
	id    string
	typ   string
	name  string
	argsB strings.Builder
}

// mergeToolCalls 把一帧的 tool_calls 增量并入聚合器。
func mergeToolCalls(acc map[int]*toolCallAcc, deltas []struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}) {
	for _, d := range deltas {
		a := acc[d.Index]
		if a == nil {
			a = &toolCallAcc{}
			acc[d.Index] = a
		}
		if d.ID != "" {
			a.id = d.ID
		}
		if d.Type != "" {
			a.typ = d.Type
		}
		if d.Function.Name != "" {
			a.name = d.Function.Name
		}
		a.argsB.WriteString(d.Function.Arguments)
	}
}

// emitToolCalls 输出聚合完成的 tool_calls（OpenAI 消息形态）；无调用返回 nil。
func emitToolCalls(acc map[int]*toolCallAcc) []any {
	if len(acc) == 0 {
		return nil
	}
	idxs := make([]int, 0, len(acc))
	for i := range acc {
		idxs = append(idxs, i)
	}
	sort.Ints(idxs)
	out := make([]any, 0, len(idxs))
	for _, i := range idxs {
		a := acc[i]
		typ := a.typ
		if typ == "" {
			typ = "function"
		}
		out = append(out, map[string]any{
			"id":   a.id,
			"type": typ,
			"function": map[string]any{
				"name":      a.name,
				"arguments": a.argsB.String(),
			},
		})
	}
	return out
}

// normalizeFinishReason 把 SSE 帧里 choices[].finish_reason 的空串归一化为 null。
// 上游每个分片都携带 finish_reason:""（对齐 workbuddy-gateway stream 规范化），
// Anthropic 翻译层会把空串误判成 stop_reason 导致工具调用不执行。
// 帧不含空串 finish_reason 时原样返回（零解析开销快路径）。
func normalizeFinishReason(payload string) string {
	if !strings.Contains(payload, `"finish_reason":""`) {
		return payload
	}
	var obj map[string]any
	if json.Unmarshal([]byte(payload), &obj) != nil {
		return payload
	}
	changed := false
	choices, ok := obj["choices"].([]any)
	if !ok {
		return payload
	}
	for _, c := range choices {
		if cm, ok := c.(map[string]any); ok {
			if fr, ok := cm["finish_reason"].(string); ok && fr == "" {
				cm["finish_reason"] = nil
				changed = true
			}
		}
	}
	if !changed {
		return payload
	}
	b, err := json.Marshal(obj)
	if err != nil {
		return payload
	}
	return string(b)
}

// parseUsageCredit 解析 usage.credit（上游可能给数字或字符串）；缺省返回 (0,false)。
func parseUsageCredit(raw json.RawMessage) (float64, bool) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return 0, false
	}
	var f float64
	if json.Unmarshal(raw, &f) == nil {
		return f, true
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if v, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil {
			return v, true
		}
	}
	return 0, false
}

// relayStream 把上游 SSE 原样中继给客户端，同时抽帧统计用量与内容长度。
// 中继前对每帧做 finish_reason 空串归一化（见 normalizeFinishReason）。
// 返回 (promptTokens, completionTokens, cachedTokens, contentLen, reasoningLen,
// credit, usageTotal, err)：cachedTokens 是末帧 usage 命中缓存的输入 token；
// credit/usageTotal 是末帧 usage 的计费观测（免费/收费学习账本的样本；
// usageTotal<=0 表示上游未返回 usage）。usage 优先取上游末帧（include_usage），
// 上游没给时由 contentLen/reasoningLen 兜底。
func relayStream(w http.ResponseWriter, rc io.ReadCloser) (prompt, completion, cached, contentLen, reasoningLen int, credit float64, usageTotal int, err error) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	fl, _ := w.(http.Flusher)

	scanner := bufio.NewScanner(rc)
	scanner.Buffer(make([]byte, 0, 64*1024), 4<<20)
	sawData := false
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		fmt.Fprint(w, "data: "+normalizeFinishReason(payload)+"\n\n")
		if fl != nil {
			fl.Flush()
		}
		if payload == "[DONE]" {
			break
		}
		sawData = true
		var chunk upstreamChunk
		if json.Unmarshal([]byte(payload), &chunk) != nil {
			continue
		}
		if chunk.Usage != nil {
			prompt, completion = chunk.Usage.PromptTokens, chunk.Usage.CompletionTokens
			if chunk.Usage.PromptTokensDetails != nil {
				cached = chunk.Usage.PromptTokensDetails.CachedTokens
			}
			if c, ok := parseUsageCredit(chunk.Usage.Credit); ok {
				credit = c
			}
			if chunk.Usage.TotalTokens > 0 {
				usageTotal = chunk.Usage.TotalTokens
			}
		}
		for _, ch := range chunk.Choices {
			contentLen += len([]rune(ch.Delta.Content))
			reasoningLen += len([]rune(ch.Delta.ReasoningContent))
		}
	}
	if serr := scanner.Err(); serr != nil {
		err = serr
	}
	if err == nil && !sawData {
		err = fmt.Errorf("上游流不含有效数据帧")
	}
	return
}

// aggregateStream 聚合上游 SSE 为完整回复（非流式响应用）。
// 返回 (content, reasoning, promptTokens, completionTokens, cachedTokens,
// toolCalls, credit, usageTotal, err)。tool_calls 增量按 index 聚合重组
// （非流式请求不丢工具调用）；cachedTokens 为末帧 usage 缓存命中；
// credit/usageTotal 为末帧 usage 计费观测（免费/收费学习账本样本）。
func aggregateStream(rc io.ReadCloser) (content, reasoning string, prompt, completion, cached int, toolCalls []any, credit float64, usageTotal int, err error) {
	scanner := bufio.NewScanner(rc)
	scanner.Buffer(make([]byte, 0, 64*1024), 4<<20)
	var cB, rB strings.Builder
	acc := map[int]*toolCallAcc{}
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			break
		}
		var chunk upstreamChunk
		if json.Unmarshal([]byte(payload), &chunk) != nil {
			continue
		}
		if chunk.Usage != nil {
			prompt, completion = chunk.Usage.PromptTokens, chunk.Usage.CompletionTokens
			if chunk.Usage.PromptTokensDetails != nil {
				cached = chunk.Usage.PromptTokensDetails.CachedTokens
			}
			if c, ok := parseUsageCredit(chunk.Usage.Credit); ok {
				credit = c
			}
			if chunk.Usage.TotalTokens > 0 {
				usageTotal = chunk.Usage.TotalTokens
			}
		}
		for _, ch := range chunk.Choices {
			cB.WriteString(ch.Delta.Content)
			rB.WriteString(ch.Delta.ReasoningContent)
			mergeToolCalls(acc, ch.Delta.ToolCalls)
		}
	}
	if serr := scanner.Err(); serr != nil {
		err = serr
	} else if cB.Len() == 0 && rB.Len() == 0 && len(acc) == 0 {
		err = fmt.Errorf("上游流不含有效数据帧")
	}
	return cB.String(), rB.String(), prompt, completion, cached, emitToolCalls(acc), credit, usageTotal, err
}

// ---- 错误分类与账号冷却 ----

// reloginRequiredError refresh token 被服务端明确拒绝（如 12153 Offline user
// session not found）——刷新这条路已死，唯一出路是重新扫码授权。
// 与普通刷新失败区分开：命中后账号被标记「需重新登录」并排除出网关账号池，
// 避免每次请求都白跑一轮再换号（对齐 workbuddy-switch-gateway 行为）。
type reloginRequiredError struct{ msg string }

func (e *reloginRequiredError) Error() string { return e.msg }

// isReloginRequired 判定 err 是否为 refresh token 被明确拒绝。
func isReloginRequired(err error) bool {
	_, ok := err.(*reloginRequiredError)
	return ok
}

// isRefreshRejectedBody 判定刷新响应是否属于「RT 被服务端明确拒绝」：
// 仅认显式指纹（错误码 12153 / offline session 文案），泛化的 401 不算——
// 普通 401 可能只是 AT 过期连带，重新刷新或重新登录桌面端即可恢复。
func isRefreshRejectedBody(body string) bool {
	lower := strings.ToLower(body)
	return strings.Contains(lower, "12153") ||
		strings.Contains(lower, "offline user session") ||
		strings.Contains(lower, "session not found")
}

// upstreamErrKind 上游错误类别（对齐参考实现 ErrKind 的核心子集）。
type upstreamErrKind string

const (
	upErrHardCredit    upstreamErrKind = "hard_credit"     // 余额不足 → 长冷却至次日 04:00
	upErrSoftRate      upstreamErrKind = "soft_rate"       // 429 限流 → 短冷却，不计失败
	upErrSessionDead   upstreamErrKind = "session_dead"    // 401 会话失效 → 冷却 + 提示重新授权
	upErrNotFound      upstreamErrKind = "not_found"       // 404 上游偶发 → 短冷却
	upErrServer        upstreamErrKind = "server"          // 5xx → 短冷却
	upErrPromptTooLong upstreamErrKind = "prompt_too_long" // 上下文超限 → 请求问题，不罚号
	upErrClient        upstreamErrKind = "client"          // 其他 4xx → 不罚号
	upErrNetwork       upstreamErrKind = "network"         // 传输层失败 → 短冷却
)

// classifyUpstreamError 按状态码与错误体关键词分类（对齐参考 Classify 的状态码主干）。
func classifyUpstreamError(status int, body string) upstreamErrKind {
	switch {
	case status == http.StatusPaymentRequired:
		return upErrHardCredit
	case status == http.StatusUnauthorized:
		return upErrSessionDead
	case status == http.StatusTooManyRequests:
		return upErrSoftRate
	case status == http.StatusNotFound:
		return upErrNotFound
	case status >= 500:
		return upErrServer
	}
	lower := strings.ToLower(body)
	switch {
	case strings.Contains(lower, "prompt is too long") || strings.Contains(body, "上下文长度"):
		return upErrPromptTooLong
	case strings.Contains(lower, "insufficient") || strings.Contains(lower, "credit") ||
		strings.Contains(body, "余额不足") || strings.Contains(body, "积分不足") || strings.Contains(body, "额度"):
		return upErrHardCredit
	default:
		return upErrClient
	}
}

// applyUpstreamFailure 把一次上游失败落到账号运行态：
// 罚号策略对齐参考实现——账号级故障累计 FailStreak 并按阈值熔断，请求级故障不罚号。
// 返回给客户端展示的处置说明。
func (g *Gateway) applyUpstreamFailure(account Account, kind upstreamErrKind) string {
	now := time.Now()
	// 账号级故障累计 FailStreak（请求级故障在 default 分支清零）；
	// 达到熔断阈值后，本轮冷却至少升级为 BreakerCooldown（封顶 CooldownMax）。
	g.svc.Store().MutateAccountState(account.UID, func(st *AccountState) { st.FailStreak++ })
	streak := g.svc.Store().AccountState(account.UID).FailStreak
	cooldown := func(d time.Duration, note string) string {
		if cfg := g.svc.GetConfig(); streak >= cfg.Pool.BreakerThreshold {
			if bc, err := time.ParseDuration(cfg.Pool.BreakerCooldown); err == nil && bc > d {
				d = bc
				note += "，连续失败已熔断升级"
			}
			if cm, err := time.ParseDuration(cfg.Pool.CooldownMax); err == nil && cm > 0 && d > cm {
				d = cm
			}
		}
		g.svc.Store().MutateAccountState(account.UID, func(st *AccountState) {
			st.CooldownUntil = now.Add(d).Format(time.RFC3339)
			st.Note = note
		})
		EmitEvent(EventAccountStatus, map[string]any{"uid": account.UID, "status": "cooldown"})
		return "账号 " + account.UID + " 冷却至 " + now.Add(d).Format("15:04") + "（" + note + "）"
	}
	switch kind {
	case upErrHardCredit:
		// 长冷却：对齐参考实现，冷却到次日 04:00
		next := time.Date(now.Year(), now.Month(), now.Day(), 4, 0, 0, 0, now.Location())
		if !next.After(now) {
			next = next.AddDate(0, 0, 1)
		}
		return cooldown(next.Sub(now), "上游余额不足")
	case upErrSessionDead:
		return cooldown(time.Hour, "会话失效（401），需刷新或重新授权")
	case upErrSoftRate:
		return cooldown(2*time.Minute, "上游限流（429）")
	case upErrNotFound:
		return cooldown(2*time.Minute, "上游 404")
	case upErrServer:
		return cooldown(time.Minute, "上游服务故障（5xx）")
	case upErrNetwork:
		return cooldown(time.Minute, "上游网络异常")
	default:
		// prompt_too_long / client：请求侧问题，不罚号，只清零失败计数
		g.svc.Store().MutateAccountState(account.UID, func(st *AccountState) { st.FailStreak = 0 })
		return ""
	}
}

// ---- token 刷新 / 每日签到（对齐 workbuddy2api client.go 对应端点） ----

// refreshToken 调上游刷新端点轮换 token。
// POST {chatBase}/v2/plugin/auth/token/refresh，请求头带 X-Refresh-Token +
// X-Auth-Refresh-Source: plugin（对齐 RefreshHeaders）；响应为业务信封
// {code,msg,data}，新 accessToken / refreshToken / expiresIn / domain 在 data 中
// （也兼容无信封的扁平响应）。成功返回更新后的凭证（不改写磁盘）。
func (c *upstreamClient) refreshToken(cred *UpstreamCred) (*UpstreamCred, error) {
	if strings.TrimSpace(cred.RefreshToken) == "" {
		return nil, fmt.Errorf("凭证缺少 refreshToken，无法刷新")
	}
	req, err := http.NewRequest(http.MethodPost, c.chatBase(cred.Realm)+upstreamRefreshPath, nil)
	if err != nil {
		return nil, err
	}
	c.setChatHeaders(req, cred) // 公共指纹（含 Authorization + 会话头族）
	req.Header.Set("X-Refresh-Token", cred.RefreshToken)
	req.Header.Set("X-Auth-Refresh-Source", "plugin")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := c.http.Do(req.WithContext(ctx))
	if err != nil {
		return nil, fmt.Errorf("刷新请求网络异常: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("读取刷新响应失败: %w", err)
	}
	if resp.StatusCode >= 400 {
		if isRefreshRejectedBody(string(raw)) {
			return nil, &reloginRequiredError{msg: fmt.Sprintf("refresh token 被服务端拒绝（%d）%s", resp.StatusCode, truncateBody(raw))}
		}
		return nil, fmt.Errorf("刷新被上游拒绝（%d）%s", resp.StatusCode, truncateBody(raw))
	}
	// 该端点与同族 /auth/* 一致返回业务信封 {code,msg,data}，token 在 data 里；
	// 同时也兼容无信封的扁平响应（历史实现 / 单测 mock）。
	payload := raw
	if env, ok := unwrapEnvelopeIfPresent(raw); ok {
		if env.Code != 0 {
			if isRefreshRejectedBody(string(raw)) {
				return nil, &reloginRequiredError{msg: fmt.Sprintf("refresh token 被服务端拒绝（code=%d）%s", env.Code, truncateBody(raw))}
			}
			return nil, fmt.Errorf("刷新被上游拒绝（code=%d msg=%s）", env.Code, env.Msg)
		}
		payload = env.Data
	}
	var tok struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		ExpiresIn    int64  `json:"expiresIn"`
		Domain       string `json:"domain"`
	}
	if json.Unmarshal(payload, &tok) != nil || tok.AccessToken == "" {
		return nil, fmt.Errorf("刷新响应缺少 accessToken，需重新授权登录")
	}
	out := *cred
	out.AccessToken = tok.AccessToken
	if tok.RefreshToken != "" {
		out.RefreshToken = tok.RefreshToken
	}
	if tok.Domain != "" {
		out.Domain = tok.Domain
	}
	// expiresIn 防脏值：>10 年视为上游脏数据，保留旧过期时间（对齐参考实现）
	const maxExpiresIn = 10 * 365 * 24 * time.Hour
	if tok.ExpiresIn > 0 && time.Duration(tok.ExpiresIn)*time.Second < maxExpiresIn {
		out.ExpiresAtUnix = time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second).Unix()
	}
	return &out, nil
}

// checkinResult 一次每日签到的真实结果。
type checkinResult struct {
	Already bool   // 今天已签到（上游 code!=0 但文案命中「已签到」）
	Message string // 上游返回的说明
}

// dailyCheckin 调上游每日签到端点。
// POST {billingBase}/v2/billing/meter/daily-checkin，body {}（对齐 DailyCheckin）。
func (c *upstreamClient) dailyCheckin(cred *UpstreamCred) (*checkinResult, error) {
	base := c.billingCN
	if cred.Realm == "global" {
		base = c.billingGlobal
	}
	req, err := http.NewRequest(http.MethodPost, base+upstreamCheckinPath, strings.NewReader("{}"))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+cred.AccessToken)
	req.Header.Set("X-CodeBuddy-Request", "1")
	req.Header.Set("Accept-Language", "zh-CN")
	req.Header.Set("User-Agent", "WorkBuddy/"+defaultClientVer+" WorkBuddy/"+defaultClientVer+" CLI/"+defaultCliVer)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := c.http.Do(req.WithContext(ctx))
	if err != nil {
		return nil, fmt.Errorf("签到请求网络异常: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("读取签到响应失败: %w", err)
	}
	body := string(raw)
	// 重复签到：上游返回 code!=0 +「已签到」文案（实测 10001/14001）→ 如实报已签到
	if resp.StatusCode >= 400 || strings.Contains(body, "已签到") || strings.Contains(strings.ToLower(body), "already") {
		if strings.Contains(body, "已签到") || strings.Contains(strings.ToLower(body), "already") {
			return &checkinResult{Already: true, Message: truncateBody(raw)}, nil
		}
		return nil, fmt.Errorf("签到被上游拒绝（%d）%s", resp.StatusCode, truncateBody(raw))
	}
	var ok struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data any    `json:"data"`
	}
	_ = json.Unmarshal(raw, &ok)
	msg := ok.Msg
	if msg == "" {
		msg = truncateBody(raw)
	}
	return &checkinResult{Message: msg}, nil
}

// SaveUpstreamCred 把刷新后的 token 原子写回凭证文件。
// 兼容嵌套（auth.*）与扁平（顶层字段）两种形态，只改 token 相关字段。
func SaveUpstreamCred(dir, file string, cred *UpstreamCred) error {
	path := filepath.Join(dir, file)
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var doc map[string]any
	if json.Unmarshal(raw, &doc) != nil {
		return fmt.Errorf("凭证 %s 不是合法 JSON，拒绝写回", file)
	}
	if auth, ok := doc["auth"].(map[string]any); ok {
		auth["accessToken"] = cred.AccessToken
		auth["refreshToken"] = cred.RefreshToken
		auth["expiresAt"] = cred.ExpiresAtUnix
		if cred.Domain != "" {
			auth["domain"] = cred.Domain
		}
	} else {
		doc["accessToken"] = cred.AccessToken
		doc["refreshToken"] = cred.RefreshToken
		doc["expiresAt"] = cred.ExpiresAtUnix
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ---- growth 域（猫猫旅行 / 连登奖励）与余额查询（对齐 workbuddy2api travel.go / growth_reward.go） ----

// growth 域路径（实测，走 chatBase + billing 风格请求头；global 同构并存）。
const (
	buddyInfoPath      = "/activity/growth/buddy/info"
	buddyFirstPath     = "/activity/growth/buddy/first"
	buddyAgreementPath = "/activity/growth/buddy/agreement"
	travelStatusPath   = "/activity/growth/buddy/travel/status"
	travelConfigPath   = "/activity/growth/buddy/travel/config"
	travelDepartPath   = "/activity/growth/buddy/travel/depart"
	travelClaimPath    = "/activity/growth/buddy/travel/claim"
	streakPath         = "/activity/growth/streak"
	redeemPath         = "/activity/growth/redeem"
	lotteryChancesPath = "/activity/growth/lottery/chances"
	lotteryDrawPath    = "/activity/growth/lottery/draw"
	trialPath          = "/billing/ide/trial"
	meterResourcePath  = "/v2/billing/meter/get-user-resource"
	v3ConfigPath       = "/v3/config"
)

// buddyTaskIncompleteMarker 领养门槛未达标的业务错误关键词（HTTP 400）。
const buddyTaskIncompleteMarker = "first_buddy task not completed yet"

// upstreamAPIError 上游业务信封错误（HTTP 状态 + 业务 code/msg）。
type upstreamAPIError struct {
	Status int
	Msg    string
}

func (e *upstreamAPIError) Error() string { return e.Msg }

// isBuddyTaskIncomplete 判定「领养门槛未达标」：HTTP 400 + first_buddy 关键词。
// 该错误当日不应重试（避免对上游重试轰炸）。
func isBuddyTaskIncomplete(err error) bool {
	ue, ok := err.(*upstreamAPIError)
	return ok && ue.Status == http.StatusBadRequest &&
		strings.Contains(strings.ToLower(ue.Msg), buddyTaskIncompleteMarker)
}

// apiEnvelope 上游 JSON 信封：code != 0 视为业务失败。
type apiEnvelope struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

// growthJSON 发 growth/billing 域 JSON 请求并解信封；body 为 nil 时不带请求体。
// HTTP 非 2xx 或业务 code != 0 → *upstreamAPIError。
func (c *upstreamClient) growthJSON(cred *UpstreamCred, method, path string, body any) (json.RawMessage, error) {
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, c.chatBase(cred.Realm)+path, rdr)
	if err != nil {
		return nil, err
	}
	c.setBillingHeaders(req, cred)
	return c.doEnvelope(req)
}

// setBillingHeaders billing/growth 域请求头（对齐 BillingHeaders 的桌面端指纹子集）。
// 设备指纹头与 chat 链路同源：X-Machine-ID / X-Session-ID 按 UID 稳定派生
//（deriveAccountStableID），签到 / 余额 / 成长任务等 billing 域请求同样呈现
// 「同一账号固定同一台设备」的形态，避免多账号在上游侧因指纹特征缺失而被关联风控
//（对齐 workbuddy2api-hub wb_fingerprint 的统一派生设计）。
func (c *upstreamClient) setBillingHeaders(req *http.Request, cred *UpstreamCred) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+cred.AccessToken)
	req.Header.Set("X-CodeBuddy-Request", "1")
	lang := "zh-CN"
	if cred.Realm == "global" {
		lang = "en-US"
	}
	req.Header.Set("Accept-Language", lang)
	req.Header.Set("User-Agent", "WorkBuddy/"+defaultClientVer+" WorkBuddy/"+defaultClientVer+" CLI/"+defaultCliVer)
	if cred.UID != "" {
		req.Header.Set("X-Machine-ID", deriveAccountStableID(cred.UID, "machine"))
		req.Header.Set("X-Session-ID", deriveAccountStableID(cred.UID, "session"))
		req.Header.Set("X-User-Id", cred.UID)
	}
	if cred.Realm == "global" {
		req.Header.Set("X-Domain", "www.workbuddy.ai")
	} else if cred.Domain != "" {
		req.Header.Set("X-Domain", cred.Domain)
	}
}

// doEnvelope 统一信封解析（对齐 doJSON）：2xx + code==0 → data；否则 *upstreamAPIError。
func (c *upstreamClient) doEnvelope(req *http.Request) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := c.http.Do(req.WithContext(ctx))
	if err != nil {
		return nil, fmt.Errorf("请求网络异常: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %w", err)
	}
	if resp.StatusCode >= 400 {
		return nil, &upstreamAPIError{Status: resp.StatusCode,
			Msg: fmt.Sprintf("HTTP %d: %s", resp.StatusCode, truncateBody(raw))}
	}
	var env apiEnvelope
	if json.Unmarshal(raw, &env) != nil {
		return nil, fmt.Errorf("响应信封解析失败: %s", truncateBody(raw))
	}
	if env.Code != 0 {
		return nil, &upstreamAPIError{Status: resp.StatusCode,
			Msg: fmt.Sprintf("code=%d msg=%s", env.Code, env.Msg)}
	}
	return env.Data, nil
}

// unwrapEnvelopeIfPresent 仅当 raw 是「含 code 字段的 JSON 对象」（业务信封）时解出信封；
// 供需要同时兼容信封与扁平响应的端点使用（如 token/refresh）。
func unwrapEnvelopeIfPresent(raw []byte) (apiEnvelope, bool) {
	var probe map[string]json.RawMessage
	if json.Unmarshal(raw, &probe) != nil {
		return apiEnvelope{}, false
	}
	if _, ok := probe["code"]; !ok {
		return apiEnvelope{}, false
	}
	var env apiEnvelope
	if json.Unmarshal(raw, &env) != nil {
		return apiEnvelope{}, false
	}
	return env, true
}

// Buddy 账号当前猫档案；nil 表示无猫。
type Buddy struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// TravelState 猫猫旅行状态。
type TravelState struct {
	State             string `json:"state"` // idle / traveling / arrived
	DailyLimitReached bool   `json:"daily_limit_reached"`
	RecordID          int64  `json:"record_id"`
	RewardCredit      int64  `json:"reward_credit"`
}

// BuddyInfo 查询当前猫档案；(nil, nil) 表示无猫。
func (c *upstreamClient) BuddyInfo(cred *UpstreamCred) (*Buddy, error) {
	data, err := c.growthJSON(cred, http.MethodGet, buddyInfoPath, nil)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Buddy json.RawMessage `json:"buddy"`
	}
	if json.Unmarshal(data, &resp) != nil {
		return nil, fmt.Errorf("buddy/info 响应解析失败")
	}
	trimmed := strings.TrimSpace(string(resp.Buddy))
	if trimmed == "" || trimmed == "null" {
		return nil, nil
	}
	var b Buddy
	if json.Unmarshal(resp.Buddy, &b) != nil {
		return nil, fmt.Errorf("buddy 档案解析失败")
	}
	return &b, nil
}

// TravelStatus 查询猫猫旅行状态。
func (c *upstreamClient) TravelStatus(cred *UpstreamCred) (*TravelState, error) {
	data, err := c.growthJSON(cred, http.MethodGet, travelStatusPath, nil)
	if err != nil {
		return nil, err
	}
	var st TravelState
	if json.Unmarshal(data, &st) != nil {
		return nil, fmt.Errorf("travel/status 响应解析失败")
	}
	return &st, nil
}

// TravelConfigLocation 拉取旅行目的地清单（GET travel/config → data.locations[]），
// 返回 (首个目的地 id, 名称)。官方前端派出发请求时 body 必须携带合法的
// location_id——空 body / 伪造 id 会被拒 400 "invalid request"（hub PR #21 实测）。
func (c *upstreamClient) TravelConfigLocation(cred *UpstreamCred) (int, string, error) {
	data, err := c.growthJSON(cred, http.MethodGet, travelConfigPath, nil)
	if err != nil {
		return 0, "", err
	}
	var resp struct {
		Locations []struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		} `json:"locations"`
	}
	if json.Unmarshal(data, &resp) != nil {
		return 0, "", fmt.Errorf("travel/config 响应解析失败")
	}
	for _, loc := range resp.Locations {
		return loc.ID, loc.Name, nil
	}
	return 0, "", fmt.Errorf("travel/config 未返回目的地")
}

// TravelDepart 派出猫旅行；locationID 必须来自 TravelConfigLocation 的目的地清单。
// 成功返回目的地名（上游 data.location.name，解析失败为空串）。
func (c *upstreamClient) TravelDepart(cred *UpstreamCred, locationID int) (string, error) {
	data, err := c.growthJSON(cred, http.MethodPost, travelDepartPath, map[string]any{"location_id": locationID})
	if err != nil {
		return "", err
	}
	var resp struct {
		Location struct {
			Name string `json:"name"`
		} `json:"location"`
	}
	_ = json.Unmarshal(data, &resp) // 地点名字段缺失不影响成功判定
	return resp.Location.Name, nil
}

// TravelClaim 领取到站奖励，返回 reward_credit。
func (c *upstreamClient) TravelClaim(cred *UpstreamCred, recordID int64) (int64, error) {
	data, err := c.growthJSON(cred, http.MethodPost, travelClaimPath, map[string]any{"record_id": recordID})
	if err != nil {
		return 0, err
	}
	var resp struct {
		RewardCredit int64 `json:"reward_credit"`
	}
	_ = json.Unmarshal(data, &resp) // 奖励字段缺失按 0 记，不算失败
	return resp.RewardCredit, nil
}

// BuddyAgreement 同意领养协议（幂等）。
func (c *upstreamClient) BuddyAgreement(cred *UpstreamCred) error {
	_, err := c.growthJSON(cred, http.MethodPost, buddyAgreementPath, map[string]any{"agree": true})
	return err
}

// BuddyFirst 领养第一只猫（+300 分）；门槛未达返回 400（isBuddyTaskIncomplete）。
func (c *upstreamClient) BuddyFirst(cred *UpstreamCred) error {
	_, err := c.growthJSON(cred, http.MethodPost, buddyFirstPath, map[string]any{})
	return err
}

// GrowthTierSpec 连登奖励档位（streak.redemption_status.tiers 元素）。
type GrowthTierSpec struct {
	Tier   string `json:"tier"` // "7d"|"14d"|"28d"
	Days   int    `json:"days"` // 达标连登天数
	Credit int    `json:"credit"`
	Status string `json:"-"` // available / claimed / locked（来自 tier_*_status）
}

// GrowthRedemptionStatus 连登奖励兑换状态。
type GrowthRedemptionStatus struct {
	Tier7dStatus  string           `json:"tier_7d_status"`
	Tier14dStatus string           `json:"tier_14d_status"`
	Tier28dStatus string           `json:"tier_28d_status"`
	Tiers         []GrowthTierSpec `json:"tiers"`
}

// FetchGrowthRedemption 拉取连登奖励档位与状态（把 tier_*_status 回填进 Tiers）。
func (c *upstreamClient) FetchGrowthRedemption(cred *UpstreamCred) (*GrowthRedemptionStatus, error) {
	data, err := c.growthJSON(cred, http.MethodGet, streakPath, nil)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Streak struct {
			RedemptionStatus GrowthRedemptionStatus `json:"redemption_status"`
		} `json:"streak"`
	}
	if json.Unmarshal(data, &resp) != nil {
		return nil, fmt.Errorf("streak 响应解析失败")
	}
	rs := resp.Streak.RedemptionStatus
	statusOf := map[string]string{"7d": rs.Tier7dStatus, "14d": rs.Tier14dStatus, "28d": rs.Tier28dStatus}
	for i := range rs.Tiers {
		rs.Tiers[i].Status = statusOf[rs.Tiers[i].Tier]
	}
	return &rs, nil
}

// RedeemTier 兑换指定档位连登奖励（clientToken 幂等键）。
func (c *upstreamClient) RedeemTier(cred *UpstreamCred, tier, clientToken string) error {
	_, err := c.growthJSON(cred, http.MethodPost, redeemPath,
		map[string]any{"tier": tier, "client_token": clientToken})
	return err
}

// ---- 连登抽奖（对齐 growth_reward.go lottery 家族） ----

// LotteryDrawResult 单次抽奖结果。prize_type: credit=积分 / physical=实物（需人工填地址）。
type LotteryDrawResult struct {
	PrizeCode    string `json:"prize_code"`
	PrizeName    string `json:"prize_name"`
	PrizeType    string `json:"prize_type"`
	CreditAmount int    `json:"credit_amount"`
}

// LotteryChances 查询当前抽奖次数余额（GET chances → data.balance）。0 = 无次数，正常态。
func (c *upstreamClient) LotteryChances(cred *UpstreamCred) (int, error) {
	data, err := c.growthJSON(cred, http.MethodGet, lotteryChancesPath, nil)
	if err != nil {
		return 0, err
	}
	var resp struct {
		Balance int `json:"balance"`
	}
	if json.Unmarshal(data, &resp) != nil {
		return 0, fmt.Errorf("lottery/chances 响应解析失败")
	}
	return resp.Balance, nil
}

// LotteryDraw 抽一次奖（clientToken 每次新键——抽奖对幂等键敏感，复用会被上游吞掉）。
// 无次数（400 insufficient lottery chance balance）与抽奖未开启（400 lottery disabled）
// 属正常态（isLotteryNoChance / isLotteryDisabled），调用方静默跳过。
func (c *upstreamClient) LotteryDraw(cred *UpstreamCred) (*LotteryDrawResult, error) {
	data, err := c.growthJSON(cred, http.MethodPost, lotteryDrawPath,
		map[string]any{"client_token": newClientToken()})
	if err != nil {
		return nil, err
	}
	var res LotteryDrawResult
	if len(data) > 0 {
		_ = json.Unmarshal(data, &res) // 奖品字段缺失不视为失败
	}
	return &res, nil
}

// isLotteryErrWithMarkers 判定 err 是否 upstreamAPIError 且（HTTP 400 + 任一关键词命中）。
func isLotteryErrWithMarkers(err error, markers ...string) bool {
	ue, ok := err.(*upstreamAPIError)
	if !ok || ue.Status != http.StatusBadRequest {
		return false
	}
	lower := strings.ToLower(ue.Msg)
	for _, m := range markers {
		if strings.Contains(lower, strings.ToLower(m)) {
			return true
		}
	}
	return false
}

func isLotteryNoChance(err error) bool {
	return isLotteryErrWithMarkers(err, "insufficient lottery chance balance")
}

func isLotteryDisabled(err error) bool {
	return isLotteryErrWithMarkers(err, "lottery disabled")
}

// ---- global 试用加油包（对齐 trial.go：global 唯一天然的积分增益动作） ----

// ClaimTrial 领取一次性 trial 加油包。仅 global 账号可调（CN 无此端点）。
// 返回 claimed：true=成功新领；false=已领过（幂等码 14051，不算失败）。
func (c *upstreamClient) ClaimTrial(cred *UpstreamCred) (claimed bool, err error) {
	if cred.Realm != "global" {
		return false, fmt.Errorf("claim trial: only global accounts")
	}
	req, err := http.NewRequest(http.MethodPost, c.billingBaseOf(cred.Realm)+trialPath, nil)
	if err != nil {
		return false, err
	}
	c.setBillingHeaders(req, cred)
	if _, err := c.doEnvelope(req); err != nil {
		// 幂等码 14051「已领取过」：HTTP 200 + 业务 code=14051（Msg 格式 code=14051）
		// 或 HTTP ≥400 原始 body（"code":14051）——两种指纹都视为已领过
		if ue, ok := err.(*upstreamAPIError); ok &&
			(strings.Contains(ue.Msg, "code=14051") || strings.Contains(ue.Msg, `"code":14051`)) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// ---- 动态模型目录（对齐 FetchModels 的 /v3/config 主路 + nonChatModel 过滤） ----

// UpstreamModel 上游动态目录返回的单个模型条目（含推理档位元数据）。
type UpstreamModel struct {
	ID              string
	DefaultEffort   string // reasoning.defaultEffort（thinking 注入的档位来源）
	SupportedEffort []string
}

// FetchModels 拉取上游动态模型目录：GET {chatBase}/v3/config（CN/global 双域通用，
// UA 门禁要求三段式 CLI UA——setChatHeaders 即满足）。容忍两种响应形态：
// data.models[]（对象，disabled 剔除，id 优先 name 兜底）或 data 字符串数组（窄表）。
// nes-/completion-/codewise- 前缀、maxOutputTokens≤256、text-to-image 标签的
// 非对话条目按 nonChatModel 口径挡在目录外（选中会报 code=11102）。
func (c *upstreamClient) FetchModels(cred *UpstreamCred) ([]UpstreamModel, error) {
	req, err := http.NewRequest(http.MethodGet, c.chatBase(cred.Realm)+v3ConfigPath, nil)
	if err != nil {
		return nil, err
	}
	c.setChatHeaders(req, cred)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := c.http.Do(req.WithContext(ctx))
	if err != nil {
		return nil, fmt.Errorf("模型目录请求网络异常: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("读取模型目录失败: %w", err)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("模型目录被上游拒绝（%d）%s", resp.StatusCode, truncateBody(raw))
	}
	var env struct {
		Code int             `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	if json.Unmarshal(raw, &env) != nil || env.Code != 0 {
		return nil, fmt.Errorf("模型目录信封异常: %s", truncateBody(raw))
	}
	out := []UpstreamModel{}
	add := func(id string, disabled bool, maxOut int64, tags []string, efforts []string, effort, defEffort string) {
		if disabled || nonChatModel(id, maxOut, tags) {
			return
		}
		um := UpstreamModel{ID: id, DefaultEffort: strings.TrimSpace(defEffort)}
		if len(efforts) > 0 {
			um.SupportedEffort = efforts
		} else if e := strings.TrimSpace(effort); e != "" {
			um.SupportedEffort = []string{e}
		}
		out = append(out, um)
	}
	trimmed := strings.TrimSpace(string(env.Data))
	if strings.HasPrefix(trimmed, "[") {
		var arr []string
		if json.Unmarshal(env.Data, &arr) != nil {
			return nil, fmt.Errorf("模型目录解析失败（窄表形态）")
		}
		for _, id := range arr {
			if id = strings.TrimSpace(id); id != "" {
				add(id, false, 0, nil, nil, "", "")
			}
		}
	} else {
		var obj struct {
			Models []struct {
				ID              string   `json:"id"`
				Name            string   `json:"name"`
				Disabled        bool     `json:"disabled"`
				MaxOutputTokens int64    `json:"maxOutputTokens"`
				Tags            []string `json:"tags"`
				Reasoning       struct {
					SupportedEfforts []string `json:"supportedEfforts"`
					Effort           string   `json:"effort"`
					DefaultEffort    string   `json:"defaultEffort"`
				} `json:"reasoning"`
			} `json:"models"`
		}
		if json.Unmarshal(env.Data, &obj) != nil {
			return nil, fmt.Errorf("模型目录解析失败")
		}
		for _, m := range obj.Models {
			id := m.ID
			if id == "" {
				id = m.Name
			}
			if id == "" {
				continue
			}
			add(id, m.Disabled, m.MaxOutputTokens, m.Tags,
				m.Reasoning.SupportedEfforts, m.Reasoning.Effort, m.Reasoning.DefaultEffort)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("模型目录为空")
	}
	return out, nil
}

// nonChatModel 判定非对话模型（对齐参考实现 harness buddy.ts 三类规则）：
// 前缀 nes-/completion-/codewise-（嵌入/补全/代码专用，选中报 code=11102）、
// maxOutputTokens≤256（tiny 输出）、tags 含 text-to-image（图片生成）。
func nonChatModel(id string, maxOutputTokens int64, tags []string) bool {
	id = strings.ToLower(strings.TrimSpace(id))
	for _, p := range [...]string{"nes-", "completion-", "codewise-"} {
		if strings.HasPrefix(id, p) {
			return true
		}
	}
	if maxOutputTokens > 0 && maxOutputTokens <= 256 {
		return true
	}
	for _, t := range tags {
		if t == "text-to-image" {
			return true
		}
	}
	return false
}

// BalanceDetail 真实余额细分（对齐 UserResourceDetailed / ResourceSummary）。
type BalanceDetail struct {
	Remain   float64 // 剩余总积分（sum 各套餐剩余）
	Used     float64 // 已用
	Size     float64 // 总量
	Packs    int     // 加油包数
	Expiring float64 // 临期（soon 窗口内到期）的剩余部分
	// ExpireDay / ExpireRemain 最早到期的非零剩余套餐的到期日（CST 2006-01-02）
	// 与该日到期的剩余合计（分层选号「先烧快过期积分」的依据；空 = 无到期信息）。
	ExpireDay    string
	ExpireRemain float64
}

// packageEndLayout 上游到期时间格式；时区为官网展示口径 UTC+8 墙钟。
const packageEndLayout = "2006-01-02 15:04:05"

var pkgEndZone = time.FixedZone("CST", 8*60*60)

// FetchBalanceDetail 查询余额细分：聚合套餐剩余/已用/总量/包数，并把 soon 窗口内
// 到期的剩余单列为 Expiring（临期作废预警）。global 账号先打无 /v2 路径（404 回退
// /v2），CN 固定 /v2（对齐 billingMeterPaths）。
func (c *upstreamClient) FetchBalanceDetail(cred *UpstreamCred, soon time.Duration) (*BalanceDetail, error) {
	paths := []string{meterResourcePath}
	if cred.Realm == "global" {
		paths = []string{"/billing/meter/get-user-resource", meterResourcePath}
	}
	now := time.Now()
	body := map[string]any{
		"PageNumber": 1, "PageSize": 100, "ProductCode": "p_tcaca", "Status": []int{0, 3},
		"PackageEndTimeRangeBegin": now.In(pkgEndZone).Format(packageEndLayout),
		"PackageEndTimeRangeEnd":   now.In(pkgEndZone).Add(365 * 24 * time.Hour).Format(packageEndLayout),
	}
	var lastErr error
	for i, p := range paths {
		req, err := http.NewRequest(http.MethodPost, c.billingBaseOf(cred.Realm)+p, bytes.NewReader(mustJSON(body)))
		if err != nil {
			return nil, err
		}
		c.setBillingHeaders(req, cred)
		data, err := c.doEnvelope(req)
		if err != nil {
			lastErr = err
			// 仅对 404 尝试下一路径（对齐 global fallback 语义）
			if ue, ok := err.(*upstreamAPIError); ok && ue.Status == http.StatusNotFound && i < len(paths)-1 {
				continue
			}
			return nil, err
		}
		var resp struct {
			Response struct {
				Data struct {
					Accounts []struct {
						CapacityRemain int64  `json:"CapacityRemain"`
						CapacityUsed   int64  `json:"CapacityUsed"`
						CapacitySize   int64  `json:"CapacitySize"`
						CycleEndTime   string `json:"CycleEndTime"` // 空 = 无到期
					} `json:"Accounts"`
				} `json:"Data"`
			} `json:"Response"`
		}
		if json.Unmarshal(data, &resp) != nil {
			return nil, fmt.Errorf("余额响应解析失败")
		}
		d := &BalanceDetail{}
		deadline := now.Add(soon)
		for _, pack := range resp.Response.Data.Accounts {
			r := pack.CapacityRemain
			if r < 0 {
				r = 0
			}
			d.Remain += float64(r)
			d.Used += float64(pack.CapacityUsed)
			d.Size += float64(pack.CapacitySize)
			d.Packs++
			if r == 0 || pack.CycleEndTime == "" {
				continue
			}
			end, perr := time.ParseInLocation(packageEndLayout, pack.CycleEndTime, pkgEndZone)
			if perr != nil {
				continue
			}
			// 分桶：仅 soon>0 且确实在窗口内 → Expiring
			if soon > 0 && !end.After(deadline) {
				d.Expiring += float64(r)
			}
			// 到期分层：追踪最早到期的非零剩余套餐（按日比较，同日到期合并剩余）
			day := end.In(pkgEndZone).Format("2006-01-02")
			if d.ExpireDay == "" || day < d.ExpireDay {
				d.ExpireDay, d.ExpireRemain = day, float64(r)
			} else if day == d.ExpireDay {
				d.ExpireRemain += float64(r)
			}
		}
		return d, nil
	}
	return nil, lastErr
}

// FetchBalance 查询真实积分余额（余额总数的便捷入口）。
func (c *upstreamClient) FetchBalance(cred *UpstreamCred) (float64, error) {
	d, err := c.FetchBalanceDetail(cred, 0)
	if err != nil {
		return 0, err
	}
	return d.Remain, nil
}

func (c *upstreamClient) billingBaseOf(realm string) string {
	if realm == "global" {
		return c.billingGlobal
	}
	return c.billingCN
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// newClientToken 生成幂等 client_token（8-4-4-4-12 形态）。
func newClientToken() string {
	h := newMessageID()
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// ---- 小工具 ----

// deriveAccountStableID 按 uid+用途稳定派生 36 hex 设备/会话标识（对齐 headers.go）。
func deriveAccountStableID(uid, purpose string) string {
	sum := sha256.Sum256([]byte("wb2a:" + purpose + ":" + uid))
	return hex.EncodeToString(sum[:18])
}

// newMessageID 生成 32 位 hex 消息 ID（对齐官方客户端 UUID 去横线形态）。
func newMessageID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return strings.ReplaceAll(NewID("")+NewID(""), "-", "")[:32]
	}
	return hex.EncodeToString(b)
}

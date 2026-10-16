package ferry

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Backend 与隔离 OpenCode 的会话层。
//
// 两条边界在这里落地：
//   - 一条事件流服务所有在途请求（OpenCode 的上游重试只在事件流上露面）；
//   - 审批监视器只负责「拒绝或转交」——查询失败绝不等于批准，本机动作会一直停在等待状态。

type healthInfo struct {
	Healthy bool   `json:"healthy"`
	Version string `json:"version"`
}

type toolPart struct {
	Tool  string
	Input map[string]any
}

type permissionInfo struct {
	ID         string `json:"id"`
	SessionID  string `json:"sessionID"`
	Permission string `json:"permission"`
	Tool       *struct {
		CallID  string `json:"callID"`
		Message string `json:"message"`
	} `json:"tool"`
	Metadata map[string]any `json:"metadata"`
}

// requestMeta 一次转发请求的运行态
type requestMeta struct {
	Model          string
	SessionID      string
	Probe          bool
	Steps          int
	NativeAttempts int
	Permissions    []any
	HandoffCheck   map[string]any
	HandoffMiss    *handoffMiss
	Repaired       map[string]*repairRecord

	// Calls / Handoff / Steps 会随结果回传给上层记账
	Calls   int
	Handoff string

	activity func(map[string]any)

	stageMu    sync.Mutex
	stageValue string
}

// Backend OpenCode 客户端
type Backend struct {
	base     string
	password string
	client   *http.Client
	logf     func(string, ...any)

	mu               sync.Mutex
	active           map[string]*requestMeta
	events           context.CancelFunc
	eventsRunning    bool
	toolParts        map[string]toolPart
	toolPartOrder    []string
	pendingApprovals map[string]permissionInfo
	usageBySession   map[string]*Tokens

	// translator 挑翻译兜底用的模型
	translator func(failedModel string) string
}

func newBackend(base, password string, logf func(string, ...any)) *Backend {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Backend{
		base:     base,
		password: password,
		client:   &http.Client{},
		logf:     logf,
		active:   map[string]*requestMeta{},
		toolParts: map[string]toolPart{},
		pendingApprovals: map[string]permissionInfo{},
		usageBySession:   map[string]*Tokens{},
	}
}

func (b *Backend) headers() map[string]string {
	auth := base64.StdEncoding.EncodeToString([]byte(opencodeProviderID + ":" + b.password))
	return map[string]string{"Content-Type": "application/json", "Authorization": "Basic " + auth}
}

// request 发一次 HTTP 请求；timeout 为 0 表示不设超时（推理那条路自己持有取消信号）。
func (b *Backend) request(ctx context.Context, route, method string, body any, timeout time.Duration) ([]byte, *BridgeError) {
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return nil, newBridgeError(http.StatusBadRequest, "invalid_request", "请求体无法序列化")
		}
		reader = bytes.NewReader(payload)
	}
	reqCtx := ctx
	var cancel context.CancelFunc
	if timeout > 0 {
		reqCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(reqCtx, method, b.base+route, reader)
	if err != nil {
		return nil, newBridgeError(http.StatusBadGateway, "upstream_error", "请求构造失败: %v", err)
	}
	for k, v := range b.headers() {
		req.Header.Set(k, v)
	}
	res, err := b.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, &BridgeError{Message: "请求已取消", Status: 499, Code: "canceled"}
		}
		return nil, newBridgeError(http.StatusBadGateway, "upstream_error", "连接隔离 OpenCode 失败: %v", err)
	}
	defer res.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(res.Body, 64<<20))
	if err != nil {
		return nil, newBridgeError(http.StatusBadGateway, "upstream_error", "读取隔离 OpenCode 响应失败: %v", err)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		status := res.StatusCode
		if status >= 500 {
			status = http.StatusBadGateway
		}
		return nil, newBridgeError(status, "upstream_error", "OpenCode HTTP %d: %s",
			res.StatusCode, truncateForLog(string(payload)))
	}
	return payload, nil
}

func (b *Backend) health(ctx context.Context) (healthInfo, error) {
	payload, err := b.request(ctx, "/global/health", http.MethodGet, nil, time.Second)
	if err != nil {
		return healthInfo{}, err
	}
	var out healthInfo
	if err := json.Unmarshal(payload, &out); err != nil {
		return healthInfo{}, err
	}
	return out, nil
}

// ---- 事件流 ----

// watchEvents 一条事件流服务所有在途请求
func (b *Backend) watchEvents() {
	b.mu.Lock()
	if b.eventsRunning {
		b.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	b.events = cancel
	b.eventsRunning = true
	b.mu.Unlock()

	go func() {
		for ctx.Err() == nil {
			if err := b.streamEvents(ctx); err != nil && ctx.Err() == nil {
				b.logf("事件流中断: %v", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
		}
	}()
}

// StopEvents 停止事件流
func (b *Backend) StopEvents() {
	b.mu.Lock()
	cancel, running := b.events, b.eventsRunning
	b.events, b.eventsRunning = nil, false
	b.mu.Unlock()
	if running && cancel != nil {
		cancel()
	}
}

func (b *Backend) streamEvents(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.base+"/event", nil)
	if err != nil {
		return err
	}
	for k, v := range b.headers() {
		req.Header.Set(k, v)
	}
	req.Header.Set("Accept", "text/event-stream")
	res, err := b.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return errors.New("事件流 HTTP " + res.Status)
	}
	reader := newLineReader(res.Body)
	for {
		line, err := reader.ReadLine(ctx)
		if err != nil {
			return err
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		var wrapper map[string]any
		if json.Unmarshal([]byte(strings.TrimSpace(line[5:])), &wrapper) != nil {
			continue
		}
		b.handleEvent(wrapper)
	}
}

// lineReader 容忍超长行的 SSE 读取器
type lineReader struct {
	reader *bufio.Reader
}

func newLineReader(r io.Reader) *lineReader {
	return &lineReader{reader: bufio.NewReaderSize(r, 64*1024)}
}

func (l *lineReader) ReadLine(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	line, err := l.reader.ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func (b *Backend) handleEvent(wrapper map[string]any) {
	event, _ := wrapper["payload"].(map[string]any)
	if event == nil {
		event = wrapper
	}
	eventType, _ := event["type"].(string)
	properties, _ := event["properties"].(map[string]any)

	switch eventType {
	case "permission.asked", "permission.updated":
		if properties != nil {
			if info, ok := decodePermission(properties); ok {
				b.mu.Lock()
				b.pendingApprovals[info.ID] = info
				b.mu.Unlock()
			}
		}
	case "permission.replied":
		if properties != nil {
			if id, ok := properties["requestID"].(string); ok {
				b.mu.Lock()
				delete(b.pendingApprovals, id)
				b.mu.Unlock()
			}
		}
	case "message.part.updated":
		// 工具 part 带着「被审批闸门拦下」的那个调用的名字与参数；
		// 它比审批先到，而 HTTP 方式列消息是查不到的。
		part, _ := properties["part"].(map[string]any)
		if part != nil && part["type"] == "tool" {
			if callID, ok := part["callID"].(string); ok && callID != "" {
				tool, _ := part["tool"].(string)
				input := map[string]any{}
				if state, ok := part["state"].(map[string]any); ok {
					if raw, ok := state["input"].(map[string]any); ok {
						input = raw
					}
				}
				b.mu.Lock()
				if _, exists := b.toolParts[callID]; !exists {
					b.toolPartOrder = append(b.toolPartOrder, callID)
				}
				b.toolParts[callID] = toolPart{Tool: tool, Input: input}
				for len(b.toolPartOrder) > 50 {
					delete(b.toolParts, b.toolPartOrder[0])
					b.toolPartOrder = b.toolPartOrder[1:]
				}
				b.mu.Unlock()
			}
		}
	case "message.updated":
		info, _ := properties["info"].(map[string]any)
		if info != nil && info["role"] == "assistant" {
			if sessionID, ok := info["sessionID"].(string); ok {
				b.mu.Lock()
				_, tracked := b.usageBySession[sessionID]
				b.mu.Unlock()
				if tracked {
					if tokens := decodeTokens(info["tokens"]); tokens != nil {
						b.mu.Lock()
						b.usageBySession[sessionID] = tokens
						b.mu.Unlock()
					}
				}
			}
		}
	}

	sessionID, _ := properties["sessionID"].(string)
	if sessionID == "" {
		return
	}
	b.mu.Lock()
	meta := b.active[sessionID]
	b.mu.Unlock()
	if meta == nil || meta.activity == nil {
		return
	}
	progress := map[string]any{"sessionID": sessionID, "model": meta.Model, "type": eventType}
	if status, ok := properties["status"].(map[string]any); ok && eventType == "session.status" {
		statusType, _ := status["type"].(string)
		if statusType == "retry" || (statusType == "busy" && (meta.stage() == "" || meta.stage() == "retry")) {
			if statusType == "busy" {
				progress["status"] = "waiting"
			} else {
				progress["status"] = "retry"
			}
		}
		if statusType == "retry" {
			progress["attempt"] = status["attempt"]
			progress["message"] = status["message"]
		}
	}
	if eventType == "session.error" {
		progress["error"] = errorMessage(properties["error"])
	}
	if part, ok := properties["part"].(map[string]any); ok {
		partType, _ := part["type"].(string)
		if (partType == "text" || partType == "reasoning") && part["text"] != nil {
			progress["content"] = true
			if partType == "reasoning" {
				progress["status"] = "reasoning"
			} else {
				progress["status"] = "receiving"
			}
		}
	}
	if eventType == "message.part.delta" && properties["delta"] != nil {
		progress["content"] = true
		progress["status"] = "receiving"
	}
	if eventType == "permission.asked" || eventType == "permission.updated" {
		progress["status"] = "permission"
	}
	if status, ok := progress["status"].(string); ok {
		meta.setStage(status)
	}
	meta.activity(progress)
}

// stage 取当前阶段（供事件流判定「是否仍停在 retry」）
func (m *requestMeta) stage() string {
	m.stageMu.Lock()
	defer m.stageMu.Unlock()
	return m.stageValue
}

func (m *requestMeta) setStage(value string) {
	m.stageMu.Lock()
	defer m.stageMu.Unlock()
	m.stageValue = value
}

func errorMessage(raw any) string {
	if object, ok := raw.(map[string]any); ok {
		if data, ok := object["data"].(map[string]any); ok {
			if message, ok := data["message"].(string); ok && message != "" {
				return message
			}
		}
		if name, ok := object["name"].(string); ok && name != "" {
			return name
		}
	}
	return "上游错误"
}

func decodePermission(properties map[string]any) (permissionInfo, bool) {
	raw, err := json.Marshal(properties)
	if err != nil {
		return permissionInfo{}, false
	}
	var info permissionInfo
	if err := json.Unmarshal(raw, &info); err != nil || info.ID == "" {
		return permissionInfo{}, false
	}
	return info, true
}

func decodeTokens(raw any) *Tokens {
	if raw == nil {
		return nil
	}
	payload, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var tokens Tokens
	if err := json.Unmarshal(payload, &tokens); err != nil {
		return nil
	}
	return &tokens
}

// ---- 审批围栏 ----

func (b *Backend) reject(ctx context.Context, permission permissionInfo, message string) {
	b.request(ctx, "/permission/"+urlPathEscape(permission.ID)+"/reply", http.MethodPost,
		map[string]any{"reply": "reject", "message": message}, 5*time.Second)
	b.mu.Lock()
	delete(b.pendingApprovals, permission.ID)
	b.mu.Unlock()
}

// blockedAction 被拦下的调用名来自事件流；part 还在等审批时参数可能是空的
func (b *Backend) blockedAction(ctx context.Context, callID string) (toolPart, string) {
	for attempt := 0; attempt < 4; attempt++ {
		b.mu.Lock()
		part, ok := b.toolParts[callID]
		b.mu.Unlock()
		if ok && part.Tool != "" {
			return part, ""
		}
		select {
		case <-ctx.Done():
			return toolPart{}, "上下文已取消"
		case <-time.After(120 * time.Millisecond):
		}
	}
	return toolPart{}, "没有任何事件带上这个调用 ID"
}

func (b *Backend) cachedPermissions(sessionID string) []permissionInfo {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]permissionInfo, 0, len(b.pendingApprovals))
	for _, info := range b.pendingApprovals {
		if info.SessionID == sessionID {
			out = append(out, info)
		}
	}
	return out
}

func (b *Backend) pendingPermissions(ctx context.Context, sessionID string) []permissionInfo {
	payload, err := b.request(ctx, "/permission", http.MethodGet, nil, 5*time.Second)
	if err != nil {
		if ctx.Err() == nil {
			b.logf("审批监视查询失败: %v", err)
		}
		return b.cachedPermissions(sessionID)
	}
	var pending []permissionInfo
	if json.Unmarshal(payload, &pending) != nil {
		b.logf("审批监视查询失败: 响应不是数组")
		return b.cachedPermissions(sessionID)
	}
	out := make([]permissionInfo, 0, len(pending))
	for _, info := range pending {
		if info.SessionID == sessionID {
			out = append(out, info)
		}
	}
	return out
}

// handoffUsage 回读本次会话的用量。
// 先 abort 让被打断的助手消息收尾，否则会话清理会把它一起删掉、用量就查不到了。
func (b *Backend) handoffUsage(ctx context.Context, sessionID string) *Tokens {
	payload, err := b.request(ctx, "/session/"+urlPathEscape(sessionID)+"/message?limit=1", http.MethodGet, nil, 5*time.Second)
	if err == nil {
		var messages []map[string]any
		if json.Unmarshal(payload, &messages) == nil {
			for i := len(messages) - 1; i >= 0; i-- {
				info, _ := messages[i]["info"].(map[string]any)
				if info == nil || info["role"] != "assistant" {
					continue
				}
				if tokens := decodeTokens(info["tokens"]); tokens != nil {
					return tokens
				}
			}
		}
	} else if ctx.Err() == nil {
		b.logf("用量回读失败: %v", err)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.usageBySession[sessionID]
}

// handlePermission 一条本机审批的两种归宿：拒绝，或转交给外部客户端。
func (b *Backend) handlePermission(
	ctx context.Context,
	permission permissionInfo,
	request *Request,
	meta *requestMeta,
	rejected map[string]bool,
) *handoffResult {
	callID := ""
	if permission.Tool != nil {
		callID = permission.Tool.CallID
	}
	if callID != "" && rejected[callID] {
		return nil
	}
	if callID != "" {
		rejected[callID] = true
	}
	meta.NativeAttempts++
	// 审批载荷逐字保留：OpenCode 的字段名与 SDK 类型并不一致，挑字段复制会静悄悄丢掉真正有用的那几个
	if len(meta.Permissions) < 5 {
		meta.Permissions = append(meta.Permissions, shrinkPermission(permissionAsMap(permission), 400))
	}
	var action toolPart
	var failure string
	if callID != "" {
		action, failure = b.blockedAction(ctx, callID)
	}
	native := action.Tool
	if native == "" && permission.Metadata != nil {
		if command, ok := permission.Metadata["command"].(string); ok && command != "" {
			native = "bash"
		}
	}
	var handoff *handoffResult
	if native != "" {
		handoff = buildHandoff(native, handoffInput(action.Input, permission.Metadata), allowedTools(request))
	}
	if handoff == nil {
		detail := failure
		if detail == "" {
			detail = "参数不足以填满外部 schema"
		}
		meta.HandoffCheck = map[string]any{"native": native, "offeredTools": len(request.Tools), "detail": detail}
		if native != "" {
			// 动作确实存在，只是表达不出来：留给翻译兜底用
			meta.HandoffMiss = &handoffMiss{Native: native, Input: handoffInput(action.Input, permission.Metadata), OfferedTools: len(request.Tools)}
		}
	}
	if handoff != nil {
		b.progress(meta, "handoff")
		b.reject(ctx, permission, "This native action is executed by the external client instead.")
		return handoff
	}
	var reason string
	switch {
	case callID == "":
		reason = "the approval request carries no call ID, so it cannot be matched to the external tool list"
	case action.Tool != "":
		reason = "its arguments cannot be mapped onto an external tool schema supplied in this request"
	default:
		reason = "the call could not be read back from the session"
	}
	label := native
	if label == "" {
		if filepath, ok := permission.Metadata["filepath"].(string); ok && filepath != "" {
			label = "a file operation on " + filepath
		}
	}
	if label == "" {
		label = permission.Permission
	}
	if label == "" {
		label = "native tool"
	}
	// 审批可能已经不在了（会话被中止、重复回复）：绝不能让这次失败的回复把整轮请求带崩
	b.reject(ctx, permission, rejectFeedback(label, reason))
	return nil
}

func permissionAsMap(permission permissionInfo) map[string]any {
	payload, _ := json.Marshal(permission)
	var out map[string]any
	_ = json.Unmarshal(payload, &out)
	return out
}

func (b *Backend) progress(meta *requestMeta, status string, extra ...map[string]any) {
	meta.setStage(status)
	if meta.activity == nil {
		return
	}
	progress := map[string]any{"sessionID": meta.SessionID, "model": meta.Model, "type": "bridge.phase", "status": status}
	for _, item := range extra {
		for k, v := range item {
			progress[k] = v
		}
	}
	meta.activity(progress)
}

func urlPathEscape(value string) string {
	return strings.NewReplacer("/", "%2F", " ", "%20").Replace(value)
}

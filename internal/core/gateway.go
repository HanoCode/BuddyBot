package core

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// CreditsPerKToken 本地网关计费口径：每 1000 token 记 1 积分。
//
// 这是本网关**自身**的配额记账规则（用于密钥的 creditQuota 生效），与上游账号
// 余额无关——账号余额只由真实的余额查询结果更新，绝不在此推算。
const CreditsPerKToken = 1.0

// Gateway 真实 HTTP 网关：监听 config.Listen，提供 /healthz、/v1/models、
// /v1/chat/completions。
//
// 与「演示网关」的区别：鉴权走密钥表的 SHA-256 摘要，配额与白名单真实生效，
// 账号路由基于 auth_dir 下的真实凭证，所有请求指标（token、总延迟、会话、错误）
// 原样落盘，不做任何估算或填充。
type Gateway struct {
	mu        sync.Mutex
	ln        net.Listener
	srv       *http.Server
	running   bool
	startedAt time.Time
	svc       *Service
	rr        uint64 // 账号轮询游标
	up        *upstreamClient
	// exploreLast 成本探索记录：model → 上次放行未观测层的时间（条件探索防抖）
	exploreLast map[string]time.Time
}

// NewGateway 创建网关
func NewGateway(svc *Service) *Gateway {
	return &Gateway{svc: svc, up: newUpstreamClient(), exploreLast: map[string]time.Time{}}
}

// Start 启动监听（addr 形如 ":7863"）
func (g *Gateway) Start(addr string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.running {
		return nil
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("监听 %s 失败: %w", addr, err)
	}
	g.ln = ln
	g.startedAt = time.Now()
	g.running = true

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", g.handleHealth)
	mux.HandleFunc("/v1/models", g.handleModels)
	mux.HandleFunc("/v1/chat/completions", g.handleChat)
	mux.HandleFunc("/v1/embeddings", g.handleEmbeddings)
	mux.HandleFunc("/v1/images/generations", g.handleImages)
	g.srv = &http.Server{Handler: mux}
	srv := g.srv // goroutine 内读局部变量，避免与后续 Start/Stop 的字段访问构成数据竞争

	go func() {
		_ = srv.Serve(ln)
	}()
	EmitEvent(EventGatewayLog, map[string]any{"level": "info", "message": "网关已启动，监听 " + addr})
	return nil
}

// Stop 停止监听
func (g *Gateway) Stop() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.running {
		return nil
	}
	g.running = false
	// 显式关闭 listener：Serve 尚未注册时 http.Server.Close 不会释放端口
	if g.ln != nil {
		_ = g.ln.Close()
		g.ln = nil
	}
	if g.srv != nil {
		_ = g.srv.Close()
	}
	EmitEvent(EventGatewayLog, map[string]any{"level": "warn", "message": "网关已停止"})
	return nil
}

// IsRunning 是否运行中
func (g *Gateway) IsRunning() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.running
}

// StartedAt 启动时间
func (g *Gateway) StartedAt() time.Time {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.startedAt
}

// Addr 当前监听地址
func (g *Gateway) Addr() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.ln == nil {
		return ""
	}
	return g.ln.Addr().String()
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeAPIError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]any{"message": msg, "type": code, "code": code},
	})
}

func (g *Gateway) handleHealth(w http.ResponseWriter, _ *http.Request) {
	g.mu.Lock()
	running, startedAt := g.running, g.startedAt
	g.mu.Unlock()
	uptime := ""
	if running {
		uptime = time.Since(startedAt).Round(time.Second).String()
	}
	accounts := g.svc.Accounts()
	online := 0
	for _, a := range accounts {
		if a.Status == "online" {
			online++
		}
	}
	status := "stopped"
	if running {
		status = "ok"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":   status,
		"running":  running,
		"uptime":   uptime,
		"accounts": map[string]int{"total": len(accounts), "online": online},
	})
}

func (g *Gateway) handleModels(w http.ResponseWriter, r *http.Request) {
	auth, _, ok := g.authenticate(w, r)
	if !ok {
		return
	}
	models := ListModels(g.svc.Store())
	// 模型中心：客户端可见名 = 别名（有映射时），白名单按客户端可见名校验
	reverse := map[string]string{}
	for client, real := range g.svc.GetConfig().Models.Aliases {
		if strings.TrimSpace(client) != "" && strings.TrimSpace(real) != "" {
			reverse[real] = client
		}
	}
	out := make([]map[string]any, 0, len(models))
	for _, m := range models {
		clientName := m.ID
		if a, ok := reverse[m.ID]; ok {
			clientName = a
		}
		if !ModelAllowed(auth.models, clientName) {
			continue
		}
		item := map[string]any{
			"id": clientName, "object": "model", "owned_by": "workbuddy",
			"context_length": m.ContextLength,
		}
		if m.MaxOutputTokens > 0 {
			item["max_output_tokens"] = m.MaxOutputTokens
		}
		out = append(out, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": out})
}

// ---- 鉴权 ----

type authInfo struct {
	keyID   string
	keyName string
	models  []string
	key     *ApiKey // nil = 配置里的根密钥（无配额）
}

// clientIP 取真实来源 IP
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// ipAllowed 校验 IP 白名单（CIDR 或精确匹配）
func ipAllowed(list []string, ip string) bool {
	parsed := net.ParseIP(ip)
	for _, entry := range list {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if strings.Contains(entry, "/") {
			if _, cidr, err := net.ParseCIDR(entry); err == nil && parsed != nil && cidr.Contains(parsed) {
				return true
			}
			continue
		}
		if entry == ip {
			return true
		}
	}
	return false
}

// authenticate 完成 IP 白名单 → 密钥解析 → 密钥状态 / 配额校验。
// 返回 (鉴权结果, 拒绝时的 HTTP 状态码, 是否通过)；未通过时已写好响应。
func (g *Gateway) authenticate(w http.ResponseWriter, r *http.Request) (authInfo, int, bool) {
	cfg := g.svc.GetConfig()
	ip := clientIP(r)

	// 0) 网关级 IP 黑名单（CIDR，优先于一切白名单——先拒后放）
	if len(cfg.Security.IPBlacklist) > 0 && ipAllowed(cfg.Security.IPBlacklist, ip) {
		writeAPIError(w, http.StatusForbidden, "ip_blocked", "来源 IP "+ip+" 已被列入黑名单")
		return authInfo{}, http.StatusForbidden, false
	}

	// 1) 网关级 IP 白名单（对全体客户端生效）
	if cfg.Security.RequireIPAllowlist && len(cfg.Security.IPWhitelist) > 0 {
		if !ipAllowed(cfg.Security.IPWhitelist, ip) {
			writeAPIError(w, http.StatusForbidden, "ip_not_allowed", "来源 IP "+ip+" 不在网关白名单内")
			return authInfo{}, http.StatusForbidden, false
		}
	}

	// 2) Bearer token
	token := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if token == "" {
		writeAPIError(w, http.StatusUnauthorized, "missing_api_key", "缺少 Authorization: Bearer <key>")
		return authInfo{}, http.StatusUnauthorized, false
	}

	// 3) 根密钥（配置项，无配额限制）
	if cfg.APIKey != "" && token == cfg.APIKey {
		return authInfo{keyID: "root", keyName: "根密钥", models: []string{"*"}}, http.StatusOK, true
	}

	// 4) 密钥表：SHA-256 摘要查找
	key, found := g.svc.Store().FindKeyByHash(HashKey(token))
	if !found {
		writeAPIError(w, http.StatusUnauthorized, "invalid_api_key", "API Key 无效")
		return authInfo{}, http.StatusUnauthorized, false
	}
	switch {
	case key.Disabled:
		writeAPIError(w, http.StatusForbidden, "key_disabled", "密钥「"+key.Name+"」已禁用")
		return authInfo{}, http.StatusForbidden, false
	case key.Expires != "" && key.Expires < time.Now().Format("2006-01-02"):
		writeAPIError(w, http.StatusUnauthorized, "key_expired", "密钥「"+key.Name+"」已于 "+key.Expires+" 过期")
		return authInfo{}, http.StatusUnauthorized, false
	case key.TokenQuota > 0 && key.TokenUsed >= key.TokenQuota:
		writeAPIError(w, http.StatusTooManyRequests, "token_quota_exceeded",
			fmt.Sprintf("密钥「%s」Token 配额已用尽（%d / %d）", key.Name, key.TokenUsed, key.TokenQuota))
		return authInfo{}, http.StatusTooManyRequests, false
	case key.CreditQuota > 0 && key.CreditUsed >= key.CreditQuota:
		writeAPIError(w, http.StatusTooManyRequests, "credit_quota_exceeded",
			fmt.Sprintf("密钥「%s」积分配额已用尽（%d / %d）", key.Name, key.CreditUsed, key.CreditQuota))
		return authInfo{}, http.StatusTooManyRequests, false
	case len(key.IPWhitelist) > 0 && !ipAllowed(key.IPWhitelist, ip):
		writeAPIError(w, http.StatusForbidden, "ip_not_allowed", "来源 IP "+ip+" 不在密钥「"+key.Name+"」的白名单内")
		return authInfo{}, http.StatusForbidden, false
	}
	return authInfo{keyID: key.ID, keyName: key.Name, models: key.Models, key: &key}, http.StatusOK, true
}

// ---- Chat Completions ----

type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []ChatMessage `json:"messages"`
	Stream      bool          `json:"stream"`
	Temperature float64       `json:"temperature"`
	MaxTokens   int           `json:"max_tokens"`
	System      string        `json:"system"`
	SessionID   string        `json:"session_id"`
}

// ChatMessage 消息
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func (g *Gateway) handleChat(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	ip := clientIP(r)

	auth, denyStatus, ok := g.authenticate(w, r)
	if !ok {
		// 鉴权失败也记账：前端需要看到真实的 401 / 403 / 429 分布
		g.recordLog(auth, "", denyStatus, 0, 0, 0, start, ip, "", false, "鉴权未通过")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "请求体读取失败")
		return
	}
	var req chatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		g.recordLog(auth, "", http.StatusBadRequest, 0, 0, 0, start, ip, "", false, "请求体不是合法 JSON")
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "请求体不是合法 JSON")
		return
	}
	if req.Model == "" {
		req.Model = "glm-5.2"
	}
	cfg := g.svc.GetConfig()
	// 模型中心：客户端可见名 → 上游真实模型（密钥白名单按客户端可见名校验）
	realModel := req.Model
	if m, ok := cfg.Models.Aliases[req.Model]; ok && strings.TrimSpace(m) != "" {
		realModel = strings.TrimSpace(m)
	}
	if !ModelAllowed(auth.models, req.Model) {
		msg := fmt.Sprintf("密钥「%s」无权访问模型 %s", auth.keyName, req.Model)
		g.recordLog(auth, req.Model, http.StatusForbidden, 0, 0, 0, start, ip, req.SessionID, req.Stream, msg)
		writeAPIError(w, http.StatusForbidden, "model_not_allowed", msg)
		return
	}

	// 账号路由（轮转重试）：账号级故障（限流/5xx/401/网络/余额）自动换下一个账号；
	// 请求级故障（参数错误/上下文超限）换号也无用，直接返回。
	// 401 会话失效时先尝试用 refreshToken 换新 token 并重试同账号一次（对齐参考实现）。
	// 出站 body：真实模型名 + 提示词三模式 + WAF 指纹净化；缓存键按账号在发请求前注入。
	upReq := req
	upReq.Model = realModel
	baseBody := g.applyPromptPolicy(upstreamChatRequest(upReq), cfg.Prompt)
	var account Account
	var upRes chatResult
	haveRes := false
	degraded := false              // 指纹拦截降级重试只做一次
	exclude := map[string]bool{}   // uid → 本次请求不再尝试
	refreshed := map[string]bool{} // uid → 已做过一次 401 刷新重试
	var lastStatus int
	var lastMsg string

	for {
		a, ok := g.pickAccountSticky(req.SessionID, realModel, exclude)
		if !ok {
			break
		}
		c, cerr := LoadUpstreamCred(g.svc.AuthDir(), a.Credential)
		if cerr != nil {
			exclude[a.UID] = true
			lastStatus, lastMsg = http.StatusBadGateway, "账号 "+a.UID+" 凭证读取失败: "+cerr.Error()
			continue
		}

		g.svc.tracker.enter(a.UID)
		// prompt_cache_key 按账号隔离注入（跨账号复用键会命中错账号的前缀缓存），
		// 会话源 = 客户端 session_id → 同会话恒定同键，配合粘性调度复用上游前缀缓存
		res, uerr := g.up.chat(r.Context(), a.Realm, c, injectPromptCacheKey(baseBody, a.UID, req.SessionID))
		if uerr != nil {
			g.svc.tracker.leave(a.UID)
			note := g.applyUpstreamFailure(a, upErrNetwork)
			exclude[a.UID] = true
			lastStatus = http.StatusBadGateway
			lastMsg = "账号 " + a.UID + " 上游网络异常: " + uerr.Error()
			if note != "" {
				lastMsg += "；" + note
			}
			continue
		}
		if res.status >= 400 {
			g.svc.tracker.leave(a.UID)
			// 指纹拦截（400+11128）：用中性降级提示词重建 body，重试同账号一次
			if !degraded && IsFingerprintBlock(res.status, string(res.errBody)) {
				_ = res.errBody
				degraded = true
				baseBody = g.applyPromptPolicy(upstreamChatRequest(upReq),
					PromptConfig{Mode: "custom", Text: DegradedPromptText})
				lastMsg = "命中上游指纹拦截，已降级提示词重试"
				continue // 不排除该账号
			}
			if res.body != nil {
				_ = res.body.Close()
			}
			kind := classifyUpstreamError(res.status, string(res.errBody))
			note := g.applyUpstreamFailure(a, kind)
			lastStatus = res.status
			lastMsg = fmt.Sprintf("账号 %s 上游返回 %d（%s）%s", a.UID, res.status, kind, truncateBody(res.errBody))
			if note != "" {
				lastMsg += "；" + note
			}
			// 401：先用 refreshToken 换新 token，重试同账号一次
			if kind == upErrSessionDead && !refreshed[a.UID] && strings.TrimSpace(c.RefreshToken) != "" {
				if nc, rerr := g.up.refreshToken(c); rerr == nil {
					if serr := SaveUpstreamCred(g.svc.AuthDir(), a.Credential, nc); serr == nil {
						refreshed[a.UID] = true
						// 清掉刚落的 401 冷却，让账号可被重新选中参与重试；
						// 若重试仍失败，会再次被 applyUpstreamFailure 冷却
						g.svc.Store().MutateAccountState(a.UID, func(st *AccountState) { st.CooldownUntil = "" })
						delete(exclude, a.UID) // 允许同账号再试一次
						continue
					}
				} else if isReloginRequired(rerr) {
					// RT 被服务端明确拒绝：标记需重新登录（状态推导不再是 online，
					// 自动排除出账号池），轮转到下一个账号
					g.svc.Store().MutateAccountState(a.UID, func(st *AccountState) {
						st.NeedsRelogin = true
						st.Note = "refresh token 被服务端拒绝，需重新授权登录"
					})
					EmitEvent(EventAccountStatus, map[string]any{"uid": a.UID, "status": "relogin"})
					exclude[a.UID] = true
					lastMsg += "；refresh token 已失效，该账号需重新授权登录"
					continue
				}
				// 刷新失败：账号已按 401 冷却，轮转到下一个
			}
			if retryableUpstream(kind) {
				exclude[a.UID] = true
				continue
			}
			// 请求级错误：不换号，直接返回
			g.recordLog(auth, req.Model, res.status, 0, 0, 0, start, ip, req.SessionID, req.Stream, lastMsg)
			writeAPIError(w, res.status, "upstream_error", lastMsg)
			return
		}
		// 成功拿到上游流：保持占用进入中继阶段
		account, upRes, haveRes = a, res, true
		if refreshed[a.UID] {
			// 刷新重试成功：账号已证可用，清掉 401 冷却
			g.svc.Store().MutateAccountState(a.UID, func(st *AccountState) { st.CooldownUntil = "" })
		}
		break
	}

	if !haveRes {
		if lastMsg == "" {
			msg := fmt.Sprintf("账号池无可用账号：请把凭证文件放入 %s 或完成一次授权登录", g.svc.AuthDir())
			g.recordLog(auth, req.Model, http.StatusServiceUnavailable, 0, 0, 0, start, ip, req.SessionID, req.Stream, msg)
			writeAPIError(w, http.StatusServiceUnavailable, "no_available_account", msg)
			return
		}
		status := lastStatus
		if status == 0 {
			status = http.StatusBadGateway
		}
		g.recordLog(auth, req.Model, status, 0, 0, 0, start, ip, req.SessionID, req.Stream, lastMsg)
		writeAPIError(w, status, "upstream_error", lastMsg)
		return
	}
	defer g.svc.tracker.leave(account.UID)
	defer upRes.body.Close()

	prompt := buildPrompt(req)
	inTokens, outTokens, cacheTokens := 0, 0, 0
	var usageCredit float64
	var usageTotal int
	if req.Stream {
		p, c, ca, cl, rl, cr, ut, serr := relayStream(w, upRes.body)
		// usage 优先取上游末帧；缺失时按内容字符数兜底（流式拿不到完整 prompt，
		// 只如实记输出侧）
		inTokens, outTokens, cacheTokens = p, c, ca
		usageCredit, usageTotal = cr, ut
		if inTokens == 0 && outTokens == 0 {
			outTokens = cl + rl
		}
		if serr != nil {
			// 流中断：已发出的帧无法撤回，落一条真实错误日志
			g.recordLog(auth, req.Model, http.StatusBadGateway, 0, 0, 0, start, ip, req.SessionID, true, "上游流中断: "+serr.Error())
			return
		}
	} else {
		content, reasoning, p, c, ca, toolCalls, cr, ut, serr := aggregateStream(upRes.body)
		if serr != nil {
			msg := "上游流聚合失败: " + serr.Error()
			g.recordLog(auth, req.Model, http.StatusBadGateway, 0, 0, 0, start, ip, req.SessionID, false, msg)
			writeAPIError(w, http.StatusBadGateway, "upstream_stream_error", msg)
			return
		}
		inTokens, outTokens, cacheTokens = p, c, ca
		usageCredit, usageTotal = cr, ut
		if inTokens == 0 {
			inTokens = countTokens(prompt)
		}
		if outTokens == 0 {
			outTokens = countTokens(content + reasoning)
		}
		message := map[string]any{
			"role": "assistant", "content": content, "reasoning_content": reasoning,
		}
		if toolCalls != nil {
			message["tool_calls"] = toolCalls
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id":      "chatcmpl-" + NewID(""),
			"object":  "chat.completion",
			"created": time.Now().Unix(),
			"model":   req.Model,
			"choices": []any{map[string]any{
				"index":         0,
				"message":       message,
				"finish_reason": "stop",
			}},
			"usage": map[string]any{
				"prompt_tokens": inTokens, "completion_tokens": outTokens,
				"total_tokens": inTokens + outTokens,
			},
		})
	}

	latency := float64(time.Since(start).Microseconds()) / 1000.0
	// 免费/收费学习：usage.credit 观测落「账号+模型」账本（对齐 workbuddy-gateway probe）
	g.learnModelCost(account.UID, realModel, usageCredit, usageTotal)
	// 先记账再落日志：日志落盘是本次请求的最后一笔写，便于测试以「日志已落盘」为完成信号
	g.chargeAndTouch(auth, account, realModel, inTokens+outTokens, latency)
	g.recordLog(auth, req.Model, http.StatusOK, inTokens+outTokens, outTokens, cacheTokens, start, ip, req.SessionID, req.Stream, "")
}

// applyPromptPolicy 出站前置处理：系统提示词三模式 + WAF 指纹净化
func (g *Gateway) applyPromptPolicy(body []byte, p PromptConfig) []byte {
	return SanitizeUpstreamBody(ApplyPromptMode(p.Mode, p.Text, body))
}

// ---- Embeddings / Images（管线完整、上游能力未开放的明确报错） ----
//
// 上游目前没有经过实测验证的 embeddings / images 端点：模型目录里 nes- 前缀
//（嵌入/补全）与 text-to-image 标签条目被官方口径排除——经 chat 端点选中会报
// code=11102（见 upstream.go nonChatModel），社区参考实现也一律不做透传。
// 这里不猜测未经验证的上游路径，而是把鉴权 / 模型白名单 / 日志管线完整跑一遍，
// 返回 OpenAI 兼容的结构化错误：接入方得到明确的失败语义而不是连接层 404，
// 上游未来开放时只需在本文件补充转发逻辑。
func (g *Gateway) handleEmbeddings(w http.ResponseWriter, r *http.Request) {
	g.handleUnsupportedUpstream(w, r, "embeddings")
}

func (g *Gateway) handleImages(w http.ResponseWriter, r *http.Request) {
	g.handleUnsupportedUpstream(w, r, "images/generations")
}

// handleUnsupportedUpstream 不支持能力的统一入口：鉴权 → 模型白名单 → 结构化报错。
func (g *Gateway) handleUnsupportedUpstream(w http.ResponseWriter, r *http.Request, capability string) {
	start := time.Now()
	ip := clientIP(r)
	auth, denyStatus, ok := g.authenticate(w, r)
	if !ok {
		g.recordLog(auth, "", denyStatus, 0, 0, 0, start, ip, "", false, "鉴权未通过")
		return
	}
	var probe struct {
		Model string `json:"model"`
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	_ = json.Unmarshal(body, &probe)
	if probe.Model != "" && !ModelAllowed(auth.models, probe.Model) {
		msg := fmt.Sprintf("密钥「%s」无权访问模型 %s", auth.keyName, probe.Model)
		g.recordLog(auth, probe.Model, http.StatusForbidden, 0, 0, 0, start, ip, "", false, msg)
		writeAPIError(w, http.StatusForbidden, "model_not_allowed", msg)
		return
	}
	model := probe.Model
	if model == "" {
		model = capability
	}
	msg := fmt.Sprintf("上游暂未开放 %s 能力（嵌入/图像类模型经 chat 端点选中报 code=11102），本网关不猜测未经验证的上游端点", capability)
	g.recordLog(auth, model, http.StatusNotImplemented, 0, 0, 0, start, ip, "", false, msg)
	writeAPIError(w, http.StatusNotImplemented, "upstream_not_supported", msg)
}

// buildPrompt 把系统提示与历史消息拼成用于 token 计数的完整输入
func buildPrompt(req chatRequest) string {
	var sb strings.Builder
	if req.System != "" {
		sb.WriteString(req.System)
		sb.WriteString("\n")
	}
	for _, m := range req.Messages {
		sb.WriteString(m.Role)
		sb.WriteString(": ")
		sb.WriteString(m.Content)
		sb.WriteString("\n")
	}
	return sb.String()
}

// truncateBody 错误响应体截断（用于日志与错误透传，避免大 HTML 刷屏）
func truncateBody(b []byte) string {
	s := strings.TrimSpace(string(b))
	if rs := []rune(s); len(rs) > 200 {
		s = string(rs[:200]) + "…"
	}
	if s == "" {
		return ""
	}
	return "：" + s
}

// pickAccount 从真实账号里挑一个可用账号（成本分层 + 轮询 + 并发上限）
func (g *Gateway) pickAccount() (Account, bool) {
	return g.pickAccountSticky("", "", nil)
}

// pickAccountExcluding 同 pickAccount，但跳过 exclude 中的账号（轮转重试用）
func (g *Gateway) pickAccountExcluding(exclude map[string]bool) (Account, bool) {
	return g.pickAccountSticky("", "", exclude)
}

// pickAccountSticky 挑选可用账号：候选集先做成本分层（对齐 pick.go），带 sessionID
// 时按其稳定哈希粘性选号（同一会话固定同账号，利于上游 prompt 缓存与会话连续性），
// 否则轮询。候选集 = 在线且未被排除且未达并发上限的账号。
func (g *Gateway) pickAccountSticky(sessionID, model string, exclude map[string]bool) (Account, bool) {
	cfg := g.svc.GetConfig()
	candidates := make([]Account, 0, 8)
	for _, a := range g.svc.Accounts() {
		if exclude != nil && exclude[a.UID] {
			continue
		}
		if a.Status != "online" {
			continue
		}
		if cfg.Pool.MaxInFlight > 0 && g.svc.tracker.get(a.UID) >= cfg.Pool.MaxInFlight {
			continue
		}
		candidates = append(candidates, a)
	}
	if len(candidates) == 0 {
		return Account{}, false
	}
	candidates = g.filterExpiryTier(candidates)
	if len(candidates) == 0 {
		return Account{}, false
	}
	candidates = g.filterCostTier(candidates, model, cfg)
	if len(candidates) == 0 {
		return Account{}, false
	}
	if sessionID != "" {
		h := fnv.New32a()
		_, _ = h.Write([]byte(sessionID))
		a := candidates[h.Sum32()%uint32(len(candidates))]
		g.svc.MirrorSetBind(sessionID, a.UID)
		return a, true
	}
	start := atomic.AddUint64(&g.rr, 1)
	return candidates[start%uint64(len(candidates))], true
}

// filterExpiryTier 到期分层选号（对齐 workbuddy-switch-gateway 的两级策略第一级）：
// 额度一旦过期就是净损失，所以「先烧快过期积分」必须是确定性行为而非概率行为。
// 规则：按账号运行态里的最早到期日（CreditsExpireDay，来自真实余额巡检）分组，
// 只保留最早到期的那一档；同日到期的账号交给后续的成本分层/粘性/轮询平均分摊；
// 到期日未知的账号排最后（只在其它账号都不可用时才轮到）；
// 全部候选都没有到期信息时回退原口径（不改变行为）。
func (g *Gateway) filterExpiryTier(cands []Account) []Account {
	if len(cands) <= 1 {
		return cands
	}
	dayOf := make(map[string]string, len(cands))
	any := false
	for _, a := range cands {
		d := g.svc.Store().AccountState(a.UID).CreditsExpireDay
		dayOf[a.UID] = d
		if d != "" {
			any = true
		}
	}
	if !any {
		return cands // 无到期数据 → 自动回退
	}
	earliest := ""
	unknownExists := false
	for _, d := range dayOf {
		if d == "" {
			unknownExists = true
			continue
		}
		if earliest == "" || d < earliest {
			earliest = d
		}
	}
	out := make([]Account, 0, len(cands))
	for _, a := range cands {
		if dayOf[a.UID] == earliest {
			out = append(out, a)
		}
	}
	// 兜底：分层结果为空（理论上不可能，earliest 来自候选集）时不阻塞请求
	if len(out) == 0 {
		return cands
	}
	// 全部账号同日到期时分层无区分度，保持原样即可；unknownExists 仅用于
	// 判断是否有未知到期账号被本轮排除（排最后语义已达成）
	_ = unknownExists
	return out
}

// filterCostTier 成本分层选号（对齐 pick.go costTier）：0=已观测免费、1=未观测、
// 2=已观测收费，只保留最优层；条件探索——最优层为「已观测免费」且存在未观测账号
// 时，按 cost_explore_interval 间隔放行一次未观测层（搭车探索，零额外请求）。
// 层级判定优先读「账号+模型」粒度的免费/收费学习账本（usage.credit 实测），
// 同名模型跨站点免费/收费差异由此天然实现「免费站点优先」；账本无记录时
// 回退账号级成本 EMA 口径。
func (g *Gateway) filterCostTier(cands []Account, model string, cfg *Config) []Account {
	if len(cands) <= 1 {
		return cands
	}
	tierOf := func(a Account) int {
		st := g.svc.Store().AccountState(a.UID)
		if model != "" {
			if free, ok := st.ModelFreeLedger[model]; ok {
				if free {
					return 0
				}
				return 2
			}
		}
		switch {
		case st.CostSamples <= 0:
			return 1 // 未观测
		case st.CostEMA <= 0:
			return 0 // 已观测免费
		default:
			return 2 // 已观测收费
		}
	}
	best := 3
	hasTier1 := false
	for _, a := range cands {
		t := tierOf(a)
		if t < best {
			best = t
		}
		if t == 1 {
			hasTier1 = true
		}
	}
	explore := false
	if d, err := time.ParseDuration(cfg.Pool.CostExploreInterval); err == nil && d > 0 &&
		best == 0 && model != "" && hasTier1 {
		g.mu.Lock()
		last, seen := g.exploreLast[model]
		if !seen || time.Since(last) >= d {
			g.exploreLast[model] = time.Now()
			explore = true
		}
		g.mu.Unlock()
	}
	out := make([]Account, 0, len(cands))
	for _, a := range cands {
		t := tierOf(a)
		if t == best || (explore && t == 1) {
			out = append(out, a)
		}
	}
	if len(out) == 0 {
		return cands // 兜底：分层结果为空时不阻塞请求
	}
	return out
}

// retryableUpstream 判断上游错误是否值得轮转换号重试：
// 账号级故障（余额/限流/会话/服务端/网络）换号有意义；请求级错误换号无用。
func retryableUpstream(kind upstreamErrKind) bool {
	switch kind {
	case upErrHardCredit, upErrSoftRate, upErrSessionDead, upErrNotFound, upErrServer, upErrNetwork:
		return true
	}
	return false
}

// recordLog 写入一条请求日志（真实指标）。cacheTokens 为上游 usage 命中的缓存
// 输入 token，仅在错误路径或上游未返回 usage 时为 0。
func (g *Gateway) recordLog(auth authInfo, model string, status, tokens, outTokens, cacheTokens int, start time.Time, ip, sessionID string, stream bool, errMsg string) {
	latency := float64(time.Since(start).Microseconds()) / 1000.0
	name := auth.keyName
	if name == "" {
		name = "(未鉴权)"
	}
	inTokens := tokens - outTokens
	if inTokens < 0 {
		inTokens = 0
	}
	g.svc.Store().AppendRequestLog(RequestLog{
		ID: NewID("r"), TS: time.Now().Unix(), Time: Now(),
		KeyName: name, KeyID: auth.keyID, Model: model, Status: status,
		Tokens: tokens, InputTokens: inTokens, OutputTokens: outTokens,
		CacheTokens: cacheTokens,
		Latency: latency, IP: ip, SessionID: sessionID, Stream: stream, Error: errMsg,
	})
	level := "info"
	if status >= 400 {
		level = "warn"
	}
	detail := fmt.Sprintf("%s · %s · %d · %d tok · %.0fms", name, model, status, tokens, latency)
	if errMsg != "" {
		detail += " · " + errMsg
	}
	EmitEvent(EventGatewayLog, map[string]any{"level": level, "message": detail})
}

// modelRate 模型积分倍率（模型中心配置；未配置的模型按 1 计）
func (g *Gateway) modelRate(model string) float64 {
	if r, ok := g.svc.GetConfig().Models.Rates[model]; ok && r >= 0 {
		return r
	}
	return 1
}

// chargeAndTouch 成功请求后：按模型倍率累加密钥配额用量 + 刷新账号最近活动时间
// + 累计服务 token 数（供成本账本在下次余额查询时换算每 1k token 成本）
func (g *Gateway) chargeAndTouch(auth authInfo, account Account, model string, tokens int, latency float64) {
	if auth.key != nil {
		credits := int(float64(tokens) / 1000.0 * g.modelRate(model))
		g.svc.Store().ConsumeKey(auth.key.ID, tokens, credits)
	}
	g.svc.Store().MutateAccountState(account.UID, func(st *AccountState) {
		st.LastActivity = Now()
		st.FailStreak = 0
		st.TokensSinceSync += tokens
	})
	_ = latency
}

// learnModelCost 免费/收费学习（对齐 workbuddy-gateway 的 usage.credit 学习口径）：
// 按「账号+模型」从上游 usage.credit 学习——credit>0 → 收费；credit=0 且
// total_tokens≥100（样本足够，避免小请求/探针被上游记 0 误判）→ 免费。
// 样本不足时不写不覆盖，保持未知。写入逻辑统一在 learnModelCostTo（主动探测共用）。
func (g *Gateway) learnModelCost(uid, model string, credit float64, usageTotal int) {
	learnModelCostTo(g.svc.Store(), uid, model, credit, usageTotal)
}

// learnFreeMinTokens 免费判定的最小样本量（对齐 workbuddy-gateway：total_tokens≥100）。
const learnFreeMinTokens = 100

// countTokens 计算 token 数。
//
// 口径：ASCII 按 4 字符 1 token、非 ASCII（中日韩等）按 1 字符 1 token——这是本
// 网关的局部口径（上游返回权威 usage 时会被替换），不是「展示用假数据」：它就是
// 网关实际用于配额记账的数字，日志里落盘的也是它。
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

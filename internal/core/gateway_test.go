package core

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	return NewStore(filepath.Join(t.TempDir(), "store.json"))
}

// newTestService 构造隔离的服务实例：数据目录、凭证目录都在临时目录里
func newTestService(t *testing.T) *Service {
	t.Helper()
	dir := t.TempDir()
	svc := NewServiceAt(dir)
	cfg := svc.GetConfig()
	cfg.AuthDir = filepath.Join(dir, "auths")
	if err := svc.UpdateConfig(cfg); err != nil {
		t.Fatalf("写入配置失败: %v", err)
	}
	return svc
}

// seedCredential 往凭证目录写入一份真实凭证（ttl 为 token 剩余有效期）
func seedCredential(t *testing.T, authDir, uid string, ttl time.Duration) {
	t.Helper()
	body := []byte(`{"auth":{"accessToken":"at-` + uid + `","refreshToken":"rt-` + uid +
		`","expiresAt":` + strconv.FormatInt(time.Now().Add(ttl).Unix(), 10) +
		`,"domain":"codebuddy.cn","realm":"cn"},"account":{"uid":"` + uid + `","nickname":"账号-` + uid + `"}}`)
	if _, err := ImportCredential(authDir, "workbuddy-"+uid+".json", body); err != nil {
		t.Fatalf("写入凭证失败: %v", err)
	}
}

// ---- 存储：不再有种子数据 ----

func TestStoreStartsEmpty(t *testing.T) {
	s := newTestStore(t)
	if len(s.ListKeys()) != 0 || len(s.ListRequestLogs()) != 0 || len(s.ListTaskLogs()) != 0 {
		t.Fatal("首次运行应为空库，不得包含种子演示数据")
	}
	if s.AccountState("uid_x").CreditsKnown {
		t.Fatal("未知账号不应带出模拟余额")
	}
}

func TestStoreKeyCRUDAndConsume(t *testing.T) {
	s := newTestStore(t)
	s.PutKey(ApiKey{ID: "key_test", Name: "t", KeyHash: HashKey("wb-plain"), Mask: MaskKey("wb-plain"), TokenQuota: 100})
	k, ok := s.GetKey("key_test")
	if !ok || k.Name != "t" {
		t.Fatal("密钥写入失败")
	}
	if found, ok := s.FindKeyByHash(HashKey("wb-plain")); !ok || found.ID != "key_test" {
		t.Fatal("按摘要查找失败")
	}
	if _, ok := s.FindKeyByHash(HashKey("wb-other")); ok {
		t.Fatal("错误明文不应命中密钥")
	}
	s.ConsumeKey("key_test", 120, 3)
	k, _ = s.GetKey("key_test")
	if k.TokenUsed != 120 || k.CreditUsed != 3 || k.Requests != 1 || k.LastUsed == "" {
		t.Fatalf("用量累计异常: %+v", k)
	}
	if !s.DeleteKey("key_test") {
		t.Fatal("密钥删除失败")
	}
}

func TestStoreLogsAndClear(t *testing.T) {
	s := newTestStore(t)
	s.AppendRequestLog(RequestLog{Time: Now(), Status: 200, Tokens: 10})
	s.AppendTaskLog(TaskLog{Time: Now(), Type: TaskCheckin, Status: TaskSkipped, Message: "m"})
	if s.ListRequestLogs()[0].TS == 0 {
		t.Fatal("请求日志应自动补齐时间戳")
	}
	if n := s.ClearLogs("request"); n != 1 {
		t.Fatalf("清空请求日志条数 = %d, want 1", n)
	}
	if len(s.ListRequestLogs()) != 0 || len(s.ListTaskLogs()) != 1 {
		t.Fatal("清空范围不正确")
	}
}

func TestAccountStatePersist(t *testing.T) {
	s := newTestStore(t)
	s.MutateAccountState("uid_1", func(st *AccountState) {
		st.Credits = 42.5
		st.CreditsKnown = true
	})
	// 合并落盘语义：显式 Flush 后重开存储校验
	if err := s.Flush(); err != nil {
		t.Fatalf("Flush 失败: %v", err)
	}
	reloaded := NewStore(s.Path())
	st := reloaded.AccountState("uid_1")
	if !st.CreditsKnown || st.Credits != 42.5 {
		t.Fatalf("运行态未持久化: %+v", st)
	}
}

// ---- 凭证：真实账号来源 ----

func TestParseCredentialBothForms(t *testing.T) {
	nested := []byte(`{"auth":{"accessToken":"a","refreshToken":"r","expiresAt":4102444800,"domain":"codebuddy.cn"},"account":{"uid":"u1","nickname":"n1"}}`)
	c, err := ParseCredential("workbuddy-1.json", nested)
	if err != nil {
		t.Fatalf("嵌套形解析失败: %v", err)
	}
	if c.UID != "u1" || c.Nickname != "n1" || c.Realm != "cn" {
		t.Fatalf("嵌套形解析结果异常: %+v", c)
	}

	flat := []byte(`{"accessToken":"a","refreshToken":"r","expiresAt":4102444800,"domain":"x.workbuddy.ai","uid":"u2","realm":"global"}`)
	c2, err := ParseCredential("workbuddy-2.json", flat)
	if err != nil {
		t.Fatalf("扁平形解析失败: %v", err)
	}
	if c2.UID != "u2" || c2.Realm != "global" {
		t.Fatalf("扁平形解析结果异常: %+v", c2)
	}

	if _, err := ParseCredential("bad.json", []byte(`{"foo":1}`)); err == nil {
		t.Fatal("缺少 token 的凭证应报错")
	}
}

func TestBuildAccountsStatus(t *testing.T) {
	store := newTestStore(t)
	now := time.Now()
	creds := []Credential{
		{File: "a.json", UID: "u_ok", Realm: "cn", HasToken: true, HasRefresh: true, ExpiresAt: now.Add(48 * time.Hour).Unix()},
		{File: "b.json", UID: "u_exp", Realm: "cn", HasToken: true, HasRefresh: true, ExpiresAt: now.Add(-time.Hour).Unix()},
		{File: "c.json", UID: "u_unknown", Realm: "cn", HasToken: true, HasRefresh: true},
		{File: "d.json", UID: "u_bad", Realm: "cn"},
	}
	store.MutateAccountState("u_ok", func(st *AccountState) {
		st.CooldownUntil = now.Add(10 * time.Minute).Format(time.RFC3339)
	})
	store.MutateAccountState("u_exp", func(st *AccountState) { st.Disabled = true })

	got := map[string]string{}
	for _, a := range BuildAccounts(creds, store, nil) {
		got[a.UID] = a.Status
	}
	want := map[string]string{
		"u_ok": "cooldown", "u_exp": "disabled", "u_unknown": "unknown", "u_bad": "invalid",
	}
	for uid, w := range want {
		if got[uid] != w {
			t.Fatalf("账号 %s 状态 = %q, want %q", uid, got[uid], w)
		}
	}
}

func TestImportDeleteCredential(t *testing.T) {
	dir := t.TempDir()
	body := []byte(`{"auth":{"accessToken":"a","refreshToken":"r","expiresAt":4102444800},"account":{"uid":"u9","nickname":"n"}}`)
	file, err := ImportCredential(dir, "my-account", body)
	if err != nil {
		t.Fatalf("导入失败: %v", err)
	}
	if file != "workbuddy-my-account.json" {
		t.Fatalf("落盘文件名 = %q", file)
	}
	creds := LoadCredentials(dir)
	if len(creds) != 1 || creds[0].UID != "u9" {
		t.Fatalf("凭证扫描结果异常: %+v", creds)
	}
	if _, err := ImportCredential(dir, "bad", []byte(`{"x":1}`)); err == nil {
		t.Fatal("非法凭证应拒绝导入")
	}
	name, err := DeleteCredential(dir, "u9")
	if err != nil || name != file {
		t.Fatalf("删除凭证失败: %v %s", err, name)
	}
	if len(LoadCredentials(dir)) != 0 {
		t.Fatal("删除后仍能扫描到凭证")
	}
}

// ---- 统计：真实聚合 ----

func TestStatsAggregation(t *testing.T) {
	store := newTestStore(t)
	base := time.Now().Add(-time.Hour).Unix()
	store.AppendRequestLog(RequestLog{TS: base, Time: Now(), KeyID: "k1", KeyName: "k1", Model: "glm-5.2", Status: 200, Tokens: 100, InputTokens: 60, OutputTokens: 40, Latency: 100, SessionID: "s1"})
	store.AppendRequestLog(RequestLog{TS: base + 60, Time: Now(), KeyID: "k1", KeyName: "k1", Model: "glm-5.2", Status: 200, Tokens: 200, InputTokens: 150, OutputTokens: 50, Latency: 300, SessionID: "s1"})
	store.AppendRequestLog(RequestLog{TS: base + 120, Time: Now(), KeyID: "k2", KeyName: "k2", Model: "kimi-k2.7", Status: 429, Tokens: 0, Latency: 20, SessionID: "s2"})

	stats := NewStats(store, func() []Account { return nil }, nil)
	d := stats.Dashboard(7)

	if d.Overview.Requests != 3 || d.Overview.Tokens != 300 {
		t.Fatalf("总览异常: %+v", d.Overview)
	}
	if d.Overview.Errors != 1 {
		t.Fatalf("错误数 = %d, want 1", d.Overview.Errors)
	}
	if d.Overview.InputTokens != 210 || d.Overview.OutputTokens != 90 {
		t.Fatalf("输入输出拆分异常: in=%d out=%d", d.Overview.InputTokens, d.Overview.OutputTokens)
	}
	if d.Overview.Sessions != 2 {
		t.Fatalf("会话数 = %d, want 2", d.Overview.Sessions)
	}
	if d.Overview.P50 <= 0 || d.Overview.P90 < d.Overview.P50 {
		t.Fatalf("延迟分位异常: p50=%v p90=%v", d.Overview.P50, d.Overview.P90)
	}
	if len(d.Daily) != 7 {
		t.Fatalf("逐日序列长度 = %d, want 7（无数据日应补 0 保持连续）", len(d.Daily))
	}
	if len(d.Models) != 2 || d.Models[0].Model != "glm-5.2" {
		t.Fatalf("模型聚合异常: %+v", d.Models)
	}
	if len(d.Hourly) != 24 {
		t.Fatalf("时段聚合长度 = %d, want 24", len(d.Hourly))
	}
	if len(d.Keys) != 2 {
		t.Fatalf("密钥聚合异常: %+v", d.Keys)
	}
}

func TestPercentile(t *testing.T) {
	vals := []float64{10, 20, 30, 40, 50}
	if got := Percentile(vals, 50); got != 30 {
		t.Fatalf("P50 = %v, want 30", got)
	}
	if got := Percentile(vals, 0); got != 10 {
		t.Fatalf("P0 = %v, want 10", got)
	}
	if got := Percentile(nil, 90); got != 0 {
		t.Fatalf("空集合应为 0, got %v", got)
	}
}

// ---- 网关：真实鉴权 / 配额 / 记账 / 账号路由 ----

// fakeUpstream 伪上游：返回一段标准 SSE（含 usage 末帧），供网关测试做端到端中继。
func fakeUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/v2/chat/completions") {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		frame := func(payload string) {
			fmt.Fprintf(w, "data: %s\n\n", payload)
			fl.Flush()
		}
		frame(`{"choices":[{"delta":{"reasoning_content":"思考中"}}]}`)
		frame(`{"choices":[{"delta":{"content":"你好"}}]}`)
		frame(`{"choices":[{"delta":{"content":"，世界"}}]}`)
		frame(`{"usage":{"prompt_tokens":9,"completion_tokens":5,"total_tokens":14}}`)
		frame("[DONE]")
	}))
}

func startTestGateway(t *testing.T, svc *Service) string {
	t.Helper()
	up := fakeUpstream(t)
	t.Cleanup(up.Close)
	svc.gateway.up.baseCN = up.URL
	svc.gateway.up.baseGlobal = up.URL
	if err := svc.gateway.Start("127.0.0.1:0"); err != nil {
		t.Fatalf("网关启动失败: %v", err)
	}
	t.Cleanup(func() { _ = svc.gateway.Stop() })
	return "http://" + svc.gateway.Addr()
}

func postChat(t *testing.T, base, key, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, base+"/v1/chat/completions", strings.NewReader(body))
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	return resp
}

func TestGatewayHealth(t *testing.T) {
	svc := newTestService(t)
	base := startTestGateway(t, svc)
	resp, err := http.Get(base + "/healthz")
	if err != nil {
		t.Fatalf("healthz 失败: %v", err)
	}
	defer resp.Body.Close()
	var health map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&health)
	if health["status"] != "ok" {
		t.Fatalf("healthz 内容异常: %v", health)
	}
}

func TestGatewayAuthErrors(t *testing.T) {
	svc := newTestService(t)
	base := startTestGateway(t, svc)

	// 无密钥 → 401
	res := postChat(t, base, "", `{"model":"glm-5.2"}`)
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("无密钥状态码 = %d, want 401", res.StatusCode)
	}

	// 错误密钥 → 401
	res = postChat(t, base, "wb-wrong", `{"model":"glm-5.2"}`)
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("错误密钥状态码 = %d, want 401", res.StatusCode)
	}

	// 两次失败都应落日志
	logs := svc.Store().ListRequestLogs()
	if len(logs) != 2 {
		t.Fatalf("鉴权失败日志条数 = %d, want 2", len(logs))
	}
	for _, l := range logs {
		if l.Status != 401 {
			t.Fatalf("鉴权失败日志状态 = %d, want 401", l.Status)
		}
	}
}

func TestGatewayNoAccountReturns503(t *testing.T) {
	svc := newTestService(t)
	base := startTestGateway(t, svc)
	res := postChat(t, base, svc.GetConfig().APIKey, `{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}]}`)
	defer res.Body.Close()
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("空账号池状态码 = %d, want 503", res.StatusCode)
	}
	var payload struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.NewDecoder(res.Body).Decode(&payload)
	if payload.Error.Code != "no_available_account" {
		t.Fatalf("错误码 = %q", payload.Error.Code)
	}
}

// 会话归属：请求体 session_id 缺失时回退 X-Session-Id header；body 恒优先于 header。
// 借空账号池的 503 错误路径校验日志里的 session 归属（无需上游）。
func TestGatewaySessionIDHeaderFallback(t *testing.T) {
	svc := newTestService(t)
	base := startTestGateway(t, svc)

	post := func(bodySession, headerSession string) {
		t.Helper()
		body := `{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}]}`
		if bodySession != "" {
			body = `{"model":"glm-5.2","session_id":"` + bodySession + `","messages":[{"role":"user","content":"hi"}]}`
		}
		req, _ := http.NewRequest(http.MethodPost, base+"/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+svc.GetConfig().APIKey)
		if headerSession != "" {
			req.Header.Set("X-Session-Id", headerSession)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("请求失败: %v", err)
		}
		res.Body.Close()
	}

	post("", "sess-hdr")    // 仅 header → 兜底生效
	post("sess-body", "")   // 仅 body
	post("sess-both", "x")  // body + header → body 优先

	logs := svc.Store().ListRequestLogs() // 最新在前
	if len(logs) != 3 {
		t.Fatalf("日志条数 = %d, want 3", len(logs))
	}
	for i, want := range []string{"sess-both", "sess-body", "sess-hdr"} {
		if logs[i].SessionID != want {
			t.Fatalf("日志[%d].SessionID = %q, want %q", i, logs[i].SessionID, want)
		}
	}
}

func TestGatewaySuccessChargesAccountAndKey(t *testing.T) {
	svc := newTestService(t)
	seedCredential(t, svc.AuthDir(), "u_ok", 30*24*time.Hour)

	// 建一个受限密钥用于验证记账
	plain := "wb-testkey-fixed"
	svc.Store().PutKey(ApiKey{
		ID: "key_t", Name: "测试密钥", KeyHash: HashKey(plain), Mask: MaskKey(plain),
		Models: []string{"glm-5.2"}, TokenQuota: 100000, CreditQuota: 10000,
	})

	base := startTestGateway(t, svc)
	res := postChat(t, base, plain,
		`{"model":"glm-5.2","stream":false,"session_id":"sess-1","messages":[{"role":"user","content":"你好"}]}`)
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d, want 200", res.StatusCode)
	}
	var payload struct {
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(res.Body).Decode(&payload); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if payload.Usage.TotalTokens <= 0 || payload.Usage.PromptTokens <= 0 {
		t.Fatalf("用量异常: %+v", payload.Usage)
	}

	logs := svc.Store().ListRequestLogs()
	if len(logs) != 1 || logs[0].Status != 200 || logs[0].SessionID != "sess-1" {
		t.Fatalf("请求日志异常: %+v", logs)
	}
	if logs[0].InputTokens != payload.Usage.PromptTokens || logs[0].OutputTokens != payload.Usage.CompletionTokens {
		t.Fatalf("日志输入输出拆分与响应不一致: %+v", logs[0])
	}

	key, _ := svc.Store().GetKey("key_t")
	if key.TokenUsed != payload.Usage.TotalTokens || key.Requests != 1 {
		t.Fatalf("密钥未按真实用量记账: %+v", key)
	}
	acc, _ := svc.FindAccount("u_ok")
	if acc.LastActivity == "" {
		t.Fatal("账号最近活动未更新")
	}
}

func TestGatewayModelWhitelistAndQuota(t *testing.T) {
	svc := newTestService(t)
	seedCredential(t, svc.AuthDir(), "u_ok", 30*24*time.Hour)

	plain := "wb-limited-fixed"
	svc.Store().PutKey(ApiKey{
		ID: "key_l", Name: "受限", KeyHash: HashKey(plain), Mask: MaskKey(plain),
		Models: []string{"glm-5.2"}, TokenQuota: 10000, CreditQuota: 10000,
	})
	base := startTestGateway(t, svc)

	// 模型不在白名单 → 403
	res := postChat(t, base, plain, `{"model":"kimi-k2.7","messages":[{"role":"user","content":"hi"}]}`)
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("模型白名单状态码 = %d, want 403", res.StatusCode)
	}

	// 配额用尽 → 429
	svc.Store().MutateAccountState("u_ok", func(st *AccountState) { st.Credits = 0 })
	key, _ := svc.Store().GetKey("key_l")
	key.TokenUsed = key.TokenQuota
	svc.Store().PutKey(key)
	res = postChat(t, base, plain, `{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}]}`)
	res.Body.Close()
	if res.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("配额用尽状态码 = %d, want 429", res.StatusCode)
	}
}

func TestGatewayStreaming(t *testing.T) {
	svc := newTestService(t)
	seedCredential(t, svc.AuthDir(), "u_ok", 30*24*time.Hour)
	base := startTestGateway(t, svc)
	res := postChat(t, base, svc.GetConfig().APIKey,
		`{"model":"glm-5.2","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("流式状态码 = %d, want 200", res.StatusCode)
	}
	scanner := bufio.NewScanner(res.Body)
	dataLines, done := 0, false
	deadline := time.Now().Add(20 * time.Second)
	for scanner.Scan() && time.Now().Before(deadline) {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		dataLines++
		if strings.Contains(line, "[DONE]") {
			done = true
			break
		}
	}
	if dataLines < 2 || !done {
		t.Fatalf("流式输出异常: data=%d done=%v", dataLines, done)
	}
	// [DONE] 之后网关才落账；等待日志出现，避免与临时目录清理产生竞态
	waitForLogs(t, svc, 1)
}

// waitForLogs 轮询等待请求日志达到 want 条（上限 5 秒）。
// 合并落盘语义：内存列表可见即代表网关请求链完成；再显式 Flush 后读盘校验，
// 避免与 TempDir 清理产生竞态。
func waitForLogs(t *testing.T, svc *Service, want int) []RequestLog {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		logs := svc.Store().ListRequestLogs()
		if len(logs) >= want {
			if err := svc.Store().Flush(); err != nil {
				t.Fatalf("Flush 失败: %v", err)
			}
			raw, err := os.ReadFile(svc.Store().Path())
			if err != nil {
				t.Fatalf("读取 store.json 失败: %v", err)
			}
			var d struct {
				RequestLogs []RequestLog `json:"request_logs"`
			}
			if err := json.Unmarshal(raw, &d); err != nil || len(d.RequestLogs) < want {
				t.Fatalf("落盘校验失败: err=%v logs=%d", err, len(d.RequestLogs))
			}
			return logs
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("等待请求日志超时：want>=%d, got=%d", want, len(svc.Store().ListRequestLogs()))
	return nil
}

func TestGatewayRestartBindsSamePort(t *testing.T) {
	svc := newTestService(t)
	if err := svc.gateway.Start("127.0.0.1:0"); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	addr := svc.gateway.Addr()
	if err := svc.gateway.Stop(); err != nil {
		t.Fatalf("停止失败: %v", err)
	}
	if svc.gateway.IsRunning() {
		t.Fatal("停止后仍标记为运行中")
	}
	if err := svc.gateway.Start(addr); err != nil {
		t.Fatalf("重启后绑定 %s 失败: %v", addr, err)
	}
	defer svc.gateway.Stop()
	if !svc.gateway.IsRunning() {
		t.Fatal("重启后未运行")
	}
}

// ---- 调度器 ----

func TestNextFire(t *testing.T) {
	loc := time.Now().Location()
	now := time.Date(2026, 9, 18, 10, 30, 0, 0, loc)
	next, ok := nextFire(now, []int{9, 21})
	if !ok || next.Hour() != 21 || next.Day() != 18 {
		t.Fatalf("下次触发 = %v, want 当天 21:00", next)
	}
	next, _ = nextFire(time.Date(2026, 9, 18, 23, 10, 0, 0, loc), []int{9, 21})
	if next.Day() != 19 || next.Hour() != 9 {
		t.Fatalf("跨天计算错误: %v", next)
	}
}

func TestSchedulerTravelFailsHonestlyWithoutUpstream(t *testing.T) {
	svc := newTestService(t)
	seedCredential(t, svc.AuthDir(), "u_ok", 30*24*time.Hour)
	// 指向一个已关闭的上游地址：连接拒绝 → 任务必须如实 failed，不得伪造成功
	up := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := up.URL
	up.Close()
	svc.Scheduler().up.baseCN, svc.Scheduler().up.baseGlobal = url, url

	run := svc.Scheduler().RunFor(TaskTravel, "manual", nil)
	if run.Total != 1 || run.Failed != 1 {
		t.Fatalf("上游不可达时任务结果应为 failed: %+v", run)
	}
	if run.Success != 0 {
		t.Fatal("不得伪造旅行成功")
	}
	if !strings.Contains(run.Details[0].Message, "查询猫档案失败") {
		t.Fatalf("失败原因应写明: %+v", run.Details[0])
	}
	logs := svc.Store().ListTaskLogs()
	if len(logs) != 1 || logs[0].Trigger != "manual" || logs[0].Status != TaskFailed {
		t.Fatalf("任务日志异常: %+v", logs)
	}
}

func TestSchedulerRecoversExpiredCooldown(t *testing.T) {
	svc := newTestService(t)
	seedCredential(t, svc.AuthDir(), "u_ok", 30*24*time.Hour)
	svc.Store().MutateAccountState("u_ok", func(st *AccountState) {
		st.CooldownUntil = time.Now().Add(-time.Minute).Format(time.RFC3339)
	})
	run := svc.Scheduler().RunFor(TaskKeepalive, "schedule", nil)
	if run.Success != 1 {
		t.Fatalf("冷却到期应恢复账号: %+v", run)
	}
	if st := svc.Store().AccountState("u_ok"); st.CooldownUntil != "" {
		t.Fatalf("冷却标记未清除: %+v", st)
	}
}

func TestSchedulerWarnsExpiringToken(t *testing.T) {
	svc := newTestService(t)
	seedCredential(t, svc.AuthDir(), "u_soon", 48*time.Hour)
	run := svc.Scheduler().RunFor(TaskCheckin, "manual", nil)
	if run.Failed != 1 {
		t.Fatalf("token 即将过期应记 failed: %+v", run)
	}
	if !strings.Contains(run.Details[0].Message, "过期") {
		t.Fatalf("预警信息缺失: %+v", run.Details[0])
	}
}

func TestModelsCatalog(t *testing.T) {
	store := newTestStore(t)
	models := ListModels(store)
	if len(models) < 10 {
		t.Fatalf("模型目录条数异常: %d", len(models))
	}
	for _, m := range models {
		if m.ID == "auto" {
			t.Fatal("auto 为路由别名，不应出现在可请求模型清单")
		}
	}
	if !ModelAllowed([]string{"*"}, "anything") {
		t.Fatal("通配白名单应放行")
	}
	if ModelAllowed([]string{"glm-5.2"}, "kimi-k2.7") {
		t.Fatal("白名单外模型不应放行")
	}
}

// ---- 上游出站：错误分类 / 冷却 / 请求体改写 ----

func TestClassifyUpstreamError(t *testing.T) {
	cases := []struct {
		status int
		body   string
		want   upstreamErrKind
	}{
		{http.StatusPaymentRequired, `{"msg":"积分不足"}`, upErrHardCredit},
		{http.StatusUnauthorized, `{"msg":"offline session"}`, upErrSessionDead},
		{http.StatusTooManyRequests, `{"msg":"rate limit"}`, upErrSoftRate},
		{http.StatusNotFound, `not found`, upErrNotFound},
		{http.StatusInternalServerError, `boom`, upErrServer},
		{http.StatusBadRequest, `{"msg":"prompt is too long"}`, upErrPromptTooLong},
		{http.StatusBadRequest, `{"msg":"余额不足，请充值"}`, upErrHardCredit},
		{http.StatusBadRequest, `{"msg":"参数错误"}`, upErrClient},
	}
	for _, c := range cases {
		if got := classifyUpstreamError(c.status, c.body); got != c.want {
			t.Fatalf("classify(%d, %q) = %s, want %s", c.status, c.body, got, c.want)
		}
	}
}

func TestUpstreamChatRequestForcesStream(t *testing.T) {
	body := upstreamChatRequest(chatRequest{
		Model: "glm-5.2", Stream: false, MaxTokens: 128,
		System: "sys", Messages: []ChatMessage{{Role: "user", Content: "hi"}},
	})
	var obj map[string]any
	if err := json.Unmarshal(body, &obj); err != nil {
		t.Fatalf("请求体非法: %v", err)
	}
	if obj["stream"] != true {
		t.Fatalf("stream 应强制为 true: %v", obj["stream"])
	}
	if _, ok := obj["stream_options"].(map[string]any); !ok {
		t.Fatalf("stream_options 缺失: %v", obj)
	}
	if obj["max_tokens"] != float64(128) {
		t.Fatalf("max_tokens 丢失: %v", obj["max_tokens"])
	}
	msgs, _ := obj["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("system 应前置为独立消息: %v", msgs)
	}
}

func TestGatewayUpstream429CooldownsAccount(t *testing.T) {
	svc := newTestService(t)
	seedCredential(t, svc.AuthDir(), "u_429", 30*24*time.Hour)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"code":429,"msg":"rate limit"}`))
	}))
	defer up.Close()
	svc.gateway.up.baseCN, svc.gateway.up.baseGlobal = up.URL, up.URL
	if err := svc.gateway.Start("127.0.0.1:0"); err != nil {
		t.Fatalf("网关启动失败: %v", err)
	}
	t.Cleanup(func() { _ = svc.gateway.Stop() })
	base := "http://" + svc.gateway.Addr()

	res := postChat(t, base, svc.GetConfig().APIKey, `{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}]}`)
	res.Body.Close()
	if res.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("上游 429 应透传 = %d, want 429", res.StatusCode)
	}
	st := svc.Store().AccountState("u_429")
	if st.CooldownUntil == "" {
		t.Fatal("429 后账号应进入短冷却")
	}
	logs := svc.Store().ListRequestLogs()
	if len(logs) != 1 || logs[0].Status != 429 {
		t.Fatalf("请求日志异常: %+v", logs)
	}
}

func TestGatewayUpstreamClientErrorNoPenalty(t *testing.T) {
	svc := newTestService(t)
	seedCredential(t, svc.AuthDir(), "u_400", 30*24*time.Hour)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":11128,"msg":"role not allowed"}`))
	}))
	defer up.Close()
	svc.gateway.up.baseCN, svc.gateway.up.baseGlobal = up.URL, up.URL
	if err := svc.gateway.Start("127.0.0.1:0"); err != nil {
		t.Fatalf("网关启动失败: %v", err)
	}
	t.Cleanup(func() { _ = svc.gateway.Stop() })
	base := "http://" + svc.gateway.Addr()

	res := postChat(t, base, svc.GetConfig().APIKey, `{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}]}`)
	res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("上游 400 应透传 = %d, want 400", res.StatusCode)
	}
	st := svc.Store().AccountState("u_400")
	if st.CooldownUntil != "" {
		t.Fatalf("请求级 400 不应罚号: %+v", st)
	}
}

func TestLoadUpstreamCredMissingToken(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "workbuddy-x.json"), []byte(`{"uid":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadUpstreamCred(dir, "workbuddy-x.json"); err == nil {
		t.Fatal("缺 accessToken 的凭证应报错")
	}
}

// ---- 账号轮转重试 / 401 刷新重试 / 调度器真实签到与保活 ----

// fakeUpstream 按路径分发的伪上游：
// chat → chatFn（每次请求自增）；refresh → 旋转 token；checkin → 可配置。
func fakeUpstreamRoutes(t *testing.T, chatFn func(call int, auth string) (int, string)) *httptest.Server {
	t.Helper()
	chatCalls := 0
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		switch r.URL.Path {
		case "/v2/chat/completions":
			chatCalls++
			status, body := chatFn(chatCalls, auth)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		case "/v2/plugin/auth/token/refresh":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"accessToken":"at-refreshed","refreshToken":"rt-new","expiresIn":5184000,"domain":"codebuddy.cn"}`))
		case "/v2/billing/meter/daily-checkin":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":{"credits":8}}`))
		default:
			http.NotFound(w, r)
		}
	}))
}

const fakeSSE200 = "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n" +
	"data: {\"choices\":[{\"delta\":{}}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"total_tokens\":5}}\n\n" +
	"data: [DONE]\n\n"

func pointUpstreamAt(t *testing.T, svc *Service, up *httptest.Server) {
	t.Helper()
	svc.gateway.up.baseCN, svc.gateway.up.baseGlobal = up.URL, up.URL
	svc.gateway.up.billingCN, svc.gateway.up.billingGlobal = up.URL, up.URL
	svc.Scheduler().up.baseCN, svc.Scheduler().up.baseGlobal = up.URL, up.URL
	svc.Scheduler().up.billingCN, svc.Scheduler().up.billingGlobal = up.URL, up.URL
}

func startGatewayAt(t *testing.T, svc *Service) string {
	t.Helper()
	if err := svc.gateway.Start("127.0.0.1:0"); err != nil {
		t.Fatalf("网关启动失败: %v", err)
	}
	t.Cleanup(func() { _ = svc.gateway.Stop() })
	return "http://" + svc.gateway.Addr()
}

func TestGatewayRotatesToNextAccountOn429(t *testing.T) {
	svc := newTestService(t)
	seedCredential(t, svc.AuthDir(), "a1", 30*24*time.Hour)
	seedCredential(t, svc.AuthDir(), "a2", 30*24*time.Hour)
	up := fakeUpstreamRoutes(t, func(call int, auth string) (int, string) {
		if call == 1 {
			return http.StatusTooManyRequests, `{"code":429,"msg":"rate limit"}`
		}
		return http.StatusOK, fakeSSE200
	})
	defer up.Close()
	pointUpstreamAt(t, svc, up)
	base := startGatewayAt(t, svc)

	res := postChat(t, base, svc.GetConfig().APIKey, `{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}]}`)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("首个账号 429 后应轮转到下一账号成功 = %d, want 200", res.StatusCode)
	}
	// 恰有一个账号被 429 冷却
	cooled := 0
	for _, uid := range []string{"a1", "a2"} {
		if svc.Store().AccountState(uid).CooldownUntil != "" {
			cooled++
		}
	}
	if cooled != 1 {
		t.Fatalf("应有且仅有一个账号被冷却: cooled=%d", cooled)
	}
}

func TestGateway401RefreshesTokenAndRetries(t *testing.T) {
	svc := newTestService(t)
	seedCredential(t, svc.AuthDir(), "u_rt", 30*24*time.Hour)
	up := fakeUpstreamRoutes(t, func(call int, auth string) (int, string) {
		if auth != "at-refreshed" {
			return http.StatusUnauthorized, `{"code":401,"msg":"offline session"}`
		}
		return http.StatusOK, fakeSSE200
	})
	defer up.Close()
	pointUpstreamAt(t, svc, up)
	base := startGatewayAt(t, svc)

	res := postChat(t, base, svc.GetConfig().APIKey, `{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}]}`)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("401 刷新后重试应成功 = %d, want 200", res.StatusCode)
	}
	// 凭证文件应已写回新 token
	cred, err := LoadUpstreamCred(svc.AuthDir(), "workbuddy-u_rt.json")
	if err != nil {
		t.Fatalf("读回凭证失败: %v", err)
	}
	if cred.AccessToken != "at-refreshed" || cred.RefreshToken != "rt-new" {
		t.Fatalf("凭证未写回新 token: at=%s rt=%s", cred.AccessToken, cred.RefreshToken)
	}
	// 刷新重试成功后应清除 401 冷却
	if st := svc.Store().AccountState("u_rt"); st.CooldownUntil != "" {
		t.Fatalf("刷新重试成功后冷却应清除: %+v", st)
	}
}

func TestSchedulerKeepaliveRefreshesToken(t *testing.T) {
	svc := newTestService(t)
	// token 仅剩 1 小时（远小于 168h 临期窗口）→ 应触发真实刷新
	seedCredential(t, svc.AuthDir(), "u_ka", 1*time.Hour)
	up := fakeUpstreamRoutes(t, func(int, string) (int, string) { return http.StatusOK, fakeSSE200 })
	defer up.Close()
	pointUpstreamAt(t, svc, up)

	run := svc.Scheduler().RunFor(TaskKeepalive, "manual", nil)
	if run.Success != 1 {
		t.Fatalf("keepalive 应刷新成功: %+v", run)
	}
	cred, err := LoadUpstreamCred(svc.AuthDir(), "workbuddy-u_ka.json")
	if err != nil {
		t.Fatalf("读回凭证失败: %v", err)
	}
	if cred.AccessToken != "at-refreshed" || cred.ExpiresAtUnix == 0 {
		t.Fatalf("刷新后的凭证异常: %+v", cred)
	}
	if left := time.Until(time.Unix(cred.ExpiresAtUnix, 0)); left < 24*time.Hour {
		t.Fatalf("expiresIn=60d 应被采用，实际剩余 %s", left)
	}
}

func TestSchedulerKeepaliveSkipsFreshToken(t *testing.T) {
	svc := newTestService(t)
	seedCredential(t, svc.AuthDir(), "u_fresh", 30*24*time.Hour)
	calls := 0
	up := fakeUpstreamRoutes(t, func(int, string) (int, string) {
		calls++
		return http.StatusOK, fakeSSE200
	})
	defer up.Close()
	pointUpstreamAt(t, svc, up)

	run := svc.Scheduler().RunFor(TaskKeepalive, "manual", nil)
	if run.Success != 1 || calls != 0 {
		t.Fatalf("有效期充足时不应发起刷新: run=%+v refreshCalls=%d", run, calls)
	}
	if !strings.Contains(run.Details[0].Message, "未刷新") {
		t.Fatalf("应说明本次未刷新: %+v", run.Details[0])
	}
}

func TestSchedulerCheckinReal(t *testing.T) {
	svc := newTestService(t)
	seedCredential(t, svc.AuthDir(), "u_ck", 30*24*time.Hour)
	up := fakeUpstreamRoutes(t, func(int, string) (int, string) { return http.StatusOK, fakeSSE200 })
	defer up.Close()
	pointUpstreamAt(t, svc, up)

	run := svc.Scheduler().RunFor(TaskCheckin, "manual", nil)
	if run.Success != 1 || run.Details[0].Status != TaskSuccess {
		t.Fatalf("签到应成功: %+v", run)
	}
	if !strings.Contains(run.Details[0].Message, "签到成功") {
		t.Fatalf("签到消息异常: %+v", run.Details[0])
	}
}

func TestSchedulerCheckinAlreadyDone(t *testing.T) {
	svc := newTestService(t)
	seedCredential(t, svc.AuthDir(), "u_ck2", 30*24*time.Hour)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":10001,"msg":"今天已签到"}`))
	}))
	defer up.Close()
	pointUpstreamAt(t, svc, up)

	run := svc.Scheduler().RunFor(TaskCheckin, "manual", nil)
	if run.Success != 1 {
		t.Fatalf("重复签到应记 success（已签到）: %+v", run)
	}
	if !strings.Contains(run.Details[0].Message, "已签到") {
		t.Fatalf("应如实报告已签到: %+v", run.Details[0])
	}
}

// ---- 猫猫旅行 / 连登奖励 / 余额刷新 / 会话粘性 / tool_calls 聚合 ----

// growthFake 可变的 growth 域伪上游（信封 {code,msg,data}）。
type growthFake struct {
	mu             sync.Mutex
	hasBuddy       bool
	adoptFail      bool // buddy/first 返回 400 门槛未达
	travel         TravelState
	redeemCalled   int
	lastRedeemTier string
}

func newGrowthFake(t *testing.T) (*growthFake, *httptest.Server) {
	t.Helper()
	g := &growthFake{}
	g.travel = TravelState{State: "idle"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		var data any
		switch {
		case r.URL.Path == "/activity/growth/buddy/info" && r.Method == http.MethodGet:
			if g.hasBuddy {
				data = map[string]any{"buddy": map[string]any{"id": 1, "name": "猫猫"}}
			} else {
				data = map[string]any{"buddy": nil}
			}
		case r.URL.Path == "/activity/growth/buddy/travel/status" && r.Method == http.MethodGet:
			data = g.travel
		case r.URL.Path == "/activity/growth/buddy/travel/depart" && r.Method == http.MethodPost:
			data = map[string]any{}
		case r.URL.Path == "/activity/growth/buddy/travel/claim" && r.Method == http.MethodPost:
			data = map[string]any{"reward_credit": g.travel.RewardCredit}
		case r.URL.Path == "/activity/growth/buddy/agreement" && r.Method == http.MethodPost:
			data = map[string]any{}
		case r.URL.Path == "/activity/growth/buddy/first" && r.Method == http.MethodPost:
			if g.adoptFail {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"code":400,"msg":"first_buddy task not completed yet"}`))
				return
			}
			data = map[string]any{}
		case r.URL.Path == "/activity/growth/streak" && r.Method == http.MethodGet:
			data = map[string]any{"streak": map[string]any{"days": 8, "redemption_status": map[string]any{
				"tier_7d_status": "available", "tier_14d_status": "locked", "tier_28d_status": "locked",
				"tiers": []any{map[string]any{"tier": "7d", "days": 7, "credit": 408}},
			}}}
		case r.URL.Path == "/activity/growth/redeem" && r.Method == http.MethodPost:
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			g.redeemCalled++
			if v, ok := body["tier"].(string); ok {
				g.lastRedeemTier = v
			}
			data = map[string]any{}
		case r.URL.Path == "/v2/billing/meter/daily-checkin" && r.Method == http.MethodPost:
			data = map[string]any{"credit": 8}
		case r.URL.Path == "/v2/billing/meter/get-user-resource" && r.Method == http.MethodPost:
			data = map[string]any{"Response": map[string]any{"Data": map[string]any{
				"Accounts": []any{
					map[string]any{"CapacityRemain": 300},
					map[string]any{"CapacityRemain": 200},
				},
			}}}
		default:
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(mustJSON(map[string]any{"code": 0, "msg": "ok", "data": data}))
	}))
	return g, srv
}

func seedGlobalCredential(t *testing.T, authDir, uid string) {
	t.Helper()
	body := []byte(`{"auth":{"accessToken":"at-` + uid + `","refreshToken":"rt-` + uid +
		`","expiresAt":` + strconv.FormatInt(time.Now().Add(30*24*time.Hour).Unix(), 10) +
		`,"domain":"workbuddy.ai","realm":"global"},"account":{"uid":"` + uid + `"}}`)
	if _, err := ImportCredential(authDir, "workbuddy-"+uid+".json", body); err != nil {
		t.Fatalf("写入凭证失败: %v", err)
	}
}

func TestSchedulerTravelAdoptGateDebounce(t *testing.T) {
	svc := newTestService(t)
	seedCredential(t, svc.AuthDir(), "u_tr1", 30*24*time.Hour)
	g, up := newGrowthFake(t)
	g.adoptFail = true // 无猫 + 对话量门槛未达
	defer up.Close()
	pointUpstreamAt(t, svc, up)

	run := svc.Scheduler().RunFor(TaskTravel, "manual", nil)
	if run.Skipped != 1 || !strings.Contains(run.Details[0].Message, "领养门槛") {
		t.Fatalf("门槛未达应 skipped: %+v", run)
	}
	// 同日第二次巡检：防抖生效，不再打上游
	run2 := svc.Scheduler().RunFor(TaskTravel, "manual", nil)
	if run2.Skipped != 1 || !strings.Contains(run2.Details[0].Message, "今日已试") {
		t.Fatalf("同日二次巡检应被防抖: %+v", run2)
	}
}

func TestSchedulerTravelAdoptAndDepartAndClaim(t *testing.T) {
	svc := newTestService(t)
	seedCredential(t, svc.AuthDir(), "u_tr2", 30*24*time.Hour)
	seedCredential(t, svc.AuthDir(), "u_tr3", 30*24*time.Hour)
	g, up := newGrowthFake(t)
	defer up.Close()
	pointUpstreamAt(t, svc, up)

	// 账号 1：无猫 + 门槛已过 → 领养成功
	run := svc.Scheduler().RunFor(TaskTravel, "manual", []string{"u_tr2"})
	if run.Success != 1 || !strings.Contains(run.Details[0].Message, "领养成功") {
		t.Fatalf("领养应成功: %+v", run)
	}
	// 账号 2：有猫 + 空闲 → 派出
	g.hasBuddy = true
	g.travel = TravelState{State: "idle"}
	run = svc.Scheduler().RunFor(TaskTravel, "manual", []string{"u_tr3"})
	if run.Success != 1 || !strings.Contains(run.Details[0].Message, "已派出") {
		t.Fatalf("空闲应派出: %+v", run)
	}
	// 到站 → 领奖
	g.travel = TravelState{State: "arrived", RecordID: 42, RewardCredit: 60}
	run = svc.Scheduler().RunFor(TaskTravel, "manual", []string{"u_tr3"})
	if run.Success != 1 || !strings.Contains(run.Details[0].Message, "领奖成功") {
		t.Fatalf("到站应领奖: %+v", run)
	}
}

func TestSchedulerTravelGlobalSkipped(t *testing.T) {
	svc := newTestService(t)
	seedGlobalCredential(t, svc.AuthDir(), "u_gl")
	calls := 0
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		http.NotFound(w, nil)
	}))
	defer up.Close()
	pointUpstreamAt(t, svc, up)

	run := svc.Scheduler().RunFor(TaskTravel, "manual", nil)
	if run.Skipped != 1 || !strings.Contains(run.Details[0].Message, "国际版") {
		t.Fatalf("global 账号旅行应跳过: %+v", run)
	}
	if calls != 0 {
		t.Fatalf("global 不应发起任何上游调用: %d", calls)
	}
}

func TestSchedulerCheckinRedeemsGrowthTier(t *testing.T) {
	svc := newTestService(t)
	seedCredential(t, svc.AuthDir(), "u_gr", 30*24*time.Hour)
	g, up := newGrowthFake(t)
	defer up.Close()
	pointUpstreamAt(t, svc, up)
	// 签到端点也走同一伪上游：补上 daily-checkin 应答
	svc.Scheduler().up.billingCN = up.URL

	run := svc.Scheduler().RunFor(TaskCheckin, "manual", nil)
	if run.Success != 1 {
		t.Fatalf("签到应成功: %+v", run)
	}
	if !strings.Contains(run.Details[0].Message, "连登奖励已兑换: 7d(+408分)") {
		t.Fatalf("应兑换 available 档位: %+v", run.Details[0])
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.lastRedeemTier != "7d" {
		t.Fatalf("redeem 请求档位异常: %s", g.lastRedeemTier)
	}
}

func TestSchedulerKeepaliveFetchesBalance(t *testing.T) {
	svc := newTestService(t)
	seedCredential(t, svc.AuthDir(), "u_bal", 30*24*time.Hour) // 有效期充足 → 不刷新只查余额
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v2/billing/meter/get-user-resource" {
			_, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":{"Response":{"Data":{"Accounts":[{"CapacityRemain":300},{"CapacityRemain":200}]}}}}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer up.Close()
	pointUpstreamAt(t, svc, up)

	run := svc.Scheduler().RunFor(TaskKeepalive, "manual", nil)
	if run.Success != 1 || !strings.Contains(run.Details[0].Message, "余额 500 分") {
		t.Fatalf("keepalive 应刷新余额: %+v", run)
	}
	st := svc.Store().AccountState("u_bal")
	if !st.CreditsKnown || st.Credits != 500 {
		t.Fatalf("余额应写入运行态: %+v", st)
	}
}

func TestGatewayStickySession(t *testing.T) {
	svc := newTestService(t)
	seedCredential(t, svc.AuthDir(), "s1", 30*24*time.Hour)
	seedCredential(t, svc.AuthDir(), "s2", 30*24*time.Hour)
	seedCredential(t, svc.AuthDir(), "s3", 30*24*time.Hour)
	var seen []string
	var mu sync.Mutex
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer up.Close()
	pointUpstreamAt(t, svc, up)
	base := startGatewayAt(t, svc)

	body := `{"model":"glm-5.2","session_id":"sess-fixed","messages":[{"role":"user","content":"hi"}]}`
	for i := 0; i < 4; i++ {
		res := postChat(t, base, svc.GetConfig().APIKey, body)
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("第 %d 次请求失败: %d", i, res.StatusCode)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 4 {
		t.Fatalf("上游应收到 4 次请求: %d", len(seen))
	}
	for _, tok := range seen {
		if tok != seen[0] {
			t.Fatalf("同一会话应固定同一账号: %v", seen)
		}
	}
}

func TestAggregateStreamToolCalls(t *testing.T) {
	rc := io.NopCloser(strings.NewReader(
		"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"get_weather\",\"arguments\":\"{\\\"ci\"}}]}}]}\n\n" +
			"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"ty\\\":\\\"杭州\\\"}\"}}]}}]}\n\n" +
			"data: {\"choices\":[{\"delta\":{}}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5,\"total_tokens\":15}}\n\n" +
			"data: [DONE]\n\n"))
	content, _, p, c, _, calls, _, _, err := aggregateStream(rc)
	if err != nil {
		t.Fatalf("聚合失败: %v", err)
	}
	if content != "" || p != 10 || c != 5 {
		t.Fatalf("聚合结果异常: content=%q p=%d c=%d", content, p, c)
	}
	if len(calls) != 1 {
		t.Fatalf("应聚合出 1 个 tool_call: %v", calls)
	}
	tc := calls[0].(map[string]any)
	if tc["id"] != "call_1" || tc["type"] != "function" {
		t.Fatalf("tool_call 头部异常: %v", tc)
	}
	fn := tc["function"].(map[string]any)
	if fn["name"] != "get_weather" || fn["arguments"] != `{"city":"杭州"}` {
		t.Fatalf("tool_call 参数重组异常: %v", fn)
	}
}

// ---- 连登抽奖 / global trial / prompt_cache_key / thinking / 余额细分 / 动态模型目录 ----

// growthEnvelope 把业务 data 包成上游 {code,msg,data} 信封写入响应。
func growthEnvelope(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "ok", "data": data})
}

func TestSchedulerLotteryDrawsAllChances(t *testing.T) {
	svc := newTestService(t)
	seedCredential(t, svc.AuthDir(), "u_lot", 30*24*time.Hour)
	balance := 2
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/billing/meter/daily-checkin":
			growthEnvelope(w, map[string]any{})
		case "/activity/growth/streak":
			// 全档 locked：不触发 redeem，只走抽奖
			growthEnvelope(w, map[string]any{"streak": map[string]any{
				"days": 3, "redemption_status": map[string]any{"tier_7d_status": "locked"}}})
		case "/activity/growth/lottery/chances":
			growthEnvelope(w, map[string]any{"balance": balance})
		case "/activity/growth/lottery/draw":
			if balance <= 0 {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"code":400,"msg":"insufficient lottery chance balance"}`))
				return
			}
			balance--
			growthEnvelope(w, map[string]any{"prize_code": "c10", "prize_name": "10积分",
				"prize_type": "credit", "credit_amount": 10})
		default:
			http.NotFound(w, r)
		}
	}))
	defer up.Close()
	pointUpstreamAt(t, svc, up)

	run := svc.Scheduler().RunFor(TaskCheckin, "manual", nil)
	if run.Success != 1 {
		t.Fatalf("签到应成功: %+v", run)
	}
	if !strings.Contains(run.Details[0].Message, "抽奖 2 次") ||
		!strings.Contains(run.Details[0].Message, "10积分(+10分)") {
		t.Fatalf("应抽完 2 次并报奖品: %+v", run.Details[0])
	}
}

func TestSchedulerTrialForGlobalAccount(t *testing.T) {
	svc := newTestService(t)
	seedGlobalCredential(t, svc.AuthDir(), "u_trial")
	trialCalls := 0
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/billing/ide/trial" && r.Method == http.MethodPost {
			trialCalls++
			if trialCalls == 1 {
				growthEnvelope(w, map[string]any{})
				return
			}
			// 第二次：幂等码 14051（HTTP 200 + 业务 code）
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":14051,"msg":"already claimed"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer up.Close()
	pointUpstreamAt(t, svc, up)

	run := svc.Scheduler().RunFor(TaskCheckin, "manual", nil)
	if run.Success != 1 || !strings.Contains(run.Details[0].Message, "trial 加油包领取成功") {
		t.Fatalf("global 首次应领取成功: %+v", run)
	}
	run = svc.Scheduler().RunFor(TaskCheckin, "manual", nil)
	if run.Success != 1 || !strings.Contains(run.Details[0].Message, "已领取过") {
		t.Fatalf("第二次应幂等报已领取: %+v", run)
	}
}

func TestInjectPromptCacheKey(t *testing.T) {
	base := upstreamChatRequest(chatRequest{Model: "glm-5.2",
		Messages: []ChatMessage{{Role: "user", Content: "hi"}}})
	k1 := string(injectPromptCacheKey(base, "uid-abcdef123456", "sess-1"))
	if !strings.Contains(k1, `"prompt_cache_key":"wbd-uid-abcd-`) {
		t.Fatalf("键格式异常: %s", k1)
	}
	// 同账号同会话稳定
	if got := string(injectPromptCacheKey(base, "uid-abcdef123456", "sess-1")); got != k1 {
		t.Fatalf("同账号同会话应生成相同键:\n%s\n%s", k1, got)
	}
	// 跨账号隔离
	if got := string(injectPromptCacheKey(base, "other-uid-99999", "sess-1")); got == k1 {
		t.Fatal("不同账号不应生成相同键")
	}
	// 同账号不同会话
	if got := string(injectPromptCacheKey(base, "uid-abcdef123456", "sess-2")); got == k1 {
		t.Fatal("不同会话不应生成相同键")
	}
	// 客户端显式带 key → 不覆盖
	withKey := []byte(`{"model":"m","prompt_cache_key":"client-key"}`)
	if got := string(injectPromptCacheKey(withKey, "u", "s")); got != string(withKey) {
		t.Fatalf("显式 key 不应被覆盖: %s", got)
	}
	// body 内 conversation_id 优先于网关 session_id
	withConv := []byte(`{"model":"m","conversation_id":"conv-9"}`)
	a1 := string(injectPromptCacheKey(withConv, "uid-abcdef123456", "sess-1"))
	a2 := string(injectPromptCacheKey(withConv, "uid-abcdef123456", "sess-2"))
	if a1 != a2 {
		t.Fatal("body 的 conversation_id 应优先于网关 session_id")
	}
}

func TestInjectThinking(t *testing.T) {
	// deepseek：注入 enabled + 默认档 high
	body := map[string]any{"model": "deepseek-v4-flash", "messages": []any{}}
	injectThinking(body, "")
	if th, _ := body["thinking"].(map[string]any); th["type"] != "enabled" {
		t.Fatalf("deepseek 应注入 thinking.enabled: %v", body)
	}
	if body["reasoning_effort"] != "high" {
		t.Fatalf("应补默认档 high: %v", body["reasoning_effort"])
	}
	// 已有 effort → 不覆盖，但开关照开
	body = map[string]any{"model": "DeepSeek-V4-Pro", "reasoning_effort": "low"}
	injectThinking(body, "medium")
	if body["reasoning_effort"] != "low" {
		t.Fatalf("显式 effort 不应被覆盖: %v", body["reasoning_effort"])
	}
	if th, _ := body["thinking"].(map[string]any); th["type"] != "enabled" {
		t.Fatalf("无 thinking 时应注入 enabled: %v", body)
	}
	// 显式 disabled → 删 effort
	body = map[string]any{"model": "deepseek-v4", "thinking": map[string]any{"type": "disabled"},
		"reasoning_effort": "high"}
	injectThinking(body, "")
	if _, ok := body["reasoning_effort"]; ok {
		t.Fatalf("disabled 应删除 reasoning_effort: %v", body)
	}
	// 非 deepseek → 零改动
	body = map[string]any{"model": "glm-5.2"}
	injectThinking(body, "")
	if _, ok := body["thinking"]; ok {
		t.Fatalf("非 deepseek 不应注入: %v", body)
	}
	// upstreamChatRequest 全链路：deepseek 出站带 thinking + effort
	raw := upstreamChatRequest(chatRequest{Model: "deepseek-v4-flash",
		Messages: []ChatMessage{{Role: "user", Content: "hi"}}})
	var obj map[string]any
	if json.Unmarshal(raw, &obj) != nil {
		t.Fatal("请求体解析失败")
	}
	if th, _ := obj["thinking"].(map[string]any); th["type"] != "enabled" || obj["reasoning_effort"] != "high" {
		t.Fatalf("出站请求应带 thinking 开关与默认档: %v", obj)
	}
}

func TestFetchBalanceDetailExpiringBucket(t *testing.T) {
	svc := newTestService(t)
	seedCredential(t, svc.AuthDir(), "u_bd", 30*24*time.Hour)
	soonEnd := time.Now().In(pkgEndZone).Add(time.Hour).Format(packageEndLayout)
	farEnd := time.Now().In(pkgEndZone).Add(30 * 24 * time.Hour).Format(packageEndLayout)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		growthEnvelope(w, map[string]any{"Response": map[string]any{"Data": map[string]any{
			"Accounts": []any{
				map[string]any{"CapacityRemain": 300, "CapacityUsed": 100, "CapacitySize": 400,
					"CycleEndTime": soonEnd},
				map[string]any{"CapacityRemain": 200, "CapacityUsed": 50, "CapacitySize": 250,
					"CycleEndTime": farEnd},
				map[string]any{"CapacityRemain": 40}, // 无到期时间 → 稳定桶
			}}}})
	}))
	defer up.Close()
	pointUpstreamAt(t, svc, up)

	cred, err := LoadUpstreamCred(svc.AuthDir(), "workbuddy-u_bd.json")
	if err != nil {
		t.Fatal(err)
	}
	d, err := svc.Scheduler().up.FetchBalanceDetail(cred, 72*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if d.Remain != 540 || d.Used != 150 || d.Size != 650 || d.Packs != 3 {
		t.Fatalf("聚合异常: %+v", d)
	}
	if d.Expiring != 300 {
		t.Fatalf("临期分桶异常: %+v", d)
	}
}

func TestFetchModelsMergedIntoStore(t *testing.T) {
	svc := newTestService(t)
	seedCredential(t, svc.AuthDir(), "u_models", 30*24*time.Hour)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v3/config" && r.Method == http.MethodGet {
			growthEnvelope(w, map[string]any{"models": []any{
				map[string]any{"id": "glm-5.9", "reasoning": map[string]any{"defaultEffort": "medium"}},
				map[string]any{"id": "nes-embed-1"},                                // 非对话 → 剔除
				map[string]any{"id": "tiny-m", "maxOutputTokens": 128},             // tiny → 剔除
				map[string]any{"id": "img-gen", "tags": []string{"text-to-image"}}, // 图片 → 剔除
				map[string]any{"name": "glm-5.8-alias", "disabled": true},          // 禁用 → 剔除
			}})
			return
		}
		http.NotFound(w, r)
	}))
	defer up.Close()
	pointUpstreamAt(t, svc, up)

	cred, err := LoadUpstreamCred(svc.AuthDir(), "workbuddy-u_models.json")
	if err != nil {
		t.Fatal(err)
	}
	infos, err := svc.Scheduler().up.FetchModels(cred)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 || infos[0].ID != "glm-5.9" || infos[0].DefaultEffort != "medium" {
		t.Fatalf("目录解析/过滤异常: %+v", infos)
	}
	svc.Store().MergeUpstreamModels(infos)
	found := false
	for _, m := range ListModels(svc.Store()) {
		if m.ID == "glm-5.9" {
			found = true
			if m.Source != "upstream" {
				t.Fatalf("动态条目来源应为 upstream: %+v", m)
			}
		}
	}
	if !found {
		t.Fatal("动态模型应出现在清单中")
	}
	if svc.Store().DefaultEffort("glm-5.9") != "medium" {
		t.Fatalf("默认档应入缓存: %q", svc.Store().DefaultEffort("glm-5.9"))
	}
	// 窄表形态：data 为字符串数组
	up2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":["glm-x1","glm-x2"]}`))
	}))
	defer up2.Close()
	svc.Scheduler().up.baseCN = up2.URL
	infos, err = svc.Scheduler().up.FetchModels(cred)
	if err != nil || len(infos) != 2 || infos[0].ID != "glm-x1" {
		t.Fatalf("窄表解析异常: %+v err=%v", infos, err)
	}
}

// ---- embeddings / images：管线完整 + 明确的「上游未开放」语义 ----

func postEndpoint(t *testing.T, base, path, key, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, base+path, strings.NewReader(body))
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	return resp
}

func TestGatewayEmbeddingsImagesUnsupported(t *testing.T) {
	svc := newTestService(t)
	base := startTestGateway(t, svc)
	key := svc.GetConfig().APIKey

	for _, path := range []string{"/v1/embeddings", "/v1/images/generations"} {
		// 未鉴权 → 401（鉴权管线完整生效）
		res := postEndpoint(t, base, path, "", `{"model":"text-embedding-x"}`)
		res.Body.Close()
		if res.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s 未鉴权状态码 = %d, want 401", path, res.StatusCode)
		}
		// 鉴权通过 → 501 + OpenAI 兼容结构化错误
		res = postEndpoint(t, base, path, key, `{"model":"text-embedding-x"}`)
		var payload struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.NewDecoder(res.Body).Decode(&payload)
		res.Body.Close()
		if res.StatusCode != http.StatusNotImplemented {
			t.Fatalf("%s 状态码 = %d, want 501", path, res.StatusCode)
		}
		if payload.Error.Code != "upstream_not_supported" {
			t.Fatalf("%s 错误码 = %q, want upstream_not_supported", path, payload.Error.Code)
		}
	}
	// 两条路径 ×（401 + 501）都应落请求日志
	logs := svc.Store().ListRequestLogs()
	if len(logs) != 4 {
		t.Fatalf("日志条数 = %d, want 4", len(logs))
	}
}

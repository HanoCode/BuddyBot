package ferry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testConfig(t *testing.T) Config {
	t.Helper()
	cfg := Config{DataDir: t.TempDir()}
	return cfg.withDefaults()
}

// 免费由价格判定，不由名字判定
func TestFreeModelsFilterByPrice(t *testing.T) {
	payload := []byte(`{
	  "all": [
	    {
	      "id": "other",
	      "models": { "sneaky-free": { "cost": { "input": 0, "output": 0 } } }
	    },
	    {
	      "id": "opencode",
	      "models": {
	        "big-pickle": {
	          "name": "Big Pickle",
	          "cost": { "input": 0, "output": 0, "cache": { "read": 0, "write": 0 } },
	          "capabilities": { "input": { "image": true }, "output": { "text": true }, "toolcall": true, "reasoning": true },
	          "limit": { "context": 200000, "output": 32000 },
	          "variants": { "fast": { "disabled": false, "reasoningEffort": "low" } }
	        },
	        "paid-model": {
	          "cost": { "input": 0.1, "output": 0.2, "cache": { "read": 0, "write": 0 } },
	          "capabilities": { "output": { "text": true } }
	        },
	        "cache-costs": {
	          "cost": { "input": 0, "output": 0, "cache": { "read": 0.01, "write": 0 } },
	          "capabilities": { "output": { "text": true } }
	        },
	        "retired": {
	          "cost": { "input": 0, "output": 0, "cache": { "read": 0, "write": 0 } },
	          "status": "deprecated",
	          "capabilities": { "output": { "text": true } }
	        },
	        "no-text": {
	          "cost": { "input": 0, "output": 0, "cache": { "read": 0, "write": 0 } },
	          "capabilities": { "output": { "text": false } }
	        },
	        "named-free-but-paid": {
	          "name": "something-free",
	          "cost": { "input": 1, "output": 0, "cache": { "read": 0, "write": 0 } },
	          "capabilities": { "output": { "text": true } }
	        }
	      }
	    }
	  ]
	}`)
	models, err := freeModels(payload)
	if err != nil {
		t.Fatalf("解析模型目录失败: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("只应留下 1 个免费模型，实际 %d: %+v", len(models), models)
	}
	m := models[0]
	if m.ID != "opencode/big-pickle" || m.Name != "Big Pickle" {
		t.Fatalf("模型 id/name 不符: %+v", m)
	}
	if !m.Toolcall || !m.Reasoning || !m.Images || m.Context != 200000 || m.Output != 32000 {
		t.Fatalf("能力字段不符: %+v", m)
	}
	if m.Variants["fast"].ReasoningEffort != "low" {
		t.Fatalf("推理档位未保留: %+v", m.Variants)
	}
	if _, err := freeModels([]byte(`{"all":[]}`)); err == nil {
		t.Fatal("缺少 opencode provider 应报错")
	}
}

func TestPrepareRejectsAndAccepts(t *testing.T) {
	models := []Model{{
		ID: "opencode/big-pickle", Name: "Big Pickle", Toolcall: true, Reasoning: true,
		Variants: map[string]VariantOption{"fast": {ReasoningEffort: "low"}},
	}}
	body := []byte(`{
	  "model": "opencode/big-pickle",
	  "messages": [{"role": "user", "content": "hi"}],
	  "tools": [{"type": "function", "function": {"name": "Read", "parameters": {"type": "object", "properties": {"file_path": {"type": "string"}}, "required": ["file_path"]}}}],
	  "reasoning_effort": "low"
	}`)
	request, err := prepare(body, models)
	if err != nil {
		t.Fatalf("prepare 失败: %v", err)
	}
	if request.Variant != "fast" || request.Choice != "auto" || !request.Parallel {
		t.Fatalf("档位/工具选择不符: %+v", request)
	}
	if !strings.Contains(request.System, `"name":"Read"`) {
		t.Fatalf("系统提示里应带上本次请求的工具表: %s", request.System)
	}

	// 未知模型 / 不支持的档位 / 非法 tool_choice
	if _, err := prepare([]byte(`{"model":"nope","messages":[{"role":"user","content":"x"}]}`), models); err == nil || err.Code != "model_not_found" {
		t.Fatalf("未知模型应返回 model_not_found，实际 %+v", err)
	}
	if _, err := prepare([]byte(`{"model":"opencode/big-pickle","messages":[{"role":"user","content":"x"}],"reasoning_effort":"xhigh"}`), models); err == nil || err.Code != "unsupported_reasoning_effort" {
		t.Fatalf("不支持的档位应被拒，实际 %+v", err)
	}
	if _, err := prepare([]byte(`{"model":"opencode/big-pickle","messages":[{"role":"user","content":"x"}],"tool_choice":"whatever"}`), models); err == nil {
		t.Fatal("非法 tool_choice 应被拒")
	}
	// 图片消息：目录未声明图片能力时必须拒（绝不把 data URL 变成宿主机读文件动作）
	imageBody := []byte(`{"model":"opencode/big-pickle","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]}]}`)
	if _, err := prepare(imageBody, models); err == nil || err.Code != "unsupported_content" {
		t.Fatalf("未声明图片能力时应拒，实际 %+v", err)
	}
}

func TestDecodeEnvelope(t *testing.T) {
	tools := []Tool{{Type: "function", Function: ToolFunction{
		Name: "Read", Parameters: map[string]any{
			"type": "object", "properties": map[string]any{"file_path": map[string]any{"type": "string"}},
			"required": []string{"file_path"},
		},
	}}}
	request := &Request{Model: Model{ID: "opencode/x"}, Tools: tools, Choice: "auto", Parallel: true}

	message, err := decode(`{"content":"ok","calls":[{"name":"Read","arguments":{"file_path":"/tmp/a"}}]}`, request)
	if err != nil {
		t.Fatalf("合法信封应通过: %v", err)
	}
	calls, _ := message["tool_calls"].([]any)
	if len(calls) != 1 {
		t.Fatalf("应产出 1 个工具调用: %+v", message)
	}

	// 代码围栏 + OpenAI 形状的 tool_calls 等价写法 + 参数字符串
	message, err = decode("```json\n{\"content\":\"\",\"tool_calls\":[{\"type\":\"function\",\"function\":{\"name\":\"Read\",\"arguments\":\"{\\\"file_path\\\":\\\"/tmp/b\\\"}\"}}]}\n```", request)
	if err != nil {
		t.Fatalf("tool_calls 等价写法应被接受: %v", err)
	}
	if len(message["tool_calls"].([]any)) != 1 {
		t.Fatalf("等价写法未转成调用: %+v", message)
	}

	// 语义不明的形状 / 未列出的工具 / 纯文本（无 calls 也无 content）
	if _, err := decode(`{"content":"x","calls":[],"tool_calls":[]}`, request); err == nil {
		t.Fatal("同时给出 calls 与 tool_calls 应报错")
	}
	if _, err := decode(`{"content":"","calls":[{"name":"Bash","arguments":{}}]}`, request); err == nil || err.Code != "invalid_tool_call" {
		t.Fatalf("未列出的工具应被拒，实际 %+v", err)
	}
	if _, err := decode(`hello`, request); err == nil || err.Code != "invalid_model_output" {
		t.Fatalf("非 JSON 应报 invalid_model_output，实际 %+v", err)
	}

	// tool_choice: none 时返回调用必须被拒（静默丢掉动作比报错更危险）
	noneRequest := &Request{Model: request.Model, Tools: nil, Choice: "none"}
	if _, err := decode(`{"content":"","calls":[{"name":"Read","arguments":{"file_path":"/x"}}]}`, noneRequest); err == nil {
		t.Fatal("tool_choice:none 下的调用应被拒")
	}
	// 纯文本答复在 none 下合法
	if _, err := decode(`{"content":"答案","calls":[]}`, noneRequest); err != nil {
		t.Fatalf("纯文本答复应通过: %v", err)
	}
	// 被禁止并行时返回多个调用必须被拒
	serial := &Request{Model: request.Model, Tools: tools, Choice: "auto", Parallel: false}
	if _, err := decode(`{"content":"","calls":[{"name":"Read","arguments":{"file_path":"/a"}},{"name":"Read","arguments":{"file_path":"/b"}}]}`, serial); err == nil {
		t.Fatal("禁止并行时多调用应被拒")
	}
}

func TestBuildHandoffMapsBlockedNativeAction(t *testing.T) {
	tools := []Tool{{Type: "function", Function: ToolFunction{
		Name: "Read", Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"file_path": map[string]any{"type": "string"}, "offset": map[string]any{"type": "number"},
			},
			"required": []string{"file_path"},
		},
	}}}
	handoff := buildHandoff("read", map[string]any{"filePath": "/tmp/a", "offset": 10}, tools)
	if handoff == nil || handoff.Name != "Read" {
		t.Fatalf("read → Read 应转交成功: %+v", handoff)
	}
	if handoff.Arguments["file_path"] != "/tmp/a" || handoff.Arguments["offset"] != 10 {
		t.Fatalf("参数未按目标 schema 改名: %+v", handoff.Arguments)
	}
	// 必填项凑不齐就不转交（退回给模型一条带原因的纠正反馈）
	if buildHandoff("read", map[string]any{"offset": 1}, tools) != nil {
		t.Fatal("缺少必填参数时不应转交")
	}
	// 映射表里没有的本机动作一律不转交
	if buildHandoff("todowrite", map[string]any{"filePath": "/tmp/a"}, tools) != nil {
		t.Fatal("未知本机动作不应转交")
	}
	// 目标工具不存在时也不转交
	if buildHandoff("read", map[string]any{"filePath": "/tmp/a"}, nil) != nil {
		t.Fatal("目标工具不存在时不应转交")
	}
	// 审批载荷补参：工具 part 还没拿到 input 时用审批元数据补齐
	input := handoffInput(nil, map[string]any{"filepath": "/tmp/b"})
	if input["filePath"] != "/tmp/b" {
		t.Fatalf("审批元数据未补齐参数: %+v", input)
	}
}

func TestValidateActionRefusesToWidenScope(t *testing.T) {
	tools := []Tool{{Type: "function", Function: ToolFunction{
		Name: "Write", Parameters: map[string]any{
			"type": "object", "properties": map[string]any{
				"file_path": map[string]any{"type": "string"}, "content": map[string]any{"type": "string"},
			},
			"required": []string{"file_path", "content"},
		},
	}}}
	if _, err := validateAction(map[string]any{"name": "Write", "arguments": map[string]any{"file_path": "/a", "content": "x"}}, tools); err != nil {
		t.Fatalf("合规翻译应通过: %v", err)
	}
	if _, err := validateAction(map[string]any{"name": "Write", "arguments": map[string]any{"file_path": "/a", "content": "x", "extra": 1}}, tools); err == nil {
		t.Fatal("接收方未声明的参数应被拒")
	}
	if _, err := validateAction(map[string]any{"name": "Write", "arguments": map[string]any{"file_path": "/a"}}, tools); err == nil {
		t.Fatal("缺必填参数应被拒")
	}
	if _, err := validateAction(map[string]any{"name": "Bash", "arguments": map[string]any{}}, tools); err == nil {
		t.Fatal("接收方没提供的工具应被拒")
	}
}

// required 在两条来源上形状不同：客户端 JSON 解出来是 []any，本项目自建的工具（探测请求）是 []string。
// 曾经只认 []any，导致内部构造的必填项被静默跳过 —— 于是「缺参数也放行」。
func TestRequiredSatisfiedAcceptsAllShapes(t *testing.T) {
	cases := []struct {
		name     string
		required any
		want     bool
	}{
		{"any 缺参", []any{"file_path"}, false},
		{"any 齐参", []any{"file_path"}, true},
		{"string 缺参", []string{"file_path"}, false},
		{"string 齐参", []string{"file_path"}, true},
		{"裸字符串缺参", "file_path", false},
		{"裸字符串齐参", "file_path", true},
		{"无 required", nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			parameters := map[string]any{"type": "object"}
			if tc.required != nil {
				parameters["required"] = tc.required
			}
			args := map[string]any{}
			if strings.HasSuffix(tc.name, "齐参") {
				args["file_path"] = "/tmp/a"
			}
			if got := requiredSatisfied(parameters, args); got != tc.want {
				t.Fatalf("requiredSatisfied(%v) = %v，应为 %v", tc.required, got, tc.want)
			}
		})
	}
	// 空字符串等同于缺失：模型给个空占位不算满足必填
	if requiredSatisfied(map[string]any{"required": []any{"file_path"}}, map[string]any{"file_path": ""}) {
		t.Fatal("空字符串不应视为满足必填")
	}
}

func TestJudgeProbe(t *testing.T) {
	token := "abc123"
	ok := map[string]any{"choices": []any{map[string]any{"message": map[string]any{
		"tool_calls": []any{map[string]any{"function": map[string]any{
			"name": "Read", "arguments": `{"file_path":"/external/probe-abc123.txt"}`,
		}}},
	}}}}
	if err := judgeProbe(ok, token); err != nil {
		t.Fatalf("正确动作应通过: %v", err)
	}
	textOnly := map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": "done"}}}}
	if err := judgeProbe(textOnly, token); err == nil || err.Code != "no_action" {
		t.Fatalf("只回文本应报 no_action，实际 %+v", err)
	}
	wrong := map[string]any{"choices": []any{map[string]any{"message": map[string]any{
		"tool_calls": []any{map[string]any{"function": map[string]any{
			"name": "Bash", "arguments": `{"command":"ls"}`,
		}}},
	}}}}
	if err := judgeProbe(wrong, token); err == nil || err.Code != "probe_mismatch" {
		t.Fatalf("动作不符应报 probe_mismatch，实际 %+v", err)
	}
	if !retryableProbe["no_action"] || !retryableProbe["probe_mismatch"] {
		t.Fatal("语义没命中应可重试")
	}
	if formatUnsupported(&BridgeError{Code: "invalid_tool_call"}) != true {
		t.Fatal("格式不支持应走仅对话降级")
	}
	if formatUnsupported(&BridgeError{Code: "timeout", Message: "Model probe timed out"}) {
		t.Fatal("超时不是格式问题")
	}
}

func TestProbeCacheTTLAndFingerprint(t *testing.T) {
	cfg := testConfig(t)
	cache := newProbeCache(filepath.Join(cfg.DataDir, "probe-cache.json"), cfg, t.Logf)
	model := Model{ID: "opencode/x", Toolcall: true, Context: 1000, Output: 100}
	cache.record(model, cacheEntry{OK: true, At: time.Now().UnixMilli()})
	if cache.fresh(model) == nil {
		t.Fatal("新鲜的成功判定应可用")
	}
	// 能力声明变了，旧判定作废
	changed := model
	changed.Context = 2000
	if cache.fresh(changed) != nil {
		t.Fatal("能力指纹变化后判定应失效")
	}
	// 过期
	cfg.OKTTL = time.Millisecond
	stale := newProbeCache(filepath.Join(cfg.DataDir, "stale.json"), cfg, t.Logf)
	stale.record(model, cacheEntry{OK: true, At: time.Now().Add(-time.Hour).UnixMilli()})
	if stale.fresh(model) != nil {
		t.Fatal("过期判定应失效")
	}
	// 跨重启复用：重新 load 同一份文件应拿到判定
	reloaded := newProbeCache(filepath.Join(cfg.DataDir, "probe-cache.json"), testConfig(t), t.Logf)
	if n := reloaded.load(); n != 1 || reloaded.fresh(model) == nil {
		t.Fatalf("判定缓存应跨重启复用，load=%d", n)
	}
	// 目录里已经没有的模型，判定要被清掉
	if dropped := reloaded.prune([]Model{{ID: "opencode/other"}}); dropped != 1 {
		t.Fatalf("应清掉 1 条失效判定，实际 %d", dropped)
	}
}

func TestClassifyVerdict(t *testing.T) {
	cases := []struct {
		ok      bool
		message string
		status  int
		want    string
	}{
		{true, "", 0, "available"},
		{false, "insufficient_quota: 额度不足", 429, "quota"},
		{false, "Too many requests", 429, "rate_limit"},
		{false, "forbidden", 403, "access"},
		{false, "Model probe timed out", 0, "timeout"},
		// 我们自己的探测预算用尽：backend.request 报的是 499「请求已取消」，
		// 按当时的负载归类（缓存 10 分钟），不能按模型结论缓存一小时
		{false, "请求已取消", 499, "timeout"},
		{false, "boom", 0, "error"},
	}
	for _, c := range cases {
		if got := classifyVerdict(c.ok, c.message, c.status); got != c.want {
			t.Fatalf("%q/%d 应归类为 %s，实际 %s", c.message, c.status, c.want, got)
		}
	}
}

// 失败的判定也必须按模型 ID 落位。曾经的写法是 verdictFromError 不带 model，
// 结果失败判定被写成空键（m.verdicts[""]），界面上那些模型永远显示「未检测」，
// 失败原因一并丢掉 —— 只有成功的模型显示得出判定。
func TestFailedProbeVerdictKeyedByModel(t *testing.T) {
	// 隔离服务一律 429：模拟「上游限流」这种最常见的失败
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"Too many requests"}`))
	}))
	defer server.Close()

	m := New(testConfig(t))
	m.mu.Lock()
	m.backend = newBackend(server.URL, "probe-password", nil)
	m.models = []Model{
		{ID: "opencode/big-pickle", Toolcall: true},
		{ID: "opencode/no-tools", Toolcall: false},
	}
	m.mu.Unlock()

	// ① 请求被上游拒绝 ② 目录明说没有工具能力（不花请求直接判失败）
	for _, model := range m.snapshotModels() {
		m.probeOneModel(model)
	}

	status := m.Status()
	if len(status.Models) != 2 {
		t.Fatalf("状态里应有 2 个模型，实际 %d", len(status.Models))
	}
	for _, view := range status.Models {
		if view.Verdict == nil {
			t.Fatalf("模型 %s 缺少判定（界面会显示「未检测」，失败原因也丢了）", view.ID)
		}
		if view.Verdict.Model != view.ID {
			t.Fatalf("判定挂在 %q 上，应为 %q", view.Verdict.Model, view.ID)
		}
		if view.Verdict.Category == "" {
			t.Fatalf("模型 %s 的判定没有归类", view.ID)
		}
		if view.Verdict.Error == "" {
			t.Fatalf("模型 %s 的判定没有留下失败原因", view.ID)
		}
		if view.Available || view.Selected {
			t.Fatalf("模型 %s 探测失败却标成可用/已选", view.ID)
		}
	}
	if _, leaked := m.verdicts[""]; leaked {
		t.Fatal("出现了空键判定：失败判定没带上模型 ID")
	}
	if _, leaked := m.validated[""]; leaked {
		t.Fatal("空模型 ID 不该进已验证集合")
	}
}

// verdictFromError 是失败判定的唯一入口，必须自己带上模型 ID
func TestVerdictFromErrorCarriesModel(t *testing.T) {
	verdict := verdictFromError("opencode/big-pickle", newBridgeError(429, "rate_limit", "Too many requests"))
	if verdict.Model != "opencode/big-pickle" {
		t.Fatalf("判定应带模型 ID，实际 %q", verdict.Model)
	}
	if verdict.OK || verdict.Category != "rate_limit" {
		t.Fatalf("判定内容不符: %+v", verdict)
	}
}

// 读到目录 != 检测完成。阶段报早了，界面就会显示「运行中」而列表里一片「未检测」。
func TestCatalogPhaseOnlyReadyWhenAllJudged(t *testing.T) {
	if phase, _ := catalogPhase(12, 12); phase != "reading" {
		t.Fatalf("模型全没判定时应停在 reading，实际 %s", phase)
	}
	if phase, _ := catalogPhase(12, 1); phase != "reading" {
		t.Fatalf("还有一个模型没判定也应停在 reading，实际 %s", phase)
	}
	if phase, msg := catalogPhase(12, 0); phase != "ready" || !strings.Contains(msg, "12") {
		t.Fatalf("全部判定完成才是 ready，实际 %s / %s", phase, msg)
	}
}

// 检测期间必须对外报 probing：前端就是靠它把轮询提到 1.5 秒的
func TestProbeAllPublishesProbingPhase(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release // 卡住第一次请求，好在「检测中」这一瞬间读取状态
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"Too many requests"}`))
	}))
	defer server.Close()

	m := New(testConfig(t))
	m.mu.Lock()
	m.backend = newBackend(server.URL, "probe-password", nil)
	m.models = []Model{{ID: "opencode/slow", Toolcall: true}}
	m.mu.Unlock()

	m.ProbeAll(true)
	status := m.Status()
	if !status.Probing {
		t.Fatal("检测已开始，Probing 应为 true")
	}
	if status.Phase != "probing" {
		t.Fatalf("检测期间阶段应为 probing，实际 %s", status.Phase)
	}
	if len(status.ProbePending) != 1 {
		t.Fatalf("待测清单应有 1 项，实际 %v", status.ProbePending)
	}

	close(release)
	deadline := time.Now().Add(20 * time.Second)
	for {
		status = m.Status()
		if !status.Probing {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("检测迟迟不结束")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if status.Phase != "ready" {
		t.Fatalf("检测结束后阶段应为 ready，实际 %s（%s）", status.Phase, status.Message)
	}
	if len(status.ProbePending) != 0 || len(status.ProbeActive) != 0 {
		t.Fatalf("结束后待测/进行中清单应清空: %v / %v", status.ProbeActive, status.ProbePending)
	}
}

// 探测预算用尽必须读作「超时（当时的负载）」，而不是「这个模型坏了（一小时结论）」
func TestProbeDeadlineReadsAsTimeout(t *testing.T) {
	expired, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	time.Sleep(2 * time.Millisecond)

	canceled := &BridgeError{Message: "请求已取消", Status: 499, Code: "canceled"}
	got := probeDeadlineError(expired, canceled)
	if got.Code != "timeout" || !strings.Contains(got.Message, "超时") {
		t.Fatalf("预算用尽应换成语义准确的超时，实际 %+v", got)
	}
	if category := classifyVerdict(false, got.Message, got.Status); category != "timeout" {
		t.Fatalf("应归类为 timeout（短 TTL），实际 %s", category)
	}

	// 预算没用尽时原样返回，不能把上游真实的错误改写掉
	alive, cancel2 := context.WithTimeout(context.Background(), time.Minute)
	defer cancel2()
	if passthrough := probeDeadlineError(alive, canceled); passthrough != canceled {
		t.Fatal("预算没用尽就不该改写上游错误")
	}
	if probeDeadlineError(alive, nil) != nil {
		t.Fatal("没有错误时应保持 nil")
	}
}

func TestParseScutilProxy(t *testing.T) {
	text := "<dictionary> {\n" +
		"  HTTPEnable : 1\n  HTTPPort : 7890\n  HTTPProxy : 127.0.0.1\n" +
		"  HTTPSEnable : 1\n  HTTPSPort : 7890\n  HTTPSProxy : 127.0.0.1\n" +
		"  SOCKSEnable : 0\n}\n"
	env, err := ParseScutilProxy(text)
	if err != nil {
		t.Fatalf("解析系统代理失败: %v", err)
	}
	if env["HTTPS_PROXY"] != "http://127.0.0.1:7890" || env["HTTP_PROXY"] != "http://127.0.0.1:7890" {
		t.Fatalf("代理环境不符: %+v", env)
	}
	if env["NO_PROXY"] != "localhost,127.0.0.1,::1" {
		t.Fatalf("本机服务必须绕过代理: %+v", env)
	}
	// 只开 SOCKS / PAC 时必须报错，而不是猜一个地址去连
	if _, err := ParseScutilProxy("<dictionary> {\n  HTTPSEnable : 0\n}\n"); err == nil {
		t.Fatal("没有 HTTPS 代理时应报错")
	}
	if env, err := SystemProxyEnv(false); err != nil || env["HTTPS_PROXY"] != "" {
		t.Fatalf("关闭代理时不应下发任何代理地址: %+v %v", env, err)
	}
	// Windows 的两种 ProxyServer 写法
	if env, err := ParseWindowsProxy(true, "http=10.0.0.1:8080;https=10.0.0.2:8080"); err != nil || env["HTTPS_PROXY"] != "http://10.0.0.2:8080" {
		t.Fatalf("Windows 分段写法解析失败: %+v %v", env, err)
	}
	if env, err := ParseWindowsProxy(true, "10.0.0.3:3128"); err != nil || env["HTTPS_PROXY"] != "http://10.0.0.3:3128" {
		t.Fatalf("Windows 单值写法解析失败: %+v %v", env, err)
	}
	if _, err := ParseWindowsProxy(false, ""); err == nil {
		t.Fatal("未启用系统代理时应报错")
	}
}

func TestRuntimePackageAndMetaValidation(t *testing.T) {
	pkg, err := runtimePackage()
	if err != nil {
		t.Fatalf("取运行时包名失败: %v", err)
	}
	if !strings.HasPrefix(pkg.Name, "opencode-") || pkg.Binary == "" {
		t.Fatalf("包名不符: %+v", pkg)
	}
	registries := []string{registryOfficial, registryMirror}
	good := installMeta{Name: pkg.Name, Version: "1.2.3"}
	good.Dist.Tarball = registryOfficial + "/" + pkg.Name + "/-/" + pkg.Name + "-1.2.3.tgz"
	good.Dist.Integrity = "sha512-abc"
	if !good.valid(pkg, registries) {
		t.Fatal("合法安装信息应通过")
	}
	bad := good
	bad.Dist.Tarball = "https://evil.example/" + pkg.Name + "/-/" + pkg.Name + "-1.2.3.tgz"
	if bad.valid(pkg, registries) {
		t.Fatal("非官方源的 tarball 必须被拒")
	}
	bad2 := good
	bad2.Version = "not-a-version"
	if bad2.valid(pkg, registries) {
		t.Fatal("非法版本号必须被拒")
	}
	bad3 := good
	bad3.Dist.Integrity = "md5-abc"
	if bad3.valid(pkg, registries) {
		t.Fatal("非 sha512 校验值必须被拒")
	}
	// 版本比较：本机不比官方旧就直接用
	if compareRuntimeVersions("1.19.2", "1.19.1") <= 0 || compareRuntimeVersions("1.19.2", "1.19.2") != 0 {
		t.Fatal("版本比较不正确")
	}
	if !isLauncherScript("/x/opencode.cmd") || isLauncherScript("/x/opencode") {
		t.Fatal("启动脚本不能当运行时二进制")
	}
}

func TestEnvelopeSchemaFollowsRequestTools(t *testing.T) {
	tools := []Tool{{Type: "function", Function: ToolFunction{
		Name: "Read", Parameters: map[string]any{"type": "object", "properties": map[string]any{"file_path": map[string]any{"type": "string"}}},
	}}}
	schema := envelopeSchema(&Request{Tools: tools, Choice: "auto", Parallel: false})
	calls, _ := schema["properties"].(map[string]any)["calls"].(map[string]any)
	if calls["maxItems"] != 1 {
		t.Fatalf("禁止并行时数组上限应为 1: %+v", calls)
	}
	items, _ := calls["items"].(map[string]any)
	if _, ok := items["anyOf"]; !ok {
		t.Fatalf("调用数组的 schema 应由本次请求的工具表生成: %+v", calls)
	}
	// choice=none 时数组上限归零，模型没有表达动作的余地
	noneSchema := envelopeSchema(&Request{Tools: tools, Choice: "none", Parallel: true})
	noneCalls, _ := noneSchema["properties"].(map[string]any)["calls"].(map[string]any)
	if noneCalls["maxItems"] != 0 {
		t.Fatalf("choice=none 时数组上限应为 0: %+v", noneCalls)
	}
}

func TestCompletionUsesOpenAITokenAccounting(t *testing.T) {
	tokens := &Tokens{Input: 10, Output: 20, Reasoning: 5, Cache: &struct {
		Read  int64 `json:"read"`
		Write int64 `json:"write"`
	}{Read: 3, Write: 2}}
	result := completion("opencode/x", map[string]any{"role": "assistant", "content": "hi"}, tokens)
	usage, _ := result["usage"].(map[string]any)
	// OpenCode 把缓存与思考分别计费，换算回 OpenAI 口径后必须算进总量
	// OpenAI 对外口径
	if usage["prompt_tokens"] != int64(15) || usage["completion_tokens"] != int64(25) || usage["total_tokens"] != int64(40) {
		t.Fatalf("用量换算不符: %+v", usage)
	}
	// 网关记账口径：缓存命中必须从输入里拆出来（prompt 15 = 未命中 10 + 命中 3 + 缓存写 2）
	got := UsageTotals(result)
	if got.Output != 25 || got.Cache != 3 || got.Input != 12 {
		t.Fatalf("提取用量不符: %+v", got)
	}
	if got.Total() != 37 {
		t.Fatalf("网关 Tokens 口径应为未命中输入+输出 = 37，实际 %d", got.Total())
	}
}

// 没有 usage 或脏数据时不能把账记歪
func TestUsageTotalsDegradesSafely(t *testing.T) {
	if got := UsageTotals(map[string]any{}); got != (TokenUsage{}) {
		t.Fatalf("没有 usage 应全 0，实际 %+v", got)
	}
	// cached_tokens 超过 prompt_tokens：宁可不认这笔缓存命中，也不能算出负的输入
	dirty := map[string]any{"usage": map[string]any{
		"prompt_tokens": 10, "completion_tokens": 5,
		"prompt_tokens_details": map[string]any{"cached_tokens": 99},
	}}
	if got := UsageTotals(dirty); got.Input != 10 || got.Cache != 0 || got.Total() != 15 {
		t.Fatalf("脏数据兜底不符: %+v", got)
	}
}

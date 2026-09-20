package mcp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"testing"

	"workbuddy-desktop/internal/core"
)

// runOnce 往 server 喂若干行请求，收集所有响应行
func runOnce(t *testing.T, svc *core.Service, lines ...string) []map[string]any {
	t.Helper()
	in := &bytes.Buffer{}
	for _, l := range lines {
		in.WriteString(l + "\n")
	}
	// 用测试替身跑协议主干：直接复用 handleRequest，避免依赖进程级 stdin/stdout
	var buf bytes.Buffer
	out := bufio.NewWriter(&buf)
	sc := bufio.NewScanner(bytes.NewReader(in.Bytes()))
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		line := []byte(sc.Text())
		if len(line) == 0 {
			continue
		}
		var req rpcRequest
		if err := json.Unmarshal(line, &req); err != nil {
			continue
		}
		if len(req.ID) == 0 || string(req.ID) == "null" {
			continue
		}
		handleRequest(out, svc, &req)
	}
	_ = out.Flush()

	var resps []map[string]any
	for _, l := range bytes.Split(buf.Bytes(), []byte("\n")) {
		l = bytes.TrimSpace(l)
		if len(l) == 0 {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(l, &m); err != nil {
			t.Fatalf("响应不是合法 JSON: %s", l)
		}
		resps = append(resps, m)
	}
	return resps
}

func newTestService(t *testing.T) *core.Service {
	t.Helper()
	svc := core.NewServiceAt(t.TempDir())
	// 测试替身不依赖真实配置，直接塞单价表
	cfg := svc.GetConfig()
	cfg.Models.Prices = map[string]core.ModelPrice{
		"test-model": {Input: 2, Output: 8, CacheRead: 0.04, CacheWrite: 0},
	}
	if err := svc.UpdateConfig(cfg); err != nil {
		t.Fatalf("配置写入失败: %v", err)
	}
	return svc
}

func TestInitializeEchoesProtocolVersion(t *testing.T) {
	svc := newTestService(t)
	resps := runOnce(t, svc,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","clientInfo":{"name":"t","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
	)
	if len(resps) != 1 {
		t.Fatalf("notification 不应产生响应，得到 %d 条", len(resps))
	}
	res := resps[0]["result"].(map[string]any)
	if res["protocolVersion"] != "2025-03-26" {
		t.Fatalf("协议版本应回显客户端值，得到 %v", res["protocolVersion"])
	}
	if _, ok := res["capabilities"].(map[string]any)["tools"]; !ok {
		t.Fatal("capabilities 应声明 tools")
	}
}

func TestToolsListAndUnknownMethod(t *testing.T) {
	svc := newTestService(t)
	resps := runOnce(t, svc,
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":2,"method":"no/such"}`,
	)
	tools := resps[0]["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 4 {
		t.Fatalf("应为 4 个工具，得到 %d", len(tools))
	}
	names := map[string]bool{}
	for _, tl := range tools {
		m := tl.(map[string]any)
		names[m["name"].(string)] = true
		if m["description"] == "" || m["inputSchema"] == nil {
			t.Fatalf("工具 %v 缺少 description/inputSchema", m["name"])
		}
	}
	for _, want := range []string{"buddybot_quota", "buddybot_estimate_cost", "buddybot_search_sessions", "buddybot_search_code"} {
		if !names[want] {
			t.Fatalf("缺少工具 %s", want)
		}
	}
	errObj, ok := resps[1]["error"].(map[string]any)
	if !ok || int(errObj["code"].(float64)) != -32601 {
		t.Fatalf("未知方法应回 -32601，得到 %v", resps[1])
	}
}

func TestEstimateCostPricedAndUnpriced(t *testing.T) {
	svc := newTestService(t)
	// 100 万输入 + 100 万输出：2 + 8 = 10 元
	resps := runOnce(t, svc,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"buddybot_estimate_cost","arguments":{"model":"test-model","input_tokens":1000000,"output_tokens":1000000}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"buddybot_estimate_cost","arguments":{"model":"unknown-model","input_tokens":10,"output_tokens":10}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"buddybot_estimate_cost","arguments":{"model":"test-model"}}}`,
	)
	text1 := toolText(t, resps[0])
	if !bytes.Contains([]byte(text1), []byte("¥10.0000")) {
		t.Fatalf("金额应为 ¥10.0000，得到 %s", text1)
	}
	text2 := toolText(t, resps[1])
	if !bytes.Contains([]byte(text2), []byte("未配置单价")) {
		t.Fatalf("未定价模型应显式标注，得到 %s", text2)
	}
	// 缺参：按工具错误返回（isError），而不是协议错误
	if resps[2]["result"].(map[string]any)["isError"] != true {
		t.Fatalf("缺参应回 isError 结果，得到 %v", resps[2])
	}
}

func TestQuotaToolOnEmptyPool(t *testing.T) {
	svc := newTestService(t)
	resps := runOnce(t, svc,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"buddybot_quota"}}`,
	)
	if text := toolText(t, resps[0]); !bytes.Contains([]byte(text), []byte("账号池为空")) {
		t.Fatalf("空池应如实说明，得到 %s", text)
	}
}

func toolText(t *testing.T, resp map[string]any) string {
	t.Helper()
	res, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("缺少 result: %v", resp)
	}
	content := res["content"].([]any)
	return content[0].(map[string]any)["text"].(string)
}

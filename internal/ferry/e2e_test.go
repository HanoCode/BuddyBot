package ferry

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func truncateForTestLog(s string, limit int) string {
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit]) + "…"
}

// 真正的端到端冒烟：下载隔离 OpenCode → 起隔离后端 → 读免费模型目录 → 逐模型探测。
//
// 默认不跑（会真的联网下载约 50MB 运行时并访问上游免费额度），需要时显式打开：
//
//	BUDDYBOT_FERRY_E2E=1 go test -count=1 -v -run TestRuntimeBootstrapEndToEnd ./internal/ferry/
//
// 反复调试这条链路时用 BUDDYBOT_FERRY_E2E_DIR 固定数据目录，可以复用已下载的运行时
// （否则每跑一次都要重新下载 50MB）：
//
//	BUDDYBOT_FERRY_E2E=1 BUDDYBOT_FERRY_E2E_DIR=/tmp/ferry-e2e \
//	  go test -count=1 -v -run TestRuntimeBootstrapEndToEnd ./internal/ferry/
//
// 这条链路只要有一环接错（下载校验、隔离参数、目录解析、探测判定），这里就会红；
// 单元测试用的是假后端，替不了它。
func TestRuntimeBootstrapEndToEnd(t *testing.T) {
	if os.Getenv("BUDDYBOT_FERRY_E2E") == "" {
		t.Skip("设置 BUDDYBOT_FERRY_E2E=1 才跑：会真的下载 OpenCode 运行时并访问上游")
	}
	dir := os.Getenv("BUDDYBOT_FERRY_E2E_DIR")
	if dir == "" {
		dir = t.TempDir()
	} else if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("准备数据目录失败: %v", err)
	}
	t.Logf("数据目录: %s", dir)
	m := New(Config{
		DataDir:     dir,
		Concurrency: 3,
		Logf:        func(format string, args ...any) { t.Logf(format, args...) },
	})
	m.Start()
	defer m.Stop()

	// 1. 引导：准备运行时 → 起后端 → 读目录（首次要下载，给足时间）
	st := waitPhase(t, m, 12*time.Minute, "ready", "error")
	t.Logf("引导结束: phase=%s message=%s err=%s 隔离 OpenCode=%s 模型=%d",
		st.Phase, st.Message, st.LastError, st.OpenCodeVersion, st.ModelCount)
	if st.Phase != "ready" {
		t.Fatalf("引导未达 ready: %s / %s / %s", st.Phase, st.Message, st.LastError)
	}
	if st.OpenCodeVersion == "" {
		t.Fatal("未识别到隔离运行时版本")
	}
	if st.ModelCount == 0 {
		t.Fatal("目录里没有免费模型：价格判定或目录解析有问题")
	}

	// 2. 探测：全部跑完再看可用数
	st = waitProbes(t, m, 15*time.Minute)
	ok, fail := 0, 0
	for _, view := range st.Models {
		if view.Available {
			ok++
		} else {
			fail++
		}
	}
	t.Logf("探测结束: 可用 %d / 不可用 %d（共 %d）", ok, fail, len(st.Models))
	for _, view := range st.Models {
		// 探测跑完后每个模型都必须有判定，且判定挂在它自己身上。
		// 否则界面会显示「未检测」，失败原因也一起丢失。
		if view.Verdict == nil {
			t.Errorf("模型 %s 探测结束后仍然没有判定（界面显示「未检测」）", view.ID)
			continue
		}
		if view.Verdict.Model != view.ID {
			t.Errorf("模型 %s 的判定挂在了 %q 上", view.ID, view.Verdict.Model)
		}
		t.Logf("  %-48s 可用=%-5v 类别=%-10s chatOnly=%-5v %s",
			view.ID, view.Available, view.Verdict.Category, view.chatOnly(), view.Verdict.Error)
	}
	if ok == 0 {
		t.Fatal("没有模型通过探测：适配器或探测判定有问题（免费额度全部不可用等于功能不可用）")
	}
	if got := len(m.AvailableModels()); got != ok {
		t.Fatalf("AvailableModels() = %d，与状态里的可用数 %d 不一致", got, ok)
	}

	// 3. 真发一次请求：走完「OpenAI 请求 → 隔离会话 → JSON 信封 → OpenAI 响应」整条适配器链路。
	//    单元测试里后端是假的，只有这里能证明信封协议与真实 OpenCode 对得上。
	var pick string
	for _, view := range st.Models {
		if view.Available && !view.chatOnly() {
			pick = view.ID
			break
		}
	}
	if pick == "" {
		t.Skip("没有支持工具的可用模型，跳过真实请求验证")
	}
	body := []byte(`{"model":"` + pick + `","messages":[{"role":"user","content":"只回复两个字：收到"}],"max_tokens":64}`)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	res, berr := m.Chat(ctx, body)
	if berr != nil {
		t.Fatalf("真实转发失败: %d %s %s", berr.Status, berr.Code, berr.Message)
	}
	if res.Body == nil {
		t.Fatal("转发返回空响应")
	}
	choices, _ := res.Body["choices"].([]any)
	if len(choices) == 0 {
		t.Fatalf("响应里没有 choices: %+v", res.Body)
	}
	first, _ := choices[0].(map[string]any)
	message, _ := first["message"].(map[string]any)
	content, _ := message["content"].(string)
	if strings.TrimSpace(content) == "" && message["tool_calls"] == nil {
		t.Fatalf("响应既没有文本也没有工具调用: %+v", message)
	}
	t.Logf("真实转发 OK: model=%s 未命中输入=%d 命中缓存=%d 输出=%d content=%q",
		pick, res.Usage.Input, res.Usage.Cache, res.Usage.Output, truncateForTestLog(content, 120))

	// 4. 判定缓存落盘：重启后不必重新探测（adoptCached 直接接上）
	cache := filepath.Join(dir, "probe-cache.json")
	if _, err := os.Stat(cache); err != nil {
		t.Fatalf("判定缓存未落盘: %v", err)
	}
	again := New(Config{DataDir: dir, Concurrency: 3, Logf: func(string, ...any) {}})
	again.cache.load()
	if n := len(again.cache.entries); n == 0 {
		t.Fatal("判定缓存读回来是空的")
	}
	t.Logf("判定缓存: %d 条", len(again.cache.entries))
}

func waitPhase(t *testing.T, m *Manager, budget time.Duration, want ...string) Status {
	t.Helper()
	deadline := time.Now().Add(budget)
	for {
		st := m.Status()
		for _, phase := range want {
			if st.Phase == phase {
				return st
			}
		}
		if time.Now().After(deadline) {
			return st
		}
		time.Sleep(2 * time.Second)
	}
}

func waitProbes(t *testing.T, m *Manager, budget time.Duration) Status {
	t.Helper()
	deadline := time.Now().Add(budget)
	last := -1
	for {
		st := m.Status()
		if !st.Probing && len(st.ProbePending) == 0 {
			return st
		}
		if len(st.Models) != last {
			last = len(st.Models)
		}
		if time.Now().After(deadline) {
			t.Fatalf("探测超时未结束：进行中 %v，待测 %v", st.ProbeActive, st.ProbePending)
		}
		time.Sleep(2 * time.Second)
	}
}

package core

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestProbeModelCosts P2-9：主动探测把 usage.credit 观测写入免费/收费账本，
// 已观测模型不再重复探测。
func TestProbeModelCosts(t *testing.T) {
	svc := newTestService(t)
	seedCredential(t, svc.AuthDir(), "u_probe", 30*24*time.Hour)
	svc.Store().MergeUpstreamModels([]UpstreamModel{
		{ID: "m-free"}, {ID: "m-paid"}, {ID: "m-unknown"},
	})
	// 预置：m-paid 已观测为收费（credit>0 学习），不应再探测
	svc.Store().MutateAccountState("u_probe", func(st *AccountState) {
		st.ModelFreeLedger = map[string]bool{"m-paid": false}
	})

	probed := map[string]int{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		var body map[string]any
		raw := make([]byte, 4096)
		n, _ := r.Body.Read(raw)
		_ = json.Unmarshal(raw[:n], &body)
		model, _ := body["model"].(string)
		probed[model]++
		credit := 0
		if model == "m-paid" {
			credit = 5
		}
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{}}],\"usage\":{\"prompt_tokens\":60,\"completion_tokens\":40,\"total_tokens\":100,\"credit\":%d}}\n\n", credit)
		fmt.Fprint(w, "data: [DONE]\n\n")
		fl.Flush()
	}))
	defer up.Close()

	s := NewScheduler(svc)
	s.up.baseCN, s.up.baseGlobal = up.URL, up.URL
	// store 预置了多个默认模型且单轮上限 3：循环探测直到自定义模型入账本（有界）
	for i := 0; i < 10; i++ {
		s.probeModelCosts()
		led := svc.Store().AccountState("u_probe").ModelFreeLedger
		if _, hasFree := led["m-free"]; hasFree {
			if _, hasUnknown := led["m-unknown"]; hasUnknown {
				break
			}
		}
	}

	// m-free: credit=0 且 total=100 ≥100 → 免费
	if free, ok := svc.Store().AccountState("u_probe").ModelFreeLedger["m-free"]; !ok || !free {
		t.Fatalf("m-free 应被实测为免费，ledger=%v", svc.Store().AccountState("u_probe").ModelFreeLedger)
	}
	// m-unknown: 同样 credit=0 → 免费；m-paid 已观测不被探测
	if probed["m-paid"] != 0 {
		t.Fatalf("已观测模型不应被探测，实际 %d 次", probed["m-paid"])
	}
	if probed["m-free"] != 1 || probed["m-unknown"] != 1 {
		t.Fatalf("未观测模型应各探测一次: %v", probed)
	}
}

package core

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// TestPatrolExpiryConcurrentPaced P0-3：巡逻在保持启动节流（全局请求速率与
// 串行版一致）的同时并发执行——所有在线账号都被巡检到，且在途请求受
// patrolConcurrency 封顶。
func TestPatrolExpiryConcurrentPaced(t *testing.T) {
	svc := newTestService(t)
	for _, uid := range []string{"u_p1", "u_p2", "u_p3"} {
		seedCredential(t, svc.AuthDir(), uid, 30*24*time.Hour)
	}
	var hits, inflight, maxInflight atomic.Int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cur := inflight.Add(1)
		for {
			m := maxInflight.Load()
			if cur <= m || maxInflight.CompareAndSwap(m, cur) {
				break
			}
		}
		defer inflight.Add(-1)
		if r.URL.Path != meterResourcePath {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":{"Response":{"Data":{"Accounts":[
			{"CapacityRemain":100,"CapacityUsed":10,"CapacitySize":200,"CycleEndTime":"2026-12-01 00:00:00"}]}}}}`))
	}))
	defer up.Close()

	s := NewScheduler(svc)
	s.up.baseCN, s.up.baseGlobal = up.URL, up.URL
	s.up.billingCN, s.up.billingGlobal = up.URL, up.URL

	start := time.Now()
	s.patrolExpiry()
	elapsed := time.Since(start)

	if hits.Load() != 3 {
		t.Fatalf("上游余额请求次数 = %d, want 3（每个在线账号一次）", hits.Load())
	}
	for _, a := range svc.Accounts() {
		if !svc.Store().AccountState(a.UID).CreditsKnown {
			t.Fatalf("账号 %s 巡逻后余额仍未写入", a.UID)
		}
	}
	if m := maxInflight.Load(); m > patrolConcurrency {
		t.Fatalf("在途请求峰值 %d 超过并发上限 %d", m, patrolConcurrency)
	}
	// 并发版墙钟时间应明显低于串行版（3 账号 × 300ms 间隔 + 响应；这里只做宽松下界：
	// 必须至少经历两轮 300ms 启动节流，但不应达到串行版的 3×(300ms+响应) 量级）
	if elapsed < 600*time.Millisecond {
		t.Fatalf("巡逻耗时 %v，启动节流似乎未生效", elapsed)
	}
}

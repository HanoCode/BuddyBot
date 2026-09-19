package core

import (
	"testing"
	"time"
)

// TestRefreshInterval 验证定时刷新间隔解析：0/负数回落默认值，正数原样生效
func TestRefreshInterval(t *testing.T) {
	if got := refreshInterval(0); got != DefaultRefreshIntervalMinutes*time.Minute {
		t.Fatalf("0 应回落默认间隔，得到 %v", got)
	}
	if got := refreshInterval(-5); got != DefaultRefreshIntervalMinutes*time.Minute {
		t.Fatalf("负数应回落默认间隔，得到 %v", got)
	}
	if got := refreshInterval(15); got != 15*time.Minute {
		t.Fatalf("15 分钟应原样生效，得到 %v", got)
	}
}

// TestRefreshTickDisabled 验证关闭定时刷新时不触发刷新
func TestRefreshTickDisabled(t *testing.T) {
	svc := NewServiceAt(t.TempDir())
	svc.config.Schedule.Refresh = RefreshConfig{Enabled: false, IntervalMinutes: 30}
	s := svc.scheduler
	s.refreshTick() // 不应 panic，也不应置 lastRefresh
	s.mu.Lock()
	fired := !s.lastRefresh.IsZero()
	s.mu.Unlock()
	if fired {
		t.Fatal("刷新关闭时不应记录刷新时刻")
	}
}

// TestRefreshTickDue 验证开启定时刷新后到期会触发刷新（账号池为空，刷新空跑）
func TestRefreshTickDue(t *testing.T) {
	svc := NewServiceAt(t.TempDir())
	svc.config.Schedule.Refresh = RefreshConfig{Enabled: true, IntervalMinutes: 30}
	s := svc.scheduler
	s.refreshTick()
	s.mu.Lock()
	fired := !s.lastRefresh.IsZero()
	s.mu.Unlock()
	if !fired {
		t.Fatal("首次 tick 应视为到期并记录刷新时刻")
	}
	// 间隔内不重复触发
	s.mu.Lock()
	s.lastRefresh = time.Now().Add(-time.Minute) // 仅 1 分钟前刷新过
	s.mu.Unlock()
	s.refreshTick()
	s.mu.Lock()
	defer s.mu.Unlock()
	if elapsed := time.Since(s.lastRefresh); elapsed > 2*time.Minute {
		t.Fatalf("间隔内不应重复触发刷新（lastRefresh=%v）", s.lastRefresh)
	}
}

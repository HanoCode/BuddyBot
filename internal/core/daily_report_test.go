package core

import (
	"strings"
	"testing"
	"time"
)

// TestBuildDailyReport P1-5：报告聚合账号/积分/24h 用量三个板块。
func TestBuildDailyReport(t *testing.T) {
	svc := newTestService(t)
	seedCredential(t, svc.AuthDir(), "u_rep1", 30*24*time.Hour)
	seedCredential(t, svc.AuthDir(), "u_rep2", 30*24*time.Hour)

	now := time.Now().Unix()
	svc.Store().MutateAccountState("u_rep1", func(st *AccountState) {
		st.CreditsKnown, st.Credits, st.CreditsExpiring = true, 120, 30
		st.CreditsExpireDay = "2026-09-25"
	})
	svc.Store().MutateAccountState("u_rep2", func(st *AccountState) {
		st.CreditsKnown, st.Credits = true, 80
	})
	svc.Store().AppendRequestLog(RequestLog{ID: NewID("r"), TS: now - 3600, Model: "glm-5.2",
		Status: 200, Tokens: 300, OutputTokens: 100, CacheTokens: 50})
	svc.Store().AppendRequestLog(RequestLog{ID: NewID("r"), TS: now - 60, Model: "glm-5.2",
		Status: 200, Tokens: 100, OutputTokens: 40})
	svc.Store().AppendRequestLog(RequestLog{ID: NewID("r"), TS: now - 60, Model: "deepseek-v4",
		Status: 500, Tokens: 0})

	s := NewScheduler(svc)
	title, body := s.buildDailyReport()

	if !strings.Contains(title, "日报") || !strings.Contains(title, "2 账号") || !strings.Contains(title, "3 请求") {
		t.Fatalf("标题异常: %q", title)
	}
	for _, want := range []string{"在线 2", "总余额 200", "临期 30", "2026-09-25", "请求 3（失败 1）", "tokens 400", "缓存命中 50", "glm-5.2 400"} {
		if !strings.Contains(body, want) {
			t.Fatalf("报告缺少 %q:\n%s", want, body)
		}
	}
}

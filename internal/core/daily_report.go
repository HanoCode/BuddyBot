package core

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

// 每日中文运营报告（对齐 WorkBuddy-Daily 的推送能力）：每天 fixedReportHour 点
// （CST）聚合一日的账号 / 积分 / 网关用量概况，经已有 PushPlus / Bark 通道推送。
// 未配置任何推送渠道时静默跳过（报告本身仍可通过事件总线在前端查看）。

// fixedReportHour 每日报告推送时刻（CST 小时）。
const fixedReportHour = 21

// dailyReportLoop 每分钟对表，命中推送时刻且当日未发过则发报告。
func (s *Scheduler) dailyReportLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	lastSent := ""
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			now := time.Now().In(cstZone)
			today := now.Format("2006-01-02")
			if now.Hour() != fixedReportHour || today == lastSent {
				continue
			}
			lastSent = today // 先记日期再发送：推送失败也不在当日反复重试
			s.sendDailyReport()
		}
	}
}

// sendDailyReport 构建并推送当日报告。
func (s *Scheduler) sendDailyReport() {
	title, body := s.buildDailyReport()
	EmitEvent(EventGatewayLog, map[string]any{"level": "info", "message": "每日报告已生成：" + title})
	cfg := s.svc.GetConfig().Schedule.Notify
	if !cfg.Enabled {
		return
	}
	client := &http.Client{Timeout: 10 * time.Second}
	if tok := strings.TrimSpace(cfg.PushPlusToken); tok != "" {
		go pushPlus(client, tok, title, body)
	}
	if bu := strings.TrimSpace(cfg.BarkURL); bu != "" {
		go barkPush(client, bu, title, body)
	}
}

// buildDailyReport 聚合报告：账号概况 / 积分与临期 / 过去 24h 网关用量。
func (s *Scheduler) buildDailyReport() (string, string) {
	var b strings.Builder
	online, relogin := 0, 0
	totalCredits, expiring := 0.0, 0.0
	earliestDay, earliestUID := "", ""
	accounts := s.svc.Accounts()
	for _, a := range accounts {
		switch a.Status {
		case "online":
			online++
		}
		st := s.svc.Store().AccountState(a.UID)
		if st.NeedsRelogin {
			relogin++
		}
		if st.CreditsKnown {
			totalCredits += st.Credits
			expiring += st.CreditsExpiring
			if st.CreditsExpireDay != "" && (earliestDay == "" || st.CreditsExpireDay < earliestDay) {
				earliestDay, earliestUID = st.CreditsExpireDay, a.UID
			}
		}
	}
	fmt.Fprintf(&b, "【账号】共 %d 个：在线 %d · 需重新登录 %d\n", len(accounts), online, relogin)
	fmt.Fprintf(&b, "【积分】总余额 %.0f", totalCredits)
	if expiring > 0 {
		fmt.Fprintf(&b, "（临期 %.0f）", expiring)
	}
	if earliestDay != "" {
		fmt.Fprintf(&b, "\n【到期】最早 %s（账号 %s）——先烧快过期积分", earliestDay, shortUID(earliestUID))
	}
	b.WriteString("\n")

	// 过去 24h 网关用量（按请求日志聚合）
	since := time.Now().Add(-24 * time.Hour).Unix()
	reqs, failed, tokens, outTok, cacheTok := 0, 0, 0, 0, 0
	modelTokens := map[string]int{}
	for _, l := range s.svc.Store().ListRequestLogs() {
		if l.TS < since {
			continue
		}
		reqs++
		if l.Status >= 400 {
			failed++
		}
		tokens += l.Tokens
		outTok += l.OutputTokens
		cacheTok += l.CacheTokens
		modelTokens[l.Model] += l.Tokens
	}
	fmt.Fprintf(&b, "【网关 24h】请求 %d（失败 %d）· tokens %d（输出 %d · 缓存命中 %d）",
		reqs, failed, tokens, outTok, cacheTok)
	if len(modelTokens) > 0 {
		type kv struct {
			k string
			v int
		}
		var tops []kv
		for k, v := range modelTokens {
			tops = append(tops, kv{k, v})
		}
		sort.Slice(tops, func(i, j int) bool { return tops[i].v > tops[j].v })
		n := 3
		if len(tops) < n {
			n = len(tops)
		}
		var parts []string
		for _, t := range tops[:n] {
			parts = append(parts, fmt.Sprintf("%s %d", t.k, t.v))
		}
		fmt.Fprintf(&b, "\n【模型 Top】%s", strings.Join(parts, " · "))
	}

	title := fmt.Sprintf("WorkBuddy 日报 %s（%d 账号 · %d 请求）",
		time.Now().In(cstZone).Format("01-02"), len(accounts), reqs)
	return title, b.String()
}

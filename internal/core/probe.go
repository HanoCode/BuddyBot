package core

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

// ============================================================
// 模型免费性 / 计费周期实测探测（对齐 workbuddy-gateway 的 probe 思路）
//
// 双通道定价观测：
//   被动学习 —— learnModelCost：真实网关流量的 usage.credit 落「账号+模型」账本；
//   主动探测 —— 本文件：周期性（12h）对「未观测」的账号×模型发一次最小请求，
//   用 usage.credit 实测免费/收费，写入同一账本，供成本分层选号提前分层。
//
// 约束：每账号每轮最多探测 modelProbeMaxPerRound 个未观测模型，探测体仅 "hi"
// （最小 token 消耗），请求间保留 growthEventGap 防风控间隔。
// ============================================================

// modelProbeInterval 探测周期（对齐 workbuddy-gateway：12 小时实测）。
const modelProbeInterval = 12 * time.Hour

// modelProbeMaxPerRound 单账号单轮探测模型数上限（控制探测成本）。
const modelProbeMaxPerRound = 3

// modelProbeLoop 周期探测循环。
func (s *Scheduler) modelProbeLoop(ctx context.Context) {
	ticker := time.NewTicker(modelProbeInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.probeModelCosts()
		}
	}
}

// probeModelCosts 对所有在线账号的未观测模型做一轮实测。
func (s *Scheduler) probeModelCosts() {
	models := ListModels(s.svc.Store())
	if len(models) == 0 {
		return
	}
	for _, a := range s.svc.Accounts() {
		if a.Status != "online" {
			continue
		}
		cred, err := LoadUpstreamCred(s.svc.AuthDir(), a.Credential)
		if err != nil {
			continue
		}
		ledger := s.svc.Store().AccountState(a.UID).ModelFreeLedger
		probed := 0
		for _, m := range models {
			if probed >= modelProbeMaxPerRound {
				break
			}
			if _, ok := ledger[m.ID]; ok {
				continue // 已观测（免费或收费）：跳过
			}
			credit, total, err := s.probeOneModel(cred, m.ID)
			if err != nil {
				// 探测失败不写账本（保持未知），也不中断本轮
				continue
			}
			learnModelCostTo(s.svc.Store(), a.UID, m.ID, credit, total)
			probed++
			time.Sleep(growthEventGap)
		}
	}
}

// probeOneModel 发一次最小 chat 请求观测 usage.credit（返回 credit 与 total_tokens）。
func (s *Scheduler) probeOneModel(cred *UpstreamCred, model string) (float64, int, error) {
	body := map[string]any{
		"model":          model,
		"messages":       []map[string]any{{"role": "user", "content": "hi"}},
		"stream":         true,
		"stream_options": map[string]any{"include_usage": true},
	}
	injectThinking(body, "")
	req, err := http.NewRequest(http.MethodPost,
		s.up.chatBase(cred.Realm)+upstreamChatPath, bytes.NewReader(mustJSON(body)))
	if err != nil {
		return 0, 0, err
	}
	s.up.setChatHeaders(req, cred)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	resp, err := s.up.http.Do(req.WithContext(ctx))
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return 0, 0, fmt.Errorf("探测被上游拒绝（%d）%s", resp.StatusCode, truncateBody(raw))
	}
	_, _, _, _, _, _, credit, usageTotal, err := aggregateStream(resp.Body)
	if err != nil {
		return 0, 0, err
	}
	return credit, usageTotal, nil
}

// ---- 账本写入（网关被动学习与主动探测共用） ----

// learnModelCostTo 把一次 usage.credit 观测写入「账号+模型」免费/收费账本。
// credit>0 → 收费；credit=0 且 total_tokens≥learnFreeMinTokens → 免费；
// 样本不足时不写不覆盖，保持未知。
func learnModelCostTo(store *Store, uid, model string, credit float64, usageTotal int) {
	if model == "" || uid == "" || usageTotal <= 0 {
		return
	}
	store.MutateAccountState(uid, func(st *AccountState) {
		if credit > 0 {
			if st.ModelFreeLedger == nil {
				st.ModelFreeLedger = map[string]bool{}
			}
			st.ModelFreeLedger[model] = false
			return
		}
		if usageTotal >= learnFreeMinTokens {
			if st.ModelFreeLedger == nil {
				st.ModelFreeLedger = map[string]bool{}
			}
			st.ModelFreeLedger[model] = true
		}
	})
}

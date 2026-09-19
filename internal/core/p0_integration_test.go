package core

import (
	"encoding/json"
	"testing"
	"time"
)

// ---- P0-4：finish_reason 空串归一化 ----

func TestNormalizeFinishReason(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"空串归一化为null",
			`{"choices":[{"index":0,"delta":{"content":"a"},"finish_reason":""}]}`},
		{"非空串原样保留（快路径）",
			`{"choices":[{"finish_reason":"stop","delta":{}}]}`},
		{"无finish_reason原样保留",
			`{"choices":[{"delta":{"content":"x"}}]}`},
		{"多choice混合只改空串",
			`{"choices":[{"finish_reason":""},{"finish_reason":"stop"}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := normalizeFinishReason(tc.in)
			var inMap, gotMap map[string]any
			if err := json.Unmarshal([]byte(tc.in), &inMap); err != nil {
				t.Fatalf("输入不是合法 JSON: %v", err)
			}
			if err := json.Unmarshal([]byte(got), &gotMap); err != nil {
				t.Fatalf("输出不是合法 JSON: %v", err)
			}
			inChoices := inMap["choices"].([]any)
			gotChoices := gotMap["choices"].([]any)
			if len(inChoices) != len(gotChoices) {
				t.Fatalf("choices 数量改变: %d -> %d", len(inChoices), len(gotChoices))
			}
			for i := range gotChoices {
				gc := gotChoices[i].(map[string]any)
				ic := inChoices[i].(map[string]any)
				inFR, hasIn := ic["finish_reason"]
				gotFR, hasGot := gc["finish_reason"]
				if hasIn != hasGot {
					t.Fatalf("choice[%d] finish_reason 存在性改变", i)
				}
				if hasIn {
					inStr, inEmpty := inFR.(string)
					if inEmpty && inStr == "" && gotFR != nil {
						t.Fatalf("choice[%d] 空串未被归一化为 null: %v", i, gotFR)
					}
					if !(inEmpty && inStr == "") && gotFR != inFR {
						t.Fatalf("choice[%d] 非空 finish_reason 被篡改: %v -> %v", i, inFR, gotFR)
					}
				}
			}
		})
	}
}

// ---- P0-5：refresh token 被明确拒绝的判定 ----

func TestIsRefreshRejectedBody(t *testing.T) {
	hits := []string{
		`{"code":12153,"msg":"Offline user session not found"}`,
		`HTTP 401: session not found`,
	}
	for _, b := range hits {
		if !isRefreshRejectedBody(b) {
			t.Fatalf("应命中 RT 明确拒绝: %s", b)
		}
	}
	misses := []string{
		`{"code":10001,"msg":"unauthorized"}`,
		`{"code":12100,"msg":"token expired"}`,
	}
	for _, b := range misses {
		if isRefreshRejectedBody(b) {
			t.Fatalf("不应命中 RT 明确拒绝: %s", b)
		}
	}
}

func TestDeriveStatusNeedsRelogin(t *testing.T) {
	c := Credential{HasToken: true, HasRefresh: true, ExpiresAt: 1 << 40}
	st := AccountState{NeedsRelogin: true}
	status, note := deriveStatus(c, st, time.Now())
	if status != "relogin" {
		t.Fatalf("NeedsRelogin 应推导为 relogin 状态，得到 %s", status)
	}
	if note == "" {
		t.Fatal("relogin 状态必须带说明")
	}
	// Disabled 优先级更高
	st2 := AccountState{NeedsRelogin: true, Disabled: true}
	if s, _ := deriveStatus(c, st2, time.Now()); s != "disabled" {
		t.Fatalf("Disabled 应优先，得到 %s", s)
	}
}

// ---- P1-6：到期分层选号 ----

func TestFilterExpiryTier(t *testing.T) {
	svc := newTestService(t)
	g := NewGateway(svc)
	store := svc.Store()
	cands := []Account{{UID: "a"}, {UID: "b"}, {UID: "c"}}
	// a 最先到期；b 同日；c 无到期信息
	store.MutateAccountState("a", func(st *AccountState) { st.CreditsExpireDay = "2026-09-01" })
	store.MutateAccountState("b", func(st *AccountState) { st.CreditsExpireDay = "2026-09-01" })
	store.MutateAccountState("c", func(st *AccountState) { st.CreditsExpireDay = "2026-12-31" })

	out := g.filterExpiryTier(cands)
	if len(out) != 2 {
		t.Fatalf("应只保留最早到期档的 2 个账号: %v", out)
	}
	for _, a := range out {
		if a.UID == "c" {
			t.Fatal("更晚到期的账号不应留在候选集")
		}
	}

	// 全部无到期信息 → 原样回退
	cands2 := []Account{{UID: "x"}, {UID: "y"}}
	out2 := g.filterExpiryTier(cands2)
	if len(out2) != 2 {
		t.Fatalf("无到期数据应回退原候选集: %v", out2)
	}
}

// ---- P1-7：免费/收费学习账本 ----

func TestLearnModelCost(t *testing.T) {
	svc := newTestService(t)
	g := NewGateway(svc)
	store := svc.Store()

	g.learnModelCost("u1", "m1", 0.42, 820) // 收费
	g.learnModelCost("u1", "m2", 0, 500)    // 免费样本足够
	g.learnModelCost("u1", "m3", 0, 20)     // 样本过小 → 保持未知
	g.learnModelCost("u1", "m4", 0, 80)     // 免费判定后再次小样本 → 保留免费结论
	g.learnModelCost("u1", "m4", 1.5, 300)  // 实测收费 → 覆盖为收费

	st := store.AccountState("u1")
	if v, ok := st.ModelFreeLedger["m1"]; !ok || v {
		t.Fatalf("m1 应学习为收费: %v", st.ModelFreeLedger)
	}
	if v, ok := st.ModelFreeLedger["m2"]; !ok || !v {
		t.Fatalf("m2 应学习为免费: %v", st.ModelFreeLedger)
	}
	if _, ok := st.ModelFreeLedger["m3"]; ok {
		t.Fatalf("m3 样本过小不应写入账本: %v", st.ModelFreeLedger)
	}
	if v, ok := st.ModelFreeLedger["m4"]; !ok || v {
		t.Fatalf("m4 应被后续实测覆盖为收费: %v", st.ModelFreeLedger)
	}
}

// ---- P0-2：专家/团队事件 id 轮换 ----

func TestExpertEventRotation(t *testing.T) {
	// 单任务 need ≤ target ≤ 5，远小于池长 18 —— 池内轮换保证同一任务内
	// id 互不重复（上游按 eventCode+id 去重，重复 id 进度卡死）
	seen := map[string]bool{}
	for i := 0; i < 5; i++ {
		ev := buildGrowthTaskEvent("uid-x", "expert", i, expertIDPool[i%len(expertIDPool)])
		id, _ := ev["id"].(string)
		if id == "" {
			t.Fatalf("第 %d 个 expert 事件缺少 id", i)
		}
		if seen[id] {
			t.Fatalf("expert 事件 id 重复（上游按 eventCode+id 去重，进度会卡死）: %s", id)
		}
		seen[id] = true
		if ev["eventCode"] != "expert_actual_use" {
			t.Fatalf("expert 事件 eventCode 异常: %v", ev["eventCode"])
		}
	}
	// 团队同理
	seenTeam := map[string]bool{}
	for i := 0; i < 3; i++ {
		ev := buildGrowthTaskEvent("uid-x", "team", i, teamIDPool[i%len(teamIDPool)])
		id, _ := ev["id"].(string)
		if seenTeam[id] {
			t.Fatalf("team 事件 id 重复: %s", id)
		}
		seenTeam[id] = true
	}
}

func TestBuildGrowthTaskEventCodes(t *testing.T) {
	want := map[string]string{
		"canvas": "wbx_design_canvas_task_create",
		"team":   "expert_actual_use",
		"chat":   "chat_request_send",
		"skill":  "skill_info",
	}
	for kind, code := range want {
		ev := buildGrowthTaskEvent("u", kind, 0, [2]string{"CloudOpsTeam", "运维专家团队"})
		if ev["eventCode"] != code {
			t.Fatalf("kind=%s eventCode 应为 %s，得到 %v", kind, code, ev["eventCode"])
		}
		if ev["userId"] != "u" {
			t.Fatalf("事件必须带 userId（否则上游静默丢弃）")
		}
	}
}

// ---- P0-2：任务规格完整性（dark-side 防回归：夜猫子白天必须跳过） ----

func TestGrowthSpecsCoverCriticalTasks(t *testing.T) {
	for _, code := range []string{"chat_5", "expert_5", "Expert_team_use_3", "create_canvas"} {
		if _, ok := growthTaskSpecs[code]; !ok {
			t.Fatalf("关键任务 %s 缺少事件链规格", code)
		}
	}
	if _, only := desktopOnlyTasks["RichMeow_Chat"]; !only {
		t.Fatal("RichMeow_Chat 应标记为桌面端专属任务")
	}
}

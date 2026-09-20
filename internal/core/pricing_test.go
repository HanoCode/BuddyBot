package core

import (
	"math"
	"path/filepath"
	"testing"
	"time"
)

// almost 金额浮点比较（单价含小数，逐项相乘后不宜用 ==）
func almost(got, want float64) bool { return math.Abs(got-want) < 1e-9 }

// 单价表：四类 token 分别计价；未配置的模型返回 priced=false（不是「免费」）
func TestPriceTableCost(t *testing.T) {
	pt := NewPriceTable(map[string]ModelPrice{
		"glm-5.2": {Input: 1, Output: 4, CacheRead: 0.2, CacheWrite: 2},
	})
	// (1e6×1 + 5e5×4 + 2e6×0.2 + 1e5×2) / 1e6 = 1 + 2 + 0.4 + 0.2 = 3.6
	cost, priced := pt.Cost("glm-5.2", 1_000_000, 500_000, 2_000_000, 100_000)
	if !priced {
		t.Fatal("已配置单价的模型应 priced=true")
	}
	if !almost(cost, 3.6) {
		t.Fatalf("cost = %v, want 3.6", cost)
	}
	// 未定价：金额恒 0，且必须与「定价为 0 元」区分
	if cost, priced := pt.Cost("no-such-model", 1_000_000, 1_000_000, 0, 0); priced || cost != 0 {
		t.Fatalf("未定价模型应返回 (0,false)，得到 (%v,%v)", cost, priced)
	}
	// 空表等价于全部未定价
	if _, priced := NewPriceTable(nil).Cost("glm-5.2", 1, 1, 1, 1); priced {
		t.Fatal("空单价表不应给出金额")
	}
	// 定价为 0 元 = 免费（priced=true，金额 0）
	if cost, priced := NewPriceTable(map[string]ModelPrice{"free": {}}).Cost("free", 1e6, 1e6, 0, 0); !priced || cost != 0 {
		t.Fatalf("零价模型应返回 (0,true)，得到 (%v,%v)", cost, priced)
	}
}

// 网关口径：InputTokens 不含缓存命中，缓存单列按缓存价计；未定价模型不计入金额但要报数
func TestStatsCostByModelPrice(t *testing.T) {
	withFakeHome(t) // 会话标题扫描走临时目录，避免误扫真实 home
	store := newTestStore(t)
	// 锚定当日：now-1h 在零点后一小时内跑会落到昨天，「今日」断言随之崩掉；
	// 但也不能早于今天零点太多导致落到未来被窗口过滤，取两者交集
	nowT := time.Now()
	dayStart := time.Date(nowT.Year(), nowT.Month(), nowT.Day(), 0, 0, 0, 0, nowT.Location())
	dayBase := nowT.Add(-time.Hour)
	if dayBase.Before(dayStart) {
		dayBase = dayStart
	}
	base := dayBase.Unix()
	// in=1M（未命中）+ out=0.5M + cache=2M
	// → (1e6×1 + 5e5×4 + 2e6×0.2)/1e6 = 1 + 2 + 0.4 = 3.4 元
	store.AppendRequestLog(RequestLog{
		TS: base, Time: Now(), KeyID: "k1", KeyName: "k1", Model: "glm-5.2", Status: 200,
		Tokens: 1_500_000, InputTokens: 1_000_000, OutputTokens: 500_000, CacheTokens: 2_000_000, SessionID: "s1",
	})
	store.AppendRequestLog(RequestLog{
		TS: base + 60, Time: Now(), KeyID: "k1", KeyName: "k1", Model: "unpriced-x", Status: 200,
		Tokens: 100, InputTokens: 60, OutputTokens: 40, SessionID: "s1",
	})
	// 鉴权失败一类记录：model 为空、0 token —— 不构成计价缺口，不得计入未定价名单
	store.AppendRequestLog(RequestLog{
		TS: base + 90, Time: Now(), KeyName: "(未鉴权)", Status: 401, Tokens: 0, Error: "鉴权未通过",
	})

	prices := map[string]ModelPrice{"glm-5.2": {Input: 1, Output: 4, CacheRead: 0.2}}
	stats := NewStats(store, func() []Account { return nil }, func() map[string]ModelPrice { return prices })
	d := stats.Dashboard(7)

	if !almost(d.Overview.Cost, 3.4) {
		t.Fatalf("总览金额 = %v, want 3.4", d.Overview.Cost)
	}
	if d.Overview.UnpricedModels != 1 {
		t.Fatalf("未定价模型数 = %d, want 1（0 token 的失败记录不算计价缺口）", d.Overview.UnpricedModels)
	}
	// 两条记录都在今天 → 今日金额 = 窗口金额；活跃 1 天 → 日均 = 金额
	if !almost(d.Overview.TodayCost, 3.4) || !almost(d.Overview.AvgCostPerDay, 3.4) {
		t.Fatalf("今日/日均金额异常: today=%v avg=%v", d.Overview.TodayCost, d.Overview.AvgCostPerDay)
	}
	// 逐日：无数据日补 0 且不产生金额
	for _, p := range d.Daily {
		if p.Tokens == 0 && p.Cost != 0 {
			t.Fatalf("无数据日 %s 金额应为 0，得到 %v", p.Date, p.Cost)
		}
	}
	// 模型榜：已定价带金额，未定价标 priced=false
	var priced, unpriced *ModelStat
	for i := range d.Models {
		switch d.Models[i].Model {
		case "glm-5.2":
			priced = &d.Models[i]
		case "unpriced-x":
			unpriced = &d.Models[i]
		}
	}
	if priced == nil || !priced.Priced || !almost(priced.Cost, 3.4) {
		t.Fatalf("已定价模型异常: %+v", priced)
	}
	if unpriced == nil || unpriced.Priced || unpriced.Cost != 0 {
		t.Fatalf("未定价模型异常: %+v", unpriced)
	}
	// 密钥榜金额（未鉴权记录单独成组、金额 0）
	var key1 *KeyStat
	for i := range d.Keys {
		if d.Keys[i].KeyID == "k1" {
			key1 = &d.Keys[i]
		}
	}
	if key1 == nil || !almost(key1.Cost, 3.4) {
		t.Fatalf("密钥金额异常: %+v", d.Keys)
	}
	// 会话下钻：合计 + 单会话 + 明细行金额
	drill := stats.SessionDrilldown(7)
	if !almost(drill.Cost, 3.4) {
		t.Fatalf("下钻金额 = %v, want 3.4", drill.Cost)
	}
	if len(drill.Sessions) != 1 || !almost(drill.Sessions[0].Cost, 3.4) {
		t.Fatalf("会话金额异常: %+v", drill.Sessions)
	}
	rows := stats.SessionRequests("s1", 7)
	if len(rows) != 2 {
		t.Fatalf("明细条数 = %d, want 2", len(rows))
	}
	if !rows[0].Priced || !almost(rows[0].Cost, 3.4) {
		t.Fatalf("明细首行金额异常: %+v", rows[0])
	}
	if rows[1].Priced || rows[1].Cost != 0 {
		t.Fatalf("未定价明细行应 priced=false: %+v", rows[1])
	}
}

// 客户端口径：日志里的 input 已含 cacheRead，计价前必须扣除（否则重复计一次）
func TestClientTokenStatsCostDeductsCacheRead(t *testing.T) {
	root := withFakeHome(t)
	nowMs := time.Now().UnixMilli()
	// input 1M 含 cacheRead 0.8M → 未命中 0.2M；out 0.1M；无 cacheWrite
	writeJSONL(t, filepath.Join(root, "cost.jsonl"),
		`{"timestamp":`+i64(nowMs-5000)+`,"cwd":"/Users/tester/proj","providerData":{"model":"glm-5.2"},"message":{"usage":{"input_tokens":1000000,"output_tokens":100000,"cache_read_input_tokens":800000}}}`)

	svc := NewServiceAt(t.TempDir())
	cfg := svc.GetConfig()
	cfg.Models.Prices = map[string]ModelPrice{"glm-5.2": {Input: 1, Output: 4, CacheRead: 0.2, CacheWrite: 2}}
	if err := svc.SaveConfigWith(cfg); err != nil {
		t.Fatalf("写入配置失败: %v", err)
	}

	rep, err := svc.ClientTokenStats(7, true)
	if err != nil {
		t.Fatal(err)
	}
	// (0.2e6×1 + 0.1e6×4 + 0.8e6×0.2)/1e6 = 0.2 + 0.4 + 0.16 = 0.76 元
	// 若未扣除 cacheRead（bug 形态）会算成 1.56，断言能明确分辨
	if !almost(rep.Summary.Cost, 0.76) {
		t.Fatalf("客户端金额 = %v, want 0.76（input 须先扣除 cacheRead）", rep.Summary.Cost)
	}
	if rep.Summary.UnpricedModels != 0 {
		t.Fatalf("未定价模型数 = %d, want 0", rep.Summary.UnpricedModels)
	}
	if len(rep.Models) != 1 || !rep.Models[0].Priced || !almost(rep.Models[0].Cost, 0.76) {
		t.Fatalf("模型金额异常: %+v", rep.Models)
	}
	if len(rep.Projects) != 1 || !almost(rep.Projects[0].Cost, 0.76) {
		t.Fatalf("项目金额异常: %+v", rep.Projects)
	}
	if len(rep.Sessions) != 1 || !almost(rep.Sessions[0].Cost, 0.76) {
		t.Fatalf("会话金额异常: %+v", rep.Sessions)
	}
	if len(rep.Daily) != 1 || !almost(rep.Daily[0].Cost, 0.76) {
		t.Fatalf("逐日金额异常: %+v", rep.Daily)
	}
	// 分源：CN 档位带金额；国际版档位数据根缺失（missing），金额恒为 0
	if len(rep.Sources) != 2 || !almost(rep.Sources[0].Summary.Cost, 0.76) || !almost(rep.Sources[1].Summary.Cost, 0) {
		t.Fatalf("分源金额异常: %+v", rep.Sources)
	}
}

// 生产链路：单价表来自 config（Service.ModelPrices → NewStats provider），
// 且改配置后客户端用量缓存立即失效（金额口径不吃 5 分钟旧缓存）
func TestServicePriceTableWiring(t *testing.T) {
	root := withFakeHome(t)
	svc := newTestService(t)
	// 先落真实记录：一条网关请求日志 + 一条客户端会话日志，再配价
	svc.Store().AppendRequestLog(RequestLog{
		TS: time.Now().Unix(), Time: Now(), KeyID: "k1", KeyName: "k1", Model: "glm-5.2",
		Status: 200, Tokens: 1_000_000, InputTokens: 1_000_000, OutputTokens: 0,
	})
	writeJSONL(t, filepath.Join(root, "wire.jsonl"),
		`{"timestamp":`+i64(time.Now().UnixMilli()-3000)+`,"cwd":"/Users/tester/proj","providerData":{"model":"glm-5.2"},"message":{"usage":{"input_tokens":1000000,"output_tokens":0}}}`)

	if got, want := svc.Stats().Dashboard(7).Overview.Cost, DefaultModelPrices()["glm-5.2"].Input; !almost(got, want) {
		t.Fatalf("内置默认单价表未生效：金额 = %v, want %v（1M 未命中输入 × 默认输入价）", got, want)
	}
	before, err := svc.ClientTokenStats(7, false) // 进缓存，稍后验证是否失效
	if err != nil {
		t.Fatal(err)
	}
	if !almost(before.Summary.Cost, DefaultModelPrices()["glm-5.2"].Input) || before.Summary.UnpricedModels != 0 {
		t.Fatalf("默认价表下客户端报告异常: cost=%v unpriced=%d", before.Summary.Cost, before.Summary.UnpricedModels)
	}

	cfg := svc.GetConfig()
	cfg.Models.Prices = map[string]ModelPrice{"glm-5.2": {Input: 2}}
	if err := svc.UpdateConfig(cfg); err != nil {
		t.Fatalf("更新配置失败: %v", err)
	}
	// 1e6 token × 2 元 / 1e6 = 2 元
	if got := svc.Stats().Dashboard(7).Overview.Cost; !almost(got, 2) {
		t.Fatalf("配价后网关金额 = %v, want 2（单价表须经 ModelPrices 生效）", got)
	}
	// 关键：仍走缓存路径（force=false），金额必须已是新价——否则用户改完单价要等 5 分钟
	after, err := svc.ClientTokenStats(7, false)
	if err != nil {
		t.Fatal(err)
	}
	if !almost(after.Summary.Cost, 2) || after.Summary.UnpricedModels != 0 {
		t.Fatalf("配置变更后客户端缓存未失效: cost=%v unpriced=%d", after.Summary.Cost, after.Summary.UnpricedModels)
	}
}

// 未定价模型：金额为 0，但口径要如实报数（不能静默当 0 元）
func TestClientTokenStatsUnpricedModel(t *testing.T) {
	root := withFakeHome(t)
	nowMs := time.Now().UnixMilli()
	writeJSONL(t, filepath.Join(root, "u.jsonl"),
		`{"timestamp":`+i64(nowMs-5000)+`,"cwd":"/Users/tester/proj","providerData":{"model":"no-price"},"message":{"usage":{"input_tokens":1000,"output_tokens":100}}}`)

	svc := NewServiceAt(t.TempDir())
	cfg := svc.GetConfig()
	cfg.Models.Prices = map[string]ModelPrice{"other": {Input: 1}}
	if err := svc.SaveConfigWith(cfg); err != nil {
		t.Fatalf("写入配置失败: %v", err)
	}
	rep, err := svc.ClientTokenStats(7, true)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Summary.Cost != 0 || rep.Summary.UnpricedModels != 1 {
		t.Fatalf("未定价口径异常: cost=%v unpriced=%d", rep.Summary.Cost, rep.Summary.UnpricedModels)
	}
	if len(rep.Models) != 1 || rep.Models[0].Priced {
		t.Fatalf("未定价模型应标记 priced=false: %+v", rep.Models)
	}
}

// 内置默认单价表：新装开箱可用、不污染用户配置、用户删掉的模型不在重启后复活
func TestDefaultModelPricesInit(t *testing.T) {
	// 实际在用（真实日志里出现过）的模型必须在默认表里，否则金额还是 0
	for _, m := range []string{"hy3", "glm-5.2", "glm-5.3-flash", "deepseek-v4-flash", "deepseek-v4.1-flash"} {
		if _, ok := DefaultModelPrices()[m]; !ok {
			t.Fatalf("默认单价表缺少实际在用模型 %s", m)
		}
	}
	// 默认表不含负价（0 只表示该项免费）
	for m, p := range DefaultModelPrices() {
		if p.Input < 0 || p.Output < 0 || p.CacheRead < 0 || p.CacheWrite < 0 {
			t.Fatalf("默认单价出现负值: %s %+v", m, p)
		}
	}

	dir := t.TempDir()
	// 新装：配置目录为空 → 自动初始化
	if n := len(NewServiceAt(dir).GetConfig().Models.Prices); n == 0 {
		t.Fatal("新装配置未初始化单价表")
	}

	// 用户改一个、删掉其余 → 重启后必须严格等于用户那份（默认表不许按 key 合并回来）
	cfg := NewServiceAt(dir).GetConfig()
	cfg.Models.Prices = map[string]ModelPrice{"hy3": {Input: 9}}
	if err := NewServiceAt(dir).SaveConfigWith(cfg); err != nil {
		t.Fatalf("写入配置失败: %v", err)
	}
	if got := NewServiceAt(dir).GetConfig().Models.Prices; len(got) != 1 || got["hy3"].Input != 9 {
		t.Fatalf("用户单价表被默认表污染: %+v", got)
	}

	// 整表清空 → 视为未初始化，回落默认表（取舍见 DefaultModelPrices 注释）
	cfg2 := NewServiceAt(dir).GetConfig()
	cfg2.Models.Prices = map[string]ModelPrice{}
	if err := NewServiceAt(dir).SaveConfigWith(cfg2); err != nil {
		t.Fatalf("写入配置失败: %v", err)
	}
	if len(NewServiceAt(dir).GetConfig().Models.Prices) == 0 {
		t.Fatal("空单价表应回落内置默认表")
	}
}

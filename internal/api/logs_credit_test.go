package api

import (
	"context"
	"testing"
	"time"

	"workbuddy-desktop/internal/core"
)

// TestGetCreditDetailAggregates 领取积分明细的汇总口径：
// 今日 / 近 7 天跟随维度筛选但不随时间范围变化，范围内按任务与按天聚合，
// 未返回积分数值的执行记录只计数、不写 0 充数。
func TestGetCreditDetailAggregates(t *testing.T) {
	svc := core.NewServiceAt(t.TempDir())
	api := NewLogsAPI(svc)
	now := time.Now()

	add := func(typ string, credits int, at time.Time, msg string) {
		svc.Store().AppendTaskLog(core.TaskLog{
			ID: core.NewID("t"), TS: at.Unix(), Time: at.Format("2006-01-02 15:04:05"),
			Type: typ, Trigger: "manual", UID: "u1", Status: core.TaskSuccess,
			Message: msg, Credits: credits,
		})
	}
	add(core.TaskCheckin, 12, now, "签到成功（本次领取 12 分）")
	add(core.TaskGrowth, 30, now.Add(-time.Minute), "领取 1 个奖励(+30分)")
	add(core.TaskGrowth, 8, now.AddDate(0, 0, -3), "领取 1 个奖励(+8分)")
	add(core.TaskCheckin, 5, now.AddDate(0, 0, -20), "签到成功（本次领取 5 分）")
	// 上游未返回数值的领取动作（开学季领奖）：不得计入积分，但必须被计数
	add(core.TaskSchool, 0, now.Add(-2*time.Minute), "开学季任务处理 2 个，领取 2 个")

	got, err := api.GetCreditDetail(context.Background(), LogQuery{})
	if err != nil {
		t.Fatalf("GetCreditDetail 失败: %v", err)
	}
	if got.Today != 42 {
		t.Fatalf("今日领取应为 42（12+30），got %d", got.Today)
	}
	if got.Last7d != 50 {
		t.Fatalf("近 7 天领取应为 50（42+8，不含 20 天前），got %d", got.Last7d)
	}
	if got.Credits != 55 {
		t.Fatalf("范围内合计应为 55（12+30+8+5），got %d", got.Credits)
	}
	if got.Runs != 5 {
		t.Fatalf("执行记录应为 5 条，got %d", got.Runs)
	}
	if got.NoAmount != 1 {
		t.Fatalf("未返回数值的记录应为 1 条（开学季），got %d", got.NoAmount)
	}
	if got.ItemHits != 4 || len(got.Items) != 4 {
		t.Fatalf("有积分的明细应为 4 条，ItemHits=%d len=%d", got.ItemHits, len(got.Items))
	}

	// ByTask：按积分降序（growth 38 → checkin 17），不依赖 map 遍历顺序
	if len(got.ByTask) != 2 {
		t.Fatalf("按任务汇总应为 2 组，got %d", len(got.ByTask))
	}
	if got.ByTask[0].Type != core.TaskGrowth || got.ByTask[0].Credits != 38 || got.ByTask[0].Count != 2 {
		t.Fatalf("第 1 组应为 growth/38分/2次，got %+v", got.ByTask[0])
	}
	if got.ByTask[1].Type != core.TaskCheckin || got.ByTask[1].Credits != 17 || got.ByTask[1].Count != 2 {
		t.Fatalf("第 2 组应为 checkin/17分/2次，got %+v", got.ByTask[1])
	}

	// ByDay：按日期降序，最新在前
	if len(got.ByDay) != 3 {
		t.Fatalf("按天汇总应为 3 组，got %d", len(got.ByDay))
	}
	if got.ByDay[0].Credits != 42 {
		t.Fatalf("最新一天应为 42 分，got %+v", got.ByDay[0])
	}
	for i := 1; i < len(got.ByDay); i++ {
		if got.ByDay[i-1].Date < got.ByDay[i].Date {
			t.Fatalf("按天汇总未按日期降序: %v", got.ByDay)
		}
	}
}

// TestGetCreditDetailFilterAndPaging 类型/关键字过滤与分页；
// 今日卡跟随维度筛选（不再固定看全量），但仍不受时间范围影响。
func TestGetCreditDetailFilterAndPaging(t *testing.T) {
	svc := core.NewServiceAt(t.TempDir())
	api := NewLogsAPI(svc)
	now := time.Now()

	for i, c := range []struct {
		typ     string
		credits int
	}{
		{core.TaskGrowth, 30},
		{core.TaskGrowth, 8},
		{core.TaskCheckin, 12},
		{core.TaskCheckin, 5},
	} {
		svc.Store().AppendTaskLog(core.TaskLog{
			ID: core.NewID("t"), TS: now.Add(-time.Duration(i) * time.Minute).Unix(),
			Time: now.Format("2006-01-02 15:04:05"), Type: c.typ, Trigger: "manual",
			UID: "u1", Status: core.TaskSuccess, Message: "m", Credits: c.credits,
		})
	}

	filtered, err := api.GetCreditDetail(context.Background(), LogQuery{Type: core.TaskGrowth})
	if err != nil {
		t.Fatalf("GetCreditDetail 失败: %v", err)
	}
	if filtered.Credits != 38 || filtered.Runs != 2 || filtered.ItemHits != 2 {
		t.Fatalf("按 growth 过滤应为 38分/2条，got %d分 runs=%d items=%d",
			filtered.Credits, filtered.Runs, filtered.ItemHits)
	}
	// 今日卡跟随维度筛选：只看 growth 的今日（30+8），而不是全量 55
	if filtered.Today != 38 {
		t.Fatalf("今日卡应跟随类型筛选（growth 30+8=38），got %d", filtered.Today)
	}
	// 但时间范围不该影响今日卡：把范围切成「近 2 分钟」后今日卡仍是 38
	since, err := api.GetCreditDetail(context.Background(), LogQuery{Type: core.TaskGrowth, From: now.Add(-2 * time.Minute).Unix()})
	if err != nil {
		t.Fatalf("GetCreditDetail 带时间范围失败: %v", err)
	}
	if since.Today != 38 {
		t.Fatalf("今日卡不应随时间范围变化（仍应为 38），got %d", since.Today)
	}
	if since.Credits != 38 || since.Runs != 2 {
		t.Fatalf("范围合计应为 38分/2条（两条 growth 都在 2 分钟内），got %d分 runs=%d",
			since.Credits, since.Runs)
	}

	paged, err := api.GetCreditDetail(context.Background(), LogQuery{Page: 2, PageSize: 2})
	if err != nil {
		t.Fatalf("GetCreditDetail 分页失败: %v", err)
	}
	if len(paged.Items) != 2 || paged.ItemHits != 4 {
		t.Fatalf("第 2 页应返回 2 条、命中 4 条，got len=%d hits=%d", len(paged.Items), paged.ItemHits)
	}
	if paged.Page != 2 || paged.PageSize != 2 {
		t.Fatalf("分页参数回显异常: page=%d size=%d", paged.Page, paged.PageSize)
	}
}

// TestGetCreditDetailByUID 账号维度的领取明细（账号管理弹窗用的就是这条路径）：
// 三张卡、按任务汇总、明细列表全部只算该账号，别的账号一条都不能混进来。
func TestGetCreditDetailByUID(t *testing.T) {
	svc := core.NewServiceAt(t.TempDir())
	api := NewLogsAPI(svc)
	now := time.Now()
	// 锚定当日零点而非 now-偏移：now-1h 在零点后一小时内跑测试会落到昨天，「今日」断言随之崩掉
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())

	add := func(uid, typ string, credits int, at time.Time) {
		svc.Store().AppendTaskLog(core.TaskLog{
			ID: core.NewID("t"), TS: at.Unix(), Time: at.Format("2006-01-02 15:04:05"),
			Type: typ, Trigger: "schedule", UID: uid, Status: core.TaskSuccess,
			Message: uid + " 的动作", Credits: credits,
		})
	}
	add("uA", core.TaskGrowth, 30, dayStart.Add(time.Hour))
	add("uA", core.TaskCheckin, 12, dayStart.Add(2*time.Hour))
	add("uA", core.TaskSchool, 0, dayStart.Add(time.Hour).Add(time.Minute)) // 未返回数值：计数不计分
	add("uB", core.TaskGrowth, 99, now)                                    // 干扰账号，必须被排除
	add("uA", core.TaskGrowth, 7, now.AddDate(0, 0, -30))
	add("uB", core.TaskCheckin, 55, now.AddDate(0, 0, -30))

	got, err := api.GetCreditDetail(context.Background(), LogQuery{UID: "uA"})
	if err != nil {
		t.Fatalf("GetCreditDetail 失败: %v", err)
	}
	if got.Today != 42 {
		t.Fatalf("uA 今日应为 42（30+12），got %d", got.Today)
	}
	if got.Credits != 49 {
		t.Fatalf("uA 范围合计应为 49（30+12+7），got %d", got.Credits)
	}
	if got.Runs != 4 {
		t.Fatalf("uA 执行记录应为 4 条，got %d", got.Runs)
	}
	if got.NoAmount != 1 {
		t.Fatalf("uA 未返回数值的记录应为 1 条，got %d", got.NoAmount)
	}
	if got.ItemHits != 3 || len(got.Items) != 3 {
		t.Fatalf("uA 有积分的明细应为 3 条，ItemHits=%d len=%d", got.ItemHits, len(got.Items))
	}
	for _, it := range got.Items {
		if it.UID != "uA" {
			t.Fatalf("明细混入了其它账号: %+v", it)
		}
	}
	if len(got.ByTask) != 2 || got.ByTask[0].Type != core.TaskGrowth || got.ByTask[0].Credits != 37 {
		t.Fatalf("uA 按任务汇总应为 growth/37分 打头，got %+v", got.ByTask)
	}

	// uid 不存在时返回全 0 的空结构，而不是报错或回落全量
	empty, err := api.GetCreditDetail(context.Background(), LogQuery{UID: "nobody"})
	if err != nil {
		t.Fatalf("不存在的账号不应报错: %v", err)
	}
	if empty.Today != 0 || empty.Last7d != 0 || empty.Credits != 0 || empty.Runs != 0 || len(empty.Items) != 0 {
		t.Fatalf("不存在的账号应全 0，got %+v", empty)
	}

	// 任务日志同样支持 uid 精确过滤（列表页按账号下钻）
	logs, err := api.GetTaskLogs(context.Background(), LogQuery{UID: "uB"})
	if err != nil {
		t.Fatalf("GetTaskLogs 失败: %v", err)
	}
	if logs.Total != 2 {
		t.Fatalf("uB 任务日志应为 2 条，got %d", logs.Total)
	}
	for _, it := range logs.Items {
		if it.UID != "uB" {
			t.Fatalf("任务日志混入了其它账号: %+v", it)
		}
	}
}

// TestGetCreditDetailObserved 观测入账口径：积分流水里余额上升（delta<0）的条目
// 单列进 Observed，不与确认领取混算；消耗（delta>0）绝不计入；Type 筛选时返回空。
func TestGetCreditDetailObserved(t *testing.T) {
	svc := core.NewServiceAt(t.TempDir())
	api := NewLogsAPI(svc)
	now := time.Now()
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())

	addLog := func(uid string, delta float64, at time.Time) {
		svc.Store().AppendCreditLog(core.CreditLog{
			ID: core.NewID("c"), TS: at.Unix(), Time: at.Format("2006-01-02 15:04:05"),
			UID: uid, Delta: delta, Balance: 100 + delta,
		})
	}
	addLog("uA", -24, dayStart.Add(time.Hour))  // 今日入账 24（上游异步发放）
	addLog("uA", -6, now.AddDate(0, 0, -3))     // 近 7 天入账 6
	addLog("uA", 4, now.Add(-time.Minute))      // 消耗：绝不能算进观测入账
	addLog("uA", -50, now.AddDate(0, 0, -30))   // 范围外（默认全量也含它，见下）
	addLog("uB", -99, dayStart.Add(time.Hour))  // 干扰账号
	svc.Store().AppendTaskLog(core.TaskLog{ // 确认领取：不得混进观测入账
		ID: core.NewID("t"), TS: now.Unix(), Time: now.Format("2006-01-02 15:04:05"),
		Type: core.TaskGrowth, Trigger: "manual", UID: "uA", Status: core.TaskSuccess,
		Message: "m", Credits: 30,
	})

	got, err := api.GetCreditDetail(context.Background(), LogQuery{})
	if err != nil {
		t.Fatalf("GetCreditDetail 失败: %v", err)
	}
	// 不带 uid 筛选时是全局口径（uA + uB 都算）
	if got.ObservedToday != 123 {
		t.Fatalf("今日观测入账应为 123（uA 24 + uB 99），got %d", got.ObservedToday)
	}
	if got.ObservedLast7d != 129 {
		t.Fatalf("近 7 天观测入账应为 129（uA 24+6 + uB 99），got %d", got.ObservedLast7d)
	}
	if got.ObservedCredits != 179 {
		t.Fatalf("范围内观测入账应为 179（24+6+50+99），got %d", got.ObservedCredits)
	}
	for _, it := range got.Observed {
		if it.Delta >= 0 {
			t.Fatalf("观测入账混入了消耗条目: %+v", it)
		}
	}
	if got.Credits != 30 {
		t.Fatalf("确认领取必须与观测入账分账（应为 30），got %d", got.Credits)
	}

	// 账号维度过滤：只看 uB
	byUID, err := api.GetCreditDetail(context.Background(), LogQuery{UID: "uB"})
	if err != nil {
		t.Fatalf("GetCreditDetail(uid) 失败: %v", err)
	}
	if byUID.ObservedCredits != 99 || byUID.ObservedToday != 99 {
		t.Fatalf("uB 观测入账应为 99，got range=%d today=%d", byUID.ObservedCredits, byUID.ObservedToday)
	}

	// 类型筛选：观测入账没有任务类型，返回空而不是硬凑
	byType, err := api.GetCreditDetail(context.Background(), LogQuery{Type: core.TaskGrowth})
	if err != nil {
		t.Fatalf("GetCreditDetail(type) 失败: %v", err)
	}
	if len(byType.Observed) != 0 || byType.ObservedCredits != 0 {
		t.Fatalf("带类型筛选时观测入账应为空，got %+v", byType.Observed)
	}
}

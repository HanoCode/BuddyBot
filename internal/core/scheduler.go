package core

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// 任务类型
const (
	TaskCheckin   = "checkin"
	TaskTravel    = "travel"
	TaskKeepalive = "keepalive"
	TaskActivity  = "activity" // 活跃地图
	TaskSchool    = "school"   // 开学季
	TaskCat       = "cat"      // 夜猫子
	TaskGrowth    = "growth"   // 成长任务中心
)

// 任务状态
const (
	TaskSuccess = "success"
	TaskFailed  = "failed"
	TaskSkipped = "skipped"
)

// TaskRunDetail 单账号执行明细
type TaskRunDetail struct {
	UID     string `json:"uid"`
	Status  string `json:"status"`
	Message string `json:"message"`
	// Credits 该账号本次领取到的积分合计（动作级，口径见 TaskLog.Credits 注释）
	Credits int `json:"credits"`
}

// TaskRun 一次任务运行的真实结果
type TaskRun struct {
	Type      string          `json:"type"`
	Trigger   string          `json:"trigger"` // manual / schedule
	StartedAt string          `json:"startedAt"`
	Duration  float64         `json:"duration"` // ms
	Total     int             `json:"total"`
	Success   int             `json:"success"`
	Failed    int             `json:"failed"`
	Skipped   int             `json:"skipped"`
	Credits   int             `json:"credits"` // 本轮全部账号领取到的积分合计
	Details   []TaskRunDetail `json:"details"`
}

// NextRun 排程的下次执行时间
type NextRun struct {
	Type    string `json:"type"`
	Enabled bool   `json:"enabled"`
	Hours   []int  `json:"hours"`
	Next    string `json:"next"` // RFC3339；空 = 已禁用
}

// SchedulerStatus 调度器运行态
type SchedulerStatus struct {
	Running  bool      `json:"running"`
	NextRuns []NextRun `json:"nextRuns"`
	LastRun  *TaskRun  `json:"lastRun,omitempty"`
	History  []TaskRun `json:"history"`
	Busy     []string  `json:"busy"` // 正在执行中的任务类型（手动/排程共用）
}

// Scheduler 定时任务调度器。
//
// 真实行为：按配置的小时整点触发（每个时点每天一次），逐账号执行并写任务日志。
// 各任务的真实能力：
//   - checkin：调用上游每日签到端点 + 兑换已达标的连登奖励档位，结果如实上报；
//   - travel：猫猫旅行巡检状态机（无猫领养 / 空闲派出 / 到站领奖，global 账号无此体系）；
//   - keepalive：临期时调上游刷新端点轮换 token 并原子写回凭证文件，顺带刷新真实余额；
//   - 账号级真实状态（熔断冷却到期、token 过期）会被如实记录为真实结果。
type Scheduler struct {
	svc *Service
	up  *upstreamClient

	mu         sync.Mutex
	cancel     context.CancelFunc
	running    bool
	fired      map[string]string // 任务类型 → 最近触发标记 "2006-01-02 15"
	busy       map[string]bool   // 任务类型 → 正在执行
	history    []TaskRun         // 最近若干次运行
	lastRun    *TaskRun
	adoptTried map[string]string // uid → 当日已判定领养门槛未达（CST 日期）
	lastRefresh time.Time        // 定时刷新：上次执行时刻（零值 = 尚未执行）
	creditNotified map[string]string // uid → 最近一次积分预警通知的标记（CST 日期，防重复提醒）
}

const maxHistory = 20

// defaultRunBudgetMinutes 单轮全池任务默认时间预算（分钟）
const defaultRunBudgetMinutes = 10

// travelLocationID 派出地点固定 4（古镇客栈）：4 个地点收益/时长区间完全相同。
const travelLocationID = 4

// cstZone 上游每日重置按自然日 00:00 CST（Asia/Shanghai）。
var cstZone = time.FixedZone("CST", 8*60*60)

func travelDay(t time.Time) string { return t.In(cstZone).Format("2006-01-02") }

// NewScheduler 创建调度器
func NewScheduler(svc *Service) *Scheduler {
	return &Scheduler{
		svc:        svc,
		up:         newUpstreamClient(),
		fired:      map[string]string{},
		busy:       map[string]bool{},
		history:    []TaskRun{},
		adoptTried: map[string]string{},
		creditNotified: map[string]string{},
	}
}

// Start 启动调度循环（幂等）
func (s *Scheduler) Start(ctx context.Context) {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	runCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.running = true
	s.mu.Unlock()

	go s.loop(runCtx)
	go s.expiryPatrolLoop(runCtx)
	go s.refreshLoop(runCtx)
	go s.dailyReportLoop(runCtx)
	go s.modelProbeLoop(runCtx)
	go s.sessionArchiveLoop(runCtx)
}

// expiryPatrolInterval 积分到期巡检周期（对齐 switch-gateway：15 分钟）。
const expiryPatrolInterval = 15 * time.Minute

// expiryPatrolLoop 周期巡检各在线账号的余额与最早到期日（驱动网关「先烧快过期
// 积分」的分层选号）。只刷新余额与到期日，不签到、不改账号状态；账号间 300ms
// 间隔防风控。查询失败静默（下一轮再试）。
func (s *Scheduler) expiryPatrolLoop(ctx context.Context) {
	ticker := time.NewTicker(expiryPatrolInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.patrolExpiry()
		}
	}
}

// sessionArchiveInterval 会话自动归档巡检周期：30 分钟（低频写外部库，足够及时）
const sessionArchiveInterval = 30 * time.Minute

// sessionArchiveLoop 会话自动归档巡检：把官方客户端中空闲超过阈值的
// 已结束会话标记为 archived。开关关闭或目标库不存在时静默跳过；
// 失败写事件日志（下一轮再试），不参与任务日志/推送体系（非账号型任务）。
func (s *Scheduler) sessionArchiveLoop(ctx context.Context) {
	ticker := time.NewTicker(sessionArchiveInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cfg := s.svc.GetConfig()
			if !cfg.Schedule.SessionArchive.Enabled {
				continue
			}
			if n, err := ArchiveIdleSessions(s.svc); err != nil {
				EmitEvent(EventGatewayLog, map[string]any{
					"level": "error", "message": "会话自动归档失败: " + err.Error(),
				})
			} else if n > 0 {
				EmitEvent(EventGatewayLog, map[string]any{
					"level": "info", "message": fmt.Sprintf("会话自动归档：已归档 %d 个空闲会话", n),
				})
			}
		}
	}
}

// RunSessionArchiveNow 立即执行一轮会话归档（设置页手动触发入口，
// 不受开关限制：用户明确点了按钮就该跑一次）
func (s *Scheduler) RunSessionArchiveNow() (int64, error) {
	return ArchiveIdleSessions(s.svc)
}

// patrolIntervalMs 巡逻/刷新类请求的账号启动间隔（防风控节流口径，保持与历史
// 串行版本一致的全局请求速率：每 300ms 至多发起一个上游请求）。
const patrolIntervalMs = 300

// forEachAccountPaced 并发巡逻同一套账号遍历的节流骨架：启动侧按
// patrolIntervalMs 间隔逐个派发（全局请求速率与串行版一致），但**不等待响应**
// ——上游余额/模型查询的响应耗时（常 >1s）不再串进下一账号的启动等待里。
// 账号数较多时一轮巡逻的墙钟时间从 N×(间隔+响应) 降为 N×间隔+一轮响应；
// patrolConcurrency 封顶同时在途请求数，防响应堆积。
const patrolConcurrency = 4

func (s *Scheduler) forEachAccountPaced(accounts []Account, fetch func(a Account, cred *UpstreamCred)) {
	sem := make(chan struct{}, patrolConcurrency)
	var wg sync.WaitGroup
	started := 0
	for _, a := range accounts {
		if a.Status != "online" {
			continue
		}
		if started > 0 {
			time.Sleep(patrolIntervalMs * time.Millisecond) // 启动间隔防风控
		}
		started++
		sem <- struct{}{}
		wg.Add(1)
		go func(a Account) {
			defer func() { <-sem; wg.Done() }()
			cred, err := LoadUpstreamCred(s.svc.AuthDir(), a.Credential)
			if err != nil {
				return
			}
			fetch(a, cred)
		}(a)
	}
	wg.Wait()
}

func (s *Scheduler) patrolExpiry() {
	s.forEachAccountPaced(s.svc.Accounts(), func(a Account, cred *UpstreamCred) {
		if d, err := s.up.FetchBalanceDetail(cred, expiringCreditWindow); err == nil {
			s.applyBalance(a.UID, d)
		}
	})
}

// refreshInterval 解析定时刷新间隔配置（分钟）；<=0 回落默认值。
func refreshInterval(minutes int) time.Duration {
	if minutes <= 0 {
		minutes = DefaultRefreshIntervalMinutes
	}
	return time.Duration(minutes) * time.Minute
}

// refreshLoop 定时刷新：按设置中的间隔自动刷新需要手工刷新的内容——
// 各在线账号的积分余额与上游模型目录（等效于把「查余额 / 深度刷新」交给
// 调度器定时做）。token 轮换不在此处：仍由 keepalive 定时任务按临期窗口处理。
// 账号间 300ms 间隔防风控；失败静默（下一轮再试），余额成功后经 balance 事件
// 自动更新前端列表。
func (s *Scheduler) refreshLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.refreshTick()
		}
	}
}

func (s *Scheduler) refreshTick() {
	cfg := s.svc.GetConfig()
	rc := cfg.Schedule.Refresh
	if !rc.Enabled {
		return
	}
	s.mu.Lock()
	due := s.lastRefresh.IsZero() || time.Since(s.lastRefresh) >= refreshInterval(rc.IntervalMinutes)
	if due {
		s.lastRefresh = time.Now()
	}
	s.mu.Unlock()
	if due {
		go s.refreshAll()
	}
}

func (s *Scheduler) refreshAll() {
	s.forEachAccountPaced(s.svc.Accounts(), func(a Account, cred *UpstreamCred) {
		if d, err := s.up.FetchBalanceDetail(cred, expiringCreditWindow); err == nil {
			s.applyBalance(a.UID, d)
		}
		if infos, err := s.up.FetchModels(cred); err == nil {
			s.svc.Store().MergeUpstreamModels(infos)
		}
	})
}

// Stop 停止调度循环
func (s *Scheduler) Stop() {
	s.mu.Lock()
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	s.running = false
	s.mu.Unlock()
}

// IsRunning 是否运行中
func (s *Scheduler) IsRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

// loop 每 30 秒检查一次是否命中配置时点（步长远小于 1 小时，不会漏点）
func (s *Scheduler) loop(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.tick()
		}
	}
}

func (s *Scheduler) tick() {
	now := time.Now()
	stamp := now.Format("2006-01-02 15")
	cfg := s.svc.GetConfig()
	for _, t := range scheduleTable(cfg) {
		if !t.Enabled || len(t.Hours) == 0 {
			continue
		}
		if !containsInt(t.Hours, now.Hour()) {
			continue
		}
		s.mu.Lock()
		already := s.fired[t.Type] == stamp
		if !already {
			s.fired[t.Type] = stamp
		}
		s.mu.Unlock()
		if already {
			continue
		}
		go s.Run(t.Type, "schedule")
	}
}

type scheduleEntry struct {
	Type    string
	Enabled bool
	Hours   []int
}

func scheduleTable(cfg *Config) []scheduleEntry {
	return []scheduleEntry{
		{TaskCheckin, cfg.Schedule.CheckinEnabled, cfg.Schedule.CheckinHours},
		{TaskTravel, cfg.Schedule.TravelEnabled, cfg.Schedule.TravelHours},
		{TaskKeepalive, cfg.Schedule.KeepaliveEnabled, cfg.Schedule.KeepaliveHours},
		{TaskActivity, cfg.Schedule.ActivityEnabled, cfg.Schedule.ActivityHours},
		{TaskSchool, cfg.Schedule.SchoolEnabled, cfg.Schedule.SchoolHours},
		{TaskCat, cfg.Schedule.CatEnabled, cfg.Schedule.CatHours},
	}
}

func containsInt(xs []int, v int) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

// Run 执行一次指定任务（手动或排程触发），作用于全部账号。
func (s *Scheduler) Run(taskType, trigger string) TaskRun {
	return s.RunFor(taskType, trigger, nil)
}

// RunFor 执行一次指定任务；uids 非空时只作用于这些账号。
// 手工触发与排程互斥（同一任务类型串行）。
func (s *Scheduler) RunFor(taskType, trigger string, uids []string) TaskRun {
	return s.runForAccounts(taskType, trigger, uids, func(a Account) TaskRunDetail {
		return s.execute(taskType, a)
	})
}

// runForAccounts 通用执行器：对全部（或指定）账号逐个执行 exec 并记录日志。
// 每个账号执行完即推送 task:progress 事件（对齐 panel 任务队列的逐项可见性：
// 前端无需轮询即可实时看到第几个账号、什么状态、什么原因）。
func (s *Scheduler) runForAccounts(taskType, trigger string, uids []string, exec func(Account) TaskRunDetail) TaskRun {
	s.mu.Lock()
	if s.busy[taskType] {
		s.mu.Unlock()
		run := TaskRun{Type: taskType, Trigger: trigger, StartedAt: Now(),
			Details: []TaskRunDetail{{Status: TaskSkipped, Message: "上一次执行尚未结束，本次跳过"}}}
		run.Skipped = 1
		run.Total = 1
		// 异步提交场景下前端只认事件：跳过也要广播完成，否则提交方永远等不到回音
		EmitEvent(EventTaskCompleted, map[string]any{
			"type": taskType, "trigger": trigger, "total": 1,
			"success": 0, "failed": 0, "skipped": 1,
		})
		return run
	}
	s.busy[taskType] = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.busy[taskType] = false
		s.mu.Unlock()
	}()

	only := map[string]bool{}
	for _, u := range uids {
		only[u] = true
	}

	// 预收集目标账号：总数用于进度事件的 index/total 口径
	targets := make([]Account, 0)
	for _, a := range s.svc.Accounts() {
		if len(only) == 0 || only[a.UID] {
			targets = append(targets, a)
		}
	}

	start := time.Now()
	run := TaskRun{Type: taskType, Trigger: trigger, StartedAt: Now(), Details: []TaskRunDetail{}}
	// 时间预算只约束全池轮询（手动勾选的少量账号预期跑完，不设预算）
	var deadline time.Time
	if len(only) == 0 {
		deadline = s.runBudgetDeadline(start)
	}
	budgetHit := false
	for i, a := range targets {
		// 时间预算：超预算后剩余账号不再执行，直接跳过（防止单轮长任务跑穿）
		if !budgetHit && !deadline.IsZero() && time.Now().After(deadline) {
			budgetHit = true
		}
		detail := TaskRunDetail{UID: a.UID}
		if budgetHit {
			detail.Status = TaskSkipped
			detail.Message = "本轮时间预算已耗尽，跳过"
		} else {
			detail = exec(a)
		}
		run.Details = append(run.Details, detail)
		run.Total++
		run.Credits += detail.Credits
		switch detail.Status {
		case TaskSuccess:
			run.Success++
		case TaskFailed:
			run.Failed++
		default:
			run.Skipped++
		}
		s.svc.Store().AppendTaskLog(TaskLog{
			ID: NewID("t"), TS: time.Now().Unix(), Time: Now(), Type: taskType,
			Trigger: trigger, UID: a.UID, Status: detail.Status,
			Message: detail.Message, Duration: float64(time.Since(start).Microseconds()) / 1000.0,
			Credits: detail.Credits,
		})
		EmitEvent(EventTaskProgress, map[string]any{
			"type": taskType, "trigger": trigger,
			"index": i + 1, "total": len(targets),
			"uid": a.UID, "status": detail.Status, "message": detail.Message,
			"credits": detail.Credits,
		})
	}
	run.Duration = float64(time.Since(start).Microseconds()) / 1000.0

	// 推送通知（对齐 workbuddy2api notify：任务完成后按配置推 PushPlus/Bark）
	s.notifyRun(&run)

	s.mu.Lock()
	s.history = append([]TaskRun{run}, s.history...)
	if len(s.history) > maxHistory {
		s.history = s.history[:maxHistory]
	}
	s.lastRun = &run
	s.mu.Unlock()

	EmitEvent(EventTaskCompleted, map[string]any{
		"type": taskType, "trigger": trigger, "total": run.Total,
		"success": run.Success, "failed": run.Failed, "skipped": run.Skipped,
		"credits": run.Credits,
	})
	s.svc.MirrorPushState()
	return run
}

// runBudgetDeadline 计算本轮任务的截止时间；无预算（负数配置）返回零值。
func (s *Scheduler) runBudgetDeadline(start time.Time) time.Time {
	minutes := s.svc.GetConfig().Schedule.RunBudgetMinutes
	if minutes < 0 {
		return time.Time{}
	}
	if minutes == 0 {
		minutes = defaultRunBudgetMinutes
	}
	return start.Add(time.Duration(minutes) * time.Minute)
}

// expiringSoonWindow token 有效期预警窗口（对齐 workbuddy2api pool.expiring_soon 的 168h 默认值）
const expiringSoonWindow = 168 * time.Hour

// expiringCreditWindow 余额「临期作废」预警窗口：72h 内到期的剩余积分单列展示。
const expiringCreditWindow = 72 * time.Hour

// maxLotteryDraws 单账号单轮抽奖上限（防上游计数异常导致死循环；正常远小于此值）。
const maxLotteryDraws = 10

// execute 对单个账号执行任务，返回真实结果。
//
// 结果只反映「本应用实际做到的事」：
//   - 真实可判定 → success（例如熔断冷却到期恢复、签到成功、token 刷新成功）
//   - 真实失败   → failed（凭证无效 / 上游拒绝 / token 过期且无法刷新）
//   - 需要上游但未接入（travel）→ skipped，原因写明（不做假成功）
func (s *Scheduler) execute(taskType string, a Account) TaskRunDetail {
	st := s.svc.Store().AccountState(a.UID)
	now := time.Now()

	switch a.Status {
	case "disabled":
		return TaskRunDetail{UID: a.UID, Status: TaskSkipped, Message: "账号已禁用，未参与任务"}
	case "invalid":
		return TaskRunDetail{UID: a.UID, Status: TaskFailed, Message: "凭证不可用：" + a.Note}
	}

	// 熔断冷却：只看冷却标记本身，不受当前状态推导影响
	if st.CooldownUntil != "" {
		if t, err := time.Parse(time.RFC3339, st.CooldownUntil); err == nil {
			if t.After(now) {
				return TaskRunDetail{UID: a.UID, Status: TaskSkipped,
					Message: fmt.Sprintf("熔断冷却中，剩余 %s", t.Sub(now).Round(time.Second))}
			}
			// 冷却已到期：这是本应用能真实完成的动作——恢复账号
			s.svc.Store().MutateAccountState(a.UID, func(s2 *AccountState) {
				s2.CooldownUntil = ""
				s2.FailStreak = 0
			})
			EmitEvent(EventAccountStatus, map[string]any{"uid": a.UID, "status": "online"})
			return TaskRunDetail{UID: a.UID, Status: TaskSuccess, Message: "熔断冷却到期，账号已恢复可用"}
		}
	}

	// keepalive：token 过期恰好是需要刷新的信号，交给 doKeepalive 处理，不提前判死
	if taskType == TaskKeepalive {
		return s.doKeepalive(a)
	}

	if a.Status == "expired" {
		return TaskRunDetail{UID: a.UID, Status: TaskFailed,
			Message: fmt.Sprintf("access token 已于 %s 过期，需刷新或重新授权", formatExpiry(a.TokenExpiry))}
	}
	if !a.HasToken && !a.HasRefresh {
		return TaskRunDetail{UID: a.UID, Status: TaskFailed, Message: "凭证缺少 accessToken / refreshToken"}
	}

	// token 临期（微信扫码登录 token 时效仅 7 天，登录后即处于 168h 窗口内）：
	// 不判死，先用 refreshToken 自动续期，续期成功继续执行任务
	if a.ExpiresIn > 0 && a.ExpiresIn < int64(expiringSoonWindow.Seconds()) {
		if det, ok := s.tryRefresh(a); !ok {
			return det
		}
	}

	switch taskType {
	case TaskCheckin:
		return s.doCheckin(a)
	case TaskTravel:
		return s.doTravel(a)
	case TaskActivity, TaskSchool, TaskCat:
		cred, err := LoadUpstreamCred(s.svc.AuthDir(), a.Credential)
		if err != nil {
			return TaskRunDetail{UID: a.UID, Status: TaskFailed, Message: "凭证读取失败: " + err.Error()}
		}
		switch taskType {
		case TaskActivity:
			return s.doActivity(a, cred)
		case TaskSchool:
			return s.doSchool(a, cred)
		default:
			return s.doCat(a, cred)
		}
	default:
		return TaskRunDetail{UID: a.UID, Status: TaskSkipped, Message: "未知任务类型：" + taskType}
	}
}

// doTravel 猫猫旅行巡检状态机（单账号单趟：查猫 + 查状态 + 最多一个动作）。
// global 账号无旅行体系（D4 门控），不发任何上游调用。
func (s *Scheduler) doTravel(a Account) TaskRunDetail {
	if a.Realm == "global" {
		return TaskRunDetail{UID: a.UID, Status: TaskSkipped, Message: "国际版账号无猫猫旅行体系，跳过"}
	}
	cred, err := LoadUpstreamCred(s.svc.AuthDir(), a.Credential)
	if err != nil {
		return TaskRunDetail{UID: a.UID, Status: TaskFailed, Message: "凭证读取失败: " + err.Error()}
	}
	buddy, err := s.up.BuddyInfo(cred)
	if err != nil {
		return TaskRunDetail{UID: a.UID, Status: TaskFailed, Message: "查询猫档案失败: " + err.Error()}
	}
	if buddy == nil {
		return s.adoptBuddy(a, cred)
	}
	ts, err := s.up.TravelStatus(cred)
	if err != nil {
		return TaskRunDetail{UID: a.UID, Status: TaskFailed, Message: "查询旅行状态失败: " + err.Error()}
	}
	switch ts.State {
	case "arrived":
		if ts.RecordID == 0 {
			return TaskRunDetail{UID: a.UID, Status: TaskSkipped, Message: "已到站但缺少 record_id，无法领奖"}
		}
		reward, err := s.up.TravelClaim(cred, ts.RecordID)
		if err != nil {
			return TaskRunDetail{UID: a.UID, Status: TaskFailed,
				Message: fmt.Sprintf("领奖失败（record=%d）: %v", ts.RecordID, err)}
		}
		return TaskRunDetail{UID: a.UID, Status: TaskSuccess,
			Message: fmt.Sprintf("旅行到站领奖成功（record=%d，+%d 积分）", ts.RecordID, reward)}
	case "idle":
		if ts.DailyLimitReached {
			return TaskRunDetail{UID: a.UID, Status: TaskSkipped, Message: "今日已派出过，等待到站"}
		}
		// 目的地必须来自 travel/config 清单（伪造 id 会被上游拒 400 invalid request）；
		// 拉取失败时回退到历史实测可用的固定地点
		locID, locName, cerr := s.up.TravelConfigLocation(cred)
		if cerr != nil {
			locID, locName = travelLocationID, ""
		}
		name, err := s.up.TravelDepart(cred, locID)
		if err != nil {
			return TaskRunDetail{UID: a.UID, Status: TaskFailed, Message: "派出失败: " + err.Error()}
		}
		if name == "" {
			name = locName
		}
		if name != "" {
			return TaskRunDetail{UID: a.UID, Status: TaskSuccess,
				Message: fmt.Sprintf("已派出猫旅行（%s），等待到站", name)}
		}
		return TaskRunDetail{UID: a.UID, Status: TaskSuccess,
			Message: fmt.Sprintf("已派出猫旅行（地点 %d），等待到站", locID)}
	case "traveling":
		return TaskRunDetail{UID: a.UID, Status: TaskSkipped,
			Message: fmt.Sprintf("旅行在途（record=%d），到站后领奖", ts.RecordID)}
	default:
		return TaskRunDetail{UID: a.UID, Status: TaskSkipped, Message: "未知旅行状态 " + ts.State}
	}
}

// adoptBuddy 无猫时领养：先同意协议（幂等）再领养。
// 门槛未达（对话量不够）当日不再重试（adoptTried 防抖，CST 自然日重置）。
func (s *Scheduler) adoptBuddy(a Account, cred *UpstreamCred) TaskRunDetail {
	s.mu.Lock()
	triedToday := s.adoptTried[a.UID] == travelDay(time.Now())
	s.mu.Unlock()
	if triedToday {
		return TaskRunDetail{UID: a.UID, Status: TaskSkipped,
			Message: "领养门槛（对话量）未达，明日重试（今日已试）"}
	}
	if err := s.up.BuddyAgreement(cred); err != nil {
		return TaskRunDetail{UID: a.UID, Status: TaskFailed, Message: "同意领养协议失败: " + err.Error()}
	}
	err := s.up.BuddyFirst(cred)
	switch {
	case err == nil:
		return TaskRunDetail{UID: a.UID, Status: TaskSuccess, Message: "领养成功（+300 积分）"}
	case isBuddyTaskIncomplete(err):
		s.mu.Lock()
		s.adoptTried[a.UID] = travelDay(time.Now())
		s.mu.Unlock()
		return TaskRunDetail{UID: a.UID, Status: TaskSkipped, Message: "领养门槛（对话量）未达，明日重试"}
	default:
		return TaskRunDetail{UID: a.UID, Status: TaskFailed, Message: "领养失败: " + err.Error()}
	}
}

// tryRefresh token 临期时的自动续期：调上游刷新端点并原子写回凭证，
// 刷新成功解除「需重新登录」标记。ok=false 时 det 为失败原因（含需重新登录）。
// 与 doKeepalive 的刷新写回同构，但语义略异：缺 refreshToken 时此处直接判失败
// （任务无法继续），keepalive 则记 skipped。
func (s *Scheduler) tryRefresh(a Account) (det TaskRunDetail, ok bool) {
	cred, err := LoadUpstreamCred(s.svc.AuthDir(), a.Credential)
	if err != nil {
		return TaskRunDetail{UID: a.UID, Status: TaskFailed, Message: "凭证读取失败: " + err.Error()}, false
	}
	if cred.RefreshToken == "" {
		return TaskRunDetail{UID: a.UID, Status: TaskFailed,
			Message: "token 临期且凭证缺少 refreshToken，无法自动刷新，需重新扫码授权登录"}, false
	}
	nc, err := s.up.refreshToken(cred)
	if err != nil {
		if isReloginRequired(err) {
			s.svc.Store().MutateAccountState(a.UID, func(st *AccountState) {
				st.NeedsRelogin = true
				st.Note = "refresh token 被服务端拒绝，需重新授权登录"
			})
			EmitEvent(EventAccountStatus, map[string]any{"uid": a.UID, "status": "relogin"})
			return TaskRunDetail{UID: a.UID, Status: TaskFailed,
				Message: "refresh token 已被服务端拒绝，需重新扫码授权登录"}, false
		}
		return TaskRunDetail{UID: a.UID, Status: TaskFailed, Message: "token 自动刷新失败: " + err.Error()}, false
	}
	if err := SaveUpstreamCred(s.svc.AuthDir(), a.Credential, nc); err != nil {
		return TaskRunDetail{UID: a.UID, Status: TaskFailed, Message: "新 token 写回凭证失败: " + err.Error()}, false
	}
	s.svc.Store().MutateAccountState(a.UID, func(st *AccountState) {
		st.LastActivity = Now()
		st.NeedsRelogin = false
	})
	EmitEvent(EventAccountStatus, map[string]any{"uid": a.UID, "status": "online"})
	return TaskRunDetail{}, true
}

// doKeepalive 真实 token 保活：临期（或已过期）时调上游刷新端点并原子写回凭证。
func (s *Scheduler) doKeepalive(a Account) TaskRunDetail {
	cred, err := LoadUpstreamCred(s.svc.AuthDir(), a.Credential)
	if err != nil {
		return TaskRunDetail{UID: a.UID, Status: TaskFailed, Message: "凭证读取失败: " + err.Error()}
	}
	if cred.RefreshToken == "" {
		return TaskRunDetail{UID: a.UID, Status: TaskSkipped, Message: "凭证缺少 refreshToken，无法自动刷新，需重新授权登录"}
	}
	// token 仍充足（> 临期窗口）时不动它，避免无谓的刷新风暴；顺带刷新真实余额
	if cred.ExpiresAtUnix > 0 {
		left := time.Until(time.Unix(cred.ExpiresAtUnix, 0))
		if left > expiringSoonWindow {
			msg := fmt.Sprintf("token 有效期充足（剩余 %d 小时），本次未刷新", int(left.Hours()))
			if note := s.refreshBalance(a, cred); note != "" {
				msg += "；" + note
			}
			if note := s.refreshModels(a, cred); note != "" {
				msg += "；" + note
			}
			return TaskRunDetail{UID: a.UID, Status: TaskSuccess, Message: msg}
		}
	}
	nc, err := s.up.refreshToken(cred)
	if err != nil {
		if isReloginRequired(err) {
			// RT 被服务端明确拒绝：标记需重新登录，账号退出自动调度与网关池；
			// 用户重新扫码授权后下次刷新成功会自动恢复
			s.svc.Store().MutateAccountState(a.UID, func(st *AccountState) {
				st.NeedsRelogin = true
				st.Note = "refresh token 被服务端拒绝，需重新授权登录"
			})
			EmitEvent(EventAccountStatus, map[string]any{"uid": a.UID, "status": "relogin"})
			return TaskRunDetail{UID: a.UID, Status: TaskFailed,
				Message: "refresh token 已被服务端拒绝，需重新扫码授权登录"}
		}
		return TaskRunDetail{UID: a.UID, Status: TaskFailed, Message: "token 刷新失败: " + err.Error()}
	}
	if err := SaveUpstreamCred(s.svc.AuthDir(), a.Credential, nc); err != nil {
		return TaskRunDetail{UID: a.UID, Status: TaskFailed, Message: "新 token 写回凭证失败: " + err.Error()}
	}
	s.svc.Store().MutateAccountState(a.UID, func(st *AccountState) {
		st.LastActivity = Now()
		if st.NeedsRelogin {
			st.NeedsRelogin = false // 刷新成功自动解除「需重新登录」
		}
	})
	EmitEvent(EventAccountStatus, map[string]any{"uid": a.UID, "status": "online"})
	until := "未知"
	if nc.ExpiresAtUnix > 0 {
		until = time.Unix(nc.ExpiresAtUnix, 0).Format("2006-01-02 15:04")
	}
	msg := "token 已刷新，新有效期至 " + until
	if note := s.refreshBalance(a, nc); note != "" {
		msg += "；" + note
	}
	if note := s.refreshModels(a, nc); note != "" {
		msg += "；" + note
	}
	return TaskRunDetail{UID: a.UID, Status: TaskSuccess, Message: msg}
}

// refreshBalance 查询真实余额细分并写入账号运行态；失败不打断主流程，
// 但失败原因要带回任务消息（用户需要知道为什么余额没更新）。
func (s *Scheduler) refreshBalance(a Account, cred *UpstreamCred) string {
	d, err := s.up.FetchBalanceDetail(cred, expiringCreditWindow)
	if err != nil {
		return "余额刷新失败: " + err.Error()
	}
	s.applyBalance(a.UID, d)
	msg := fmt.Sprintf("余额 %.0f 分", d.Remain)
	if d.Expiring > 0 {
		msg += fmt.Sprintf("（其中 %.0f 分 %d 小时内到期）", d.Expiring, int(expiringCreditWindow.Hours()))
	}
	return msg
}

// applyBalance 余额细分落运行态并广播（refreshBalance 与 RefreshBalanceNow 共用）。
// 同时完成两件事：
//   - 积分流水：两次真实查询间的余额差值落 CreditLog（非估算，真实观测）；
//   - 成本账本：差值 / 期间服务的 token 数 → 每 1k token 成本 EMA（对齐 NoteModelCost）。
func (s *Scheduler) applyBalance(uid string, d *BalanceDetail) {
	var delta float64
	var oldRemain, oldExpiring float64
	var oldKnown bool
	s.svc.Store().MutateAccountState(uid, func(st *AccountState) {
		oldRemain, oldExpiring, oldKnown = st.Credits, st.CreditsExpiring, st.CreditsKnown
		if st.CreditsKnown && st.Credits > 0 {
			delta = st.Credits - d.Remain
		}
		st.Credits = d.Remain
		st.CreditsUsed = d.Used
		st.CreditsExpiring = d.Expiring
		st.CreditsKnown = true
		// 到期分层依据：持久化最早到期日（网关选号重启后立即恢复分层）
		st.CreditsExpireDay = d.ExpireDay
		st.CreditsExpireRemain = d.ExpireRemain
		if delta > 0 && st.TokensSinceSync > 0 {
			per1k := delta / float64(st.TokensSinceSync) * 1000
			if st.CostSamples == 0 {
				st.CostEMA = per1k
			} else {
				st.CostEMA = st.CostEMA*0.7 + per1k*0.3 // EMA α=0.3
			}
			st.CostSamples++
			st.TokensSinceSync = 0
		}
	})
	if delta != 0 {
		s.svc.Store().AppendCreditLog(CreditLog{
			ID: NewID("c"), Time: Now(), UID: uid, Delta: delta, Balance: d.Remain,
		})
	}
	s.notifyCreditTransitions(uid, d, oldRemain, oldExpiring, oldKnown)
	EmitEvent(EventAccountStatus, map[string]any{"uid": uid, "status": "balance"})
}

// notifyCreditTransitions 积分预警通知：临期作废 / 余额耗尽两个状态跃迁时推送
// 桌面通知。同一账号同一自然日（CST）只提醒一次，避免 15 分钟巡检周期刷屏；
// 首次发现（oldKnown=false）不提醒——新账号首查有临期属正常状态而非「变化」。
func (s *Scheduler) notifyCreditTransitions(uid string, d *BalanceDetail, oldRemain, oldExpiring float64, oldKnown bool) {
	if !oldKnown {
		return
	}
	today := travelDay(time.Now())
	var kind string
	switch {
	case d.Remain <= 0 && oldRemain > 0:
		kind = "credit_exhausted"
	case d.Expiring > 0 && oldExpiring <= 0:
		kind = "credit_expiring"
	default:
		return
	}
	s.mu.Lock()
	marked := s.creditNotified[uid] == today
	if !marked {
		s.creditNotified[uid] = today
	}
	s.mu.Unlock()
	if marked {
		return
	}
	nickname := shortUID(uid)
	if a, ok := s.svc.FindAccount(uid); ok && a.Nickname != "" {
		nickname = a.Nickname
	}
	if kind == "credit_exhausted" {
		EmitEvent(EventAccountStatus, map[string]any{
			"uid": uid, "status": "credit_exhausted", "nickname": nickname,
		})
		return
	}
	EmitEvent(EventAccountStatus, map[string]any{
		"uid": uid, "status": "credit_expiring", "nickname": nickname,
		"expiring": d.Expiring, "expireDay": d.ExpireDay,
	})
}

// RefreshBalanceNow 立即查询指定账号的真实积分余额（详情弹窗按需查询入口，
// 不等 keepalive 周期）。返回刷新后的账号视图。
func (s *Scheduler) RefreshBalanceNow(uid string) (Account, error) {
	a, ok := s.svc.FindAccount(uid)
	if !ok {
		return Account{}, fmt.Errorf("账号 %s 不存在", uid)
	}
	cred, err := LoadUpstreamCred(s.svc.AuthDir(), a.Credential)
	if err != nil {
		return Account{}, fmt.Errorf("凭证读取失败: %w", err)
	}
	d, err := s.up.FetchBalanceDetail(cred, expiringCreditWindow)
	if err != nil {
		return Account{}, fmt.Errorf("余额查询失败: %w", err)
	}
	s.applyBalance(a.UID, d)
	acc, _ := s.svc.FindAccount(uid)
	return acc, nil
}

// refreshModels 拉取上游动态模型目录并入本地清单（本地种子表兜底，动态条目覆盖
// 同名条目的能力值）；失败不打断主流程，失败原因带回任务消息。
func (s *Scheduler) refreshModels(a Account, cred *UpstreamCred) string {
	infos, err := s.up.FetchModels(cred)
	if err != nil {
		return "模型目录刷新失败: " + err.Error()
	}
	s.svc.Store().MergeUpstreamModels(infos)
	return ""
}

// doCheckin 真实每日签到：调上游签到端点，结果如实上报（含「今天已签到」）；
// 成功后顺带兑换已达标的连登奖励档位并抽完可用抽奖次数。
// global 账号无签到/任务中心（参考 PLAN D4）：改领一次性 trial 加油包（幂等 14051）。
func (s *Scheduler) doCheckin(a Account) TaskRunDetail {
	cred, err := LoadUpstreamCred(s.svc.AuthDir(), a.Credential)
	if err != nil {
		return TaskRunDetail{UID: a.UID, Status: TaskFailed, Message: "凭证读取失败: " + err.Error()}
	}
	if a.Realm == "global" {
		return s.doTrial(a, cred)
	}
	res, err := s.up.dailyCheckin(cred)
	if err != nil {
		return TaskRunDetail{UID: a.UID, Status: TaskFailed, Message: err.Error()}
	}
	msg := "签到成功 " + res.Message
	if res.Already {
		msg = "今天已签到（上游确认）"
	}
	note, credits := s.claimGrowthRewards(cred)
	if note != "" {
		msg += "；" + note
	}
	if credits > 0 {
		msg += fmt.Sprintf("（本次领取 %d 分）", credits)
	}
	return TaskRunDetail{UID: a.UID, Status: TaskSuccess, Message: msg, Credits: credits}
}

// doTrial global 账号领取一次性试用加油包（幂等：已领过视为正常）。
func (s *Scheduler) doTrial(a Account, cred *UpstreamCred) TaskRunDetail {
	claimed, err := s.up.ClaimTrial(cred)
	if err != nil {
		return TaskRunDetail{UID: a.UID, Status: TaskFailed, Message: "trial 加油包领取失败: " + err.Error()}
	}
	if claimed {
		return TaskRunDetail{UID: a.UID, Status: TaskSuccess, Message: "trial 加油包领取成功"}
	}
	return TaskRunDetail{UID: a.UID, Status: TaskSuccess, Message: "trial 加油包已领取过（幂等确认）"}
}

// claimGrowthRewards 兑换当月已达标未领的连登奖励档位，并抽完可用抽奖次数；
// 无可领项且无抽奖次数返回空串。各子动作失败不静默：原因带回任务消息。
// 第二个返回值是本次实际领取到的积分合计（只累加上游明确返回数量的动作）。
func (s *Scheduler) claimGrowthRewards(cred *UpstreamCred) (string, int) {
	var notes []string
	credits := 0
	if rs, err := s.up.FetchGrowthRedemption(cred); err != nil {
		notes = append(notes, "奖励档位查询失败: "+err.Error())
	} else {
		var claimed []string
		var failed []string
		for _, tier := range rs.Tiers {
			if tier.Status != "available" {
				continue // claimed 已领 / locked 未达标
			}
			if err := s.up.RedeemTier(cred, tier.Tier, newClientToken()); err != nil {
				failed = append(failed, fmt.Sprintf("%s(%v)", tier.Tier, err))
				continue // 单档失败不影响其他档
			}
			claimed = append(claimed, fmt.Sprintf("%s(+%d分)", tier.Tier, tier.Credit))
			credits += tier.Credit
		}
		if len(claimed) > 0 {
			notes = append(notes, "连登奖励已兑换: "+strings.Join(claimed, " "))
		}
		if len(failed) > 0 {
			notes = append(notes, "兑换失败: "+strings.Join(failed, " "))
		}
	}
	if note, n := s.drawLottery(cred); note != "" {
		notes = append(notes, note)
		credits += n
	}
	// 盲盒 / 补签卡 / 礼包补偿（对齐 WorkBuddy-Daily 互动玩法；幂等，正常态静默）
	if bb, err := s.up.BlindBoxRound(cred); err == nil && bb != nil && bb.Opened > 0 {
		notes = append(notes, fmt.Sprintf("盲盒开 %d 次: %s", bb.Opened, strings.Join(bb.Items, "、")))
	}
	if note, _ := s.up.UseMakeupCardIfMissed(cred); note != "" {
		notes = append(notes, note)
	}
	if note, n := s.up.ClaimGiftAndCompensation(cred); note != "" {
		notes = append(notes, note)
		credits += n
	}
	return strings.Join(notes, "；"), credits
}

// drawLottery 抽完当前可用抽奖次数（先查 chances，逐次 draw；无次数/未开启为正常态）。
// 第二个返回值是抽到的积分合计（非积分类奖品不计）。
func (s *Scheduler) drawLottery(cred *UpstreamCred) (string, int) {
	chances, err := s.up.LotteryChances(cred)
	if err != nil || chances <= 0 {
		return "", 0
	}
	var prizes []string
	credits := 0
	for i := 0; i < chances && i < maxLotteryDraws; i++ {
		res, err := s.up.LotteryDraw(cred)
		if err != nil {
			if isLotteryNoChance(err) || isLotteryDisabled(err) {
				break // 正常态：抽完或活动未开启
			}
			break // 其他失败也静默，不刷任务失败
		}
		if res.PrizeType == "credit" {
			prizes = append(prizes, fmt.Sprintf("%s(+%d分)", res.PrizeName, res.CreditAmount))
			credits += res.CreditAmount
		} else if res.PrizeName != "" {
			prizes = append(prizes, res.PrizeName)
		} else {
			prizes = append(prizes, "未知名奖品")
		}
	}
	if len(prizes) == 0 {
		return "", 0
	}
	return fmt.Sprintf("抽奖 %d 次: %s", len(prizes), strings.Join(prizes, " ")), credits
}

func taskActionName(t string) string {
	switch t {
	case TaskCheckin:
		return "每日签到"
	case TaskTravel:
		return "猫猫旅行"
	case TaskKeepalive:
		return "token 保活"
	case TaskActivity:
		return "活跃地图"
	case TaskSchool:
		return "开学季活动"
	case TaskCat:
		return "夜猫子任务"
	case TaskGrowth:
		return "成长任务中心"
	}
	return t
}

func formatExpiry(s string) string {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.Format("2006-01-02 15:04")
	}
	if s == "" {
		return "未知时间"
	}
	return s
}

// Status 调度器状态（含各任务下次执行时间）
func (s *Scheduler) Status() SchedulerStatus {
	cfg := s.svc.GetConfig()
	out := SchedulerStatus{Running: s.IsRunning(), NextRuns: []NextRun{}}
	now := time.Now()
	for _, e := range scheduleTable(cfg) {
		nr := NextRun{Type: e.Type, Enabled: e.Enabled, Hours: e.Hours}
		if e.Enabled && len(e.Hours) > 0 {
			if t, ok := nextFire(now, e.Hours); ok {
				nr.Next = t.Format(time.RFC3339)
			}
		}
		out.NextRuns = append(out.NextRuns, nr)
	}
	s.mu.Lock()
	out.History = append([]TaskRun{}, s.history...)
	if s.lastRun != nil {
		last := *s.lastRun
		out.LastRun = &last
	}
	busy := make([]string, 0, len(s.busy))
	for t, on := range s.busy {
		if on {
			busy = append(busy, t)
		}
	}
	sort.Strings(busy)
	out.Busy = busy
	s.mu.Unlock()
	return out
}

// nextFire 计算下一次命中 小时列表 的时刻（按分钟对齐到整点）
func nextFire(now time.Time, hours []int) (time.Time, bool) {
	hs := append([]int{}, hours...)
	sort.Ints(hs)
	for _, h := range hs {
		if h < 0 || h > 23 {
			continue
		}
		cand := time.Date(now.Year(), now.Month(), now.Day(), h, 0, 0, 0, now.Location())
		if cand.After(now) {
			return cand, true
		}
	}
	if len(hs) == 0 {
		return time.Time{}, false
	}
	// 今天剩余时点都已过 → 用明天第一个时点
	cand := time.Date(now.Year(), now.Month(), now.Day(), hs[0], 0, 0, 0, now.Location()).AddDate(0, 0, 1)
	return cand, true
}

package core

import (
	"sort"
	"time"
)

// ---- 统计口径 ----
//
// 全部指标来自真实记录：RequestLog（网关实际处理的请求）与 TaskLog（调度器实际
// 执行的任务）。没有任何估算值：无法从真实记录推导的指标一律不提供，由前端显示
// 「未接入」，而不是编造。

// TrendPoint 单日聚合
type TrendPoint struct {
	Date         string  `json:"date"` // MM-DD
	Requests     int     `json:"requests"`
	Tokens       int     `json:"tokens"`
	InputTokens  int     `json:"inputTokens"`
	OutputTokens int     `json:"outputTokens"`
	CacheTokens  int     `json:"cacheTokens"` // 上游 usage 命中缓存的输入 token
	Cost         float64 `json:"cost"`        // 按单价表换算的金额（元）；未定价模型不计入
	Errors       int     `json:"errors"`
	AvgLatency   float64 `json:"avgLatency"`
}

// ModelStat 单模型聚合
type ModelStat struct {
	Model        string  `json:"model"`
	Requests     int     `json:"requests"`
	Tokens       int     `json:"tokens"`
	InputTokens  int     `json:"inputTokens"`
	OutputTokens int     `json:"outputTokens"`
	CacheTokens  int     `json:"cacheTokens"` // 上游 usage 命中缓存的输入 token
	Cost         float64 `json:"cost"`        // 按单价表换算的金额（元）
	Priced       bool    `json:"priced"`      // 该模型是否已配单价（false = 未定价，Cost 恒为 0）
	Errors       int     `json:"errors"`
	AvgLatency   float64 `json:"avgLatency"`
	P50          float64 `json:"p50"`
	P90          float64 `json:"p90"`
	P99          float64 `json:"p99"`
	AvgTokens    float64 `json:"avgTokens"` // 单次请求平均 token
	TokenP50     float64 `json:"tokenP50"`  // 单次请求大小分位
	TokenP90     float64 `json:"tokenP90"`
}

// KeyStat 单密钥聚合
type KeyStat struct {
	KeyID      string  `json:"keyId"`
	KeyName    string  `json:"keyName"`
	Requests   int     `json:"requests"`
	Tokens     int     `json:"tokens"`
	Cost       float64 `json:"cost"` // 按单价表换算的金额（元）
	Errors     int     `json:"errors"`
	AvgLatency float64 `json:"avgLatency"`
}

// HourStat 0-23 时段聚合（按模型拆分，供山脊图使用）
type HourStat struct {
	Hour     int            `json:"hour"`
	Requests int            `json:"requests"`
	Tokens   int            `json:"tokens"`
	ByModel  map[string]int `json:"byModel"`
}

// TaskStat 任务类型聚合
type TaskStat struct {
	Type    string `json:"type"`
	Total   int    `json:"total"`
	Success int    `json:"success"`
	Failed  int    `json:"failed"`
	Skipped int    `json:"skipped"`
}

// Overview 总览指标
type Overview struct {
	Requests     int     `json:"requests"`
	Tokens       int     `json:"tokens"`
	InputTokens  int     `json:"inputTokens"`
	OutputTokens int     `json:"outputTokens"`
	CacheTokens  int     `json:"cacheTokens"`
	// Cost 窗口内金额（元）；TodayCost 今日金额；UnpricedModels 是窗口内出现过、
	// 但没有单价的模型数——它们不计入 Cost，前端须如实标注，避免读者把金额当全集。
	Cost           float64 `json:"cost"`
	TodayCost      float64 `json:"todayCost"`
	UnpricedModels int     `json:"unpricedModels"`
	Errors         int     `json:"errors"`
	SuccessRate    float64 `json:"successRate"`

	AvgLatency   float64 `json:"avgLatency"`
	FirstLatency float64 `json:"firstLatency"`
	P50          float64 `json:"p50"`
	P90          float64 `json:"p90"`
	P99          float64 `json:"p99"`

	Sessions int `json:"sessions"`

	Days          int     `json:"days"`
	AvgPerDay     float64 `json:"avgPerDay"`
	AvgCostPerDay float64 `json:"avgCostPerDay"` // 活跃自然日的日均金额（元）
	PeakDate      string  `json:"peakDate"`
	PeakTokens    int     `json:"peakTokens"`
	TodayTokens   int     `json:"todayTokens"`
	TodayRequests int     `json:"todayRequests"`

	AccountsTotal  int     `json:"accountsTotal"`
	AccountsOnline int     `json:"accountsOnline"`
	KeysTotal      int     `json:"keysTotal"`
	CreditsTotal   float64 `json:"creditsTotal"`
	CreditsUnknown int     `json:"creditsUnknown"`

	FirstTS int64 `json:"firstTs"`
	LastTS  int64 `json:"lastTs"`
}

// DayModelStat 单日按模型拆分
type DayModelStat struct {
	Date    string         `json:"date"`
	ByModel map[string]int `json:"byModel"`
}

// Dashboard 统计聚合结果（一次调用返回整页所需数据）
type Dashboard struct {
	RangeSecs int          `json:"rangeSecs"`
	Overview  Overview     `json:"overview"`
	Daily     []TrendPoint `json:"daily"`
	Models    []ModelStat  `json:"models"`
	Keys      []KeyStat    `json:"keys"`
	Hourly    []HourStat   `json:"hourly"`
	Tasks     []TaskStat   `json:"tasks"`

	// HeatmapDays / Heatmap 逐日 20 分钟粒度 token 矩阵（行 = 天，列 = 72 个 20 分钟桶）
	HeatmapDays []string `json:"heatmapDays"`
	Heatmap     [][]int  `json:"heatmap"`

	DailyModels []DayModelStat `json:"dailyModels"`
}

// Stats 统计聚合器：对真实记录做只读聚合。
type Stats struct {
	store    *Store
	accounts func() []Account
	prices   func() map[string]ModelPrice
}

// NewStats 创建聚合器；accounts 提供当前账号视图（用于总览中的池状态），
// prices 提供模型单价表（用于把 token 换算成金额；nil = 全部未定价）。
func NewStats(store *Store, accounts func() []Account, prices func() map[string]ModelPrice) *Stats {
	return &Stats{store: store, accounts: accounts, prices: prices}
}

// priceTable 当前单价表快照（nil provider 视为空表：金额一律 0 / 未定价）
func (s *Stats) priceTable() PriceTable {
	if s.prices == nil {
		return NewPriceTable(nil)
	}
	return NewPriceTable(s.prices())
}

// Range 时间窗口（左闭右开），全部以本地时区自然日对齐。
type Range struct {
	From int64
	To   int64
}

// RangeForDays 返回最近 days 个自然日（含今天）的窗口。
func RangeForDays(days int) Range {
	if days <= 0 {
		days = 7
	}
	now := time.Now()
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).AddDate(0, 0, -(days - 1))
	return Range{From: start.Unix(), To: now.Unix() + 1}
}

// Dashboard 生成整页统计
func (s *Stats) Dashboard(days int) Dashboard {
	r := RangeForDays(days)
	logs := s.store.ListRequestLogs()
	tasks := s.store.ListTaskLogs()

	in := filterRequestLogs(logs, r)
	pt := s.priceTable()
	d := Dashboard{RangeSecs: days * 86400}
	d.Overview = s.overview(in, tasks, r, pt)
	d.Daily = dailyTrend(in, days, pt)
	d.Models = modelStats(in, pt)
	d.Keys = keyStats(in, pt)
	d.Hourly = hourlyStats(in)
	d.Tasks = taskStats(tasks, r)
	d.HeatmapDays, d.Heatmap = heatmap(in, days)
	d.DailyModels = dailyModels(in, days)
	return d
}

// SessionStat 会话级聚合（keyName → sessionID → 请求 的下钻中间层）。
// 项目维度：请求日志只携带调用方传入的 session_id，没有项目/工作空间字段，
// 因此下钻链路为「密钥（调用方）→ 会话 → 请求」，不做任何推测性归属。
type SessionStat struct {
	SessionID    string `json:"sessionId"`
	Title        string `json:"title"` // 会话标题（本机会话日志回填；与本机 session UUID 匹配才有值）
	KeyName      string `json:"keyName"`
	FirstTS      int64  `json:"firstTs"`
	LastTS       int64  `json:"lastTs"`
	Requests     int     `json:"requests"`
	Tokens       int     `json:"tokens"`
	InputTokens  int     `json:"inputTokens"`
	OutputTokens int     `json:"outputTokens"`
	CacheTokens  int     `json:"cacheTokens"`
	Cost         float64 `json:"cost"` // 按单价表换算的金额（元）
	Errors       int     `json:"errors"`
}

// SessionDrilldown 会话下钻结果：缓存命中率 KPI + 会话聚合列表（全量，按 token 降序；
// 前端负责搜索过滤，日志上限 2000 条，会话数远小于该值，无需分页）。
type SessionDrilldown struct {
	Days         int           `json:"days"`
	InputTokens  int           `json:"inputTokens"`  // 窗口内输入侧总量（含缓存命中部分）
	CacheTokens  int           `json:"cacheTokens"`  // 窗口内缓存命中 token
	CacheHitRate float64       `json:"cacheHitRate"` // 缓存命中率 %（cache / input × 100；无输入为 0）
	Cost         float64       `json:"cost"`         // 窗口内金额合计（元）
	Sessions     []SessionStat `json:"sessions"`     // 按 token 降序
}

// SessionDrilldown 会话下钻聚合：窗口内按 session_id 聚合出会话 Top（含缓存
// 命中率 KPI）。无 session_id 的请求不入会话列表（但计入缓存命中率分母）。
func (s *Stats) SessionDrilldown(days int) SessionDrilldown {
	r := RangeForDays(days)
	in := filterRequestLogs(s.store.ListRequestLogs(), r)
	d := SessionDrilldown{Days: days}
	pt := s.priceTable()
	m := map[string]*SessionStat{}
	titles := ClientSessionTitles() // 本机会话标题（5 分钟缓存；查不到回退展示 session_id）
	for _, l := range in {
		inputTotal := l.InputTokens + l.CacheTokens
		d.InputTokens += inputTotal
		d.CacheTokens += l.CacheTokens
		if cost, priced := pt.Cost(l.Model, l.InputTokens, l.OutputTokens, l.CacheTokens, 0); priced {
			d.Cost += cost
		}
		if l.SessionID == "" {
			continue
		}
		sess := m[l.SessionID]
		if sess == nil {
			sess = &SessionStat{SessionID: l.SessionID, Title: titles[l.SessionID], KeyName: l.KeyName, FirstTS: l.TS, LastTS: l.TS}
			m[l.SessionID] = sess
		}
		sess.Requests++
		sess.Tokens += l.Tokens
		sess.InputTokens += l.InputTokens
		sess.OutputTokens += l.OutputTokens
		sess.CacheTokens += l.CacheTokens
		if cost, priced := pt.Cost(l.Model, l.InputTokens, l.OutputTokens, l.CacheTokens, 0); priced {
			sess.Cost += cost
		}
		if l.Status >= 400 {
			sess.Errors++
		}
		if l.TS < sess.FirstTS {
			sess.FirstTS = l.TS
		}
		if l.TS > sess.LastTS {
			sess.LastTS = l.TS
		}
	}
	if d.InputTokens > 0 {
		d.CacheHitRate = round2(float64(d.CacheTokens) / float64(d.InputTokens) * 100)
	}
	d.Cost = round4(d.Cost)
	for _, sess := range m {
		sess.Cost = round4(sess.Cost)
		d.Sessions = append(d.Sessions, *sess)
	}
	sort.Slice(d.Sessions, func(i, j int) bool { return d.Sessions[i].Tokens > d.Sessions[j].Tokens })
	return d
}

// RequestLogCost 请求明细行 + 按当前单价表换算的金额（仅下钻视图使用，不落盘：
// 落盘的是 token 事实，金额随单价表变动，不是历史账本）。
type RequestLogCost struct {
	RequestLog
	Cost   float64 `json:"cost"`
	Priced bool    `json:"priced"` // false = 该模型未定价，Cost 恒为 0
}

// SessionRequests 单个会话的请求明细（时间升序），供下钻第三级展示。
func (s *Stats) SessionRequests(sessionID string, days int) []RequestLogCost {
	r := RangeForDays(days)
	in := filterRequestLogs(s.store.ListRequestLogs(), r)
	pt := s.priceTable()
	var out []RequestLogCost
	for _, l := range in {
		if l.SessionID != sessionID {
			continue
		}
		cost, priced := pt.Cost(l.Model, l.InputTokens, l.OutputTokens, l.CacheTokens, 0)
		out = append(out, RequestLogCost{RequestLog: l, Cost: round4(cost), Priced: priced})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TS < out[j].TS })
	return out
}

// heatmap 逐日 20 分钟粒度 token 矩阵（行 = 天，列 = 72 桶）
func heatmap(in []RequestLog, days int) ([]string, [][]int) {
	grid := make([][]int, days)
	for i := range grid {
		grid[i] = make([]int, 72)
	}
	labels := make([]string, days)
	index := map[string]int{}
	now := time.Now()
	for i := 0; i < days; i++ {
		key := now.AddDate(0, 0, -(days - 1 - i)).Format("01-02")
		labels[i] = key
		index[key] = i
	}
	for _, l := range in {
		t := time.Unix(l.TS, 0)
		row, ok := index[t.Format("01-02")]
		if !ok {
			continue
		}
		bucket := t.Hour()*3 + t.Minute()/20
		if bucket < 0 || bucket >= 72 {
			continue
		}
		grid[row][bucket] += l.Tokens
	}
	return labels, grid
}

// dailyModels 逐日按模型拆分 token（最新在最后）
func dailyModels(in []RequestLog, days int) []DayModelStat {
	byDay := map[string]map[string]int{}
	for _, l := range in {
		key := time.Unix(l.TS, 0).Format("01-02")
		m := byDay[key]
		if m == nil {
			m = map[string]int{}
			byDay[key] = m
		}
		name := l.Model
		if name == "" {
			name = "(未指定)"
		}
		m[name] += l.Tokens
	}
	now := time.Now()
	out := make([]DayModelStat, 0, days)
	for i := days - 1; i >= 0; i-- {
		key := now.AddDate(0, 0, -i).Format("01-02")
		m := byDay[key]
		if m == nil {
			m = map[string]int{}
		}
		out = append(out, DayModelStat{Date: key, ByModel: m})
	}
	return out
}

func filterRequestLogs(logs []RequestLog, r Range) []RequestLog {
	out := make([]RequestLog, 0, len(logs))
	for _, l := range logs {
		if l.TS >= r.From && l.TS < r.To {
			out = append(out, l)
		}
	}
	return out
}

func (s *Stats) overview(in []RequestLog, tasks []TaskLog, r Range, pt PriceTable) Overview {
	o := Overview{}
	lat, first, sizes := []float64{}, []float64{}, []float64{}
	sessions := map[string]struct{}{}
	unpriced := map[string]struct{}{} // 窗口内无单价的模型（金额分母口径须透明）
	dayCost := map[string]float64{}   // 逐日金额，供今日 / 日均口径
	for _, l := range in {
		o.Requests++
		o.Tokens += l.Tokens
		o.InputTokens += l.InputTokens
		o.OutputTokens += l.OutputTokens
		o.CacheTokens += l.CacheTokens
		// 网关口径：InputTokens 不含缓存命中，缓存单列；缓存写未观测传 0
		cost, priced := pt.Cost(l.Model, l.InputTokens, l.OutputTokens, l.CacheTokens, 0)
		if priced {
			o.Cost += cost
			dayCost[dayKey(l.TS)] += cost
		} else if l.Tokens > 0 {
			// 只统计真有 token 用量的未定价模型：鉴权失败等 0 token 记录
			//（model 为空）不构成计价缺口，计入会让「未定价」名单出现幻影条目
			unpriced[l.Model] = struct{}{}
		}
		if l.Status >= 400 {
			o.Errors++
		}
		if l.Latency > 0 {
			lat = append(lat, l.Latency)
			o.AvgLatency += l.Latency
		}
		if l.FirstLatency > 0 {
			first = append(first, l.FirstLatency)
			o.FirstLatency += l.FirstLatency
		}
		if l.Tokens > 0 {
			sizes = append(sizes, float64(l.Tokens))
		}
		if l.SessionID != "" {
			sessions[l.SessionID] = struct{}{}
		}
	}
	if o.Requests > 0 {
		o.SuccessRate = round2(float64(o.Requests-o.Errors) / float64(o.Requests) * 100)
	}
	if len(lat) > 0 {
		o.AvgLatency = round2(o.AvgLatency / float64(len(lat)))
	}
	if len(first) > 0 {
		o.FirstLatency = round2(o.FirstLatency / float64(len(first)))
	}
	o.P50 = percentile(lat, 50)
	o.P90 = percentile(lat, 90)
	o.P99 = percentile(lat, 99)
	o.Sessions = len(sessions)

	// 逐日 token / 请求（用于日均与峰值）
	byDay := map[string]*TrendPoint{}
	for _, l := range in {
		key := dayKey(l.TS)
		p := byDay[key]
		if p == nil {
			p = &TrendPoint{Date: key}
			byDay[key] = p
		}
		p.Tokens += l.Tokens
		p.Requests++
	}
	today := dayKey(time.Now().Unix())
	total := 0
	costTotal := 0.0
	activeDays := 0
	for k, p := range byDay {
		total += p.Tokens
		costTotal += dayCost[k]
		if p.Requests > 0 {
			activeDays++
		}
		if p.Tokens > o.PeakTokens {
			o.PeakTokens = p.Tokens
			o.PeakDate = k
		}
		if k == today {
			o.TodayTokens = p.Tokens
			o.TodayRequests = p.Requests
			o.TodayCost = round4(dayCost[k])
		}
	}
	o.Days = activeDays
	if activeDays > 0 {
		o.AvgPerDay = round2(float64(total) / float64(activeDays))
		o.AvgCostPerDay = round4(costTotal / float64(activeDays))
	}
	o.Cost = round4(o.Cost)
	o.UnpricedModels = len(unpriced)
	if logs := s.store.ListRequestLogs(); len(logs) > 0 {
		o.FirstTS = logs[len(logs)-1].TS
		o.LastTS = logs[0].TS
	}

	if s.accounts != nil {
		accs := s.accounts()
		o.AccountsTotal = len(accs)
		for _, a := range accs {
			if a.Status == "online" {
				o.AccountsOnline++
			}
			if a.CreditsKnown {
				o.CreditsTotal += a.Credits
			} else {
				o.CreditsUnknown++
			}
		}
	}
	o.KeysTotal = len(s.store.ListKeys())
	o.CreditsTotal = round2(o.CreditsTotal)
	return o
}

func dailyTrend(in []RequestLog, days int, pt PriceTable) []TrendPoint {
	byDay := map[string]*TrendPoint{}
	latSum := map[string]float64{}
	for _, l := range in {
		key := dayKey(l.TS)
		p := byDay[key]
		if p == nil {
			p = &TrendPoint{Date: key}
			byDay[key] = p
		}
		p.Requests++
		p.Tokens += l.Tokens
		p.InputTokens += l.InputTokens
		p.OutputTokens += l.OutputTokens
		p.CacheTokens += l.CacheTokens
		if cost, priced := pt.Cost(l.Model, l.InputTokens, l.OutputTokens, l.CacheTokens, 0); priced {
			p.Cost += cost
		}
		if l.Status >= 400 {
			p.Errors++
		}
		if l.Latency > 0 {
			latSum[key] += l.Latency
			p.AvgLatency += 1
		}
	}
	// 补齐无数据的自然日，保证曲线连续可读
	out := make([]TrendPoint, 0, days)
	now := time.Now()
	for i := days - 1; i >= 0; i-- {
		day := now.AddDate(0, 0, -i)
		key := day.Format("01-02")
		p := byDay[key]
		if p == nil {
			p = &TrendPoint{Date: key}
		} else if p.AvgLatency > 0 {
			p.AvgLatency = round2(latSum[key] / p.AvgLatency)
		}
		p.Cost = round4(p.Cost)
		out = append(out, *p)
	}
	return out
}

func modelStats(in []RequestLog, pt PriceTable) []ModelStat {
	type acc struct {
		stat  ModelStat
		lat   []float64
		sizes []float64
	}
	m := map[string]*acc{}
	for _, l := range in {
		name := l.Model
		if name == "" {
			name = "(未指定)"
		}
		a := m[name]
		if a == nil {
			a = &acc{stat: ModelStat{Model: name}}
			m[name] = a
		}
		a.stat.Requests++
		a.stat.Tokens += l.Tokens
		a.stat.InputTokens += l.InputTokens
		a.stat.OutputTokens += l.OutputTokens
		a.stat.CacheTokens += l.CacheTokens
		if cost, priced := pt.Cost(l.Model, l.InputTokens, l.OutputTokens, l.CacheTokens, 0); priced {
			a.stat.Cost += cost
			a.stat.Priced = true
		}
		if l.Status >= 400 {
			a.stat.Errors++
		}
		if l.Latency > 0 {
			a.lat = append(a.lat, l.Latency)
			a.stat.AvgLatency += l.Latency
		}
		if l.Tokens > 0 {
			a.sizes = append(a.sizes, float64(l.Tokens))
		}
	}
	out := make([]ModelStat, 0, len(m))
	for _, a := range m {
		if len(a.lat) > 0 {
			a.stat.AvgLatency = round2(a.stat.AvgLatency / float64(len(a.lat)))
		}
		a.stat.P50 = percentile(a.lat, 50)
		a.stat.P90 = percentile(a.lat, 90)
		a.stat.P99 = percentile(a.lat, 99)
		if a.stat.Requests > 0 {
			a.stat.AvgTokens = round2(float64(a.stat.Tokens) / float64(a.stat.Requests))
		}
		a.stat.TokenP50 = percentile(a.sizes, 50)
		a.stat.TokenP90 = percentile(a.sizes, 90)
		a.stat.Cost = round4(a.stat.Cost)
		out = append(out, a.stat)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Tokens > out[j].Tokens })
	return out
}

func keyStats(in []RequestLog, pt PriceTable) []KeyStat {
	m := map[string]*KeyStat{}
	for _, l := range in {
		id := l.KeyID
		if id == "" {
			id = l.KeyName
		}
		if id == "" {
			id = "(未鉴权)"
		}
		k := m[id]
		if k == nil {
			k = &KeyStat{KeyID: id, KeyName: l.KeyName}
			m[id] = k
		}
		k.Requests++
		k.Tokens += l.Tokens
		if cost, priced := pt.Cost(l.Model, l.InputTokens, l.OutputTokens, l.CacheTokens, 0); priced {
			k.Cost += cost
		}
		if l.Status >= 400 {
			k.Errors++
		}
		if l.Latency > 0 {
			k.AvgLatency += l.Latency
		}
	}
	out := make([]KeyStat, 0, len(m))
	for _, k := range m {
		if k.Requests > 0 {
			k.AvgLatency = round2(k.AvgLatency / float64(k.Requests))
		}
		k.Cost = round4(k.Cost)
		out = append(out, *k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Tokens > out[j].Tokens })
	return out
}

func hourlyStats(in []RequestLog) []HourStat {
	out := make([]HourStat, 24)
	for i := range out {
		out[i] = HourStat{Hour: i, ByModel: map[string]int{}}
	}
	for _, l := range in {
		h := time.Unix(l.TS, 0).Hour()
		out[h].Requests++
		out[h].Tokens += l.Tokens
		name := l.Model
		if name == "" {
			name = "(未指定)"
		}
		out[h].ByModel[name] += l.Tokens
	}
	return out
}

func taskStats(tasks []TaskLog, r Range) []TaskStat {
	m := map[string]*TaskStat{}
	for _, t := range tasks {
		if t.TS < r.From || t.TS >= r.To {
			continue
		}
		s := m[t.Type]
		if s == nil {
			s = &TaskStat{Type: t.Type}
			m[t.Type] = s
		}
		s.Total++
		switch t.Status {
		case "success":
			s.Success++
		case "failed":
			s.Failed++
		default:
			s.Skipped++
		}
	}
	out := make([]TaskStat, 0, len(m))
	for _, s := range m {
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Type < out[j].Type })
	return out
}

// ---- 工具 ----

func dayKey(ts int64) string { return time.Unix(ts, 0).Format("01-02") }

// Percentile 线性插值分位（全项目统一口径，调用方无需自备排序）
func Percentile(values []float64, p float64) float64 {
	return percentile(values, p)
}

// percentile 线性插值分位
func percentile(values []float64, p float64) float64 {
	if len(values) == 0 {
		return 0
	}
	xs := make([]float64, len(values))
	copy(xs, values)
	sort.Float64s(xs)
	if len(xs) == 1 {
		return round2(xs[0])
	}
	rank := p / 100 * float64(len(xs)-1)
	lo := int(rank)
	hi := lo + 1
	if hi >= len(xs) {
		return round2(xs[len(xs)-1])
	}
	frac := rank - float64(lo)
	return round2(xs[lo] + (xs[hi]-xs[lo])*frac)
}

func round2(v float64) float64 {
	return float64(int64(v*100+0.5)) / 100
}

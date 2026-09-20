package core

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ============================================================
// WorkBuddy 客户端自身 token 消耗统计
//
// 数据源：本机 WorkBuddy 客户端会话日志（JSONL）——
//   国内版 ~/.workbuddy/projects/**/*.jsonl
//   国际版 ~/.workbuddy-ai/projects/ 与 sessions/（双根探测，缺哪个用哪个）
// 每条 assistant 记录携带真实 usage（输入/输出/缓存读写），纯本地聚合，
// 与官方 get-user-request-usage（仅 credit 口径）互为补充。
//
// 隐私红线：只提取数字与脱敏标识（时间/模型/项目目录名），不读不存消息正文。
// 无 mock：目录缺失返回空集与缺失说明，绝不编造。
// 口径与社区 workbuddy-switch 的 token_stats 实测协议一致：
//   total = input + output + cacheWrite（input 已含 cacheRead，避免重复计数）；
//   缓存命中率 = cacheRead / input。
// ============================================================

// clientStatsCacheTTL 扫描结果缓存时长
const clientStatsCacheTTL = 5 * time.Minute

// clientStatsMaxSessions 会话排行条数上限
const clientStatsMaxSessions = 30

// ClientUsageSummary 汇总指标
type ClientUsageSummary struct {
	Records      int     `json:"records"`
	TotalTokens  int     `json:"totalTokens"` // input + output + cacheWrite
	InputTokens  int     `json:"inputTokens"` // 含缓存命中部分
	OutputTokens int     `json:"outputTokens"`
	CacheRead    int     `json:"cacheRead"`
	CacheWrite   int     `json:"cacheWrite"`
	CacheHitRate float64 `json:"cacheHitRate"` // cacheRead / input × 100
	// Cost 按单价表换算的金额（元）；未定价模型不计入，
	// UnpricedModels 为窗口内出现过但没有单价的模型数（口径须对读者透明）；
	// AvgCostPerDay 为活跃自然日的日均金额（与网关口径一致）
	Cost           float64 `json:"cost"`
	AvgCostPerDay  float64 `json:"avgCostPerDay"`
	UnpricedModels int     `json:"unpricedModels"`
}

// ClientUsageDay 单日聚合
type ClientUsageDay struct {
	Date         string  `json:"date"` // MM-DD
	Records      int     `json:"records"`
	TotalTokens  int     `json:"totalTokens"`
	InputTokens  int     `json:"inputTokens"`
	OutputTokens int     `json:"outputTokens"`
	CacheRead    int     `json:"cacheRead"`
	CacheWrite   int     `json:"cacheWrite"`
	Cost         float64 `json:"cost"` // 按单价表换算的金额（元）
	// 日活分布：当日有 AI 调用的去重会话数 / 项目数
	ActiveSessions int `json:"activeSessions"`
	ActiveProjects int `json:"activeProjects"`
}

// ClientUsageModel 单模型聚合
type ClientUsageModel struct {
	Model       string  `json:"model"`
	Records     int     `json:"records"`
	TotalTokens int     `json:"totalTokens"`
	Cost        float64 `json:"cost"`   // 按单价表换算的金额（元）
	Priced      bool    `json:"priced"` // 该模型是否已配单价（false = 未定价，Cost 恒为 0）
}

// ClientUsageProject 单项目聚合
type ClientUsageProject struct {
	Project     string  `json:"project"`
	Records     int     `json:"records"`
	TotalTokens int     `json:"totalTokens"`
	Cost        float64 `json:"cost"` // 按单价表换算的金额（元）
}

// ClientUsageSession 单会话聚合
type ClientUsageSession struct {
	SessionID   string  `json:"sessionId"`
	Source      string  `json:"source"` // workbuddy / workbuddy-ai
	Title       string  `json:"title"`
	Project     string  `json:"project"`
	Records     int     `json:"records"`
	TotalTokens int     `json:"totalTokens"`
	Cost        float64 `json:"cost"` // 按单价表换算的金额（元）
	FirstTS     int64   `json:"firstTs"` // Unix 秒
	LastTS      int64   `json:"lastTs"`
}

// ClientTokenSource 单数据源（国内/国际版）扫描结果
type ClientTokenSource struct {
	Source       string             `json:"source"`
	Missing      bool               `json:"missing"` // 数据根不存在（未安装该版本客户端）
	FilesScanned int                `json:"filesScanned"`
	ParseErrors  int                `json:"parseErrors"`
	Summary      ClientUsageSummary `json:"summary"`
}

// ClientTokenStats 客户端自身 token 消耗总报告
type ClientTokenStats struct {
	GeneratedAt int64                `json:"generatedAt"`
	Days        int                  `json:"days"`
	Summary     ClientUsageSummary   `json:"summary"` // 两档位合计（不同账号体系，分源见 Sources）
	Daily       []ClientUsageDay     `json:"daily"`
	Models      []ClientUsageModel   `json:"models"`
	Projects    []ClientUsageProject `json:"projects"`
	Sessions    []ClientUsageSession `json:"sessions"`
	Sources     []ClientTokenSource  `json:"sources"`

	// 日活分布日历：行 = 天（MM-DD），列 = 72 个 20 分钟桶，值 = 桶内去重活跃会话数
	ActiveHeatmapDays []string `json:"activeHeatmapDays"`
	ActiveHeatmap     [][]int  `json:"activeHeatmap"`
}

// ---------- 原始记录解析（只取需要的字段） ----------

// rawUsageObj 兼容 snake / camel 两种形态的 usage 对象
type rawUsageObj struct {
	Input       *float64 `json:"input_tokens"`
	InputCamel  *float64 `json:"inputTokens"`
	Prompt      *float64 `json:"prompt_tokens"`
	Output      *float64 `json:"output_tokens"`
	OutputCamel *float64 `json:"outputTokens"`
	Completion  *float64 `json:"completion_tokens"`
	CacheRead   *float64 `json:"cache_read_input_tokens"`
	CacheReadC  *float64 `json:"cacheReadInputTokens"`
	CacheHit    *float64 `json:"prompt_cache_hit_tokens"`
	Cached      *float64 `json:"cached_tokens"`
	CacheWrite  *float64 `json:"cache_write_input_tokens"`
	CacheWriteC *float64 `json:"cacheWriteInputTokens"`
	CacheCreate *float64 `json:"cache_creation_input_tokens"`
	PTD         struct {
		Cached *float64 `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	ITD []struct {
		Cached *float64 `json:"cached_tokens"`
	} `json:"inputTokensDetails"`
}

func (u *rawUsageObj) pick(vals ...*float64) float64 {
	for _, v := range vals {
		if v != nil {
			return *v
		}
	}
	return 0
}

func (u *rawUsageObj) input() float64 {
	return u.pick(u.Input, u.InputCamel, u.Prompt)
}

func (u *rawUsageObj) output() float64 {
	return u.pick(u.Output, u.OutputCamel, u.Completion)
}

func (u *rawUsageObj) read() float64 {
	return u.pick(u.CacheRead, u.CacheReadC, u.CacheHit, u.Cached, u.PTD.Cached)
}

func (u *rawUsageObj) write() float64 {
	v := u.pick(u.CacheWrite, u.CacheWriteC, u.CacheCreate)
	for _, d := range u.ITD { // camel 形态的明细数组兜底
		if d.Cached != nil && *d.Cached > v {
			v = *d.Cached
		}
	}
	return v
}

// rawRecord JSONL 单行（仅解析统计所需字段，正文不读不存）
type rawRecord struct {
	Timestamp json.Number `json:"timestamp"`
	CWD       string      `json:"cwd"`
	Model     string      `json:"model"`
	Message   struct {
		Usage *rawUsageObj `json:"usage"`
	} `json:"message"`
	ProviderData struct {
		Model string       `json:"model"`
		Usage *rawUsageObj `json:"usage"`
	} `json:"providerData"`
	Usage *rawUsageObj `json:"usage"`
}

// clientRecord 归一化后的一条用量记录（ts 为 Unix 秒）
type clientRecord struct {
	ts      int64
	in, out int
	read    int
	write   int
	model   string
	project string
	sessID  string
}

// ---------- 扫描与聚合 ----------

type clientSourceRoots struct {
	source string
	roots  []string
}

// userHomeDir 可注入的 home 目录（测试用）
var userHomeDir = os.UserHomeDir

// clientStatsRoots 返回各档位数据根（不存在时 Missing 如实上报，不报错）
func clientStatsRoots() []clientSourceRoots {
	home, err := userHomeDir()
	if err != nil {
		return nil
	}
	cn := filepath.Join(home, ".workbuddy", "projects")
	aiP := filepath.Join(home, ".workbuddy-ai", "projects")
	aiS := filepath.Join(home, ".workbuddy-ai", "sessions")
	out := []clientSourceRoots{{source: "workbuddy", roots: []string{cn}}}
	ai := clientSourceRoots{source: "workbuddy-ai"}
	for _, r := range []string{aiP, aiS} {
		if st, err := os.Stat(r); err == nil && st.IsDir() {
			ai.roots = append(ai.roots, r)
		}
	}
	out = append(out, ai)
	return out
}

// scanClientSource 扫描单个档位：按 mtime 从旧到新处理，六元组指纹去重
// （复制/fork 会话会重放父会话历史，先到先得，用量归属原始会话）。
func scanClientSource(src clientSourceRoots, cutoffMs, maxMs int64) ([]clientRecord, ClientTokenSource, error) {
	res := ClientTokenSource{Source: src.source, Missing: len(src.roots) == 0}
	// 收集文件（跳过 subagents：子代理日志重复父会话上下文）
	type jsonlFile struct {
		path  string
		mtime time.Time
	}
	var files []jsonlFile
	for _, root := range src.roots {
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil // 单个不可读路径跳过
			}
			if d.IsDir() {
				if d.Name() == "subagents" {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasSuffix(d.Name(), ".jsonl") {
				if info, err := d.Info(); err == nil {
					files = append(files, jsonlFile{path, info.ModTime()})
				}
			}
			return nil
		})
		if err != nil && !os.IsNotExist(err) {
			return nil, res, err
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mtime.Before(files[j].mtime) })

	var records []clientRecord
	seen := map[string]struct{}{}
	for _, f := range files {
		res.FilesScanned++
		rows, errs := parseClientJSONL(f.path, cutoffMs, maxMs)
		res.ParseErrors += errs
		for i := range rows {
			fp := fmt.Sprintf("%d|%d|%d|%d|%d|%s", rows[i].ts, rows[i].in, rows[i].out, rows[i].read, rows[i].write, rows[i].model)
			if _, dup := seen[fp]; dup {
				continue
			}
			seen[fp] = struct{}{}
			records = append(records, rows[i])
		}
	}
	return records, res, nil
}

// parseClientJSONL 解析单个会话文件，返回窗口内记录（标题由 collectClientTitles 统一回填）
func parseClientJSONL(path string, cutoffMs, maxMs int64) (rows []clientRecord, parseErrors int) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, 1
	}
	sessID := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var r rawRecord
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			parseErrors++
			continue
		}
		u := r.Message.Usage
		if u == nil {
			u = r.ProviderData.Usage
		}
		if u == nil {
			u = r.Usage
		}
		if u == nil {
			continue // 无 usage 的记录（用户消息等）
		}
		in := u.input()
		// 有效性锚点：input 字段存在（可为 0），而非 > 0
		if u.Input == nil && u.InputCamel == nil && u.Prompt == nil {
			continue
		}
		tsMs := tsToMillis(r.Timestamp)
		if tsMs < cutoffMs || tsMs > maxMs {
			continue // 无时间戳（0）或窗口外一律排除，不猜测
		}
		model := r.ProviderData.Model
		if model == "" {
			model = r.Model
		}
		rows = append(rows, clientRecord{
			ts:      tsMs / 1000,
			in:      int(in),
			out:     int(u.output()),
			read:    int(u.read()),
			write:   int(u.write()),
			model:   model,
			project: clientProjectName(r.CWD),
			sessID:  sessID,
		})
	}
	return rows, parseErrors
}

// tsToMillis 时间戳兼容数字与数字字符串（毫秒）；非法返回 0（窗口外排除）
func tsToMillis(n json.Number) int64 {
	if n == "" {
		return 0
	}
	if v, err := n.Int64(); err == nil {
		return v
	}
	if v, err := strconv.ParseFloat(n.String(), 64); err == nil {
		return int64(v)
	}
	return 0
}

// clientProjectName 项目名：cwd 取 basename（≤120 字符）；无法归因时如实标注
func clientProjectName(cwd string) string {
	name := strings.TrimSpace(cwd)
	if name == "" {
		return "(未知项目)"
	}
	name = filepath.Base(name)
	if name == "." || name == string(filepath.Separator) || name == "/" {
		return "(未知项目)"
	}
	b := []rune(name)
	if len(b) > 120 {
		b = b[:120]
	}
	return string(b)
}

// ---------- Service 层入口（带缓存） ----------

type clientStatsCache struct {
	mu       sync.Mutex
	report   *ClientTokenStats
	at       time.Time
	fetching bool
}

var csCache clientStatsCache

// invalidateClientStatsCache 丢弃客户端用量缓存：单价表随配置变更后，
// 报告里的金额口径需立即重算，不吃 5 分钟旧缓存。
func invalidateClientStatsCache() {
	csCache.mu.Lock()
	csCache.report = nil
	csCache.mu.Unlock()
}

// clientUncached 客户端口径的「未命中输入」：日志里的 input 已含缓存读，
// 计价前须扣除，否则同一批 token 会被输入价与缓存价重复计一次。
func clientUncached(r clientRecord) int {
	if r.in <= r.read {
		return 0
	}
	return r.in - r.read
}

// ClientTokenStats 聚合 WorkBuddy 客户端自身最近 days 天的 token 消耗（带缓存）。
// force = 跳过缓存重新扫描。目录缺失的档位如实标注 missing，不报错。
func (s *Service) ClientTokenStats(days int, force bool) (*ClientTokenStats, error) {
	if days <= 0 {
		days = 7
	}
	if days > 90 {
		days = 90
	}
	csCache.mu.Lock()
	if !force && csCache.report != nil && csCache.report.Days == days &&
		time.Since(csCache.at) < clientStatsCacheTTL {
		rep := csCache.report
		csCache.mu.Unlock()
		return rep, nil
	}
	csCache.mu.Unlock()

	now := time.Now()
	cutoffMs := now.AddDate(0, 0, -(days - 1)).
		Truncate(24 * time.Hour).UnixMilli() // 本地自然日对齐
	maxMs := now.UnixMilli() + 24*3600*1000

	rep := &ClientTokenStats{GeneratedAt: now.Unix(), Days: days}
	type srcRecords struct {
		src     ClientTokenSource
		records []clientRecord
		titles  map[string]string // sessID → title（同 ID 跨文件先到先得）
	}
	var perSource []srcRecords

	for _, src := range clientStatsRoots() {
		records, meta, err := scanClientSource(src, cutoffMs, maxMs)
		item := srcRecords{src: meta}
		if err != nil {
			// 数据根整体不可读：如实标注，不中断其他档位
			meta.ParseErrors++
		}
		item.records = records
		item.titles = map[string]string{}
		// 会话标题：按文件重扫太浪费，扫描时已带出（parseClientJSONL 内聚），
		// 这里从记录所属文件无法直接拿标题——改为二次轻量扫描会话首行标题。
		collectClientTitles(src, item.titles)
		perSource = append(perSource, item)
	}

	// 汇总（分源归因 + 合计）
	type acc struct {
		sum        ClientUsageSummary
		daily      map[string]*ClientUsageDay
		dailySess  map[string]map[string]struct{} // date → 去重会话键
		dailyProj  map[string]map[string]struct{} // date → 去重项目名
		bucketSess map[string]map[string]struct{} // "date|bucket" → 去重会话键（20 分钟桶）
		models     map[string]*ClientUsageModel
		projects   map[string]*ClientUsageProject
		sess       map[string]*ClientUsageSession
	}
	all := acc{
		daily: map[string]*ClientUsageDay{}, dailySess: map[string]map[string]struct{}{}, dailyProj: map[string]map[string]struct{}{},
		bucketSess: map[string]map[string]struct{}{},
		models:     map[string]*ClientUsageModel{}, projects: map[string]*ClientUsageProject{}, sess: map[string]*ClientUsageSession{},
	}

	add := func(a *acc, r clientRecord, cost float64, priced bool, source, title string) {
		a.sum.Records++
		a.sum.InputTokens += r.in
		a.sum.OutputTokens += r.out
		a.sum.CacheRead += r.read
		a.sum.CacheWrite += r.write
		a.sum.TotalTokens += r.in + r.out + r.write
		if priced {
			a.sum.Cost += cost
		}
		lt := time.Unix(r.ts, 0)
		date := lt.Format("01-02")
		d := a.daily[date]
		if d == nil {
			d = &ClientUsageDay{Date: date}
			a.daily[date] = d
		}
		d.Records++
		d.InputTokens += r.in
		d.OutputTokens += r.out
		d.CacheRead += r.read
		d.CacheWrite += r.write
		d.TotalTokens += r.in + r.out + r.write
		if priced {
			d.Cost += cost
		}
		ds := a.dailySess[date]
		if ds == nil {
			ds = map[string]struct{}{}
			a.dailySess[date] = ds
		}
		dp := a.dailyProj[date]
		if dp == nil {
			dp = map[string]struct{}{}
			a.dailyProj[date] = dp
		}
		// 20 分钟桶（日历热力图用）：桶内去重会话
		bk := lt.Hour()*3 + lt.Minute()/20
		bkey := date + "|" + strconv.Itoa(bk)
		bs := a.bucketSess[bkey]
		if bs == nil {
			bs = map[string]struct{}{}
			a.bucketSess[bkey] = bs
		}
		mn := r.model
		if mn == "" {
			mn = "(未指定)"
		}
		m := a.models[mn]
		if m == nil {
			m = &ClientUsageModel{Model: mn}
			a.models[mn] = m
		}
		m.Records++
		m.TotalTokens += r.in + r.out + r.write
		if priced {
			m.Cost += cost
			m.Priced = true
		}
		p := a.projects[r.project]
		if p == nil {
			p = &ClientUsageProject{Project: r.project}
			a.projects[r.project] = p
		}
		p.Records++
		p.TotalTokens += r.in + r.out + r.write
		if priced {
			p.Cost += cost
		}
		sk := source + "|" + r.sessID
		ds[sk] = struct{}{}
		dp[r.project] = struct{}{}
		bs[sk] = struct{}{}
		sv := a.sess[sk]
		if sv == nil {
			sv = &ClientUsageSession{SessionID: r.sessID, Source: source, Title: title, Project: r.project}
			a.sess[sk] = sv
		}
		sv.Records++
		sv.TotalTokens += r.in + r.out + r.write
		if priced {
			sv.Cost += cost
		}
		if sv.FirstTS == 0 || r.ts < sv.FirstTS {
			sv.FirstTS = r.ts
		}
		if r.ts > sv.LastTS {
			sv.LastTS = r.ts
		}
	}

	// 分源汇总（各自归因）+ 合计
	pt := NewPriceTable(s.ModelPrices())
	unpriced := map[string]struct{}{}
	for i := range perSource {
		sum := &perSource[i].src.Summary
		srcUnpriced := map[string]struct{}{}
		for _, r := range perSource[i].records {
			sum.Records++
			sum.InputTokens += r.in
			sum.OutputTokens += r.out
			sum.CacheRead += r.read
			sum.CacheWrite += r.write
			sum.TotalTokens += r.in + r.out + r.write
			cost, priced := pt.Cost(r.model, clientUncached(r), r.out, r.read, r.write)
			if priced {
				sum.Cost += cost
			} else if r.in+r.out+r.write > 0 {
				// 与网关同口径：只统计真有 token 用量的未定价模型
				srcUnpriced[r.model] = struct{}{}
				unpriced[r.model] = struct{}{}
			}
			add(&all, r, cost, priced, perSource[i].src.Source, perSource[i].titles[r.sessID])
		}
		if sum.InputTokens > 0 {
			sum.CacheHitRate = round2(float64(sum.CacheRead) / float64(sum.InputTokens) * 100)
		}
		sum.Cost = round4(sum.Cost)
		sum.UnpricedModels = len(srcUnpriced)
		rep.Sources = append(rep.Sources, perSource[i].src)
	}

	rep.Summary = all.sum
	rep.Summary.Cost = round4(rep.Summary.Cost)
	rep.Summary.UnpricedModels = len(unpriced)
	// daily 只含真有记录的日期，其条数即活跃自然日数
	if active := len(all.daily); active > 0 {
		rep.Summary.AvgCostPerDay = round4(rep.Summary.Cost / float64(active))
	}
	if all.sum.InputTokens > 0 {
		rep.Summary.CacheHitRate = round2(float64(all.sum.CacheRead) / float64(all.sum.InputTokens) * 100)
	}
	for _, d := range all.daily {
		d.ActiveSessions = len(all.dailySess[d.Date])
		d.ActiveProjects = len(all.dailyProj[d.Date])
		d.Cost = round4(d.Cost)
		rep.Daily = append(rep.Daily, *d)
		// 日历热力图行（与 Daily 同序构建，保持对齐）
		row := make([]int, 72)
		for b := 0; b < 72; b++ {
			if s, ok := all.bucketSess[d.Date+"|"+strconv.Itoa(b)]; ok {
				row[b] = len(s)
			}
		}
		rep.ActiveHeatmap = append(rep.ActiveHeatmap, row)
		rep.ActiveHeatmapDays = append(rep.ActiveHeatmapDays, d.Date)
	}
	sort.Slice(rep.Daily, func(i, j int) bool { return rep.Daily[i].Date < rep.Daily[j].Date })
	for _, m := range all.models {
		m.Cost = round4(m.Cost)
		rep.Models = append(rep.Models, *m)
	}
	sort.Slice(rep.Models, func(i, j int) bool { return rep.Models[i].TotalTokens > rep.Models[j].TotalTokens })
	for _, p := range all.projects {
		p.Cost = round4(p.Cost)
		rep.Projects = append(rep.Projects, *p)
	}
	sort.Slice(rep.Projects, func(i, j int) bool { return rep.Projects[i].TotalTokens > rep.Projects[j].TotalTokens })
	for _, sv := range all.sess {
		sv.Cost = round4(sv.Cost)
		rep.Sessions = append(rep.Sessions, *sv)
	}
	sort.Slice(rep.Sessions, func(i, j int) bool {
		if rep.Sessions[i].TotalTokens != rep.Sessions[j].TotalTokens {
			return rep.Sessions[i].TotalTokens > rep.Sessions[j].TotalTokens
		}
		return rep.Sessions[i].LastTS > rep.Sessions[j].LastTS
	})
	if len(rep.Sessions) > clientStatsMaxSessions {
		rep.Sessions = rep.Sessions[:clientStatsMaxSessions]
	}

	csCache.mu.Lock()
	csCache.report = rep
	csCache.at = time.Now()
	csCache.mu.Unlock()
	return rep, nil
}

// collectClientTitles 轻量扫描会话文件首行，回填会话标题（aiTitle > summary）。
// 只解析行首字段即丢弃，不触碰消息正文。
func collectClientTitles(src clientSourceRoots, titles map[string]string) {
	for _, root := range src.roots {
		_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(d.Name(), ".jsonl") {
				if d != nil && d.IsDir() && d.Name() == "subagents" {
					return filepath.SkipDir
				}
				return nil
			}
			sessID := strings.TrimSuffix(filepath.Base(path), ".jsonl")
			if _, ok := titles[sessID]; ok {
				return nil
			}
			if title := firstTitleOfJSONL(path); title != "" {
				titles[sessID] = title
			}
			return nil
		})
	}
}

// firstTitleOfJSONL 读取会话文件前若干行找标题（只取标题字段）
func firstTitleOfJSONL(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	buf := make([]byte, 0, 64<<10)
	tmp := make([]byte, 32<<10)
	for len(buf) < 256<<10 {
		n, err := f.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
		}
		if err != nil {
			break
		}
	}
	for _, line := range strings.Split(string(buf), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var head struct {
			AITitle any    `json:"aiTitle"`
			Summary string `json:"summary"`
		}
		if json.Unmarshal([]byte(line), &head) != nil {
			continue
		}
		if s, ok := head.AITitle.(string); ok && s != "" {
			return s
		}
		if head.Summary != "" {
			return head.Summary
		}
	}
	return ""
}

// ---------- 会话标题查询（供网关会话下钻等场景复用） ----------

var clientTitleCache struct {
	mu sync.Mutex
	m  map[string]string
	at time.Time
}

// ClientSessionTitles 全量会话 ID → 标题映射（5 分钟缓存）。
// 网关下钻的 session_id 若与本机会话 UUID 一致即可回填标题；查不到由调用方回退展示 ID。
func ClientSessionTitles() map[string]string {
	clientTitleCache.mu.Lock()
	defer clientTitleCache.mu.Unlock()
	if clientTitleCache.m != nil && time.Since(clientTitleCache.at) < clientStatsCacheTTL {
		return clientTitleCache.m
	}
	m := map[string]string{}
	for _, src := range clientStatsRoots() {
		collectClientTitles(src, m)
	}
	clientTitleCache.m = m
	clientTitleCache.at = time.Now()
	return m
}

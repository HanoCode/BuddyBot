package ferry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"
	"sort"
	"sync"
	"time"
)

// Manager 免费额度摆渡的编排层：运行时生命周期、模型目录、探测队列、判定缓存、勾选。
//
// 与参考实现的一处刻意差别：免费模型不写进任何客户端配置文件，而是挂到 BuddyBot
// 网关自身的模型清单上（见 core 层的接入）。因此这里没有「导入 / 退出时摘干净」那一路。

// translatorPreference 翻译兜底挑模型时的偏好顺序
var translatorPreference = []string{
	"opencode/big-pickle", "opencode/nemotron-3.5-lightning-free", "opencode/spice-bunny-free", "opencode/mimo-v2.6-flash-free",
}

type settings struct {
	UseSystemProxy *bool    `json:"useSystemProxy,omitempty"`
	ExcludedModels []string `json:"excludedModels,omitempty"`
}

// Manager 编排器
type Manager struct {
	cfg Config

	mu       sync.Mutex
	started  bool
	stopping bool
	stopCh   chan struct{}

	phase   string
	message string
	lastErr string

	logs    *fileLogger
	binary  string
	runtime *Runtime
	backend *Backend
	cache   *probeCache

	models    []Model
	verdicts  map[string]*Verdict
	validated map[string]bool
	excluded  map[string]bool

	probing      bool
	probeActive  []string
	probePending []string

	proxyEnabled  bool
	// proxyExplicit 用户是否明确选择过代理开关：明确选过就出错要报出来，
	// 没选过则「本机没配手动代理」不该阻塞下载
	proxyExplicit bool

	sem chan struct{}
}

// New 创建一个 Manager（不启动）
func New(cfg Config) *Manager {
	cfg = cfg.withDefaults()
	m := &Manager{
		cfg:       cfg,
		verdicts:  map[string]*Verdict{},
		validated: map[string]bool{},
		excluded:  map[string]bool{},
		sem:       make(chan struct{}, maxConcurrentRequests),
		cache:     newProbeCache(cfg.probeCachePath(), cfg, cfg.Logf),
		phase:     "disabled",
		message:   "未启用",
		proxyEnabled: runtime.GOOS == "windows",
	}
	m.loadSettings()
	return m
}

// DataDir 数据目录
func (m *Manager) DataDir() string { return m.cfg.DataDir }

// ---- 生命周期 ----

// Start 启动（异步；不阻塞调用方 —— 首次启动要下载隔离运行时）
func (m *Manager) Start() {
	m.mu.Lock()
	if m.started {
		m.mu.Unlock()
		return
	}
	m.started, m.stopping = true, false
	m.stopCh = make(chan struct{})
	m.mu.Unlock()
	go m.bootstrap()
}

// Stop 停止并释放隔离运行时
func (m *Manager) Stop() {
	m.mu.Lock()
	if !m.started {
		m.mu.Unlock()
		return
	}
	m.started, m.stopping = false, true
	close(m.stopCh)
	rt := m.runtime
	m.runtime, m.backend = nil, nil
	m.probing = false
	m.mu.Unlock()
	if rt != nil {
		rt.Stop()
	}
	m.setPhase("disabled", "已停止")
}

// Enabled 是否处于运行态
func (m *Manager) Enabled() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.started
}

func (m *Manager) isStopping() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.stopping || !m.started
}

func (m *Manager) setPhase(phase, message string) {
	m.mu.Lock()
	m.phase, m.message = phase, message
	m.mu.Unlock()
}

func (m *Manager) fail(message string) {
	m.mu.Lock()
	m.phase, m.message, m.lastErr = "error", message, message
	m.mu.Unlock()
	m.cfg.Logf("%s", message)
}

// bootstrap 启动流程：解析代理 → 准备运行时 → 起隔离服务 → 校验专用 agent → 读目录 → 探测
func (m *Manager) bootstrap() {
	if err := os.MkdirAll(m.cfg.DataDir, 0o700); err != nil {
		m.fail("创建免费额度数据目录失败: " + err.Error())
		return
	}
	m.logs = newFileLogger(m.cfg.logPath())
	defer func() {
		m.mu.Lock()
		stopping := m.stopping
		m.mu.Unlock()
		if stopping && m.logs != nil {
			m.logs.Close()
		}
	}()
	m.cache.load()
	m.setPhase("starting", "正在准备隔离的 OpenCode 运行时")

	proxyEnv, err := m.resolveProxyEnv()
	if err != nil {
		m.fail(err.Error())
		return
	}
	if m.isStopping() {
		return
	}

	fetcher := &runtimeFetcher{
		client: fetchHTTPClient(proxyEnv), registries: []string{registryOfficial, registryMirror},
		logf: m.logfToFile,
	}
	if proxyEnv["HTTPS_PROXY"] != "" {
		fetcher.proxyURL = proxyEnv["HTTPS_PROXY"]
	}
	binary, err := findRuntime(m.cfg, fetcher, func(msg string) { m.setPhase("downloading", msg) })
	if err != nil {
		m.fail("准备隔离 OpenCode 运行时失败: " + err.Error())
		return
	}
	if m.isStopping() {
		return
	}
	m.mu.Lock()
	m.binary = binary
	m.mu.Unlock()

	m.setPhase("starting", "正在启动隔离模型服务")
	rt, err := startBackend(m.cfg, binary, proxyEnv, m.logs)
	if err != nil {
		m.fail("启动隔离模型服务失败: " + err.Error())
		return
	}
	if m.isStopping() {
		rt.Stop()
		return
	}
	agents, err := rt.Backend.Agents(context.Background())
	if err != nil || !containsString(agents, adapterAgentName) {
		rt.Stop()
		m.fail("隔离 OpenCode 没有加载专用 agent，拒绝继续（配置可能被外部覆盖）")
		return
	}
	m.mu.Lock()
	m.runtime, m.backend = rt, rt.Backend
	m.mu.Unlock()
	rt.Backend.translator = m.pickTranslator

	go func() {
		<-rt.Done()
		if !m.isStopping() {
			m.fail("隔离 OpenCode 服务退出，请重新启用免费额度")
		}
	}()

	if err := m.Refresh(); err != nil {
		m.fail("读取免费模型失败: " + err.Error())
		return
	}
	m.ProbeAll(true)
}

func (m *Manager) logfToFile(format string, args ...any) {
	if m.logs != nil {
		m.logs.Writef(format, args...)
	}
	m.cfg.Logf(format, args...)
}

// resolveProxyEnv 解析要注入隔离运行时的代理环境。
// 一台根本没配手动代理的机器也必须能下载：只有用户明确选择过「跟随系统代理」时才报错。
func (m *Manager) resolveProxyEnv() (map[string]string, error) {
	m.mu.Lock()
	enabled := m.proxyEnabled
	explicit := m.proxyExplicit
	m.mu.Unlock()
	env, err := SystemProxyEnv(enabled)
	if err == nil {
		return env, nil
	}
	if explicit {
		return nil, errors.New("读取系统代理失败: " + err.Error())
	}
	m.mu.Lock()
	m.proxyEnabled = false
	m.mu.Unlock()
	m.logfToFile("未检测到可用的系统代理，改为直连：%v", err)
	return SystemProxyEnv(false)
}

// pickTranslator 翻译兜底要挑另一个能干活的免费模型
func (m *Manager) pickTranslator(failed string) string {
	usable := []string{}
	for _, model := range m.AvailableModels() {
		if model.ID != failed {
			usable = append(usable, model.ID)
		}
	}
	for _, id := range translatorPreference {
		if containsString(usable, id) {
			return id
		}
	}
	if len(usable) > 0 {
		return usable[0]
	}
	return ""
}

// ---- 模型目录 ----

// Refresh 重读隔离 OpenCode 的模型目录
func (m *Manager) Refresh() error {
	m.mu.Lock()
	backend := m.backend
	m.mu.Unlock()
	if backend == nil {
		return errors.New("隔离模型服务未就绪")
	}
	m.mu.Lock()
	m.phase, m.message = "reading", "正在读取免费模型"
	m.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	models, err := backend.Models(ctx)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.models = models
	// 目录里已经没有的模型，判定留着只会让缓存越来越大
	unjudged := 0
	for _, model := range models {
		if m.verdicts[model.ID] == nil {
			unjudged++
		}
	}
	m.mu.Unlock()
	m.cache.prune(models)
	// 还有模型没判定就不能报 ready：否则界面会显示「运行中」，而列表里一堆「未检测」，
	// 外部观察者（面板轮询、端到端测试）也会以为检测已经结束。
	m.setPhase(catalogPhase(len(models), unjudged))
	return nil
}

// catalogPhase 目录读完后的阶段。判定是从另一个来源（缓存 / 探测）来的，
// 所以「读到目录」并不等于「检测完成」——只有全部模型都有判定才是真 ready。
func catalogPhase(count, unjudged int) (string, string) {
	if unjudged > 0 {
		return "reading", fmt.Sprintf("已读到 %d 个免费模型，等待检测", count)
	}
	return "ready", fmt.Sprintf("运行中 · %d 个免费模型", count)
}

// snapshotModels 目录快照
func (m *Manager) snapshotModels() []Model {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Model(nil), m.models...)
}

// AvailableModels 已判定可用且未被排除的模型（网关 /v1/models 用）
func (m *Manager) AvailableModels() []Model {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Model, 0, len(m.models))
	for _, model := range m.models {
		verdict := m.verdicts[model.ID]
		if verdict == nil || !verdict.OK || m.excluded[model.ID] {
			continue
		}
		model.chatOnlyFlag = verdict.ChatOnly
		out = append(out, model)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// HasModel 该模型当前是否由本服务提供
func (m *Manager) HasModel(id string) bool {
	for _, model := range m.AvailableModels() {
		if model.ID == id {
			return true
		}
	}
	return false
}

// ---- 探测 ----

// ProbeAll 探测全部模型；force 忽略判定缓存（用户点「重新检测」时用）
func (m *Manager) ProbeAll(force bool) {
	m.startProbes(nil, force)
}

// ProbeOne 只重测一个模型
func (m *Manager) ProbeOne(id string, force bool) error {
	if len(id) > 0 {
		found := false
		for _, model := range m.snapshotModels() {
			if model.ID == id {
				found = true
				break
			}
		}
		if !found {
			return errors.New("该模型不在当前目录中")
		}
	}
	m.startProbes([]string{id}, force)
	return nil
}

// startProbes 检测队列：先采用缓存判定，剩下的才真的发请求
func (m *Manager) startProbes(only []string, force bool) {
	m.mu.Lock()
	if m.probing || m.backend == nil {
		m.mu.Unlock()
		return
	}
	selected := make([]Model, 0, len(m.models))
	for _, model := range m.models {
		if len(only) > 0 && !containsString(only, model.ID) {
			continue
		}
		selected = append(selected, model)
	}
	if len(selected) == 0 {
		m.mu.Unlock()
		// 目录里一个模型都没有：别把阶段停在「等待检测」上
		m.setPhase("ready", "运行中 · 0 个免费模型")
		return
	}
	m.probing = true
	m.mu.Unlock()

	queue := make([]Model, 0, len(selected))
	if !force {
		for _, model := range selected {
			if verdict := m.adoptCached(model); verdict != nil {
				continue
			}
			queue = append(queue, model)
		}
	} else {
		queue = append(queue, selected...)
	}

	pending := make([]string, 0, len(queue))
	for _, model := range queue {
		pending = append(pending, model.ID)
	}
	m.setProbeState(nil, pending)
	if len(pending) > 0 {
		m.setPhase("probing", fmt.Sprintf("正在检测 %d 个免费模型", len(pending)))
	}

	go func() {
		defer func() {
			m.mu.Lock()
			m.probing = false
			m.mu.Unlock()
			m.setProbeState(nil, nil)
			m.cache.prune(m.snapshotModels())
			m.mu.Lock()
			count := len(m.models)
			failed := m.phase == "error"
			m.mu.Unlock()
			// 探测途中隔离服务挂掉的话，别用「运行中」把错误盖掉
			if !failed {
				m.setPhase("ready", fmt.Sprintf("运行中 · %d 个免费模型", count))
			}
		}()
		workers := m.cfg.Concurrency
		if workers > len(queue) {
			workers = len(queue)
		}
		var wg sync.WaitGroup
		var idx int
		var idxMu sync.Mutex
		for worker := 0; worker < workers; worker++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					idxMu.Lock()
					if idx >= len(queue) {
						idxMu.Unlock()
						return
					}
					model := queue[idx]
					idx++
					idxMu.Unlock()

					m.markProbeActive(model.ID, true)
					m.probeOneModel(model)
					m.markProbeActive(model.ID, false)
					m.removePending(model.ID)
				}
			}()
		}
		wg.Wait()
	}()
}

// adoptCached 缓存里还有新鲜判定的模型直接采用：不花一次请求，也不再等它
func (m *Manager) adoptCached(model Model) *Verdict {
	entry := m.cache.fresh(model)
	if entry == nil {
		return nil
	}
	verdict := &Verdict{
		Model: model.ID, OK: entry.OK, ChatOnly: entry.ChatOnly,
		Category: entry.Category, Time: time.UnixMilli(entry.At).Format(time.RFC3339),
		DurationMs: entry.DurationMs, Error: entry.Message, Source: sourceCache,
	}
	m.applyVerdict(verdict)
	return verdict
}

// probeOneModel 每个模型有自己的 60 秒预算
func (m *Manager) probeOneModel(model Model) {
	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()

	// OpenCode 目录里明说没有工具能力的模型不必浪费一次探测
	if !model.Toolcall {
		verdict := verdictFromError(model.ID, newBridgeError(502, "invalid_tool_call", "OpenCode 目录未声明该模型支持工具调用"))
		m.finishProbe(model, verdict, started)
		return
	}
	err := m.runProbe(ctx, model)
	if err == nil {
		verdict := Verdict{Model: model.ID, OK: true, Category: "available", Time: nowISO(), Source: sourceProbe}
		m.cache.record(model, cacheEntry{OK: true, At: time.Now().UnixMilli(), DurationMs: time.Since(started).Milliseconds()})
		m.finishProbe(model, verdict, started)
		return
	}
	switch {
	case err.Code == "no_action":
		// 只回文本说明「能用」，不是「不能用」：按仅对话发布并关掉工具
		verdict := Verdict{
			Model: model.ID, OK: true, ChatOnly: true, Category: "available",
			Time: nowISO(), Code: "chat_only", Error: "探测时只返回文本、未产生动作；已按仅对话发布",
			DurationMs: time.Since(started).Milliseconds(), Source: sourceProbe,
		}
		m.cache.record(model, cacheEntry{OK: true, ChatOnly: true, At: time.Now().UnixMilli(), DurationMs: time.Since(started).Milliseconds()})
		m.finishProbe(model, verdict, started)
	case formatUnsupported(err):
		// 格式装不下工具调用：再用一次纯对话确认它至少能聊天
		chatModel := model
		chatModel.chatOnlyFlag = true
		chatCtx, chatCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer chatCancel()
		request, perr := prepare([]byte(`{"model":"`+model.ID+`","messages":[{"role":"user","content":"Reply only OK."}]}`), []Model{chatModel})
		if perr != nil {
			m.finishProbe(model, verdictFromError(model.ID, err), started)
			return
		}
		meta := &requestMeta{Model: model.ID, Probe: true}
		if _, cerr := m.backend.Complete(chatCtx, request, meta); cerr == nil {
			verdict := Verdict{
				Model: model.ID, OK: true, ChatOnly: true, Category: "available", Time: nowISO(),
				Code: "chat_only", Error: "工具转换不兼容：" + err.Message,
				DurationMs: time.Since(started).Milliseconds(), Source: sourceProbe,
			}
			m.cache.record(model, cacheEntry{OK: true, ChatOnly: true, At: time.Now().UnixMilli(), DurationMs: time.Since(started).Milliseconds()})
			m.finishProbe(model, verdict, started)
			return
		} else {
			m.finishProbe(model, verdictFromError(model.ID, probeDeadlineError(chatCtx, cerr)), started)
			return
		}
	default:
		verdict := verdictFromError(model.ID, err)
		m.cache.record(model, cacheEntry{
			OK: false, Category: verdict.Category, Message: verdict.Error,
			At: time.Now().UnixMilli(), DurationMs: time.Since(started).Milliseconds(),
		})
		m.finishProbe(model, verdict, started)
	}
}

func (m *Manager) finishProbe(model Model, verdict Verdict, started time.Time) {
	// 判定按模型 ID 索引，这里统一兜底：任何一条失败分支忘了带 ID 都不会再被写成空键判定
	verdict.Model = model.ID
	if verdict.DurationMs == 0 {
		verdict.DurationMs = time.Since(started).Milliseconds()
	}
	if verdict.Source == "" {
		verdict.Source = sourceProbe
	}
	m.applyVerdict(&verdict)
}

func (m *Manager) applyVerdict(verdict *Verdict) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.verdicts[verdict.Model] = verdict
	if verdict.OK {
		m.validated[verdict.Model] = true
	} else {
		delete(m.validated, verdict.Model)
	}
}

// ---- 勾选与设置 ----

// SetExcluded 勾选/取消一个模型
func (m *Manager) SetExcluded(id string, excluded bool) error {
	found := false
	for _, model := range m.snapshotModels() {
		if model.ID == id {
			found = true
			break
		}
	}
	if !found {
		return errors.New("无效的模型选择")
	}
	m.mu.Lock()
	if excluded {
		m.excluded[id] = true
	} else {
		delete(m.excluded, id)
	}
	m.mu.Unlock()
	return m.saveSettings()
}

// SetSystemProxy 切换是否跟随系统手动代理（切换后重启隔离运行时）
func (m *Manager) SetSystemProxy(enabled bool) error {
	m.mu.Lock()
	if m.probing {
		m.mu.Unlock()
		return errors.New("请等待检测完成")
	}
	m.proxyEnabled, m.proxyExplicit = enabled, true
	m.mu.Unlock()
	if err := m.saveSettings(); err != nil {
		return err
	}
	if m.Enabled() {
		m.Stop()
		m.Start()
	}
	return nil
}

func (m *Manager) loadSettings() {
	raw, err := os.ReadFile(m.cfg.settingsPath())
	if err != nil {
		return
	}
	var s settings
	if json.Unmarshal(raw, &s) != nil {
		return
	}
	if s.UseSystemProxy != nil {
		m.proxyEnabled = *s.UseSystemProxy
		m.proxyExplicit = true
	}
	for _, id := range s.ExcludedModels {
		m.excluded[id] = true
	}
}

func (m *Manager) saveSettings() error {
	m.mu.Lock()
	s := settings{ExcludedModels: make([]string, 0, len(m.excluded))}
	enabled := m.proxyEnabled
	s.UseSystemProxy = &enabled
	for id := range m.excluded {
		s.ExcludedModels = append(s.ExcludedModels, id)
	}
	sort.Strings(s.ExcludedModels)
	m.mu.Unlock()
	payload, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteFileMode(m.cfg.settingsPath(), string(payload)+"\n", 0o600)
}

func (m *Manager) setProbeState(active, pending []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if active != nil {
		m.probeActive = active
	} else {
		m.probeActive = []string{}
	}
	if pending != nil {
		m.probePending = pending
	} else {
		m.probePending = []string{}
	}
}

func (m *Manager) markProbeActive(id string, active bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if active {
		m.probeActive = append(m.probeActive, id)
		return
	}
	out := m.probeActive[:0]
	for _, item := range m.probeActive {
		if item != id {
			out = append(out, item)
		}
	}
	m.probeActive = out
}

func (m *Manager) removePending(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := m.probePending[:0]
	for _, item := range m.probePending {
		if item != id {
			out = append(out, item)
		}
	}
	m.probePending = out
}

// ---- 状态 ----

// Status 面板用的完整状态
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	status := Status{
		Enabled: m.started, Phase: m.phase, Message: m.message,
		ModelCount: len(m.models), Probing: m.probing,
		ProxyEnabled: m.proxyEnabled, LastError: m.lastErr,
		ProbeActive: append([]string{}, m.probeActive...),
		ProbePending: append([]string{}, m.probePending...),
	}
	if m.runtime != nil {
		status.OpenCodeVersion = m.runtime.Version
		status.RuntimePath = m.runtime.Binary
	}
	available := 0
	for _, model := range m.models {
		view := ModelView{Model: model, Excluded: m.excluded[model.ID]}
		if verdict := m.verdicts[model.ID]; verdict != nil {
			copied := *verdict
			view.Verdict = &copied
			view.Available = verdict.OK
			view.Selected = verdict.OK && !m.excluded[model.ID]
		}
		if view.Selected {
			available++
		}
		status.Models = append(status.Models, view)
	}
	status.AvailableCount = available
	if status.Models == nil {
		status.Models = []ModelView{}
	}
	return status
}

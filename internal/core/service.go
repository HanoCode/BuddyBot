package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ScheduleConfig 定时任务配置
type ScheduleConfig struct {
	CheckinHours     []int `json:"checkin_hours"`
	TravelHours      []int `json:"travel_hours"`
	KeepaliveHours   []int `json:"keepalive_hours"`
	CheckinEnabled   bool  `json:"checkin_enabled"`
	TravelEnabled    bool  `json:"travel_enabled"`
	KeepaliveEnabled bool  `json:"keepalive_enabled"`
	// 扩展任务（对齐 workbuddy2api schedule.*_hours/*_enabled）
	ActivityHours   []int `json:"activity_hours"` // 活跃地图（上报活跃事件）
	ActivityEnabled bool  `json:"activity_enabled"`
	SchoolHours     []int `json:"school_hours"` // 开学季活动（仅 CN）
	SchoolEnabled   bool  `json:"school_enabled"`
	CatHours        []int `json:"cat_hours"` // 夜猫子任务（23:00-08:00 窗口，仅 CN）
	CatEnabled      bool  `json:"cat_enabled"`
	// RunBudgetMinutes 单轮任务时间预算（分钟）：超预算后剩余账号直接跳过，
	// 防止成长任务等长流程跑穿占用整个调度窗口（对齐 auto-signin 时间预算）。
	// 0 = 默认 10 分钟；负数 = 不限制。
	RunBudgetMinutes int `json:"run_budget_minutes"`
	// Refresh 定时刷新：按固定间隔自动刷新需要手工刷新的内容
	//（账号积分余额 + 模型目录），把「查余额 / 深度刷新」交给调度器定时做。
	Refresh RefreshConfig `json:"refresh"`
	// Notify 推送通知（对齐 workbuddy2api notify.*）
	Notify NotifyConfig `json:"notify"`
}

// RefreshConfig 定时刷新配置
type RefreshConfig struct {
	Enabled bool `json:"enabled"`
	// IntervalMinutes 刷新间隔（分钟）；0 = 默认 30 分钟
	IntervalMinutes int `json:"interval_minutes"`
}

// DefaultRefreshIntervalMinutes 定时刷新默认间隔（分钟）
const DefaultRefreshIntervalMinutes = 30

// NotifyConfig 任务完成推送通知配置
type NotifyConfig struct {
	Enabled       bool   `json:"enabled"`        // 总开关
	OnlyFailures  bool   `json:"only_failures"`  // 仅在有失败时推送
	PushPlusToken string `json:"pushplus_token"` // PushPlus token（空 = 不用 PushPlus）
	BarkURL       string `json:"bark_url"`       // Bark 地址（形如 https://api.day.app/你的key，空 = 不用 Bark）
	// Desktop 系统级桌面通知（通知中心横幅，覆盖账号掉线 / 错误类事件）。
	// 指针语义：存量配置缺字段视为开启（nil = 开），仅用户显式关闭后落盘为 false。
	Desktop *bool `json:"desktop,omitempty"`
}

// DesktopEnabled 桌面通知是否开启（缺省开启）
func (c NotifyConfig) DesktopEnabled() bool {
	return c.Desktop == nil || *c.Desktop
}

// PoolConfig 账号池策略配置
type PoolConfig struct {
	MaxInFlight      int    `json:"max_in_flight"`
	BreakerThreshold int    `json:"breaker_threshold"`
	BreakerCooldown  string `json:"breaker_cooldown"`
	CooldownMax      string `json:"cooldown_max"`
	// CostExploreInterval 成本探索间隔（形如 30m；空/0 = 关闭）。
	// 已观测到免费成本的账号层每间隔一段时间放行一次未观测账号，搭车探索真实成本。
	CostExploreInterval string `json:"cost_explore_interval"`
}

// SecurityConfig 网关安全设置
type SecurityConfig struct {
	RequireIPAllowlist bool     `json:"require_ip_allowlist"`
	IPWhitelist        []string `json:"ip_whitelist"`
	IPBlacklist        []string `json:"ip_blacklist"` // CIDR 黑名单，命中直接拒绝（优先于白名单）
	ReadOnly           bool     `json:"read_only"`    // 只读模式：管理侧一切变更被拒绝
}

// PromptConfig 系统提示词三模式（对齐 workbuddy2api prompt.mode）
type PromptConfig struct {
	Mode string `json:"mode"` // passthrough 透传 / custom 替换 / append 追加
	Text string `json:"text"` // custom/append 使用的系统提示词
}

// ModelsConfig 模型中心：别名映射 / 积分倍率 / 分组
type ModelsConfig struct {
	Aliases map[string]string  `json:"aliases"` // 客户端可见模型名 → 上游真实模型名
	Rates   map[string]float64 `json:"rates"`   // 积分倍率：每 1k token 基准 1 分 × rate（0 = 免费）
	Groups  map[string]string  `json:"groups"`  // 模型 → 分组名（展示用）
}

// RedisConfig 可选状态镜像（Upstash REST）：会话粘性绑定 + 状态快照
type RedisConfig struct {
	Enabled bool   `json:"enabled"`
	URL     string `json:"url"` // 形如 https://xxx.upstash.io
	Token   string `json:"token"`
}

// SessionConfig 会话粘性配置
type SessionConfig struct {
	Enabled bool   `json:"enabled"`
	TTL     string `json:"ttl"`
}

// Config 网关配置（字段命名对齐 workbuddy2api config.json）
// InjectConfig 官方客户端注入（CDP）设置
type InjectConfig struct {
	Enabled bool `json:"enabled"`
	// 官方客户端可执行文件路径；留空自动探测（/Applications 下 WorkBuddy*.app 等）
	ClientPath string `json:"client_path"`
	// CDP 调试端口（避开常用 9222）
	Port int `json:"port"`
	// 权限弹窗免打扰：自动点击确认类按钮
	DNDAutoConfirm bool `json:"dnd_auto_confirm"`
	// 增强开关（注入面板「设置」Tab 可控制，按弹窗/页面文本启发式匹配）
	AllowFileWrite    bool `json:"allow_file_write"`    // 沙箱外写文件免确认
	AllowCommands     bool `json:"allow_commands"`      // 常用命令行免确认
	AllowBatchDelete  bool `json:"allow_batch_delete"`  // 大批量删除免确认
	AllowSystemTools  bool `json:"allow_system_tools"`  // 系统级工具放行
	AutoResumeSession bool `json:"auto_resume_session"` // 继续异常中断会话
	QuoteMessage      bool `json:"quote_message"`       // 引用消息文本
	MessageNav        bool `json:"message_nav"`         // 会话消息索引（悬浮定位条）
	// 主题（注入面板「主题」Tab，作用于 WorkBuddy 本体换肤；ID 为空=从未设置，不注入）
	Theme ThemeConfig `json:"theme"`
	// 宠物（注入面板悬浮机器人皮肤；"" = 经典 CSS 机器人）
	Pet string `json:"pet"`
}

// ThemeConfig WorkBuddy 换肤状态（对齐 WorkDaddy 主题页的能力面）
type ThemeConfig struct {
	ID         string `json:"id"`          // default/dark/eye-care/cyber-purple/glass
	Wallpaper  string `json:"wallpaper"`   // ""无 / "preset:wallpaper-01" / "custom:<文件名>"
	Mask       int    `json:"mask"`        // 背景蒙版 0-100（壁纸压暗）
	Blur       int    `json:"blur"`        // 背景毛玻璃 0-100（壁纸模糊）
	TextShadow bool   `json:"text_shadow"` // 消息文字阴影（壁纸可读性增强）
}

type Config struct {
	Listen        string         `json:"listen"`
	APIKey        string         `json:"api_key"`
	AuthDir       string         `json:"auth_dir"`
	StateFile     string         `json:"state_file"`
	Schedule      ScheduleConfig `json:"schedule"`
	Pool          PoolConfig     `json:"pool"`
	Security      SecurityConfig `json:"security"`
	SessionSticky SessionConfig  `json:"session_sticky"`
	Prompt        PromptConfig   `json:"prompt"`
	Models        ModelsConfig   `json:"models"`
	Redis         RedisConfig    `json:"redis"`
	KeepAwake     bool           `json:"keep_awake"`
	Inject        InjectConfig   `json:"inject"`
	SkillHub      SkillHubConfig `json:"skillhub"`
}

// SkillHubConfig SkillHub 技能市场配置
type SkillHubConfig struct {
	// CacheTTLMinutes 市场数据（浏览列表 / 更新检查）缓存时长（分钟）。
	// 缓存期内同请求直接复用，减少对 SkillHub API 的调用防封禁；0 = 默认 3 天。
	// 页面「刷新 / 重新检查」始终绕过缓存强制拉取。
	CacheTTLMinutes int `json:"cache_ttl_minutes"`
}

// DefaultSkillHubCacheTTLMinutes 技能市场缓存默认时长：3 天
const DefaultSkillHubCacheTTLMinutes = 3 * 24 * 60

// SkillHubCacheTTL 缓存时长（分钟 → Duration；零值/负值回落默认）
func (s *Service) SkillHubCacheTTL() time.Duration {
	minutes := s.GetConfig().SkillHub.CacheTTLMinutes
	if minutes <= 0 {
		minutes = DefaultSkillHubCacheTTLMinutes
	}
	return time.Duration(minutes) * time.Minute
}

// Service 核心服务：配置管理 + 本地存储 + 真实账号来源 + 网关与调度器运行态
type Service struct {
	mu         sync.RWMutex
	ctx        context.Context
	cancel     context.CancelFunc
	config     *Config
	configPath string
	store      *Store
	gateway    *Gateway
	scheduler  *Scheduler
	tracker    *inflightTracker
	stats      *Stats
	mirror     *redisMirror
	awake      *Awake
	appReady   bool
	flushStop  chan struct{} // 存储合并落盘协程停止信号
}

// NewService 创建服务（默认数据目录为用户配置目录下的 workbuddy-desktop）
func NewService() *Service {
	dir := "data"
	if base, err := os.UserConfigDir(); err == nil {
		dir = filepath.Join(base, "workbuddy-desktop")
	}
	return NewServiceAt(dir)
}

// NewServiceAt 在指定目录创建服务（测试与自定义数据目录用）
func NewServiceAt(dir string) *Service {
	_ = os.MkdirAll(dir, 0o755)

	s := &Service{
		configPath: filepath.Join(dir, "config.json"),
		config:     DefaultConfig(),
		store:      NewStore(filepath.Join(dir, "store.json")),
		tracker:    newInflightTracker(),
		awake:      &Awake{},
	}
	_ = s.LoadConfig()
	s.mirror = newRedisMirror()
	s.mirror.configure(s.GetConfig().Redis)
	s.gateway = NewGateway(s)
	s.stats = NewStats(s.store, s.Accounts)
	s.scheduler = NewScheduler(s)
	s.ApplyAwakeOnStart()
	return s
}

// DefaultConfig 默认配置（首次运行随机生成网关 API Key，不使用固定占位值）
func DefaultConfig() *Config {
	return &Config{
		Listen:    ":7863",
		APIKey:    generateAPIKey(),
		AuthDir:   "./auths",
		StateFile: "./data/state.json",
		Schedule: ScheduleConfig{
			CheckinHours:     []int{9, 21},
			TravelHours:      []int{9, 21},
			KeepaliveHours:   []int{22},
			CheckinEnabled:   true,
			TravelEnabled:    true,
			KeepaliveEnabled: true,
			ActivityHours:    []int{10},
			ActivityEnabled:  true,
			SchoolHours:      []int{12},
			SchoolEnabled:    true,
			CatHours:         []int{1},
			CatEnabled:       true,
			Refresh:          RefreshConfig{Enabled: true, IntervalMinutes: DefaultRefreshIntervalMinutes},
		},
		Pool: PoolConfig{
			MaxInFlight:         3,
			BreakerThreshold:    3,
			BreakerCooldown:     "30m",
			CooldownMax:         "6h",
			CostExploreInterval: "30m",
		},
		Security:      SecurityConfig{RequireIPAllowlist: false, IPWhitelist: []string{}, IPBlacklist: []string{}},
		SessionSticky: SessionConfig{Enabled: true, TTL: "30m"},
		Prompt:        PromptConfig{Mode: "passthrough"},
		Models:        ModelsConfig{Aliases: map[string]string{}, Rates: map[string]float64{}, Groups: map[string]string{}},
		Redis:         RedisConfig{Enabled: false},
		Inject:        InjectConfig{Port: 9223, DNDAutoConfirm: true, Theme: ThemeConfig{Mask: 30, TextShadow: true}},
		SkillHub:      SkillHubConfig{CacheTTLMinutes: DefaultSkillHubCacheTTLMinutes},
	}
}

func generateAPIKey() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "wb-gw-" + NewID("k")
	}
	return "wb-gw-" + hex.EncodeToString(b)
}

// Start 启动网关与调度器
func (s *Service) Start(ctx context.Context) {
	s.mu.Lock()
	if s.cancel != nil {
		s.cancel()
	}
	s.ctx, s.cancel = context.WithCancel(ctx)
	runCtx := s.ctx
	s.mu.Unlock()

	cfg := s.GetConfig()
	if err := s.gateway.Start(cfg.Listen); err != nil {
		EmitEvent(EventGatewayLog, map[string]any{"level": "error", "message": err.Error()})
	}
	s.scheduler.Start(runCtx)
}

// Stop 停止网关与调度器
func (s *Service) Stop() {
	s.mu.Lock()
	if s.cancel != nil {
		s.cancel()
	}
	s.mu.Unlock()
	s.scheduler.Stop()
	_ = s.gateway.Stop()
}

// Restart 重启网关与调度器
func (s *Service) Restart() {
	s.Stop()
	time.Sleep(200 * time.Millisecond)
	s.Start(context.Background())
}

// IsRunning 网关是否运行中
func (s *Service) IsRunning() bool { return s.gateway.IsRunning() }

// StartedAt 启动时间
func (s *Service) StartedAt() time.Time { return s.gateway.StartedAt() }

// Store 本地存储
func (s *Service) Store() *Store { return s.store }

// Stats 统计聚合器
func (s *Service) Stats() *Stats { return s.stats }

// Scheduler 调度器
func (s *Service) Scheduler() *Scheduler { return s.scheduler }

// SetAppReady 标记应用已创建（此后事件可正常投递）
func (s *Service) SetAppReady() { s.appReady = true }

// StartFlusher 启动存储合并落盘协程（应用入口调用；高频变更定时合并写盘）
func (s *Service) StartFlusher() {
	s.flushStop = make(chan struct{})
	go func() {
		for {
			select {
			case <-s.flushStop:
				return
			case <-s.store.dirtyCh:
				// 去抖：短窗口内的连续变更合并成一次写盘
				select {
				case <-s.flushStop:
					return
				case <-time.After(800 * time.Millisecond):
				}
				_ = s.store.Flush()
			}
		}
	}()
}

// Shutdown 应用退出：停网关/调度器与后台协程，并把未落盘数据写盘（务必最终一致）
func (s *Service) Shutdown() {
	s.Stop()
	if s.flushStop != nil {
		select {
		case <-s.flushStop:
		default:
			close(s.flushStop)
		}
	}
	_ = s.store.Flush()
}

// AuthDir 解析后的凭证目录绝对路径
func (s *Service) AuthDir() string { return ResolveAuthDir(s.GetConfig().AuthDir) }

// GatewayBaseURL 本机网关对外基地址（Listen 为 ":7863" 形态时补 127.0.0.1）
func (s *Service) GatewayBaseURL() string {
	listen := s.GetConfig().Listen
	if listen == "" {
		listen = ":7863"
	}
	if strings.Contains(listen, "://") {
		return strings.TrimRight(listen, "/")
	}
	if strings.HasPrefix(listen, ":") {
		listen = "127.0.0.1" + listen
	}
	return "http://" + listen
}

// AgentBackupRoot 智能体接入配置备份根目录（与状态文件同级的 agent-backups/）
func (s *Service) AgentBackupRoot() string {
	return filepath.Join(filepath.Dir(s.store.Path()), "agent-backups")
}

// Accounts 当前账号列表：凭证文件（真实来源）+ 本地运行态合并
func (s *Service) Accounts() []Account {
	return BuildAccounts(LoadCredentials(s.GetConfig().AuthDir), s.store, s.tracker)
}

// FindAccount 按 uid 查找账号
func (s *Service) FindAccount(uid string) (Account, bool) {
	for _, a := range s.Accounts() {
		if a.UID == uid {
			return a, true
		}
	}
	return Account{}, false
}

// ReadOnly 是否处于只读模式（管理侧变更一律拒绝）
func (s *Service) ReadOnly() bool { return s.GetConfig().Security.ReadOnly }

// UpdateInjectConfig 局部更新注入配置（落盘；供 inject 管理器与设置页共用）
func (s *Service) UpdateInjectConfig(mutate func(*InjectConfig)) error {
	cfg := s.GetConfig()
	mutate(&cfg.Inject)
	if err := s.SaveConfigWith(cfg); err != nil {
		return err
	}
	EmitEvent(EventInjectStatus, map[string]any{"kind": "config"})
	return nil
}

// MirrorSetBind 写会话粘性绑定到可选镜像（未启用时 Noop）
func (s *Service) MirrorSetBind(sessionID, uid string) { s.mirror.SetBind(sessionID, uid) }

// MirrorPushState 推送状态快照到可选镜像（未启用时 Noop）
func (s *Service) MirrorPushState() { s.mirror.PushState(s.store.Path()) }

// GetConfig 读取配置
func (s *Service) GetConfig() *Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c := *s.config
	return &c
}

// UpdateConfig 更新配置（先落盘，网关运行中则热重启生效）
func (s *Service) UpdateConfig(c *Config) error {
	s.mu.Lock()
	s.config = c
	s.mu.Unlock()
	if err := s.SaveConfig(); err != nil {
		return err
	}
	s.mirror.configure(c.Redis)
	if s.gateway.IsRunning() {
		s.Restart()
	} else {
		s.scheduler.Stop()
		s.scheduler.Start(context.Background())
	}
	return nil
}

// ConfigPath 配置文件路径（备份用）
func (s *Service) ConfigPath() string { return s.configPath }

// LoadConfig 从磁盘加载配置，文件不存在时写入默认配置。
// 默认值先入再用文件覆盖：文件缺键时保留默认值（对齐 workbuddy2api 的 DefaultSchedule 语义）。
func (s *Service) LoadConfig() error {
	s.mu.RLock()
	path := s.configPath
	s.mu.RUnlock()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s.SaveConfig()
		}
		return err
	}
	cfg := DefaultConfig()
	if err := json.Unmarshal(data, cfg); err != nil {
		// 配置文件损坏：改名保留现场，避免下次启动被默认配置悄悄覆盖
		_ = os.Rename(path, path+".corrupt.bak")
		EmitEvent(EventAppError, map[string]any{"scope": "config", "message": "配置文件损坏，已重置为默认（原文件备份为 config.json.corrupt.bak）"})
		return err
	}
	if cfg.APIKey == "" {
		cfg.APIKey = generateAPIKey()
	}
	if cfg.Listen == "" {
		cfg.Listen = ":7863"
	}
	if cfg.AuthDir == "" {
		cfg.AuthDir = "./auths"
	}
	if len(cfg.Schedule.CheckinHours) == 0 {
		cfg.Schedule.CheckinHours = []int{9, 21}
	}
	if len(cfg.Schedule.TravelHours) == 0 {
		cfg.Schedule.TravelHours = []int{9, 21}
	}
	if len(cfg.Schedule.KeepaliveHours) == 0 {
		cfg.Schedule.KeepaliveHours = []int{22}
	}
	if cfg.Pool.MaxInFlight <= 0 {
		cfg.Pool.MaxInFlight = 3
	}
	if cfg.Pool.BreakerThreshold <= 0 {
		cfg.Pool.BreakerThreshold = 3
	}
	if cfg.Pool.BreakerCooldown == "" {
		cfg.Pool.BreakerCooldown = "30m"
	}
	if cfg.Schedule.ActivityHours == nil {
		cfg.Schedule.ActivityHours = []int{10}
	}
	if cfg.Schedule.SchoolHours == nil {
		cfg.Schedule.SchoolHours = []int{12}
	}
	if cfg.Schedule.CatHours == nil {
		cfg.Schedule.CatHours = []int{1}
	}
	if cfg.Pool.CostExploreInterval == "" {
		cfg.Pool.CostExploreInterval = "30m"
	}
	// 存量配置无 refresh 字段（零值）时补默认，升级后定时刷新开箱即用；
	// 用户显式关闭时 interval_minutes 会保留原值，不会被此处覆盖
	if cfg.Schedule.Refresh.IntervalMinutes == 0 && !cfg.Schedule.Refresh.Enabled {
		cfg.Schedule.Refresh = RefreshConfig{Enabled: true, IntervalMinutes: DefaultRefreshIntervalMinutes}
	}
	if cfg.Prompt.Mode == "" {
		cfg.Prompt.Mode = "passthrough"
	}
	if cfg.Models.Aliases == nil {
		cfg.Models.Aliases = map[string]string{}
	}
	if cfg.Models.Rates == nil {
		cfg.Models.Rates = map[string]float64{}
	}
	if cfg.Models.Groups == nil {
		cfg.Models.Groups = map[string]string{}
	}
	if cfg.Inject.Port <= 0 || cfg.Inject.Port > 65535 {
		cfg.Inject.Port = 9223
	}
	// 存量配置无 skillhub 字段（零值/负值）时回落默认 3 天
	if cfg.SkillHub.CacheTTLMinutes <= 0 {
		cfg.SkillHub.CacheTTLMinutes = DefaultSkillHubCacheTTLMinutes
	}
	s.mu.Lock()
	s.config = cfg
	s.mu.Unlock()
	return nil
}

// SaveConfig 配置落盘（原子替换）
func (s *Service) SaveConfig() error {
	s.mu.RLock()
	cfg, path := s.config, s.configPath
	s.mu.RUnlock()
	return s.saveConfigTo(cfg, path)
}

// SaveConfigWith 用给定配置替换内存态并落盘（不触发网关重启；轻量字段用）
func (s *Service) SaveConfigWith(cfg *Config) error {
	s.mu.Lock()
	s.config = cfg
	s.mu.Unlock()
	return s.saveConfigTo(cfg, s.configPath)
}

// saveConfigTo 原子写盘：tmp + rename
func (s *Service) saveConfigTo(cfg *Config, path string) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Package ferry 把 OpenCode 的免费模型池摆渡进 BuddyBot 网关。
//
// 参考实现为 RuiC-oc-ferry（Node/Electron）。本包是它在 BuddyBot 内的 Go 移植：
// 托管一份**隔离的** OpenCode 运行时，按价格字段筛出免费模型，逐个探测它们能否按
// 客户端（WorkBuddy / Claude Code / …）的工具协议产生动作，把通过的挂到 BuddyBot
// 网关自身的模型清单上；网关收到这些模型的请求时，经「JSON 信封适配器 + 审批围栏」
// 转交给隔离 OpenCode。
//
// 两条不可松动的边界：
//  1. **执行权在客户端手里**。模型只能回一个 JSON 信封来表达动作，任何本机动作
//     一律被拒绝或转交成外部工具调用，模型永远不会自己动手。
//  2. **免费由价格判定，不由名字判定**。输入/输出/缓存读写全为 0 才算免费。
package ferry

import (
	"path/filepath"
	"time"
)

// 默认参数（与参考实现对齐）
const (
	DefaultConcurrency          = 3
	MaxConcurrency              = 6
	DefaultOKTTL                = 6 * time.Hour
	DefaultTransientTTL         = 10 * time.Minute
	DefaultRejectTTL            = time.Hour
	// probeTimeout 单个模型的探测总预算（同一个预算里最多重试一次）。
	// 免费池里以推理模型为主，60 秒会把「慢」误判成「坏」，所以给到 120 秒。
	probeTimeout                = 120 * time.Second
	maxRequestBytes             = 8 << 20
	maxConcurrentRequests       = 4
	permissionPollInterval      = 250 * time.Millisecond
	probeCacheVersion           = 1
	managedRuntimeDirName       = "runtime"
	isolatedRuntimeDirName      = "opencode"
	probeCacheFileName          = "probe-cache.json"
	settingsFileName            = "settings.json"
	logFileName                 = "opencode.log"
	opencodeProviderID          = "opencode"
	adapterAgentName            = "buddybot-ferry"
	chatAgentName               = "buddybot-ferry-chat"
	finderAgentName             = "buddybot-ferry-finder"
	gatewayModelPrefix          = "opencode/"
	isolatedConfigEnv           = "OPENCODE_CONFIG_CONTENT"
	openCodeServerPasswordEnv   = "OPENCODE_SERVER_PASSWORD"
	openCodeServerUsernameEnv   = "OPENCODE_SERVER_USERNAME"
	openCodeDisableUpdateEnv    = "OPENCODE_DISABLE_AUTOUPDATE"
	openCodeDisableProjectEnv   = "OPENCODE_DISABLE_PROJECT_CONFIG"
	openCodeDisableClaudeEnv    = "OPENCODE_DISABLE_CLAUDE_CODE"
	openCodeDisableSkillsEnv    = "OPENCODE_DISABLE_EXTERNAL_SKILLS"
	registryOfficial            = "https://registry.npmjs.org"
	registryMirror              = "https://registry.npmmirror.com"
	runtimeVersionProbeTimeout  = 15 * time.Second
	runtimeDownloadTimeout      = 180 * time.Second
	runtimeMetadataTimeout      = 30 * time.Second
	runtimeModelsRefreshTimeout = 45 * time.Second
	runtimeReadyAttempts        = 120
	runtimeReadyInterval        = 500 * time.Millisecond
	probeCacheVersionField      = "version"
	sourceProbe                 = "probe"
	sourceCache                 = "cache"
	sourceRequest               = "request"
)

// Config 免费额度摆渡的配置（由 core 层注入）
type Config struct {
	// DataDir 数据目录，默认 <BuddyBot 数据目录>/ferry
	DataDir string
	// Concurrency 探测并发度：上游对并发敏感，默认 3，上限 6
	Concurrency int
	// OKTTL 成功判定有效期（默认 6h）
	OKTTL time.Duration
	// TransientTTL 超时/限流这类「当时的负载」判定的有效期（默认 10m）
	TransientTTL time.Duration
	// RejectTTL 已下线/无权限/额度用尽这类「关于这个模型的结论」的有效期（默认 1h）
	RejectTTL time.Duration
	// UseSystemProxy 下载与隔离运行时是否跟随系统手动 HTTP/HTTPS 代理
	UseSystemProxy bool
	// Enabled 是否启用：关闭时不下载、不启动隔离运行时，网关也不暴露免费模型
	Enabled bool
	// Logf 日志回调（默认丢弃）
	Logf func(format string, args ...any)
}

func (c Config) withDefaults() Config {
	if c.DataDir == "" {
		c.DataDir = "ferry"
	}
	if c.Concurrency <= 0 {
		c.Concurrency = DefaultConcurrency
	}
	if c.Concurrency > MaxConcurrency {
		c.Concurrency = MaxConcurrency
	}
	if c.OKTTL <= 0 {
		c.OKTTL = DefaultOKTTL
	}
	if c.TransientTTL <= 0 {
		c.TransientTTL = DefaultTransientTTL
	}
	if c.RejectTTL <= 0 {
		c.RejectTTL = DefaultRejectTTL
	}
	if c.Logf == nil {
		c.Logf = func(string, ...any) {}
	}
	return c
}

func (c Config) runtimeRoot() string    { return filepath.Join(c.DataDir, managedRuntimeDirName) }
func (c Config) isolatedRoot() string   { return filepath.Join(c.DataDir, isolatedRuntimeDirName) }
func (c Config) probeCachePath() string { return filepath.Join(c.DataDir, probeCacheFileName) }
func (c Config) settingsPath() string   { return filepath.Join(c.DataDir, settingsFileName) }
func (c Config) logPath() string        { return filepath.Join(c.DataDir, logFileName) }

// Model 一个免费模型（目录条目）
type Model struct {
	// ID 网关可见 id，形如 opencode/big-pickle
	ID        string                   `json:"id"`
	Name      string                   `json:"name"`
	Context   int64                    `json:"context,omitempty"`
	Input     int64                    `json:"input,omitempty"`
	Output    int64                    `json:"output,omitempty"`
	Images    bool                     `json:"images"`
	Toolcall  bool                     `json:"toolcall"`
	Reasoning bool                     `json:"reasoning"`
	Variants  map[string]VariantOption `json:"variants,omitempty"`

	// chatOnlyFlag 探测结论：只能聊天，工具被关掉。不落目录、只随判定走。
	chatOnlyFlag bool
}

// VariantOption OpenCode 的推理档位变体（variant 名 → 它对应的 reasoningEffort）
type VariantOption struct {
	Disabled        bool   `json:"disabled"`
	ReasoningEffort string `json:"reasoningEffort"`
}

// Verdict 一个模型的判定结果
type Verdict struct {
	Model      string `json:"model"`
	OK         bool   `json:"ok"`
	ChatOnly   bool   `json:"chatOnly,omitempty"`
	Category   string `json:"category"`
	Time       string `json:"time"`
	DurationMs int64  `json:"durationMs,omitempty"`
	Error      string `json:"error,omitempty"`
	Status     int    `json:"status,omitempty"`
	Code       string `json:"code,omitempty"`
	// Source 判定来源：probe（真探测）/ cache（沿用缓存）/ request（真实调用观察）
	Source string `json:"source,omitempty"`
}

// ModelView 面板用的模型视图（目录条目 + 判定 + 勾选）
type ModelView struct {
	Model
	Verdict  *Verdict `json:"verdict,omitempty"`
	Available bool     `json:"available"` // 已判定可用且未被排除
	Excluded  bool     `json:"excluded"`
	Selected  bool     `json:"selected"` // 可用于发布（可用且未排除）
}

// Status 运行状态
type Status struct {
	Enabled         bool        `json:"enabled"`
	Phase           string      `json:"phase"` // disabled|starting|downloading|reading|probing|ready|error
	Message         string      `json:"message"`
	OpenCodeVersion string      `json:"opencodeVersion,omitempty"`
	RuntimePath     string      `json:"runtimePath,omitempty"`
	ModelCount      int         `json:"modelCount"`
	AvailableCount  int         `json:"availableCount"`
	Probing         bool        `json:"probing"`
	ProbePending    []string    `json:"probePending,omitempty"`
	ProbeActive     []string    `json:"probeActive,omitempty"`
	ProxyEnabled    bool        `json:"proxyEnabled"`
	LastError       string      `json:"lastError,omitempty"`
	Models          []ModelView `json:"models"`
}

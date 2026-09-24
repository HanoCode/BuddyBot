package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"workbuddy-desktop/internal/core"
)

// ConfigAPI 配置管理API
type ConfigAPI struct {
	service *core.Service
}

// NewConfigAPI 创建配置API
func NewConfigAPI(service *core.Service) *ConfigAPI {
	return &ConfigAPI{service: service}
}

// GatewayConfig 网关配置（复用 core 模型，字段与 config.json 一致）
type GatewayConfig = core.Config

// Get 获取当前配置
func (c *ConfigAPI) Get(ctx context.Context) (*GatewayConfig, error) {
	return c.service.GetConfig(), nil
}

// Defaults 返回默认配置（含随机生成的根密钥），供「恢复默认」使用。
// 默认值由后端单点定义，前端不再自行拼一份硬编码配置。
func (c *ConfigAPI) Defaults(ctx context.Context) (*GatewayConfig, error) {
	return core.DefaultConfig(), nil
}

// ConfigMeta 配置元信息（路径等，供界面展示）
type ConfigMeta struct {
	ConfigPath string `json:"configPath"`
	DataDir    string `json:"dataDir"`
	AuthDir    string `json:"authDir"`
	StorePath  string `json:"storePath"`
}

// Meta 返回文件落盘位置
func (c *ConfigAPI) Meta(ctx context.Context) (*ConfigMeta, error) {
	return &ConfigMeta{
		ConfigPath: c.service.ConfigPath(),
		DataDir:    filepath.Dir(c.service.ConfigPath()),
		AuthDir:    c.service.AuthDir(),
		StorePath:  c.service.Store().Path(),
	}, nil
}

// Update 校验并更新配置（落盘；网关运行中则热重启生效）
func (c *ConfigAPI) Update(ctx context.Context, config *GatewayConfig) error {
	if err := ensureWritable(c.service); err != nil {
		return err
	}
	if err := validateConfig(config); err != nil {
		return err
	}
	// 根密钥留空表示「不修改」，避免误清空导致客户端全部失联
	if strings.TrimSpace(config.APIKey) == "" {
		config.APIKey = c.service.GetConfig().APIKey
	}
	if err := c.service.UpdateConfig(config); err != nil {
		return err
	}
	audit(c.service, "config.update", "config.json", "网关配置已更新")
	return nil
}

// RotateAPIKey 重新生成根密钥，返回新密钥
func (c *ConfigAPI) RotateAPIKey(ctx context.Context) (string, error) {
	if err := ensureWritable(c.service); err != nil {
		return "", err
	}
	cfg := c.service.GetConfig()
	def := core.DefaultConfig()
	cfg.APIKey = def.APIKey
	if err := c.service.UpdateConfig(cfg); err != nil {
		return "", err
	}
	audit(c.service, "config.rotate_root_key", "config.json", "根密钥已轮换")
	return cfg.APIKey, nil
}

// validateConfig 配置校验：监听地址、小时范围、冷却时长
func validateConfig(cfg *GatewayConfig) error {
	if strings.TrimSpace(cfg.Listen) == "" {
		return fmt.Errorf("监听地址不能为空")
	}
	if _, _, err := net.SplitHostPort(cfg.Listen); err != nil {
		return fmt.Errorf("监听地址格式无效（应形如 :7863 或 127.0.0.1:7863）: %w", err)
	}
	check := func(name string, hours []int) error {
		for _, h := range hours {
			if h < 0 || h > 23 {
				return fmt.Errorf("%s 含非法小时 %d（合法范围 0-23）", name, h)
			}
		}
		return nil
	}
	if err := check("签到时间", cfg.Schedule.CheckinHours); err != nil {
		return err
	}
	if err := check("旅行时间", cfg.Schedule.TravelHours); err != nil {
		return err
	}
	if err := check("保活时间", cfg.Schedule.KeepaliveHours); err != nil {
		return err
	}
	if cfg.Pool.MaxInFlight <= 0 {
		return fmt.Errorf("单账号最大并发必须大于 0")
	}
	if cfg.Pool.BreakerThreshold <= 0 {
		return fmt.Errorf("熔断阈值必须大于 0")
	}
	if _, err := time.ParseDuration(cfg.Pool.BreakerCooldown); err != nil {
		return fmt.Errorf("熔断冷却时长格式无效（形如 30m / 1h）: %w", err)
	}
	for _, ip := range cfg.Security.IPWhitelist {
		entry := strings.TrimSpace(ip)
		if entry == "" {
			continue
		}
		if strings.Contains(entry, "/") {
			if _, _, err := net.ParseCIDR(entry); err != nil {
				return fmt.Errorf("IP 白名单项 %q 不是合法 CIDR", entry)
			}
			continue
		}
		if net.ParseIP(entry) == nil {
			return fmt.Errorf("IP 白名单项 %q 不是合法 IP", entry)
		}
	}
	for _, ip := range cfg.Security.IPBlacklist {
		entry := strings.TrimSpace(ip)
		if entry == "" {
			continue
		}
		if strings.Contains(entry, "/") {
			if _, _, err := net.ParseCIDR(entry); err != nil {
				return fmt.Errorf("IP 黑名单项 %q 不是合法 CIDR", entry)
			}
			continue
		}
		if net.ParseIP(entry) == nil {
			return fmt.Errorf("IP 黑名单项 %q 不是合法 IP", entry)
		}
	}
	switch cfg.Prompt.Mode {
	case "", "passthrough", "custom", "append":
	default:
		return fmt.Errorf("提示词模式无效: %s（合法值 passthrough / custom / append）", cfg.Prompt.Mode)
	}
	if cfg.Pool.CostExploreInterval != "" {
		if _, err := time.ParseDuration(cfg.Pool.CostExploreInterval); err != nil {
			return fmt.Errorf("成本探索间隔格式无效（形如 30m）: %w", err)
		}
	}
	// 0 = 走默认间隔（30 分钟），显式填写时限制在 5-1440 分钟
	if rc := cfg.Schedule.Refresh; rc.Enabled && rc.IntervalMinutes != 0 &&
		(rc.IntervalMinutes < 5 || rc.IntervalMinutes > 1440) {
		return fmt.Errorf("定时刷新间隔须在 5-1440 分钟之间（当前 %d）", rc.IntervalMinutes)
	}
	// 会话自动归档：空闲阈值 1-365 天（0 = 默认 7 天，归一化在加载时做）
	if ad := cfg.Schedule.SessionArchive.IdleDays; ad != 0 && (ad < 1 || ad > 365) {
		return fmt.Errorf("会话归档空闲阈值须在 1-365 天之间（当前 %d）", ad)
	}
	if cfg.Redis.Enabled {
		u := strings.TrimSpace(cfg.Redis.URL)
		if !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") {
			return fmt.Errorf("Redis 镜像地址必须为 Upstash REST URL（https://…）")
		}
		if strings.TrimSpace(cfg.Redis.Token) == "" {
			return fmt.Errorf("Redis 镜像已启用但未填写 Token")
		}
	}
	for model, rate := range cfg.Models.Rates {
		if rate < 0 {
			return fmt.Errorf("模型 %s 的积分倍率不能为负", model)
		}
	}
	for model, p := range cfg.Models.Prices {
		if p.Input < 0 || p.Output < 0 || p.CacheRead < 0 || p.CacheWrite < 0 {
			return fmt.Errorf("模型 %s 的单价不能为负（元 / 百万 token）", model)
		}
	}
	return nil
}

// Backup 备份配置，返回备份文件路径
func (c *ConfigAPI) Backup(ctx context.Context) (string, error) {
	return c.backup()
}

func (c *ConfigAPI) backup() (string, error) {
	cfg := c.service.GetConfig()
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", err
	}
	dir := BackupDir(c.service.ConfigPath())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "config-"+time.Now().Format("20060102-150405")+".json")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// BackupDir 备份目录
func BackupDir(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), "backups")
}

// BackupItem 备份文件条目
type BackupItem struct {
	Path string `json:"path"`
	Name string `json:"name"`
	Size int64  `json:"size"`
	Time string `json:"time"`
}

// ListBackups 列出已有备份（真实文件系统扫描）
func (c *ConfigAPI) ListBackups(ctx context.Context) ([]BackupItem, error) {
	dir := BackupDir(c.service.ConfigPath())
	entries, err := os.ReadDir(dir)
	if err != nil {
		return []BackupItem{}, nil
	}
	out := []BackupItem{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, BackupItem{
			Path: filepath.Join(dir, e.Name()), Name: e.Name(),
			Size: info.Size(), Time: info.ModTime().Format(time.RFC3339),
		})
	}
	return out, nil
}

// Restore 从备份文件恢复配置
func (c *ConfigAPI) Restore(ctx context.Context, path string) error {
	if err := ensureWritable(c.service); err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var cfg core.Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("备份文件格式无效: %w", err)
	}
	if err := validateConfig(&cfg); err != nil {
		return fmt.Errorf("备份内容校验失败: %w", err)
	}
	if err := c.service.UpdateConfig(&cfg); err != nil {
		return err
	}
	audit(c.service, "config.restore", filepath.Base(path), "从备份恢复配置")
	return nil
}

// ConfigExportResult 配置导出结果（含落盘路径）
type ConfigExportResult struct {
	Path        string `json:"path"`        // 用户取消时为空
	Credentials int    `json:"credentials"` // 打包的凭证份数（仅含凭证导出时 >0）
}

// Export 弹出系统保存对话框，把配置写入用户选择的位置。
// includeCredentials 时把登录凭证一并打包成合并包（password 非空则 AES-256-GCM 加密信封）。
func (c *ConfigAPI) Export(ctx context.Context, includeCredentials bool, password string) (*ConfigExportResult, error) {
	bundle := map[string]any{
		"kind":       "workbuddy-config-bundle",
		"exportedAt": time.Now().Format(time.RFC3339),
		"config":     c.service.GetConfig(),
	}
	creds := 0
	if includeCredentials {
		payload, n, err := exportCredentialsPayload(c.service, password)
		if err != nil {
			return nil, err
		}
		bundle["credentials"] = json.RawMessage(payload)
		creds = n
	}
	b, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		return nil, err
	}
	name := "workbuddy-config"
	if includeCredentials {
		name = "workbuddy-config-bundle"
	}
	path, err := saveExportDialog(fmt.Sprintf("%s-%s.json", name, time.Now().Format("20060102-150405")), "JSON 文件", "*.json")
	if err != nil {
		return nil, err
	}
	if path == "" {
		return &ConfigExportResult{}, nil // 用户取消
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return nil, fmt.Errorf("写入文件失败: %w", err)
	}
	revealInFileManager(path)
	if includeCredentials {
		audit(c.service, "config.export", filepath.Base(path), fmt.Sprintf("导出配置与 %d 份凭证", creds))
	} else {
		audit(c.service, "config.export", filepath.Base(path), "导出配置")
	}
	return &ConfigExportResult{Path: path, Credentials: creds}, nil
}

// Import 导入配置 JSON 字符串
func (c *ConfigAPI) Import(ctx context.Context, configJson string) error {
	if err := ensureWritable(c.service); err != nil {
		return err
	}
	var cfg core.Config
	if err := json.Unmarshal([]byte(configJson), &cfg); err != nil {
		return fmt.Errorf("配置格式无效: %w", err)
	}
	if err := validateConfig(&cfg); err != nil {
		return err
	}
	if err := c.service.UpdateConfig(&cfg); err != nil {
		return err
	}
	audit(c.service, "config.import", "config.json", "导入外部配置")
	return nil
}

package api

import (
	"context"

	"workbuddy-desktop/internal/core"
)

// SkillsAPI 技能市场API：SkillHub 技能浏览/搜索 + 安装到本机 AI 编程客户端
type SkillsAPI struct {
	service *core.Service
}

// NewSkillsAPI 创建技能市场API
func NewSkillsAPI(service *core.Service) *SkillsAPI {
	return &SkillsAPI{service: service}
}

// Status 探测 skillhub CLI 安装状态
func (a *SkillsAPI) Status(ctx context.Context) (core.SkillHubCLIStatus, error) {
	return core.DetectSkillHubCLI(), nil
}

// InstallCLI 自动安装 skillhub CLI（官方 install.sh --cli-only），完成后返回新状态
func (a *SkillsAPI) InstallCLI(ctx context.Context) (core.SkillHubCLIStatus, error) {
	return core.InstallSkillHubCLI(ctx)
}

// BrowseParams 浏览参数
type BrowseParams struct {
	Tab   string `json:"tab"`   // score(推荐) / hot(下载量) / trending(近期飙升) / newest(最近上新)
	Query string `json:"query"` // 搜索关键词，可空
	Force bool   `json:"force"` // true = 绕过缓存强制拉取（页面刷新按钮）
}

// Browse 浏览/搜索技能（结果按设置里的缓存时长复用，减少对 SkillHub API 的调用）
func (a *SkillsAPI) Browse(ctx context.Context, params BrowseParams) (*core.SkillHubBrowseResult, error) {
	tab := params.Tab
	if tab == "" {
		tab = "score"
	}
	return core.BrowseSkillHub(ctx, tab, params.Query, params.Force, a.service.SkillHubCacheTTL())
}

// Targets 列出可安装的客户端及其 skills 目录（含已装技能数）
func (a *SkillsAPI) Targets(ctx context.Context) ([]core.SkillTargetInfo, error) {
	return core.ListSkillTargets(), nil
}

// SkillInstallParams 安装参数
type SkillInstallParams struct {
	Slug      string `json:"slug"`
	Namespace string `json:"namespace,omitempty"`
	Target    string `json:"target"` // 客户端 ID（claude-code / codex / …）
}

// Install 安装技能到指定客户端（CLI 未安装时自动先装 CLI）
func (a *SkillsAPI) Install(ctx context.Context, params SkillInstallParams) (*core.SkillInstallResult, error) {
	return core.InstallSkillToAgent(ctx, params.Slug, params.Namespace, params.Target)
}

// Installed 列出某客户端已安装的技能
func (a *SkillsAPI) Installed(ctx context.Context, target string) ([]core.InstalledSkill, error) {
	return core.ListInstalledSkills(target)
}

// Uninstall 卸载某客户端下的技能（前端需二次确认）
func (a *SkillsAPI) Uninstall(ctx context.Context, target, dirName string) error {
	return core.UninstallSkill(target, dirName)
}

// Updates 检查所有客户端中经技能市场安装的技能的可更新情况（远端版本比对，结果整体缓存）
func (a *SkillsAPI) Updates(ctx context.Context, force bool) (*core.SkillUpdatesResult, error) {
	return core.CheckSkillUpdates(ctx, force, a.service.SkillHubCacheTTL())
}

// Update 更新单个技能到最新版（实为 --force 重装，CLI 会同步刷新版本记录）
func (a *SkillsAPI) Update(ctx context.Context, target, key string) (*core.SkillUpdateResult, error) {
	return core.UpdateSkillByKey(ctx, target, key)
}

// UpdateAll 一键更新所有可更新的技能（跨客户端汇总后串行执行）
func (a *SkillsAPI) UpdateAll(ctx context.Context) ([]core.SkillUpdateResult, error) {
	return core.UpdateAllSkills(ctx, a.service.SkillHubCacheTTL())
}

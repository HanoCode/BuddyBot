package core

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// SkillHub 技能市场集成：
// - 浏览/搜索走 api.skillhub.cn 公开搜索 API；榜单走本机 CLI（skillhub skill rankings）
// - 安装统一走 CLI `skillhub install <slug> --dir <目标 skills 目录>`（含签名校验），
//   CLI 未安装时先自动执行官方安装脚本（--cli-only），对用户透明
// - 目标目录映射复用 agents.go 已探测的客户端清单

const (
	skillHubSearchURL        = "https://api.skillhub.cn/api/v1/search"
	skillHubInstallScriptURL = "https://skillhub-1388575217.cos.ap-guangzhou.myqcloud.com/install/install.sh"
)

// SkillHubCLIStatus skillhub CLI 安装状态
type SkillHubCLIStatus struct {
	Installed bool   `json:"installed"`
	Version   string `json:"version,omitempty"`
	Path      string `json:"path,omitempty"`
}

// SkillHubSkill 技能条目（搜索/榜单统一归一化后的形态）
type SkillHubSkill struct {
	Slug           string  `json:"slug"`
	Name           string  `json:"name"`
	Namespace      string  `json:"namespace,omitempty"`
	Description    string  `json:"description"`
	Category       string  `json:"category,omitempty"`
	Version        string  `json:"version,omitempty"`
	Source         string  `json:"source,omitempty"`
	IconURL        string  `json:"iconUrl,omitempty"`
	Downloads      int64   `json:"downloads"`
	Installs       int64   `json:"installs"`
	Stars          int64   `json:"stars"`
	Score          float64 `json:"score"`
	RequiresAPIKey bool    `json:"requiresApiKey"`
	UpdatedAt      int64   `json:"updatedAt"`
}

// SkillHubBrowseResult 浏览结果
type SkillHubBrowseResult struct {
	Tab        string          `json:"tab"`
	Query      string          `json:"query"`
	Skills     []SkillHubSkill `json:"skills"`
	CLIMissing bool            `json:"cliMissing"` // 榜单需要 CLI，缺失时降级为搜索接口
	FetchedAt  int64           `json:"fetchedAt"`  // 数据拉取时间（毫秒时间戳，用于前端展示缓存年龄）
	FromCache  bool            `json:"fromCache"`  // 本次响应是否来自缓存
}

// SkillInstallResult 安装结果
type SkillInstallResult struct {
	Slug      string `json:"slug"`
	Namespace string `json:"namespace,omitempty"`
	Target    string `json:"target"`
	Dir       string `json:"dir"`
	Output    string `json:"output"`
}

// InstalledSkill 已安装技能（扫描目标目录）
type InstalledSkill struct {
	DirName     string `json:"dirName"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Path        string `json:"path"`
}

// SkillTargetInfo 可安装的客户端及其 skills 目录
type SkillTargetInfo struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Dir       string `json:"dir"`
	Installed bool   `json:"installed"` // 客户端本身是否已安装（配置目录存在）
	Count     int    `json:"count"`     // 目录下已装技能数
}

// SkillUpdatesResult 更新检查结果（整体缓存：每次检查对每个技能发一次 API 请求）
type SkillUpdatesResult struct {
	Items     []SkillUpdateItem `json:"items"`
	FetchedAt int64             `json:"fetchedAt"` // 检查时间（毫秒时间戳）
	FromCache bool              `json:"fromCache"`
}

// SkillUpdateItem 可更新的技能（CLI 通过 lockfile 安装的技能才能检测版本）
type SkillUpdateItem struct {
	Key              string `json:"key"`    // lockfile 键，如 @user_865e3592/ui-new
	Target           string `json:"target"` // 客户端 ID
	TargetName       string `json:"targetName"`
	Slug             string `json:"slug"`
	Namespace        string `json:"namespace"` // namespace handle，用于 --namespace 精确安装
	Name             string `json:"name"`
	Description      string `json:"description"`
	InstalledVersion string `json:"installedVersion"`
	LatestVersion    string `json:"latestVersion"`
	HasUpdate        bool   `json:"hasUpdate"`
	Note             string `json:"note,omitempty"` // 无法检测时的说明（未收录 / 查询失败等）
}

// SkillUpdateResult 单个技能的更新执行结果
type SkillUpdateResult struct {
	Key         string `json:"key"`
	Target      string `json:"target"`
	OK          bool   `json:"ok"`
	FromVersion string `json:"fromVersion,omitempty"`
	ToVersion   string `json:"toVersion,omitempty"`
	Message     string `json:"message,omitempty"`
}

// skillsLockEntry lockfile 中单个技能的版本记录
type skillsLockEntry struct {
	Name       string `json:"name"`
	Version    string `json:"version"`
	PublicSlug string `json:"publicSlug"`
	Namespace  struct {
		CanonicalName string `json:"canonicalName"`
		Handle        string `json:"handle"`
	} `json:"namespace"`
	InstallDir string `json:"installDir"`
}

// ---- 在线更新检测（lockfile 本地版本 vs 搜索接口远端版本） ----

// readSkillsLockfile 读取某 skills 目录下 CLI 写入的 .skills_store_lock.json
func readSkillsLockfile(dir string) map[string]skillsLockEntry {
	data, err := os.ReadFile(filepath.Join(dir, ".skills_store_lock.json"))
	if err != nil {
		return nil
	}
	var parsed struct {
		Skills map[string]skillsLockEntry `json:"skills"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil
	}
	return parsed.Skills
}

// latestSkillVersion 查询远端最新版本：按 slug 搜索后在结果中精确匹配 namespace handle，
// 避免同名不同作者的技能混淆
func latestSkillVersion(ctx context.Context, publicSlug, handle string) (string, error) {
	results, err := skillHubSearch(ctx, publicSlug, 20)
	if err != nil {
		return "", err
	}
	for _, r := range results {
		if r.Slug == publicSlug && handle != "" && r.Namespace == handle {
			return r.Version, nil
		}
	}
	// 精确匹配失败时，若 slug 唯一命中也可接受
	var only *SkillHubSkill
	for i := range results {
		if results[i].Slug == publicSlug {
			if only != nil {
				return "", fmt.Errorf("市场存在多个同名技能，无法确定来源")
			}
			only = &results[i]
		}
	}
	if only != nil {
		return only.Version, nil
	}
	return "", fmt.Errorf("未在 SkillHub 市场找到该技能")
}

// CheckSkillUpdates 汇总所有客户端目录中经 CLI 安装的技能，逐个比对远端最新版本。
// 结果整体缓存（TTL 内直接复用；force=true 强制重新检查）。并发查询（池大小 5），
// 已装列表顺序按"有更新在前"排列
func CheckSkillUpdates(ctx context.Context, force bool, ttl time.Duration) (*SkillUpdatesResult, error) {
	if ttl <= 0 {
		ttl = DefaultSkillHubCacheTTLMinutes * time.Minute
	}
	res, fetchedAt, fromCache, err := skillsCacheGet("updates", ttl, force, func() (*SkillUpdatesResult, error) {
		items, err := checkSkillUpdatesFresh(ctx)
		if err != nil {
			return nil, err
		}
		return &SkillUpdatesResult{Items: items}, nil
	})
	if err != nil {
		return nil, err
	}
	out := *res // 浅拷贝，理由同 BrowseSkillHub
	out.FetchedAt = fetchedAt.UnixMilli()
	out.FromCache = fromCache
	return &out, nil
}

func checkSkillUpdatesFresh(ctx context.Context) ([]SkillUpdateItem, error) {
	type job struct {
		target, targetName, dir, key string
		entry                        skillsLockEntry
	}
	var jobs []job
	targetNames := map[string]string{}
	for _, t := range skillTargetRefs() {
		targetNames[t.id] = t.name
		lock := readSkillsLockfile(t.dir)
		for key, e := range lock {
			jobs = append(jobs, job{target: t.id, targetName: t.name, dir: t.dir, key: key, entry: e})
		}
	}
	if len(jobs) == 0 {
		return []SkillUpdateItem{}, nil
	}

	items := make([]SkillUpdateItem, len(jobs))
	sem := make(chan struct{}, 5)
	var wg sync.WaitGroup
	for i, j := range jobs {
		wg.Add(1)
		go func(i int, j job) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			item := SkillUpdateItem{
				Key: j.key, Target: j.target, TargetName: j.targetName,
				Slug: j.entry.PublicSlug, Namespace: j.entry.Namespace.Handle,
				Name: j.entry.Name, InstalledVersion: j.entry.Version,
			}
			if item.Name == "" {
				item.Name = j.entry.PublicSlug
			}
			if item.Description == "" {
				if _, desc := parseSkillFrontmatterAt(j.dir, j.key); desc != "" {
					item.Description = desc
				}
			}
			lookupCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			latest, err := latestSkillVersion(lookupCtx, j.entry.PublicSlug, j.entry.Namespace.Handle)
			cancel()
			if err != nil {
				item.Note = err.Error()
			} else {
				item.LatestVersion = latest
				item.HasUpdate = versionLess(item.InstalledVersion, latest)
			}
			items[i] = item
		}(i, j)
	}
	wg.Wait()

	sort.SliceStable(items, func(i, k int) bool {
		if items[i].HasUpdate != items[k].HasUpdate {
			return items[i].HasUpdate
		}
		return items[i].Key < items[k].Key
	})
	return items, nil
}

// UpdateSkillByKey 把某个经 CLI 安装的技能更新到最新版（实为 --force 重装，CLI 会同步刷新 lockfile）
func UpdateSkillByKey(ctx context.Context, target, key string) (*SkillUpdateResult, error) {
	dir, ok := skillTargetDir(target)
	if !ok {
		return nil, fmt.Errorf("不支持的客户端: %s", target)
	}
	lock := readSkillsLockfile(dir)
	entry, ok := lock[key]
	if !ok {
		return nil, fmt.Errorf("该技能不在 %s 的安装记录中（仅支持更新经技能市场安装的技能）", dir)
	}
	res := &SkillUpdateResult{Key: key, Target: target, FromVersion: entry.Version}
	if _, err := InstallSkillToAgent(ctx, entry.PublicSlug, entry.Namespace.Handle, target); err != nil {
		res.Message = err.Error()
		return res, nil
	}
	// 重读 lockfile 拿安装后的版本号
	if newLock := readSkillsLockfile(dir); newLock != nil {
		if e, ok := newLock[key]; ok {
			res.ToVersion = e.Version
		}
	}
	res.OK = true
	skillsCacheInvalidate("updates") // 已装版本变化，使更新检查缓存失效
	return res, nil
}

// UpdateAllSkills 一键更新：检查所有客户端的可更新技能并串行执行（CLI 写 lockfile 不并发）
func UpdateAllSkills(ctx context.Context, ttl time.Duration) ([]SkillUpdateResult, error) {
	updates, err := CheckSkillUpdates(ctx, false, ttl)
	if err != nil {
		return nil, err
	}
	out := make([]SkillUpdateResult, 0)
	for _, it := range updates.Items {
		if !it.HasUpdate {
			continue
		}
		r, err := UpdateSkillByKey(ctx, it.Target, it.Key)
		if err != nil {
			out = append(out, SkillUpdateResult{Key: it.Key, Target: it.Target, Message: err.Error()})
			continue
		}
		out = append(out, *r)
	}
	return out, nil
}

// versionLess 语义化版本比较：a < b 时返回 true。逐段数值比较，非数字段按字符串比
func versionLess(a, b string) bool {
	a, b = strings.TrimPrefix(strings.TrimSpace(a), "v"), strings.TrimPrefix(strings.TrimSpace(b), "v")
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return false
	}
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		var av, bv string
		if i < len(as) {
			av = as[i]
		}
		if i < len(bs) {
			bv = bs[i]
		}
		if av == bv {
			continue
		}
		an, aerr := strconv.Atoi(av)
		bn, berr := strconv.Atoi(bv)
		if aerr == nil && berr == nil {
			return an < bn
		}
		return av < bv
	}
	return false
}

// parseSkillFrontmatterAt 读取技能目录 SKILL.md 的描述（用于更新列表展示）
func parseSkillFrontmatterAt(root, rel string) (name, desc string) {
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel), "SKILL.md"))
	if err != nil {
		return "", ""
	}
	return parseSkillFrontmatter(string(data))
}

// ---- CLI 探测与自动安装 ----

// skillhubCandidatePaths GUI 启动时 PATH 很短（/usr/bin:/bin:…），显式补常见安装位置
func skillhubCandidatePaths() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return []string{
		filepath.Join(home, ".local", "bin", "skillhub"),
		filepath.Join(home, ".skillhub", "bin", "skillhub"),
		"/usr/local/bin/skillhub",
	}
}

// DetectSkillHubCLI 探测 skillhub CLI（PATH + 常见安装位置）
func DetectSkillHubCLI() SkillHubCLIStatus {
	candidates := []string{"skillhub"}
	candidates = append(candidates, skillhubCandidatePaths()...)
	for _, c := range candidates {
		path, err := exec.LookPath(c)
		if err != nil {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		out, err := exec.CommandContext(ctx, path, "--skip-self-upgrade", "--version").CombinedOutput()
		cancel()
		if err != nil {
			continue // 装了但跑不起来，继续探测下一个候选
		}
		return SkillHubCLIStatus{Installed: true, Version: parseSkillhubVersion(string(out)), Path: path}
	}
	return SkillHubCLIStatus{Installed: false}
}

func parseSkillhubVersion(out string) string {
	for _, field := range strings.Fields(strings.TrimSpace(out)) {
		// 形如 "skillhub 2026.8.5"，取第一个像版本号的字段
		if field != "skillhub" && len(field) > 0 && field[0] >= '0' && field[0] <= '9' {
			return field
		}
	}
	return strings.TrimSpace(out)
}

// InstallSkillHubCLI 自动安装 skillhub CLI：下载官方 install.sh 后以 --cli-only 执行。
// macOS/Linux 走 bash 脚本；Windows 无 bash 环境，提示手动安装。
func InstallSkillHubCLI(ctx context.Context) (SkillHubCLIStatus, error) {
	if runtime.GOOS == "windows" {
		return SkillHubCLIStatus{}, fmt.Errorf("Windows 暂不支持自动安装，请到 skillhub.cn 按文档手动安装")
	}

	// 1. 下载安装脚本
	dlCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(dlCtx, http.MethodGet, skillHubInstallScriptURL, nil)
	if err != nil {
		return SkillHubCLIStatus{}, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return SkillHubCLIStatus{}, fmt.Errorf("下载安装脚本失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return SkillHubCLIStatus{}, fmt.Errorf("下载安装脚本失败: HTTP %d", resp.StatusCode)
	}
	tmp, err := os.CreateTemp("", "skillhub-install-*.sh")
	if err != nil {
		return SkillHubCLIStatus{}, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := io.Copy(tmp, resp.Body); err != nil {
		tmp.Close()
		return SkillHubCLIStatus{}, fmt.Errorf("保存安装脚本失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return SkillHubCLIStatus{}, err
	}
	if err := os.Chmod(tmpName, 0o700); err != nil {
		return SkillHubCLIStatus{}, err
	}

	// 2. 执行 --cli-only 安装（不装默认 Skill，不污染当前目录）
	runCtx, runCancel := context.WithTimeout(ctx, 5*time.Minute)
	defer runCancel()
	cmd := exec.CommandContext(runCtx, "bash", tmpName, "--cli-only")
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return SkillHubCLIStatus{}, fmt.Errorf("安装脚本执行失败: %w\n%s", err, tailOutput(string(out), 800))
	}

	// 3. 复检（脚本可能装到 ~/.local/bin，GUI 的 PATH 里未必有）
	status := DetectSkillHubCLI()
	if !status.Installed {
		return status, fmt.Errorf("安装脚本执行完成但未探测到 CLI，请重启应用后重试\n%s", tailOutput(string(out), 800))
	}
	return status, nil
}

// ---- 市场数据缓存（防 API 封禁：同 key 在 TTL 内直接复用） ----

type skillsCacheEntry struct {
	value     any
	fetchedAt time.Time
}

var (
	skillsCacheMu   sync.Mutex
	skillsCacheData = map[string]skillsCacheEntry{}
)

// skillsCacheGet 通用缓存读取：命中且未过期（或 force=false）直接返回；
// 未命中/过期/强制刷新时执行 fetch 并回写。fetch 失败时回退过期旧值（比直接报错友好）。
// 返回：(数据, 数据拉取时间, 是否来自缓存, 错误)
func skillsCacheGet[T any](key string, ttl time.Duration, force bool, fetch func() (T, error)) (T, time.Time, bool, error) {
	skillsCacheMu.Lock()
	entry, hasOld := skillsCacheData[key]
	skillsCacheMu.Unlock()

	if hasOld && !force && time.Since(entry.fetchedAt) < ttl {
		if v, ok := entry.value.(T); ok {
			return v, entry.fetchedAt, true, nil
		}
	}
	v, err := fetch()
	if err != nil && hasOld {
		if v2, ok := entry.value.(T); ok {
			return v2, entry.fetchedAt, true, nil
		}
	}
	if err != nil {
		var zero T
		return zero, time.Time{}, false, err
	}
	now := time.Now()
	skillsCacheMu.Lock()
	skillsCacheData[key] = skillsCacheEntry{value: v, fetchedAt: now}
	skillsCacheMu.Unlock()
	return v, now, false, nil
}

// skillsCacheInvalidate 使前缀匹配的缓存失效（安装/卸载/更新后使更新检查缓存失效）
func skillsCacheInvalidate(prefix string) {
	skillsCacheMu.Lock()
	for k := range skillsCacheData {
		if strings.HasPrefix(k, prefix) {
			delete(skillsCacheData, k)
		}
	}
	skillsCacheMu.Unlock()
}

// ---- 浏览与搜索 ----

// BrowseSkillHub 浏览技能市场（结果按 tab+query 缓存，TTL 内直接复用；force=true 绕过缓存）：
// - 有关键词 → 搜索 API（服务端不认排序参数，取回后本地排序）
// - 无关键词 → CLI 榜单（hot/trending/newest/recommended 四个榜各 100 条）；CLI 缺失时降级搜索接口
func BrowseSkillHub(ctx context.Context, tab, query string, force bool, ttl time.Duration) (*SkillHubBrowseResult, error) {
	query = strings.TrimSpace(query)
	if ttl <= 0 {
		ttl = DefaultSkillHubCacheTTLMinutes * time.Minute
	}
	res, fetchedAt, fromCache, err := skillsCacheGet("browse:"+tab+":"+query, ttl, force, func() (*SkillHubBrowseResult, error) {
		return browseSkillHubFresh(ctx, tab, query)
	})
	if err != nil {
		return nil, err
	}
	out := *res // 浅拷贝：缓存对象被共享，直接赋值元数据会污染其他调用方的视图
	out.FetchedAt = fetchedAt.UnixMilli()
	out.FromCache = fromCache
	return &out, nil
}

func browseSkillHubFresh(ctx context.Context, tab, query string) (*SkillHubBrowseResult, error) {
	query = strings.TrimSpace(query)
	if query != "" {
		skills, err := skillHubSearch(ctx, query, 40)
		if err != nil {
			return nil, err
		}
		sortSkillHubSkills(skills, tab)
		return &SkillHubBrowseResult{Tab: tab, Query: query, Skills: skills}, nil
	}

	status := DetectSkillHubCLI()
	if status.Installed {
		if skills, err := skillHubRankings(ctx, tab); err == nil {
			return &SkillHubBrowseResult{Tab: tab, Skills: skills}, nil
		}
		// 榜单失败降级为搜索接口，不直接报错
	}
	skills, err := skillHubSearch(ctx, "", 40)
	if err != nil {
		return nil, err
	}
	sortSkillHubSkills(skills, tab)
	return &SkillHubBrowseResult{Tab: tab, Skills: skills, CLIMissing: !status.Installed}, nil
}

// sortSkillHubSkills 榜单键本地排序（score/downloads/newest，其余默认综合分）
func sortSkillHubSkills(skills []SkillHubSkill, tab string) {
	switch tab {
	case "hot":
		sort.SliceStable(skills, func(i, j int) bool { return skills[i].Downloads > skills[j].Downloads })
	case "newest":
		sort.SliceStable(skills, func(i, j int) bool { return skills[i].UpdatedAt > skills[j].UpdatedAt })
	default: // score / trending / recommend 都按综合分
		sort.SliceStable(skills, func(i, j int) bool { return skills[i].Score > skills[j].Score })
	}
}

// skillHubSearch 调用公开搜索接口并归一化字段
func skillHubSearch(ctx context.Context, query string, limit int) ([]SkillHubSkill, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	u := skillHubSearchURL + "?page_size=" + itoa(limit)
	if query != "" {
		u += "&q=" + url.QueryEscape(query)
	}
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	body, err := httpGetBody(req)
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Results []rawSkillHubItem `json:"results"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("搜索接口返回解析失败: %w", err)
	}
	skills := make([]SkillHubSkill, 0, len(parsed.Results))
	for _, r := range parsed.Results {
		skills = append(skills, r.normalize())
	}
	return skills, nil
}

// skillHubRankings 走 CLI 榜单（tab → 榜单 section）
func skillHubRankings(ctx context.Context, tab string) ([]SkillHubSkill, error) {
	status := DetectSkillHubCLI()
	if !status.Installed {
		return nil, fmt.Errorf("skillhub CLI 未安装")
	}
	section := map[string]string{
		"hot": "hot", "trending": "trending", "newest": "newest",
		"score": "recommended", "recommend": "recommended",
	}[tab]
	if section == "" {
		section = "recommended"
	}
	runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(runCtx, status.Path, "--skip-self-upgrade", "skill", "rankings").Output()
	if err != nil {
		return nil, fmt.Errorf("获取榜单失败: %w", err)
	}
	var parsed struct {
		Rankings map[string]struct {
			Section string            `json:"section"`
			Skills  []rawSkillHubItem `json:"skills"`
		} `json:"rankings"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		return nil, fmt.Errorf("榜单返回解析失败: %w", err)
	}
	sec, ok := parsed.Rankings[section]
	if !ok {
		return nil, fmt.Errorf("榜单缺少 section: %s", section)
	}
	skills := make([]SkillHubSkill, 0, len(sec.Skills))
	for _, r := range sec.Skills {
		skills = append(skills, r.normalize())
	}
	return skills, nil
}

// rawSkillHubItem 搜索/榜单原始条目（两处字段命名有差异：icon_url vs iconUrl 等，全部兜住）
type rawSkillHubItem struct {
	Slug          string            `json:"slug"`
	Name          string            `json:"name"`
	DisplayName   string            `json:"displayName"`
	Description   string            `json:"description"`
	DescriptionZh string            `json:"description_zh"`
	Category      string            `json:"category"`
	Version       string            `json:"version"`
	Source        string            `json:"source"`
	IconURLSnake  string            `json:"icon_url"`
	IconURLCamel  string            `json:"iconUrl"`
	Downloads     int64             `json:"downloads"`
	Installs      int64             `json:"installs"`
	Stars         int64             `json:"stars"`
	Score         float64           `json:"score"`
	Labels        map[string]string `json:"labels"`
	UpdatedAt     int64             `json:"updated_at"`
	Namespace     struct {
		CanonicalName string `json:"canonicalName"`
		Handle        string `json:"handle"`
		DisplayName   string `json:"displayName"`
	} `json:"namespace"`
}

func (r rawSkillHubItem) normalize() SkillHubSkill {
	desc := strings.TrimSpace(r.DescriptionZh)
	if desc == "" {
		desc = strings.TrimSpace(r.Description)
	}
	icon := r.IconURLCamel
	if icon == "" {
		icon = r.IconURLSnake
	}
	name := r.DisplayName
	if name == "" {
		name = r.Name
	}
	ns := r.Namespace.Handle
	if ns == "" {
		ns = r.Namespace.DisplayName
	}
	return SkillHubSkill{
		Slug: r.Slug, Name: name, Namespace: ns, Description: desc,
		Category: r.Category, Version: r.Version, Source: r.Source, IconURL: icon,
		Downloads: r.Downloads, Installs: r.Installs, Stars: r.Stars, Score: r.Score,
		RequiresAPIKey: strings.EqualFold(r.Labels["requires_api_key"], "true"),
		UpdatedAt:      r.UpdatedAt,
	}
}

// ---- 安装到 Agent ----

// skillTargetRefs 客户端 → skills 目录映射（目录口径：claude-code/codex/workbuddy 来自
// skillhub.md 官方表；opencode/pi/kimi-code/codebuddy 为按各客户端惯例推断，装错目录
// 客户端不会识别，UI 上已提示）
func skillTargetRefs() []struct{ id, name, dir string } {
	home := agentHome()
	env := func(k, def string) string {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
		return filepath.Join(home, def)
	}
	return []struct{ id, name, dir string }{
		{"claude-code", "Claude Code", env("CLAUDE_CONFIG_DIR", ".claude") + "/skills"},
		{"codex", "Codex CLI", env("CODEX_HOME", ".codex") + "/skills"},
		{"opencode", "OpenCode", filepath.Join(home, ".config", "opencode", "skills")},
		{"pi", "Pi Coding Agent", env("PI_CODING_AGENT_DIR", filepath.Join(".pi", "agent")) + "/skills"},
		{"kimi-code", "Kimi Code", env("KIMI_CODE_HOME", ".kimi-code") + "/skills"},
		{"codebuddy", "CodeBuddy", env("CODEBUDDY_HOME", ".codebuddy") + "/skills"},
		{"workbuddy", "WorkBuddy", env("WORKBUDDY_HOME", ".workbuddy") + "/skills"},
	}
}

// ListSkillTargets 列出可安装目标及已装技能数
func ListSkillTargets() []SkillTargetInfo {
	out := make([]SkillTargetInfo, 0, 7)
	for _, t := range skillTargetRefs() {
		info := SkillTargetInfo{ID: t.id, Name: t.name, Dir: t.dir}
		if _, err := os.ReadDir(t.dir); err == nil {
			info.Installed = true
			info.Count = len(scanSkillDirs(t.dir))
		} else if pathExists(filepath.Dir(t.dir)) {
			// 客户端已装但还没有 skills 目录
			info.Installed = true
		}
		out = append(out, info)
	}
	return out
}

// scanSkillDirs 列出 skills 根目录下的技能相对路径（兼容 @ns/slug 嵌套形态）
func scanSkillDirs(dir string) []string {
	var out []string
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		rel := e.Name()
		if pathExists(filepath.Join(dir, rel, "SKILL.md")) {
			out = append(out, rel)
			continue
		}
		sub, err := os.ReadDir(filepath.Join(dir, rel))
		if err != nil {
			continue
		}
		for _, se := range sub {
			if se.IsDir() && pathExists(filepath.Join(dir, rel, se.Name(), "SKILL.md")) {
				out = append(out, filepath.Join(rel, se.Name()))
			}
		}
	}
	sort.Strings(out)
	return out
}

// InstallSkillToAgent 安装技能到指定客户端的 skills 目录。
// CLI 未安装时先自动安装（对用户透明），再执行 `skillhub install <slug> --dir <dir>`。
func InstallSkillToAgent(ctx context.Context, slug, namespace, target string) (*SkillInstallResult, error) {
	dir, ok := skillTargetDir(target)
	if !ok {
		return nil, fmt.Errorf("不支持的客户端: %s", target)
	}
	status := DetectSkillHubCLI()
	if !status.Installed {
		var err error
		if status, err = InstallSkillHubCLI(ctx); err != nil {
			return nil, fmt.Errorf("skillhub CLI 未安装且自动安装失败: %w", err)
		}
	}

	// --force：目标已存在时覆盖安装（重复点击"安装"= 更新，而不是报错）
	args := []string{"--skip-self-upgrade", "install", slug, "--dir", dir, "--force"}
	if ns := strings.TrimSpace(namespace); ns != "" {
		args = append(args, "--namespace", ns)
	}
	runCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(runCtx, status.Path, args...).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("安装失败: %w\n%s", err, tailOutput(string(out), 800))
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	skillsCacheInvalidate("updates") // 安装会改写 lockfile，使更新检查缓存失效
	return &SkillInstallResult{
		Slug: slug, Namespace: namespace, Target: target, Dir: dir,
		Output: tailOutput(string(out), 500),
	}, nil
}

// ListInstalledSkills 扫描目标客户端已安装的技能。
// 目录布局兼容两种：`<skills>/<slug>/SKILL.md` 与 CLI 的命名空间形态
// `<skills>/@<ns>/<slug>/SKILL.md`；DirName 返回相对 skills 根目录的路径。
func ListInstalledSkills(target string) ([]InstalledSkill, error) {
	dir, ok := skillTargetDir(target)
	if !ok {
		return nil, fmt.Errorf("不支持的客户端: %s", target)
	}
	out := make([]InstalledSkill, 0)
	collect := func(rel string) {
		data, err := os.ReadFile(filepath.Join(dir, rel, "SKILL.md"))
		if err != nil {
			return
		}
		name, desc := parseSkillFrontmatter(string(data))
		if name == "" {
			name = filepath.Base(rel)
		}
		out = append(out, InstalledSkill{DirName: rel, Name: name, Description: desc, Path: filepath.Join(dir, rel)})
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		rel := e.Name()
		if pathExists(filepath.Join(dir, rel, "SKILL.md")) {
			collect(rel)
			continue
		}
		// 命名空间子目录（如 @clawhub_xxx/slug）再下钻一层
		sub, err := os.ReadDir(filepath.Join(dir, rel))
		if err != nil {
			continue
		}
		for _, se := range sub {
			if se.IsDir() {
				collect(filepath.Join(rel, se.Name()))
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DirName < out[j].DirName })
	return out, nil
}

// UninstallSkill 删除目标客户端下的已装技能目录（UI 侧需二次确认）。
// dirName 允许命名空间相对路径（如 @ns/slug），但禁止路径穿越。
func UninstallSkill(target, dirName string) error {
	if dirName == "" || dirName == "." || strings.Contains(dirName, "..") {
		return fmt.Errorf("非法的技能目录名: %q", dirName)
	}
	dir, ok := skillTargetDir(target)
	if !ok {
		return fmt.Errorf("不支持的客户端: %s", target)
	}
	full := filepath.Join(dir, filepath.FromSlash(dirName))
	cleanRoot, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	cleanFull, err := filepath.Abs(full)
	if err != nil {
		return err
	}
	if cleanFull == cleanRoot || !strings.HasPrefix(cleanFull, cleanRoot+string(filepath.Separator)) {
		return fmt.Errorf("非法的技能目录名: %q", dirName)
	}
	if !pathExists(cleanFull) {
		return fmt.Errorf("技能目录不存在: %s", cleanFull)
	}
	if err := os.RemoveAll(cleanFull); err != nil {
		return err
	}
	skillsCacheInvalidate("updates") // 卸载会改写 lockfile，使更新检查缓存失效
	return nil
}

func skillTargetDir(target string) (string, bool) {
	for _, t := range skillTargetRefs() {
		if t.id == target {
			return t.dir, true
		}
	}
	return "", false
}

// parseSkillFrontmatter 解析 SKILL.md 头部 YAML frontmatter 的 name / description
func parseSkillFrontmatter(text string) (name, desc string) {
	lines := strings.Split(text, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return "", ""
	}
	for _, line := range lines[1:] {
		trimmed := strings.TrimSpace(line)
		if trimmed == "---" {
			break
		}
		k, v, ok := strings.Cut(trimmed, ":")
		if !ok {
			continue
		}
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		switch strings.TrimSpace(k) {
		case "name":
			if name == "" {
				name = v
			}
		case "description":
			if desc == "" {
				desc = v
			}
		}
	}
	return name, desc
}

// ---- 小工具 ----

func httpGetBody(req *http.Request) ([]byte, error) {
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
}

func tailOutput(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}

func itoa(n int) string {
	return fmt.Sprintf("%d", n)
}

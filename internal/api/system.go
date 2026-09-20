package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"workbuddy-desktop/internal/core"
)

// processStart 进程启动时刻（真实运行时长基准）
var processStart = time.Now()

// appVersion 应用版本。发布构建时通过 ldflags 注入：
//
//	go build -ldflags "-X workbuddy-desktop/internal/api.appVersion=1.1.0"
var appVersion = "1.0.0"

// updateRepo GitHub 更新通道（owner/repo）。默认指向正式发布仓库；
// 构建期仍可用 ldflags 覆盖（如指向测试仓库）。为空表示未配置更新通道
// （CheckUpdate 如实返回不支持，不假装检测）。
//
//	go build -ldflags "-X workbuddy-desktop/internal/api.updateRepo=owner/repo"
var updateRepo = "HanoCode/BuddyBot"

const updateCheckTimeout = 15 * time.Second

// SystemAPI 系统信息API
type SystemAPI struct {
	service *core.Service
}

// NewSystemAPI 创建系统API
func NewSystemAPI(service *core.Service) *SystemAPI {
	s := &SystemAPI{service: service}
	s.startUpdateScheduler()
	return s
}

// SystemInfo 系统信息（全部为真实运行时采样值）
type SystemInfo struct {
	Version       string  `json:"version"`
	GoVersion     string  `json:"goVersion"`
	Platform      string  `json:"platform"`
	Arch          string  `json:"arch"`
	NumCPU        int     `json:"numCpu"`
	ProcessUptime string  `json:"processUptime"` // 进程运行时长
	GatewayUptime string  `json:"gatewayUptime"` // 网关运行时长（未运行则为空）
	MemoryAlloc   float64 `json:"memoryAlloc"`   // 当前堆分配 MB
	MemorySys     float64 `json:"memorySys"`     // 向 OS 申请的总内存 MB
	HeapObjects   uint64  `json:"heapObjects"`
	GCCount       uint32  `json:"gcCount"`
	Goroutines    int     `json:"goroutines"`
}

// GetInfo 获取系统信息（runtime 真实采样）
func (s *SystemAPI) GetInfo(ctx context.Context) (*SystemInfo, error) {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	gatewayUptime := ""
	if s.service.IsRunning() {
		gatewayUptime = time.Since(s.service.StartedAt()).Round(time.Second).String()
	}
	return &SystemInfo{
		Version:       appVersion,
		GoVersion:     runtime.Version(),
		Platform:      runtime.GOOS,
		Arch:          runtime.GOARCH,
		NumCPU:        runtime.NumCPU(),
		ProcessUptime: time.Since(processStart).Round(time.Second).String(),
		GatewayUptime: gatewayUptime,
		MemoryAlloc:   round1(float64(ms.Alloc) / 1024 / 1024),
		MemorySys:     round1(float64(ms.Sys) / 1024 / 1024),
		HeapObjects:   ms.HeapObjects,
		GCCount:       ms.NumGC,
		Goroutines:    runtime.NumGoroutine(),
	}, nil
}

// UpdateInfo 更新信息
type UpdateInfo struct {
	Supported    bool   `json:"supported"`
	Note         string `json:"note"`
	Current      string `json:"current,omitempty"`
	Latest       string `json:"latest,omitempty"`
	HasUpdate    bool   `json:"hasUpdate,omitempty"`
	DownloadURL  string `json:"downloadUrl,omitempty"`  // 与当前平台匹配的安装包直链
	ReleasePage  string `json:"releasePage,omitempty"`  // Release 页面（手动下载入口）
	ReleaseNotes string `json:"releaseNotes,omitempty"` // Release 说明（原文截断）
	AssetName    string `json:"assetName,omitempty"`    // 匹配到的安装包文件名
	AutoInstall  bool   `json:"autoInstall,omitempty"`  // 该安装包是否支持应用内自动安装
}

// ghRelease GitHub Releases API 响应（只解析网关关心的字段）
type ghRelease struct {
	TagName string    `json:"tag_name"`
	Name    string    `json:"name"`
	Body    string    `json:"body"`
	HTMLURL string    `json:"html_url"`
	Assets  []ghAsset `json:"assets"`
}

type ghAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
}

// CheckUpdate 检查更新：查询 GitHub Releases 最新发布，按当前平台挑选安装包。
//
// 更新通道由构建期 ldflags 注入（updateRepo）；未配置时如实返回「不支持」，
// 不返回看起来像真的版本号。这是检测 + 手动更新：下载安装包到本地缓存目录，
// 由用户执行安装——桌面端静默替换自身进程需要平台专属机制，不在本步范围内。
func (s *SystemAPI) CheckUpdate(ctx context.Context) (*UpdateInfo, error) {
	out := &UpdateInfo{Supported: false, Current: appVersion}
	if strings.TrimSpace(updateRepo) == "" {
		out.Note = "当前构建未配置更新通道（ldflags updateRepo）：如需升级请重新拉取源码构建，或安装发行版安装包。"
		return out, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://api.github.com/repos/"+updateRepo+"/releases/latest", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	client := &http.Client{Timeout: updateCheckTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("连接更新服务器失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		out.Supported = true
		out.Note = "更新通道已配置，但仓库还没有任何正式发布。"
		return out, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("更新服务器返回 %d", resp.StatusCode)
	}
	var rel ghRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&rel); err != nil {
		return nil, fmt.Errorf("解析发布信息失败: %w", err)
	}

	latest := strings.TrimPrefix(strings.TrimSpace(rel.TagName), "v")
	asset := pickUpdateAsset(rel.Assets, runtime.GOOS, runtime.GOARCH)
	out.Supported = true
	out.Latest = latest
	out.ReleasePage = rel.HTMLURL
	if notes := strings.TrimSpace(rel.Body); notes != "" {
		if rs := []rune(notes); len(rs) > 300 {
			notes = string(rs[:300]) + "…"
		}
		out.ReleaseNotes = notes
	}
	out.HasUpdate = compareVersion(latest, appVersion) > 0
	if asset != nil {
		out.DownloadURL = asset.BrowserDownloadURL
		out.AssetName = asset.Name
		out.AutoInstall = autoInstallable(asset.Name, runtime.GOOS)
	}
	switch {
	case !out.HasUpdate:
		out.Note = "已是最新版本（" + appVersion + "）"
	case out.DownloadURL == "":
		out.Note = "发现新版本 " + latest + "，但发布物中没有匹配 " + runtime.GOOS + "/" + runtime.GOARCH + " 的安装包，请到发布页手动下载。"
	case out.AutoInstall:
		out.Note = "发现新版本 " + latest + "，可直接下载并自动安装。"
	default:
		out.Note = "发现新版本 " + latest + "，请到发布页手动下载安装。"
	}
	return out, nil
}

// DownloadUpdate 下载指定安装包到本地更新缓存目录，返回落盘绝对路径。
// 仅允许下载 CheckUpdate 返回的 GitHub Release 直链（防 SSRF / 任意文件写）；
// 下载过程推送 update:progress 进度事件，完成后按 Release 内 checksums.txt 做 sha256 校验。
func (s *SystemAPI) DownloadUpdate(ctx context.Context, downloadURL string) (string, error) {
	if strings.TrimSpace(updateRepo) == "" {
		return "", fmt.Errorf("当前构建未配置更新通道，无法下载更新")
	}
	u, err := url.Parse(downloadURL)
	if err != nil || u.Host != "github.com" ||
		!strings.HasPrefix(u.Path, "/"+updateRepo+"/releases/download/") {
		return "", fmt.Errorf("下载地址不在更新通道内，已拒绝")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return "", err
	}
	client := &http.Client{Timeout: 0} // 安装包可能很大，不设总时长
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("下载失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("下载服务器返回 %d", resp.StatusCode)
	}

	dir, err := updateCacheDir()
	if err != nil {
		return "", err
	}
	name := filepath.Base(u.Path)
	if name == "" || name == "." || name == "/" {
		return "", fmt.Errorf("下载地址缺少文件名")
	}
	dst := filepath.Join(dir, name)
	f, err := os.Create(dst)
	if err != nil {
		return "", err
	}
	defer f.Close()

	// 带进度写盘：按累计字节节流推送 update:progress 事件（≥1% 或 ≥2MB）
	total := resp.ContentLength
	var written int64
	lastEmit, lastBytes := int64(-1), int64(-1)
	hash := sha256.New()
	buf := make([]byte, 256<<10)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			written += int64(n)
			if _, werr := f.Write(buf[:n]); werr != nil {
				_ = os.Remove(dst)
				return "", fmt.Errorf("写盘失败: %w", werr)
			}
			_, _ = hash.Write(buf[:n])
			if total > 0 {
				pct := written * 100 / total
				if pct != lastEmit && (written-lastBytes >= 2<<20 || pct == 100) {
					lastEmit, lastBytes = pct, written
					core.EmitEvent(core.EventUpdateProgress, map[string]any{
						"stage": "downloading", "downloaded": written, "total": total, "percent": pct,
					})
				}
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			_ = os.Remove(dst)
			return "", fmt.Errorf("下载中断: %w", rerr)
		}
	}
	core.EmitEvent(core.EventUpdateProgress, map[string]any{
		"stage": "verifying", "downloaded": written, "total": total, "percent": 100,
	})

	// sha256 校验：与 Release 内 checksums.txt 比对；旧版本发布没有该文件时跳过
	if expect, ok := fetchChecksum(u.Path); ok {
		if !strings.EqualFold(expect, hex.EncodeToString(hash.Sum(nil))) {
			_ = os.Remove(dst)
			return "", fmt.Errorf("校验失败：安装包 sha256 与发布清单不符")
		}
	}
	core.EmitEvent(core.EventUpdateProgress, map[string]any{
		"stage": "done", "downloaded": written, "total": total, "percent": 100,
	})
	return dst, nil
}

// fetchChecksum 从同通道下载 checksums.txt 并返回 filename 对应的 sha256。
// checksums.txt 与安装包同目录：/releases/download/<tag>/checksums.txt。
func fetchChecksum(assetPath string) (string, bool) {
	base := strings.TrimSuffix(assetPath, "/"+filepath.Base(assetPath))
	sumURL := "https://github.com/" + updateRepo + base + "/checksums.txt"
	req, err := http.NewRequest(http.MethodGet, sumURL, nil)
	if err != nil {
		return "", false
	}
	resp, err := (&http.Client{Timeout: updateCheckTimeout}).Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		return "", false
	}
	defer resp.Body.Close()
	baseName := filepath.Base(assetPath)
	for _, line := range strings.Split(readAllString(resp.Body), "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		// 兼容 "sha256sum  file" 与 "<hash>  file" 两种格式
		if len(fields) >= 2 && strings.EqualFold(fields[len(fields)-1], baseName) {
			h := fields[0]
			if len(fields) >= 3 && (strings.EqualFold(h, "sha256") || strings.HasPrefix(strings.ToLower(h), "sha256")) {
				h = fields[1]
			}
			return h, true
		}
	}
	return "", false
}

func readAllString(r io.Reader) string {
	b, _ := io.ReadAll(io.LimitReader(r, 1<<20))
	return string(b)
}

// SendTestNotify 发送桌面测试通知（macOS 首次调用会触发系统授权弹窗）。
// 未打包（go run / 裸二进制）时如实返回不可用，不假装成功。
func (s *SystemAPI) SendTestNotify(ctx context.Context) error {
	return core.SendDesktopTestNotify()
}

// SessionArchiveNow 立即执行一轮会话自动归档，返回本次归档数量
//（设置页「立即归档」按钮入口；不受定时开关限制）
func (s *SystemAPI) SessionArchiveNow(ctx context.Context) (int64, error) {
	return s.service.Scheduler().RunSessionArchiveNow()
}

// pickUpdateAsset 按 GOOS/GOARCH 挑选发布物里的安装包。
// 优先可自动安装的产物（darwin: .app zip；windows: 便携 exe），其次手动安装的（dmg / NSIS 安装器）。
func pickUpdateAsset(assets []ghAsset, goos, goarch string) *ghAsset {
	// 每平台按优先级排列的 (扩展名, 是否排除安装器) 组合
	type want struct{ ext string; noInstaller bool }
	var prefs []want
	switch goos {
	case "darwin":
		prefs = []want{{".zip", false}, {".dmg", false}, {".pkg", false}}
	case "windows":
		prefs = []want{{".exe", true}, {".exe", false}, {".msi", false}}
	case "linux":
		prefs = []want{{".appimage", false}, {".deb", false}}
	default:
		return nil
	}
	match := func(a *ghAsset, w want, requireArch bool) bool {
		lower := strings.ToLower(a.Name)
		if !strings.HasSuffix(lower, w.ext) {
			return false
		}
		// darwin 的 .zip 仅认 app 包 zip（名字含 app/darwin/macos），避免误匹配源码 zip
		if goos == "darwin" && w.ext == ".zip" &&
			!strings.Contains(lower, "app") && !strings.Contains(lower, "darwin") && !strings.Contains(lower, "macos") {
			return false
		}
		if w.noInstaller && strings.Contains(lower, "installer") {
			return false
		}
		return !requireArch || strings.Contains(lower, strings.ToLower(goarch))
	}
	// 先按优先级 × 精确架构匹配，再放宽架构
	for _, requireArch := range []bool{true, false} {
		for _, w := range prefs {
			for i := range assets {
				if match(&assets[i], w, requireArch) {
					return &assets[i]
				}
			}
		}
	}
	return nil
}

// autoInstallable 判断该安装包能否被应用内自动安装：
// darwin 需要 .app 的 zip 包（解压替换）；windows 需要便携 exe（rename 自替换）。
// dmg / NSIS 安装器需要挂载或提权，只引导用户手动安装。
func autoInstallable(assetName, goos string) bool {
	lower := strings.ToLower(assetName)
	switch goos {
	case "darwin":
		return strings.HasSuffix(lower, ".zip")
	case "windows":
		return strings.HasSuffix(lower, ".exe") && !strings.Contains(lower, "installer")
	default:
		return false
	}
}

// compareVersion 比较 a.b.c 形式版本号：a>b 返回 1，a<b 返回 -1，相等返回 0。
// 非数字段按 0 处理，长度不齐时缺段补 0。
func compareVersion(a, b string) int {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	n := len(pa)
	if len(pb) > n {
		n = len(pb)
	}
	for i := 0; i < n; i++ {
		x, y := verSeg(pa, i), verSeg(pb, i)
		switch {
		case x > y:
			return 1
		case x < y:
			return -1
		}
	}
	return 0
}

func verSeg(parts []string, i int) int {
	if i >= len(parts) {
		return 0
	}
	v, err := strconv.Atoi(strings.TrimSpace(strings.TrimSuffix(parts[i], "-beta")))
	if err != nil {
		return 0
	}
	return v
}

func round1(v float64) float64 { return float64(int64(v*10+0.5)) / 10 }

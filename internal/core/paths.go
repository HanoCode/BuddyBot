package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// 应用数据目录约定：默认 ~/.buddybot（对齐 ~/.claude、~/.workbuddy 这类开发者工具约定），
// 可用 BUDDYBOT_HOME 覆盖（测试、多实例并存用）。
//
// 所有应用私有目录（配置/存储/凭证/日志导出/更新缓存/单实例锁）都必须从 AppDir() 派生。
// 打包成 App 后双击启动时进程 CWD 是只读的 /，任何按 CWD 解析的相对路径都会写失败
// （见 mkdir /auths: read-only file system）。

const (
	appDirName = ".buddybot"
	// legacyAppDirName v1.0.5 及以前的数据目录名：<UserConfigDir>/workbuddy-desktop
	// （macOS: ~/Library/Application Support，Windows: %AppData%）
	legacyAppDirName = "workbuddy-desktop"
)

// AppDir 应用数据目录（绝对路径）。
func AppDir() string {
	if v := strings.TrimSpace(os.Getenv("BUDDYBOT_HOME")); v != "" {
		return v
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, appDirName)
	}
	// 极端兜底：拿不到家目录时退到系统配置目录（仍是绝对路径，不要用 CWD 相对路径）
	if base, err := os.UserConfigDir(); err == nil && base != "" {
		return filepath.Join(base, appDirName)
	}
	return appDirName
}

// legacyAppDirs 可能存放旧版本数据的目录（按优先级）
func legacyAppDirs() []string {
	base, err := os.UserConfigDir()
	if err != nil || base == "" {
		return nil
	}
	return []string{filepath.Join(base, legacyAppDirName)}
}

// EnsureAppDir 返回应用数据目录，并保证旧版本目录已迁移（幂等）。
// 启动早期由 main 的单实例保护与 NewService 共同调用，两者必须拿到同一目录。
func EnsureAppDir() string {
	dir := AppDir()
	migrateLegacyAppDir(dir, legacyAppDirs())
	return dir
}

// migrateLegacyAppDir 旧数据目录 → 新目录的一次性迁移。
//
// 触发条件：新目录还没有应用数据，而某个旧目录有数据。同卷直接 rename（原子、瞬间完成），
// 跨卷（或目标目录已非空）退回目录树复制合并。迁移失败不阻断启动，旧目录原样保留，
// 用户数据不会被删除——宁可让用户手动处理，也不静默丢配置与凭证。
func migrateLegacyAppDir(dir string, legacyDirs []string) {
	if dir == "" || hasAppData(dir) {
		return
	}
	for _, legacy := range legacyDirs {
		if legacy == "" || legacy == dir || !hasAppData(legacy) {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
			return
		}
		if err := os.Rename(legacy, dir); err != nil {
			if cerr := copyDir(legacy, dir); cerr != nil {
				return
			}
		}
		rewriteLegacyAuthDir(dir, legacy)
		return
	}
}

// hasAppData 目录里是否已有应用数据。用配置/存储文件判定，避免把只落了单实例锁的
// 空目录误判为"已迁移"。
func hasAppData(dir string) bool {
	for _, name := range []string{"config.json", "store.json"} {
		if st, err := os.Stat(filepath.Join(dir, name)); err == nil && !st.IsDir() {
			return true
		}
	}
	return false
}

// rewriteLegacyAuthDir 迁移后修正 config.json 里指向旧目录的绝对 auth_dir：改回相对值 "auths"，
// 由配置加载时的 normalizePaths 重新锚定到新数据目录（否则仍会去写旧目录，登录会失败）。
func rewriteLegacyAuthDir(dir, legacy string) {
	path := filepath.Join(dir, "config.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(data, &raw) != nil {
		return
	}
	var authDir string
	if json.Unmarshal(raw["auth_dir"], &authDir) != nil {
		return
	}
	if !filepath.IsAbs(authDir) || !isUnder(authDir, legacy) {
		return
	}
	rel, err := json.Marshal("auths")
	if err != nil {
		return
	}
	raw["auth_dir"] = rel
	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(path, out, 0o600)
}

// isUnder 判断 path 是否位于 parent 之下
func isUnder(path, parent string) bool {
	rel, err := filepath.Rel(parent, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

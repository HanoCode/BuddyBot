# BuddyBot 在线更新（Auto Update）— 实现文档

> 更新通道：GitHub Releases（`HanoCode/BuddyBot`）。Gitee 方案已评估、暂不启用（决策记录见 §9）。
> 本文档描述当前代码的真实实现，代码位置见各节标注。

## 1. 架构总览

```
发布（push tag v*）
  GitHub Actions 矩阵构建（macos-14/arm64、macos-13/amd64、windows-2022/amd64）
  → wails3 打包 → 产物规范命名 → upload artifact
  → release job 汇总 + 生成 checksums.txt（sha256）
  → gh release create（含 checksums.txt）

运行时（internal/api）
  CheckUpdate    → GitHub Releases API（releases/latest），按 GOOS/GOARCH 匹配产物
  DownloadUpdate → 缓存目录下载，update:progress 进度事件，checksums.txt sha256 强校验
  InstallUpdate  → macOS: ditto 解压 + sh helper 退出后替换 .app 并 relaunch
                 → Windows: 便携 exe rename 自替换 + 延迟重启 / NSIS 静默重装
  前端（Settings → 关于）→ 检查 / 进度条 / 下载并自动安装 / 发布页兜底
```

## 2. 版本号管理

- 唯一来源 = git tag。CI 传 `APP_VERSION=<tag 去掉 v>` → 平台 Taskfile 拼进
  `-X workbuddy-desktop/internal/api.appVersion=<ver>`（`build/darwin/Taskfile.yml`、`build/windows/Taskfile.yml`）。
- 代码默认 `1.0.0`；`updateRepo` 默认 `HanoCode/BuddyBot`，均可被 ldflags 覆盖（如指向测试仓库）。
- 版本比较用自研 `compareVersion`（a.b.c 数值逐段比较，缺段补 0）。

## 3. 产物命名与匹配（关键约定）

CI 产物名必须符合 `pickUpdateAsset`（internal/api/system.go）的匹配规则：

| 产物 | 用途 | 自动安装 |
|---|---|---|
| `BuddyBot-darwin-<arch>.zip` | .app 包 zip | 是（autoInstallable） |
| `BuddyBot-windows-amd64.exe` | 便携 exe | 是（文件名不含 "installer"） |
| `BuddyBot-windows-*-installer.exe` | NSIS 安装器 | 否（手动/UAC） |
| `*.dmg` / `*.pkg` / `*.msi` | 手动安装 | 否 |
| `checksums.txt` | sha256 清单 | — |

优先级：精确架构匹配 > 无架构匹配；Windows 优先非 installer 的 exe；darwin 的 zip 需名字含 app/darwin/macos（防误配源码 zip）。

## 4. 下载与校验（internal/api/system.go）

- `DownloadUpdate(url)`：仅放行 `https://github.com/<updateRepo>/releases/download/...` 直链（防 SSRF）；
  落盘到 `<UserCacheDir>/workbuddy-desktop/updates/`。
- 进度：按累计字节节流（≥1% 且 ≥2MB）emit `update:progress`
  `{stage: downloading|verifying|installing|done, downloaded, total, percent}`（事件常量在 internal/core/events.go）。
- 校验：下载完成后拉取同 Release 的 `checksums.txt`（`releases/latest/download/checksums.txt`），
  比对 sha256，不符即删除重下；旧发布没有该文件时跳过校验。

### 4.1 定期检查（internal/api/update_schedule.go）

- 启动 1 分钟后首次检查，之后**每 6 小时**一次（协程随 NewSystemAPI 启动，随进程退出）。
- 命中新版本时 emit `update:available`（负载为 UpdateInfo）：
  - 前端全局（App.tsx）：toast 提醒一次（同一版本去重，避免每 6h 轰炸）；
  - 设置 → 关于：更新信息自动刷新，「检查更新」按钮替换为高亮「立即更新」（实色主色 + 呼吸光晕 `.btn-update`）。
- 手动「检查更新」保留：无新版时仍是常规软色按钮。

## 5. 安装与重启（internal/api/update_install*.go）

- `InstallUpdate(filePath)`：仅接受更新缓存目录内的文件（防任意路径执行）。
- **macOS**（update_install_darwin.go）：
  1. `ditto -x -k` 解压 zip 到临时目录，`findAppBundle` 定位新 .app；
  2. 写入 sh helper（路径经环境变量传递，避免转义问题）并 detached 启动；
  3. 主进程 `app.Quit()`；helper：备份旧 bundle → `ditto` 新 bundle（保留权限/签名）→ `open` 重启 → 清理备份。
  - 开发模式（可执行文件不在 .app 内）如实拒绝，提示手动安装。
- **Windows**（update_install_windows.go）：
  - 便携 exe：复制到运行目录 → 当前 exe 改名 `.old.exe` → 新 exe 顶替原路径 →
    `cmd /C timeout 2 & start 新exe` 延迟重启 → 应用退出；启动时 init() 清理残留 `.old.exe` / `BuddyBot.new.exe`。
  - NSIS 安装器：`installer.exe /S` 静默重装（安装器自提权 UAC），应用退出。
- 非 macOS/Windows 平台：stub 返回"不支持自动安装"。

## 6. 前端（frontend/src/pages/Settings.tsx → 关于分区）

- `systemApi.checkUpdate / downloadUpdate / installUpdate`（services/api.ts，bindings 由 wails3 自动生成）。
- UI 交互：检查更新 → 发现新版且有 `autoInstall` 时按钮变为「下载并自动安装」；
  下载期间订阅 `update:progress` 渲染进度条（下载中 % / 校验中 / 安装中）；
  不支持自动安装的产物保留「下载安装包」+「发布页」兜底。
- 更新包下载校验完成后调用 `installUpdate`，应用自动退出并重启。

## 7. CI（.github/workflows/release.yml）

- 触发：push tag `v*`；权限 `contents: write`。
- 矩阵构建后由 release job 下载全部 artifact → `sha256sum` 生成 `checksums.txt` → `gh release create --generate-notes`。
- macOS 构建依赖 runner 自带 Xcode；Windows 纯 Go 交叉构建（CGO_ENABLED=0，无需 NSIS）。
- 尚未做代码签名/公证：macOS 未签名 app 首次安装仍需右键打开；接入 build/darwin 既有 sign/notarize 任务即可补齐。

## 8. 已知取舍

- 未做差量更新：单包体积不大，全量下载简单可靠。
- 自动更新不覆盖手动安装形态（dmg/pkg/NSIS），这些产物走「发布页」兜底。
- GitHub API 匿名限流 60 次/h/IP：检查更新由用户手动触发，正常使用远低于限额。

## 9. 决策记录：Gitee 暂不启用

曾评估双源方案（Gitee raw manifest + Releases API 双通道、国内优先回退 GitHub）。
结论：当前用户群体 GitHub 可达，双通道会引入 manifest 同步、双仓库 Release 同步、
Gitee 开源审核与附件限额（单文件 100MB）等额外运维成本，暂不启用。
代码侧无需改造即可回归：`updateRepo` 与下载 host 白名单集中在 internal/api/system.go，
manifest 双源方案设计稿见本文档早期版本（git 历史）。

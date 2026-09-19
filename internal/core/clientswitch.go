package core

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// ============================================================
// 客户端账号切换（写入官方登录文件 + 重启客户端）
//
// 思路对齐 workbuddy-switch / WorkDaddy（思路复刻、实现自写）：
//   1. 备份官方当前登录文件（可随时手工还原）
//   2. 退出官方客户端
//   3. 把账号池中目标账号的凭证写入官方固定登录位（原子写，0600）
//   4. 重新启动客户端，写入的账号即当前登录
//
// 防御逻辑（对齐 WorkDaddy resolveAuthTarget 的校验思路）：
//   - 跨通道拒绝：官方当前登录与目标账号 realm 不同（国内/国际）时拒绝切换；
//   - 坏数据拒绝：官方登录文件存在但解析失败时不覆盖（防止毁掉现有登录）。
// ============================================================

// EventClientSwitch 客户端切换进度事件（step: backup/stop/write/start/done/error）
const EventClientSwitch = "clientswitch:progress"

// OfficialClientAuthFile 官方客户端固定登录位路径（官方唯一实际读取的文件）。
func OfficialClientAuthFile() (string, error) {
	switch runtime.GOOS {
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, "Library", "Application Support",
			"CodeBuddyExtension", "Data", "Public", "auth", "workbuddy-desktop.info"), nil
	case "windows":
		local, err := os.UserCacheDir()
		if err != nil {
			return "", err
		}
		// Windows 官方客户端登录位（社区工具实测路径；未经真机验证，如不一致请反馈）
		return filepath.Join(local, "CodeBuddyExtension", "Data", "Public", "auth", "workbuddy-desktop.info"), nil
	default:
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".config", "CodeBuddyExtension", "Data", "Public", "auth", "workbuddy-desktop.info"), nil
	}
}

// findOfficialClient 探测官方客户端（显式路径优先；macOS 扫描 /Applications）。
func findOfficialClient(explicit string) (bin string, appDir string, err error) {
	if p := strings.TrimSpace(explicit); p != "" {
		if st, serr := os.Stat(p); serr == nil && !st.IsDir() {
			return p, filepath.Dir(filepath.Dir(p)), nil
		}
		return "", "", fmt.Errorf("指定的客户端路径不存在: %s", p)
	}
	switch runtime.GOOS {
	case "darwin":
		matches, _ := filepath.Glob("/Applications/WorkBuddy*.app/Contents/MacOS/Electron")
		if len(matches) == 0 {
			return "", "", fmt.Errorf("未在 /Applications 下找到 WorkBuddy 客户端，可在设置中指定路径")
		}
		return matches[0], filepath.Dir(filepath.Dir(matches[0])), nil
	case "windows":
		local, e := os.UserCacheDir()
		if e != nil {
			return "", "", e
		}
		candidates, _ := filepath.Glob(filepath.Join(local, "Programs", "WorkBuddy*", "WorkBuddy.exe"))
		if len(candidates) == 0 {
			return "", "", fmt.Errorf("未找到 WorkBuddy 客户端，可在设置中指定路径")
		}
		return candidates[0], filepath.Dir(candidates[0]), nil
	default:
		return "", "", fmt.Errorf("当前平台（%s）暂不支持自动探测客户端，请手动指定", runtime.GOOS)
	}
}

// ClientSwitchPrecheck 切换前检查结果（前端据此渲染确认信息）
type ClientSwitchPrecheck struct {
	UID              string `json:"uid"`
	Nickname         string `json:"nickname"`
	TargetRealm      string `json:"targetRealm"`
	OfficialAuthFile string `json:"officialAuthFile"`
	OfficialExists   bool   `json:"officialExists"`
	OfficialRealm    string `json:"officialRealm"` // 当前官方登录区域（解析失败为空）
	ClientFound      bool   `json:"clientFound"`
	ClientPath       string `json:"clientPath"`
	ClientRunning    bool   `json:"clientRunning"`
	BackupDir        string `json:"backupDir"`
	Warnings         string `json:"warnings,omitempty"`
}

// ClientSwitchPrecheck 切换前检查：目标账号、官方登录位状态、客户端探测。
func (s *Service) ClientSwitchPrecheck(uid string) (*ClientSwitchPrecheck, error) {
	pre := &ClientSwitchPrecheck{}
	acc, ok := s.FindAccount(uid)
	if !ok {
		return nil, fmt.Errorf("账号 %s 不存在", uid)
	}
	pre.UID, pre.Nickname, pre.TargetRealm = uid, acc.Nickname, acc.Realm

	authFile, err := OfficialClientAuthFile()
	if err != nil {
		return nil, err
	}
	pre.OfficialAuthFile = authFile
	if raw, err := os.ReadFile(authFile); err == nil {
		pre.OfficialExists = true
		if c, perr := ParseCredential(authFile, raw); perr == nil && c != nil {
			pre.OfficialRealm = c.Realm
		} else {
			pre.Warnings = joinWarn(pre.Warnings, "官方登录文件存在但无法解析——为保护现有登录，本次切换将拒绝执行")
		}
	}
	if pre.OfficialRealm != "" && pre.TargetRealm != "" && pre.OfficialRealm != pre.TargetRealm {
		pre.Warnings = joinWarn(pre.Warnings, fmt.Sprintf(
			"跨通道切换：官方当前为 %s 版，目标账号为 %s 版，已拒绝（请先在官方客户端登录同通道账号）",
			pre.OfficialRealm, pre.TargetRealm))
	}

	bin, _, ferr := findOfficialClient(s.GetConfig().Inject.ClientPath)
	if ferr == nil {
		pre.ClientFound, pre.ClientPath = true, bin
		pre.ClientRunning = clientProcessRunning(bin)
	} else {
		pre.Warnings = joinWarn(pre.Warnings, ferr.Error())
	}
	pre.BackupDir = s.clientSwitchBackupDir()
	return pre, nil
}

func joinWarn(a, b string) string {
	if a == "" {
		return b
	}
	return a + "；" + b
}

func (s *Service) clientSwitchBackupDir() string {
	return filepath.Join(filepath.Dir(s.ConfigPath()), "clientswitch")
}

// ClientSwitchAccount 执行切换四步流水线，每步发进度事件。同步执行（整体约 5-10 秒）。
func (s *Service) ClientSwitchAccount(uid string) error {
	acc, ok := s.FindAccount(uid)
	if !ok {
		return fmt.Errorf("账号 %s 不存在", uid)
	}
	pre, err := s.ClientSwitchPrecheck(uid)
	if err != nil {
		return err
	}
	if strings.Contains(pre.Warnings, "拒绝") {
		return fmt.Errorf("%s", pre.Warnings)
	}
	if !pre.ClientFound {
		return fmt.Errorf("未找到官方客户端，无法切换")
	}

	switchStep := func(step, message string) {
		EmitEvent(EventClientSwitch, map[string]any{"step": step, "uid": uid, "message": message})
	}

	// 1. 备份官方当前登录文件
	switchStep("backup", "备份官方当前登录文件")
	if pre.OfficialExists {
		dir := s.clientSwitchBackupDir()
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("创建备份目录失败: %w", err)
		}
		backup := filepath.Join(dir, fmt.Sprintf("backup-%s.info", time.Now().Format("20060102-150405")))
		raw, err := os.ReadFile(pre.OfficialAuthFile)
		if err != nil {
			return fmt.Errorf("读取官方登录文件失败: %w", err)
		}
		if err := os.WriteFile(backup, raw, 0o600); err != nil {
			return fmt.Errorf("写入备份失败: %w", err)
		}
	}

	// 2. 退出官方客户端
	if pre.ClientRunning {
		switchStep("stop", "退出官方客户端（请先确认已保存工作内容）")
		if err := stopOfficialClient(pre.ClientPath, pre.ClientPath); err != nil {
			return fmt.Errorf("退出客户端失败: %w", err)
		}
		time.Sleep(2 * time.Second)
	}

	// 3. 写入目标账号凭证到官方登录位（原子写）
	switchStep("write", fmt.Sprintf("写入账号 %s 的登录凭证", acc.Nickname))
	cred, err := LoadUpstreamCred(s.AuthDir(), acc.Credential)
	if err != nil {
		return fmt.Errorf("读取账号凭证失败: %w", err)
	}
	_ = cred // LoadUpstreamCred 校验凭证可解析；写入用原始文件内容，避免字段重排
	raw, err := os.ReadFile(filepath.Join(s.AuthDir(), acc.Credential))
	if err != nil {
		return fmt.Errorf("读取凭证文件失败: %w", err)
	}
	tmp := pre.OfficialAuthFile + ".tmp"
	if err := os.MkdirAll(filepath.Dir(pre.OfficialAuthFile), 0o700); err != nil {
		return fmt.Errorf("创建登录目录失败: %w", err)
	}
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("写入登录文件失败: %w", err)
	}
	if err := os.Rename(tmp, pre.OfficialAuthFile); err != nil {
		return fmt.Errorf("替换登录文件失败: %w", err)
	}

	// 4. 重新启动客户端（带 CDP 端口，避免切换后再次注入时强杀）
	switchStep("start", "重新启动官方客户端")
	if err := startOfficialClient(pre.ClientPath, s.GetConfig().Inject.Port); err != nil {
		return fmt.Errorf("启动客户端失败: %w（登录文件已写入，可手动打开客户端）", err)
	}
	switchStep("done", "切换完成，客户端将以 "+acc.Nickname+" 的身份启动")
	s.Store().Audit("client.switch", uid, "official="+pre.OfficialAuthFile)
	return nil
}

// clientProcessRunning 探测官方客户端是否在运行（按二进制路径匹配）
func clientProcessRunning(bin string) bool {
	switch runtime.GOOS {
	case "darwin", "linux":
		out, err := exec.Command("pgrep", "-f", bin).Output()
		return err == nil && len(strings.TrimSpace(string(out))) > 0
	case "windows":
		out, err := exec.Command("tasklist", "/FI", fmt.Sprintf("IMAGENAME eq %s", filepath.Base(bin)), "/FO", "CSV").Output()
		return err == nil && strings.Contains(strings.ToLower(string(out)), strings.ToLower(filepath.Base(bin)))
	}
	return false
}

// stopOfficialClient 退出客户端：macOS 优雅退出（osascript quit）+ 路径精确 pkill 兜底；
// Windows 按可执行路径精确结束（不按映像名，避免误伤其他 Electron 应用）。
func stopOfficialClient(bin, _ string) error {
	switch runtime.GOOS {
	case "darwin":
		// 应用名从二进制路径推导（兼容 WorkBuddy AI 等企业定制版；进程名是
		// Electron，不能按 "WorkBuddy" killall）。先 AppleScript 正常退出让
		// Electron 保存会话，pkill 仅兜底。
		appName := strings.TrimSuffix(filepath.Base(filepath.Dir(filepath.Dir(bin))), ".app")
		if appName != "" && appName != "MacOS" {
			_ = exec.Command("osascript", "-e", "quit app \""+appName+"\"").Run()
		}
		time.Sleep(2 * time.Second)
		if clientProcessRunning(bin) {
			_ = exec.Command("pkill", "-f", bin).Run()
			time.Sleep(1 * time.Second)
		}
		return nil
	case "windows":
		script := fmt.Sprintf(
			`Get-Process | Where-Object { $_.Path -eq '%s' } | Stop-Process -Force`, bin)
		return exec.Command("powershell", "-NoProfile", "-Command", script).Run()
	default:
		_ = exec.Command("pkill", "-f", bin).Run()
		return nil
	}
}

// startOfficialClient 重新启动客户端。
// cdpPort > 0 时带 --remote-debugging-port 启动（macOS 直接执行二进制以保证
// 参数生效，对齐 WorkDaddy relaunch-with-cdp.sh）——否则切换后客户端以普通
// 模式运行，下次注入又要强杀重启一次。
func startOfficialClient(bin string, cdpPort int) error {
	arg := ""
	if cdpPort > 0 {
		arg = fmt.Sprintf("--remote-debugging-port=%d", cdpPort)
	}
	switch runtime.GOOS {
	case "darwin":
		if arg != "" {
			cmd := exec.Command(bin, arg)
			if err := cmd.Start(); err != nil {
				return err
			}
			go func() { _ = cmd.Process.Release() }() // 长驻进程交给用户管理，避免僵尸
			return nil
		}
		app := filepath.Dir(filepath.Dir(bin)) // .app 目录
		return exec.Command("open", app).Start()
	case "windows":
		if arg != "" {
			return exec.Command("cmd", "/c", "start", "", bin, arg).Start()
		}
		return exec.Command("cmd", "/c", "start", "", bin).Start()
	default:
		if arg != "" {
			return exec.Command(bin, arg).Start()
		}
		return exec.Command(bin).Start()
	}
}

// ClientRestoreAuthBackup 用登录态备份（官方登录文件原文）恢复官方登录位并重启客户端。
// 防御校验与 ClientSwitchAccount 同一口径：
//   - 备份内容必须可解析（坏数据拒绝，防止毁掉登录位）；
//   - 官方现有登录文件存在但无法解析时拒绝覆盖；
//   - 备份与官方当前登录跨通道（国内/国际）时拒绝。
//
// 每步发 EventClientSwitch 进度事件（step: check/stop/write/start/done/error）。
func (s *Service) ClientRestoreAuthBackup(raw []byte) error {
	if len(raw) == 0 {
		return fmt.Errorf("备份内容为空")
	}
	authFile, err := OfficialClientAuthFile()
	if err != nil {
		return err
	}
	cred, perr := ParseCredential(authFile, raw)
	if perr != nil {
		return fmt.Errorf("备份内容无法解析，为保护登录位已拒绝恢复: %w", perr)
	}

	switchStep := func(step, message string) {
		EmitEvent(EventClientSwitch, map[string]any{"step": step, "uid": cred.UID, "message": message})
	}
	switchStep("check", fmt.Sprintf("校验备份账号 %s（%s 版）", cred.Nickname, strings.ToUpper(cred.Realm)))

	// 官方当前登录校验：坏数据拒绝、跨通道拒绝
	if cur, err := os.ReadFile(authFile); err == nil {
		curCred, curErr := ParseCredential(authFile, cur)
		if curErr != nil {
			return fmt.Errorf("官方登录文件存在但无法解析，为保护现有登录已拒绝恢复")
		}
		if curCred.Realm != "" && cred.Realm != "" && curCred.Realm != cred.Realm {
			return fmt.Errorf("跨通道恢复：官方当前为 %s 版登录，备份为 %s 版，已拒绝", curCred.Realm, cred.Realm)
		}
	}

	// 退出客户端（登录文件在客户端启动时读取，热替换无效）
	bin, _, ferr := findOfficialClient(s.GetConfig().Inject.ClientPath)
	if ferr != nil {
		return fmt.Errorf("未找到官方客户端，无法恢复: %w", ferr)
	}
	if clientProcessRunning(bin) {
		switchStep("stop", "退出官方客户端")
		if err := stopOfficialClient(bin, bin); err != nil {
			return fmt.Errorf("退出客户端失败: %w", err)
		}
		time.Sleep(2 * time.Second)
	}

	// 原子写入官方登录位
	switchStep("write", fmt.Sprintf("写入账号 %s 的登录凭证", cred.Nickname))
	tmp := authFile + ".tmp"
	if err := os.MkdirAll(filepath.Dir(authFile), 0o700); err != nil {
		return fmt.Errorf("创建登录目录失败: %w", err)
	}
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("写入登录文件失败: %w", err)
	}
	if err := os.Rename(tmp, authFile); err != nil {
		return fmt.Errorf("替换登录文件失败: %w", err)
	}

	// 重启客户端
	switchStep("start", "重新启动官方客户端")
	if err := startOfficialClient(bin, s.GetConfig().Inject.Port); err != nil {
		return fmt.Errorf("启动客户端失败: %w（登录文件已写入，可手动打开客户端）", err)
	}
	switchStep("done", "恢复完成，客户端将以 "+cred.Nickname+" 的身份启动")
	s.Store().Audit("client.restore", cred.UID, "official="+authFile)
	return nil
}

// ClientSwitchBackups 列出切换时的官方登录文件备份
func (s *Service) ClientSwitchBackups() []map[string]any {
	dir := s.clientSwitchBackupDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return []map[string]any{}
	}
	out := []map[string]any{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".info") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, map[string]any{
			"name": e.Name(),
			"path": filepath.Join(dir, e.Name()),
			"size": info.Size(),
			"time": info.ModTime().Format("2006-01-02 15:04"),
		})
	}
	// 新备份在前
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

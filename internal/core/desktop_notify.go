package core

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/services/notifications"
)

// 系统级桌面通知：Wails v3 内置 notifications 服务
// （macOS UNUserNotificationCenter / Windows Toast / Linux D-Bus）。
//
// 挂接点：EmitEvent 出口（events.go），复用现有事件流，业务代码零改动。转发范围：
//   - account:statusChanged(status=relogin)      → 账号需要重新登录
//   - app:error / gateway:log(level=error)       → 基础设施错误
//   - inject:status(kind=error|disconnected)     → 注入中断
//   - task:completed                             → 任务结果摘要（遵循推送 Enabled + OnlyFailures）
//
// 发送异步执行（底层 SDK 每次调用有 5 秒超时，不能阻塞事件链路）；
// 发送失败只写进程日志，不回流 EmitEvent，避免「通知失败 → app:error → 再通知」死循环。
// 同键 30 秒节流，防止落盘失败等高频错误刷屏。

// desktopNotifyThrottle 同一通知键的最小发送间隔
const desktopNotifyThrottle = 30 * time.Second

type desktopNotifyHub struct {
	mu       sync.Mutex
	notifier *notifications.NotificationService
	svc      *Service
	authed   bool             // macOS 通知授权状态（懒获取）
	lastSeen map[string]time.Time
}

var desktopHub = &desktopNotifyHub{lastSeen: map[string]time.Time{}}

// SetDesktopNotifier 注册通知服务（main 启动时调用）。svc 用于读取实时配置开关。
func SetDesktopNotifier(n *notifications.NotificationService, svc *Service) {
	desktopHub.mu.Lock()
	defer desktopHub.mu.Unlock()
	desktopHub.notifier = n
	desktopHub.svc = svc
}

// DesktopNotificationsAvailable 当前进程能否使用系统通知。
// macOS 的 UNUserNotificationCenter 要求进程有 bundle identifier（打包进 .app 才有），
// 未打包的 go run / 裸二进制下服务 Startup 会失败并中止整个应用启动，
// 因此这里先行探测：不可用就不注册服务（桌面通知静默降级为不可用）。
func DesktopNotificationsAvailable() bool {
	if runtime.GOOS != "darwin" {
		return true
	}
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	return strings.Contains(exe, ".app/Contents/MacOS/")
}

// SendDesktopTestNotify 设置页「测试通知」入口：走完整授权 + 发送链路。
// macOS 首次调用会触发系统授权弹窗。
func SendDesktopTestNotify() error {
	desktopHub.mu.Lock()
	n := desktopHub.notifier
	desktopHub.mu.Unlock()
	if n == nil {
		return fmt.Errorf("系统通知不可用：需以打包后的应用（.app / 安装包）运行")
	}
	if err := ensureDesktopAuth(n); err != nil {
		return err
	}
	return n.SendNotification(notifications.NotificationOptions{
		ID:    "wb-desktop-test",
		Title: "BuddyBot 测试通知",
		Body:  "桌面通知工作正常，这是一条测试消息。",
	})
}

// ensureDesktopAuth 查询并按需请求通知授权（Windows/Linux 恒为已授权）
func ensureDesktopAuth(n *notifications.NotificationService) error {
	ok, err := n.CheckNotificationAuthorization()
	if err != nil {
		return fmt.Errorf("查询通知授权失败: %w", err)
	}
	if ok {
		return nil
	}
	ok, err = n.RequestNotificationAuthorization()
	if err != nil {
		return fmt.Errorf("请求通知授权失败: %w", err)
	}
	if !ok {
		return fmt.Errorf("通知授权被拒绝，请在系统设置 → 通知中允许本应用")
	}
	return nil
}

// notifyDesktopEvent EmitEvent 出口的转发钩子；未注册服务或开关关闭时为空操作
func notifyDesktopEvent(name string, data any) {
	desktopHub.mu.Lock()
	n, svc, authed := desktopHub.notifier, desktopHub.svc, desktopHub.authed
	desktopHub.mu.Unlock()
	if n == nil || svc == nil {
		return
	}
	cfg := svc.GetConfig().Schedule.Notify
	if !cfg.DesktopEnabled() {
		return
	}

	title, body, key := desktopNotifyPayload(name, data)
	if title == "" {
		return
	}
	// 任务摘要额外遵循推送通道的总开关与「仅失败」开关，避免与远端推送行为不一致
	if name == EventTaskCompleted {
		m, _ := data.(map[string]any)
		failed := toInt(m["failed"])
		if !cfg.Enabled || (cfg.OnlyFailures && failed == 0) {
			return
		}
	}

	now := time.Now()
	desktopHub.mu.Lock()
	if t, ok := desktopHub.lastSeen[key]; ok && now.Sub(t) < desktopNotifyThrottle {
		desktopHub.mu.Unlock()
		return
	}
	desktopHub.lastSeen[key] = now
	authNeed := !authed
	desktopHub.mu.Unlock()

	go sendDesktopNotify(n, title, body, key, authNeed)
}

// sendDesktopNotify 异步发送：懒授权 + 发送，失败只写进程日志
func sendDesktopNotify(n *notifications.NotificationService, title, body, key string, needAuth bool) {
	if needAuth {
		if err := ensureDesktopAuth(n); err != nil {
			log.Printf("[desktop-notify] 授权失败，跳过通知: %v", err)
			return
		}
		desktopHub.mu.Lock()
		desktopHub.authed = true
		desktopHub.mu.Unlock()
	}
	err := n.SendNotification(notifications.NotificationOptions{
		ID:    "wb-" + key, // 确定性 ID：同键通知在通知中心原地替换而非堆叠
		Title: title,
		Body:  body,
	})
	if err != nil {
		log.Printf("[desktop-notify] 发送失败 (%s): %v", key, err)
	}
}

// desktopNotifyPayload 事件 → 通知文案映射；返回空 title 表示不通知
func desktopNotifyPayload(name string, data any) (title, body, key string) {
	m, _ := data.(map[string]any)
	get := func(k string) string {
		if m == nil {
			return ""
		}
		v, _ := m[k].(string)
		return v
	}
	switch name {
	case EventAccountStatus:
		uid := get("uid")
		switch get("status") {
		case "relogin":
			return "账号需要重新登录",
				fmt.Sprintf("账号 %s 的登录凭证已失效，请重新扫码授权", shortUID(uid)),
				"acc-relogin-" + uid
		case "credit_expiring":
			// 积分临期作废预警（调度器已按账号×自然日去重，这里只做文案）
			return "积分临期预警",
				fmt.Sprintf("账号 %s 有 %s 积分将在 %s 前到期，建议优先使用该账号",
					get("nickname"), trimFloat(toFloat(m["expiring"])), get("expireDay")),
				"acc-credit-expiring-" + uid
		case "credit_exhausted":
			return "账号积分已耗尽",
				fmt.Sprintf("账号 %s 的积分余额已为 0，可从池中移除或等待下个周期", get("nickname")),
				"acc-credit-exhausted-" + uid
		}
		return "", "", ""
	case EventAppError:
		scope, msg := get("scope"), get("message")
		return "应用错误（" + scope + "）", msg, "app-error-" + scope
	case EventGatewayLog:
		if get("level") != "error" {
			return "", "", ""
		}
		return "网关错误", get("message"), "gw-error"
	case EventInjectStatus:
		switch get("kind") {
		case "error":
			return "注入异常", get("message"), "inject-error"
		case "disconnected":
			return "官方客户端连接断开", "注入会话已断开，请检查官方客户端是否在运行", "inject-disc"
		}
		return "", "", ""
	case EventTaskCompleted:
		taskType := get("type")
		num := func(k string) int { return toInt(m[k]) }
		return "任务完成：" + taskLabel(taskType),
			fmt.Sprintf("触发 %s · 成功 %d · 失败 %d · 跳过 %d", get("trigger"), num("success"), num("failed"), num("skipped")),
			"task-" + taskType
	}
	return "", "", ""
}

// toInt 事件负载中的数值字段安全取值（JSON 数字 / int 均可）
func toInt(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case float64:
		return int(x)
	case int64:
		return int(x)
	}
	return 0
}

// toFloat 事件负载中的浮点字段安全取值
func toFloat(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int:
		return float64(x)
	case int64:
		return float64(x)
	}
	return 0
}

// trimFloat 积分数值紧凑显示：整数值不带小数，其余保留一位
func trimFloat(v float64) string {
	if v == float64(int64(v)) {
		return fmt.Sprintf("%d", int64(v))
	}
	return fmt.Sprintf("%.1f", v)
}

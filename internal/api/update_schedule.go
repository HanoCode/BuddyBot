package api

import (
	"context"
	"strings"
	"time"

	"workbuddy-desktop/internal/core"
)

// 定期检查更新节奏：启动 1 分钟后查一次，之后每 6 小时一次。
// 检查为轻量 GET（GitHub Releases API，匿名限流 60 次/h/IP），该频率远低于限额。
const (
	updateCheckStartupDelay = time.Minute
	updateCheckInterval     = 6 * time.Hour
)

// startUpdateScheduler 启动后台定期检查协程：发现新版本时 emit update:available
// （负载为 CheckUpdate 的 UpdateInfo）。应用退出时协程随进程结束，无需显式停止。
func (s *SystemAPI) startUpdateScheduler() {
	go func() {
		time.Sleep(updateCheckStartupDelay)
		for {
			if strings.TrimSpace(updateRepo) != "" {
				// CheckUpdate 内部已做超时与错误处理；失败静默，下个周期重试
				if info, err := s.CheckUpdate(context.Background()); err == nil && info.HasUpdate {
					core.EmitEvent(core.EventUpdateAvailable, info)
				}
			}
			time.Sleep(updateCheckInterval)
		}
	}()
}

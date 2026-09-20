package core

import (
	"sync"
)

// ============================================================
// 防休眠（keep-awake）
//
// 目标：开启后阻止系统在自动化任务运行期间进入休眠，关闭后立即恢复。
// 实现按平台走系统能力，不引第三方依赖：
//   - macOS:  spawn /usr/bin/caffeinate（子进程退出即释放断言）
//   - Windows: SetThreadExecutionState(ES_CONTINUOUS|ES_SYSTEM_REQUIRED)
//   - Linux:  systemd-inhibit --what=sleep（无 systemd 时如实上报不支持）
//
// 本文件为全平台公共逻辑；平台差异仅 startPlatformKeepAwake 一个函数：
// 非 Windows 见 awake_exec.go，Windows 见 awake_windows.go。
// ============================================================

// Awake 防休眠管理器（进程内单例语义，由 Service 持有）
type Awake struct {
	mu     sync.Mutex
	stopFn func() // 非 nil 表示已启用
	note   string // 当前实现方式的说明（托盘/界面展示用）
	active bool
}

// SetAwake 开启/关闭防休眠；重复开启返回 nil（幂等）。
func (s *Service) SetAwake(on bool) error {
	if on {
		if err := s.awake.start(); err != nil {
			return err
		}
	} else {
		s.awake.stop()
	}
	// 持久化到配置，重启后保持用户选择
	cfg := s.GetConfig()
	if cfg.KeepAwake != on {
		cfg.KeepAwake = on
		if err := s.UpdateConfig(cfg); err != nil {
			return err
		}
		EmitEvent(EventInjectStatus, map[string]any{"kind": "awake", "on": on})
	}
	return nil
}

// AwakeActive 当前是否处于防休眠状态
func (s *Service) AwakeActive() bool { return s.awake.active }

// StopAwake 停止防休眠断言：MCP 子命令等常驻后台形态使用——
// server 生命周期等于 agent 会话时长，不应在无人值守时阻止系统休眠
func (s *Service) StopAwake() { s.awake.stop() }

// AwakeNote 实现方式说明（如 "caffeinate"）
func (s *Service) AwakeNote() string { return s.awake.note }

// ApplyAwakeOnStart 按配置恢复防休眠状态（应用启动时调用）
func (s *Service) ApplyAwakeOnStart() {
	if s.GetConfig().KeepAwake {
		_ = s.awake.start()
	}
}

func (a *Awake) start() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.active {
		return nil
	}
	stop, note, err := startPlatformKeepAwake()
	if err != nil {
		return err
	}
	a.stopFn = stop
	a.note = note
	a.active = true
	return nil
}

func (a *Awake) stop() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.active {
		return
	}
	if a.stopFn != nil {
		a.stopFn()
	}
	a.stopFn, a.note, a.active = nil, "", false
}

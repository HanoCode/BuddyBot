//go:build windows

package core

import (
	"fmt"
	"sync"

	"golang.org/x/sys/windows"
)

// Windows 实现：SetThreadExecutionState(ES_CONTINUOUS|ES_SYSTEM_REQUIRED)。
// ES_CONTINUOUS 使效果持续到下一次调用；恢复时单独传 ES_CONTINUOUS 清除。

const (
	esContinuouslyActive = 0x80000000
	esSystemRequired     = 0x00000001
)

var awakeMu sync.Mutex

func startPlatformKeepAwake() (stop func(), note string, err error) {
	awakeMu.Lock()
	defer awakeMu.Unlock()
	ret, _, lastErr := procSetThreadExecutionState.Call(esContinuouslyActive | esSystemRequired)
	if ret == 0 {
		return nil, "", fmt.Errorf("SetThreadExecutionState 失败: %v", lastErr)
	}
	return func() {
		awakeMu.Lock()
		defer awakeMu.Unlock()
		_, _, _ = procSetThreadExecutionState.Call(esContinuouslyActive)
	}, "SetThreadExecutionState", nil
}

var (
	kernel32                    = windows.NewLazySystemDLL("kernel32.dll")
	procSetThreadExecutionState = kernel32.NewProc("SetThreadExecutionState")
)

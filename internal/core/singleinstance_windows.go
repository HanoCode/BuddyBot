//go:build windows

package core

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// 单实例保护（Windows）：命名互斥体。
// 场景：关窗口默认隐藏到托盘，旧实例仍在运行并占用网关端口 7863；
// 再次双击 exe 会启动第二个实例，网关 bind 冲突报 WSAEADDRINUSE。
// 这里让第二个实例弹原生提示后直接退出。

var (
	siKernel32        = windows.NewLazySystemDLL("kernel32.dll")
	siProcCreateMutex = siKernel32.NewProc("CreateMutexW")
	siUser32          = windows.NewLazySystemDLL("user32.dll")
	siProcMessageBox  = siUser32.NewProc("MessageBoxW")
)

const siErrAlreadyExists = syscall.Errno(183) // ERROR_ALREADY_EXISTS

// AcquireSingleInstance 尝试持有全局单实例锁；已有实例在运行时弹提示并返回 false。
func AcquireSingleInstance() (ok bool) {
	name, _ := syscall.UTF16PtrFromString("Local\\BuddyBot.SingleInstance")
	h, _, err := siProcCreateMutex.Call(0, 0, uintptr(unsafe.Pointer(name)))
	if h == 0 {
		return true // 创建互斥体失败（权限等极端情况）：不阻止启动，宁缺勿滥
	}
	if err == siErrAlreadyExists {
		title, _ := syscall.UTF16PtrFromString("BuddyBot")
		text, _ := syscall.UTF16PtrFromString("BuddyBot 已在运行（可能在系统托盘）。请先退出已运行的实例，或在设置中更换网关端口。")
		siProcMessageBox.Call(0, uintptr(unsafe.Pointer(text)), uintptr(unsafe.Pointer(title)), 0x40)
		return false
	}
	return true
}

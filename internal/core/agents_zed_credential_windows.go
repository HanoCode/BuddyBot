//go:build windows

package core

import (
	"fmt"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	credTypeGeneric         = 1 // CRED_TYPE_GENERIC
	credPersistLocalMachine = 2 // CRED_PERSIST_LOCAL_MACHINE
	zedCredentialUser       = "Bearer"
)

var (
	advapi32       = windows.NewLazySystemDLL("advapi32.dll")
	procCredWriteW = advapi32.NewProc("CredWriteW")
	procCredReadW  = advapi32.NewProc("CredReadW")
	procCredFree   = advapi32.NewProc("CredFree")
)

// zedCredentialStore 把密钥写进 Windows 凭据管理器，形态与 Zed 自己写入的一致
// （见 agents_zed_cred_windows_layout.go 的说明）。同名条目会被覆盖。
func zedCredentialStore(apiURL, apiKey string) error {
	if strings.TrimSpace(apiURL) == "" || apiKey == "" {
		return nil
	}
	target, err := syscall.UTF16PtrFromString(zedCredentialTarget(apiURL))
	if err != nil {
		return fmt.Errorf("凭据名编码失败: %w", err)
	}
	user, err := syscall.UTF16PtrFromString(zedCredentialUser)
	if err != nil {
		return fmt.Errorf("凭据用户名编码失败: %w", err)
	}
	blob := []byte(apiKey)
	cred := credW{
		Type:               credTypeGeneric,
		TargetName:         target,
		CredentialBlobSize: uint32(len(blob)),
		CredentialBlob:     &blob[0],
		Persist:            credPersistLocalMachine,
		UserName:           user,
	}
	if ret, _, callErr := procCredWriteW.Call(uintptr(unsafe.Pointer(&cred)), 0); ret == 0 {
		return fmt.Errorf("CredWriteW 失败: %w", callErr)
	}
	return nil
}

// zedCredentialCheck 只判断条目是否存在（CredReadW 会读回内容，但不返回给调用方，
// 也不会像 macOS 钥匙串那样弹授权框）。
func zedCredentialCheck(apiURL string) bool {
	if strings.TrimSpace(apiURL) == "" {
		return false
	}
	target, err := syscall.UTF16PtrFromString(zedCredentialTarget(apiURL))
	if err != nil {
		return false
	}
	var cred *credW
	if ret, _, _ := procCredReadW.Call(
		uintptr(unsafe.Pointer(target)), credTypeGeneric, 0, uintptr(unsafe.Pointer(&cred)),
	); ret == 0 || cred == nil {
		return false
	}
	procCredFree.Call(uintptr(unsafe.Pointer(cred)))
	return true
}

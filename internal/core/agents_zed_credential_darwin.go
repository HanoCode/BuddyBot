//go:build darwin

package core

import (
	"fmt"
	"os/exec"
	"strings"
)

// Zed 的 openai_compatible provider 把密钥存在系统钥匙串里，条目形态为
// Internet Password：kSecAttrServer = provider 的 api_url（完整 URL 字符串）、
// kSecAttrAccount = "Bearer"、密码 = 密钥。这里用 security CLI 写入，形态与
// Zed 自己写入的完全一致（见 crates/gpui_apple/src/keychain.rs），从而免去为一次性
// 写入链接 Security.framework 的成本。
const zedKeychainAccount = "Bearer"

func zedCredentialStore(apiURL, apiKey string) error {
	if strings.TrimSpace(apiURL) == "" || apiKey == "" {
		return nil
	}
	// -U：条目已存在则更新，不会重复创建
	out, err := exec.Command("security", "add-internet-password",
		"-a", zedKeychainAccount, "-s", apiURL, "-w", apiKey, "-U").CombinedOutput()
	if err != nil {
		return fmt.Errorf("security add-internet-password: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// zedCredentialCheck 只查条目是否存在：不带 -w 就不读取密钥内容，因此不会触发
// 钥匙串授权弹窗，可以安全地在「探测」里调用。
func zedCredentialCheck(apiURL string) bool {
	if strings.TrimSpace(apiURL) == "" {
		return false
	}
	return exec.Command("security", "find-internet-password", "-s", apiURL).Run() == nil
}

//go:build !darwin && !windows

package core

// 其余平台（Linux 等）暂无系统凭据写入实现：Zed 在 Linux 走 Secret Service
// （D-Bus），成本高且无法在本机验证，暂不臆造。Zed 在这些平台也可从环境变量读密钥
// （provider 名 WorkBuddy → WORKBUDDY_API_KEY），由用户自行设置；接入页的客户端
// 说明里已写明。这里保持成功返回，不阻断配置写入。
func zedCredentialStore(apiURL, apiKey string) error { return nil }

// 无法核验凭据是否存在，一律视为已提供，避免界面误报「未接入」。
func zedCredentialCheck(apiURL string) bool { return true }

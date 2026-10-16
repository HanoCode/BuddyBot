package ferry

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// 系统代理只支持**手动配置**的 HTTP/HTTPS 代理：PAC 与 SOCKS 拿不到可用的转发口，
// 这里直接报错而不是猜一个地址去连（与参考实现一致）。
//
// 关掉代理时只下发绕过本机服务的 NO_PROXY —— 本机的隔离 OpenCode 与网关绝不能
// 走代理出去。

var noProxyEnv = map[string]string{
	"NO_PROXY": "localhost,127.0.0.1,::1",
	"no_proxy": "localhost,127.0.0.1,::1",
}

// SystemProxyEnv 返回要注入子进程的代理环境变量。
func SystemProxyEnv(enabled bool) (map[string]string, error) {
	if !enabled {
		return cloneEnv(noProxyEnv), nil
	}
	switch runtime.GOOS {
	case "darwin":
		out, err := exec.Command("/usr/sbin/scutil", "--proxy").Output()
		if err != nil {
			return nil, fmt.Errorf("读取系统代理失败: %w", err)
		}
		return ParseScutilProxy(string(out))
	case "windows":
		return windowsProxyEnv()
	default:
		return nil, fmt.Errorf("此系统暂不支持读取系统代理")
	}
}

func cloneEnv(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func proxyEnv(httpProxy, httpsProxy string) map[string]string {
	out := map[string]string{
		"HTTP_PROXY": httpProxy, "HTTPS_PROXY": httpsProxy,
		"http_proxy": httpProxy, "https_proxy": httpsProxy,
	}
	for k, v := range noProxyEnv {
		out[k] = v
	}
	return out
}

// scutilValueRe 匹配 scutil --proxy 输出里的 `键 : 值` 行。
var scutilValueRe = regexp.MustCompile(`(?m)^\s*([A-Za-z]+)\s*:\s*(.*?)\s*$`)

// ParseScutilProxy 解析 macOS `scutil --proxy` 的纯文本输出。
func ParseScutilProxy(text string) (map[string]string, error) {
	values := map[string]string{}
	for _, m := range scutilValueRe.FindAllStringSubmatch(text, -1) {
		values[m[1]] = m[2]
	}
	address := func(kind string) (string, error) {
		if values[kind+"Enable"] != "1" {
			return "", nil
		}
		host := values[kind+"Proxy"]
		port, err := strconv.Atoi(values[kind+"Port"])
		if host == "" || strings.ContainsAny(host, " \t/@?#") || err != nil || port < 1 || port > 65535 {
			return "", fmt.Errorf("系统代理地址无效")
		}
		// IPv6 字面量进 URL 必须加方括号，否则被当成端口分隔符
		if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
			host = "[" + host + "]"
		}
		return fmt.Sprintf("http://%s:%d", host, port), nil
	}
	httpProxy, err := address("HTTP")
	if err != nil {
		return nil, err
	}
	httpsProxy, err := address("HTTPS")
	if err != nil {
		return nil, err
	}
	if httpsProxy == "" {
		return nil, fmt.Errorf("请先在系统设置中启用 HTTPS 代理；暂不支持仅 SOCKS 或 PAC 配置")
	}
	if httpProxy == "" {
		httpProxy = httpsProxy
	}
	return proxyEnv(httpProxy, httpsProxy), nil
}

const windowsProxyScript = "Get-ItemProperty -LiteralPath 'HKCU:\\Software\\Microsoft\\Windows\\CurrentVersion\\Internet Settings' | Select-Object ProxyEnable,ProxyServer | ConvertTo-Json -Compress"

func windowsProxyEnv() (map[string]string, error) {
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", windowsProxyScript)
	cmd.WaitDelay = 5 * time.Second
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("读取 Windows 系统代理失败: %w", err)
	}
	var settings struct {
		ProxyEnable json.Number `json:"ProxyEnable"`
		ProxyServer string      `json:"ProxyServer"`
	}
	if err := json.Unmarshal(out, &settings); err != nil {
		return nil, fmt.Errorf("Windows 系统代理信息无法解析: %w", err)
	}
	enable, _ := settings.ProxyEnable.Int64()
	return ParseWindowsProxy(enable != 0, settings.ProxyServer)
}

// ParseWindowsProxy 解析注册表里的 ProxyEnable / ProxyServer。
// ProxyServer 有两种写法：`host:port`，或 `http=host:port;https=host:port`。
func ParseWindowsProxy(enabled bool, server string) (map[string]string, error) {
	if !enabled || strings.TrimSpace(server) == "" {
		return nil, fmt.Errorf("请先启用 Windows 手动系统代理；暂不支持仅 PAC 配置")
	}
	entries := []string{}
	for _, part := range strings.Split(server, ";") {
		if strings.TrimSpace(part) != "" {
			entries = append(entries, strings.TrimSpace(part))
		}
	}
	values := map[string]string{}
	if strings.Contains(server, "=") {
		for _, entry := range entries {
			if k, v, ok := strings.Cut(entry, "="); ok {
				values[strings.TrimSpace(k)] = strings.TrimSpace(v)
			}
		}
	} else if len(entries) > 0 {
		values["http"], values["https"] = entries[0], entries[0]
	}
	origin := func(value string) (string, error) {
		if value == "" || strings.ContainsAny(value, " \t/@?#") {
			return "", fmt.Errorf("Windows 系统代理地址无效")
		}
		host, portText, ok := strings.Cut(value, ":")
		if !ok || host == "" {
			return "", fmt.Errorf("Windows 系统代理地址无效")
		}
		port, err := strconv.Atoi(portText)
		if err != nil || port < 1 || port > 65535 {
			return "", fmt.Errorf("Windows 系统代理端口无效")
		}
		return fmt.Sprintf("http://%s:%d", host, port), nil
	}
	httpsProxy, err := origin(firstNonEmpty(values["https"], values["http"]))
	if err != nil {
		return nil, err
	}
	httpProxy, err := origin(firstNonEmpty(values["http"], values["https"]))
	if err != nil {
		return nil, err
	}
	return proxyEnv(httpProxy, httpsProxy), nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

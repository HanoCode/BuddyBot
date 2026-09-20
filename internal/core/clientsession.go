package core

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ============================================================
// 官方客户端当前登录账号探测（账号管理「自动获取登录信息」数据源）
//
// 官方客户端把登录信息写入固定登录位（OfficialClientAuthFile），其中
// uid 为明文，token / 昵称 / 手机号为 $wbEncrypted 加密信封（客户端私有
// 格式，这里不做也不应解密）。因此「自动获取登录信息」的可行路径是：
//  1. 从登录位读出当前登录 uid（明文）；
//  2. 在客户端凭证发现目录（config.client_auth_dirs，如 workbuddy2api
//     系工具 OAuth 登录落盘的 auths 目录）里找同 uid 的明文凭证；
//  3. 找到 → 账号管理页提示一键导入账号池；找不到 → 提示走扫码接入。
// ============================================================

// ClientSession 官方客户端当前登录账号探测结果
type ClientSession struct {
	Detected  bool   `json:"detected"`            // 登录位存在且能读到 uid
	UID       string `json:"uid,omitempty"`       // 当前登录账号 uid（明文）
	Nickname  string `json:"nickname,omitempty"`  // 昵称（登录位里加密，取自已发现的凭证文件）
	Realm     string `json:"realm,omitempty"`     // cn / global（取自已发现的凭证文件）
	AuthFile  string `json:"authFile,omitempty"`  // 官方登录位绝对路径
	CredFile  string `json:"credFile,omitempty"`  // 发现的可用明文凭证绝对路径（空=未发现）
	CredName  string `json:"credName,omitempty"`  // 凭证文件名（导入时的落盘名）
	ExpiresAt int64  `json:"expiresAt,omitempty"` // 凭证 expiresAt（Unix 秒；0=未知）
	InPool    bool   `json:"inPool"`              // 该 uid 已在账号池（auth_dir 已有凭证）
}

// clientSessionUID 从官方登录位文件提取当前登录 uid（容忍加密形态：只取明文字段）。
// 兼容两种形态：嵌套 {"account":{"uid":…}} 与扁平 {"uid":…}。
func clientSessionUID(raw []byte) string {
	var doc struct {
		UID     string `json:"uid"`
		Account struct {
			UID string `json:"uid"`
		} `json:"account"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return ""
	}
	if doc.Account.UID != "" {
		return doc.Account.UID
	}
	return strings.TrimSpace(doc.UID)
}

// expandHome 展开 ~/ 前缀（凭证发现目录支持用户家目录相对写法）
func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(p, "~"), "/"))
		}
	}
	return p
}

// DetectClientSession 探测官方客户端当前登录账号，并在发现目录里找同 uid 的
// 可用明文凭证。全程只读磁盘，不碰网络与客户端进程，可安全频繁调用。
func (s *Service) DetectClientSession() *ClientSession {
	authFile, err := OfficialClientAuthFile()
	if err != nil {
		return &ClientSession{}
	}
	return s.detectClientSessionAt(authFile)
}

// ImportClientSession 一键导入客户端当前登录账号：把发现的明文凭证复制进
// auth_dir（不移动原文件，官方登录位里的加密 token 不做也不需要解密），
// 返回落盘凭证绝对路径。调用方负责审计与事件广播。
func (s *Service) ImportClientSession() (string, error) {
	sess := s.DetectClientSession()
	if !sess.Detected {
		return "", fmt.Errorf("未检测到官方客户端登录信息（登录位不存在或未登录）")
	}
	if sess.CredFile == "" {
		return "", fmt.Errorf("未找到该账号的可用明文凭证（官方登录位中的 token 为客户端加密形态，无法直接提取），请通过「接入账号」扫码授权")
	}
	raw, err := os.ReadFile(sess.CredFile)
	if err != nil {
		return "", fmt.Errorf("读取凭证文件失败: %w", err)
	}
	dir := s.GetConfig().AuthDir
	name := sess.CredName
	if name == "" {
		name = fmt.Sprintf("workbuddy-%s.json", sess.UID)
	}
	return ImportCredential(dir, name, raw)
}

// detectClientSessionAt 探测实现（authFile 可注入，测试用）
func (s *Service) detectClientSessionAt(authFile string) *ClientSession {
	out := &ClientSession{}
	out.AuthFile = authFile
	raw, err := os.ReadFile(authFile)
	if err != nil {
		return out // 未登录 / 文件不存在：Detected=false
	}
	out.UID = clientSessionUID(raw)
	if out.UID == "" {
		return out
	}
	out.Detected = true
	if _, ok := s.FindAccount(out.UID); ok {
		out.InPool = true
	}
	if out.InPool {
		return out // 已在账号池：无需再找凭证
	}
	// 发现目录里逐个扫描 workbuddy*.json，解析合法且 uid 匹配即为可用凭证
	seen := map[string]bool{}
	for _, dir := range s.GetConfig().ClientAuthDirs {
		dir = expandHome(strings.TrimSpace(dir))
		if dir == "" || seen[dir] {
			continue
		}
		seen[dir] = true
		files, err := filepath.Glob(filepath.Join(dir, "workbuddy*.json"))
		if err != nil {
			continue
		}
		for _, p := range files {
			raw, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			cred, err := ParseCredential(p, raw)
			if err != nil || cred == nil || cred.UID != out.UID {
				continue
			}
			out.CredFile = p
			out.CredName = filepath.Base(p)
			out.Nickname = cred.Nickname
			out.Realm = cred.Realm
			out.ExpiresAt = cred.ExpiresAt
			return out
		}
	}
	return out
}

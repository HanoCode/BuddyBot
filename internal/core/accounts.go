package core

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Credential 从 auth_dir 下凭证文件解析出的快照。
//
// 兼容 workbuddy2api 的两种磁盘形态：
//
//	嵌套形 {"auth":{...},"account":{...}}  （插件 OAuth 输出）
//	扁平形 {"accessToken":...,"uid":...}   （手写 / 旧版）
type Credential struct {
	File         string
	UID          string
	Nickname     string
	EnterpriseID string
	Domain       string
	Realm        string
	ExpiresAt    int64 // Unix 秒；0 = 文件未带 expiresAt
	HasToken     bool
	HasRefresh   bool
	HasDevice    bool
	// EncAuth token 字段为官方客户端加密信封（{"$wbEncrypted":1,...}）：
	// 存在但 BuddyBot 读不出明文，无法用于网关调用 / 账号池接入。
	EncAuth bool
}

type credDoc struct {
	Auth *struct {
		AccessToken  encString `json:"accessToken"`
		RefreshToken encString `json:"refreshToken"`
		ExpiresAt    int64     `json:"expiresAt"`
		Domain       string    `json:"domain"`
		Realm        string    `json:"realm"`
	} `json:"auth"`
	Account *struct {
		UID          string    `json:"uid"`
		EnterpriseID string    `json:"enterpriseId"`
		Nickname     encString `json:"nickname"`
	} `json:"account"`

	// 扁平形字段
	AccessToken  encString `json:"accessToken"`
	RefreshToken encString `json:"refreshToken"`
	ExpiresAt    int64     `json:"expiresAt"`
	Domain       string    `json:"domain"`
	Realm        string    `json:"realm"`
	UID          string    `json:"uid"`
	EnterpriseID string    `json:"enterpriseId"`
	Nickname     encString `json:"nickname"`
	DeviceToken  string    `json:"device_token"`
}

// encString 兼容官方客户端 v5.6+ 的加密字符串字段（实测 2026-09-25）：
// 明文时是 JSON 字符串；加密时是信封对象 {"$wbEncrypted":1,"envelope":"..."}，
// 密钥在官方客户端侧，BuddyBot 无法解密。这里只记「存在与否 + 是否加密」，
// 不因字段从字符串变成对象而让整份凭证解析失败（uid/domain 仍是明文，够用）。
type encString struct {
	Value     string
	Present   bool
	Encrypted bool
}

func (e *encString) UnmarshalJSON(b []byte) error {
	*e = encString{}
	s := string(b)
	if s == "" || s == "null" {
		return nil
	}
	if s[0] == '"' {
		e.Present = true
		return json.Unmarshal(b, &e.Value)
	}
	// 非字符串一律视为「存在但不可读」（当前为 $wbEncrypted 信封，容忍未来格式漂移）
	e.Present, e.Encrypted = true, true
	return nil
}

// ParseCredential 解析单个凭证文件内容；仅要求 accessToken 存在。
func ParseCredential(file string, raw []byte) (*Credential, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("凭证文件为空")
	}
	var d credDoc
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, fmt.Errorf("凭证格式无效: %w", err)
	}
	c := &Credential{File: filepath.Base(file), HasDevice: d.DeviceToken != ""}
	if d.Auth != nil {
		c.UID, c.Nickname, c.EnterpriseID = "", "", ""
		if d.Account != nil {
			c.UID, c.EnterpriseID = d.Account.UID, d.Account.EnterpriseID
			c.Nickname = d.Account.Nickname.Value
		}
		applyAuthFields(c, d.Auth.AccessToken, d.Auth.RefreshToken, d.Auth.ExpiresAt, d.Auth.Domain, d.Auth.Realm)
	} else {
		c.UID, c.EnterpriseID = d.UID, d.EnterpriseID
		c.Nickname = d.Nickname.Value
		applyAuthFields(c, d.AccessToken, d.RefreshToken, d.ExpiresAt, d.Domain, d.Realm)
	}
	if !c.HasToken && !c.HasRefresh {
		return nil, fmt.Errorf("凭证缺少 accessToken / refreshToken")
	}
	if c.UID == "" {
		// 无 uid 时用文件名兜底标识，避免同一目录下多个账号被合并
		c.UID = strings.TrimSuffix(c.File, filepath.Ext(c.File))
	}
	c.Realm = resolveRealm(c.Realm, c.Domain)
	return c, nil
}

func applyAuthFields(c *Credential, access, refresh encString, expires int64, domain, realm string) {
	// 加密信封也算「存在」（官方登录文件升级后 token 恒为信封）；
	// 是否可读用 EncAuth 单独标记，供账号池导入口拒绝。
	c.HasToken = access.Present && (access.Encrypted || strings.TrimSpace(access.Value) != "")
	c.HasRefresh = refresh.Present && (refresh.Encrypted || strings.TrimSpace(refresh.Value) != "")
	c.EncAuth = (access.Present && access.Encrypted) || (refresh.Present && refresh.Encrypted)
	c.ExpiresAt = expires
	c.Domain = domain
	c.Realm = realm
}

// resolveRealm 归一化 realm：显式值优先，否则按 domain 后缀推断（workbuddy.ai 家族 = global）
func resolveRealm(explicit, domain string) string {
	if r := strings.TrimSpace(explicit); r != "" {
		return r
	}
	d := strings.ToLower(strings.TrimSpace(domain))
	if d == "workbuddy.ai" || strings.HasSuffix(d, ".workbuddy.ai") {
		return "global"
	}
	return "cn"
}

// LoadCredentials 扫描 dir 下 workbuddy*.json 并解析；损坏文件不阻断，作为坏凭证返回。
func LoadCredentials(dir string) []Credential {
	abs := ResolveAuthDir(dir)
	files, err := filepath.Glob(filepath.Join(abs, "workbuddy*.json"))
	if err != nil {
		return nil
	}
	sort.Strings(files)
	out := make([]Credential, 0, len(files))
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			out = append(out, Credential{File: filepath.Base(f)})
			continue
		}
		c, err := ParseCredential(f, raw)
		if err != nil {
			out = append(out, Credential{File: filepath.Base(f), UID: strings.TrimSuffix(filepath.Base(f), ".json")})
			continue
		}
		out = append(out, *c)
	}
	return out
}

// ResolveAuthDir 把配置里的 auth_dir 解析为绝对路径（相对路径按当前工作目录展开）。
func ResolveAuthDir(dir string) string {
	if dir == "" {
		dir = "auths"
	}
	if filepath.IsAbs(dir) {
		return dir
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return dir
	}
	return abs
}

// Account 账号视图：凭证文件（身份/凭证事实）+ 本地运行态（余额/冷却/活动）合并结果。
type Account struct {
	UID                 string  `json:"uid"`
	Nickname            string  `json:"nickname"`
	Domain              string  `json:"domain"`
	Realm               string  `json:"realm"`
	EnterpriseID        string  `json:"enterpriseId"`
	Credential          string  `json:"credential"`
	Status              string  `json:"status"` // online / cooldown / expired / unknown / disabled / invalid
	TokenExpiry         string  `json:"tokenExpiry"`
	ExpiresIn           int64   `json:"expiresIn"` // 剩余秒；<0 已过期
	HasToken            bool    `json:"hasToken"`
	HasRefresh          bool    `json:"hasRefresh"`
	Credits             float64 `json:"credits"`
	CreditsKnown        bool    `json:"creditsKnown"`
	CreditsUsed         float64 `json:"creditsUsed,omitempty"`
	CreditsExpiring     float64 `json:"creditsExpiring,omitempty"` // 72h 内到期的剩余积分
	LastCheckin         string  `json:"lastCheckin"`
	LastActivity        string  `json:"lastActivity"`
	CooldownUntil       string  `json:"cooldownUntil"`
	FailStreak          int     `json:"failStreak"`
	Inflight            int     `json:"inflight"`
	NeedsRelogin        bool    `json:"needsRelogin,omitempty"`
	CreditsExpireDay    string  `json:"creditsExpireDay,omitempty"`    // 最早积分到期日（YYYY-MM-DD，来自真实余额巡检）
	CreditsExpireRemain float64 `json:"creditsExpireRemain,omitempty"` // 该到期日的剩余积分
	Note                string  `json:"note,omitempty"`
}

// inflightTracker 账号在途请求计数（真实运行态，网关并发维护）
type inflightTracker struct {
	mu     sync.Mutex
	counts map[string]int
}

func newInflightTracker() *inflightTracker {
	return &inflightTracker{counts: map[string]int{}}
}

func (t *inflightTracker) enter(uid string) {
	if uid == "" {
		return
	}
	t.mu.Lock()
	t.counts[uid]++
	t.mu.Unlock()
}

func (t *inflightTracker) leave(uid string) {
	if uid == "" {
		return
	}
	t.mu.Lock()
	if t.counts[uid] > 0 {
		t.counts[uid]--
	}
	t.mu.Unlock()
}

func (t *inflightTracker) get(uid string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.counts[uid]
}

// deriveStatus 由凭证有效期 + 本地运行态推导账号状态。
func deriveStatus(c Credential, st AccountState, now time.Time) (status string, note string) {
	if st.Disabled {
		return "disabled", "已手动禁用"
	}
	if st.NeedsRelogin {
		// RT 被服务端明确拒绝（12153 类）：排除出网关账号池与自动任务，
		// 重新授权登录后自动恢复
		return "relogin", "refresh token 已失效，需重新授权登录"
	}
	if !c.HasToken && !c.HasRefresh {
		return "invalid", "凭证缺失或损坏"
	}
	if st.CooldownUntil != "" {
		if t, err := time.Parse(time.RFC3339, st.CooldownUntil); err == nil && t.After(now) {
			return "cooldown", "熔断冷却至 " + t.Format("15:04")
		}
	}
	if c.ExpiresAt <= 0 {
		return "unknown", "凭证未提供 expiresAt，无法判断有效期"
	}
	if c.ExpiresAt <= now.Unix() {
		if c.HasRefresh {
			return "expired", "access token 已过期，可刷新"
		}
		return "expired", "access token 已过期且无 refresh token"
	}
	return "online", ""
}

// BuildAccounts 合并凭证与运行态，产出账号列表（按 realm + uid 排序，保持顺序稳定）。
func BuildAccounts(creds []Credential, store *Store, tracker *inflightTracker) []Account {
	now := time.Now()
	out := make([]Account, 0, len(creds))
	for _, c := range creds {
		st := store.AccountState(c.UID)
		status, note := deriveStatus(c, st, now)
		a := Account{
			UID:                 c.UID,
			Nickname:            c.Nickname,
			Domain:              c.Domain,
			Realm:               c.Realm,
			EnterpriseID:        c.EnterpriseID,
			Credential:          c.File,
			Status:              status,
			HasToken:            c.HasToken,
			HasRefresh:          c.HasRefresh,
			Credits:             st.Credits,
			CreditsKnown:        st.CreditsKnown,
			CreditsUsed:         st.CreditsUsed,
			CreditsExpiring:     st.CreditsExpiring,
			LastCheckin:         st.LastCheckin,
			LastActivity:        st.LastActivity,
			CooldownUntil:       st.CooldownUntil,
			FailStreak:          st.FailStreak,
			NeedsRelogin:        st.NeedsRelogin,
			CreditsExpireDay:    st.CreditsExpireDay,
			CreditsExpireRemain: st.CreditsExpireRemain,
			Note:                note,
		}
		if c.ExpiresAt > 0 {
			a.TokenExpiry = time.Unix(c.ExpiresAt, 0).Format(time.RFC3339)
			a.ExpiresIn = c.ExpiresAt - now.Unix()
		}
		if tracker != nil {
			a.Inflight = tracker.get(c.UID)
		}
		if a.Note == "" {
			a.Note = st.Note
		}
		out = append(out, a)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Realm != out[j].Realm {
			return out[i].Realm < out[j].Realm
		}
		return out[i].UID < out[j].UID
	})
	return out
}

// ImportCredential 导入一份凭证 JSON 到 auth_dir，返回落盘文件名。
//
// 校验必须通过（可解析且带 token），否则拒绝写入，避免把坏文件放进凭证目录。
func ImportCredential(dir, name string, raw []byte) (string, error) {
	abs := ResolveAuthDir(dir)
	if c, err := ParseCredential(name, raw); err != nil {
		return "", err
	} else if c.UID == "" {
		return "", fmt.Errorf("凭证缺少 uid")
	} else if c.EncAuth {
		// 官方客户端加密信封里的 token BuddyBot 读不出明文，接入网关必然 401，
		// 不能让坏账号混进账号池（客户端切换不受影响，走 clientswitch 通道）。
		return "", fmt.Errorf("token 为官方客户端加密信封，无法接入账号池（请在官方客户端内登录该账号）")
	}
	base := strings.TrimSpace(name)
	if base == "" {
		base = "workbuddy-" + time.Now().Format("20060102-150405") + ".json"
	}
	if !strings.HasPrefix(base, "workbuddy") {
		base = "workbuddy-" + base
	}
	if !strings.HasSuffix(base, ".json") {
		base += ".json"
	}
	base = filepath.Base(base)
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return "", err
	}
	var pretty map[string]any
	if json.Unmarshal(raw, &pretty) == nil {
		if b, err := json.MarshalIndent(pretty, "", "  "); err == nil {
			raw = b
		}
	}
	dst := filepath.Join(abs, base)
	if err := os.WriteFile(dst, raw, 0o600); err != nil {
		return "", err
	}
	return base, nil
}

// DeleteCredential 删除指定账号（uid）对应的凭证文件，返回被删除的文件名。
func DeleteCredential(dir, uid string) (string, error) {
	for _, c := range LoadCredentials(dir) {
		if c.UID != uid {
			continue
		}
		p := filepath.Join(ResolveAuthDir(dir), c.File)
		if err := os.Remove(p); err != nil {
			return "", err
		}
		return c.File, nil
	}
	return "", fmt.Errorf("账号 %s 没有对应凭证文件", uid)
}

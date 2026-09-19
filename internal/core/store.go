package core

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// AccountState 账号本地运行态。
//
// 账号的身份/凭证以 auth_dir 下的凭证文件为准（见 accounts.go），本结构只保存
// 运行期产生、无法从凭证文件推导的数据（最近一次真实查询到的余额、签到时间、
// 熔断冷却、失败计数）。首次运行空库启动，不再有种子演示数据。
type AccountState struct {
	UID             string  `json:"uid"`
	Credits         float64 `json:"credits"`
	CreditsKnown    bool    `json:"creditsKnown"`              // false = 从未真实查询到余额
	CreditsUsed     float64 `json:"creditsUsed,omitempty"`     // 已用（细分查询写入）
	CreditsExpiring float64 `json:"creditsExpiring,omitempty"` // 临期（72h 窗口内到期）剩余
	LastCheckin     string  `json:"lastCheckin"`
	LastActivity    string  `json:"lastActivity"`
	CooldownUntil   string  `json:"cooldownUntil"` // RFC3339；空 = 未冷却
	FailStreak      int     `json:"failStreak"`
	Disabled        bool    `json:"disabled"`
	NeedsRelogin    bool    `json:"needsRelogin,omitempty"` // RT 被服务端明确拒绝，需重新扫码授权
	Note            string  `json:"note,omitempty"`

	// 成本账本（对齐 workbuddy2api NoteModelCost 的 EMA 口径）：
	// 两次真实余额查询之间的积分消耗 / 服务 token 数 → 每 1k token 成本 EMA。
	CostEMA         float64 `json:"costEma,omitempty"`     // 每 1k token 上游积分成本（EMA α=0.3）
	CostSamples     int     `json:"costSamples,omitempty"` // 成本观测次数
	TokensSinceSync int     `json:"tokensSinceSync,omitempty"`

	// 免费/收费学习账本（对齐 workbuddy-gateway probe）：模型 ID → 该账号请求
	// 该模型时 usage.credit 是否为 0（true=免费）。仅 total_tokens≥100 的样本
	// 才写入，防小样本误判。
	ModelFreeLedger map[string]bool `json:"modelFreeLedger,omitempty"`

	// 积分到期分层（对齐 workbuddy-switch-gateway 分层选号）：最早到期的
	// 非零剩余套餐的到期日（CST 2006-01-02）与该部分剩余量；空 = 无到期信息。
	CreditsExpireDay    string  `json:"creditsExpireDay,omitempty"`
	CreditsExpireRemain float64 `json:"creditsExpireRemain,omitempty"`
}

// ApiKey API 密钥。
//
// KeyHash 是明文的 SHA-256（网关鉴权用，绝不下发前端）；Mask 是展示串；
// Key 是明文本体（本地桌面应用，仅存本机存储文件，供管理界面随时复制完整密钥）。
type ApiKey struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	KeyHash     string   `json:"keyHash"`
	Key         string   `json:"key,omitempty"`
	Mask        string   `json:"mask"`
	Created     string   `json:"created"`
	Expires     string   `json:"expires"`
	Models      []string `json:"models"`
	IPWhitelist []string `json:"ipWhitelist"`
	MaxIps      int      `json:"maxIps"`
	TokenQuota  int      `json:"tokenQuota"`
	CreditQuota int      `json:"creditQuota"`
	TokenUsed   int      `json:"tokenUsed"`
	CreditUsed  int      `json:"creditUsed"`
	Requests    int      `json:"requests"`
	LastUsed    string   `json:"lastUsed"`
	Disabled    bool     `json:"disabled"`
}

// RequestLog 请求日志（网关真实记录，非估算）。
type RequestLog struct {
	ID           string  `json:"id"`
	TS           int64   `json:"ts"` // Unix 秒，聚合口径
	Time         string  `json:"time"`
	KeyName      string  `json:"keyName"`
	KeyID        string  `json:"keyId"`
	Model        string  `json:"model"`
	Status       int     `json:"status"`
	Tokens       int     `json:"tokens"`
	InputTokens  int     `json:"inputTokens"`
	OutputTokens int     `json:"outputTokens"`
	CacheTokens  int     `json:"cacheTokens,omitempty"` // 上游 usage 命中的缓存 token（未报为 0）
	Latency      float64 `json:"latency"`               // 总耗时 ms
	FirstLatency float64 `json:"firstLatency"`          // 首字节耗时 ms
	IP           string  `json:"ip"`
	SessionID    string  `json:"sessionId"`
	Stream       bool    `json:"stream"`
	Error        string  `json:"error,omitempty"`
}

// TaskLog 任务日志（调度器 / 手动触发的真实执行记录）。
type TaskLog struct {
	ID       string  `json:"id"`
	TS       int64   `json:"ts"`
	Time     string  `json:"time"`
	Type     string  `json:"type"`    // checkin / travel / keepalive / activity / school / cat / growth
	Trigger  string  `json:"trigger"` // manual / schedule
	UID      string  `json:"uid"`
	Status   string  `json:"status"` // success / failed / skipped
	Message  string  `json:"message"`
	Duration float64 `json:"duration"` // 耗时 ms
}

// CreditLog 积分变动流水（真实余额查询之间的差值，非估算）。
type CreditLog struct {
	ID      string  `json:"id"`
	TS      int64   `json:"ts"`
	Time    string  `json:"time"`
	UID     string  `json:"uid"`
	Delta   float64 `json:"delta"`   // 变动量（正 = 消耗，负 = 增加）
	Balance float64 `json:"balance"` // 变动后余额
	Note    string  `json:"note,omitempty"`
}

// AuditLog 审计日志（管理侧敏感操作记录）。
type AuditLog struct {
	ID     string `json:"id"`
	TS     int64  `json:"ts"`
	Time   string `json:"time"`
	Action string `json:"action"` // config.update / key.create / account.delete ...
	Target string `json:"target,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// storeData 持久化结构
type storeData struct {
	AccountStates map[string]*AccountState `json:"account_states"`
	Keys          []ApiKey                 `json:"keys"`
	RequestLogs   []RequestLog             `json:"request_logs"`
	TaskLogs      []TaskLog                `json:"task_logs"`
	CreditLogs    []CreditLog              `json:"credit_logs"`
	AuditLogs     []AuditLog               `json:"audit_logs"`
}

const (
	maxRequestLogs = 2000
	maxTaskLogs    = 500
	maxCreditLogs  = 1000
	maxAuditLogs   = 500
)

// Store 线程安全的本地存储（JSON 落盘，原子替换）。
//
// 首次运行生成空库：不再写入任何演示账号 / 演示密钥 / 演示日志。
// dynModels / dynEfforts 是上游动态模型目录（内存态，不落盘）：
// 由调度器 keepalive 周期性从 /v3/config 刷新，进程重启后等下一次刷新。
type Store struct {
	mu   sync.RWMutex
	path string
	data storeData

	dynModels map[string]ModelCap
	dynEffort map[string]string // 模型 ID → reasoning.defaultEffort

	// 脏标记合并落盘：高频变更（记账/日志/账号状态）只置脏，由后台定时合并写盘，
	// 避免每请求 3+ 次全量 MarshalIndent + rename。关键数据（密钥增删/显式 Persist）仍立即写。
	dirty   atomic.Bool
	dirtyCh chan struct{}
}

// markDirty 标记有未落盘变更并唤醒 flusher（须持有 s.mu 调用）
func (s *Store) markDirty() {
	s.dirty.Store(true)
	select {
	case s.dirtyCh <- struct{}{}:
	default:
	}
}

// Flush 若存在未落盘的变更则立即写盘（应用退出/关键节点调用）
func (s *Store) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.dirty.CompareAndSwap(true, false) {
		return nil
	}
	return s.save()
}

// NewStore 创建存储并加载数据；文件不存在或损坏时以空库启动。
func NewStore(path string) *Store {
	s := &Store{path: path, data: emptyData(), dirtyCh: make(chan struct{}, 1)}
	if err := s.load(); err != nil {
		// 损坏文件改名保留（可人工排查/恢复），绝不用空库直接覆盖原文件
		if os.IsNotExist(err) {
			_ = s.save()
		} else {
			_ = os.Rename(s.path, s.path+".corrupt.bak")
			_ = s.save()
		}
	}
	return s
}

func emptyData() storeData {
	return storeData{
		AccountStates: map[string]*AccountState{},
		Keys:          []ApiKey{},
		RequestLogs:   []RequestLog{},
		TaskLogs:      []TaskLog{},
		CreditLogs:    []CreditLog{},
		AuditLogs:     []AuditLog{},
	}
}

func (s *Store) load() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return err
	}
	d := emptyData()
	if err := json.Unmarshal(data, &d); err != nil {
		return err
	}
	if d.AccountStates == nil {
		d.AccountStates = map[string]*AccountState{}
	}
	if d.Keys == nil {
		d.Keys = []ApiKey{}
	}
	if d.RequestLogs == nil {
		d.RequestLogs = []RequestLog{}
	}
	if d.TaskLogs == nil {
		d.TaskLogs = []TaskLog{}
	}
	if d.CreditLogs == nil {
		d.CreditLogs = []CreditLog{}
	}
	if d.AuditLogs == nil {
		d.AuditLogs = []AuditLog{}
	}
	s.data = d
	return nil
}

func (s *Store) save() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if f, err := os.Open(tmp); err == nil {
		_ = f.Sync()
		_ = f.Close()
	}
	if err := os.Rename(tmp, s.path); err != nil {
		// 落盘失败必须可见：磁盘满/权限问题会让密钥、记账、日志全部静默丢失
		EmitEvent(EventAppError, map[string]any{"scope": "store", "message": "状态落盘失败: " + err.Error()})
		return err
	}
	return nil
}

// Path 存储文件路径
func (s *Store) Path() string { return s.path }

// MergeUpstreamModels 把上游动态模型目录并入内存清单：
// 目录外的模型以 upstream 来源新增；目录内的模型覆盖种子表的能力值（context/output）。
// 同时记录 reasoning.defaultEffort（deepseek thinking 注入的档位来源）。
func (s *Store) MergeUpstreamModels(infos []UpstreamModel) {
	if len(infos) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dynModels == nil {
		s.dynModels = map[string]ModelCap{}
	}
	if s.dynEffort == nil {
		s.dynEffort = map[string]string{}
	}
	for _, m := range infos {
		if m.ID == "" || m.ID == "auto" {
			continue
		}
		s.dynModels[m.ID] = ModelCap{Source: "upstream"}
		if m.DefaultEffort != "" {
			s.dynEffort[m.ID] = m.DefaultEffort
		}
	}
}

// UpstreamModels 返回动态目录快照（只读副本，供 ListModels 合并）。
func (s *Store) UpstreamModels() (map[string]ModelCap, map[string]string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	m := make(map[string]ModelCap, len(s.dynModels))
	for k, v := range s.dynModels {
		m[k] = v
	}
	e := make(map[string]string, len(s.dynEffort))
	for k, v := range s.dynEffort {
		e[k] = v
	}
	return m, e
}

// DefaultEffort 查询模型声明的默认推理档位；无记录返回空串。
func (s *Store) DefaultEffort(model string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.dynEffort[model]
}

// Persist 落盘（对外）
func (s *Store) Persist() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.save()
}

// ---- 账号运行态 ----

// AccountState 取账号运行态；不存在时返回零值副本（不落盘）
func (s *Store) AccountState(uid string) AccountState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if st, ok := s.data.AccountStates[uid]; ok && st != nil {
		return *st
	}
	return AccountState{UID: uid}
}

// MutateAccountState 在锁内修改账号运行态并落盘
func (s *Store) MutateAccountState(uid string, fn func(*AccountState)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.data.AccountStates[uid]
	if st == nil {
		st = &AccountState{UID: uid}
		s.data.AccountStates[uid] = st
	}
	fn(st)
	s.markDirty()
}

// PurgeAccountState 移除账号运行态（账号被删除时调用）
func (s *Store) PurgeAccountState(uid string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data.AccountStates, uid)
	s.markDirty()
}

// ---- 密钥 ----

func (s *Store) ListKeys() []ApiKey {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]ApiKey, len(s.data.Keys))
	copy(out, s.data.Keys)
	return out
}

func (s *Store) GetKey(id string) (ApiKey, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, k := range s.data.Keys {
		if k.ID == id {
			return k, true
		}
	}
	return ApiKey{}, false
}

// FindKeyByHash 按密钥摘要查找（网关鉴权唯一入口）
func (s *Store) FindKeyByHash(hash string) (ApiKey, bool) {
	if hash == "" {
		return ApiKey{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, k := range s.data.Keys {
		if k.KeyHash == hash {
			return k, true
		}
	}
	return ApiKey{}, false
}

func (s *Store) PutKey(k ApiKey) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Keys {
		if s.data.Keys[i].ID == k.ID {
			s.data.Keys[i] = k
			_ = s.save()
			return
		}
	}
	s.data.Keys = append(s.data.Keys, k)
	_ = s.save()
}

func (s *Store) DeleteKey(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, k := range s.data.Keys {
		if k.ID == id {
			s.data.Keys = append(s.data.Keys[:i], s.data.Keys[i+1:]...)
			_ = s.save()
			return true
		}
	}
	return false
}

// ConsumeKey 在一次成功请求后累加密钥用量（token / 积分 / 请求数）
func (s *Store) ConsumeKey(id string, tokens, credits int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Keys {
		if s.data.Keys[i].ID == id {
			s.data.Keys[i].TokenUsed += tokens
			s.data.Keys[i].CreditUsed += credits
			s.data.Keys[i].Requests++
			s.data.Keys[i].LastUsed = Now()
			s.markDirty()
			return
		}
	}
}

// ---- 日志 ----

func (s *Store) ListRequestLogs() []RequestLog {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]RequestLog, len(s.data.RequestLogs))
	copy(out, s.data.RequestLogs)
	return out
}

func (s *Store) ListTaskLogs() []TaskLog {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]TaskLog, len(s.data.TaskLogs))
	copy(out, s.data.TaskLogs)
	return out
}

func (s *Store) AppendRequestLog(l RequestLog) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if l.TS == 0 {
		l.TS = time.Now().Unix()
	}
	s.data.RequestLogs = append([]RequestLog{l}, s.data.RequestLogs...)
	if len(s.data.RequestLogs) > maxRequestLogs {
		s.data.RequestLogs = s.data.RequestLogs[:maxRequestLogs]
	}
	s.markDirty()
}

func (s *Store) AppendTaskLog(l TaskLog) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if l.TS == 0 {
		l.TS = time.Now().Unix()
	}
	s.data.TaskLogs = append([]TaskLog{l}, s.data.TaskLogs...)
	if len(s.data.TaskLogs) > maxTaskLogs {
		s.data.TaskLogs = s.data.TaskLogs[:maxTaskLogs]
	}
	s.markDirty()
}

// ClearLogs 清空日志（scope: request / task / credit / audit / all），返回清除条数
func (s *Store) ClearLogs(scope string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	if scope == "request" || scope == "all" {
		n += len(s.data.RequestLogs)
		s.data.RequestLogs = []RequestLog{}
	}
	if scope == "task" || scope == "all" {
		n += len(s.data.TaskLogs)
		s.data.TaskLogs = []TaskLog{}
	}
	if scope == "credit" || scope == "all" {
		n += len(s.data.CreditLogs)
		s.data.CreditLogs = []CreditLog{}
	}
	if scope == "audit" || scope == "all" {
		n += len(s.data.AuditLogs)
		s.data.AuditLogs = []AuditLog{}
	}
	_ = s.save()
	return n
}

// ---- 积分流水 ----

func (s *Store) ListCreditLogs() []CreditLog {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]CreditLog, len(s.data.CreditLogs))
	copy(out, s.data.CreditLogs)
	return out
}

// AppendCreditLog 记录一条积分变动（旧 → 新头部插入，超限裁剪）
func (s *Store) AppendCreditLog(l CreditLog) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if l.TS == 0 {
		l.TS = time.Now().Unix()
	}
	s.data.CreditLogs = append([]CreditLog{l}, s.data.CreditLogs...)
	if len(s.data.CreditLogs) > maxCreditLogs {
		s.data.CreditLogs = s.data.CreditLogs[:maxCreditLogs]
	}
	s.markDirty()
}

// ---- 审计日志 ----

func (s *Store) ListAuditLogs() []AuditLog {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]AuditLog, len(s.data.AuditLogs))
	copy(out, s.data.AuditLogs)
	return out
}

// AppendAuditLog 记录一条管理侧审计日志
func (s *Store) AppendAuditLog(l AuditLog) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if l.TS == 0 {
		l.TS = time.Now().Unix()
	}
	s.data.AuditLogs = append([]AuditLog{l}, s.data.AuditLogs...)
	if len(s.data.AuditLogs) > maxAuditLogs {
		s.data.AuditLogs = s.data.AuditLogs[:maxAuditLogs]
	}
	s.markDirty()
}

// Audit 记录管理侧操作的便捷入口
func (s *Store) Audit(action, target, detail string) {
	s.AppendAuditLog(AuditLog{ID: NewID("a"), Time: Now(), Action: action, Target: target, Detail: detail})
}

// ---- 工具 ----

// NewID 生成短随机 ID
func NewID(prefix string) string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return prefix + "_" + hex.EncodeToString(b)
}

// Now 格式化当前时间
func Now() string { return time.Now().Format("2006-01-02 15:04:05") }

// NowTS 当前 Unix 秒
func NowTS() int64 { return time.Now().Unix() }

// HashKey 计算密钥摘要（SHA-256 十六进制）
func HashKey(plain string) string {
	sum := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(sum[:])
}

// MaskKey 生成密钥展示串：保留前缀与尾部 4 位，中间打码
func MaskKey(plain string) string {
	if len(plain) <= 14 {
		return plain
	}
	return plain[:10] + "••••••••" + plain[len(plain)-4:]
}

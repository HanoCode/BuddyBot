package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"workbuddy-desktop/internal/core"
)

// KeysAPI 密钥管理API
type KeysAPI struct {
	service *core.Service
}

// NewKeysAPI 创建密钥API
func NewKeysAPI(service *core.Service) *KeysAPI {
	return &KeysAPI{service: service}
}

// KeyView 密钥视图：Mask 展示脱敏串，Key 为完整明文（仅在本机管理界面展示/复制）
type KeyView struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Mask        string   `json:"mask"`
	Key         string   `json:"key,omitempty"`
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

func toView(k core.ApiKey) KeyView {
	models := k.Models
	if models == nil {
		models = []string{}
	}
	ips := k.IPWhitelist
	if ips == nil {
		ips = []string{}
	}
	return KeyView{
		ID: k.ID, Name: k.Name, Mask: k.Mask, Key: k.Key, Created: k.Created, Expires: k.Expires,
		Models: models, IPWhitelist: ips, MaxIps: k.MaxIps,
		TokenQuota: k.TokenQuota, CreditQuota: k.CreditQuota,
		TokenUsed: k.TokenUsed, CreditUsed: k.CreditUsed,
		Requests: k.Requests, LastUsed: k.LastUsed, Disabled: k.Disabled,
	}
}

// CreateKeyParams 创建/更新密钥参数
type CreateKeyParams struct {
	Name        string   `json:"name"`
	Expires     string   `json:"expires,omitempty"`
	Models      []string `json:"models,omitempty"`
	IPWhitelist []string `json:"ipWhitelist,omitempty"`
	MaxIps      int      `json:"maxIps,omitempty"`
	TokenQuota  int      `json:"tokenQuota,omitempty"`
	CreditQuota int      `json:"creditQuota,omitempty"`
	Disabled    *bool    `json:"disabled,omitempty"`
}

// CreateKeyResult 创建密钥结果（明文同时落盘，可在列表再次复制）
type CreateKeyResult struct {
	ID   string `json:"id"`
	Key  string `json:"key"`
	Mask string `json:"mask"`
}

// List 获取所有密钥
func (k *KeysAPI) List(ctx context.Context) ([]KeyView, error) {
	keys := k.service.Store().ListKeys()
	out := make([]KeyView, 0, len(keys))
	for _, x := range keys {
		out = append(out, toView(x))
	}
	return out, nil
}

// Create 创建密钥：生成 wb- 前缀随机明文，本机落盘明文 + SHA-256 摘要（网关鉴权仍用摘要）
func (k *KeysAPI) Create(ctx context.Context, params CreateKeyParams) (*CreateKeyResult, error) {
	if err := ensureWritable(k.service); err != nil {
		return nil, err
	}
	if params.Name == "" {
		return nil, fmt.Errorf("密钥名称不能为空")
	}
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return nil, err
	}
	plain := "wb-" + hex.EncodeToString(buf)
	id := core.NewID("key")
	models := params.Models
	if len(models) == 0 {
		models = []string{"*"}
	}
	k.service.Store().PutKey(core.ApiKey{
		ID: id, Name: params.Name,
		KeyHash: core.HashKey(plain), Key: plain, Mask: core.MaskKey(plain),
		Created: time.Now().Format("2006-01-02"), Expires: params.Expires,
		Models:      models,
		IPWhitelist: params.IPWhitelist,
		MaxIps:      params.MaxIps,
		TokenQuota:  params.TokenQuota, CreditQuota: params.CreditQuota,
	})
	audit(k.service, "key.create", params.Name, "ID: "+id)
	return &CreateKeyResult{ID: id, Key: plain, Mask: core.MaskKey(plain)}, nil
}

// Update 更新密钥配置（名称 / 配额 / 白名单 / 启停）
//
// 说明：0 值视为「不修改」，因此把配额改回 0（不限）需要通过解析为 0 的显式字段——
// 这里用指针语义之外的最简办法：只在 >0 时覆盖，0 表示不限的语义由 Disabled 之外的
// 「不设置」承担。前端在表单里对「0 = 不限」有明确提示。
func (k *KeysAPI) Update(ctx context.Context, id string, params CreateKeyParams) error {
	if err := ensureWritable(k.service); err != nil {
		return err
	}
	store := k.service.Store()
	key, ok := store.GetKey(id)
	if !ok {
		return fmt.Errorf("密钥 %s 不存在", id)
	}
	if params.Name != "" {
		key.Name = params.Name
	}
	if params.Expires != "" {
		key.Expires = params.Expires
	}
	if params.Models != nil {
		key.Models = params.Models
	}
	if params.IPWhitelist != nil {
		key.IPWhitelist = params.IPWhitelist
	}
	if params.MaxIps > 0 {
		key.MaxIps = params.MaxIps
	}
	if params.TokenQuota > 0 {
		key.TokenQuota = params.TokenQuota
	}
	if params.CreditQuota > 0 {
		key.CreditQuota = params.CreditQuota
	}
	if params.Disabled != nil {
		key.Disabled = *params.Disabled
	}
	store.PutKey(key)
	audit(k.service, "key.update", key.Name, "ID: "+id)
	return nil
}

// Delete 删除密钥
func (k *KeysAPI) Delete(ctx context.Context, id string) error {
	if err := ensureWritable(k.service); err != nil {
		return err
	}
	key, _ := k.service.Store().GetKey(id)
	if !k.service.Store().DeleteKey(id) {
		return fmt.Errorf("密钥 %s 不存在", id)
	}
	audit(k.service, "key.delete", key.Name, "ID: "+id)
	return nil
}

// KeyUsage 单个密钥的真实用量统计（来自请求日志，非累计字段推算）
type KeyUsage struct {
	KeyID      string       `json:"keyId"`
	Requests   int          `json:"requests"`
	Success    int          `json:"success"`
	Errors     int          `json:"errors"`
	Tokens     int          `json:"tokens"`
	Credits    int          `json:"credits"`
	AvgLatency float64      `json:"avgLatency"`
	LastUsed   string       `json:"lastUsed"`
	ByModel    []ModelUsage `json:"byModel"`
}

// ModelUsage 按模型拆分的用量
type ModelUsage struct {
	Model    string `json:"model"`
	Requests int    `json:"requests"`
	Tokens   int    `json:"tokens"`
	Errors   int    `json:"errors"`
}

// Stats 密钥用量统计：直接聚合请求日志，与「用量 / 配额」进度条同源
func (k *KeysAPI) Stats(ctx context.Context, id string) (*KeyUsage, error) {
	key, ok := k.service.Store().GetKey(id)
	if !ok {
		return nil, fmt.Errorf("密钥 %s 不存在", id)
	}
	usage := &KeyUsage{KeyID: id, ByModel: []ModelUsage{}}
	byModel := map[string]*ModelUsage{}
	latSum := 0.0
	for _, l := range k.service.Store().ListRequestLogs() {
		if l.KeyID != id {
			continue
		}
		usage.Requests++
		usage.Tokens += l.Tokens
		if l.Status >= 400 {
			usage.Errors++
		} else {
			usage.Success++
		}
		latSum += l.Latency
		if l.Time > usage.LastUsed {
			usage.LastUsed = l.Time
		}
		m := byModel[l.Model]
		if m == nil {
			m = &ModelUsage{Model: l.Model}
			byModel[l.Model] = m
		}
		m.Requests++
		m.Tokens += l.Tokens
		if l.Status >= 400 {
			m.Errors++
		}
	}
	if usage.Requests > 0 {
		usage.AvgLatency = float64(int64(latSum/float64(usage.Requests)*100+0.5)) / 100
	}
	usage.Credits = key.CreditUsed
	for _, m := range byModel {
		usage.ByModel = append(usage.ByModel, *m)
	}
	return usage, nil
}

package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// 成长中心互动玩法补齐（对齐 WorkBuddy-Daily workbuddy_daily.py 的盲盒 / 补签卡 /
// 礼包补偿三个模块；端点与请求体形态均来自 Daily 实测）。
const (
	buddyQuotaPath        = "/v2/activity/growth/buddy/quota"
	buddyOpenPath         = "/v2/activity/growth/buddy/open"
	growthHeatmapPath     = "/v2/activity/growth/heatmap"
	makeupCardUsePath     = "/v2/activity/growth/makeup-cards/use"
	claimGiftPath         = "/billing/meter/claim-gift"
	claimCompensationPath = "/billing/meter/claim-compensation"
)

// blindBoxMaxOpens 单轮盲盒开启上限（对齐 Daily：每次 10 能量，最多连开 5 次）。
const blindBoxMaxOpens = 5

// BlindBoxResult 一轮盲盒开启结果。
type BlindBoxResult struct {
	Opened int      // 实际开启次数
	Items  []string // 开出的物品描述（名称+稀有度）
}

// BlindBoxQuota 查询盲盒可开次数（GET buddy/quota → data.affordable）。
func (c *upstreamClient) BlindBoxQuota(cred *UpstreamCred) (int, error) {
	data, err := c.growthJSON(cred, http.MethodGet, buddyQuotaPath, nil)
	if err != nil {
		return 0, err
	}
	var resp struct {
		Affordable int `json:"affordable"`
		Balance    int `json:"balance"`
	}
	if json.Unmarshal(data, &resp) != nil {
		return 0, fmt.Errorf("buddy/quota 响应解析失败")
	}
	return resp.Affordable, nil
}

// BlindBoxOpen 开一次盲盒（POST buddy/open，count=1）。
// 返回物品描述；结果字段缺失按「已开启」计，不算失败。
func (c *upstreamClient) BlindBoxOpen(cred *UpstreamCred) (string, error) {
	data, err := c.growthJSON(cred, http.MethodPost, buddyOpenPath, map[string]any{"count": 1})
	if err != nil {
		return "", err
	}
	var resp struct {
		Results []struct {
			Instance struct {
				Name   string `json:"name"`
				Rarity string `json:"rarity"`
			} `json:"instance"`
			Template struct {
				Name   string `json:"name"`
				Rarity string `json:"rarity"`
			} `json:"template"`
		} `json:"results"`
	}
	_ = json.Unmarshal(data, &resp)
	if len(resp.Results) == 0 {
		return "未知物品", nil
	}
	it := resp.Results[0]
	name, rarity := it.Instance.Name, it.Instance.Rarity
	if name == "" {
		name, rarity = it.Template.Name, it.Template.Rarity
	}
	if name == "" {
		return "未知物品", nil
	}
	if rarity != "" {
		return name + "(" + rarity + ")", nil
	}
	return name, nil
}

// BlindBoxRound 盲盒一轮：能量足够就开，最多 blindBoxMaxOpens 次（对齐 Daily）。
// 能量不足 / 接口不可用属正常态，返回空结果而非错误。
func (c *upstreamClient) BlindBoxRound(cred *UpstreamCred) (*BlindBoxResult, error) {
	affordable, err := c.BlindBoxQuota(cred)
	if err != nil {
		return nil, nil // 接口不可用：静默跳过（与抽奖的无次数同口径）
	}
	if affordable <= 0 {
		return &BlindBoxResult{}, nil
	}
	res := &BlindBoxResult{}
	n := affordable
	if n > blindBoxMaxOpens {
		n = blindBoxMaxOpens
	}
	for i := 0; i < n; i++ {
		item, err := c.BlindBoxOpen(cred)
		if err != nil {
			break // 单次失败即止，下轮再开
		}
		res.Opened++
		res.Items = append(res.Items, item)
		time.Sleep(growthEventGap) // 防风控间隔，与任务事件链同口径
	}
	return res, nil
}

// UseMakeupCardIfMissed 补签卡：查热力图找昨日漏签 → 有补签卡则自动补签（保连签）。
// 返回处理说明；无漏签 / 无卡 / 接口不可用都属正常态。
func (c *upstreamClient) UseMakeupCardIfMissed(cred *UpstreamCred) (string, error) {
	data, err := c.growthJSON(cred, http.MethodGet, growthHeatmapPath, nil)
	if err != nil {
		return "", nil // 热力图不可用：跳过
	}
	var hm struct {
		Cells []struct {
			Date  string `json:"date"`
			Score int    `json:"score"`
		} `json:"cells"`
	}
	if json.Unmarshal(data, &hm) != nil {
		return "", nil
	}
	yesterday := time.Now().In(cstZone).AddDate(0, 0, -1).Format("2006-01-02")
	missed := false
	for _, cell := range hm.Cells {
		if strings.HasPrefix(cell.Date, yesterday) && cell.Score == 0 {
			missed = true
			break
		}
	}
	if !missed {
		return "", nil
	}
	// 昨日漏签：查补签卡余额
	streakData, err := c.growthJSON(cred, http.MethodGet, streakPath, nil)
	if err != nil {
		return "昨日漏签但补签卡余额查询失败", nil
	}
	var st struct {
		MakeupCards struct {
			Balance int `json:"balance"`
		} `json:"makeup_cards"`
	}
	if json.Unmarshal(streakData, &st) != nil || st.MakeupCards.Balance <= 0 {
		return "昨日(" + yesterday + ")漏签但无补签卡", nil
	}
	if _, err := c.growthJSON(cred, http.MethodPost, makeupCardUsePath,
		map[string]any{"target_date": yesterday}); err != nil {
		return "补签 " + yesterday + " 失败: " + err.Error(), nil
	}
	return "补签 " + yesterday + " 成功，连签保住", nil
}

// creditAmountOf 把上游返回的 credit 字段（数字或数字字符串）转成整数分。
// 非数值（对象 / 布尔 / null / 非数字串）返回 false，由调用方降级为纯文本说明——
// 不猜数值，宁可只报「领取成功」也不写一个凭空的分数。
func creditAmountOf(v any) (int, bool) {
	switch t := v.(type) {
	case json.Number:
		if n, err := t.Int64(); err == nil {
			return int(n), true
		}
		if f, err := t.Float64(); err == nil {
			return int(f), true
		}
	case float64:
		return int(t), true
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(t)); err == nil {
			return n, true
		}
	}
	return 0, false
}

// ClaimGiftAndCompensation 礼包补偿：新手礼包 + 补偿领取（均幂等，无可领时上游
// 拒绝属正常态，静默跳过；成功返回 "+N分" 说明）。
// 第二个返回值是本次实际领取到的积分合计（只有拿到确切数值才累加）。
func (c *upstreamClient) ClaimGiftAndCompensation(cred *UpstreamCred) (string, int) {
	var notes []string
	credits := 0
	claim := func(path, name string) {
		req, err := http.NewRequest(http.MethodPost, c.billingBaseOf(cred.Realm)+path, strings.NewReader("{}"))
		if err != nil {
			return
		}
		c.setBillingHeaders(req, cred)
		data, err := c.doEnvelope(req)
		if err != nil {
			return // 无可领 / 幂等拒绝：正常态
		}
		// credit 可能是数字或字符串：走 UseNumber 解析，不让数值经 float64 中转失真
		var resp struct {
			Credit any `json:"credit"`
		}
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.UseNumber()
		if dec.Decode(&resp) != nil || resp.Credit == nil {
			notes = append(notes, name+" 领取成功")
			return
		}
		n, ok := creditAmountOf(resp.Credit)
		if !ok {
			notes = append(notes, fmt.Sprintf("%s +%v分", name, resp.Credit))
			return
		}
		notes = append(notes, fmt.Sprintf("%s +%d分", name, n))
		credits += n
	}
	claim(claimGiftPath, "新手礼包")
	claim(claimCompensationPath, "补偿领取")
	return strings.Join(notes, "、"), credits
}

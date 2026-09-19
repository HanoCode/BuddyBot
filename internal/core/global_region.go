package core

// 国际版（global）账号的地区注册。国际版新号必须先完成地区注册，
// 否则聊天报 14017「trial not activated」。
//
// 端点与请求体对齐 workbuddy-manager server/services/tencent.py（其来源为上游
// scripts/global_region.py 反编译结果，注释标注「实测」）：
//   GET  {billingBase}/auth/realms/copilot/overseas/user/register?userId=<uid>  查是否已注册
//   POST {billingBase}/console/login/account                                    提交地区
//   POST {billingBase}/billing/area/get-country-code                            查地区字段
//
// 地区由使用者决定（弹窗选择），这里只提供提交能力，不替用户选国家。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// internationalRegions 国际版可选地区短名单（与国际版官网一致，键为 IOS2 短码）。
var internationalRegions = map[string]string{
	"HK": "中国香港",
	"MO": "中国澳门",
	"SG": "新加坡",
	"TH": "泰国",
	"PH": "菲律宾",
	"MY": "马来西亚",
	"ID": "印度尼西亚",
}

const (
	regionStatusPath = "/auth/realms/copilot/overseas/user/register"
	regionSubmitPath = "/console/login/account"
	regionFieldsPath = "/billing/area/get-country-code"
)

// RegistrationStatus 查询国际版账号是否已完成地区注册。
// 返回 (ok, 说明)。ok=true 表示已完成（或国内版无需注册）。
func RegistrationStatus(cred *UpstreamCred) (bool, string) {
	if cred.Realm != "global" {
		return true, ""
	}
	if cred.UID == "" || cred.AccessToken == "" {
		return false, "缺少 uid 或 accessToken，无法查询注册状态"
	}
	c := newUpstreamClient()
	req, err := http.NewRequest(http.MethodGet,
		c.billingBaseOf("global")+regionStatusPath+"?userId="+cred.UID, nil)
	if err != nil {
		return false, fmt.Sprintf("查询注册状态异常: %v", err)
	}
	c.setBillingHeaders(req, cred)
	if _, err := c.doEnvelope(req); err == nil {
		return true, "地区注册已完成"
	} else if isRegionNotRegistered(err) {
		return false, "尚未完成地区注册"
	} else {
		return false, fmt.Sprintf("注册状态未知: %v", err)
	}
}

// ClaimTrialGlobal 领取一次性 trial 加油包的导出入口（上游实现见 upstream.go）。
// 仅 global 账号有效；幂等，已领过返回 (false, nil)。
func ClaimTrialGlobal(cred *UpstreamCred) (bool, error) {
	return newUpstreamClient().ClaimTrial(cred)
}

// isRegionNotRegistered 判定注册状态查询的错误是否为「未注册」：
// 上游未注册时返回 code=500（body 可能带 "region required" 文案）。
func isRegionNotRegistered(err error) bool {
	ue, ok := err.(*upstreamAPIError)
	if !ok {
		return false
	}
	lower := strings.ToLower(ue.Msg)
	return strings.Contains(lower, "500") || strings.Contains(lower, "region required")
}

// SubmitRegion 提交国际版账号的地区（regionCode 如 'HK'）。
// 提交成功返回 (true, 说明)；请求体三个字段落库口径各不相同
// （countryCode=数字码 / countryFullName=英文全名 / countryName=短码），
// 不能都填短码，否则地区归属会写歪——按 IOS2 先查回真实条目，查不到退回
// 只用短码（不至于提交非法值）。
func SubmitRegion(cred *UpstreamCred, regionCode string) (bool, string) {
	if cred.Realm != "global" {
		return false, "国内版无需地区注册"
	}
	code := strings.ToUpper(strings.TrimSpace(regionCode))
	if _, ok := internationalRegions[code]; !ok {
		return false, fmt.Sprintf("不支持的地区代码 %q", regionCode)
	}
	if cred.AccessToken == "" {
		return false, "缺少 accessToken，无法提交地区"
	}
	ios2, enName, numeric := regionFields(code)
	body := map[string]any{
		"attributes": map[string]any{
			"countryCode":     []string{numeric},
			"countryFullName": []string{enName},
			"countryName":     []string{ios2},
		},
	}
	c := newUpstreamClient()
	req, err := http.NewRequest(http.MethodPost,
		c.billingBaseOf("global")+regionSubmitPath, bytes.NewReader(mustJSON(body)))
	if err != nil {
		return false, fmt.Sprintf("提交地区异常: %v", err)
	}
	c.setBillingHeaders(req, cred)
	if _, err := c.doEnvelope(req); err != nil {
		return false, fmt.Sprintf("提交地区失败: %v", err)
	}
	return true, "地区已提交（" + code + "）"
}

// regionFields 按 IOS2 短码查地区的 (IOS2, EnName, 数字Code)。
// 地区列表来自 /billing/area/get-country-code（响应 data 是内嵌 JSON 字符串）。
// 查询失败时退回 (ios2, ios2, ios2)——提交非法值会被上游拒绝，好过瞎猜。
func regionFields(ios2 string) (string, string, string) {
	c := newUpstreamClient()
	req, err := http.NewRequest(http.MethodPost,
		c.billingBaseOf("global")+regionFieldsPath, bytes.NewReader(mustJSON(map[string]any{"filterForbidden": 1})))
	if err != nil {
		return ios2, ios2, ios2
	}
	c.setLoginHeaders(req, "global")
	data, err := c.doEnvelope(req)
	if err != nil {
		return ios2, ios2, ios2
	}
	// data 可能是内嵌 JSON 字符串（上游脚本明确处理了这一层）
	var inner any
	if err := json.Unmarshal(data, &inner); err != nil {
		return ios2, ios2, ios2
	}
	if s, ok := inner.(string); ok {
		if json.Unmarshal([]byte(s), &inner) != nil {
			return ios2, ios2, ios2
		}
	}
	obj, ok := inner.(map[string]any)
	if !ok {
		return ios2, ios2, ios2
	}
	nested, _ := obj["data"].(map[string]any)
	list, _ := nested["list"].([]any)
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if strings.EqualFold(str(m["IOS2"]), ios2) {
			return firstNonEmpty(str(m["IOS2"]), ios2),
				firstNonEmpty(str(m["EnName"]), ios2),
				firstNonEmpty(str(m["Code"]), ios2)
		}
	}
	return ios2, ios2, ios2
}

func str(v any) string {
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

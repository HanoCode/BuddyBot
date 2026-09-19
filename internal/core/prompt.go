package core

import (
	"encoding/json"
	"regexp"
	"strings"
)

// 系统提示词三模式 + WAF 指纹脱敏（对齐 workbuddy2api internal/prompt 与
// internal/upstream/sanitize.go 的核心口径）。
//
// 三模式（作用于出站 body 的 messages）：
//   - passthrough：不改动；
//   - custom：删除全部 system/developer 消息，头部插入一条配置的系统提示词；
//   - append：在开头连续 system/developer 块末尾插入网关系统提示词，其余保留。
//
// 指纹脱敏：剥离请求内容里暴露「非官方客户端」的特征（billing 头片段、cc_*
// 键值、特征短语），上游 400+11128 反探测拦截时用中性降级提示词重试。

// DegradedPromptText 上游指纹拦截降级用的中性系统提示词（对齐 prompt.Degraded）。
const DegradedPromptText = "You are a helpful assistant. Follow the user's instructions carefully."

// ApplyPromptMode 按模式改写出站 body 的 messages（原地语义，返回新 body）。
// 空 body / 坏 JSON / 空 messages 时尽量保真：custom 无 messages 时只插一条 system。
func ApplyPromptMode(mode, text string, body []byte) []byte {
	if len(body) == 0 {
		return body
	}
	var obj map[string]any
	if json.Unmarshal(body, &obj) != nil {
		return body
	}
	rawMsgs, _ := obj["messages"].([]any)
	switch mode {
	case "custom":
		if strings.TrimSpace(text) == "" {
			return body // 无提示词可替换时不动，避免清空全部系统消息
		}
		kept := make([]any, 0, len(rawMsgs))
		for _, m := range rawMsgs {
			if roleOf(m) == "system" || roleOf(m) == "developer" {
				continue
			}
			kept = append(kept, m)
		}
		obj["messages"] = append([]any{map[string]any{"role": "system", "content": text}}, kept...)
	case "append":
		if strings.TrimSpace(text) == "" {
			return body
		}
		ins := 0
		for _, m := range rawMsgs {
			r := roleOf(m)
			if r == "system" || r == "developer" {
				ins++
				continue
			}
			break
		}
		sys := map[string]any{"role": "system", "content": text}
		out := make([]any, 0, len(rawMsgs)+1)
		out = append(out, rawMsgs[:ins]...)
		out = append(out, sys)
		out = append(out, rawMsgs[ins:]...)
		obj["messages"] = out
	default: // passthrough
		return body
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return body
	}
	return out
}

func roleOf(m any) string {
	mm, ok := m.(map[string]any)
	if !ok {
		return ""
	}
	r, _ := mm["role"].(string)
	return strings.ToLower(strings.TrimSpace(r))
}

// ---- 指纹脱敏（对齐 sanitize.go） ----

// sanitizeFeatureMarkers 出站内容中的「非官方客户端」特征标记（预检，命中才做净化）。
var sanitizeFeatureMarkers = []string{
	"x-anthropic-billing-header", "cc_entrypoint=", "you are claude code",
	"main branch (", "codex cli", "github.com/anthropics/", "11128",
}

var (
	sanitizeHdrRe = regexp.MustCompile(`(?i)x-anthropic-billing-header:[^;\n]*;?`)
	sanitizeKvRe  = regexp.MustCompile(`(?i)\s*cc_[a-z0-9_]+=[^;\s]*;?`)
)

// ShouldSanitizeBody 预检 body 是否携带指纹特征（命中才值得做净化改写）。
func ShouldSanitizeBody(body []byte) bool {
	lower := strings.ToLower(string(body))
	for _, m := range sanitizeFeatureMarkers {
		if strings.Contains(lower, m) {
			return true
		}
	}
	return false
}

// SanitizeText 对单条文本做指纹净化：剥 billing 头片段与 cc_* 键值，特征短语
// 逐句改一词，裸 11128 错误码改写（对齐 sanitize.go 的两层处理）。
func SanitizeText(s string) string {
	if s == "" {
		return s
	}
	s = sanitizeHdrRe.ReplaceAllString(s, "")
	for sanitizeKvRe.MatchString(s) {
		s = sanitizeKvRe.ReplaceAllString(s, "")
	}
	rep := strings.NewReplacer(
		"for Claude", "for Claude tool",
		"Main branch", "Default branch",
		"Codex CLI", "Codex CLI tool",
		"give feedback", "provide feedback",
		"11128", "11-128",
	)
	return rep.Replace(s)
}

// SanitizeUpstreamBody 对出站 body 做指纹净化：覆盖每条 message 的 content
// 与 tool_calls 参数串。坏 JSON 原样返回。
func SanitizeUpstreamBody(body []byte) []byte {
	if !ShouldSanitizeBody(body) {
		return body
	}
	var obj map[string]any
	if json.Unmarshal(body, &obj) != nil {
		return body
	}
	if msgs, ok := obj["messages"].([]any); ok {
		for _, m := range msgs {
			mm, ok := m.(map[string]any)
			if !ok {
				continue
			}
			if c, ok := mm["content"].(string); ok {
				mm["content"] = SanitizeText(c)
			}
			if tc, ok := mm["tool_calls"].([]any); ok {
				for _, call := range tc {
					cm, ok := call.(map[string]any)
					if !ok {
						continue
					}
					if fn, ok := cm["function"].(map[string]any); ok {
						if a, ok := fn["arguments"].(string); ok {
							fn["arguments"] = SanitizeText(a)
						}
					}
				}
			}
		}
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return body
	}
	return out
}

// IsFingerprintBlock 判定上游 400+11128 反探测拦截（触发降级重试的条件）。
func IsFingerprintBlock(status int, body string) bool {
	return status == 400 && strings.Contains(body, "11128")
}

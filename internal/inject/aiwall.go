package inject

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"workbuddy-desktop/internal/core"
)

// ============================================================
// AI 生成壁纸：一句话描述 → 本机网关对话模型 → SVG 横幅 → 面板栅格化 →
// 复用既有 wall_add 流水线落盘并应用。
//
// 为什么是 SVG：网关上游是文本对话端点（images 端点明确不支持），但
// 顶尖模型出扁平矢量插画的完成度足够做壁纸底图；SVG 文本经面板
// canvas 栅格化为 webp，零新增 Go 依赖、零外部服务。
// Prompt 参考 Codex Dream Skin prompts/themes.json 的横幅约束：
// 3:1 全景、左侧留白给原生界面、禁伪界面元素与外部引用。
// ============================================================

// aiWallSystemPrompt 横幅生成系统提示词
const aiWallSystemPrompt = "You are a desktop wallpaper artist. Reply with ONE complete <svg> element and nothing else — no markdown fence, no explanation, no XML declaration. " +
	"Requirements: viewBox=\"0 0 1500 500\" with width=\"1500\" height=\"500\" attributes; 3:1 panoramic desktop banner; " +
	"flat vector illustration, refined color palette, premium and uncluttered; keep the LEFT THIRD visually simple and low-contrast (native window titles overlay there); main visual focus on the right half; " +
	"no text, no letters, no logos, no watermark, no <image> or href references, no external resources, no CSS animations, no scripts; " +
	"keep the file under 60KB by reusing shapes and gradients efficiently."

var svgExtractRe = regexp.MustCompile(`(?s)<svg[\s>].*</svg>`)

// aiWallForbidden 禁用特性（外部引用/脚本/位图内嵌），命中即拒绝
var aiWallForbidden = []*regexp.Regexp{
	regexp.MustCompile(`(?i)<image`),
	regexp.MustCompile(`(?i)href\s*=`),
	regexp.MustCompile(`(?i)<script`),
	regexp.MustCompile(`(?i)<foreignObject`),
	regexp.MustCompile(`(?i)url\(\s*['"]?\s*http`),
}

// callGatewayChat 调本机网关 /v1/chat/completions（非流式，与聊天测试同链路：
// 鉴权/配额/账号路由/记账一个不漏）。返回首个 choice 的文本内容。
func (m *Manager) callGatewayChat(model, system, user string, maxTokens int) (string, error) {
	cfg := m.svc.GetConfig()
	if !m.svc.IsRunning() {
		return "", fmt.Errorf("%s", m.tt("网关未运行：请先在桌面端启动网关", "gateway not running: start it from the desktop app first"))
	}
	if model == "" {
		return "", fmt.Errorf("%s", m.tt("未选择生成模型", "no generation model selected"))
	}
	body, _ := json.Marshal(map[string]any{
		"model": model,
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": user},
		},
		"stream":     false,
		"max_tokens": maxTokens,
	})
	url := "http://" + m.svc.GatewayBaseURL() + "/v1/chat/completions"
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	// 生成大段 SVG 耗时较长，给足超时
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("%s: %w", m.tt("请求本机网关失败", "request to local gateway failed"), err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode >= 400 {
		msg := strings.TrimSpace(string(raw))
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return "", fmt.Errorf("%s %d: %s", m.tt("网关返回", "gateway returned"), resp.StatusCode, msg)
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("%s: %w", m.tt("网关响应解析失败", "failed to parse gateway response"), err)
	}
	if out.Error != nil && out.Error.Message != "" {
		return "", fmt.Errorf("%s: %s", m.tt("网关错误", "gateway error"), out.Error.Message)
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("%s", m.tt("网关未返回内容", "gateway returned no content"))
	}
	return out.Choices[0].Message.Content, nil
}

// extractBannerSVG 从模型回复中提取横幅 SVG 并做安全/尺寸校验
func (m *Manager) extractBannerSVG(reply string) (string, error) {
	// 剥掉可能的 markdown 围栏再截取 <svg>…</svg>
	trimmed := strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(reply, "```svg", "```"), "```xml", "```"))
	trimmed = strings.ReplaceAll(trimmed, "```", "")
	svg := svgExtractRe.FindString(trimmed)
	if svg == "" {
		return "", fmt.Errorf("%s", m.tt("模型未返回 SVG（换个描述或模型再试）", "model returned no SVG (try another description or model)"))
	}
	if len(svg) > 512<<10 {
		return "", fmt.Errorf("%s（%d KB）", m.tt("SVG 过大，已拒绝", "SVG too large, rejected"), len(svg)/1024)
	}
	for _, re := range aiWallForbidden {
		if re.MatchString(svg) {
			return "", fmt.Errorf("%s", m.tt("SVG 含外部引用/脚本等被禁止的元素，已拒绝", "SVG contains forbidden elements (external refs / scripts), rejected"))
		}
	}
	return svg, nil
}

// handleWallAI 面板「AI 生成壁纸」入口：产出 SVG 后回推面板栅格化，
// 面板转 webp 后走既有 wall_add（落盘 → 自动应用 → 回推壁纸库）。
func (m *Manager) handleWallAI(desc, model string) {
	desc = strings.TrimSpace(desc)
	if desc == "" {
		return
	}
	svg, err := m.callGatewayChat(model, aiWallSystemPrompt, desc, 8000)
	if err == nil {
		svg, err = m.extractBannerSVG(svg)
	}
	m.mu.Lock()
	conn := m.conn
	m.mu.Unlock()
	if conn == nil {
		return // 页面已刷新/连接已断，结果无处投递
	}
	if err != nil {
		raw, _ := json.Marshal(err.Error())
		_, _ = conn.evaluate(`window.__wbdeskAIWall && window.__wbdeskAIWall("",`+string(raw)+`)`, 5*time.Second)
		return
	}
	core.EmitEvent(core.EventInjectStatus, map[string]any{"kind": "wall_ai", "model": model, "bytes": len(svg)})
	raw, _ := json.Marshal(svg)
	_, _ = conn.evaluate(`window.__wbdeskAIWall && window.__wbdeskAIWall(`+string(raw)+`,"")`, 30*time.Second)
}

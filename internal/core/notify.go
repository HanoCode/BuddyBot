package core

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// 任务完成推送通知：PushPlus（微信公众号）与 Bark（iOS）。
// 对齐 workbuddy2api notify.py：任务结束后按配置推送摘要，仅失败模式只在有失败时推。

// notifyRun 任务轮完成后按配置推送（网络失败只记日志，不影响任务结果）
func (s *Scheduler) notifyRun(run *TaskRun) {
	cfg := s.svc.GetConfig().Schedule.Notify
	if !cfg.Enabled {
		return
	}
	if cfg.OnlyFailures && run.Failed == 0 {
		return
	}
	title := fmt.Sprintf("WorkBuddy %s：%s %d / 失败 %d / 跳过 %d",
		taskLabel(run.Type), run.Trigger, run.Success, run.Failed, run.Skipped)
	var b strings.Builder
	fmt.Fprintf(&b, "任务 %s（%s）\n成功 %d · 失败 %d · 跳过 %d · 耗时 %.0fms\n",
		taskLabel(run.Type), run.StartedAt, run.Success, run.Failed, run.Skipped, run.Duration)
	for _, d := range run.Details {
		if d.Status == TaskFailed {
			fmt.Fprintf(&b, "· [%s] %s：%.120s\n", shortUID(d.UID), d.Status, d.Message)
		}
	}
	body := b.String()

	client := &http.Client{Timeout: 10 * time.Second}
	if tok := strings.TrimSpace(cfg.PushPlusToken); tok != "" {
		go pushPlus(client, tok, title, body)
	}
	if bu := strings.TrimSpace(cfg.BarkURL); bu != "" {
		go barkPush(client, bu, title, body)
	}
}

func taskLabel(t string) string {
	switch t {
	case "checkin":
		return "每日签到"
	case "travel":
		return "猫猫旅行"
	case "keepalive":
		return "Token 保活"
	case "activity":
		return "活跃地图"
	case "school":
		return "开学季"
	case "cat":
		return "夜猫子"
	case "growth":
		return "成长任务"
	default:
		return t
	}
}

func shortUID(uid string) string {
	if len(uid) > 8 {
		return uid[:8]
	}
	return uid
}

// pushPlus PushPlus 推送：POST JSON 到 www.pushplus.plus/send（失败记入应用错误事件）
func pushPlus(client *http.Client, token, title, content string) {
	body := fmt.Sprintf(`{"token":%q,"title":%q,"content":%q,"template":"txt"}`, token, title, content)
	resp, err := client.Post("https://www.pushplus.plus/send", "application/json", strings.NewReader(body))
	if err != nil {
		EmitEvent(EventAppError, map[string]any{"scope": "notify", "message": "PushPlus 推送失败: " + err.Error()})
		return
	}
	resp.Body.Close()
}

// barkPush Bark 推送：GET <barkURL>/<title>/<body>（URL path 编码；失败记入应用错误事件）
func barkPush(client *http.Client, base, title, content string) {
	base = strings.TrimRight(base, "/")
	u := fmt.Sprintf("%s/%s/%s", base, url.PathEscape(title), url.PathEscape(content))
	resp, err := client.Get(u)
	if err != nil {
		EmitEvent(EventAppError, map[string]any{"scope": "notify", "message": "Bark 推送失败: " + err.Error()})
		return
	}
	resp.Body.Close()
}

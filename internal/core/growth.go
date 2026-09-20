package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// 成长任务域扩展（对齐 workbuddy2api-panel internal/upstream/{tasks,report,school}.go
// 的最小可移植子集）。所有端点走 growth/billing 域，复用 setBillingHeaders 指纹。
const (
	growthTasksListPath   = "/v2/activity/growth/tasks"
	growthTasksAcceptPath = "/v2/activity/growth/tasks/accept"
	growthTaskClaimPath   = "/activity/growth/tasks/%s/claim"
	reportPath            = "/v2/report"
	schoolBasePath        = "/portal/activity/school"
)

// GrowthTask 成长任务条目（兼容 progress 嵌套/平铺两种响应形态）。
type GrowthTask struct {
	TaskCode     string `json:"task_code"`
	AcceptStatus string `json:"accept_status"` // not_accepted / accepted / completed / claimed
	Status       string `json:"status"`
	Current      int    `json:"current"`
	Target       int    `json:"target"`
	RewardCredit int    `json:"reward_credit"`
	Locked       bool   `json:"locked"`
}

// Claimable 是否可领（进度达标、未领、未锁）
func (t GrowthTask) Claimable() bool {
	return !t.Locked && t.Target > 0 && t.Current >= t.Target &&
		t.AcceptStatus != "claimed" && !strings.EqualFold(t.Status, "claimed")
}

// GrowthTasks 拉取成长任务清单（GET /v2/activity/growth/tasks）。
func (c *upstreamClient) GrowthTasks(cred *UpstreamCred) ([]GrowthTask, error) {
	data, err := c.growthJSON(cred, http.MethodGet, growthTasksListPath, nil)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Tasks []struct {
			TaskCode     string `json:"task_code"`
			AcceptStatus string `json:"accept_status"`
			Status       string `json:"status"`
			Progress     *struct {
				Current int `json:"current"`
				Target  int `json:"target"`
			} `json:"progress"`
			Current      int  `json:"current"`
			Target       int  `json:"target"`
			RewardCredit int  `json:"reward_credit"`
			Locked       bool `json:"locked"`
		} `json:"tasks"`
	}
	if json.Unmarshal(data, &resp) != nil {
		return nil, fmt.Errorf("growth/tasks 响应解析失败")
	}
	out := make([]GrowthTask, 0, len(resp.Tasks))
	for _, t := range resp.Tasks {
		gt := GrowthTask{TaskCode: t.TaskCode, AcceptStatus: t.AcceptStatus, Status: t.Status,
			RewardCredit: t.RewardCredit, Locked: t.Locked}
		if t.Progress != nil {
			gt.Current, gt.Target = t.Progress.Current, t.Progress.Target
		} else {
			gt.Current, gt.Target = t.Current, t.Target
		}
		out = append(out, gt)
	}
	return out, nil
}

// GrowthTasksAccept 批量接受任务（POST accept，body task_codes 数组）。
func (c *upstreamClient) GrowthTasksAccept(cred *UpstreamCred, codes []string) error {
	if len(codes) == 0 {
		return nil
	}
	_, err := c.growthJSON(cred, http.MethodPost, growthTasksAcceptPath,
		map[string]any{"task_codes": codes})
	return err
}

// GrowthTaskClaim 领取单个任务奖励（Web 域指纹：Origin/Referer + x-client-platform）。
func (c *upstreamClient) GrowthTaskClaim(cred *UpstreamCred, code string) error {
	req, err := http.NewRequest(http.MethodPost,
		c.chatBase(cred.Realm)+fmt.Sprintf(growthTaskClaimPath, code), nil)
	if err != nil {
		return err
	}
	c.setBillingHeaders(req, cred)
	origin := originRefererCN
	if cred.Realm == "global" {
		origin = originRefererGlobal
	}
	req.Header.Set("Origin", origin)
	req.Header.Set("Referer", origin+"/")
	req.Header.Set("X-Client-Platform", "web")
	_, err = c.doEnvelope(req)
	return err
}

// ReportEvents 上报活跃行为事件（POST {billingBase}/v2/report，数组体）。
// 事件必须带 userId，否则上游 200 但静默丢弃。
func (c *upstreamClient) ReportEvents(cred *UpstreamCred, events []map[string]any) error {
	req, err := http.NewRequest(http.MethodPost, c.billingBaseOf(cred.Realm)+reportPath,
		bytes.NewReader(mustJSON(events)))
	if err != nil {
		return err
	}
	c.setBillingHeaders(req, cred)
	_, err = c.doEnvelope(req)
	return err
}

// ChatReportEvents 构造 n 条 chat_request_send 活跃事件（对齐 wb_tasks.py 字段口径：
// 每条事件必须携带 timestamp（毫秒）与 reportDelay，否则上游 10001 拒收）。
func ChatReportEvents(uid string, n int) []map[string]any {
	now := time.Now().UnixMilli()
	conv := fmt.Sprintf("wbd-%d", now)
	events := make([]map[string]any, 0, n)
	for i := 0; i < n; i++ {
		events = append(events, map[string]any{
			"eventCode":      "chat_request_send",
			"timestamp":      now + int64(i)*1500,
			"reportDelay":    0,
			"userId":         uid,
			"conversationId": conv,
			"requestId":      fmt.Sprintf("%s-r%d", conv, i),
			"mode":           "craft",
			"inputLength":    12,
			"requestModelId": "deepseek-v4-flash",
			"isPlan":         false,
			"agentName":      "default",
			"agentType":      "conversation",
		})
	}
	return events
}

// ---- 成长任务事件链（对齐 workbuddy2api-hub wb_tasks.py 的任务规格与事件构造） ----

// growthTaskSpec 可伪造事件点亮的任务规格（kind 对应 buildGrowthTaskEvent 的分支）。
// 不在表内的任务码没有已验证的事件链，保持"只领取不伪造"，避免无效上报。
type growthTaskSpec struct {
	kind   string
	name   string
	target int
}

var growthTaskSpecs = map[string]growthTaskSpec{
	"create_canvas":     {"canvas", "创建设计任务", 1},
	"template_5":        {"template", "模板创建任务", 5},
	"expert_5":          {"expert", "使用专家助手", 5},
	"Expert_team_use_3": {"team", "使用专家团队", 3},
	"skill_1":           {"skill", "体验技能", 1},
	"automation_1":      {"automation", "创建自动化任务", 1},
	"playbook_prompt":   {"playbook", "灵感案例使用", 1},
	"Expert_lighthouse": {"lighthouse", "轻量云专家使用", 1},
	"chat_5":            {"chat", "发起 5 次对话", 5},
	"Model_chat_GLM5.2": {"glmchat", "体验 GLM-5.2", 1},
	"Hp_Appearance":     {"skin", "应用主题外观", 1},
}

// desktopOnlyTasks 上游只认桌面端真实行为信号的任务（伪造事件不推进进度，
// 诚实跳过；DeepSeek 多轮实测伪造 /v2/report 会被忽略或落到 heartbeat）。
var desktopOnlyTasks = map[string]string{
	"RichMeow_Chat": "在桌面端发起 1 次对话",
	"Library_read":  "在桌面端打开「资料库」并读完介绍文档",
	"Buddy_App":     "在桌面端「发现应用」进入任意一个 Buddy 应用",
	"Buddy_App_QQ":  "在桌面端「发现应用」进入「企鹅教师助手」",
}

// expertIDPool / teamIDPool 专家与团队事件 id 池。
// 上游按 (eventCode, id) 去重——重复发同一个 id 进度永远不动，必须每次轮换
// 互不相同的 id。以下 id 已在参考实现 2026-09 实测中验证可推进任务进度。
var expertIDPool = [][2]string{
	{"ex_PZw8Gu81HfN4", "运维工程师"}, {"ex_ROsDtJbzADFV", "产品经理"},
	{"ex_SMUnl0nJbPix", "UI设计师"}, {"ex_ZTR062oVBOCW", "数据分析师"},
	{"ex_a3sSSFBy8qaC", "后端架构师"}, {"ex_aG1kvKbq8lPx", "文案策划"},
	{"ex_al1vxtUOYQ10", "测试专家"}, {"ex_cZfiyuET9UQP", "安全顾问"},
	{"ex_eggOvQuVP0hq", "算法工程师"}, {"ex_hSwsQjkSKnkX", "前端工程师"},
	{"ex_mMbwwmFA9n9P", "项目管理专家"}, {"ex_uAQE5POfk7Zh", "增长运营专家"},
	{"ex_uZzSAScSy7FZ", "行业研究员"}, {"ex_LHywGrZOtG7G", "数据分析师"},
	{"ex_NX5C8GBciVed", "测试架构师"}, {"ex_DdCsaoq4AtcO", "云端运维专家"},
	{"ex_KzqKQguubrNQ", "内容创作专家"}, {"ex_2cvvUZQhDyeJ", "腾讯轻量云专家"},
}

var teamIDPool = [][2]string{
	{"CloudOpsTeam", "运维专家团队"}, {"CloudContentTeam", "内容专家团队"},
	{"CloudDevTeam", "研发专家团队"}, {"ProductStrategyTeam", "产品战略团队"},
	{"MarketingCampaignTeam", "营销活动团队"}, {"SalesBattleTeam", "销售作战团队"},
	{"DesignEngineTeam", "设计引擎团队"}, {"HrOperationsTeam", "人力运营团队"},
}

// buildGrowthTaskEvent 构造指定任务类型的一条规范事件（对齐 wb_tasks.build_event）。
// expertID 非空时（expert/team 类）作为事件的 id/name——必须逐次轮换互不相同。
func buildGrowthTaskEvent(uid, kind string, idx int, expertID [2]string) map[string]any {
	now := time.Now().UnixMilli()
	cid := fmt.Sprintf("wb-task-%d-%d", now, idx)
	rid := cid + "-req"

	switch kind {
	case "canvas":
		return map[string]any{"eventCode": "wbx_design_canvas_task_create", "timestamp": now,
			"reportDelay": 0, "conversationId": cid, "requestId": rid,
			"source": "summon_keyword", "isCustomModel": false, "name": "",
			"inputLength": 12, "id": fmt.Sprintf("wbx-canvas-%d", now), "cost": 0,
			"isSuccessful": true, "userId": uid}
	case "template":
		return map[string]any{"eventCode": "agent_task_created_with_template", "timestamp": now,
			"reportDelay": 0, "isCustomModel": true, "id": fmt.Sprint(idx),
			"name": "幻灯片", "requestId": rid, "conversationId": cid, "userId": uid}
	case "expert", "team", "lighthouse":
		etype := "agent"
		if kind == "team" {
			etype = "team"
		}
		id, name := expertID[0], expertID[1]
		if id == "" { // 兜底：池缺失时用固定值（至少 lighthouse 有实测锚点）
			id, name = "ex_2cvvUZQhDyeJ", "腾讯轻量云专家"
		}
		return map[string]any{"eventCode": "expert_actual_use", "timestamp": now, "reportDelay": 0,
			"mode": "CLOUD", "id": id, "name": name, "expertTitle": name,
			"type": "02-Engineering", "expertType": etype, "source": "builtin",
			"version": "1.0.2", "cost": 0, "characterCount": 12, "conversationId": cid,
			"requestId": rid, "messageId": rid, "requestModelId": "deepseek-v4-flash",
			"requestModelName": "DeepSeek V4 Flash", "userId": uid}
	case "skill":
		return map[string]any{"eventCode": "skill_info", "timestamp": now, "reportDelay": 0,
			"skillId": "skill_2096525080079265792", "name": "pptx", "userId": uid}
	case "automation":
		return map[string]any{"eventCode": "automated_task_create_suc", "timestamp": now, "reportDelay": 0,
			"name": "每周工作整理", "type": "cron", "source": "manually",
			"modelId": "deepseek-v4-flash", "modelIsThinking": false,
			"conversationId": cid, "requestId": rid,
			"schedule": map[string]any{"type": "recurring", "rrule": "FREQ=WEEKLY;BYDAY=FR;BYHOUR=9;BYMINUTE=0"},
			"prompt":   "每周五自动整理本周工作", "userId": uid}
	case "playbook":
		return map[string]any{"eventCode": "playbook_prompt_send", "timestamp": now, "reportDelay": 0,
			"id": "worker-ledger-freedom-dashboard", "name": "打工人小账本",
			"type": "other", "promptLength": 10, "isOfficial": 1,
			"source": "discover", "conversationId": cid, "requestId": rid, "userId": uid}
	case "skin":
		return map[string]any{"eventCode": "appearance_skin_apply", "timestamp": now, "reportDelay": 0,
			"action": "apply", "source": "settings_close", "id": "theme-tkmw7j",
			"vipLevel": "free", "series": "craft", "type": "unknown",
			"name": "和平精英激战金秋", "userId": uid}
	case "glmchat":
		return map[string]any{"eventCode": "chat_request_send", "timestamp": now, "reportDelay": 0,
			"mode": "craft", "conversationId": cid, "requestId": rid,
			"inputLength": 12, "requestModelId": "glm-5.2", "requestModelName": "GLM-5.2",
			"isPlan": false, "agentName": "default", "agentType": "conversation",
			"userId": uid}
	default: // chat
		return map[string]any{"eventCode": "chat_request_send", "timestamp": now, "reportDelay": 0,
			"mode": "craft", "conversationId": cid, "requestId": rid,
			"inputLength": 12, "requestModelId": "deepseek-v4-flash",
			"requestModelName": "DeepSeek V4 Flash",
			"isPlan":           false, "agentName": "default", "agentType": "conversation",
			"userId": uid}
	}
}

// ---- 调度器扩展任务（活跃地图 / 开学季 / 夜猫子 / 成长任务中心） ----

// activityReportCount 活跃地图每账号上报事件数（对齐 ActivityReportCount 默认 5）。
const activityReportCount = 5

// doActivity 活跃地图：上报活跃事件并回读连登确认（CN 账号）。
func (s *Scheduler) doActivity(a Account, cred *UpstreamCred) TaskRunDetail {
	if a.Realm == "global" {
		return TaskRunDetail{UID: a.UID, Status: TaskSkipped, Message: "国际版账号无活跃地图体系，跳过"}
	}
	if err := s.up.ReportEvents(cred, ChatReportEvents(a.UID, activityReportCount)); err != nil {
		return TaskRunDetail{UID: a.UID, Status: TaskFailed, Message: "活跃事件上报失败: " + err.Error()}
	}
	msg := fmt.Sprintf("已上报 %d 条活跃事件", activityReportCount)
	note, credits := s.claimGrowthRewards(cred)
	if note != "" {
		msg += "；" + note
	}
	if credits > 0 {
		msg += fmt.Sprintf("（本次领取 %d 分）", credits)
	}
	return TaskRunDetail{UID: a.UID, Status: TaskSuccess, Message: msg, Credits: credits}
}

// doSchool 开学季活动（仅 CN）：查任务 → 补浏览/分享 → 领奖 → 有抽奖机会则抽。
// 上游字段形态未在参考实现之外公开，按真实信封解析，失败如实上报。
func (s *Scheduler) doSchool(a Account, cred *UpstreamCred) TaskRunDetail {
	if a.Realm == "global" {
		return TaskRunDetail{UID: a.UID, Status: TaskSkipped, Message: "国际版账号无开学季活动，跳过"}
	}
	get := func(path string, out any) error {
		data, err := s.up.growthJSON(cred, http.MethodGet, schoolBasePath+path, nil)
		if err != nil {
			return err
		}
		if out != nil && len(data) > 0 {
			return json.Unmarshal(data, out)
		}
		return nil
	}
	post := func(path string, body any) error {
		_, err := s.up.growthJSON(cred, http.MethodPost, schoolBasePath+path, body)
		return err
	}
	var tasks struct {
		Tasks []struct {
			Code     string `json:"code"`
			TaskCode string `json:"task_code"`
			Status   string `json:"status"`
		} `json:"tasks"`
	}
	if err := get("/tasks", &tasks); err != nil {
		return TaskRunDetail{UID: a.UID, Status: TaskSkipped, Message: "开学季任务不可用: " + err.Error()}
	}
	done, claimed := 0, 0
	for _, t := range tasks.Tasks {
		code := t.Code
		if code == "" {
			code = t.TaskCode
		}
		if code == "" || strings.EqualFold(t.Status, "claimed") {
			continue
		}
		_ = post("/tasks/"+code+"/viewed", map[string]any{})
		if strings.Contains(strings.ToLower(t.Status), "share") {
			_ = post("/tasks/share-complete", map[string]any{"channel": "wechat"})
		}
		if err := post("/tasks/"+code+"/claim", nil); err == nil {
			claimed++
		}
		done++
	}
	msg := fmt.Sprintf("开学季任务处理 %d 个，领取 %d 个", done, claimed)
	// 抽奖：查 config 里的 chance.balance，>0 就抽完
	var cfg struct {
		Chance struct {
			Balance int `json:"balance"`
		} `json:"chance"`
	}
	if err := get("/config", &cfg); err == nil && cfg.Chance.Balance > 0 {
		draws := 0
		for i := 0; i < cfg.Chance.Balance && i < maxLotteryDraws; i++ {
			if err := post("/wheel/draw", map[string]any{"draw_uuid": newClientToken()}); err != nil {
				break
			}
			draws++
		}
		if draws > 0 {
			msg += fmt.Sprintf("；抽奖 %d 次", draws)
		}
	}
	if done == 0 && claimed == 0 {
		return TaskRunDetail{UID: a.UID, Status: TaskSkipped, Message: msg + "（无待处理任务）"}
	}
	return TaskRunDetail{UID: a.UID, Status: TaskSuccess, Message: msg}
}

// nightCatTaskCode 夜猫子任务码；nightWindow 23:00–08:00 CST。
const nightCatTaskCode = "black_cat"

func inNightWindow(now time.Time) bool {
	h := now.In(cstZone).Hour()
	return h >= 23 || h < 8
}

// doCat 夜猫子（仅 CN，23:00–08:00 CST）：接受任务 → 3 次 night 模式对话 → 领奖。
func (s *Scheduler) doCat(a Account, cred *UpstreamCred) TaskRunDetail {
	if a.Realm == "global" {
		return TaskRunDetail{UID: a.UID, Status: TaskSkipped, Message: "国际版账号无夜猫子任务，跳过"}
	}
	if !inNightWindow(time.Now()) {
		return TaskRunDetail{UID: a.UID, Status: TaskSkipped, Message: "不在夜猫子窗口（23:00–08:00），跳过"}
	}
	tasks, err := s.up.GrowthTasks(cred)
	if err != nil {
		return TaskRunDetail{UID: a.UID, Status: TaskFailed, Message: "查询成长任务失败: " + err.Error()}
	}
	var target *GrowthTask
	for i := range tasks {
		if tasks[i].TaskCode == nightCatTaskCode {
			target = &tasks[i]
			break
		}
	}
	if target == nil {
		return TaskRunDetail{UID: a.UID, Status: TaskSkipped, Message: "无夜猫子任务（活动未开启）"}
	}
	if target.AcceptStatus == "not_accepted" {
		if err := s.up.GrowthTasksAccept(cred, []string{nightCatTaskCode}); err != nil {
			return TaskRunDetail{UID: a.UID, Status: TaskFailed, Message: "接受夜猫子任务失败: " + err.Error()}
		}
	}
	if target.Claimable() {
		if err := s.up.GrowthTaskClaim(cred, nightCatTaskCode); err != nil {
			return TaskRunDetail{UID: a.UID, Status: TaskFailed, Message: "领取夜猫子奖励失败: " + err.Error()}
		}
		return TaskRunDetail{UID: a.UID, Status: TaskSuccess, Message: "夜猫子奖励领取成功"}
	}
	nightChats := 3
	fail := 0
	for i := 0; i < nightChats; i++ {
		if err := s.up.nightChat(cred); err != nil {
			fail++
		}
		time.Sleep(1200 * time.Millisecond)
	}
	if fail == nightChats {
		return TaskRunDetail{UID: a.UID, Status: TaskFailed, Message: "night 对话全部失败，任务未推进"}
	}
	msg := fmt.Sprintf("night 对话完成 %d/%d", nightChats-fail, nightChats)
	if err := s.up.GrowthTaskClaim(cred, nightCatTaskCode); err == nil {
		msg += "；奖励已领取"
	} else {
		msg += "；奖励暂不可领（计分异步，下轮确认）"
	}
	return TaskRunDetail{UID: a.UID, Status: TaskSuccess, Message: msg}
}

// nightChat 发一次 night 模式对话（对齐 task_runner.py：glm-5.2 + mode:"night"）。
func (c *upstreamClient) nightChat(cred *UpstreamCred) error {
	body := map[string]any{
		"model":          "glm-5.2",
		"messages":       []map[string]any{{"role": "user", "content": "hi"}},
		"stream":         true,
		"stream_options": map[string]any{"include_usage": true},
		"mode":           "night",
	}
	injectThinking(body, "")
	req, err := http.NewRequest(http.MethodPost, c.chatBase(cred.Realm)+upstreamChatPath, bytes.NewReader(mustJSON(body)))
	if err != nil {
		return err
	}
	c.setChatHeaders(req, cred)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	resp, err := c.http.Do(req.WithContext(ctx))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return fmt.Errorf("night 对话被上游拒绝（%d）%s", resp.StatusCode, truncateBody(raw))
	}
	return nil
}

// RunGrowthTaskCenter 任务中心：全账号扫描成长任务 → 接受 → 逐任务点亮（事件链）→
// 轮询进度 → 达成后领取。
func (s *Scheduler) RunGrowthTaskCenter(trigger string) TaskRun {
	return s.runForAccounts("growth", trigger, nil, s.growthForAccount)
}

// growthEventGap 事件链相邻两次上报的防风控间隔（参考实现 gap 下限 1.0s）。
const growthEventGap = 1050 * time.Millisecond

// growthClaimPoll 成就落账轮询参数：上报到进度可见存在秒级延迟，立刻领奖会吃
// 400 "task not completed"，表现为全部 +0（hub PR #21 竞态修复）。
const (
	growthClaimPollTimes    = 5
	growthClaimPollInterval = 4 * time.Second
)

// growthForAccount 单账号成长任务闭环（对齐 wb_tasks.run_growth_tasks）：
// 1) 批量接受未接受任务；2) 逐任务按规格构造事件链点亮（专家/团队 id 轮换）；
// 3) 每个任务上报后轮询进度、达成再领奖；4) 已达标的顺手领取。
func (s *Scheduler) growthForAccount(a Account) TaskRunDetail {
	if a.Realm == "global" {
		return TaskRunDetail{UID: a.UID, Status: TaskSkipped, Message: "国际版账号无成长任务体系，跳过"}
	}
	cred, err := LoadUpstreamCred(s.svc.AuthDir(), a.Credential)
	if err != nil {
		return TaskRunDetail{UID: a.UID, Status: TaskFailed, Message: "凭证读取失败: " + err.Error()}
	}
	tasks, err := s.up.GrowthTasks(cred)
	if err != nil {
		return TaskRunDetail{UID: a.UID, Status: TaskFailed, Message: "查询成长任务失败: " + err.Error()}
	}
	// 1) 批量接受未接受且未锁定的任务（每批 20 个，对齐 panel）
	var accept []string
	for _, t := range tasks {
		if !t.Locked && t.AcceptStatus == "not_accepted" {
			accept = append(accept, t.TaskCode)
		}
	}
	for start := 0; start < len(accept); start += 20 {
		end := start + 20
		if end > len(accept) {
			end = len(accept)
		}
		if err := s.up.GrowthTasksAccept(cred, accept[start:end]); err != nil {
			return TaskRunDetail{UID: a.UID, Status: TaskFailed, Message: "批量接受任务失败: " + err.Error()}
		}
		time.Sleep(1050 * time.Millisecond)
	}
	if len(accept) > 0 { // 接取后重拉一次，拿到最新进度基线
		time.Sleep(1050 * time.Millisecond)
		if fresh, ferr := s.up.GrowthTasks(cred); ferr == nil {
			tasks = fresh
		}
	}

	// 2) 逐任务处理
	accepted := len(accept)
	claimed, lit := 0, 0
	earned := 0 // 本次领取到的积分合计（每个成功领奖按 reward_credit 累加）
	var notes []string
	for _, t := range tasks {
		spec, ok := growthTaskSpecs[t.TaskCode]
		if !ok {
			continue // 无已验证事件链的任务：只领取不伪造
		}
		if t.AcceptStatus == "claimed" || strings.EqualFold(t.Status, "claimed") {
			continue
		}
		// 只认桌面端真实行为的任务：诚实跳过（伪造事件进度永远 0）
		if reason, only := desktopOnlyTasks[t.TaskCode]; only {
			notes = append(notes, spec.name+" 需真实操作（"+reason+"），跳过")
			continue
		}
		// 夜猫子任务只认 23:00–08:00 上报（由独立的 cat 调度处理），白天跳过
		if t.TaskCode == nightCatTaskCode && !inNightWindow(time.Now()) {
			continue
		}
		// 已达标 → 直接领奖
		if t.Claimable() {
			if err := s.up.GrowthTaskClaim(cred, t.TaskCode); err == nil {
				claimed++
				earned += t.RewardCredit
				notes = append(notes, fmt.Sprintf("%s 领奖成功(+%d分)", spec.name, t.RewardCredit))
			} else {
				notes = append(notes, spec.name+" 领奖失败: "+err.Error())
			}
			time.Sleep(growthEventGap)
			continue
		}
		// 需点亮：上报 (target - current) 条事件；专家/团队类必须逐次轮换 id
		need := spec.target - t.Current
		if need <= 0 {
			continue
		}
		if need > spec.target {
			need = spec.target
		}
		var pool [][2]string
		if spec.kind == "expert" {
			pool = expertIDPool
		} else if spec.kind == "team" {
			pool = teamIDPool
		}
		reportOK := true
		for i := 0; i < need; i++ {
			var expertID [2]string
			if len(pool) > 0 {
				expertID = pool[(t.Current+i)%len(pool)] // 按已有进度偏移，避免跨轮重复
			}
			ev := buildGrowthTaskEvent(a.UID, spec.kind, i, expertID)
			if err := s.up.ReportEvents(cred, []map[string]any{ev}); err != nil {
				reportOK = false
			}
			if i < need-1 {
				time.Sleep(growthEventGap)
			}
		}
		if !reportOK {
			notes = append(notes, spec.name+" 部分事件上报失败，继续尝试领奖")
		}
		lit++
		// 3) 竞态修复：等上游把进度落账再领奖（计分异步，立刻领必吃 400）
		reached := false
		for i := 0; i < growthClaimPollTimes; i++ {
			time.Sleep(growthClaimPollInterval)
			fresh, err := s.up.GrowthTasks(cred)
			if err != nil {
				continue
			}
			for _, ft := range fresh {
				if ft.TaskCode != t.TaskCode {
					continue
				}
				if ft.Current >= ft.Target || ft.Claimable() {
					reached = true
				}
			}
			if reached {
				break
			}
		}
		if !reached {
			notes = append(notes, spec.name+" 已上报但进度未达成，领奖顺延下轮")
			continue
		}
		if err := s.up.GrowthTaskClaim(cred, t.TaskCode); err == nil {
			claimed++
			earned += t.RewardCredit
			notes = append(notes, fmt.Sprintf("%s 点亮并领奖成功(+%d分)", spec.name, t.RewardCredit))
		} else {
			notes = append(notes, spec.name+" 进度已达成但领奖失败: "+err.Error())
		}
		time.Sleep(growthEventGap)
	}

	msg := fmt.Sprintf("接受 %d 个任务，点亮 %d 个，领取 %d 个奖励", accepted, lit, claimed)
	if earned > 0 {
		msg += fmt.Sprintf("，共领取 %d 分", earned)
	}
	if len(notes) > 0 {
		msg += "；" + strings.Join(notes, "；")
	}
	if accepted == 0 && lit == 0 && claimed == 0 && len(notes) == 0 {
		return TaskRunDetail{UID: a.UID, Status: TaskSkipped, Message: "成长任务无可推进项"}
	}
	return TaskRunDetail{UID: a.UID, Status: TaskSuccess, Message: msg, Credits: earned}
}

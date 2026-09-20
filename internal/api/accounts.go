package api

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"workbuddy-desktop/internal/core"
)

// AccountsAPI 账号管理API
type AccountsAPI struct {
	service *core.Service
	mu      sync.Mutex
	logins  map[string]*oauthSession // region → 进行中的设备授权会话
}

// NewAccountsAPI 创建账号API
func NewAccountsAPI(service *core.Service) *AccountsAPI {
	return &AccountsAPI{service: service, logins: map[string]*oauthSession{}}
}

// oauthSession 一次进行中的设备授权会话（StartOAuth 签发的 state）。
type oauthSession struct {
	Realm        string
	State        string
	GlobalRegion string // 国际版地区代码（如 HK）；国内版为空
	AuthURL      string // 本次签发的官方授权页地址（OpenLoginWindow 只允许打开它）
	StartedAt    time.Time
}

// oauthStateTTL 授权链接有效期。上游签发的 state 超时后轮询永远 pending，
// 这里按 15 分钟（官方授权页的有效期）主动判过期，让前端能提示重取。
const oauthStateTTL = 15 * time.Minute

// AccountListResult 账号列表结果（服务端分页）
type AccountListResult struct {
	Items       []core.Account `json:"items"`
	Total       int            `json:"total"`
	Page        int            `json:"page"`
	PageSize    int            `json:"pageSize"`
	OnlineCount int            `json:"onlineCount"`
	AuthDir     string         `json:"authDir"`
}

// AccountQuery 账号查询参数
type AccountQuery struct {
	Keyword  string `json:"keyword,omitempty"`
	Status   string `json:"status,omitempty"`
	Realm    string `json:"realm,omitempty"`
	Page     int    `json:"page,omitempty"`
	PageSize int    `json:"pageSize,omitempty"`
	// Sort 排序方式：credit_expiry = 积分到期优先（有到期信息的在前，
	// 按最早到期日升序；无到期信息的按余额升序排后），其余值 = 默认顺序。
	Sort string `json:"sort,omitempty"`
}

// List 账号列表：唯一来源是 auth_dir 下的真实凭证文件，服务端过滤 + 分页。
func (a *AccountsAPI) List(ctx context.Context, q AccountQuery) (*AccountListResult, error) {
	all := a.service.Accounts()
	online := 0
	for _, x := range all {
		if x.Status == "online" {
			online++
		}
	}

	kw := strings.ToLower(strings.TrimSpace(q.Keyword))
	filtered := make([]core.Account, 0, len(all))
	for _, x := range all {
		if q.Status != "" && x.Status != q.Status {
			continue
		}
		if q.Realm != "" && x.Realm != q.Realm {
			continue
		}
		if kw != "" {
			hit := false
			for _, f := range []string{x.UID, x.Nickname, x.Credential, x.EnterpriseID, x.Domain, x.Realm} {
				if strings.Contains(strings.ToLower(f), kw) {
					hit = true
					break
				}
			}
			if !hit {
				continue
			}
		}
		filtered = append(filtered, x)
	}

	if q.Sort == "credit_expiry" {
		sort.SliceStable(filtered, func(i, j int) bool {
			a, b := filtered[i], filtered[j]
			// 有到期信息的账号排前（最早到期日升序）；都没有的按剩余余额升序
			if a.CreditsExpireDay != "" && b.CreditsExpireDay != "" {
				return a.CreditsExpireDay < b.CreditsExpireDay
			}
			if a.CreditsExpireDay != "" {
				return true
			}
			if b.CreditsExpireDay != "" {
				return false
			}
			if a.CreditsKnown && b.CreditsKnown {
				return a.Credits < b.Credits
			}
			return false
		})
	}

	page, size := q.Page, q.PageSize
	if page <= 0 {
		page = 1
	}
	if size <= 0 {
		size = 20
	}
	start := (page - 1) * size
	if start > len(filtered) {
		start = len(filtered)
	}
	end := start + size
	if end > len(filtered) {
		end = len(filtered)
	}

	return &AccountListResult{
		Items:       filtered[start:end],
		Total:       len(filtered),
		Page:        page,
		PageSize:    size,
		OnlineCount: online,
		AuthDir:     a.service.AuthDir(),
	}, nil
}

// Detail 账号详情（凭证快照 + 本地运行态）
func (a *AccountsAPI) Detail(ctx context.Context, uid string) (*core.Account, error) {
	acc, ok := a.service.FindAccount(uid)
	if !ok {
		return nil, fmt.Errorf("账号 %s 不存在", uid)
	}
	return &acc, nil
}

// Checkin 手动触发签到任务：结果如实反映本应用实际做到的事（未接入上游即 skipped）
func (a *AccountsAPI) Checkin(ctx context.Context, uids []string) (*core.TaskRun, error) {
	run := a.service.Scheduler().RunFor(core.TaskCheckin, "manual", uids)
	return &run, nil
}

// RunTask 手动触发任意任务（checkin / travel / keepalive / activity / school / cat / growth）
func (a *AccountsAPI) RunTask(ctx context.Context, taskType string, uids []string) (*core.TaskRun, error) {
	switch taskType {
	case core.TaskCheckin, core.TaskTravel, core.TaskKeepalive,
		core.TaskActivity, core.TaskSchool, core.TaskCat:
		// 常规任务走调度器
	case core.TaskGrowth:
		run := a.service.Scheduler().RunGrowthTaskCenter("manual")
		return &run, nil
	default:
		return nil, fmt.Errorf("未知任务类型: %s", taskType)
	}
	run := a.service.Scheduler().RunFor(taskType, "manual", uids)
	return &run, nil
}

// RunTaskAsync 异步触发任务：提交后立即返回，不阻塞前端；任务在后台执行，
// 完成时通过 task:completed 事件广播（运行记录见调度器 Status 的 lastRun）。
// 同类任务已有执行未结束时调度器会自动跳过本次（结果里如实标注 skipped）。
func (a *AccountsAPI) RunTaskAsync(ctx context.Context, taskType string, uids []string) error {
	switch taskType {
	case core.TaskCheckin, core.TaskTravel, core.TaskKeepalive,
		core.TaskActivity, core.TaskSchool, core.TaskCat:
		// 常规任务走调度器
	case core.TaskGrowth:
		go a.service.Scheduler().RunGrowthTaskCenter("manual")
		return nil
	default:
		return fmt.Errorf("未知任务类型: %s", taskType)
	}
	go a.service.Scheduler().RunFor(taskType, "manual", uids)
	return nil
}

// SetDisabled 设置账号禁用/启用（禁用后不参与网关选号与定时任务）
func (a *AccountsAPI) SetDisabled(ctx context.Context, uid string, disabled bool) error {
	if err := ensureWritable(a.service); err != nil {
		return err
	}
	if _, ok := a.service.FindAccount(uid); !ok {
		return fmt.Errorf("账号 %s 不存在", uid)
	}
	a.service.Store().MutateAccountState(uid, func(st *core.AccountState) { st.Disabled = disabled })
	action := "account.enable"
	if disabled {
		action = "account.disable"
	}
	audit(a.service, action, uid, "")
	core.EmitEvent(core.EventAccountStatus, map[string]any{"uid": uid, "status": "disabled"})
	return nil
}

// TaskStatus 调度器状态（含各任务下次执行时间与最近运行记录）
func (a *AccountsAPI) TaskStatus(ctx context.Context) (*core.SchedulerStatus, error) {
	st := a.service.Scheduler().Status()
	return &st, nil
}

// RefreshCredit 立即查询指定账号的真实积分余额（按需触发，不等 keepalive 周期），
// 返回刷新后的账号视图。
func (a *AccountsAPI) RefreshCredit(ctx context.Context, uid string) (*core.Account, error) {
	acc, err := a.service.Scheduler().RefreshBalanceNow(uid)
	if err != nil {
		return nil, err
	}
	return &acc, nil
}

// Delete 删除账号：移除其凭证文件（auth_dir 是该账号的唯一真实来源）
func (a *AccountsAPI) Delete(ctx context.Context, uid string) error {
	if err := ensureWritable(a.service); err != nil {
		return err
	}
	file, err := core.DeleteCredential(a.service.GetConfig().AuthDir, uid)
	if err != nil {
		return err
	}
	a.service.Store().PurgeAccountState(uid)
	audit(a.service, "account.delete", uid, "凭证文件: "+file)
	core.EmitEvent(core.EventAccountStatus, map[string]any{"uid": uid, "status": "deleted", "credential": file})
	return nil
}

// OAuthHint 授权引导
type OAuthHint struct {
	URL     string `json:"url"`
	AuthDir string `json:"authDir"`
	Note    string `json:"note"`
}

// StartOAuth 发起设备授权：请求上游签发 state，返回官方授权页地址（authUrl）。
// 用户在浏览器完成登录后，前端轮询 PollOAuth 检测结果并自动落盘凭证。
func (a *AccountsAPI) StartOAuth(ctx context.Context, region string) (*OAuthHint, error) {
	realm := region
	if realm != "cn" && realm != "global" {
		realm = "cn"
	}
	state, authURL, err := core.OAuthStart(realm)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	a.logins[region] = &oauthSession{Realm: realm, State: state, AuthURL: authURL, StartedAt: time.Now()}
	a.mu.Unlock()
	return &OAuthHint{
		URL:     authURL,
		AuthDir: a.service.AuthDir(),
		Note:    "用手机扫弹窗内二维码（或在浏览器打开链接）完成官方登录后，凭证会自动落盘并加入账号池。",
	}, nil
}

// OAuthPollResult 前端轮询结果。
// status：pending=等待登录；success=已完成；expired=授权链接过期；
// invalid=会话丢失（应用重启过 / 没有进行中的授权）。
type OAuthPollResult struct {
	Status   string `json:"status"`
	UID      string `json:"uid,omitempty"`
	Nickname string `json:"nickname,omitempty"`
	Realm    string `json:"realm,omitempty"`
	Updated  bool   `json:"updated,omitempty"` // true=该账号已存在，本次是重新授权
	Note     string `json:"note,omitempty"`    // 附带说明（国际版地区注册 / trial 结果）
}

// PollOAuth 轮询授权状态；完成时把凭证写入 auth_dir 并广播账号事件。
// globalRegion：国际版账号的地区代码（如 HK）。国际版新号必须先完成地区注册，
// 否则聊天报 14017；地区由用户在弹窗里选，这里只负责提交，不替他决定。
// 落盘失败时不删除会话（issue #26 同款教训）：state 上游仍有效，前端重试轮询
// 可直接再走落盘，不会把「落盘失败」误报成「二维码已失效」。
func (a *AccountsAPI) PollOAuth(ctx context.Context, region string, globalRegion string) (*OAuthPollResult, error) {
	a.mu.Lock()
	sess := a.logins[region]
	a.mu.Unlock()
	if sess == nil {
		return &OAuthPollResult{Status: "invalid"}, nil
	}
	if time.Since(sess.StartedAt) > oauthStateTTL {
		a.mu.Lock()
		delete(a.logins, region)
		a.mu.Unlock()
		return &OAuthPollResult{Status: "expired"}, nil
	}
	poll, err := core.OAuthPoll(sess.Realm, sess.State)
	if err != nil {
		return nil, err
	}
	if poll.Pending {
		return &OAuthPollResult{Status: "pending"}, nil
	}

	// 国际版：地区注册 + trial（失败不阻断落盘，原因写进 Note）
	note := ""
	if poll.Cred.Realm == "global" {
		note = a.provisionGlobal(poll.Cred, sess.GlobalRegion)
	}

	name := fmt.Sprintf("workbuddy-%s.json", poll.Cred.UID)
	dir := a.service.GetConfig().AuthDir
	_, statErr := os.Stat(filepath.Join(core.ResolveAuthDir(dir), name))
	updated := statErr == nil
	if _, err := core.ImportCredential(dir, name,
		core.OAuthCredentialJSON(poll.Cred, poll.Nickname)); err != nil {
		return nil, fmt.Errorf("凭证落盘失败: %w", err)
	}
	a.mu.Lock()
	delete(a.logins, region)
	a.mu.Unlock()
	core.EmitEvent(core.EventAccountStatus, map[string]any{
		"uid": poll.Cred.UID, "status": "online", "credential": name,
	})
	return &OAuthPollResult{
		Status: "success", UID: poll.Cred.UID, Nickname: poll.Nickname,
		Realm: poll.Cred.Realm, Updated: updated, Note: note,
	}, nil
}

// provisionGlobal 国际版账号的登录后供给：地区注册 + 领取 trial。
// 对齐 workbuddy-manager：未选地区时探测注册状态，未注册则提示重试；
// trial 幂等，已领过不算失败。
func (a *AccountsAPI) provisionGlobal(cred *core.UpstreamCred, region string) string {
	var msgs []string
	if region != "" {
		if _, m := core.SubmitRegion(cred, region); m != "" {
			msgs = append(msgs, m)
		}
	} else if ok, m := core.RegistrationStatus(cred); !ok {
		msgs = append(msgs, m+"：请在「接入账号」时选择地区后重试（否则聊天会报 14017）")
	}
	if claimed, err := core.ClaimTrialGlobal(cred); err == nil && claimed {
		msgs = append(msgs, "已领取 trial 加油包")
	}
	return strings.Join(msgs, "；")
}

// OpenLoginWindow 在应用内开一个 WebView 窗口加载官方授权页：手机号+短信登录
// 可以完全在应用内完成，不碰系统浏览器（state 轮询与登录页面互相独立，不受影响）。
// 只允许打开当前会话签发的 authUrl，不接受前端传任意 URL。
func (a *AccountsAPI) OpenLoginWindow(ctx context.Context, region string) error {
	a.mu.Lock()
	sess := a.logins[region]
	a.mu.Unlock()
	if sess == nil || sess.AuthURL == "" {
		return fmt.Errorf("没有进行中的授权会话，请先重新发起授权")
	}
	app := application.Get()
	if app == nil {
		return fmt.Errorf("应用未就绪")
	}
	title := "官方登录 · 国内版"
	if sess.Realm == "global" {
		title = "官方登录 · 国际版"
	}
	// 已开过登录窗则只更新地址并置前，避免叠窗口
	if w, ok := app.Window.GetByName("oauth-login"); ok {
		if ww, ok := w.(*application.WebviewWindow); ok {
			ww.SetURL(sess.AuthURL)
			ww.SetTitle(title)
			ww.Show()
			ww.Focus()
			return nil
		}
	}
	app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:    "oauth-login",
		Title:   title,
		Width:   440,
		Height:  660,
		MinWidth: 360,
		MinHeight: 480,
		URL:     sess.AuthURL,
	})
	return nil
}

// FocusMainWindow 把主窗口带回前台（授权成功时提示用）。
func (a *AccountsAPI) FocusMainWindow(ctx context.Context) error {
	if app := application.Get(); app != nil {
		if w, ok := app.Window.GetByName("main"); ok {
			w.Focus()
			return nil
		}
	}
	return fmt.Errorf("主窗口未就绪")
}

// ClientSession 官方客户端当前登录账号探测（账号管理页提示条数据源）：
// 从官方登录位读当前登录 uid（明文），并在 client_auth_dirs 发现目录里找
// 同 uid 的可用明文凭证。只读磁盘，可安全频繁调用。
func (a *AccountsAPI) ClientSession(ctx context.Context) (*core.ClientSession, error) {
	return a.service.DetectClientSession(), nil
}

// ImportClientSession 一键导入客户端当前登录账号：把发现的明文凭证复制进
// auth_dir（不移动原文件，官方登录位里的加密 token 不做也不需要解密）。
func (a *AccountsAPI) ImportClientSession(ctx context.Context) (*ImportResult, error) {
	if err := ensureWritable(a.service); err != nil {
		return nil, err
	}
	sess := a.service.DetectClientSession()
	file, err := a.service.ImportClientSession()
	if err != nil {
		return nil, err
	}
	audit(a.service, "account.import_client_session", sess.UID, "from="+sess.CredFile)
	core.EmitEvent(core.EventAccountStatus, map[string]any{
		"uid": sess.UID, "status": "imported", "credential": file,
	})
	return &ImportResult{
		Files:   []string{file},
		Skipped: []string{},
		AuthDir: core.ResolveAuthDir(a.service.GetConfig().AuthDir),
	}, nil
}

// ImportResult 导入结果
type ImportResult struct {
	Files   []string `json:"files"`
	Skipped []string `json:"skipped"`
	AuthDir string   `json:"authDir"`
}

// ImportCredentials 导入凭证内容到 auth_dir；支持单个对象或对象数组，
// 也支持 v2 加密导出信封（此时必须提供导出时的 password）。
func (a *AccountsAPI) ImportCredentials(ctx context.Context, fileName string, content string, password string) (*ImportResult, error) {
	if err := ensureWritable(a.service); err != nil {
		return nil, err
	}
	dir := a.service.GetConfig().AuthDir
	res := &ImportResult{Files: []string{}, Skipped: []string{}}

	body := strings.TrimSpace(content)
	if body == "" {
		return nil, fmt.Errorf("凭证内容为空")
	}
	// v2 加密信封：解密后按内层 JSON 继续走明文导入路径
	if isEncryptedExport(body) {
		if password == "" {
			return nil, fmt.Errorf("该文件为加密导出，请输入导出密码")
		}
		inner, err := core.DecryptExport([]byte(body), password)
		if err != nil {
			return nil, err
		}
		var payload ExportResult
		if err := json.Unmarshal(inner, &payload); err != nil {
			return nil, fmt.Errorf("解密后的内容不是凭证导出: %w", err)
		}
		for _, c := range payload.Credentials {
			name := c.File
			if name == "" {
				name = fmt.Sprintf("workbuddy-%d.json", len(res.Files)+1)
			}
			f, err := core.ImportCredential(dir, name, c.Content)
			if err != nil {
				res.Skipped = append(res.Skipped, name+"："+err.Error())
				continue
			}
			res.Files = append(res.Files, f)
		}
		res.AuthDir = core.ResolveAuthDir(dir)
		audit(a.service, "account.import", fileName, fmt.Sprintf("解密导入 %d 个，跳过 %d 个", len(res.Files), len(res.Skipped)))
		core.EmitEvent(core.EventAccountStatus, map[string]any{"uid": "*", "status": "imported", "count": len(res.Files)})
		return res, nil
	}
	if body[0] == '[' {
		var arr []json.RawMessage
		if err := json.Unmarshal([]byte(body), &arr); err != nil {
			return nil, fmt.Errorf("凭证数组格式无效: %w", err)
		}
		for i, raw := range arr {
			name := fmt.Sprintf("%s-%d.json", baseName(fileName), i+1)
			f, err := core.ImportCredential(dir, name, raw)
			if err != nil {
				res.Skipped = append(res.Skipped, name+"："+err.Error())
				continue
			}
			res.Files = append(res.Files, f)
		}
	} else {
		f, err := core.ImportCredential(dir, fileName, []byte(body))
		if err != nil {
			return nil, err
		}
		res.Files = append(res.Files, f)
	}
	res.AuthDir = core.ResolveAuthDir(dir)
	audit(a.service, "account.import", fileName, fmt.Sprintf("导入 %d 个，跳过 %d 个", len(res.Files), len(res.Skipped)))
	core.EmitEvent(core.EventAccountStatus, map[string]any{"uid": "*", "status": "imported", "count": len(res.Files)})
	return res, nil
}

// CredentialExport 单份凭证导出
type CredentialExport struct {
	File    string          `json:"file"`
	Content json.RawMessage `json:"content"`
}

// ExportResult 导出结果
type ExportResult struct {
	AuthDir     string             `json:"authDir"`
	ExportedAt  string             `json:"exportedAt"`
	Credentials []CredentialExport `json:"credentials"`
}

// ExportCredentials 导出全部凭证内容。
// password 非空时输出加密信封（AES-256-GCM + PBKDF2，密码不写入文件）；
// 为空时保持 v1 明文导出（向后兼容，前端会提示明文风险）。
func (a *AccountsAPI) ExportCredentials(ctx context.Context, password string) (json.RawMessage, error) {
	dir := core.ResolveAuthDir(a.service.GetConfig().AuthDir)
	entries := []CredentialExport{}
	names, _ := filepath.Glob(filepath.Join(dir, "workbuddy*.json"))
	for _, p := range names {
		raw, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		entries = append(entries, CredentialExport{File: filepath.Base(p), Content: json.RawMessage(raw)})
	}
	exportedAt := time.Now().Format(time.RFC3339)
	if password != "" {
		if len(entries) == 0 {
			return nil, fmt.Errorf("没有可导出的凭证")
		}
		env, err := core.EncryptExport(map[string]any{
			"authDir":     dir,
			"exportedAt":  exportedAt,
			"credentials": entries,
		}, password, exportedAt, len(entries))
		if err != nil {
			return nil, err
		}
		audit(a.service, "account.export", "", fmt.Sprintf("加密导出 %d 份", len(entries)))
		return json.RawMessage(env), nil
	}
	plain, err := json.Marshal(&ExportResult{
		AuthDir:     dir,
		ExportedAt:  exportedAt,
		Credentials: entries,
	})
	if err != nil {
		return nil, err
	}
	audit(a.service, "account.export", "", fmt.Sprintf("明文导出 %d 份", len(entries)))
	return json.RawMessage(plain), nil
}

// OpenAuthDir 在系统文件管理器中打开凭证目录，返回目录绝对路径
func (a *AccountsAPI) OpenAuthDir(ctx context.Context) (string, error) {
	dir := core.ResolveAuthDir(a.service.GetConfig().AuthDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if app := application.Get(); app != nil {
		app.Browser.OpenURL("file://" + dir)
	}
	return dir, nil
}

// OpenURL 用系统浏览器打开外部链接
func (a *AccountsAPI) OpenURL(ctx context.Context, url string) error {
	if app := application.Get(); app != nil {
		app.Browser.OpenURL(url)
		return nil
	}
	return fmt.Errorf("应用未就绪")
}

func baseName(name string) string {
	base := filepath.Base(strings.TrimSpace(name))
	if base == "" || base == "." {
		return "workbuddy"
	}
	if ext := filepath.Ext(base); ext != "" {
		base = base[:len(base)-len(ext)]
	}
	return base
}

// isEncryptedExport 判断内容是否为 v2 加密导出信封（version 字段非 0 即认）。
func isEncryptedExport(body string) bool {
	var probe struct {
		Version int `json:"version"`
	}
	if json.Unmarshal([]byte(body), &probe) != nil {
		return false
	}
	return probe.Version >= 2
}

package core

// 设备授权登录（对齐 workbuddy2api cmd/login 的真实上游流程）。
//
// 流程：
//  1. POST {chatBase}/v2/plugin/auth/state?platform=CLI → {state, authUrl}
//  2. 用户在浏览器打开 authUrl 完成登录（扫码/账密均在官方页面完成）
//  3. GET {chatBase}/v2/plugin/auth/token?state= → 未完成时业务 code!=0（login ing），
//     完成时返回 accessToken/refreshToken/expiresIn/domain
//  4. GET {chatBase}/v2/plugin/login/account?state=（Bearer）→ uid/nickname
//
// 请求头按 realm 切换 Origin/Referer：cn → www.codebuddy.cn，global → www.workbuddy.ai；
// UA 用 CLI 指纹（与参考实现一致，非桌面端 chat 指纹）。

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

const (
	loginStatePath = "/v2/plugin/auth/state"
	loginTokenPath = "/v2/plugin/auth/token"
	loginAcctPath  = "/v2/plugin/login/account"
	loginClientUA  = "CLI/2.63.2 CodeBuddy/2.63.2"
)

// OAuthStart 发起设备授权（导出入口，供 API 层调用）。
func OAuthStart(realm string) (state, authURL string, err error) {
	return newUpstreamClient().StartLogin(realm)
}

// OAuthPoll 轮询设备授权（导出入口，供 API 层调用）。
func OAuthPoll(realm, state string) (*LoginPoll, error) {
	return newUpstreamClient().PollLogin(realm, state)
}

// setLoginHeaders 登录域请求头（对齐 cmd/login commonHeaders）。
func (c *upstreamClient) setLoginHeaders(req *http.Request, realm string) {
	origin := originRefererCN
	if realm == "global" {
		origin = originRefererGlobal
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("Origin", origin)
	req.Header.Set("Referer", origin+"/")
	req.Header.Set("User-Agent", loginClientUA)
}

// StartLogin 发起设备授权：返回 state（轮询凭据）与浏览器授权地址 authUrl。
func (c *upstreamClient) StartLogin(realm string) (state, authURL string, err error) {
	req, err := http.NewRequest(http.MethodPost,
		c.chatBase(realm)+loginStatePath+"?platform=CLI", bytes.NewReader([]byte("{}")))
	if err != nil {
		return "", "", err
	}
	c.setLoginHeaders(req, realm)
	data, err := c.doEnvelope(req)
	if err != nil {
		return "", "", fmt.Errorf("发起授权失败: %w", err)
	}
	var st struct {
		State   string `json:"state"`
		AuthURL string `json:"authUrl"`
	}
	if json.Unmarshal(data, &st) != nil || st.State == "" {
		return "", "", fmt.Errorf("上游未返回 state 或 authUrl")
	}
	// 上游偶发缺 authUrl（尤其 global），此时用 base 兜底拼一个登录页（对齐 gui client）。
	if st.AuthURL == "" {
		st.AuthURL = c.chatBase(realm) + "/login?state=" + st.State + "&platform=CLI"
	}
	return st.State, st.AuthURL, nil
}

// LoginPoll 一次授权轮询的结果；Pending 表示用户尚未在浏览器完成登录。
type LoginPoll struct {
	Pending  bool
	Cred     *UpstreamCred
	Nickname string
}

// PollLogin 轮询登录状态；完成时返回完整凭证（不落盘，由调用方写入凭证目录）。
func (c *upstreamClient) PollLogin(realm, state string) (*LoginPoll, error) {
	req, err := http.NewRequest(http.MethodGet,
		c.chatBase(realm)+loginTokenPath+"?state="+url.QueryEscape(state), nil)
	if err != nil {
		return nil, err
	}
	c.setLoginHeaders(req, realm)
	data, err := c.doEnvelope(req)
	if err != nil {
		// 未完成：上游以业务 code!=0（"login ing"）或 4xx 表示等待中；
		// 网络异常 / 5xx 才是真实错误（对齐 cmd/login runPoll 的判据）。
		var ue *upstreamAPIError
		if errors.As(err, &ue) && ue.Status < 500 {
			return &LoginPoll{Pending: true}, nil
		}
		return nil, fmt.Errorf("轮询登录状态失败: %w", err)
	}
	var tok struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		ExpiresIn    int64  `json:"expiresIn"`
		Domain       string `json:"domain"`
	}
	if json.Unmarshal(data, &tok) != nil || tok.AccessToken == "" {
		return &LoginPoll{Pending: true}, nil
	}
	// login/account 拿 uid/nickname（Bearer；失败不阻断，uid 有兜底）
	var acct struct {
		UID          string `json:"uid"`
		EnterpriseID string `json:"enterpriseId"`
		Nickname     string `json:"nickname"`
	}
	if req2, err := http.NewRequest(http.MethodGet,
		c.chatBase(realm)+loginAcctPath+"?state="+url.QueryEscape(state), nil); err == nil {
		c.setLoginHeaders(req2, realm)
		req2.Header.Set("Authorization", "Bearer "+tok.AccessToken)
		if data2, err := c.doEnvelope(req2); err == nil {
			_ = json.Unmarshal(data2, &acct)
		}
	}
	cred := &UpstreamCred{
		UID:           acct.UID,
		AccessToken:   tok.AccessToken,
		RefreshToken:  tok.RefreshToken,
		Domain:        tok.Domain,
		EnterpriseID:  acct.EnterpriseID,
		ExpiresAtUnix: time.Now().Unix() + tok.ExpiresIn,
	}
	cred.Realm = resolveRealm(realm, tok.Domain)
	if cred.UID == "" {
		cred.UID = "login-" + time.Now().Format("20060102-150405")
	}
	return &LoginPoll{Cred: cred, Nickname: acct.Nickname}, nil
}

// OAuthCredentialJSON 把登录得到的凭证序列化为扁平形凭证文件内容（与导入口径一致，
// ParseCredential 可直接解析）。
func OAuthCredentialJSON(cred *UpstreamCred, nickname string) []byte {
	doc := map[string]any{
		"accessToken":  cred.AccessToken,
		"refreshToken": cred.RefreshToken,
		"expiresAt":    cred.ExpiresAtUnix,
		"domain":       cred.Domain,
		"realm":        cred.Realm,
		"uid":          cred.UID,
	}
	if cred.EnterpriseID != "" {
		doc["enterpriseId"] = cred.EnterpriseID
	}
	if nickname != "" {
		doc["nickname"] = nickname
	}
	raw, _ := json.MarshalIndent(doc, "", "  ")
	return raw
}

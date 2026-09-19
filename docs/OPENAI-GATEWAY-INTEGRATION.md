# 把任意 OpenAI 客户端 / DSH 接入 BuddyBot 网关

> BuddyBot（WorkBuddy Desktop）内置一个 **OpenAI 兼容网关**：账号池里所有 WorkBuddy / CodeBuddy 账号对外统一暴露为 `POST /v1/chat/completions`。任何支持 OpenAI Chat Completions 协议的工具都能直连，无需为每个框架单独写适配器。
>
> 本文面向 DeepSeek Harness（DSH）、Open WebUI、LobeChat、ChatBox、Continue、Cline 等工具的用户——你们不再需要 workbuddy2api / dsh-connect 这类第三方桥接，BuddyBot 就是那个桥。

---

## 1. 网关信息

| 项 | 值 |
| --- | --- |
| Base URL | `http://127.0.0.1:7863/v1`（默认监听 `:7863`，可在设置中修改） |
| Chat | `POST /v1/chat/completions`（支持 SSE 流式、tool_calls、reasoning_content） |
| 模型列表 | `GET /v1/models`（动态来自账号池上游模型目录） |
| Embeddings / Images | `POST /v1/embeddings`、`POST /v1/images/generations` 已端点就绪：完整鉴权/白名单/日志管线，上游未开放该能力时返回结构化 501（`upstream_not_supported`），RAG/画图框架可探测到明确的失败语义而非连接层 404 |
| 健康检查 | `GET /healthz` |
| 鉴权 | `Authorization: Bearer <API Key>`（设置 → 网关，形如 `wb-gw-xxxx`） |

**选号规则（内置，无需配置）**：网关自动按积分到期分层选号——72h 内到期的积分优先烧，余额耗尽/冷却/过期账号自动剔除轮换，单会话粘性固定账号。

## 2. 快速接入

### 2.1 通用 OpenAI 客户端

```bash
curl http://127.0.0.1:7863/v1/chat/completions \
  -H "Authorization: Bearer wb-gw-你的密钥" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "GLM-5.3",
    "messages": [{"role": "user", "content": "你好"}],
    "stream": true
  }'
```

- 在 BuddyBot「API 密钥」页创建密钥，可配置：Token/积分配额、模型白名单、IP 白名单（CIDR）。
- 模型名以 `/v1/models` 返回为准（与官方客户端模型菜单一致，如 `GLM-5.3`、`DeepSeek-V4-Pro`）。

### 2.2 DeepSeek Harness（DSH）

DSH 侧添加自定义 OpenAI 兼容 provider（或使用 DSH 的 `openai-compatible` 接入方式）：

```
base_url = http://127.0.0.1:7863/v1
api_key  = wb-gw-你的密钥
models   = 来自 GET /v1/models（可手动列举）
```

> 对比 dsh-connect-workbuddy：那个插件需要 loopback shim 中转官方协议且只能接 DSH；
> BuddyBot 直接给标准 OpenAI 协议，DSH、Open WebUI、LobeChat 等一次接入全部可用。

### 2.3 Open WebUI / LobeChat / ChatBox

设置 → OpenAI 兼容接口：

- API Base URL：`http://127.0.0.1:7863/v1`
- API Key：`wb-gw-你的密钥`
- 点击「获取模型列表」自动填充。

### 2.4 Claude Code / Cline 等支持自定义 base 的 CLI

以环境变量方式指向网关：

```bash
export OPENAI_BASE_URL=http://127.0.0.1:7863/v1
export OPENAI_API_KEY=wb-gw-你的密钥
```

### 2.5 智能体一键接入（推荐）

不想手改配置？「智能体」页提供一键接入：自动探测本机已安装的 AI 编程客户端
（Claude Code / Claude Desktop / Codex / Cursor 等 11 类），把网关写入其配置——
写入前自动备份、可一键回滚，且只动与网关相关的字段。接入细节见 `agents.go`
与「智能体」页 UI。

## 3. 远程访问与安全

1. **默认仅本机**：监听 `:7863` 实际绑定所有网卡，如只在本机使用，建议改为 `127.0.0.1:7863`。
2. **局域网访问**：保持默认监听并在「API 密钥」页为每台设备独立发密钥 + 配 IP 白名单；不要复用同一密钥。
3. **公网暴露**：不建议直接暴露。确需远程使用时套反代 + TLS（Caddy/Nginx），并在密钥层限流。

## 4. 常见问题

| 现象 | 处理 |
| --- | --- |
| 401 | 密钥错误/被禁用；「API 密钥」页核对 |
| 无可用账号（503） | 账号页执行「刷新」；检查账号是否全部冷却/过期/禁用 |
| 国际版账号报 14017 | 未完成地区注册；重新走「接入账号」选地区授权 |
| 模型列表为空 | 深度刷新账号（保活会同步上游模型目录） |
| 想固定某账号测试 | 用只含该账号的密钥模型白名单 + 临时禁用其他账号 |

## 5. 与社区工具的关系

| 社区方案 | BuddyBot 对应能力 |
| --- | --- |
| workbuddy2api（裸网关） | 同源核心，另加桌面 UI / 密钥 / 日志 / 任务中心 |
| workbuddy-switch（切号+签到） | 账号池自动保活签到 + 网关层分层选号（无需切客户端） |
| WorkDaddy（客户端增强） | 「客户端注入」功能：免打扰、登录态备份切换、账号池积分面板（设置中显式开启） |
| dsh-connect-workbuddy（DSH 桥） | 本网关即标准 OpenAI 协议，DSH 直连 |

---

*文档对应版本：BuddyBot（WorkBuddy Desktop）；网关默认端口与密钥管理见 `PROJECT.md` 与「系统设置」页。*

# BuddyBot

<p align="center">
  <img src="docs/appicon.png" width="72" alt="BuddyBot"/>
</p>

**WorkBuddy Account Pool & Gateway Console** — a cross-platform desktop app (macOS / Windows / Linux) for CodeBuddy account pool management with an OpenAI-compatible gateway.

> Aggregate multiple official accounts into a local account pool and serve them through a unified gateway at `/v1/chat/completions`. Account rotation, credential keep-alive, and daily credit check-ins are fully automated.

**Tech Stack**: Wails v3 (beta.23) + Go 1.22+ · React 18 + TypeScript + Vite · Zustand · Design system in `../docs/UI-DESIGN.md`

[简体中文](README.md) | English

### Support the Project

If BuddyBot has been helpful, consider buying the author a coffee ☕

<table>
  <tr>
    <td align="center"><img src="docs/微信赞赏码.jpg" width="220" alt="WeChat reward QR"/><br/><sub><b>微信赞赏码.jpg</b></sub></td>
    <td align="center"><img src="docs/支付宝赞赏码.jpg" width="220" alt="Alipay reward QR"/><br/><sub><b>支付宝赞赏码.jpg</b></sub></td>
  </tr>
</table>


**Donor Wall** (thanks to every supporter)

| Date       | Name     | Amount | Channel | Message                 |
| ---------- | -------- | -----: | ------- | ----------------------- |
| 2026-09-12 | 星河\_风 | ¥18.88 | WeChat  | Cat Travel is amazing 😄 |
| 2026-09-15 | M\*\*o   |  ¥6.66 | Alipay  | Rock-solid gateway      |
| 2026-09-18 | 阿哲     | ¥50.00 | WeChat  | Keep it up!             |

---

## Features

**Account Pool**
- Account aggregation: scans local credential files to build the pool automatically; QR-code onboarding (CN / international) and credential import/export
- Status derivation: online / cooldown / expired / re-login needed / disabled — all computed from real expiry times
- Expiring-first: near-expiry credits and tokens are surfaced and prioritized

**Automated Tasks**
- Auto check-in: daily `/v2/billing/meter/daily-checkin` + streak reward tier redemption + using up lottery draws
- Token keep-alive: near-expiry credentials refreshed and written back automatically, along with real balance breakdown (used / total / expiring) and dynamic model catalog refresh
- Cat travel: fully automated adopt / dispatch / claim state machine (international accounts without this system are skipped honestly)
- Real scheduler: triggers at configured hours and writes task logs; policies adjustable in Settings

**OpenAI-Compatible Gateway**
- Unified endpoint `/v1/chat/completions`; SSE streamed as-is; non-streaming supports incremental tool_calls aggregation
- Pool resilience: automatic rotation on 429/5xx/401/network/balance failures; on 401 the token is refreshed before retry
- Session stickiness: same `session_id` pinned to one account, reusing upstream prompt cache to cut costs
- Quota & security: per-key token/credit throttling, model & IP allowlists, SHA-256 key auth
- deepseek-family requests get `thinking:{type:"enabled"}` + default reasoning effort injected automatically

**Extras**
- One-click agent integration: Claude Code / Codex / OpenCode / Pi / Kimi Code / CodeBuddy configs written automatically, with rollback
- Analytics: token usage, success rate, P50/P90 latency — aggregated from real logs only
- Chat Playground: built-in console exercising the full auth / quota / routing / metering pipeline
- Logs: unified search over request logs + task logs
- Data & backup: credentials, config, and keys stored locally with export support

## UI Tour

### Dashboard

Account pool and gateway overview: online accounts, request volume, token consumption, account credit balance, healthy-account ratio curve, pool status distribution (online / cooldown / disabled / expired), recent tasks and system health — auto-refreshed every 30 seconds.

![Dashboard](docs/screenshots/dashboard.png)

### Token Usage

Aggregated exclusively from real gateway-recorded usage: total tokens, daily average, request count, sessions, success rate, and average latency (P50/P90). Switch between 1/3/7/30-day ranges, split by input/output/cache, and compare against official usage.

![Token Usage](docs/screenshots/tokens.png)

### Account Management

The core of the account pool: scans local credential files to build the pool automatically. Supports QR-code onboarding (CN / international), credential import/export, batch check-in, batch disable; filter by status (online / cooldown / expired / re-login needed / disabled), sort by expiring credits first; per-account views for credit balance, token validity, recent activity, and manual refresh.

![Account Management](docs/screenshots/accounts.png)

### API Keys

Gateway access key management: create/revoke SHA-256 keys, per-key quota throttling (tokens / credits), model & IP allowlists, usage and last-used tracking.

![API Keys](docs/screenshots/keys.png)

### Chat Playground

A built-in OpenAI-compatible debugging console wired directly to the local gateway `/v1/chat/completions`: model picker (with context length and local usage hints), System Prompt, Temperature, max_tokens, streaming (SSE), session stickiness, reasoning effort, quick prompts — exercising the full auth / quota / account-routing / metering pipeline.

![Chat Playground](docs/screenshots/chat.png)

### Agent Integration

One-click gateway setup for popular coding agent clients — each client's config file is written automatically:

| Client | What gets written |
|---|---|
| Claude Code | env in `~/.claude/settings.json` (ANTHROPIC_BASE_URL / AUTH_TOKEN / model slots) |
| Codex | model_provider in `~/.codex/config.toml` and OPENAI_API_KEY in `auth.json` |
| OpenCode | provider.workbuddy added to `~/.config/opencode/opencode.json` |
| Pi | providers.workbuddy added to `~/.pi/agent/models.json` |
| Kimi Code | default_model and providers in `~/.kimi-code/config.toml` |
| CodeBuddy / WorkBuddy | vendor=WorkBuddy custom model entry in `models.json` (existing models untouched) |

Pick model slots, filter by model family, integrate or roll back with one click — client configs are backed up and restorable anytime.

![Agent Integration](docs/screenshots/agents.png)

### Skills Market

Browse and install skill packs to extend agent domain capabilities.

![Skills Market](docs/screenshots/skills.png)

### Prompt Library

A curated library of productivity prompts — copy any prompt to the clipboard in one click.

![Prompt Library](docs/screenshots/prompts.png)

### Logs

Unified viewer for gateway request logs and scheduled task logs: filter by level/time, inspect per-request account routing, model, token usage, and error details.

![Logs](docs/screenshots/logs.png)

### Settings

Grouped configuration: General (language/theme), Gateway (port/toggles), Security & Access (REST token), Prompts & Models, Session Stickiness, Scheduled Tasks, Account Pool Strategy, Data & Backup, etc. Everything persists to the local `config.json`.

![Settings](docs/screenshots/settings.png)

## Gateway

Once started, an **OpenAI-compatible** endpoint is served locally (default `:7863`):

```
POST http://127.0.0.1:7863/v1/chat/completions
Authorization: Bearer <key created in "API Keys">
```

- **Streaming / non-streaming**: SSE relayed as-is; non-streaming supports incremental tool_calls aggregation and reassembly
- **Pool resilience**: account-level failures (429/5xx/401/network/balance) trigger automatic rotation and retry; on 401 the refreshToken is exchanged for a new token (atomically written back) before retrying; requests with a `session_id` stick to the same account (better upstream prompt caching)
- **Quota & allowlist**: per-key token/credit quota throttling, model & IP allowlists
- **Reasoning models**: deepseek-family requests get `thinking:{type:"enabled"}` + default reasoning effort injected automatically
- **Cache optimization**: per-account isolated `prompt_cache_key` injection (stable within a session, reusing upstream prefix cache for significantly lower cost)

See [docs/OPENAI-GATEWAY-INTEGRATION.md](docs/OPENAI-GATEWAY-INTEGRATION.md) for client integration examples, or use the Agent Integration page to write configs automatically.

## Scheduled Tasks

A real scheduler triggers tasks at configured hours and writes task logs:

- **Daily check-in**: `/v2/billing/meter/daily-checkin` + streak reward tier redemption + using up lottery draws
- **Token keep-alive**: refresh near expiry and write back; also fetch real balance breakdown (used / total / expiring buckets) and refresh the upstream dynamic model catalog
- **Cat travel**: adopt / dispatch / claim state machine (international accounts without this system are skipped honestly)
- **International account check-in day**: claim a one-shot trial booster pack instead (idempotent 14051)

## Getting Started

```bash
# Install Wails CLI (one-time)
go install github.com/wailsapp/wails/v3/cmd/wails3@latest

# Development mode (hot reload)
wails3 dev

# Build
wails3 build -platform darwin/arm64    # macOS
wails3 build -platform windows/amd64   # Windows
```

Frontend-only preview (browser, UI only without real data):

```bash
cd frontend && npm install && npm run dev   # http://localhost:5173
```

The browser preview does not connect to the local backend — pages honestly show errors and empty states (no mock data is ever filled in).
Run via `wails3 dev` or an installed build for real data.

## Data Sources (Real-Implementation Boundary)

- **Accounts**: scans real credential files under `auth_dir` (both nested and flat layouts supported); status derived from real expiry times
- **Gateway**: SHA-256 key auth, model/IP allowlists, token/credit quota throttling, real metering per key and per account
- **Upstream calls**: real requests to `{realm-base}/v2/chat/completions` (official desktop client fingerprint headers + SSE relay,
  see `internal/core/upstream.go`); usage comes from upstream responses, and errors drive account cooldown by category
- **Statistics**: aggregated from real request/task logs only; metrics without a data source display as empty or "not integrated"
- **Model catalog**: three-way merge of local seed catalog + upstream dynamic catalog (refreshed on keepalive cycles,
  non-chat models excluded per nonChatModel rule) + locally observed models
- Every number on screen must be traceable to real data: **no mock fallback** — the browser environment shows explicit errors or empty states

## Project Layout

```
workbuddy-desktop/
├── main.go                 # Wails v3 entry (frameless window + service registration)
├── tray.go                 # System tray
├── internal/
│   ├── core/               # service / store / gateway / scheduler / accounts / stats / models
│   └── api/                # Service bindings (Accounts/Config/Gateway/Keys/Logs/System/Stats/Models/Chat/Agents)
├── docs/                   # Gateway integration docs + UI screenshots
└── frontend/
    ├── src/
    │   ├── styles/         # tokens.css (design system) + app.css (component styles)
    │   ├── components/     # layout (TitleBar/Sidebar) + common (feedback/state blocks)
    │   ├── pages/          # Dashboard / Tokens / Accounts / Keys / Chat / Agents / Skills / Prompts / Logs / Settings
    │   ├── services/       # api.ts (IPC bridge, zero mock) + events.ts (backend event subscriptions)
    │   ├── hooks/          # useAsync (real error exposure) / useTheme
    │   └── types/          # re-exported from generated Wails bindings (single source of truth)
    ├── bindings/           # wails3 generate bindings output (do not edit by hand)
    └── dist/               # Build output (embedded into the binary via go:embed)
```

## Development Conventions

- The frontend accesses the backend only through `services/api.ts`; types are re-exported from `wails3 generate bindings` output — hand-written mirror types are forbidden
- Colors/spacing/radii must use `tokens.css` variables; hardcoded color values are forbidden
- WAF fingerprint sanitization, reasoning-effort downgrade, and activity reporting (inflating conversation volume) are intentional omissions: the first two will be completed per reference implementations once real-account risk control is observed; activity reporting involves fabricating conversation volume and will not be implemented; orphan tool_call pairing cleanup and malformed-argument detection are defensive items to be added when real-account testing exposes issues

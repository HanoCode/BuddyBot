/**
 * 英文词典 · agents 页面分片。key = 中文原文。
 */
export const en: Record<string, string> = {
  // 客户端说明（AGENT_DESC，路径/配置键保持原样）
  "写入 ~/.claude/settings.json 的 env（ANTHROPIC_BASE_URL / AUTH_TOKEN / 四个模型槽位）":
    "Writes the env of ~/.claude/settings.json (ANTHROPIC_BASE_URL / AUTH_TOKEN / four model slots)",
  "写入 ~/.codex/config.toml 的 model_provider 与 auth.json 的 OPENAI_API_KEY":
    "Writes model_provider in ~/.codex/config.toml and OPENAI_API_KEY in auth.json",
  "在 ~/.config/opencode/opencode.json 增加 provider.workbuddy":
    "Adds provider.workbuddy to ~/.config/opencode/opencode.json",
  "在 ~/.pi/agent/models.json 增加 providers.workbuddy":
    "Adds providers.workbuddy to ~/.pi/agent/models.json",
  "写入 ~/.kimi-code/config.toml 的 default_model 与 providers.workbuddy":
    "Writes default_model and providers.workbuddy to ~/.kimi-code/config.toml",
  "在 ~/.codebuddy/models.json 写入 vendor=WorkBuddy 的自定义模型条目（其余模型原样保留）":
    "Writes a vendor=WorkBuddy custom model entry into ~/.codebuddy/models.json (other models kept as-is)",
  "在 ~/.workbuddy/models.json 写入 vendor=WorkBuddy 的自定义模型条目（其余模型原样保留）":
    "Writes a vendor=WorkBuddy custom model entry into ~/.workbuddy/models.json (other models kept as-is)",
  "在 ~/.zcode/v2/config.json 写入 provider.workbuddy（需先退出 ZCode，运行中的 ZCode 会在退出时覆盖本文件）":
    "Writes provider.workbuddy to ~/.zcode/v2/config.json (quit ZCode first; a running ZCode overwrites this file on exit)",
  "在 ~/.qwen/settings.json 写入 modelProviders.openai 与 WORKBUDDY_GATEWAY_API_KEY":
    "Writes modelProviders.openai and WORKBUDDY_GATEWAY_API_KEY to ~/.qwen/settings.json",
  "在 ~/.minimax/config.yaml 写入 provider.workbuddy（@ai-sdk/openai-compatible）":
    "Writes provider.workbuddy to ~/.minimax/config.yaml (@ai-sdk/openai-compatible)",
  "在 ~/.config/crush/crush.json 写入 provider.workbuddy（type=openai）":
    "Writes provider.workbuddy to ~/.config/crush/crush.json (type=openai)",
  "在 ~/.aider.conf.yml 写入 model（openai/ 前缀）与 openai-api-base / openai-api-key":
    "Writes model (openai/ prefix) and openai-api-base / openai-api-key to ~/.aider.conf.yml",
  "在 ~/.config/zed/settings.json 写入 language_models.openai_compatible.WorkBuddy":
    "Writes language_models.openai_compatible.WorkBuddy to ~/.config/zed/settings.json",
  "在 ~/.continue/config.yaml 的 models[] 追加网关模型条目（其余条目原样保留）":
    "Appends gateway model entries to models[] in ~/.continue/config.yaml (other entries kept as-is)",

  // toast / confirmDialog
  "读取备份失败": "Failed to read backup",
  "请至少选择一个模型": "Please select at least one model",
  "更新 {name} 配置": "Update {name} configuration",
  "该客户端已接入过本网关，将覆盖网关相关字段（其余配置保留，原文件自动备份）。":
    "This client has been connected to this gateway before; gateway-related fields will be overwritten (other config preserved, original file auto-backed up).",
  "更新": "Update",
  "{name} 接入成功": "{name} connected successfully",
  "写入 {n} 个文件，备份于 {dir}": "Wrote {n} files, backed up to {dir}",
  "{name} 接入失败": "{name} connection failed",
  "回滚 {name} 配置": "Roll back {name} configuration",
  "将把备份 {id} 中的 {n} 个文件恢复到原路径，当前配置会被覆盖。":
    "This will restore {n} files from backup {id} to their original paths; the current configuration will be overwritten.",
  "回滚": "Roll back",
  "{name} 已回滚": "{name} rolled back",
  "恢复了 {n} 个文件": "Restored {n} files",
  "回滚失败": "Rollback failed",

  // 标题与页面文案
  "一键把本机网关写入 Claude Code / Codex 等 AI 编程客户端的配置 · 检测到 {a} / {b} 个客户端":
    "One-click write this gateway into the config of AI coding clients like Claude Code / Codex · {a} / {b} clients detected",
  "重新探测": "Re-detect",
  "接入模型": "Models to connect",
  "选择要写入客户端的模型（Claude Code 只取前 4 个，其余客户端全量写入）":
    "Select the models to write to clients (Claude Code takes only the first 4; other clients write all)",
  "已选 {n} 个": "{n} selected",
  "模型清单为空：先在账号页完成保活以拉取上游模型":
    "Model list is empty: keep accounts alive on the Accounts page first to fetch upstream models",
  "点击取消选择": "Click to deselect",
  "点击选择": "Click to select",
  "搜索模型…": "Search models…",
  "全部分类": "All categories",
  "全选": "Select all",
  "清空": "Clear",
  "没有匹配的模型": "No matching models",
  "显示 {a} / {b} 个模型": "Showing {a} / {b} models",
  "没有可用客户端": "No available clients",
  "已接入": "Connected",
  "未接入": "Not connected",
  "未安装": "Not installed",
  "写入网关配置（自动备份）": "Write gateway config (auto-backup)",
  "未检测到该客户端": "Client not detected",
  "更新配置": "Update configuration",
  "一键接入": "Connect in one click",
  "历史备份": "History backups",
  "暂无备份（首次接入前没有旧配置）": "No backups yet (no old config before first connection)",
  "{n} 个文件": "{n} files",
};

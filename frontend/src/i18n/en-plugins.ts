/**
 * 英文词典 · 插件中心分片。
 * key = 中文原文，value = 英文译文；未收录的 key 在英文界面下原样显示。
 */
export const en: Record<string, string> = {
  // ---------- 侧边栏 / 页面 ----------
  "插件中心": "Plugin Center",
  "把 BuddyBot 的能力以插件形式交给编程智能体使用：不接管、不驱动 agent，只提供数据与方法。":
    "Hand BuddyBot's capabilities to coding agents as plugins: not orchestrating them, just providing data and methods.",
  "所有写入都会先备份；卸载只移除本插件写入的内容，你自己的配置不受影响。":
    "Every write is backed up first; uninstalling removes only what the plugin wrote — your own config stays untouched.",

  // ---------- 状态 ----------
  "已接入": "Connected",
  "未接入": "Not connected",
  "已写入": "Written",
  "未写入": "Not written",
  "未检测到安装": "Not installed",
  "未检测到支持的客户端": "No supported client detected",
  "一键接入": "Enable",
  "重新写入": "Rewrite",
  "卸载": "Uninstall",
  "写入 {n} 个文件": "Wrote {n} file(s)",

  // ---------- 插件名与说明 ----------
  "MCP 工具接入": "MCP tools",
  "把本机数据暴露为 MCP 工具：buddybot_quota（账号池余额）/ buddybot_estimate_cost（费用估算）/ buddybot_search_sessions（历史会话检索）/ buddybot_search_code（代码检索）":
    "Expose local data as MCP tools: buddybot_quota (pool balance), buddybot_estimate_cost (cost estimate), buddybot_search_sessions (session search), buddybot_search_code (code search)",
  "规则一源多写": "Single-source rules",
  "维护一份编码规范，同步写入各客户端的规则文件（托管块内，不影响你自己的规则）":
    "Maintain one coding guideline and sync it into each client's rules file (inside a managed block, your own rules untouched)",
  "安全命令自动放行": "Auto-approve safe commands",
  "PreToolUse 钩子：白名单内的只读类命令直接放行（不触碰敏感路径、不带删除/执行类参数），减少确认弹窗":
    "PreToolUse hook: read-only allowlisted commands are approved automatically (no sensitive paths, no delete/exec-style arguments)",
  "会话上下文注入": "Session context injection",
  "SessionStart 钩子：会话开始注入账号池与今日消耗摘要，让智能体感知本机资源":
    "SessionStart hook: injects pool status and today's spend at session start",
  "任务完成推送": "Task completion push",
  "Stop 钩子：整轮任务结束推送完成/失败摘要（走已配置的 Bark / PushPlus 通道）":
    "Stop hook: pushes a completion summary when the turn ends (via configured Bark / PushPlus channels)",
  "提效指令安装为命令": "Install prompts as commands",
  "把提效指令库安装为客户端 slash 命令（/buddybot-<编号>-<分类>），在会话里直接调用":
    "Install the prompt library as client slash commands (/buddybot-<id>-<category>) for direct use in a session",

  // ---------- 操作提示 ----------
  "重新写入「{name}」": "Rewrite \"{name}\"",
  "将按当前配置重新写入各客户端文件；原文件会先备份，可在本页底部回滚。":
    "Rewrite client files with the current config; originals are backed up and can be restored at the bottom of this page.",
  "「{name}」已写入": "\"{name}\" written",
  "「{name}」写入失败": "Failed to write \"{name}\"",
  "卸载「{name}」": "Uninstall \"{name}\"",
  "将移除本插件写入的条目/托管块，你原有的配置内容保持不变。":
    "Removes the entries/managed block written by this plugin; your existing config stays as is.",
  "「{name}」已卸载": "\"{name}\" uninstalled",
  "「{name}」卸载失败": "Failed to uninstall \"{name}\"",
  "源文本已变更，待重新同步": "Source text changed, resync needed",
  "已同步": "In sync",
  "该客户端不支持 MCP 配置写入": "This client does not support MCP config writes",
  "命令路径已失效，请重新写入": "Command path is stale — rewrite it",

  // ---------- 插件配置（规则文本 + 放行白名单）----------
  "覆盖客户端配置文件的写入都会先备份（可在本页底部回滚）；卸载只移除本插件写入的内容。":
    "Writes that overwrite client config files are backed up first (restore at the bottom of this page); uninstalling removes only what this plugin wrote.",
  "注意：MCP 工具返回的本机内容会进入智能体上下文，并随请求上行到模型服务商。":
    "Note: content returned by MCP tools enters the agent context and travels upstream to the model provider with the request.",
  "插件配置": "Plugin settings",
  "规则文本决定「规则一源多写」同步什么；放行白名单决定哪些命令免确认执行。":
    "The rules text is what \"single-source rules\" syncs; the allowlist decides which commands skip confirmation.",
  "插件配置读取失败": "Failed to read plugin settings",
  "规则文本（写进 CLAUDE.md / AGENTS.md / .cursor/rules 的托管块）":
    "Rules text (written into the managed block of CLAUDE.md / AGENTS.md / .cursor/rules)",
  "安全命令放行白名单（前缀匹配；命中且不含组合符号才放行）":
    "Safe-command allowlist (prefix match; allowed only when no shell metacharacters are present)",
  "（空：任何命令都按客户端默认流程询问）": "(empty: every command follows the client's default prompt flow)",
  "移除 {v}": "Remove {v}",
  "如：go test ./...": "e.g. go test ./...",
  "添加": "Add",
  "恢复默认": "Restore defaults",
  "放行 = 跳过确认。默认只放行只读检视与不执行项目代码的构建命令；加入 go run / npm test / make 等会执行项目代码，请自行权衡。":
    "Allowing means skipping confirmation. Defaults cover read-only inspection and builds that do not execute project code; adding go run / npm test / make runs project code — your call.",
  "有未保存的修改": "Unsaved changes",
  "配置已是最新": "Settings are up to date",
  "保存配置": "Save settings",
  "插件配置已保存": "Plugin settings saved",
  "规则文本与放行白名单已写入本机配置": "Rules text and allowlist written to the local config",
  "保存失败": "Save failed",
  "已填入默认白名单": "Default allowlist filled in",
  "确认无误后点「保存配置」生效": "Click \"Save settings\" to apply",
  "读取默认值失败": "Failed to read defaults",

  // ---------- 提效指令与备份 ----------
  "管理指令": "Manage commands",
  "提效指令安装": "Prompt commands",
  "已安装 {n} / {all} 条；安装后可在会话里用 /buddybot-编号-分类 调用。":
    "{n} of {all} installed; call them in a session as /buddybot-<id>-<category>.",
  "筛选指令": "Filter commands",
  "指令清单读取失败": "Failed to read the command list",
  "没有匹配的指令": "No matching command",
  "安装": "Install",
  "操作失败": "Operation failed",
  "写入备份与回滚": "Write backups & restore",
  "每次写入前的原始文件副本；回滚会用备份覆盖当前文件。":
    "Original file copies taken before each write; restoring overwrites the current files.",
  "回滚备份": "Restore backup",
  "将用 {time} 的备份覆盖 {n} 个文件：{files}":
    "Overwrite {n} file(s) with the {time} backup: {files}",
  "回滚": "Restore",
  "已回滚 {n} 个文件": "Restored {n} file(s)",
  "回滚失败": "Restore failed",
  "{n} 个文件": "{n} file(s)",
  "备份于 {dir}": "Backed up to {dir}",
};

// ============================================================
// IPC 桥接层
//
// 只做两件事：环境守卫 + 调用 Wails 生成的真实绑定。
// 这里**没有任何 mock 回退**——浏览器里跑不出真实数据，就明确报错，
// 而不是用假数据把界面填满。
// ============================================================
import * as API from "../../bindings/workbuddy-desktop/internal/api";
import type {
  Account,
  AccountListResult,
  AccountQuery,
  AccountBackup,
  AwakeStatus,
  AutoStartStatus,
  AuditLogPage,
  BackupItem,
  BrowseParams,
  ChatParams,
  ChatResult,
  ClientSession,
  ClientSwitchPrecheck,
  ClientTokenStats,
  ConfigExportResult,
  ConfigMeta,
  CreateKeyParams,
  CreateKeyResult,
  CredentialExportResult,
  CreditDetail,
  CreditLogPage,
  Dashboard,
  GatewayConfig,
  GatewayStatus,
  ImportResult,
  InjectConfig,
  InjectStatus,
  KeyUsage,
  KeyView,
  LogExportResult,
  LogQuery,
  ModelInfo,
  AgentTarget,
  AgentApplyResult,
  AgentBackup,
  ApplyParams,
  PluginStatus,
  PluginApplyResult,
  PluginSettings,
  PluginSlashCommand,
  PluginBackup,
  OAuthHint,
  OAuthPollResult,
  OfficialUsageReport,
  Overview,
  RequestLogCost,
  RequestLogPage,
  SessionDrilldown,
  DataMigrationDiag,
  DataMigrationResult,
  SchedulerStatus,
  SkillHubBrowseResult,
  SkillHubCLIStatus,
  SkillInstallParams,
  SkillInstallResult,
  SkillTargetInfo,
  SkillUpdateResult,
  SkillUpdatesResult,
  InstalledSkill,
  SystemInfo,
  TaskLogPage,
  TaskRun,
  UpdateInfo,
} from "../types";

/**
 * 是否运行在 Wails 桌面应用内（只有这里能拿到真实后端数据）。
 *
 * 注意不能用 `"_wails" in window` 判断：@wailsio/runtime 在纯浏览器里
 * 也会注入 window._wails（vite 预览时同样成立）。这里与运行时自身的
 * 桥接检测保持一致——只有存在真实 WebView 桥时才算桌面环境。
 */
export const IS_WAILS = (() => {
  if (typeof window === "undefined") return false;
  const w = window as unknown as {
    chrome?: { webview?: { postMessage?: unknown } };
    webkit?: { messageHandlers?: { external?: unknown } };
    wails?: { invoke?: unknown };
  };
  return Boolean(
    w.chrome?.webview?.postMessage ||
      w.webkit?.messageHandlers?.external ||
      w.wails?.invoke,
  );
})();

/** 未接入后端时的统一错误信息 */
export const NO_BACKEND_MESSAGE =
  "未连接到桌面应用后端。浏览器预览只提供界面，请用 wails3 dev 或安装包运行以获得真实数据。";

function native(): void {
  if (!IS_WAILS) throw new Error(NO_BACKEND_MESSAGE);
}

async function call<T>(fn: () => Promise<T | null>): Promise<T> {
  native();
  const res = await fn();
  if (res === null || res === undefined) throw new Error("后端返回空结果");
  return res;
}

// ---------- 账号 ----------

export const accountsApi = {
  list(query: AccountQuery = {}): Promise<AccountListResult> {
    return call(() => API.AccountsAPI.List(query));
  },
  detail(uid: string): Promise<Account> {
    return call(() => API.AccountsAPI.Detail(uid));
  },
  refreshCredit(uid: string): Promise<Account> {
    return call(() => API.AccountsAPI.RefreshCredit(uid));
  },
  checkin(uids: string[]): Promise<TaskRun> {
    return call(() => API.AccountsAPI.Checkin(uids));
  },
  runTask(taskType: string, uids: string[] = []): Promise<TaskRun> {
    return call(() => API.AccountsAPI.RunTask(taskType, uids));
  },
  runTaskAsync(taskType: string, uids: string[] = []): Promise<void> {
    native();
    return API.AccountsAPI.RunTaskAsync(taskType, uids);
  },
  taskStatus(): Promise<SchedulerStatus> {
    return call(() => API.AccountsAPI.TaskStatus());
  },
  remove(uid: string): Promise<void> {
    native();
    return API.AccountsAPI.Delete(uid);
  },
  setDisabled(uid: string, disabled: boolean): Promise<void> {
    native();
    return API.AccountsAPI.SetDisabled(uid, disabled);
  },
  startOAuth(region: "cn" | "global"): Promise<OAuthHint> {
    return call(() => API.AccountsAPI.StartOAuth(region));
  },
  pollOAuth(region: "cn" | "global", globalRegion = ""): Promise<OAuthPollResult> {
    return call(() => API.AccountsAPI.PollOAuth(region, globalRegion));
  },
  focusMainWindow(): Promise<void> {
    return API.AccountsAPI.FocusMainWindow();
  },
  openLoginWindow(region: "cn" | "global"): Promise<void> {
    native();
    return API.AccountsAPI.OpenLoginWindow(region);
  },
  importCredentials(fileName: string, content: string, password = ""): Promise<ImportResult> {
    return call(() => API.AccountsAPI.ImportCredentials(fileName, content, password));
  },
  /** 官方客户端当前登录账号探测（登录位 uid 明文 + 发现目录找可用明文凭证） */
  clientSession(): Promise<ClientSession> {
    return call(() => API.AccountsAPI.ClientSession());
  },
  /** 一键导入客户端当前登录账号（复制发现的明文凭证进 auth_dir） */
  importClientSession(): Promise<ImportResult> {
    return call(() => API.AccountsAPI.ImportClientSession());
  },
  /** 导出凭证：弹出系统保存对话框，返回落盘路径与份数（path 为空 = 用户取消） */
  exportCredentials(password = ""): Promise<CredentialExportResult> {
    native();
    return API.AccountsAPI.ExportCredentials(password).then((r) => r ?? { path: "", count: 0 });
  },
  openAuthDir(): Promise<string> {
    native();
    return API.AccountsAPI.OpenAuthDir();
  },
  openURL(url: string): Promise<void> {
    native();
    return API.AccountsAPI.OpenURL(url);
  },
};

// ---------- 网关 ----------

export const gatewayApi = {
  status(): Promise<GatewayStatus> {
    return call(() => API.GatewayAPI.Status());
  },
  start(): Promise<void> {
    native();
    return API.GatewayAPI.Start();
  },
  stop(): Promise<void> {
    native();
    return API.GatewayAPI.Stop();
  },
  restart(): Promise<void> {
    native();
    return API.GatewayAPI.Restart();
  },
};

// ---------- 密钥 ----------

export const keysApi = {
  list(): Promise<KeyView[]> {
    return call(() => API.KeysAPI.List());
  },
  create(params: CreateKeyParams): Promise<CreateKeyResult> {
    return call(() => API.KeysAPI.Create(params));
  },
  update(id: string, params: CreateKeyParams): Promise<void> {
    native();
    return API.KeysAPI.Update(id, params);
  },
  remove(id: string): Promise<void> {
    native();
    return API.KeysAPI.Delete(id);
  },
  stats(id: string): Promise<KeyUsage> {
    return call(() => API.KeysAPI.Stats(id));
  },
};

// ---------- 统计 ----------

export const statsApi = {
  dashboard(days: number): Promise<Dashboard> {
    return call(() => API.StatsAPI.Dashboard(days));
  },
  overview(days: number): Promise<Overview> {
    return call(() => API.StatsAPI.Overview(days));
  },
  /** 官方口径用量对账（force = 跳过 30 分钟缓存重新拉取） */
  officialUsage(days: number, force = false): Promise<OfficialUsageReport | null> {
    return call(() => API.StatsAPI.OfficialUsage(days, force));
  },
  /** WorkBuddy 客户端自身 token 消耗（本机会话日志扫描；force = 跳过 5 分钟缓存重扫） */
  clientTokenStats(days: number, force = false): Promise<ClientTokenStats | null> {
    return call(() => API.StatsAPI.ClientTokenStats(days, force));
  },
  /** 会话下钻：缓存命中率 KPI + 会话 Top 聚合 */
  sessionDrilldown(days: number): Promise<SessionDrilldown | null> {
    return call(() => API.StatsAPI.SessionDrilldown(days));
  },
  /** 单个会话的请求明细（时间升序；每行带按当前单价表换算的金额） */
  sessionRequests(sessionID: string, days: number): Promise<RequestLogCost[] | null> {
    return call(() => API.StatsAPI.SessionRequests(sessionID, days));
  },
};

// ---------- 客户端账号切换 ----------

export const clientSwitchApi = {
  precheck(uid: string): Promise<ClientSwitchPrecheck> {
    return call(() => API.ClientSwitchAPI.Precheck(uid));
  },
  /** 执行切换：备份 → 退出客户端 → 写入登录位 → 重启（进度走 clientswitch:progress 事件） */
  switch(uid: string): Promise<void> {
    native();
    return API.ClientSwitchAPI.Switch(uid);
  },
  backups(): Promise<Record<string, unknown>[]> {
    return call(() => API.ClientSwitchAPI.Backups() as unknown as Promise<Record<string, unknown>[]>);
  },
};

// ---------- 账号数据迁移 ----------

export const dataMigrateApi = {
  /** 诊断：当前登录身份 + 各 user_id 的数据分布 */
  diagnose(): Promise<DataMigrationDiag | null> {
    return call(() => API.DataMigrateAPI.Diagnose());
  },
  /** 执行迁移：source 账号数据并入 target（自动备份，可回滚） */
  migrate(source: string, target: string): Promise<DataMigrationResult> {
    native();
    return API.DataMigrateAPI.Migrate(source, target).then((r) => r as DataMigrationResult);
  },
  backups(): Promise<string[]> {
    return call(() => API.DataMigrateAPI.Backups().then((r) => r ?? []));
  },
  rollback(tag: string): Promise<void> {
    native();
    return API.DataMigrateAPI.Rollback(tag);
  },
};

// ---------- 模型 ----------

export const modelsApi = {
  list(): Promise<ModelInfo[]> {
    return call(() => API.ModelsAPI.List());
  },
};

// ---------- 智能体接入 ----------

export const agentsApi = {
  list(): Promise<AgentTarget[]> {
    return call(() => API.AgentsAPI.List().then((r) => r ?? []));
  },
  apply(params: ApplyParams): Promise<AgentApplyResult> {
    native();
    return API.AgentsAPI.Apply(params).then((r) => r as AgentApplyResult);
  },
  backups(target: string): Promise<AgentBackup[]> {
    return call(() => API.AgentsAPI.Backups(target).then((r) => r ?? []));
  },
  restore(target: string, backupID: string): Promise<number> {
    native();
    return API.AgentsAPI.Restore(target, backupID);
  },
};

// ---------- 插件中心 ----------

export const pluginsApi = {
  statuses(): Promise<PluginStatus[]> {
    return call(() => API.PluginsAPI.Statuses().then((r) => r ?? []));
  },
  apply(plugin: string, ids: number[] = []): Promise<PluginApplyResult> {
    native();
    return API.PluginsAPI.Apply({ plugin, ids }).then((r) => r as PluginApplyResult);
  },
  remove(plugin: string, ids: number[] = []): Promise<PluginApplyResult> {
    native();
    return API.PluginsAPI.Remove({ plugin, ids }).then((r) => r as PluginApplyResult);
  },
  settings(): Promise<PluginSettings> {
    return call(() => API.PluginsAPI.Settings().then((r) => r ?? { rulesText: "", safetyAllowlist: [] }));
  },
  saveSettings(s: PluginSettings): Promise<void> {
    native();
    return API.PluginsAPI.SaveSettings(s);
  },
  slashCommands(): Promise<PluginSlashCommand[]> {
    return call(() => API.PluginsAPI.SlashCommands().then((r) => r ?? []));
  },
  defaultSettings(): Promise<PluginSettings> {
    return call(() =>
      API.PluginsAPI.DefaultSettings().then((r) => r ?? { rulesText: "", safetyAllowlist: [] }),
    );
  },
  backups(): Promise<PluginBackup[]> {
    return call(() => API.PluginsAPI.Backups().then((r) => r ?? []));
  },
  restore(target: string, backupID: string): Promise<number> {
    native();
    return API.PluginsAPI.Restore(target, backupID);
  },
};

// ---------- 日志 ----------

export const logsApi = {
  requestLogs(q: LogQuery = {}): Promise<RequestLogPage> {
    return call(() => API.LogsAPI.GetRequestLogs(q));
  },
  taskLogs(q: LogQuery = {}): Promise<TaskLogPage> {
    return call(() => API.LogsAPI.GetTaskLogs(q));
  },
  creditLogs(q: LogQuery = {}): Promise<CreditLogPage> {
    return call(() => API.LogsAPI.GetCreditLogs(q));
  },
  creditDetail(q: LogQuery = {}): Promise<CreditDetail> {
    return call(() => API.LogsAPI.GetCreditDetail(q));
  },
  auditLogs(q: LogQuery = {}): Promise<AuditLogPage> {
    return call(() => API.LogsAPI.GetAuditLogs(q));
  },
  export(format: "csv" | "json", scope: "request" | "task", q: LogQuery = {}): Promise<LogExportResult> {
    return call(() => API.LogsAPI.Export(format, scope, q));
  },
  clear(scope: "request" | "task" | "all"): Promise<number> {
    native();
    return API.LogsAPI.ClearLogs(scope);
  },
  exportDir(): Promise<string> {
    native();
    return API.LogsAPI.OpenExportDir();
  },
};

// ---------- 配置 ----------

export const configApi = {
  get(): Promise<GatewayConfig> {
    return call(() => API.ConfigAPI.Get());
  },
  defaults(): Promise<GatewayConfig> {
    return call(() => API.ConfigAPI.Defaults());
  },
  meta(): Promise<ConfigMeta> {
    return call(() => API.ConfigAPI.Meta());
  },
  update(config: GatewayConfig): Promise<void> {
    native();
    return API.ConfigAPI.Update(config);
  },
  rotateAPIKey(): Promise<string> {
    native();
    return API.ConfigAPI.RotateAPIKey();
  },
  backup(): Promise<string> {
    native();
    return API.ConfigAPI.Backup();
  },
  listBackups(): Promise<BackupItem[]> {
    return call(() => API.ConfigAPI.ListBackups());
  },
  restore(path: string): Promise<void> {
    native();
    return API.ConfigAPI.Restore(path);
  },
  /** 导出配置：弹出系统保存对话框（includeCredentials 时打包凭证），path 为空 = 用户取消 */
  export(includeCredentials = false, password = ""): Promise<ConfigExportResult> {
    native();
    return API.ConfigAPI.Export(includeCredentials, password).then((r) => r ?? { path: "", credentials: 0 });
  },
  import(json: string): Promise<void> {
    native();
    return API.ConfigAPI.Import(json);
  },
};

// ---------- 防休眠 ----------

export const powerApi = {
  setKeepAwake(on: boolean): Promise<void> {
    native();
    return API.PowerAPI.SetKeepAwake(on);
  },
  status(): Promise<AwakeStatus> {
    return call(() => API.PowerAPI.Status());
  },
};

// ---------- 开机自启 ----------

export const autoStartApi = {
  status(): Promise<AutoStartStatus> {
    return call(() => API.AutoStartAPI.Status());
  },
  set(on: boolean): Promise<void> {
    native();
    return API.AutoStartAPI.Set(on);
  },
};

// ---------- 客户端注入 ----------

export const injectApi = {
  status(): Promise<InjectStatus> {
    return call(() => API.InjectAPI.Status());
  },
  start(): Promise<void> {
    native();
    return API.InjectAPI.Start();
  },
  stop(): Promise<void> {
    native();
    return API.InjectAPI.Stop();
  },
  getConfig(): Promise<InjectConfig> {
    return call(() => API.InjectAPI.GetConfig());
  },
  updateConfig(patch: { enabled?: boolean; clientPath?: string; port?: number; dnd?: boolean }): Promise<void> {
    native();
    return API.InjectAPI.UpdateConfig(
      patch.enabled ?? null,
      patch.clientPath ?? null,
      patch.port ?? null,
      patch.dnd ?? null,
    );
  },
  accounts(): Promise<AccountBackup[]> {
    return call(() => API.InjectAPI.Accounts());
  },
  backup(name: string): Promise<AccountBackup> {
    return call(() => API.InjectAPI.Backup(name));
  },
  switchAccount(id: string): Promise<void> {
    native();
    return API.InjectAPI.Switch(id);
  },
  deleteBackup(id: string): Promise<void> {
    native();
    return API.InjectAPI.DeleteBackup(id);
  },
};

// ---------- 技能市场 ----------

export const skillsApi = {
  status(): Promise<SkillHubCLIStatus> {
    return call(() => API.SkillsAPI.Status());
  },
  installCLI(): Promise<SkillHubCLIStatus> {
    native();
    return API.SkillsAPI.InstallCLI();
  },
  browse(params: BrowseParams): Promise<SkillHubBrowseResult> {
    return call(() => API.SkillsAPI.Browse(params).then((r) => r as SkillHubBrowseResult));
  },
  targets(): Promise<SkillTargetInfo[]> {
    return call(() => API.SkillsAPI.Targets().then((r) => r ?? []));
  },
  install(params: SkillInstallParams): Promise<SkillInstallResult> {
    native();
    return API.SkillsAPI.Install(params).then((r) => r as SkillInstallResult);
  },
  installed(target: string): Promise<InstalledSkill[]> {
    return call(() => API.SkillsAPI.Installed(target).then((r) => r ?? []));
  },
  uninstall(target: string, dirName: string): Promise<void> {
    native();
    return API.SkillsAPI.Uninstall(target, dirName);
  },
  updates(force: boolean): Promise<SkillUpdatesResult> {
    return call(() => API.SkillsAPI.Updates(force).then((r) => r as SkillUpdatesResult));
  },
  update(target: string, key: string): Promise<SkillUpdateResult> {
    native();
    return API.SkillsAPI.Update(target, key).then((r) => r as SkillUpdateResult);
  },
  updateAll(): Promise<SkillUpdateResult[]> {
    native();
    return API.SkillsAPI.UpdateAll().then((r) => r ?? []);
  },
};

// ---------- 系统 ----------

export const systemApi = {
  info(): Promise<SystemInfo> {
    return call(() => API.SystemAPI.GetInfo());
  },
  checkUpdate(): Promise<UpdateInfo> {
    return call(() => API.SystemAPI.CheckUpdate());
  },
  downloadUpdate(url: string): Promise<string> {
    return call(() => API.SystemAPI.DownloadUpdate(url));
  },
  /** 安装已下载并校验通过的更新包，随后应用自动退出并替换重启 */
  installUpdate(filePath: string): Promise<void> {
    return call(() => API.SystemAPI.InstallUpdate(filePath));
  },
  /** 发送桌面测试通知（macOS 首次调用会触发系统授权弹窗） */
  sendTestNotify(): Promise<void> {
    return call(() => API.SystemAPI.SendTestNotify());
  },
  /** 立即执行一轮会话自动归档，返回本次归档数量 */
  sessionArchiveNow(): Promise<number> {
    return call(() => API.SystemAPI.SessionArchiveNow());
  },
};

// ---------- 聊天测试 ----------

export const chatApi = {
  send(params: ChatParams): Promise<ChatResult> {
    return call(() => API.ChatAPI.Send(params));
  },
  /** 停止当前进行中的对话（已生成内容保留） */
  abort(): Promise<boolean> {
    return call(() => API.ChatAPI.Abort());
  },
};

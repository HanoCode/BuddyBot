import { useCallback, useEffect, useMemo, useState } from "react";
import { useSearchParams } from "react-router-dom";
import {
  AlertTriangle, Archive, Bell, Copy, Download, ExternalLink, Eye, EyeOff, FolderOpen, Info, Loader2,
  RefreshCw, RotateCw, Save, ShieldCheck, Upload, X,
} from "lucide-react";
import { powerApi, injectApi, accountsApi, configApi, systemApi, dataMigrateApi } from "../services/api";
import { EVENT, onEvent } from "../services/events";
import { confirmDialog, toast } from "../components/common/Feedback";
import { EmptyBlock, ErrorBlock, LoadingBlock } from "../components/common/StateBlock";
import { broadcastRefresh, errText, useAsync } from "../hooks/useAsync";
import { ACCENTS, useAccent, useTheme } from "../hooks/useTheme";
import { getCloseBehavior, setCloseBehavior, type CloseBehavior } from "../hooks/useCloseBehavior";
import { setLang, t, useLang, useT } from "../i18n";
import type {
  AccountBackup, AwakeStatus, BackupItem, DataMigrationDiag, GatewayConfig, InjectConfig, InjectStatus,
  SchedulerStatus, SystemInfo, UpdateInfo,
} from "../types";

// ============================================================
// 系统设置
//
// 全部字段都来自 configApi.get()（真实 config.json），
// 「恢复默认」也由后端 configApi.defaults() 单点提供，
// 前端不再自行拼一份硬编码配置。
//
// i18n：NAV / TASK_TITLE / PROMPT_MODES 里的中文均为 i18n key（= zh 原文），
// 展示时经 t() 翻译；深链 ?nav= 参数沿用中文 key，切换语言不影响跳转。
// ============================================================

export const NAV = [
  "通用", "网关服务", "安全与访问", "提示词与模型", "会话粘性", "定时任务", "账号池策略", "状态镜像", "增强功能", "数据与备份", "关于",
] as const;
type NavKey = (typeof NAV)[number];

const TASK_TITLE: Record<string, string> = {
  checkin: "每日签到",
  travel: "猫猫旅行",
  keepalive: "保活任务",
  activity: "活跃地图",
  school: "开学季",
  cat: "夜猫子",
};

function Switch({ on, onChange }: { on: boolean; onChange?: () => void }) {
  return <button className={`switch${on ? " on" : ""}`} onClick={onChange} />;
}

interface Bundle {
  cfg: GatewayConfig;
  meta: Awaited<ReturnType<typeof configApi.meta>>;
  sys: SystemInfo;
  sched: SchedulerStatus;
  backups: BackupItem[];
}

export default function Settings() {
  const t = useT();
  const [nav, setNav] = useState<NavKey>("网关服务");
  const [searchParams] = useSearchParams();

  // 分区深链：/settings?nav=定时任务（⌘K 设置项直达入口）
  useEffect(() => {
    const q = searchParams.get("nav");
    if (q && (NAV as readonly string[]).includes(q)) setNav(q as NavKey);
  }, [searchParams]);

  const [draft, setDraft] = useState<GatewayConfig | null>(null);
  const [baseline, setBaseline] = useState("");
  const [saving, setSaving] = useState(false);
  const [showKey, setShowKey] = useState(false);
  const [ipText, setIpText] = useState("");
  const [blackText, setBlackText] = useState("");
  const [busy, setBusy] = useState<string | null>(null);

  const bundle = useAsync<Bundle>(async () => {
    const [cfg, meta, sys, sched, backups] = await Promise.all([
      configApi.get(),
      configApi.meta(),
      systemApi.info(),
      accountsApi.taskStatus(),
      configApi.listBackups(),
    ]);
    return { cfg, meta, sys, sched, backups };
  }, []);

  // 每次拿到真实配置就重置草稿与基线（保存成功后也走这里）
  useEffect(() => {
    const cfg = bundle.data?.cfg;
    if (!cfg) return;
    setDraft(structuredClone(cfg));
    setBaseline(JSON.stringify(cfg));
    setIpText((cfg.security?.ip_whitelist ?? []).join("\n"));
    setBlackText((cfg.security?.ip_blacklist ?? []).join("\n"));
  }, [bundle.data]);

  const dirty = draft !== null && JSON.stringify(draft) !== baseline;

  const save = useCallback(async () => {
    if (!draft) return;
    if (!draft.listen.trim()) {
      toast.warn(t("监听地址不能为空"), t("例如 :7863"));
      return;
    }
    setSaving(true);
    try {
      await configApi.update(draft);
      setBaseline(JSON.stringify(draft));
      toast.success(t("设置已保存"), t("已写入 config.json；网关运行中会自动热重启生效"));
      broadcastRefresh();
      await bundle.reload();
    } catch (e) {
      toast.error(t("保存失败"), errText(e));
    } finally {
      setSaving(false);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [draft, bundle]);

  const resetDefaults = async () => {
    const ok = await confirmDialog({
      title: t("恢复默认配置"),
      desc: t("将用后端默认值覆盖当前配置（含新的随机根密钥）。此操作不会立即生效，需再点「保存设置」。"),
      danger: true,
      confirmText: t("载入默认值"),
    });
    if (!ok) return;
    try {
      const def = await configApi.defaults();
      setDraft(def);
      setIpText((def.security?.ip_whitelist ?? []).join("\n"));
      setBlackText((def.security?.ip_blacklist ?? []).join("\n"));
      toast.info(t("已载入默认值"), t("确认无误后点击「保存设置」写入磁盘"));
    } catch (e) {
      toast.error(t("获取默认配置失败"), errText(e));
    }
  };

  if (bundle.error && !bundle.data) {
    return (
      <section className="page">
        <div className="page-head">
          <div>
            <h1>{t("系统设置")}</h1>
            <p>{t("网关 / 安全 / 定时任务 / 账号池 / 备份")}</p>
          </div>
        </div>
        <ErrorBlock message={bundle.error} onRetry={() => void bundle.reload()} />
      </section>
    );
  }

  return (
    <section className="page">
      <div className="page-head">
        <div>
          <h1>{t("系统设置")}</h1>
          <p>{t("网关 / 安全 / 定时任务 / 账号池 / 备份 · 保存后落盘，网关运行中自动热重启")}</p>
        </div>
        {dirty && (
          <span className="badge b-amber">
            <span className="d" />
            {t("有未保存的修改")}
          </span>
        )}
      </div>

      <div className="set-grid">
        <nav className="set-nav">
          {NAV.map((n) => (
            <button key={n} className={nav === n ? "on" : ""} onClick={() => setNav(n)}>
              {t(n)}
            </button>
          ))}
        </nav>

        <div>
          {bundle.loading && !draft ? (
            <LoadingBlock label={t("读取配置")} />
          ) : draft && bundle.data ? (
            <>
              {nav === "通用" && <GeneralSection />}

              {nav === "网关服务" && (
                <GatewaySection
                  draft={draft}
                  setDraft={setDraft}
                  showKey={showKey}
                  setShowKey={setShowKey}
                  meta={bundle.data.meta}
                />
              )}

              {nav === "安全与访问" && (
                <SecuritySection
                  draft={draft}
                  setDraft={setDraft}
                  ipText={ipText}
                  setIpText={setIpText}
                  blackText={blackText}
                  setBlackText={setBlackText}
                />
              )}

              {nav === "提示词与模型" && <PromptSection draft={draft} setDraft={setDraft} />}

              {nav === "会话粘性" && <SessionSection draft={draft} setDraft={setDraft} />}

              {nav === "定时任务" && <ScheduleSection draft={draft} setDraft={setDraft} sched={bundle.data.sched} />}

              {nav === "账号池策略" && <PoolSection draft={draft} setDraft={setDraft} />}

              {nav === "状态镜像" && <RedisSection draft={draft} setDraft={setDraft} />}

              {nav === "增强功能" && <EnhanceSection />}

              {nav === "数据与备份" && (
                <DataSection
                  meta={bundle.data.meta}
                  backups={bundle.data.backups}
                  busy={busy}
                  setBusy={setBusy}
                  reload={bundle.reload}
                  loadDraft={async () => {
                    const cfg = await configApi.get();
                    setDraft(structuredClone(cfg));
                    setBaseline(JSON.stringify(cfg));
                    setIpText((cfg.security?.ip_whitelist ?? []).join("\n"));
                    setBlackText((cfg.security?.ip_blacklist ?? []).join("\n"));
                  }}
                />
              )}

              {nav === "关于" && <AboutSection sys={bundle.data.sys} meta={bundle.data.meta} />}

              <div className="flex" style={{ justifyContent: "flex-end", gap: 10, marginTop: 4 }}>
                <span className="muted">{dirty ? t("有未保存的修改") : t("配置已与磁盘同步")}</span>
                <button className="btn btn-ghost" onClick={resetDefaults}>
                  {t("恢复默认")}
                </button>
                <button className="btn btn-primary" disabled={saving || !dirty} onClick={save}>
                  {saving ? <Loader2 size={13} className="spin" /> : <Save size={13} strokeWidth={2} />} {t("保存设置")}
                </button>
              </div>
            </>
          ) : null}
        </div>
      </div>
    </section>
  );
}

// ---------- 通用（语言 / 主题 / 强调色） ----------

function GeneralSection() {
  const [lang] = useLang();
  const t = useT();
  const [theme, toggleTheme] = useTheme();
  const [accent, setAccent] = useAccent();
  const [closeBehavior, setCloseBehaviorState] = useState<CloseBehavior>(getCloseBehavior());

  const change = (next: "zh" | "en") => {
    if (next === lang) return;
    setLang(next);
    toast.success(next === "zh" ? t("已切换为中文界面") : t("已切换为英文界面"));
  };

  const closeBehaviorOpts: { id: CloseBehavior; label: string }[] = [
    { id: "ask", label: t("每次询问") },
    { id: "tray", label: t("最小化到托盘") },
    { id: "quit", label: t("退出程序") },
  ];

  return (
    <div className="card set-group">
      <div className="card-h">
        <div>
          <h3>{t("通用")}</h3>
          <div className="sub">{t("界面偏好 · 语言 / 主题 / 强调色")}</div>
        </div>
      </div>
      <div className="card-b">
        <SetItem title={t("语言")} desc={t("界面显示语言")}>
          <div className="seg">
            <button className={lang === "zh" ? "on" : ""} onClick={() => change("zh")}>
              {t("简体中文")}
            </button>
            <button className={lang === "en" ? "on" : ""} onClick={() => change("en")}>
              {t("English")}
            </button>
          </div>
        </SetItem>
        <SetItem title={t("主题")} desc={t("浅色 / 深色，也可用标题栏按钮或命令面板切换")}>
          <div className="seg">
            <button className={theme === "light" ? "on" : ""} onClick={() => { if (theme !== "light") toggleTheme(); }}>
              {t("浅色")}
            </button>
            <button className={theme === "dark" ? "on" : ""} onClick={() => { if (theme !== "dark") toggleTheme(); }}>
              {t("深色")}
            </button>
          </div>
        </SetItem>
        <SetItem title={t("强调色")} desc={t("主色族即时生效，选择保存在本机")}>
          <div className="flex" style={{ gap: 8 }}>
            {ACCENTS.map((a) => (
              <button
                key={a.id}
                data-tip={t(a.name)}
                onClick={() => setAccent(a.id)}
                aria-label={t(a.name)}
                style={{
                  width: 22, height: 22, borderRadius: 8, background: a.color, cursor: "pointer",
                  border: accent === a.id ? "2px solid var(--text-1)" : "2px solid transparent",
                  outline: "2px solid var(--outline)", outlineOffset: 1,
                }}
              />
            ))}
          </div>
        </SetItem>
        <SetItem title={t("关闭按钮行为")} desc={t("点击窗口关闭按钮时的动作；选「退出程序」时仍会弹窗确认")}>
          <div className="seg">
            {closeBehaviorOpts.map((o) => (
              <button
                key={o.id}
                className={closeBehavior === o.id ? "on" : ""}
                onClick={() => { setCloseBehavior(o.id); setCloseBehaviorState(o.id); }}
              >
                {o.label}
              </button>
            ))}
          </div>
        </SetItem>
      </div>
    </div>
  );
}

// ---------- 网关服务 ----------

function GatewaySection({
  draft, setDraft, showKey, setShowKey, meta,
}: {
  draft: GatewayConfig;
  setDraft: (c: GatewayConfig) => void;
  showKey: boolean;
  setShowKey: (v: boolean) => void;
  meta: Bundle["meta"];
}) {
  const t = useT();
  const [rotating, setRotating] = useState(false);

  const rotate = async () => {
    const ok = await confirmDialog({
      title: t("轮换根密钥"),
      desc: t("将生成新的根密钥并立即写入配置。所有仍在使用旧密钥的客户端会立刻收到 401，需要同步更新。"),
      danger: true,
      confirmText: t("生成并启用新密钥"),
    });
    if (!ok) return;
    setRotating(true);
    try {
      const next = await configApi.rotateAPIKey();
      setDraft({ ...draft, api_key: next });
      toast.success(t("根密钥已轮换"), t("已写入配置；请同步更新客户端"));
      broadcastRefresh();
    } catch (e) {
      toast.error(t("轮换失败"), errText(e));
    } finally {
      setRotating(false);
    }
  };

  return (
    <div className="card set-group">
      <div className="card-h">
        <div>
          <h3>{t("网关服务")}</h3>
          <div className="sub">config.json · listen / api_key / auth_dir / state_file</div>
        </div>
      </div>
      <div className="card-b">
        <SetItem title={t("监听地址")} desc={t("网关 HTTP 服务监听的地址与端口，形如 :7863 或 127.0.0.1:7863")}>
          <input
            className="mini-input"
            style={{ width: 170 }}
            value={draft.listen}
            onChange={(e) => setDraft({ ...draft, listen: e.target.value })}
          />
        </SetItem>

        <SetItem title={t("根密钥")} desc={t("客户端调用网关所需的鉴权密钥；留空保存表示不修改")}>
          <div className="flex">
            <input
              className="mini-input"
              style={{ width: 190 }}
              type={showKey ? "text" : "password"}
              value={draft.api_key}
              placeholder={t("留空 = 不修改")}
              onChange={(e) => setDraft({ ...draft, api_key: e.target.value })}
            />
            <button className="icon-btn" data-tip={showKey ? t("隐藏") : t("显示")} onClick={() => setShowKey(!showKey)}>
              {showKey ? <EyeOff size={14} strokeWidth={2} /> : <Eye size={14} strokeWidth={2} />}
            </button>
            <button
              className="icon-btn"
              data-tip={t("复制")}
              onClick={() =>
                navigator.clipboard.writeText(draft.api_key).then(
                  () => toast.success(t("根密钥已复制")),
                  () => toast.error(t("复制失败"), t("当前环境不允许访问剪贴板")),
                )
              }
            >
              <Copy size={14} strokeWidth={2} />
            </button>
            <button className="btn btn-ghost sm" disabled={rotating} onClick={rotate}>
              {rotating ? <Loader2 size={13} className="spin" /> : <RotateCw size={13} strokeWidth={2} />} {t("轮换")}
            </button>
          </div>
        </SetItem>

        <SetItem title={t("凭证目录")} desc={t("账号凭证文件所在目录，账号列表即扫描该目录得到")}>
          <input
            className="mini-input"
            style={{ width: 300 }}
            value={draft.auth_dir}
            onChange={(e) => setDraft({ ...draft, auth_dir: e.target.value })}
          />
        </SetItem>

        <SetItem title={t("状态文件")} desc={t("账号池运行态（熔断、失败计数、最近活动）持久化路径")}>
          <input
            className="mini-input"
            style={{ width: 300 }}
            value={draft.state_file}
            onChange={(e) => setDraft({ ...draft, state_file: e.target.value })}
          />
        </SetItem>

        <SetItem title={t("配置文件位置")} desc={t("保存后立即写入该文件")}>
          <PathChip path={meta.configPath} />
        </SetItem>
        <SetItem title={t("数据目录")} desc={t("存储文件（请求日志、任务日志、密钥、运行态）所在目录")}>
          <PathChip path={meta.dataDir} />
        </SetItem>
      </div>
    </div>
  );
}

// ---------- 安全与访问 ----------

function SecuritySection({
  draft, setDraft, ipText, setIpText, blackText, setBlackText,
}: {
  draft: GatewayConfig;
  setDraft: (c: GatewayConfig) => void;
  ipText: string;
  setIpText: (v: string) => void;
  blackText: string;
  setBlackText: (v: string) => void;
}) {
  const t = useT();
  const sec = draft.security ?? { require_ip_allowlist: false, ip_whitelist: [], ip_blacklist: [], read_only: false };
  const entries = useMemo(() => parseList(ipText), [ipText]);
  const blacks = useMemo(() => parseList(blackText), [blackText]);

  return (
    <div className="card set-group">
      <div className="card-h">
        <div>
          <h3>{t("安全与访问")}</h3>
          <div className="sub">security · {t("IP 黑名单 / 白名单 / 只读模式")}</div>
        </div>
      </div>
      <div className="card-b">
        <SetItem
          title={t("只读模式")}
          desc={t("开启后管理侧一切变更（配置、密钥、账号、日志清空）都会被拒绝，仅保留查询与网关转发")}
        >
          <Switch
            on={sec.read_only ?? false}
            onChange={() => setDraft({ ...draft, security: { ...sec, read_only: !(sec.read_only ?? false) } })}
          />
        </SetItem>

        <div className="set-vert">
          <div className="info">
            <div className="t">{t("IP 黑名单")}</div>
            <div className="d">
              {t("每行一条，支持单个 IP 或 CIDR（10.0.0.0/8）；命中黑名单的请求最先被拒绝，优先于白名单与密钥校验。")}
            </div>
          </div>
          <textarea
            className="input"
            rows={4}
            style={{ fontFamily: "var(--mono)", fontSize: 12.5 }}
            placeholder={"192.0.2.1\n203.0.113.0/24"}
            value={blackText}
            onChange={(e) => {
              setBlackText(e.target.value);
              setDraft({ ...draft, security: { ...sec, ip_blacklist: parseList(e.target.value) } });
            }}
          />
          <div className="hint">
            {blacks.length === 0
              ? t("当前为空：不做 IP 级封禁。")
              : t("已配置 {n} 条：{list}", { n: blacks.length, list: blacks.join("、") })}
          </div>
        </div>

        <SetItem title={t("启用 IP 白名单")} desc={t("开启后，来源 IP 不在白名单内的请求一律返回 403")}>
          <Switch
            on={sec.require_ip_allowlist}
            onChange={() =>
              setDraft({ ...draft, security: { ...sec, require_ip_allowlist: !sec.require_ip_allowlist } })
            }
          />
        </SetItem>

        <div className="set-vert">
          <div className="info">
            <div className="t">{t("白名单条目")}</div>
            <div className="d">
              {t("每行一条，支持单个 IP（127.0.0.1）或 CIDR（192.168.1.0/24）；")}<code>::1</code> {t("可放行本机 IPv6 回环。")}
            </div>
          </div>
          <textarea
            className="input"
            rows={6}
            style={{ fontFamily: "var(--mono)", fontSize: 12.5 }}
            placeholder={"127.0.0.1\n::1\n192.168.1.0/24"}
            value={ipText}
            onChange={(e) => {
              setIpText(e.target.value);
              setDraft({ ...draft, security: { ...sec, ip_whitelist: parseList(e.target.value) } });
            }}
          />
          <div className="hint">
            {entries.length === 0
              ? t("当前为空：开启白名单后将拒绝所有来源，请至少配置一条。")
              : t("已配置 {n} 条：{list}", { n: entries.length, list: entries.join("、") })}
          </div>
        </div>

        {sec.require_ip_allowlist && entries.length === 0 && (
          <div className="notice warn">
            <AlertTriangle size={15} />
            <span>{t("已开启白名单但条目为空，所有客户端都会被拒绝。请补充条目或关闭该开关。")}</span>
          </div>
        )}

        <div className="notice">
          <ShieldCheck size={15} />
          <span>
            {t("密钥级白名单不在此处配置：在「密钥管理」中为单个密钥设置来源 IP 限制，粒度更细。")}
          </span>
        </div>
      </div>
    </div>
  );
}

// ---------- 会话粘性 ----------

function SessionSection({ draft, setDraft }: { draft: GatewayConfig; setDraft: (c: GatewayConfig) => void }) {
  const t = useT();
  const ss = draft.session_sticky ?? { enabled: false, ttl: "30m" };
  return (
    <div className="card set-group">
      <div className="card-h">
        <div>
          <h3>{t("会话粘性")}</h3>
          <div className="sub">session_sticky · {t("同一会话固定复用同一账号")}</div>
        </div>
      </div>
      <div className="card-b">
        <SetItem
          title={t("启用会话粘性")}
          desc={t("开启后，携带同一 session id 的请求会被路由到同一账号，利于命中上游侧的前缀缓存")}
        >
          <Switch on={ss.enabled} onChange={() => setDraft({ ...draft, session_sticky: { ...ss, enabled: !ss.enabled } })} />
        </SetItem>
        <SetItem title={t("粘性有效期")} desc={t("超过该时长未活动的会话绑定自动失效，形如 30m / 2h")}>
          <input
            className="mini-input"
            style={{ width: 110 }}
            value={ss.ttl}
            onChange={(e) => setDraft({ ...draft, session_sticky: { ...ss, ttl: e.target.value } })}
          />
        </SetItem>
        <div className="notice">
          <Info size={15} />
          <span>
            {t("会话标识由客户端通过请求体")} <code>session_id</code> {t("字段或")} <code>X-Session-Id</code> {t("头传入；")}
            {t("请求日志中的「会话」列即取自该字段。")}
          </span>
        </div>
      </div>
    </div>
  );
}

// ---------- 定时任务 ----------

function ScheduleSection({
  draft, setDraft, sched,
}: {
  draft: GatewayConfig;
  setDraft: (c: GatewayConfig) => void;
  sched: SchedulerStatus;
}) {
  const t = useT();
  const sc = draft.schedule;
  const nextOf = (type: string) => sched.nextRuns?.find((n) => n.type === type);
  const [testingNotify, setTestingNotify] = useState(false);

  const setHours = (
    key:
      | "checkin_hours" | "travel_hours" | "keepalive_hours"
      | "activity_hours" | "school_hours" | "cat_hours",
    hours: number[],
  ) => setDraft({ ...draft, schedule: { ...sc, [key]: hours } });

  const setEnabled = (
    key:
      | "checkin_enabled" | "travel_enabled" | "keepalive_enabled"
      | "activity_enabled" | "school_enabled" | "cat_enabled",
    v: boolean,
  ) => setDraft({ ...draft, schedule: { ...sc, [key]: v } });

  const sendTestNotify = async () => {
    setTestingNotify(true);
    try {
      await systemApi.sendTestNotify();
      toast.success(t("测试通知已发送"), t("若未看到横幅，请检查系统设置中的通知权限"));
    } catch (e) {
      toast.error(t("测试通知发送失败"), errText(e));
    } finally {
      setTestingNotify(false);
    }
  };

  return (
    <div className="card set-group">
      <div className="card-h">
        <div>
          <h3>{t("定时任务")}</h3>
          <div className="sub">
            {t("每日按配置的整点触发 · 调度器{s}", { s: sched.running ? t("运行中") : t("未运行") })}
          </div>
        </div>
        <span className={`badge ${sched.running ? "b-green" : "b-gray"}`}>
          <span className="d" />
          {sched.running ? t("已启动") : t("未启动")}
        </span>
      </div>
      <div className="card-b">
        <TaskRow
          title={t(TASK_TITLE.checkin)}
          desc={t("为账号池内全部可用账号执行签到")}
          hours={sc.checkin_hours ?? []}
          onHours={(h) => setHours("checkin_hours", h)}
          enabled={sc.checkin_enabled}
          onEnabled={(v) => setEnabled("checkin_enabled", v)}
          next={nextOf("checkin")?.next}
        />
        <TaskRow
          title={t(TASK_TITLE.travel)}
          desc={t("为账号池内全部可用账号执行旅行任务")}
          hours={sc.travel_hours ?? []}
          onHours={(h) => setHours("travel_hours", h)}
          enabled={sc.travel_enabled}
          onEnabled={(v) => setEnabled("travel_enabled", v)}
          next={nextOf("travel")?.next}
        />
        <TaskRow
          title={t(TASK_TITLE.keepalive)}
          desc={t("保持账号活跃，避免因长期静默失效")}
          hours={sc.keepalive_hours ?? []}
          onHours={(h) => setHours("keepalive_hours", h)}
          enabled={sc.keepalive_enabled}
          onEnabled={(v) => setEnabled("keepalive_enabled", v)}
          next={nextOf("keepalive")?.next}
        />

        <div className="set-vert" style={{ borderTop: "1px dashed var(--border)", paddingTop: 12 }}>
          <div className="info">
            <div className="t">{t("扩展任务")}</div>
            <div className="d">{t("对齐 workbuddy2api 的运营型定时任务；活跃地图/夜猫子全区域可用，开学季仅国内版（CN）")}</div>
          </div>
        </div>
        <TaskRow
          title={t(TASK_TITLE.activity)}
          desc={t("按事件链上报活跃度，维持账号活跃画像")}
          hours={sc.activity_hours ?? []}
          onHours={(h) => setHours("activity_hours", h)}
          enabled={sc.activity_enabled ?? false}
          onEnabled={(v) => setEnabled("activity_enabled", v)}
          next={nextOf("activity")?.next}
        />
        <TaskRow
          title={t(TASK_TITLE.school)}
          desc={t("开学季活动任务（仅 CN 区域账号生效）")}
          hours={sc.school_hours ?? []}
          onHours={(h) => setHours("school_hours", h)}
          enabled={sc.school_enabled ?? false}
          onEnabled={(v) => setEnabled("school_enabled", v)}
          next={nextOf("school")?.next}
        />
        <TaskRow
          title={t(TASK_TITLE.cat)}
          desc={t("夜猫子任务：在 23:00–08:00 窗口内完成夜间对话（仅 CN 区域账号生效）")}
          hours={sc.cat_hours ?? []}
          onHours={(h) => setHours("cat_hours", h)}
          enabled={sc.cat_enabled ?? false}
          onEnabled={(v) => setEnabled("cat_enabled", v)}
          next={nextOf("cat")?.next}
        />

        <div className="set-vert" style={{ borderTop: "1px dashed var(--border)", paddingTop: 12 }}>
          <div className="info">
            <div className="t">{t("时间预算")}</div>
            <div className="d">{t("单轮全池任务的最长执行时间；超预算后剩余账号记为「跳过」，防止长任务跑穿占用调度窗口")}</div>
          </div>
          <div className="flex" style={{ gap: 8, alignItems: "center" }}>
            <input
              className="mini-input"
              style={{ width: 90 }}
              type="number"
              min={-1}
              max={720}
              value={sc.run_budget_minutes ?? 0}
              onChange={(e) => setDraft({ ...draft, schedule: { ...sc, run_budget_minutes: Number(e.target.value) } })}
            />
            <span className="muted" style={{ fontSize: 12 }}>{t("分钟（0 = 默认 10 分钟，负数 = 不限制）")}</span>
          </div>
        </div>

        <div className="set-vert" style={{ borderTop: "1px dashed var(--border)", paddingTop: 12 }}>
          <div className="info">
            <div className="t">{t("定时刷新")}</div>
            <div className="d">
              {t("按固定间隔自动刷新需要手工刷新的内容：各在线账号的积分余额与上游模型目录")}
              {t("（等效于定时执行「查余额 / 深度刷新」；token 轮换仍由保活任务按临期窗口处理）")}
            </div>
          </div>
          <div className="flex" style={{ gap: 8, alignItems: "center" }}>
            <Switch
              on={sc.refresh?.enabled ?? false}
              onChange={() => setDraft({ ...draft, schedule: { ...sc, refresh: { ...(sc.refresh ?? { enabled: false, interval_minutes: 0 }), enabled: !(sc.refresh?.enabled ?? false) } } })}
            />
            <span className="muted" style={{ fontSize: 12 }}>
              {(sc.refresh?.enabled ?? false) ? t("已开启") : t("已关闭")}
            </span>
            <input
              className="mini-input"
              style={{ width: 90 }}
              type="number"
              min={5}
              max={1440}
              value={sc.refresh?.interval_minutes ?? 0}
              onChange={(e) => setDraft({ ...draft, schedule: { ...sc, refresh: { ...(sc.refresh ?? { enabled: true, interval_minutes: 0 }), interval_minutes: Number(e.target.value) } } })}
            />
            <span className="muted" style={{ fontSize: 12 }}>{t("分钟（0 = 默认 30 分钟，合法范围 5-1440）")}</span>
          </div>
        </div>

        <div className="set-vert" style={{ borderTop: "1px dashed var(--border)", paddingTop: 12 }}>
          <div className="info">
            <div className="t">{t("系统级桌面通知")}</div>
            <div className="d">
              {t("推送到系统通知中心（macOS 通知中心 / Windows Toast / Linux 桌面通知）：")}
              {t("账号掉线需重登、落盘失败与网关错误、注入中断、任务结果摘要（摘要同时遵循下方推送开关）。")}
              {t("macOS 首次触发时会请求通知权限；需以打包后的应用运行")}
            </div>
          </div>
          <div className="flex" style={{ gap: 8, alignItems: "center" }}>
            <Switch
              on={sc.notify?.desktop ?? true}
              onChange={() => setDraft({ ...draft, schedule: { ...sc, notify: { ...(sc.notify ?? {}), desktop: !(sc.notify?.desktop ?? true) } } })}
            />
            <span className="muted" style={{ fontSize: 12 }}>
              {(sc.notify?.desktop ?? true) ? t("已开启") : t("已关闭")}
            </span>
            <button
              className="btn btn-ghost"
              style={{ marginLeft: 8, display: "inline-flex", alignItems: "center", gap: 6 }}
              disabled={testingNotify}
              onClick={sendTestNotify}
            >
              {testingNotify ? <Loader2 size={13} className="spin" /> : <Bell size={13} />}
              {t("发送测试通知")}
            </button>
          </div>
        </div>

        <div className="set-vert" style={{ borderTop: "1px dashed var(--border)", paddingTop: 12 }}>
          <div className="info">
            <div className="t">{t("任务完成推送")}</div>
            <div className="d">{t("每轮任务结束后推送结果摘要到 PushPlus（微信公众号）或 Bark（iOS）；网络失败不影响任务本身")}</div>
          </div>
          <label className="flex" style={{ gap: 8, alignItems: "center", cursor: "pointer" }}>
            <input
              type="checkbox"
              checked={sc.notify?.enabled ?? false}
              onChange={(e) => setDraft({ ...draft, schedule: { ...sc, notify: { ...(sc.notify ?? {}), enabled: e.target.checked } } })}
            />
            <span style={{ fontSize: 12.5 }}>{t("启用推送")}</span>
          </label>
          <label className="flex" style={{ gap: 8, alignItems: "center", cursor: "pointer" }}>
            <input
              type="checkbox"
              checked={sc.notify?.only_failures ?? false}
              onChange={(e) => setDraft({ ...draft, schedule: { ...sc, notify: { ...(sc.notify ?? {}), only_failures: e.target.checked } } })}
            />
            <span style={{ fontSize: 12.5 }}>{t("仅失败时推送")}</span>
          </label>
          <div className="flex" style={{ gap: 8, alignItems: "center" }}>
            <span className="muted" style={{ fontSize: 12, width: 96 }}>PushPlus Token</span>
            <input
              className="mini-input"
              style={{ width: 260 }}
              type="password"
              placeholder={t("留空 = 不用 PushPlus")}
              value={sc.notify?.pushplus_token ?? ""}
              onChange={(e) => setDraft({ ...draft, schedule: { ...sc, notify: { ...(sc.notify ?? {}), pushplus_token: e.target.value } } })}
            />
          </div>
          <div className="flex" style={{ gap: 8, alignItems: "center" }}>
            <span className="muted" style={{ fontSize: 12, width: 96 }}>{t("Bark 地址")}</span>
            <input
              className="mini-input"
              style={{ width: 260 }}
              type="text"
              placeholder={t("形如 https://api.day.app/你的Key")}
              value={sc.notify?.bark_url ?? ""}
              onChange={(e) => setDraft({ ...draft, schedule: { ...sc, notify: { ...(sc.notify ?? {}), bark_url: e.target.value } } })}
            />
          </div>
        </div>

        <div className="notice">
          <Info size={15} />
          <span>
            {t("执行结果逐账号写入任务日志。账号状态异常（凭证不可用、token 过期、熔断冷却）会被如实记为失败或跳过；")}
            {t("需要上游接口的动作在未接入时会记为「跳过」并写明原因，不会伪造成功。")}
          </span>
        </div>
      </div>
    </div>
  );
}

function TaskRow({
  title, desc, hours, onHours, enabled, onEnabled, next,
}: {
  title: string;
  desc: string;
  hours: number[];
  onHours: (h: number[]) => void;
  enabled: boolean;
  onEnabled: (v: boolean) => void;
  next?: string;
}) {
  const t = useT();
  return (
    <div className="set-vert">
      <div className="flex" style={{ justifyContent: "space-between", alignItems: "flex-start", gap: 16 }}>
        <div className="info">
          <div className="t">{title}</div>
          <div className="d">{desc}</div>
        </div>
        <Switch on={enabled} onChange={() => onEnabled(!enabled)} />
      </div>
      <div className="flex" style={{ justifyContent: "space-between", alignItems: "center", gap: 16 }}>
        <span className="hint" style={{ margin: 0 }}>
          {t("下次执行：")}{enabled ? nextDisplay(next) : t("已禁用")}
        </span>
        <HourEditor hours={hours} onChange={onHours} disabled={!enabled} />
      </div>
    </div>
  );
}

function HourEditor({ hours, onChange, disabled }: { hours: number[]; onChange: (h: number[]) => void; disabled?: boolean }) {
  const t = useT();
  const sorted = [...hours].sort((a, b) => a - b);
  const avail = Array.from({ length: 24 }, (_, i) => i).filter((h) => !hours.includes(h));
  return (
    <div className="hour-edit">
      {sorted.map((h) => (
        <span className="chip" key={h}>
          {String(h).padStart(2, "0")}:00
          {!disabled && (
            <button
              className="chip-x"
              data-tip={t("移除该时点")}
              onClick={() => onChange(sorted.filter((x) => x !== h))}
            >
              <X size={10} strokeWidth={3} />
            </button>
          )}
        </span>
      ))}
      {sorted.length === 0 && <span className="hint" style={{ margin: 0 }}>{t("未设置时点，任务不会触发")}</span>}
      {!disabled && avail.length > 0 && (
        <select
          className="mini-input"
          style={{ width: 108, flex: "none" }}
          value=""
          onChange={(e) => onChange([...sorted, Number(e.target.value)])}
        >
          <option value="">＋ {t("添加时点")}</option>
          {avail.map((h) => (
            <option key={h} value={h}>
              {String(h).padStart(2, "0")}:00
            </option>
          ))}
        </select>
      )}
    </div>
  );
}

// ---------- 账号池策略 ----------

function PoolSection({ draft, setDraft }: { draft: GatewayConfig; setDraft: (c: GatewayConfig) => void }) {
  const t = useT();
  const pool = draft.pool;
  return (
    <div className="card set-group">
      <div className="card-h">
        <div>
          <h3>{t("账号池策略")}</h3>
          <div className="sub">pool · {t("并发与熔断")}</div>
        </div>
      </div>
      <div className="card-b">
        <SetItem title={t("单账号最大并发")} desc={t("同一账号同时在途请求数上限，超出后请求会被排队到其它账号")}>
          <input
            className="mini-input"
            type="number"
            min={1}
            value={pool.max_in_flight}
            onChange={(e) => setDraft({ ...draft, pool: { ...pool, max_in_flight: Number(e.target.value) || 1 } })}
          />
        </SetItem>
        <SetItem title={t("熔断阈值")} desc={t("账号连续失败达到该次数后进入熔断冷却")}>
          <input
            className="mini-input"
            type="number"
            min={1}
            value={pool.breaker_threshold}
            onChange={(e) => setDraft({ ...draft, pool: { ...pool, breaker_threshold: Number(e.target.value) || 1 } })}
          />
        </SetItem>
        <SetItem title={t("基础冷却时长")} desc={t("熔断后的基础冷却时长，形如 30m / 1h")}>
          <input
            className="mini-input"
            style={{ width: 110 }}
            value={pool.breaker_cooldown}
            onChange={(e) => setDraft({ ...draft, pool: { ...pool, breaker_cooldown: e.target.value } })}
          />
        </SetItem>
        <SetItem title={t("冷却上限")} desc={t("重复熔断时冷却时长按倍数递增，但不超过该上限，形如 30m / 2h")}>
          <input
            className="mini-input"
            style={{ width: 110 }}
            value={pool.cooldown_max ?? ""}
            placeholder={t("如 30m")}
            onChange={(e) => setDraft({ ...draft, pool: { ...pool, cooldown_max: e.target.value } })}
          />
        </SetItem>
        <div className="notice">
          <Info size={15} />
          <span>
            {t("当前熔断与并发统计实时反映在「仪表盘 → 账号池状态」与「账号管理」的账号详情中；")}
            {t("冷却到期后由定时任务自动恢复账号。")}
          </span>
        </div>
      </div>
    </div>
  );
}

// ---------- 提示词与模型 ----------

const PROMPT_MODES = [
  { value: "passthrough", label: "透传", desc: "客户端发什么就转什么" },
  { value: "custom", label: "替换", desc: "丢弃客户端 system，统一改用下方提示词" },
  { value: "append", label: "追加", desc: "保留客户端 system，并在其后追加下方提示词" },
];

/** "key=value" 行 ↔ Record 的互转（值为空时用 "key=" 表示） */
function mapToText(map: Record<string, unknown> | null | undefined): string {
  return Object.entries(map ?? {})
    .map(([k, v]) => `${k}=${String(v ?? "")}`)
    .join("\n");
}

function textToMap(text: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const line of text.split("\n")) {
    const s = line.trim();
    if (!s) continue;
    const i = s.indexOf("=");
    if (i <= 0) continue;
    out[s.slice(0, i).trim()] = s.slice(i + 1).trim();
  }
  return out;
}

function textToNumMap(text: string): Record<string, number> {
  const out: Record<string, number> = {};
  for (const [k, v] of Object.entries(textToMap(text))) {
    const n = Number(v);
    if (Number.isFinite(n)) out[k] = n;
  }
  return out;
}

function PromptSection({ draft, setDraft }: { draft: GatewayConfig; setDraft: (c: GatewayConfig) => void }) {
  const t = useT();
  const pc = draft.prompt ?? { mode: "passthrough", text: "" };
  const mc = draft.models ?? { aliases: {}, rates: {}, groups: {} };

  return (
    <div className="card set-group">
      <div className="card-h">
        <div>
          <h3>{t("提示词与模型")}</h3>
          <div className="sub">prompt · {t("系统提示词三模式")} · models · {t("别名映射 / 积分倍率 / 分组")}</div>
        </div>
      </div>
      <div className="card-b">
        <div className="set-vert">
          <div className="info">
            <div className="t">{t("系统提示词模式")}</div>
            <div className="d">{t("网关在转发请求时对 system 消息的统一处理策略")}</div>
          </div>
          <div className="seg">
            {PROMPT_MODES.map((m) => (
              <button
                key={m.value}
                className={pc.mode === m.value ? "on" : ""}
                data-tip={t(m.desc)}
                onClick={() => setDraft({ ...draft, prompt: { ...pc, mode: m.value } })}
              >
                {t(m.label)}
              </button>
            ))}
          </div>
        </div>

        {pc.mode !== "passthrough" && (
          <div className="set-vert">
            <div className="info">
              <div className="t">{pc.mode === "custom" ? t("替换用系统提示词") : t("追加的系统提示词")}</div>
              <div className="d">{t("留空表示不注入；请求侧指纹脱敏（billing 头与 cc_* 字段剥离）始终生效")}</div>
            </div>
            <textarea
              className="input"
              rows={4}
              placeholder={t("输入统一注入的系统提示词…")}
              value={pc.text ?? ""}
              onChange={(e) => setDraft({ ...draft, prompt: { ...pc, text: e.target.value } })}
            />
          </div>
        )}

        <div className="set-vert" style={{ borderTop: "1px dashed var(--border)", paddingTop: 12 }}>
          <div className="info">
            <div className="t">{t("模型别名映射")}</div>
            <div className="d">
              {t("每行一条")} <code>{t("显示名=上游模型名")}</code>；{t("客户端请求显示名时网关自动改写为上游真实名，/v1/models 也会输出显示名。")}
            </div>
          </div>
          <textarea
            className="input"
            rows={4}
            style={{ fontFamily: "var(--mono)", fontSize: 12.5 }}
            placeholder={"glm-5.2=glm-4.6"}
            value={mapToText(mc.aliases)}
            onChange={(e) => setDraft({ ...draft, models: { ...mc, aliases: textToMap(e.target.value) } })}
          />
        </div>

        <div className="set-vert">
          <div className="info">
            <div className="t">{t("积分倍率")}</div>
            <div className="d">
              {t("每行一条")} <code>{t("模型名=倍率")}</code>；{t("密钥积分配额按 基准 1 分/1k token × 倍率 记账，未配置的模型按 1 计。")}
            </div>
          </div>
          <textarea
            className="input"
            rows={4}
            style={{ fontFamily: "var(--mono)", fontSize: 12.5 }}
            placeholder={"glm-5.2=1.5\nglm-5-air=0.2"}
            value={mapToText(mc.rates)}
            onChange={(e) => setDraft({ ...draft, models: { ...mc, rates: textToNumMap(e.target.value) } })}
          />
        </div>

        <div className="set-vert">
          <div className="info">
            <div className="t">{t("模型分组")}</div>
            <div className="d">{t("每行一条")} <code>{t("模型名=分组名")}</code>；{t("纯展示用途，便于在模型列表中归类。")}</div>
          </div>
          <textarea
            className="input"
            rows={3}
            style={{ fontFamily: "var(--mono)", fontSize: 12.5 }}
            placeholder={"glm-5.2=旗舰"}
            value={mapToText(mc.groups)}
            onChange={(e) => setDraft({ ...draft, models: { ...mc, groups: textToMap(e.target.value) } })}
          />
        </div>
      </div>
    </div>
  );
}

// ---------- 状态镜像 ----------

function RedisSection({ draft, setDraft }: { draft: GatewayConfig; setDraft: (c: GatewayConfig) => void }) {
  const t = useT();
  const rc = draft.redis ?? { enabled: false, url: "", token: "" };
  return (
    <div className="card set-group">
      <div className="card-h">
        <div>
          <h3>{t("状态镜像")}</h3>
          <div className="sub">redis · {t("可选的 Upstash Redis 状态镜像（默认关闭）")}</div>
        </div>
      </div>
      <div className="card-b">
        <SetItem
          title={t("启用镜像")}
          desc={t("把会话粘性绑定与账号池状态快照异步写入 Upstash Redis（REST），便于多实例共享状态；写入失败不影响本地运行")}
        >
          <Switch on={rc.enabled} onChange={() => setDraft({ ...draft, redis: { ...rc, enabled: !rc.enabled } })} />
        </SetItem>
        <SetItem title="REST URL" desc={t("Upstash 控制台提供的 REST endpoint，形如 https://xxx.upstash.io")}>
          <input
            className="mini-input"
            style={{ width: 300 }}
            placeholder="https://xxx.upstash.io"
            value={rc.url ?? ""}
            onChange={(e) => setDraft({ ...draft, redis: { ...rc, url: e.target.value } })}
          />
        </SetItem>
        <SetItem title="REST Token" desc={t("对应的访问令牌（仅存储在本机 config.json）")}>
          <input
            className="mini-input"
            style={{ width: 300 }}
            type="password"
            placeholder="AX..."
            value={rc.token ?? ""}
            onChange={(e) => setDraft({ ...draft, redis: { ...rc, token: e.target.value } })}
          />
        </SetItem>
        <div className="notice">
          <Info size={15} />
          <span>
            {t("镜像为异步 fire-and-forget：本地存储始终是唯一权威数据源，Redis 只作只读副本用于观测或多进程共享。")}
          </span>
        </div>
      </div>
    </div>
  );
}

// ---------- 增强功能（防休眠 + 客户端注入） ----------

function EnhanceSection() {
  const t = useT();
  const [awake, setAwake] = useState<AwakeStatus | null>(null);
  const [injectSt, setInjectSt] = useState<InjectStatus | null>(null);
  const [injectCfg, setInjectCfg] = useState<InjectConfig | null>(null);
  const [accts, setAccts] = useState<AccountBackup[]>([]);
  const [busy, setBusy] = useState<string | null>(null);

  const [injectErr, setInjectErr] = useState<string | null>(null);
  const load = useCallback(async () => {
    const [aw, st, cfg, list] = await Promise.all([
      powerApi.status(),
      injectApi.status().catch((e) => {
        setInjectErr(errText(e));
        return null;
      }),
      injectApi.getConfig().catch(() => null),
      injectApi.accounts().catch(() => []),
    ]);
    if (st) setInjectErr(null);
    setAwake(aw);
    setInjectSt(st);
    setInjectCfg(cfg);
    setAccts(list);
  }, []);

  useEffect(() => {
    void load().catch((e) => toast.error(t("读取增强功能状态失败"), errText(e)));
  }, [load, t]);

  // 后端注入事件：启动/停止/断开即时同步状态，错误醒目弹出（面板动作失败在这里可见）
  useEffect(
    () =>
      onEvent(EVENT.injectStatus, (data) => {
        const d = (data ?? {}) as { kind?: string; message?: string };
        if (d.kind === "error" && d.message) {
          toast.error(t("注入面板操作失败"), d.message);
        } else if (d.kind === "disconnected") {
          toast.warn(t("注入连接已断开"), t("客户端页面已刷新或关闭，需要时请重新启动注入"));
        }
        void load().catch(() => undefined);
      }),
    [load, t],
  );

  const toggleAwake = async () => {
    setBusy("awake");
    try {
      await powerApi.setKeepAwake(!(awake?.active ?? false));
      await load();
      toast.success(awake?.active ? t("防休眠已关闭") : t("防休眠已开启"));
    } catch (e) {
      toast.error(t("设置失败"), errText(e));
    } finally {
      setBusy(null);
    }
  };

  const toggleInject = async () => {
    setBusy("inject");
    try {
      if (injectSt?.running) {
        await injectApi.stop();
        toast.success(t("注入已停止"));
      } else {
        await injectApi.start();
        toast.success(t("注入已启动"), t("官方客户端窗口右下角会出现 🧩 面板按钮"));
      }
      await load();
    } catch (e) {
      toast.error(t("注入操作失败"), errText(e));
    } finally {
      setBusy(null);
    }
  };

  const patchInject = async (patch: Parameters<typeof injectApi.updateConfig>[0]) => {
    setBusy("cfg");
    try {
      await injectApi.updateConfig(patch);
      await load();
    } catch (e) {
      toast.error(t("保存失败"), errText(e));
    } finally {
      setBusy(null);
    }
  };

  const doBackup = async () => {
    setBusy("backup");
    try {
      const b = await injectApi.backup("");
      toast.success(t("已备份当前账号"), `${b.name}（${b.id}）`);
      await load();
    } catch (e) {
      toast.error(t("备份失败"), errText(e));
    } finally {
      setBusy(null);
    }
  };

  const doSwitch = async (a: AccountBackup) => {
    const ok = await confirmDialog({
      title: t("切换到「{name}」", { name: a.name }),
      desc: t("将用该备份覆盖官方客户端当前的登录态并刷新页面。若当前账号尚未备份，其登录态会丢失。"),
      danger: true,
      confirmText: t("切换"),
    });
    if (!ok) return;
    setBusy("switch-" + a.id);
    try {
      await injectApi.switchAccount(a.id);
      toast.success(t("已切换"), t("客户端页面已刷新；注入连接已断开，可重新启动"));
      await load();
    } catch (e) {
      toast.error(t("切换失败"), errText(e));
    } finally {
      setBusy(null);
    }
  };

  const doDelete = async (a: AccountBackup) => {
    const ok = await confirmDialog({
      title: t("删除备份「{name}」", { name: a.name }),
      desc: t("该备份文件将被删除，不可恢复。"),
      danger: true,
      confirmText: t("删除"),
    });
    if (!ok) return;
    setBusy("del-" + a.id);
    try {
      await injectApi.deleteBackup(a.id);
      await load();
    } catch (e) {
      toast.error(t("删除失败"), errText(e));
    } finally {
      setBusy(null);
    }
  };

  return (
    <>
      <div className="card set-group">
        <div className="card-h">
          <div>
            <h3>{t("防休眠")}</h3>
            <div className="sub">{t("自动化任务运行期间阻止系统休眠 · caffeinate / SetThreadExecutionState")}</div>
          </div>
        </div>
        <div className="card-b">
          <SetItem
            title={t("保持系统唤醒")}
            desc={
              awake?.active
                ? t("已启用（{note}）：系统不会进入休眠", { note: awake.note ?? "" })
                : t("开启后系统不会自动休眠；关闭后立即恢复系统默认休眠策略")
            }
          >
            <button className="btn btn-soft sm" disabled={busy === "awake"} onClick={() => void toggleAwake()}>
              {busy === "awake" ? <Loader2 size={13} className="spin" /> : awake?.active ? t("停用") : t("启用")}
            </button>
          </SetItem>
          <div className="notice">
            <Info size={15} />
            <span>{t("选择会持久化：下次启动应用时自动恢复上次的防休眠状态。也可在系统托盘菜单快速切换。")}</span>
          </div>
        </div>
      </div>

      <div className="card set-group">
        <div className="card-h">
          <div>
            <h3>{t("客户端注入（实验性）")}</h3>
            <div className="sub">{t("通过本机回环 CDP 向官方客户端注入增强面板：免打扰 / 账号备份切换")}</div>
          </div>
          <span className={`badge ${injectSt?.running ? "b-green" : "b-gray"}`}>
            <span className="d" />
            {injectSt?.running ? t("已注入") : t("未注入")}
          </span>
        </div>
        <div className="card-b">
          {injectErr && (
            <div className="notice warn">
              <AlertTriangle size={15} />
              <span>{t("注入状态读取失败：{err}（下方操作可能同样不可用）", { err: injectErr })}</span>
            </div>
          )}
          <SetItem
            title={t("注入面板")}
            desc={
              injectSt?.running
                ? t("已附着：{target} · 端口 {port} · 自 {since}", { target: injectSt.target ?? "", port: injectSt.port ?? 0, since: injectSt.since ?? "" })
                : t("启动后会在官方客户端右下角显示 🧩 面板（客户端需可探测或以调试模式重启）")
            }
          >
            <button className="btn btn-soft sm" disabled={busy === "inject"} onClick={() => void toggleInject()}>
              {busy === "inject" ? <Loader2 size={13} className="spin" /> : injectSt?.running ? t("停止") : t("启动")}
            </button>
          </SetItem>

          <SetItem title={t("客户端路径")} desc={t("留空自动探测 /Applications 下 WorkBuddy*.app；企业定制版可手动指定")}>
            <input
              className="mini-input"
              style={{ width: 300 }}
              placeholder={t("自动探测")}
              value={injectCfg?.client_path ?? ""}
              onChange={(e) => setInjectCfg((c) => (c ? { ...c, client_path: e.target.value } : c))}
              onBlur={(e) => void patchInject({ clientPath: e.target.value })}
            />
          </SetItem>
          <SetItem title={t("调试端口")} desc={t("CDP 调试端口，默认 9223（客户端需以该端口重启进入调试模式）")}>
            <input
              className="mini-input"
              style={{ width: 110 }}
              type="number"
              value={injectCfg?.port ?? 9223}
              onChange={(e) => setInjectCfg((c) => (c ? { ...c, port: Number(e.target.value) || 0 } : c))}
              onBlur={(e) => void patchInject({ port: Number(e.target.value) || 9223 })}
            />
          </SetItem>
          <SetItem title={t("权限弹窗免打扰")} desc={t("自动点击弹窗中的确认类按钮（允许 / 确定 / 同意…）")}>
            <Switch
              on={injectCfg?.dnd_auto_confirm ?? false}
              onChange={() => void patchInject({ dnd: !(injectCfg?.dnd_auto_confirm ?? false) })}
            />
          </SetItem>

          <div className="set-vert" style={{ borderTop: "1px dashed var(--border)", paddingTop: 12 }}>
            <div className="info">
              <div className="t">{t("账号备份 / 切换")}</div>
              <div className="d">
                {t("备份官方客户端当前登录态（localStorage 快照）；切换时恢复所选备份并刷新页面。")}
                {t("需先「启动注入」。也可直接在客户端内面板操作。")}
              </div>
            </div>
            <div className="flex" style={{ justifyContent: "flex-end" }}>
              <button className="btn btn-soft sm" disabled={busy === "backup" || !injectSt?.running} onClick={() => void doBackup()}>
                {busy === "backup" ? <Loader2 size={13} className="spin" /> : null} {t("备份当前登录态")}
              </button>
            </div>
            {accts.length === 0 ? (
              <div className="hint">{t("暂无备份")}</div>
            ) : (
              accts.map((a) => (
                <div className="flex" key={a.id} style={{ justifyContent: "space-between", padding: "6px 10px", background: "var(--surface-2)", borderRadius: 8 }}>
                  <div>
                    <div style={{ fontSize: 12.5 }}>{a.name}</div>
                    <div className="hint" style={{ margin: 0 }}>
                      {a.uid ? `${a.uid} · ` : ""}{a.time}
                    </div>
                  </div>
                  <div className="flex" style={{ gap: 6 }}>
                    <button
                      className="btn btn-ghost sm"
                      disabled={busy === "switch-" + a.id || !injectSt?.running}
                      onClick={() => void doSwitch(a)}
                    >
                      {busy === "switch-" + a.id ? <Loader2 size={12} className="spin" /> : null} {t("切换")}
                    </button>
                    <button className="btn btn-danger-soft sm" disabled={busy === "del-" + a.id} onClick={() => void doDelete(a)}>
                      {t("删除")}
                    </button>
                  </div>
                </div>
              ))
            )}
          </div>

          <div className="notice">
            <Info size={15} />
            <span>
              {t("注入通过官方客户端的调试协议（本机回环）实现，不修改安装包；确认按钮识别依赖界面文案启发式，")}
              {t("客户端升级后免打扰可能失效，其余功能不受影响。")}
            </span>
          </div>
        </div>
      </div>
    </>
  );
}

// ---------- 账号数据迁移（Session / 记忆 / Connectors，对齐 account-migrate 能力） ----------

function DataMigrationCard() {
  const t = useT();
  const [diag, setDiag] = useState<DataMigrationDiag | null>(null);
  const [diagErr, setDiagErr] = useState("");
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<string>("");

  const load = useCallback(() => {
    return dataMigrateApi
      .diagnose()
      .then((d) => {
        setDiag(d);
        setDiagErr("");
      })
      .catch((e) => setDiagErr(errText(e)));
  }, []);

  useEffect(() => {
    void load().catch(() => {});
  }, [load]);

  const currentUid = diag?.currentUid ?? "";

  const doMigrate = async (source: string) => {
    const ok = await confirmDialog({
      title: t("迁移 {a} 的数据", { a: source }),
      desc: t("将把该账号的 Session 历史 / 记忆 / Connectors 合并进当前登录账号 {a}（合并而非覆盖，迁移前自动备份）。", { a: currentUid }),
      confirmText: t("开始迁移"),
    });
    if (!ok) return;
    setBusy(true);
    setResult("");
    try {
      const r = await dataMigrateApi.migrate(source, currentUid);
      setResult(t("Session {a} 条 · 记忆 {b} 行 · Connectors {c} 个", { a: r.sessionsMoved, b: r.memoryAppended, c: r.connectorCopied }));
      toast.success(t("迁移完成"), t("备份标签：{a}", { a: r.backupTag }));
      await load();
    } catch (e) {
      toast.error(t("迁移失败"), errText(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="card set-group">
      <div className="card-h">
        <div>
          <h3>{t("账号数据迁移")}</h3>
          <div className="sub">
            {t("切换登录账号后，把旧账号的 Session / 记忆 / Connectors 合并进当前账号")}
            {currentUid && ` · ${t("当前登录：{a}", { a: currentUid })}`}
          </div>
        </div>
        <button className="btn btn-ghost sm" onClick={() => void load()}>
          <RefreshCw size={12} strokeWidth={2} /> {t("重新诊断")}
        </button>
      </div>
      <div className="card-b" style={{ paddingTop: 6 }}>
        {diagErr && <ErrorBlock message={diagErr} onRetry={() => void load()} />}
        {!diagErr && !diag && <LoadingBlock label={t("诊断中")} />}
        {diag && (
          <table className="tbl">
            <thead>
              <tr>
                <th>{t("账号")}</th><th>{t("Session")}</th><th>{t("记忆")}</th><th>{t("Connectors")}</th><th>{t("操作")}</th>
              </tr>
            </thead>
            <tbody>
              {(diag.users ?? []).map((u) => (
                <tr key={u.uid} className={u.uid === currentUid ? "row-expiring" : ""}>
                  <td className="em mono">{u.uid}{u.uid === currentUid && <span className="chip" style={{ marginLeft: 6 }}>{t("当前")}</span>}</td>
                  <td className="num">{u.sessions}</td>
                  <td className="num">{u.memory ? "✓" : "—"}</td>
                  <td className="num">{u.connectors ? "✓" : "—"}</td>
                  <td>
                    {u.uid !== currentUid && (
                      <button className="btn btn-ghost sm" disabled={busy} onClick={() => void doMigrate(u.uid)}>
                        {busy ? <Loader2 size={13} className="spin" /> : <Archive size={13} strokeWidth={2} />} {t("迁移到此账号")}
                      </button>
                    )}
                  </td>
                </tr>
              ))}
              {(diag.users ?? []).length === 0 && (
                <tr><td colSpan={5}><EmptyBlock title={t("未发现任何账号数据")} /></td></tr>
              )}
            </tbody>
          </table>
        )}
        {result && <div className="muted" style={{ marginTop: 8, fontSize: 12 }}>{t("上次迁移：")}{result}</div>}
      </div>
    </div>
  );
}

// ---------- 数据与备份 ----------

function DataSection({
  meta, backups, busy, setBusy, reload, loadDraft,
}: {
  meta: Bundle["meta"];
  backups: BackupItem[];
  busy: string | null;
  setBusy: (v: string | null) => void;
  reload: () => Promise<void>;
  loadDraft: () => Promise<void>;
}) {
  const t = useT();
  const doBackup = async () => {
    setBusy("backup");
    try {
      const path = await configApi.backup();
      toast.success(t("备份已创建"), path);
      await reload();
    } catch (e) {
      toast.error(t("备份失败"), errText(e));
    } finally {
      setBusy(null);
    }
  };

  const doExport = async () => {
    setBusy("export");
    try {
      const json = await configApi.export();
      const blob = new Blob([json], { type: "application/json" });
      const url = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = "workbuddy-config.json";
      a.click();
      URL.revokeObjectURL(url);
      toast.success(t("配置已导出"), "workbuddy-config.json");
    } catch (e) {
      toast.error(t("导出失败"), errText(e));
    } finally {
      setBusy(null);
    }
  };

  const doImport = () => {
    const input = document.createElement("input");
    input.type = "file";
    input.accept = "application/json";
    input.onchange = async () => {
      const file = input.files?.[0];
      if (!file) return;
      const ok = await confirmDialog({
        title: t("导入配置"),
        desc: t("将用「{name}」覆盖当前配置（网关运行中将自动重启）。", { name: file.name }),
        confirmText: t("导入并生效"),
      });
      if (!ok) return;
      setBusy("import");
      try {
        await configApi.import(await file.text());
        await reload();
        await loadDraft();
        toast.success(t("配置已导入"), file.name);
        broadcastRefresh();
      } catch (e) {
        toast.error(t("导入失败"), errText(e));
      } finally {
        setBusy(null);
      }
    };
    input.click();
  };

  const doRestore = async (item: BackupItem) => {
    const ok = await confirmDialog({
      title: t("恢复该备份"),
      desc: t("将用「{name}」覆盖当前配置（网关运行中将自动重启）。", { name: item.name }),
      danger: true,
      confirmText: t("恢复"),
    });
    if (!ok) return;
    setBusy(item.path);
    try {
      await configApi.restore(item.path);
      await reload();
      await loadDraft();
      toast.success(t("已从备份恢复"), item.name);
      broadcastRefresh();
    } catch (e) {
      toast.error(t("恢复失败"), errText(e));
    } finally {
      setBusy(null);
    }
  };

  return (
    <>
      <div className="card set-group">
        <div className="card-h">
          <div>
            <h3>{t("数据与备份")}</h3>
            <div className="sub">{t("配置导入导出 / 备份与恢复")}</div>
          </div>
        </div>
        <div className="card-b">
          <SetItem title={t("导出配置")} desc={t("下载当前 config.json（不含登录凭证）")}>
            <button className="btn btn-ghost sm" disabled={busy === "export"} onClick={doExport}>
              {busy === "export" ? <Loader2 size={13} className="spin" /> : <Download size={13} strokeWidth={2} />} {t("导出")}
            </button>
          </SetItem>
          <SetItem title={t("导入配置")} desc={t("从 JSON 文件覆盖当前配置")}>
            <button className="btn btn-ghost sm" disabled={busy === "import"} onClick={doImport}>
              {busy === "import" ? <Loader2 size={13} className="spin" /> : <Upload size={13} strokeWidth={2} />} {t("导入")}
            </button>
          </SetItem>
          <SetItem title={t("立即备份")} desc={t("在数据目录的 backups/ 下生成带时间戳的配置备份")}>
            <button className="btn btn-soft sm" disabled={busy === "backup"} onClick={doBackup}>
              {busy === "backup" ? <Loader2 size={13} className="spin" /> : <Archive size={13} strokeWidth={2} />} {t("创建备份")}
            </button>
          </SetItem>
          <SetItem title={t("数据文件")} desc={t("请求日志 / 任务日志 / 密钥 / 账号运行态均存于此文件")}>
            <PathChip path={meta.storePath} />
          </SetItem>
        </div>
      </div>

      <div className="card set-group">
        <div className="card-h">
          <div>
            <h3>{t("备份记录")}</h3>
            <div className="sub">{t("扫描 {dir}/backups 得到的真实文件列表", { dir: meta.dataDir })}</div>
          </div>
          <button className="btn btn-ghost sm" onClick={() => void reload()}>
            {t("刷新")}
          </button>
        </div>
        <div className="card-b" style={{ paddingTop: 6 }}>
          {backups.length === 0 ? (
            <EmptyBlock title={t("暂无备份")} desc={t("点击上方「创建备份」生成第一个备份文件。")} />
          ) : (
            backups.map((b) => (
              <div className="bak-row" key={b.path}>
                <div className="bi">
                  <div className="n mono">{b.name}</div>
                  <div className="m">
                    {formatSize(b.size)} · {formatTime(b.time)}
                  </div>
                </div>
                <button className="btn btn-ghost sm" disabled={busy === b.path} onClick={() => doRestore(b)}>
                  {busy === b.path ? <Loader2 size={13} className="spin" /> : <RotateCw size={13} strokeWidth={2} />} {t("恢复")}
                </button>
              </div>
            ))
          )}
        </div>
      </div>

      <DataMigrationCard />

      <div className="card set-group danger-zone">
        <div className="card-h">
          <div>
            <h3>{t("凭证目录")}</h3>
            <div className="sub">{t("账号凭证由该目录下的文件提供，删除文件等同于移除账号")}</div>
          </div>
        </div>
        <div className="card-b">
          <SetItem title={t("凭证目录路径")} desc={t("扫描结果即「账号管理」列表中的账号")}>
            <PathChip path={meta.authDir} />
          </SetItem>
          <SetItem title={t("打开目录")} desc={t("在系统文件管理器中打开，手动增删凭证文件")}>
            <button
              className="btn btn-ghost sm"
              onClick={() =>
                void accountsApi
                  .openAuthDir()
                  .then(() => toast.success(t("已打开凭证目录")))
                  .catch((e) => toast.error(t("打开失败"), errText(e)))
              }
            >
              <FolderOpen size={13} strokeWidth={2} /> {t("打开")}
            </button>
          </SetItem>
        </div>
      </div>
    </>
  );
}

// ---------- 关于 ----------

function AboutSection({ sys, meta }: { sys: SystemInfo; meta: Bundle["meta"] }) {
  const t = useT();
  const [update, setUpdate] = useState<UpdateInfo | null>(null);
  const [checking, setChecking] = useState(false);
  const [downloading, setDownloading] = useState(false);
  const [progress, setProgress] = useState<{ stage: string; percent: number } | null>(null);

  // 下载/校验进度：订阅后端 update:progress 事件（installing 阶段后应用会退出替换）
  useEffect(() => {
    if (!downloading) return;
    return onEvent(EVENT.updateProgress, (data) => {
      const p = data as { stage?: string; percent?: number };
      if (typeof p?.stage !== "string") return;
      setProgress({ stage: p.stage, percent: Math.min(100, Math.max(0, Math.round(p.percent ?? 0))) });
    });
  }, [downloading]);

  // 后台定期检查命中新版本：直接刷新更新信息（按钮会变为「立即更新」）
  useEffect(() => {
    return onEvent(EVENT.updateAvailable, (data) => {
      const info = data as UpdateInfo | null;
      if (info?.hasUpdate) setUpdate(info);
    });
  }, []);

  const check = async () => {
    setChecking(true);
    try {
      const res = await systemApi.checkUpdate();
      setUpdate(res);
      if (res.hasUpdate) toast.info(t("发现新版本 {v}", { v: res.latest ?? "" }), res.note);
      else toast.success(res.note || t("已是最新版本"));
    } catch (e) {
      toast.error(t("检查更新失败"), errText(e));
    } finally {
      setChecking(false);
    }
  };

  const download = async () => {
    if (!update?.downloadUrl) return;
    setDownloading(true);
    setProgress({ stage: "downloading", percent: 0 });
    try {
      const path = await systemApi.downloadUpdate(update.downloadUrl);
      if (update.autoInstall) {
        toast.info(t("即将退出并安装更新"), t("安装完成后应用会自动重启"));
        await systemApi.installUpdate(path);
        return; // 走到这里说明应用未退出（替换被拒绝），提示用户
      }
      toast.success(t("安装包已下载"), path);
    } catch (e) {
      toast.error(update?.autoInstall ? t("自动更新失败") : t("下载失败"), errText(e));
    } finally {
      setDownloading(false);
      setProgress(null);
    }
  };

  const stageText: Record<string, string> = {
    downloading: t("下载中"),
    verifying: t("校验中"),
    installing: t("安装中"),
    done: t("完成"),
  };

  return (
    <div className="card set-group">
      <div className="card-h">
        <div>
          <h3>{t("关于")}</h3>
          <div className="sub">{t("版本与运行时（全部为真实采样值）")}</div>
        </div>
      </div>
      <div className="card-b">
        <div className="kv-grid">
          <KV k={t("应用版本")} v={sys.version || "—"} />
          <KV k={t("Go 版本")} v={sys.goVersion || "—"} />
          <KV k={t("平台 / 架构")} v={`${sys.platform} / ${sys.arch}`} />
          <KV k={t("CPU 核心数")} v={String(sys.numCpu)} />
          <KV k={t("进程运行时长")} v={sys.processUptime || "—"} />
          <KV k={t("网关运行时长")} v={sys.gatewayUptime || t("未运行")} />
          <KV k={t("堆内存占用")} v={`${sys.memoryAlloc.toFixed(1)} MB`} />
          <KV k={t("向 OS 申请内存")} v={`${sys.memorySys.toFixed(1)} MB`} />
          <KV k={t("堆对象数")} v={sys.heapObjects.toLocaleString()} />
          <KV k={t("GC 次数")} v={String(sys.gcCount)} />
          <KV k={t("协程数")} v={String(sys.goroutines)} />
          <KV k={t("数据目录")} v={meta.dataDir} />
        </div>
        <div className="set-item" style={{ marginTop: 6 }}>
          <div className="info">
            <div className="t">
              {update?.hasUpdate
                ? t("发现新版本 {v}，立即升级", { v: update.latest ?? "" })
                : t("检查更新")}
            </div>
            <div className="d">
              {update
                ? update.note
                : t("查询 GitHub Releases 最新发布，按当前平台匹配安装包")}
              {update?.releaseNotes && (
                <span style={{ display: "block", marginTop: 4, whiteSpace: "pre-wrap" }}>
                  {update.releaseNotes}
                </span>
              )}
            </div>
          </div>
          <div className="flex" style={{ gap: 8 }}>
            {update?.hasUpdate && update.releasePage && (
              <button className="btn btn-ghost sm" onClick={() => void accountsApi.openURL(update.releasePage!)}>
                <ExternalLink size={12} strokeWidth={2} /> {t("发布页")}
              </button>
            )}
            {update?.hasUpdate && update.downloadUrl ? (
              // 有新版：高亮主按钮「立即更新」（颜色鲜明 + 呼吸动效，引导一键升级）
              <button
                className="btn btn-primary btn-update sm"
                disabled={downloading}
                onClick={() => void download()}
                data-tip={update.autoInstall ? t("下载、校验后自动替换并重启") : undefined}
              >
                {downloading ? <Loader2 size={12} className="spin" /> : <Download size={12} strokeWidth={2} />}
                {update.autoInstall ? t("立即更新") : t("去下载新版本")}
              </button>
            ) : (
              <button className="btn btn-soft sm" disabled={checking} onClick={() => void check()}>
                {checking ? <Loader2 size={12} className="spin" /> : <RefreshCw size={12} strokeWidth={2} />} {t("检查更新")}
              </button>
            )}
          </div>
        </div>
        {downloading && progress && (
          <div style={{ marginTop: 10 }}>
            <div className="flex" style={{ justifyContent: "space-between", marginBottom: 4 }}>
              <span className="sub" style={{ fontSize: 12 }}>
                {stageText[progress.stage] ?? progress.stage}
                {progress.stage === "downloading" ? ` ${progress.percent}%` : ""}
              </span>
              <span className="sub mono" style={{ fontSize: 12 }}>
                {progress.percent}%
              </span>
            </div>
            <div
              style={{
                height: 6,
                borderRadius: 999,
                background: "var(--color-background-tertiary, rgba(127,127,127,.18))",
                overflow: "hidden",
              }}
            >
              <div
                style={{
                  width: `${progress.percent}%`,
                  height: "100%",
                  borderRadius: 999,
                  background: "var(--accent, #4f7cff)",
                  transition: "width .3s ease",
                }}
              />
            </div>
          </div>
        )}
        <div className="notice" style={{ marginTop: 14 }}>
          <Info size={15} />
          <span>BuddyBot · {t("WorkBuddy 账号池与网关控制台")} · Wails v3 + React 18</span>
        </div>
      </div>
    </div>
  );
}

// ---------- 通用小组件 ----------

function SetItem({ title, desc, children }: { title: string; desc: string; children: React.ReactNode }) {
  return (
    <div className="set-item">
      <div className="info">
        <div className="t">{title}</div>
        <div className="d">{desc}</div>
      </div>
      {children}
    </div>
  );
}

function KV({ k, v }: { k: string; v: string }) {
  return (
    <div className="kv">
      <span>{k}</span>
      <b className="mono" data-tip={v}>
        {v}
      </b>
    </div>
  );
}

function PathChip({ path }: { path: string }) {
  const t = useT();
  return (
    <div className="flex" style={{ gap: 6, maxWidth: 380 }}>
      <span className="mono path-chip" data-tip={path}>
        {path || "—"}
      </span>
      <button
        className="icon-btn"
        data-tip={t("复制路径")}
        onClick={() =>
          navigator.clipboard.writeText(path).then(
            () => toast.success(t("路径已复制")),
            () => toast.error(t("复制失败"), t("当前环境不允许访问剪贴板")),
          )
        }
      >
        <Copy size={13} strokeWidth={2} />
      </button>
    </div>
  );
}

// ---------- 工具函数 ----------

/** 白名单文本 → 条目数组（按行/逗号切分，去空去重） */
function parseList(text: string): string[] {
  const out: string[] = [];
  for (const raw of text.split(/[\n,]/)) {
    const item = raw.trim();
    if (item && !out.includes(item)) out.push(item);
  }
  return out;
}

/** RFC3339 → 本地可读时间 + 剩余时长 */
function nextDisplay(iso?: string): string {
  if (!iso) return t("未排程（请检查时点配置）");
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  const diffMs = d.getTime() - Date.now();
  const mins = Math.round(diffMs / 60000);
  let rel: string;
  if (diffMs <= 0) rel = t("即将执行");
  else if (mins < 60) rel = t("{n} 分钟后", { n: mins });
  else if (mins < 60 * 24) rel = t("{n} 小时后", { n: Math.floor(mins / 60) });
  else rel = t("{n} 天后", { n: Math.floor(mins / 1440) });
  return `${formatTime(iso)}（${rel}）`;
}

function formatTime(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  const p = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`;
}

function formatSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / 1024 / 1024).toFixed(1)} MB`;
}

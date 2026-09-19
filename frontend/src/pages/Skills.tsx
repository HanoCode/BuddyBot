import { useCallback, useEffect, useRef, useState } from "react";
import { useSearchParams } from "react-router-dom";
import {
  ArrowRight, Check, CheckCircle2, Download, ExternalLink, KeyRound, Loader2, PackageOpen,
  Puzzle, RefreshCw, Search, Star, Trash2, X, XCircle,
} from "lucide-react";
import { skillsApi } from "../services/api";
import { errText, useAsync } from "../hooks/useAsync";
import { confirmDialog, toast } from "../components/common/Feedback";
import { EmptyBlock, ErrorBlock } from "../components/common/StateBlock";
import { createPortal } from "react-dom";
import { useT, t } from "../i18n";
import type { InstalledSkill, SkillHubBrowseResult, SkillHubSkill, SkillTargetInfo, SkillUpdateItem, SkillUpdatesResult } from "../types";

/** 排序 Tab（tab 值与后端 BrowseSkillHub / 榜单 section 对应；installed/updates 为本地管理页） */
const TABS: { value: string; label: string }[] = [
  { value: "score", label: "推荐" },
  { value: "hot", label: "下载量" },
  { value: "trending", label: "近期飙升" },
  { value: "newest", label: "最近上新" },
  { value: "installed", label: "已安装" },
  { value: "updates", label: "可更新" },
];

const LOCAL_TABS = new Set(["installed", "updates"]);

/** 常见场景分类的中文标签（未收录的显示原始 key） */
const CATEGORY_ZH: Record<string, string> = {
  "dev-programming": "开发编程",
  "office-efficiency": "办公效率",
  "life-services": "生活服务",
  "ai-agent": "AI 智能体",
  "content-creation": "内容创作",
  "data-analysis": "数据分析",
  "education-learning": "教育学习",
  "finance-business": "金融商业",
};

/** 安装目标目录口径备注（claude-code/codex/workbuddy 来自 skillhub 官方表，其余为推断） */
const TARGET_CONFIRMED: Record<string, boolean> = {
  "claude-code": true,
  codex: true,
  workbuddy: true,
};

function fmtCount(n: number): string {
  if (n >= 10000) return `${(n / 10000).toFixed(1)}万`;
  return String(n);
}

function fmtTime(ts: number): string {
  if (!ts) return "—";
  const days = Math.floor((Date.now() - ts) / 86400000);
  if (days <= 0) return t("今天");
  if (days < 30) return t("{n} 天前", { n: days });
  return new Date(ts).toLocaleDateString("zh-CN");
}

/** 缓存年龄：毫秒时间戳 → "刚刚 / X 分钟前 / X 小时前 / X 天前" */
function fmtAge(ts: number): string {
  if (!ts) return "—";
  const mins = Math.floor((Date.now() - ts) / 60000);
  if (mins < 1) return t("刚刚");
  if (mins < 60) return t("{n} 分钟前", { n: mins });
  const hours = Math.floor(mins / 60);
  if (hours < 24) return t("{n} 小时前", { n: hours });
  return t("{n} 天前", { n: Math.floor(hours / 24) });
}

/** 技能图标：加载失败回退到首字母色块 */
function SkillIcon({ skill, size = 36 }: { skill: SkillHubSkill; size?: number }) {
  const [broken, setBroken] = useState(false);
  useEffect(() => setBroken(false), [skill.iconUrl]);
  if (!skill.iconUrl || broken) {
    return (
      <span
        className="avatar"
        style={{ width: size, height: size, background: "linear-gradient(135deg,#F0997B,#D85A30)", fontSize: size * 0.42, fontWeight: 700, color: "#fff", display: "inline-flex", alignItems: "center", justifyContent: "center" }}
      >
        {(skill.name || "?").slice(0, 1).toUpperCase()}
      </span>
    );
  }
  return (
    <img
      src={skill.iconUrl}
      alt=""
      width={size}
      height={size}
      onError={() => setBroken(true)}
      style={{ width: size, height: size, borderRadius: 9, objectFit: "cover", flex: "none", background: "var(--surface-3)" }}
    />
  );
}

/** 安装弹窗：选择目标客户端，串行安装并逐个展示结果 */
function InstallDialog({
  skill, targets, onClose, onInstalled,
}: {
  skill: SkillHubSkill;
  targets: SkillTargetInfo[];
  onClose: () => void;
  onInstalled: () => void;
}) {
  const installable = targets.filter((t) => t.installed);
  const [picked, setPicked] = useState<Set<string>>(new Set());
  const [running, setRunning] = useState(false);
  const [results, setResults] = useState<Record<string, "ok" | "fail">>({});

  const t = useT();

  const toggle = (id: string) =>
    setPicked((prev) => {
      const next = new Set(prev);
      next.has(id) ? next.delete(id) : next.add(id);
      return next;
    });

  const run = async () => {
    setRunning(true);
    const done: Record<string, "ok" | "fail"> = {};
    for (const id of picked) {
      try {
        await skillsApi.install({ slug: skill.slug, namespace: skill.namespace, target: id });
        done[id] = "ok";
      } catch (e) {
        done[id] = "fail";
        toast.error(t("安装到 {id} 失败", { id }), errText(e));
      }
      setResults({ ...done });
    }
    setRunning(false);
    const okCount = Object.values(done).filter((v) => v === "ok").length;
    if (okCount > 0) {
      toast.success(t("「{name}」安装完成", { name: skill.name }), t("{n} 个客户端已写入，重启对应客户端后生效", { n: okCount }));
      onInstalled();
    }
  };

  return createPortal(
    <div className="overlay" onMouseDown={(e) => e.target === e.currentTarget && !running && onClose()}>
      <div className="dialog">
        <div className="dlg-head">
          <div>
            <h3>{t("安装「{name}」", { name: skill.name })}</h3>
            <p>{t("选择要写入的客户端 skills 目录")}</p>
          </div>
          <button className="icon-btn" disabled={running} onClick={onClose}><X size={14} strokeWidth={2.2} /></button>
        </div>
        <div className="dlg-body">
          {installable.length === 0 && (
            <div className="muted" style={{ fontSize: 12.5 }}>{t("未检测到任何已安装的客户端（配置目录不存在）")}</div>
          )}
          <div className="flex" style={{ gap: 8, flexWrap: "wrap" }}>
            {installable.map((tg) => {
              const on = picked.has(tg.id);
              return (
                <button
                  key={tg.id}
                  className={`chip-btn${on ? " on" : ""}`}
                  disabled={running}
                  title={on ? t("点击取消") : t("安装到 {dir}", { dir: tg.dir })}
                  onClick={() => toggle(tg.id)}
                >
                  {on ? <Check size={12} strokeWidth={2.6} /> : null}
                  {tg.name}
                  <span className="muted" style={{ fontSize: 11 }}>({tg.count})</span>
                </button>
              );
            })}
          </div>
          <div className="muted" style={{ fontSize: 11.5, marginTop: 12, lineHeight: 1.6 }}>
            {installable.map((tg) => (
              <div key={tg.id} className="mono">
                {tg.name} → {tg.dir}
                {!TARGET_CONFIRMED[tg.id] && <span style={{ color: "var(--warn, #BA7517)" }}>{t("（目录待验证）")}</span>}
                {results[tg.id] === "ok" && <CheckCircle2 size={12} strokeWidth={2.4} style={{ verticalAlign: -2, marginLeft: 6, color: "var(--ok, #3B6D11)" }} />}
                {results[tg.id] === "fail" && <XCircle size={12} strokeWidth={2.4} style={{ verticalAlign: -2, marginLeft: 6, color: "var(--danger, #A32D2D)" }} />}
              </div>
            ))}
          </div>
          <div className="muted" style={{ fontSize: 11.5, marginTop: 8 }}>
            {t("安装后需重启对应客户端才会加载新技能；技能内含提示词与脚本，请确认来源可信。")}
          </div>
        </div>
        <div className="dlg-foot">
          <button className="btn btn-ghost" disabled={running} onClick={onClose}>{t("取消")}</button>
          <button
            className="btn btn-primary"
            disabled={running || picked.size === 0}
            onClick={() => void run()}
          >
            {running ? <Loader2 size={13} className="spin" /> : <Download size={13} strokeWidth={2} />}
            {running ? t("安装中…") : t("安装到 {n} 个客户端", { n: picked.size })}
          </button>
        </div>
      </div>
    </div>,
    document.body,
  );
}

export default function Skills() {
  const cli = useAsync(() => skillsApi.status(), []);
  const targets = useAsync(() => skillsApi.targets(), []);

  const t = useT();
  const [tab, setTab] = useState("score");
  const [input, setInput] = useState("");
  const [query, setQuery] = useState("");
  // 市场数据走后端缓存（TTL 在设置里配置，默认 3 天）；刷新按钮置位 force 绕过缓存
  const forceBrowse = useRef(false);
  const forceUpdates = useRef(false);
  // 本地管理 tab 不发浏览请求，返回空结果占位
  const list = useAsync<SkillHubBrowseResult>(() => {
    const force = forceBrowse.current;
    forceBrowse.current = false;
    return LOCAL_TABS.has(tab)
      ? Promise.resolve({ tab, query, skills: [], cliMissing: false, fetchedAt: 0, fromCache: false })
      : skillsApi.browse({ tab, query, force });
  }, [tab, query]);

  // ⌘K 搜索词转跳：/skills?q=<词> 预填并直接执行搜索（仅挂载时消费一次）
  const [searchParams] = useSearchParams();
  useEffect(() => {
    const q = searchParams.get("q");
    if (q) {
      setInput(q);
      setQuery(q);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const [installingCLI, setInstallingCLI] = useState(false);
  const [installSkill, setInstallSkill] = useState<SkillHubSkill | null>(null);

  // 已安装管理：当前查看的客户端 + 技能列表（切到该 tab 时自动刷新）
  const [managedTarget, setManagedTarget] = useState<string>("");
  const managed = useAsync(
    () => (tab === "installed" && managedTarget ? skillsApi.installed(managedTarget) : Promise.resolve([])),
    [tab, managedTarget],
  );

  // 可更新 tab：进入即检查更新（lockfile 本地版本 vs 市场最新版本，结果整体缓存）
  const updates = useAsync<SkillUpdatesResult>(() => {
    const force = forceUpdates.current;
    forceUpdates.current = false;
    return tab === "updates"
      ? skillsApi.updates(force)
      : Promise.resolve({ items: [], fetchedAt: 0, fromCache: false });
  }, [tab]);
  const updateItems = updates.data?.items ?? [];
  const updatable = updateItems.filter((i) => i.hasUpdate);
  const uncheckable = updateItems.filter((i) => i.note);

  // 单项更新进行中的 key 集合 + 一键更新进行中标志
  const [updatingKeys, setUpdatingKeys] = useState<Set<string>>(new Set());
  const [updatingAll, setUpdateAllRunning] = useState(false);

  useEffect(() => {
    if (!managedTarget) {
      const first = (targets.data ?? []).find((t) => t.installed && t.count > 0);
      if (first) setManagedTarget(first.id);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [targets.data]);

  const updateOne = useCallback(async (item: SkillUpdateItem) => {
    setUpdatingKeys((prev) => new Set(prev).add(item.key));
    try {
      const r = await skillsApi.update(item.target, item.key);
      if (r.ok) {
        toast.success(t("「{name}」已更新", { name: item.name }), r.toVersion ? `${r.fromVersion || "?"} → ${r.toVersion}` : undefined);
      } else {
        toast.error(t("「{name}」更新失败", { name: item.name }), r.message);
      }
      await Promise.all([updates.reload(), targets.reload(), managed.reload()]);
    } catch (e) {
      toast.error(t("「{name}」更新失败", { name: item.name }), errText(e));
    } finally {
      setUpdatingKeys((prev) => {
        const next = new Set(prev);
        next.delete(item.key);
        return next;
      });
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [updates.reload, targets.reload, managed.reload]);

  const updateAll = useCallback(async () => {
    if (updatable.length === 0) return;
    const ok = await confirmDialog({
      title: t("一键更新 {n} 个技能", { n: updatable.length }),
      desc: t("将从 SkillHub 市场重新下载最新版本并覆盖安装（客户端需重启后生效）。"),
      confirmText: t("全部更新"),
    });
    if (!ok) return;
    setUpdateAllRunning(true);
    try {
      const results = await skillsApi.updateAll();
      const okCount = results.filter((r) => r.ok).length;
      const failCount = results.length - okCount;
      if (failCount === 0) {
        toast.success(t("全部更新完成"), t("{n} 个技能已是最新版本", { n: okCount }));
      } else {
        toast.info(t("更新完成（{ok} 成功 / {fail} 失败）", { ok: okCount, fail: failCount }), t("失败详情见列表中的错误提示"));
      }
      await Promise.all([updates.reload(), targets.reload(), managed.reload()]);
    } catch (e) {
      toast.error(t("一键更新失败"), errText(e));
    } finally {
      setUpdateAllRunning(false);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [updatable.length, updates.reload, targets.reload, managed.reload]);

  const installCLI = async () => {
    const ok = await confirmDialog({
      title: t("自动安装 skillhub CLI"),
      desc: t("将下载 SkillHub 官方安装脚本并执行（--cli-only，安装到 ~/.local/bin 与 ~/.skillhub，约 1~2 分钟）。"),
      confirmText: t("开始安装"),
    });
    if (!ok) return;
    setInstallingCLI(true);
    try {
      const s = await skillsApi.installCLI();
      toast.success(t("skillhub CLI 安装成功"), `${s.version} · ${s.path}`);
      await Promise.all([cli.reload(), targets.reload(), list.reload()]);
    } catch (e) {
      toast.error(t("skillhub CLI 安装失败"), errText(e));
    } finally {
      setInstallingCLI(false);
    }
  };

  const uninstall = useCallback(async (tg: SkillTargetInfo, s: InstalledSkill) => {
    const ok = await confirmDialog({
      title: t("卸载「{name}」", { name: s.name }),
      desc: t("将删除目录 {path}，该操作不可恢复。", { path: s.path }),
      danger: true,
      confirmText: t("卸载"),
    });
    if (!ok) return;
    try {
      await skillsApi.uninstall(tg.id, s.dirName);
      toast.success(t("已卸载"), s.dirName);
      await Promise.all([managed.reload(), targets.reload()]);
    } catch (e) {
      toast.error(t("卸载失败"), errText(e));
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [managed.reload, targets.reload]);

  const submitSearch = () => setQuery(input.trim());
  const skills = list.data?.skills ?? [];
  const cliMissingBanner = list.data?.cliMissing ?? false;

  return (
    <section className="page">
      <div className="page-head">
        <div>
          <h1>{t("技能市场")}</h1>
          <p>{t("SkillHub 技能库 · 浏览、搜索并一键安装到本机 AI 编程客户端")}</p>
        </div>
        <div className="head-actions">
          {!LOCAL_TABS.has(tab) && (
            <>
              <div className="search-box">
                <Search size={13} strokeWidth={2.2} />
                <input
                  className="search-input"
                  placeholder={t("搜索技能名称、描述或关键词…")}
                  value={input}
                  onChange={(e) => setInput(e.target.value)}
                  onKeyDown={(e) => e.key === "Enter" && submitSearch()}
                />
              </div>
              <button className="btn btn-soft" onClick={submitSearch}>{t("搜索")}</button>
            </>
          )}
        </div>
      </div>

      {/* CLI 状态卡片 */}
      <div className="card" style={{ marginBottom: 14 }}>
        <div className="card-b flex" style={{ gap: 12, alignItems: "center", justifyContent: "space-between", flexWrap: "wrap" }}>
          <div className="flex" style={{ gap: 12, alignItems: "center" }}>
            <span className="avatar" style={{ width: 34, height: 34, background: "linear-gradient(135deg,#0F6E56,#1D9E75)" }}>
              <Puzzle size={16} />
            </span>
            <div>
              <h3 style={{ fontSize: 13.5 }}>skillhub CLI</h3>
              <div className="sub" style={{ fontSize: 12 }}>
                {cli.loading ? t("探测中…") : cli.data?.installed
                  ? `${cli.data.version} · ${cli.data.path}`
                  : t("未安装 —— 浏览可用搜索接口，安装技能与榜单需要 CLI（可自动安装）")}
              </div>
            </div>
          </div>
          <div className="flex" style={{ gap: 8, alignItems: "center" }}>
            <span className={`badge ${cli.data?.installed ? "b-green" : "b-amber"}`}>
              <span className="d" />
              {cli.data?.installed ? t("已安装") : t("未安装")}
            </span>
            {!cli.data?.installed && (
              <button className="btn btn-primary sm" disabled={installingCLI || cli.loading} onClick={() => void installCLI()}>
                {installingCLI ? <Loader2 size={13} className="spin" /> : <Download size={13} strokeWidth={2} />}
                {installingCLI ? t("安装中，约 1~2 分钟…") : t("一键自动安装")}
              </button>
            )}
          </div>
        </div>
      </div>

      {/* 排序 Tab */}
      <div className="flex" style={{ gap: 10, alignItems: "center", marginBottom: 12 }}>
        <div className="seg">
          {TABS.map((tabDef) => (
            <button key={tabDef.value} className={tab === tabDef.value ? "on" : ""} onClick={() => setTab(tabDef.value)}>
              {tabDef.value === "updates" && updates.settled && updatable.length > 0
                ? t("可更新 · {n}", { n: updatable.length })
                : t(tabDef.label)}
            </button>
          ))}
        </div>
        {!LOCAL_TABS.has(tab) && query && (
          <span className="badge b-blue"><span className="d" />{t("“{query}” 的搜索结果", { query })}</span>
        )}
        <span className="spacer" />
        <button
          className="icon-btn"
          title={t("刷新")}
          onClick={() => {
            if (tab === "installed") {
              void targets.reload();
              void managed.reload();
            } else if (tab === "updates") {
              forceUpdates.current = true;
              void updates.reload();
            } else {
              forceBrowse.current = true;
              void list.reload();
              void cli.reload();
            }
          }}
        >
          <RefreshCw
            size={13}
            className={(tab === "installed" ? managed.loading : tab === "updates" ? updates.loading : list.loading) ? "spin" : undefined}
          />
        </button>
      </div>

      {tab === "installed" ? (
        /* 已安装管理（最后一个 tab） */
        <div className="card">
          <div className="card-h">
            <div>
              <h3>{t("已安装管理")}</h3>
              <div className="sub">{t("查看与卸载各客户端 skills 目录中的技能（安装后需重启对应客户端生效）")}</div>
            </div>
            <select className="select" value={managedTarget} onChange={(e) => setManagedTarget(e.target.value)}>
              {(targets.data ?? []).filter((t) => t.installed).map((t) => (
                <option key={t.id} value={t.id}>{t.name}（{t.count}）</option>
              ))}
            </select>
          </div>
          <div className="card-b">
            {!(targets.data ?? []).some((t) => t.installed) && (
              <div className="muted" style={{ fontSize: 12 }}>{t("未检测到已安装的客户端")}</div>
            )}
            {managedTarget && managed.loading && <div className="muted" style={{ fontSize: 12 }}>{t("加载中…")}</div>}
            {managedTarget && managed.error && <ErrorBlock message={managed.error} onRetry={() => void managed.reload()} />}
            {managedTarget && managed.settled && (managed.data ?? []).length === 0 && (
              <div className="muted" style={{ fontSize: 12 }}>{t("该客户端尚未安装任何技能 —— 切到「推荐」等分类页安装")}</div>
            )}
            {(managed.data ?? []).map((s) => {
              const tg = (targets.data ?? []).find((x) => x.id === managedTarget)!;
              return (
                <div className="flex" key={s.dirName} style={{ gap: 12, alignItems: "center", padding: "8px 0", borderBottom: "1px solid var(--outline)" }}>
                  <PackageOpen size={14} strokeWidth={2} style={{ flex: "none", color: "var(--text-3)" }} />
                  <div style={{ flex: 1, minWidth: 0 }}>
                    <b style={{ fontSize: 12.5 }}>{s.name}</b>
                    <div className="muted ellip" style={{ fontSize: 11.5 }} title={s.description}>{s.description || s.dirName}</div>
                  </div>
                  <span className="muted mono ellip" style={{ fontSize: 11, maxWidth: 260 }} title={s.path}>{s.path}</span>
                  <button className="btn btn-danger-soft sm" onClick={() => tg && void uninstall(tg, s)}>
                    <Trash2 size={12} strokeWidth={2} /> {t("卸载")}
                  </button>
                </div>
              );
            })}
          </div>
        </div>
      ) : tab === "updates" ? (
        /* 可更新（最后一个 tab）：lockfile 本地版本 vs 市场最新版本 */
        <div className="card">
          <div className="card-h">
            <div>
              <h3>{t("在线更新")}</h3>
              <div className="sub">
                {t("比对 SkillHub 市场最新版本（仅统计经技能市场安装的技能）")}
                {updates.data?.fetchedAt ? ` · ${t("检查于")} ${fmtAge(updates.data.fetchedAt)}${updates.data.fromCache ? t("（缓存）") : ""}` : ""}
              </div>
            </div>
            <div className="flex" style={{ gap: 8, alignItems: "center" }}>
              <button
                className="btn btn-soft sm"
                disabled={updates.loading}
                onClick={() => {
                  forceUpdates.current = true;
                  void updates.reload();
                }}
              >
                <RefreshCw size={12} strokeWidth={2} className={updates.loading ? "spin" : undefined} />
                {updates.loading ? t("检查中…") : t("重新检查")}
              </button>
              <button
                className="btn btn-primary sm"
                disabled={updatingAll || updatable.length === 0}
                onClick={() => void updateAll()}
              >
                {updatingAll ? <Loader2 size={13} className="spin" /> : <Download size={13} strokeWidth={2} />}
                {updatingAll ? t("更新中…") : t(updatable.length > 0 ? "一键更新（{n}）" : "一键更新", { n: updatable.length })}
              </button>
            </div>
          </div>
          <div className="card-b">
            {updates.error && <ErrorBlock message={updates.error} onRetry={() => void updates.reload()} />}
            {updates.loading && updateItems.length === 0 && (
              <div className="muted" style={{ fontSize: 12 }}>{t("正在比对各客户端已装技能的版本…")}</div>
            )}
            {updates.settled && updateItems.length === 0 && !updates.error && (
              <div className="muted" style={{ fontSize: 12 }}>
                {t("还没有经技能市场安装的技能 —— 在「推荐」等分类页安装后可在此检查更新")}
              </div>
            )}
            {updates.settled && updatable.length === 0 && uncheckable.length === 0 && updateItems.length > 0 && (
              <EmptyBlock title={t("所有技能均为最新版本")} desc={t("点「重新检查」可随时再次比对")} />
            )}
            {updatable.map((item) => {
              const busy = updatingAll || updatingKeys.has(item.key);
              return (
                <div className="flex" key={`${item.target}/${item.key}`} style={{ gap: 12, alignItems: "center", padding: "8px 0", borderBottom: "1px solid var(--outline)" }}>
                  <PackageOpen size={14} strokeWidth={2} style={{ flex: "none", color: "var(--text-3)" }} />
                  <div style={{ flex: 1, minWidth: 0 }}>
                    <div className="flex" style={{ gap: 8, alignItems: "center", flexWrap: "wrap" }}>
                      <b style={{ fontSize: 12.5 }}>{item.name}</b>
                      <span className="badge b-indigo">{item.targetName}</span>
                      <span className="muted mono" style={{ fontSize: 11 }}>
                        v{item.installedVersion || "?"} <ArrowRight size={10} strokeWidth={2.2} style={{ verticalAlign: -1 }} /> <span style={{ color: "var(--ok, #3B6D11)", fontWeight: 600 }}>v{item.latestVersion}</span>
                      </span>
                    </div>
                    {item.description && (
                      <div className="muted ellip" style={{ fontSize: 11.5, marginTop: 2 }} title={item.description}>{item.description}</div>
                    )}
                  </div>
                  <span className="muted mono ellip" style={{ fontSize: 11, maxWidth: 200 }} title={item.key}>{item.key}</span>
                  <button className="btn btn-primary sm" disabled={busy} onClick={() => void updateOne(item)}>
                    {busy ? <Loader2 size={12} className="spin" /> : <Download size={12} strokeWidth={2} />}
                    {busy ? t("更新中…") : t("更新")}
                  </button>
                </div>
              );
            })}
            {updates.settled && updatable.length > 0 && uncheckable.length > 0 && (
              <div className="muted" style={{ fontSize: 11.5, marginTop: 8 }}>
                {t("另有 {n} 项无法检查版本：", { n: uncheckable.length })}{uncheckable.map((i) => i.key).join("、")}
              </div>
            )}
            {updates.settled && uncheckable.length > 0 && updatable.length === 0 && updateItems.length > 0 && (
              <div className="notice" style={{ marginTop: 8, fontSize: 11.5, lineHeight: 1.7 }}>
                {uncheckable.map((i) => (
                  <div key={`${i.target}/${i.key}`} className="mono">
                    {i.key}（{i.targetName}）：{i.note}
                  </div>
                ))}
              </div>
            )}
          </div>
        </div>
      ) : (
        <>
          {cliMissingBanner && (
            <div className="notice" style={{ marginBottom: 12 }}>
              {t("CLI 未安装，当前为搜索接口的降级列表（仅前 40 条）。安装 CLI 后可查看完整的下载榜 / 飙升榜 / 新品榜。")}
            </div>
          )}

          {list.error && <ErrorBlock message={list.error} onRetry={() => void list.reload()} />}

          {/* 技能列表 */}
          <div className="stack" style={{ gap: 10 }}>
            {!list.settled && (
              <div className="card"><div className="card-b muted" style={{ fontSize: 12.5 }}>{t("加载中…")}</div></div>
            )}
            {list.settled && skills.length === 0 && !list.error && (
              <EmptyBlock title={t("没有匹配的技能")} desc={t("换个关键词或切换分类试试")} />
            )}
            {skills.map((s) => (
              <div className="card" key={`${s.namespace}/${s.slug}`}>
                <div className="card-b flex" style={{ gap: 14, alignItems: "flex-start" }}>
                  <SkillIcon skill={s} />
                  <div style={{ flex: 1, minWidth: 0 }}>
                    <div className="flex" style={{ gap: 8, alignItems: "center", flexWrap: "wrap" }}>
                      <b style={{ fontSize: 13.5 }}>{s.name}</b>
                      {s.category && <span className="badge b-indigo">{t(CATEGORY_ZH[s.category] ?? s.category)}</span>}
                      {s.requiresApiKey && (
                        <span className="badge b-amber"><KeyRound size={10} strokeWidth={2.4} style={{ verticalAlign: -1 }} /> {t("需配置 API Key")}</span>
                      )}
                      <span className="sub mono" style={{ fontSize: 11 }}>v{s.version}</span>
                    </div>
                    <div
                      className="muted"
                      style={{ fontSize: 12.5, lineHeight: 1.55, marginTop: 5, display: "-webkit-box", WebkitLineClamp: 2, WebkitBoxOrient: "vertical", overflow: "hidden" }}
                      title={s.description}
                    >
                      {s.description || t("（无描述）")}
                    </div>
                    <div className="flex muted" style={{ gap: 14, fontSize: 11.5, marginTop: 6, alignItems: "center" }}>
                      <span className="mono">@{s.namespace || s.slug}</span>
                      <span><Download size={11} strokeWidth={2.2} style={{ verticalAlign: -1.5 }} /> {fmtCount(s.downloads)}</span>
                      <span><Star size={11} strokeWidth={2.2} style={{ verticalAlign: -1.5 }} /> {fmtCount(s.stars)}</span>
                      <span>{t("更新于 {time}", { time: fmtTime(s.updatedAt) })}</span>
                    </div>
                  </div>
                  <div className="flex" style={{ gap: 8, alignItems: "center", flex: "none" }}>
                    <button className="btn btn-primary sm" onClick={() => setInstallSkill(s)}>
                      <Download size={13} strokeWidth={2} /> {t("安装")}
                    </button>
                  </div>
                </div>
              </div>
            ))}
          </div>
        </>
      )}

      {/* 安装弹窗 */}
      {installSkill && (
        <InstallDialog
          skill={installSkill}
          targets={targets.data ?? []}
          onClose={() => setInstallSkill(null)}
          onInstalled={() => { void targets.reload(); void managed.reload(); }}
        />
      )}

      {/* 外链提示 */}
      <div className="muted" style={{ fontSize: 11.5, marginTop: 14 }}>
        {t("数据来源 SkillHub（skillhub.cn）")}
        <ExternalLink size={11} strokeWidth={2} style={{ verticalAlign: -1.5, marginLeft: 4 }} />
        {t("，共收录 15 万+ 技能")}
        {list.data?.fetchedAt && !LOCAL_TABS.has(tab) && (
          <> · {t("数据缓存于")} {fmtAge(list.data.fetchedAt)}，{t("缓存时长可在 设置 → 定时任务 中调整（点刷新强制更新）")}</>
        )}
      </div>
    </section>
  );
}

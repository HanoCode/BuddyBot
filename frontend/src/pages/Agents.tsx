import { useCallback, useEffect, useMemo, useState } from "react";
import {
  Bot, Check, CheckCircle2, ChevronDown, Clock3, Loader2, MonitorSmartphone, RefreshCw, Search, Undo2,
} from "lucide-react";
import { agentsApi, modelsApi } from "../services/api";
import { errText, useAsync } from "../hooks/useAsync";
import { confirmDialog, toast } from "../components/common/Feedback";
import { EmptyRow, ErrorBlock, SkeletonRows } from "../components/common/StateBlock";
import { useT, t as tr } from "../i18n";
import type { AgentBackup, AgentTarget } from "../types";

/** 支持的客户端说明 */
const AGENT_DESC: Record<string, string> = {
  "claude-code": "写入 ~/.claude/settings.json 的 env（ANTHROPIC_BASE_URL / AUTH_TOKEN / 四个模型槽位）",
  codex: "写入 ~/.codex/config.toml 的 model_provider 与 auth.json 的 OPENAI_API_KEY",
  opencode: "在 ~/.config/opencode/opencode.json 增加 provider.workbuddy",
  pi: "在 ~/.pi/agent/models.json 增加 providers.workbuddy",
  "kimi-code": "写入 ~/.kimi-code/config.toml 的 default_model 与 providers.workbuddy",
  codebuddy: "在 ~/.codebuddy/models.json 写入 vendor=WorkBuddy 的自定义模型条目（其余模型原样保留）",
  workbuddy: "在 ~/.workbuddy/models.json 写入 vendor=WorkBuddy 的自定义模型条目（其余模型原样保留）",
};

export default function Agents() {
  const t = useT();
  const targets = useAsync<AgentTarget[]>(() => agentsApi.list(), []);
  const models = useAsync<{ id: string }[]>(() => modelsApi.list(), []);

  // 选中的模型（默认取模型清单前 3 个可用模型）
  const [picked, setPicked] = useState<Set<string>>(new Set());
  useEffect(() => {
    if (picked.size === 0 && models.data?.length) {
      setPicked(new Set(models.data.slice(0, 3).map((m) => m.id)));
    }
    // 仅在模型清单首次到位时初始化
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [models.data]);

  const toggleModel = (id: string) =>
    setPicked((prev) => {
      const next = new Set(prev);
      next.has(id) ? next.delete(id) : next.add(id);
      return next;
    });

  // 模型家族：取 id 首段（claude-sonnet-4 → claude / gpt-4o → gpt），用于分类筛选
  const familyOf = useCallback((id: string) => id.split(/[-/_.]/)[0] || id, []);

  // 搜索 + 家族筛选
  const [kw, setKw] = useState("");
  const [cat, setCat] = useState("all");
  const allModels = useMemo(() => models.data ?? [], [models.data]);
  const families = useMemo(() => {
    const map = new Map<string, number>();
    for (const m of allModels) map.set(familyOf(m.id), (map.get(familyOf(m.id)) ?? 0) + 1);
    return [...map.entries()].sort((a, b) => a[0].localeCompare(b[0]));
  }, [allModels, familyOf]);
  const filtered = useMemo(() => {
    const k = kw.trim().toLowerCase();
    return allModels.filter(
      (m) => (cat === "all" || familyOf(m.id) === cat) && (!k || m.id.toLowerCase().includes(k)),
    );
  }, [allModels, kw, cat, familyOf]);
  // 紧凑模式：搜索 + 家族下拉过滤，不再分组
  const visibleModels = useMemo(() => filtered, [filtered]);

  // 每个客户端的备份列表（懒加载：点开时拉取）
  const [backups, setBackups] = useState<Record<string, AgentBackup[]>>({});
  const [expanded, setExpanded] = useState<string | null>(null);
  const loadBackups = useCallback(async (target: string) => {
    try {
      const list = await agentsApi.backups(target);
      setBackups((b) => ({ ...b, [target]: list }));
    } catch (e) {
      toast.error(tr("读取备份失败"), errText(e));
    }
  }, []);

  const [busy, setBusy] = useState<string | null>(null);
  const apply = async (t: AgentTarget) => {
    if (picked.size === 0) {
      toast.warn(tr("请至少选择一个模型"));
      return;
    }
    if (t.configured) {
      const ok = await confirmDialog({
        title: tr("更新 {name} 配置", { name: t.name }),
        desc: tr("该客户端已接入过本网关，将覆盖网关相关字段（其余配置保留，原文件自动备份）。"),
        confirmText: tr("更新"),
      });
      if (!ok) return;
    }
    setBusy(t.id);
    try {
      const res = await agentsApi.apply({ target: t.id, models: [...picked] });
      toast.success(tr("{name} 接入成功", { name: t.name }), tr("写入 {n} 个文件，备份于 {dir}", { n: res.files?.length ?? 0, dir: res.backupDir }));
      await targets.reload();
      void loadBackups(t.id);
    } catch (e) {
      toast.error(tr("{name} 接入失败", { name: t.name }), errText(e));
    } finally {
      setBusy(null);
    }
  };

  const restore = async (t: AgentTarget, b: AgentBackup) => {
    const ok = await confirmDialog({
      title: tr("回滚 {name} 配置", { name: t.name }),
      desc: tr("将把备份 {id} 中的 {n} 个文件恢复到原路径，当前配置会被覆盖。", { id: b.id, n: (b.files ?? []).length }),
      danger: true,
      confirmText: tr("回滚"),
    });
    if (!ok) return;
    setBusy(t.id);
    try {
      const n = await agentsApi.restore(t.id, b.id);
      toast.success(tr("{name} 已回滚", { name: t.name }), tr("恢复了 {n} 个文件", { n }));
      await targets.reload();
      void loadBackups(t.id);
    } catch (e) {
      toast.error(tr("回滚失败"), errText(e));
    } finally {
      setBusy(null);
    }
  };

  const rows = targets.data ?? [];
  const installedCount = useMemo(() => rows.filter((t) => t.installed).length, [rows]);

  return (
    <section className="page">
      <div className="page-head">
        <div>
          <h1>{t("智能体接入")}</h1>
          <p>
            {t("一键把本机网关写入 Claude Code / Codex 等 AI 编程客户端的配置 · 检测到 {a} / {b} 个客户端", { a: installedCount, b: rows.length })}
          </p>
        </div>
        <div className="head-actions">
          <button className="btn btn-ghost" onClick={() => void targets.reload()}>
            <RefreshCw size={13} strokeWidth={2} /> {t("重新探测")}
          </button>
        </div>
      </div>

      {targets.error && <ErrorBlock message={targets.error} onRetry={() => void targets.reload()} />}

      {/* 模型选择 */}
      <div className="card" style={{ marginBottom: 14 }}>
        <div className="card-h">
          <div>
            <h3>{t("接入模型")}</h3>
            <div className="sub">{t("选择要写入客户端的模型（Claude Code 只取前 4 个，其余客户端全量写入）")}</div>
          </div>
          <span className="badge b-blue"><span className="d" />{t("已选 {n} 个", { n: picked.size })}</span>
        </div>
        <div className="card-b">
          {!models.settled && <SkeletonRows colSpan={1} />}
          {models.settled && (models.data ?? []).length === 0 && <EmptyRow colSpan={1} text={t("模型清单为空：先在账号页完成保活以拉取上游模型")} />}
          {allModels.length > 0 && (
            <>
              {/* 紧凑工具行：搜索 + 家族筛选 + 批量操作 */}
              <div className="flex" style={{ gap: 8, marginBottom: 8 }}>
                <div className="flex" style={{ gap: 6, alignItems: "center", flex: 1, position: "relative" }}>
                  <Search size={13} style={{ position: "absolute", left: 9, opacity: 0.55, pointerEvents: "none" }} />
                  <input
                    className="mini-input"
                    style={{ flex: 1, paddingLeft: 26 }}
                    placeholder={t("搜索模型…")}
                    value={kw}
                    onChange={(e) => setKw(e.target.value)}
                  />
                </div>
                <select className="mini-input" style={{ width: 130, flex: "none" }} value={cat} onChange={(e) => setCat(e.target.value)}>
                  <option value="all">{t("全部分类")} ({allModels.length})</option>
                  {families.map(([f, n]) => (
                    <option key={f} value={f}>{f} ({n})</option>
                  ))}
                </select>
                <button className="btn btn-ghost sm" style={{ flex: "none" }} disabled={visibleModels.length === 0} onClick={() => setPicked((prev) => { const next = new Set(prev); for (const m of visibleModels) next.add(m.id); return next; })}>{t("全选")}</button>
                <button className="btn btn-ghost sm" style={{ flex: "none" }} disabled={picked.size === 0} onClick={() => setPicked(new Set())}>{t("清空")}</button>
              </div>
              {/* 模型 chips：固定高度内部滚动，不撑开页面 */}
              <div style={{ maxHeight: 168, overflowY: "auto" }}>
                <div className="flex" style={{ gap: 8, flexWrap: "wrap" }}>
                  {visibleModels.map((m) => {
                    const on = picked.has(m.id);
                    return (
                      <button
                        key={m.id}
                        className={`chip-btn${on ? " on" : ""}`}
                        onClick={() => toggleModel(m.id)}
                        data-tip={on ? tr("点击取消选择") : tr("点击选择")}
                      >
                        {on ? <Check size={12} strokeWidth={2.6} /> : null}
                        {m.id}
                      </button>
                    );
                  })}
                </div>
                {visibleModels.length === 0 && <div className="muted" style={{ fontSize: 12, padding: "8px 0" }}>{t("没有匹配的模型")}</div>}
              </div>
              {filtered.length < allModels.length && (
                <div className="muted" style={{ fontSize: 11, marginTop: 6 }}>
                  {t("显示 {a} / {b} 个模型", { a: filtered.length, b: allModels.length })}
                </div>
              )}
            </>
          )}
        </div>
      </div>

      {/* 客户端卡片列表 */}
      <div className="stack" style={{ gap: 20, marginTop: 18 }}>
        {!targets.settled && (
          <div className="card"><div className="card-b"><SkeletonRows colSpan={1} /></div></div>
        )}
        {targets.settled && rows.length === 0 && <EmptyRow colSpan={1} text={t("没有可用客户端")} />}
        {rows.map((t) => (
          <div className="card" key={t.id}>
            <div className="card-h">
              <div className="flex" style={{ gap: 10, alignItems: "flex-start" }}>
                <span className="avatar" style={{ width: 34, height: 34, background: "linear-gradient(135deg,#6366F1,#8B5CF6)" }}>
                  {t.id === "claude-code" || t.id === "codex" ? <MonitorSmartphone size={16} /> : <Bot size={16} />}
                </span>
                <div>
                  <h3>{t.name}</h3>
                  <div className="sub mono" style={{ fontSize: 11 }}>{t.configPath}</div>
                  <div className="sub" style={{ fontSize: 12, marginTop: 4 }}>{tr(AGENT_DESC[t.id] ?? "")}</div>
                </div>
              </div>
              <div className="flex" style={{ gap: 8, alignItems: "center" }}>
                <span className={`badge ${t.configured ? "b-green" : t.installed ? "b-amber" : "b-gray"}`}>
                  <span className="d" />
                  {t.configured ? tr("已接入") : t.installed ? tr("未接入") : tr("未安装")}
                </span>
                <button
                  className="btn btn-soft sm"
                  disabled={!t.installed || busy === t.id || picked.size === 0}
                  data-tip={t.installed ? tr("写入网关配置（自动备份）") : tr("未检测到该客户端")}
                  onClick={() => void apply(t)}
                >
                  {busy === t.id ? <Loader2 size={13} className="spin" /> : <CheckCircle2 size={13} strokeWidth={2} />}
                  {t.configured ? tr("更新配置") : tr("一键接入")}
                </button>
                <button
                  className="icon-btn"
                  data-tip={tr("历史备份")}
                  onClick={() => {
                    const next = expanded === t.id ? null : t.id;
                    setExpanded(next);
                    if (next) void loadBackups(t.id);
                  }}
                >
                  <ChevronDown size={14} className={expanded === t.id ? "spin-90" : undefined} style={{ transition: "transform .2s", transform: expanded === t.id ? "rotate(180deg)" : undefined }} />
                </button>
              </div>
            </div>
            {expanded === t.id && (
              <div className="card-b" style={{ borderTop: "1px solid var(--border)", paddingTop: 10 }}>
                {(backups[t.id] ?? []).length === 0 ? (
                  <div className="muted" style={{ fontSize: 12 }}>
                    <Clock3 size={12} style={{ verticalAlign: -2 }} /> {tr("暂无备份（首次接入前没有旧配置）")}
                  </div>
                ) : (
                  (backups[t.id] ?? []).map((b) => (
                    <div className="flex" key={b.id} style={{ gap: 10, alignItems: "center", padding: "6px 0" }}>
                      <span className="mono" style={{ fontSize: 12 }}>{b.id}</span>
                      <span className="muted" style={{ fontSize: 12 }}>{tr("{n} 个文件", { n: b.files?.length ?? 0 })}</span>
                      <span className="spacer" />
                      <button className="btn btn-ghost sm" disabled={busy === t.id} onClick={() => void restore(t, b)}>
                        <Undo2 size={12} strokeWidth={2} /> {tr("回滚")}
                      </button>
                    </div>
                  ))
                )}
              </div>
            )}
          </div>
        ))}
      </div>
    </section>
  );
}

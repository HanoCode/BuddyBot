import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  AlertTriangle, Check, Globe, Loader2, Play, RefreshCw, Square, Zap,
} from "lucide-react";
import { ferryApi } from "../services/api";
import { errText } from "../hooks/useAsync";
import { toast } from "../components/common/Feedback";
import { EmptyRow, ErrorBlock, SkeletonRows } from "../components/common/StateBlock";
import { useT, t as tr } from "../i18n";
import type { FerryModelView, FerryStatus } from "../types";

/** 判定类别的中文标签 */
const CATEGORY_LABEL: Record<string, string> = {
  available: "可用",
  quota: "额度用尽",
  rate_limit: "限流",
  access: "无权限",
  timeout: "超时",
  error: "失败",
};

/** 阶段标签 */
const PHASE_LABEL: Record<string, string> = {
  disabled: "未启用",
  starting: "启动中",
  downloading: "准备运行时",
  reading: "读取模型",
  probing: "检测中",
  ready: "运行中",
  error: "出错",
};

export default function Ferry() {
  const t = useT();
  const [status, setStatus] = useState<FerryStatus | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [settled, setSettled] = useState(false);
  const [busy, setBusy] = useState<string | null>(null);
  const [pending, setPending] = useState<Set<string>>(new Set());
  const alive = useRef(true);

  const load = useCallback(async () => {
    try {
      const s = await ferryApi.status();
      if (!alive.current) return;
      setStatus(s);
      setError(null);
    } catch (e) {
      if (!alive.current) return;
      setError(errText(e));
    } finally {
      if (alive.current) setSettled(true);
    }
  }, []);

  useEffect(() => {
    alive.current = true;
    void load();
    return () => {
      alive.current = false;
    };
  }, [load]);

  // 启动/下载/检测期间状态一直在变，按秒跟一下；稳定后停在 10 秒（对齐侧栏节奏）
  const active = status?.probing || ["starting", "downloading", "reading", "probing"].includes(status?.phase ?? "");
  useEffect(() => {
    const timer = setInterval(() => void load(), active ? 1500 : 10000);
    return () => clearInterval(timer);
  }, [active, load]);

  const run = async (key: string, fn: () => Promise<FerryStatus | void>) => {
    setBusy(key);
    try {
      const next = await fn();
      if (next) setStatus(next);
      else await load();
    } catch (e) {
      toast.error(tr("操作失败"), errText(e));
      await load();
    } finally {
      setBusy(null);
    }
  };

  const rows = useMemo<FerryModelView[]>(() => status?.models ?? [], [status]);
  const available = rows.filter((m) => m.selected).length;
  const probing = status?.probing ?? false;

  const toggle = async (id: string, selected: boolean) => {
    setPending((prev) => new Set(prev).add(id));
    try {
      const next = await ferryApi.setSelected(id, selected);
      setStatus(next);
    } catch (e) {
      toast.error(tr("操作失败"), errText(e));
    } finally {
      setPending((prev) => {
        const n = new Set(prev);
        n.delete(id);
        return n;
      });
    }
  };

  const phase = status?.phase ?? "disabled";

  return (
    <section className="page">
      <div className="page-head">
        <div>
          <h1>{t("免费额度")}</h1>
          <p>{t("把 OpenCode 的免费模型摆渡进本网关 · 免费由价格判定，可用由探测判定 · 当前可提供 {n} 个模型", { n: available })}</p>
        </div>
        <div className="head-actions">
          <button
            className="btn btn-ghost"
            disabled={busy !== null || !status?.enabled}
            onClick={() => void run("refresh", () => ferryApi.refresh())}
          >
            {busy === "refresh" ? <Loader2 size={13} className="spin" /> : <RefreshCw size={13} strokeWidth={2} />} {t("读取模型")}
          </button>
          <button
            className="btn btn-ghost"
            disabled={busy !== null || !status?.enabled || probing}
            onClick={() => void run("probe", () => ferryApi.probe("", true))}
          >
            {busy === "probe" ? <Loader2 size={13} className="spin" /> : <Play size={13} strokeWidth={2} />} {t("重新检测")}
          </button>
          {status?.enabled ? (
            <button className="btn btn-ghost" disabled={busy !== null} onClick={() => void run("toggle", () => ferryApi.setEnabled(false))}>
              <Square size={13} strokeWidth={2} /> {t("停用")}
            </button>
          ) : (
            <button className="btn btn-primary" disabled={busy !== null} onClick={() => void run("toggle", () => ferryApi.setEnabled(true))}>
              <Zap size={13} strokeWidth={2} /> {t("启用")}
            </button>
          )}
        </div>
      </div>

      {error && <ErrorBlock message={error} onRetry={() => void load()} />}

      <div className="card" style={{ marginBottom: 14 }}>
        <div className="card-h">
          <div>
            <h3>{t("运行状态")}</h3>
            <div className="sub">{t("隔离运行时托管在数据目录的 ferry/ 下，与系统上其它 OpenCode 互不影响")}</div>
          </div>
          <span className={`badge ${phase === "ready" ? "b-green" : phase === "error" ? "b-red" : "b-blue"}`}>
            <span className="d" />{t(PHASE_LABEL[phase] ?? phase)}
          </span>
        </div>
        <div className="card-b">
          <div className="flex" style={{ gap: 18, flexWrap: "wrap" }}>
            <Stat label={t("说明")} value={status?.message ?? "—"} />
            <Stat label={t("模型总数")} value={String(status?.modelCount ?? 0)} />
            <Stat label={t("可用模型")} value={String(status?.availableCount ?? 0)} />
            <Stat label={t("隔离 OpenCode")} value={status?.opencodeVersion || "—"} />
            <Stat label={t("系统代理")} value={status?.proxyEnabled ? t("跟随") : t("直连")} />
          </div>
          {probing && (
            <div className="flex muted" style={{ gap: 8, alignItems: "center", marginTop: 10 }}>
              <Loader2 size={13} className="spin" />
              {t("检测进行中：正在检测 {a} 个，剩余 {b} 个", { a: status?.probeActive?.length ?? 0, b: status?.probePending?.length ?? 0 })}
              {(status?.probeActive ?? []).length > 0 && <span>· {status?.probeActive?.join("、")}</span>}
            </div>
          )}
          {status?.lastError && (
            <div className="flex" style={{ gap: 6, alignItems: "flex-start", marginTop: 10, fontSize: 12.5, color: "var(--red)" }}>
              <AlertTriangle size={13} style={{ marginTop: 2, flex: "none" }} />
              <span>{status.lastError}</span>
            </div>
          )}
          <div className="flex" style={{ gap: 8, marginTop: 12 }}>
            <button
              className="btn btn-ghost sm"
              disabled={busy !== null}
              onClick={() => void run("proxy", () => ferryApi.setSystemProxy(!status?.proxyEnabled))}
            >
              <Globe size={12} strokeWidth={2} />
              {status?.proxyEnabled ? t("改为直连") : t("跟随系统代理")}
            </button>
            <span className="muted" style={{ alignSelf: "center" }}>
              {t("下载隔离运行时与访问免费模型时用；切换会重启隔离服务")}
            </span>
          </div>
        </div>
      </div>

      <div className="card">
        <div className="card-h">
          <div>
            <h3>{t("免费模型")}</h3>
            <div className="sub">{t("通过的模型会成为本网关的模型：既出现在 /v1/models，也出现在「智能体接入」与「密钥」的模型选择里；取消勾选即不再对外提供")}</div>
          </div>
          <span className="badge b-blue"><span className="d" />{t("可用 {n} 个", { n: available })}</span>
        </div>
        <div className="card-b">
          <table className="tbl">
            <thead>
              <tr>
                <th>{t("模型")}</th><th>{t("能力")}</th><th>{t("判定")}</th>
                <th>{t("耗时")}</th><th style={{ width: 96 }}>{t("对外提供")}</th>
              </tr>
            </thead>
            <tbody>
              {!settled && <SkeletonRows colSpan={5} rows={4} />}
              {settled && rows.length === 0 && (
                <EmptyRow colSpan={5} text={status?.enabled ? t("还没有读取到免费模型：点「读取模型」重试，或查看上方状态里的错误") : t("未启用：点右上角「启用」开始（首次会在数据目录里下载隔离 OpenCode 运行时）")} />
              )}
              {rows.map((m) => (
                <tr key={m.id}>
                  <td>
                    <div style={{ fontWeight: 500 }}>{m.name}</div>
                    <div className="mono" style={{ fontSize: 11, color: "var(--text-3)" }}>{m.id}</div>
                  </td>
                  <td>
                    <div className="flex" style={{ gap: 6, flexWrap: "wrap" }}>
                      {/* 能力徽标只在探测确认后才敢这么说：没判定或判定失败时，
                          不能拿目录里的声明当成已验证的事实 */}
                      {m.verdict?.ok && !m.verdict.chatOnly ? (
                        <span className="badge b-green"><span className="d" />{t("工具")}</span>
                      ) : m.verdict?.chatOnly ? (
                        <span className="badge b-amber"><span className="d" />{t("仅对话")}</span>
                      ) : (
                        <span className="badge"><span className="d" />{t("待检测")}</span>
                      )}
                      {m.reasoning && <span className="badge">{t("推理")}</span>}
                      {m.images && <span className="badge">{t("图片")}</span>}
                      {m.context ? <span className="badge">{fmtTokens(m.context)} {t("上下文")}</span> : null}
                    </div>
                  </td>
                  <td>
                    {m.verdict ? (
                      <div>
                        <span className={`badge ${m.verdict.ok ? "b-green" : "b-red"}`}>
                          <span className="d" />{t(CATEGORY_LABEL[m.verdict.category] ?? m.verdict.category)}
                        </span>
                        {m.verdict.source === "cache" && <span className="muted" style={{ marginLeft: 6 }}>{t("缓存判定")}</span>}
                        {m.verdict.error && (
                          <div className="muted" style={{ fontSize: 11.5, marginTop: 4, maxWidth: 420 }} title={m.verdict.error}>
                            {m.verdict.error}
                          </div>
                        )}
                      </div>
                    ) : (
                      <span className="muted">{t("待检测")}</span>
                    )}
                  </td>
                  <td>{m.verdict?.durationMs ? `${(m.verdict.durationMs / 1000).toFixed(1)}s` : "—"}</td>
                  <td>
                    <button
                      className="btn btn-ghost sm"
                      disabled={!m.available || pending.has(m.id)}
                      title={m.available ? t("勾选/取消对外提供") : t("未通过检测的模型不对外提供")}
                      onClick={() => void toggle(m.id, !m.selected)}
                    >
                      {pending.has(m.id) ? <Loader2 size={12} className="spin" /> : m.selected ? <Check size={12} strokeWidth={2.6} /> : null}
                      {m.selected ? t("已勾选") : t("未勾选")}
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </div>
    </section>
  );
}

function Stat({ label, value }: { label: string; value: string }) {
  // 字号/颜色对齐全局 .kv 约定（tokens.css / app.css）：标签 11px text-3，值 12.5px/650
  return (
    <div style={{ minWidth: 96 }}>
      <div className="muted" style={{ fontSize: 11 }}>{label}</div>
      <div style={{ fontSize: 12.5, fontWeight: 650, fontVariantNumeric: "tabular-nums", userSelect: "text" }}>{value}</div>
    </div>
  );
}

function fmtTokens(n: number): string {
  if (n >= 1_000_000) return `${Math.round(n / 100_000) / 10}M`;
  if (n >= 1000) return `${Math.round(n / 1000)}K`;
  return String(n);
}

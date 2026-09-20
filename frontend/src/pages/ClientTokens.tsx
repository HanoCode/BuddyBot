import { useEffect, useMemo, useState } from "react";
import { AppWindow, RefreshCw } from "lucide-react";
import { statsApi } from "../services/api";
import { useAsync } from "../hooks/useAsync";
import { toast } from "../components/common/Feedback";
import { EmptyBlock, ErrorBlock, LoadingBlock } from "../components/common/StateBlock";
import type { ClientTokenStats } from "../types";
import { useT } from "../i18n";

const RANGES = [
  { days: 1, label: "1 天" },
  { days: 3, label: "3 天" },
  { days: 7, label: "近 7 天" },
  { days: 30, label: "近 30 天" },
];

const MODEL_COLORS = ["#6366F1", "#38BDF8", "#8B5CF6", "#F59E0B", "#10B981", "#EC4899", "#14B8A6"];

// 堆叠段配色：未命中输入 / 缓存读 / 输出 / 缓存写
const SEG_COLORS = ["#6366F1", "#8CB2D8", "#10B981", "#F59E0B"];

// 日历热力图色阶（与「Token 消耗」页日活分布一致）
const HEAT_L = ["#ECEFF4", "#DFE8F4", "#C7DAEF", "#ABC9E5", "#8CB2D8", "#6D97C6", "#4F79A9", "#35597F"];
const HEAT_D = ["#262B36", "#2C3847", "#33455A", "#3B536E", "#446182", "#4F7096", "#5F82AC", "#7BA0C9"];

const fmtTok = (v: number) =>
  v >= 1e9 ? (v / 1e9).toFixed(2) + "B" : v >= 1e6 ? (v / 1e6).toFixed(2) + "M" : v >= 1e3 ? (v / 1e3).toFixed(1) + "K" : String(v);

// 金额（元）：≥1 元保留 2 位；小额保留 4 位，避免零星用量被抹成 ¥0.00
const fmtMoney = (v: number) => "¥" + (v >= 1 ? v.toFixed(2) : v.toFixed(4));

export default function ClientTokens() {
  const t = useT();
  const [days, setDays] = useState(7);
  const [rescanning, setRescanning] = useState(false);

  const stats = useAsync<ClientTokenStats | null>(() => statsApi.clientTokenStats(days), [days]);
  // 「重新扫描」结果为本地覆盖态：跳过 5 分钟缓存的真实重扫，切换时间窗后失效
  const [forced, setForced] = useState<ClientTokenStats | null>(null);
  useEffect(() => setForced(null), [days]);
  const s = forced && forced.days === days ? forced : stats.data;

  // 日历热力图：行 = 天，列 = 20 分钟桶，颜色 = 桶内去重活跃会话数
  const ramp = typeof document !== "undefined" && document.documentElement.dataset.theme === "dark" ? HEAT_D : HEAT_L;
  const heat = useMemo(() => {
    const daysArr = s?.activeHeatmapDays ?? [];
    const matrix = s?.activeHeatmap ?? [];
    let max = 0;
    for (const row of matrix) for (const v of row ?? []) max = Math.max(max, v ?? 0);
    const dayActive = new Map<string, number>((s?.daily ?? []).map((d) => [d.date, d.activeSessions]));
    return { daysArr, matrix, max, dayActive };
  }, [s]);
  const lvl = (v: number) => (v <= 0 || heat.max <= 0 ? 0 : Math.min(7, 1 + Math.floor(Math.sqrt(v / heat.max) * 7)));

  const rescan = async () => {
    setRescanning(true);
    try {
      setForced(await statsApi.clientTokenStats(days, true));
    } catch (e) {
      toast.error(t("扫描失败"), String(e));
    } finally {
      setRescanning(false);
    }
  };

  // 日趋势堆叠段：未命中输入 / 缓存读 / 输出 / 缓存写
  const daily = useMemo(() => {
    const rows = (s?.daily ?? []).map((d) => ({
      ...d,
      uncached: Math.max(0, d.inputTokens - d.cacheRead),
    }));
    const max = Math.max(1, ...rows.map((d) => d.totalTokens));
    return { rows, max };
  }, [s]);

  const topModels = useMemo(
    () => (s?.models ?? []).slice(0, 8).map((m, i) => ({ ...m, color: MODEL_COLORS[i % MODEL_COLORS.length] })),
    [s],
  );
  const topProjects = useMemo(() => (s?.projects ?? []).slice(0, 8), [s]);

  const missing = (s?.sources ?? []).filter((x) => x.missing);
  // 计价覆盖与未定价名单都基于「真有 token 用量」的模型（与后端 unpricedModels 同口径）
  const billedModels = useMemo(() => (s?.models ?? []).filter((m) => m.totalTokens > 0), [s]);
  const unpricedList = useMemo(() => billedModels.filter((m) => !m.priced).map((m) => m.model), [billedModels]);
  const pricedCount = billedModels.length - unpricedList.length;

  return (
    <section className="page">
      <div className="page-head">
        <div>
          <h1>{t("客户端消耗")}</h1>
          <p>
            {t("WorkBuddy 客户端自身消耗（本机会话日志聚合，不经网关）· 与「Token 消耗」的网关口径并列")}
            <br />
            {t("金额按「设置 → 模型单价」换算")}
          </p>
        </div>
        <div className="head-actions">
          <div className="seg">
            {RANGES.map((r) => (
              <button key={r.days} className={days === r.days ? "on" : ""} onClick={() => setDays(r.days)}>
                {t(r.label)}
              </button>
            ))}
          </div>
          <button className="btn btn-ghost" onClick={() => void stats.reload()}>
            <RefreshCw size={13} strokeWidth={2} /> {t("刷新")}
          </button>
          <button
            className="btn btn-soft"
            disabled={rescanning}
            data-tip={t("重新扫描本机会话日志（跳过 5 分钟缓存）")}
            onClick={() => void rescan()}
          >
            <AppWindow size={13} strokeWidth={2} /> {rescanning ? t("扫描中…") : t("重新扫描")}
          </button>
        </div>
      </div>

      {stats.error && <ErrorBlock message={stats.error} onRetry={() => void stats.reload()} />}
      {!stats.settled && !stats.error && <LoadingBlock label={t("扫描会话日志")} />}

      {stats.settled && s && (
        <>
          {missing.length > 0 && (
            <div className="card" style={{ marginBottom: 14 }}>
              <div className="card-b" style={{ padding: "10px 14px" }}>
                <span className="muted">
                  {t("未安装以下版本的客户端（数据根不存在）：{a}", { a: missing.map((m) => m.source).join("、") })}
                </span>
              </div>
            </div>
          )}

          {unpricedList.length > 0 && (
            <div className="card" style={{ marginBottom: 14 }}>
              <div className="card-b" style={{ padding: "10px 14px" }}>
                <span className="muted">
                  {t("以下模型未配置单价，其用量未计入金额：{a}。到「设置 → 提示词与模型 → 模型单价」填写后金额才会完整。", { a: unpricedList.join("、") })}
                </span>
              </div>
            </div>
          )}

          <div className="kpis7">
            {[
              { k: t("合计 Token"), v: fmtTok(s.summary.totalTokens), s: t("{a} 条 AI 调用记录", { a: s.summary.records }) },
              { k: t("输入（含缓存）"), v: fmtTok(s.summary.inputTokens), s: t("未命中 {a}", { a: fmtTok(Math.max(0, s.summary.inputTokens - s.summary.cacheRead)) }) },
              { k: t("输出"), v: fmtTok(s.summary.outputTokens), s: t("含思考与正文") },
              { k: t("缓存读"), v: fmtTok(s.summary.cacheRead), s: t("上游前缀缓存命中") },
              { k: t("缓存写"), v: fmtTok(s.summary.cacheWrite), s: t("新写入前缀缓存") },
              { k: t("缓存命中率"), v: s.summary.cacheHitRate.toFixed(1), u: "%", s: t("缓存读 / 输入") },
              { k: t("会话数"), v: String(s.sessions?.length ?? 0), s: t("按会话文件聚合") },
            ].map((x) => (
              <div className="card tok-kpi" key={x.k}>
                <div className="k">{x.k}</div>
                <div className="v">{x.v}<small>{x.u ?? ""}</small></div>
                <div className="s">{x.s}</div>
              </div>
            ))}
          </div>

          {/* 金额口径：按「设置 → 模型单价」把 token 换算成人民币，未定价模型一律不计入 */}
          <div className="kpis3">
            {[
              { k: t("合计金额"), v: fmtMoney(s.summary.cost), u: "", s: t("按模型单价表换算 · 元 / 百万 token") },
              { k: t("日均金额"), v: fmtMoney(s.summary.avgCostPerDay), u: "", s: t("按有调用的自然日折算") },
              { k: t("计价覆盖"), v: String(pricedCount), u: `/${billedModels.length}`, s: unpricedList.length > 0 ? t("{a} 个模型未定价，未计入金额", { a: unpricedList.length }) : t("窗口内模型均已定价") },
            ].map((x) => (
              <div className="card tok-kpi" key={x.k}>
                <div className="k">{x.k}</div>
                <div className="v">{x.v}<small>{x.u}</small></div>
                <div className="s">{x.s}</div>
              </div>
            ))}
          </div>

          {s.summary.records === 0 ? (
            <div className="card" style={{ marginTop: 14 }}>
              <div className="card-b">
                <EmptyBlock
                  title={t("窗口内没有客户端消耗记录")}
                  desc={t("使用 WorkBuddy 客户端进行 AI 对话后，这里会显示从本机会话日志聚合的真实 token 用量。")}
                />
              </div>
            </div>
          ) : (
            <>
              <div className="dash-grid" style={{ marginTop: 14, gridTemplateColumns: "1.45fr 1fr" }}>
                <div className="card">
                  <div className="card-h">
                    <div>
                      <h3>{t("日消耗构成")}</h3>
                      <div className="sub">{t("未命中输入 + 缓存读 + 输出 + 缓存写（缓存读计入输入）")}</div>
                    </div>
                    <div className="legend heat-ramp">
                      {["未命中输入", "缓存读", "输出", "缓存写"].map((c, i) => (
                        <span key={c} style={{ display: "inline-flex", alignItems: "center", gap: 4, margin: "0 6px" }}>
                          <i style={{ background: SEG_COLORS[i], width: 8, height: 8, borderRadius: 2 }} />
                          {t(c)}
                        </span>
                      ))}
                    </div>
                  </div>
                  <div className="card-b">
                    <svg width="100%" height="160" viewBox="0 0 560 160" preserveAspectRatio="none" style={{ display: "block" }}>
                      <g stroke="var(--outline)" strokeDasharray="3 5">
                        {[130, 90, 50].map((y) => <line key={y} x1="0" y1={y} x2="560" y2={y} />)}
                      </g>
                      {daily.rows.map((d, i, arr) => {
                        const colW = Math.max(6, Math.min(36, 520 / Math.max(1, arr.length)) - 6);
                        const x = 14 + (i * (532 - colW)) / Math.max(1, arr.length);
                        const scale = (v: number) => (v / daily.max) * 118;
                        const segs = [
                          { v: d.uncached, c: SEG_COLORS[0] },
                          { v: d.cacheRead, c: SEG_COLORS[1] },
                          { v: d.outputTokens, c: SEG_COLORS[2] },
                          { v: d.cacheWrite, c: SEG_COLORS[3] },
                        ];
                        let y = 148;
                        return (
                          <g key={d.date} data-tip={`${d.date} · ${fmtTok(d.totalTokens)} token · ${fmtMoney(d.cost)} · ${d.records} 次`}>
                            {segs.map((seg, si) => {
                              if (seg.v <= 0) return null;
                              const h = Math.max(1, scale(seg.v));
                              y -= h;
                              return <rect key={si} x={x} y={y} width={colW} height={h} rx="2" fill={seg.c} opacity=".92" />;
                            })}
                          </g>
                        );
                      })}
                    </svg>
                    <div className="axis-lab">
                      <span>{daily.rows[0]?.date ?? ""}</span>
                      <span>{daily.rows[Math.floor((daily.rows.length ?? 1) / 2)]?.date ?? ""}</span>
                      <span>{t("今天")}</span>
                    </div>
                  </div>
                </div>

                <div className="card">
                  <div className="card-h">
                    <div>
                      <h3>{t("模型消耗 Top")}</h3>
                      <div className="sub">{t("按窗口内总 token 降序")}</div>
                    </div>
                  </div>
                  <div className="card-b" style={{ paddingTop: 10 }}>
                    {topModels.length === 0 && <EmptyBlock title={t("暂无模型用量")} />}
                    {topModels.map((m) => {
                      const share = s.summary.totalTokens ? m.totalTokens / s.summary.totalTokens : 0;
                      return (
                        <div className="ws-row" key={m.model}>
                          <span className="nm" style={{ width: 170, overflow: "hidden", textOverflow: "ellipsis" }} data-tip={m.model}>
                            <i className="d" style={{ background: m.color, width: 8, height: 8, borderRadius: 3, display: "inline-block", marginRight: 6 }} />
                            {m.model}
                          </span>
                          <span className="meta">{t("{a} 次", { a: m.records })}</span>
                          <span className="num">{fmtTok(m.totalTokens)}<small>{m.priced ? fmtMoney(m.cost) : t("未定价")}</small></span>
                          <span className="bar"><i style={{ width: `${Math.min(100, share * 100)}%`, background: m.color }} /></span>
                        </div>
                      );
                    })}
                  </div>
                </div>
              </div>

              <div className="card" style={{ marginTop: 14 }}>
                <div className="card-h">
                  <div>
                    <h3>{t("日活分布")}</h3>
                    <div className="sub">{t("20 分钟粒度 · 颜色越深活跃会话越多（悬停查看）")}</div>
                  </div>
                  <div className="legend heat-ramp">
                    {t("少")}&nbsp;{ramp.map((c) => <i key={c} style={{ background: c }} />)}&nbsp;{t("多")}
                  </div>
                </div>
                <div className="card-b">
                  <div className="heat-wrap">
                    <div className="heat-head">
                      {Array.from({ length: 72 }, (_, k) => (
                        <span key={k}>{k % 18 === 0 ? String(Math.floor(k / 3)).padStart(2, "0") + ":00" : ""}</span>
                      ))}
                    </div>
                    {heat.daysArr.map((day, d) => {
                      const row = heat.matrix[d] ?? [];
                      return (
                        <div className="heat-row" key={day}>
                          <span className="heat-lab">{day}</span>
                          {row.map((v, k) => {
                            const hh = String(Math.floor(k / 3)).padStart(2, "0");
                            const mm = String((k % 3) * 20).padStart(2, "0");
                            return (
                              <div
                                key={k}
                                className="heat-cell"
                                style={{ background: ramp[lvl(v ?? 0)] }}
                                data-tip={t("{a} {b}:{c} · {d} 会话活跃", { a: day, b: hh, c: mm, d: v ?? 0 })}
                              />
                            );
                          })}
                          <span className="heat-tot">{heat.dayActive.get(day) ?? 0}</span>
                        </div>
                      );
                    })}
                  </div>
                </div>
              </div>

              <div className="dash-grid" style={{ marginTop: 14, gridTemplateColumns: "1fr 1.45fr" }}>
                <div className="card">
                  <div className="card-h">
                    <div>
                      <h3>{t("项目消耗 Top")}</h3>
                      <div className="sub">{t("按会话所属工作空间聚合")}</div>
                    </div>
                  </div>
                  <div className="card-b" style={{ paddingTop: 10 }}>
                    {topProjects.length === 0 && <EmptyBlock title={t("暂无项目用量")} />}
                    {topProjects.map((p) => {
                      const share = s.summary.totalTokens ? p.totalTokens / s.summary.totalTokens : 0;
                      return (
                        <div className="ws-row" key={p.project}>
                          <span className="nm" style={{ width: 170, overflow: "hidden", textOverflow: "ellipsis" }} data-tip={p.project}>
                            {p.project}
                          </span>
                          <span className="meta">{t("{a} 次", { a: p.records })}</span>
                          <span className="num">{fmtTok(p.totalTokens)}<small>{fmtMoney(p.cost)}</small></span>
                          <span className="bar"><i style={{ width: `${Math.min(100, share * 100)}%`, background: MODEL_COLORS[2] }} /></span>
                        </div>
                      );
                    })}
                  </div>
                </div>

                <div className="card">
                  <div className="card-h">
                    <div>
                      <h3>{t("会话消耗排行")}</h3>
                      <div className="sub">{t("Top 30 · 标题来自客户端会话（只读元信息，不含正文）")}</div>
                    </div>
                  </div>
                  <div className="card-b" style={{ maxHeight: 320, overflow: "auto" }}>
                    <table className="tbl">
                      <thead>
                        <tr>
                          <th>{t("会话")}</th><th>{t("项目")}</th><th>{t("来源")}</th>
                          <th>{t("调用")}</th><th>{t("Token")}</th><th>{t("金额")}</th><th>{t("最后活跃")}</th>
                        </tr>
                      </thead>
                      <tbody>
                        {(s.sessions ?? []).map((sess) => (
                          <tr key={sess.source + "|" + sess.sessionId}>
                            <td className="em" data-tip={sess.sessionId} style={{ maxWidth: 220, overflow: "hidden", textOverflow: "ellipsis" }}>
                              {sess.title || sess.sessionId}
                            </td>
                            <td className="muted">{sess.project}</td>
                            <td><span className="chip">{sess.source === "workbuddy-ai" ? "AI" : "CN"}</span></td>
                            <td className="num">{sess.records}</td>
                            <td className="num">{fmtTok(sess.totalTokens)}</td>
                            <td className="num" style={{ color: "var(--text-2)" }}>{fmtMoney(sess.cost)}</td>
                            <td className="muted">{sess.lastTs > 0 ? new Date(sess.lastTs * 1000).toLocaleString() : "—"}</td>
                          </tr>
                        ))}
                        {(s.sessions ?? []).length === 0 && (
                          <tr><td colSpan={7}><EmptyBlock title={t("暂无会话记录")} /></td></tr>
                        )}
                      </tbody>
                    </table>
                  </div>
                </div>
              </div>
            </>
          )}
        </>
      )}
    </section>
  );
}

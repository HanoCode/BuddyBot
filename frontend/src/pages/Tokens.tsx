import { Fragment, useEffect, useMemo, useState } from "react";
import { Landmark, RefreshCw, Search } from "lucide-react";
import { statsApi } from "../services/api";
import { useAsync } from "../hooks/useAsync";
import { EVENT, onEvent } from "../services/events";
import { toast } from "../components/common/Feedback";
import { EmptyBlock, ErrorBlock, LoadingBlock } from "../components/common/StateBlock";
import type { Dashboard, OfficialUsageReport, SessionDrilldown, RequestLogCost } from "../types";
import { useT } from "../i18n";

const HEAT_L = ["#ECEFF4", "#DFE8F4", "#C7DAEF", "#ABC9E5", "#8CB2D8", "#6D97C6", "#4F79A9", "#35597F"];
const HEAT_D = ["#262B36", "#2C3847", "#33455A", "#3B536E", "#446182", "#4F7096", "#5F82AC", "#7BA0C9"];

const MODEL_COLORS = ["#6366F1", "#38BDF8", "#8B5CF6", "#F59E0B", "#10B981", "#EC4899", "#14B8A6"];

const RANGES = [
  { days: 1, label: "1 天" },
  { days: 3, label: "3 天" },
  { days: 7, label: "近 7 天" },
  { days: 30, label: "近 30 天" },
];

const fmtTok = (v: number) =>
  v >= 1e6 ? (v / 1e6).toFixed(2) + "M" : v >= 1e3 ? (v / 1e3).toFixed(1) + "K" : String(v);

// 金额（元）：≥1 元保留 2 位；小额保留 4 位，避免零星用量被抹成 ¥0.00
const fmtMoney = (v: number) => "¥" + (v >= 1 ? v.toFixed(2) : v.toFixed(4));

export default function Tokens() {
  const t = useT();
  const [days, setDays] = useState(7);
  const [renderKey, setRenderKey] = useState(0);

  // 主题变化时重绘色阶
  useEffect(() => {
    const obs = new MutationObserver(() => setRenderKey((k) => k + 1));
    obs.observe(document.documentElement, { attributes: true, attributeFilter: ["data-theme"] });
    return () => obs.disconnect();
  }, []);

  const dash = useAsync<Dashboard>(() => statsApi.dashboard(days), [days]);

  // 会话下钻：缓存命中率 KPI + 会话列表（keyName → session → 请求）
  const drill = useAsync<SessionDrilldown | null>(() => statsApi.sessionDrilldown(days), [days]);
  const [sessQuery, setSessQuery] = useState("");
  const [openSession, setOpenSession] = useState<string | null>(null);
  const [sessionRows, setSessionRows] = useState<RequestLogCost[]>([]);
  const [sessionLoading, setSessionLoading] = useState(false);
  const allSessions = drill.data?.sessions ?? [];
  const sessions = useMemo(() => {
    const q = sessQuery.trim().toLowerCase();
    if (!q) return allSessions;
    return allSessions.filter(
      (s) =>
        s.sessionId.toLowerCase().includes(q) ||
        (s.keyName || "").toLowerCase().includes(q) ||
        (s.title || "").toLowerCase().includes(q),
    );
  }, [allSessions, sessQuery]);
  const toggleSession = async (sid: string) => {
    if (openSession === sid) {
      setOpenSession(null);
      return;
    }
    setOpenSession(sid);
    setSessionLoading(true);
    try {
      setSessionRows((await statsApi.sessionRequests(sid, days)) ?? []);
    } catch (e) {
      toast.error(t("会话明细拉取失败"), String(e));
      setOpenSession(null);
    } finally {
      setSessionLoading(false);
    }
  };

  // 官方口径用量对账（本地统计与官方计费的并排视图）；30 分钟缓存，按钮强制刷新
  const [official, setOfficial] = useState<OfficialUsageReport | null>(null);
  const [officialLoading, setOfficialLoading] = useState(false);
  const loadOfficial = async (force: boolean) => {
    setOfficialLoading(true);
    try {
      const rep = await statsApi.officialUsage(days, force);
      setOfficial(rep);
      if (rep?.source === "unavailable") toast.warn(t("官方用量不可用"), t("所有账号拉取失败，请以本地统计为准"));
    } catch (e) {
      toast.error(t("官方用量拉取失败"), String(e));
    } finally {
      setOfficialLoading(false);
    }
  };

  useEffect(() => {
    const offTask = onEvent(EVENT.taskCompleted, () => { void dash.reload(); void drill.reload(); });
    const offLog = onEvent(EVENT.gatewayLog, () => { void dash.reload(); void drill.reload(); });
    return () => {
      offTask();
      offLog();
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [dash.reload]);

  const ramp = typeof document !== "undefined" && document.documentElement.dataset.theme === "dark" ? HEAT_D : HEAT_L;
  const ov = dash.data?.overview;
  // 计价覆盖与未定价名单都基于「真有 token 用量」的模型：0 token 的失败请求（model 为空）
  // 不构成计价缺口，混进来会让名单出现幻影条目、也让 cover 分母失真
  const billedModels = useMemo(() => (dash.data?.models ?? []).filter((m) => m.tokens > 0), [dash.data]);
  const unpricedList = useMemo(() => billedModels.filter((m) => !m.priced).map((m) => m.model), [billedModels]);
  const pricedCount = billedModels.length - unpricedList.length;

  const heatMax = useMemo(() => {
    let max = 0;
    for (const row of dash.data?.heatmap ?? []) for (const v of row ?? []) max = Math.max(max, v);
    return max;
  }, [dash.data]);

  const lvl = (v: number) => (v <= 0 || heatMax <= 0 ? 0 : Math.min(7, 1 + Math.floor(Math.sqrt(v / heatMax) * 7)));

  const topModels = useMemo(
    () => (dash.data?.models ?? []).slice(0, 6).map((m, i) => ({ ...m, color: MODEL_COLORS[i % MODEL_COLORS.length] })),
    [dash.data],
  );

  const exportJson = () => {
    if (!dash.data) return;
    const blob = new Blob([JSON.stringify(dash.data, null, 2)], { type: "application/json" });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = `workbuddy-token-stats-${days}d.json`;
    a.click();
    URL.revokeObjectURL(url);
    toast.success(t("统计已导出"), t("{a} 天聚合数据 JSON", { a: days }));
  };

  return (
    <section className="page" key={renderKey}>
      <div className="page-head">
        <div>
          <h1>{t("Token 消耗")}</h1>
          <p>
            {t("全部为网关记录的真实 usage 聚合 · 输入 / 输出 / 缓存按请求拆分裂算")}
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
          <button className="btn btn-ghost" onClick={() => void dash.reload()}>
            <RefreshCw size={13} strokeWidth={2} /> {t("刷新")}
          </button>
          <button className="btn btn-soft" disabled={!dash.data} onClick={exportJson}>{t("导出")}</button>
          <button
            className="btn btn-soft"
            disabled={officialLoading}
            data-tip={t("拉取官方计费口径的请求用量，与本地统计对账")}
            onClick={() => void loadOfficial(!official)}
          >
            <Landmark size={13} strokeWidth={2} /> {officialLoading ? t("对账中…") : t("官方用量对账")}
          </button>
        </div>
      </div>

      {official && (
        <div className="card" style={{ marginBottom: 14 }}>
          <div className="card-h">
            <div>
              <h3>{t("官方口径用量（对账）")}</h3>
              <div className="sub">
                {t("来源：官方 get-user-request-usage · 拉取于 {a}", { a: official.fetchedAt })}
                {official.source === "partial" && ` · ${t("部分账号拉取失败，见明细")}`}
                {official.source === "unavailable" && ` · ${t("全部账号拉取失败：请以本地统计为准")}`}
              </div>
            </div>
            <button className="btn btn-ghost sm" disabled={officialLoading} onClick={() => void loadOfficial(true)}>
              <RefreshCw size={12} className={officialLoading ? "spin" : ""} strokeWidth={2} /> {t("重新拉取")}
            </button>
          </div>
          <div className="card-b" style={{ maxHeight: 260, overflow: "auto" }}>
            <table className="tbl">
              <thead>
                <tr>
                  <th>{t("账号")}</th><th>{t("区域")}</th><th>{t("今日消耗")}</th>
                  <th>{t("{a} 天合计", { a: official.days })}</th><th>{t("请求数")}</th><th>{t("说明")}</th>
                </tr>
              </thead>
              <tbody>
                {(official.accounts ?? []).map((a) => (
                  <tr key={a.uid}>
                    <td className="em">{a.nickname || a.uid}</td>
                    <td><span className="chip">{(a.realm || "—").toUpperCase()}</span></td>
                    <td className="num">{a.error ? "—" : a.today.toFixed(2)}</td>
                    <td className="num">{a.error ? "—" : a.total.toFixed(2)}</td>
                    <td className="num">{a.error ? "—" : a.requests}</td>
                    <td className="muted">{a.error ?? t("近 {a} 天 · 按模型 Top3：{b}", { a: official.days, b: (a.models ?? []).slice(0, 3).map((m) => `${m.model}(${m.credits.toFixed(1)})`).join("、") || "—" })}</td>
                  </tr>
                ))}
                {(official.accounts ?? []).length === 0 && (
                  <tr><td colSpan={6}><EmptyBlock title={t("账号池为空，无可对账账号")} /></td></tr>
                )}
              </tbody>
            </table>
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

      {dash.error && <ErrorBlock message={dash.error} onRetry={() => void dash.reload()} />}
      {!dash.settled && !dash.error && <LoadingBlock label={t("汇总统计")} />}

      {dash.settled && (
        <>
          <div className="kpis7">
            {[
              { k: t("合计 Token"), v: fmtTok(ov?.tokens ?? 0), u: "", s: t("输入 {a} · 输出 {b} · 缓存 {c}", { a: fmtTok(ov?.inputTokens ?? 0), b: fmtTok(ov?.outputTokens ?? 0), c: fmtTok(ov?.cacheTokens ?? 0) }) },
              { k: t("日均"), v: fmtTok(Math.round(ov?.avgPerDay ?? 0)), u: "", s: ov?.peakDate ? t("峰值 {a} · {b}", { a: fmtTok(ov.peakTokens), b: ov.peakDate }) : t("窗口内无请求") },
              { k: t("今日"), v: fmtTok(ov?.todayTokens ?? 0), u: "", s: t("今日 {a} 次请求", { a: ov?.todayRequests ?? 0 }) },
              { k: t("请求数"), v: String(ov?.requests ?? 0), u: "", s: t("活跃 {a} 天", { a: ov?.days ?? 0 }) },
              { k: t("会话"), v: String(ov?.sessions ?? 0), u: "", s: t("按请求携带的 session_id 去重") },
              { k: t("成功率"), v: (ov?.successRate ?? 0).toFixed(1), u: "%", s: t("失败 {a} 次", { a: ov?.errors ?? 0 }) },
              { k: t("平均延迟"), v: (ov?.avgLatency ?? 0).toFixed(0), u: "ms", s: t("P90 {a}ms · P99 {b}ms", { a: (ov?.p90 ?? 0).toFixed(0), b: (ov?.p99 ?? 0).toFixed(0) }) },
            ].map((x) => (
              <div className="card tok-kpi" key={x.k}>
                <div className="k">{x.k}</div>
                <div className="v">{x.v}<small>{x.u}</small></div>
                <div className="s">{x.s}</div>
              </div>
            ))}
          </div>

          {/* 金额口径：按「设置 → 模型单价」把 token 换算成人民币，未定价模型一律不计入 */}
          <div className="kpis3">
            {[
              { k: t("合计金额"), v: fmtMoney(ov?.cost ?? 0), u: "", s: t("按模型单价表换算 · 元 / 百万 token") },
              { k: t("日均金额"), v: fmtMoney(ov?.avgCostPerDay ?? 0), u: "", s: t("按活跃 {a} 天折算", { a: ov?.days ?? 0 }) },
              { k: t("计价覆盖"), v: String(pricedCount), u: `/${billedModels.length}`, s: unpricedList.length > 0 ? t("{a} 个模型未定价，未计入金额", { a: unpricedList.length }) : t("窗口内模型均已定价") },
            ].map((x) => (
              <div className="card tok-kpi" key={x.k}>
                <div className="k">{x.k}</div>
                <div className="v">{x.v}<small>{x.u}</small></div>
                <div className="s">{x.s}</div>
              </div>
            ))}
          </div>

          {(ov?.requests ?? 0) === 0 ? (
            <div className="card" style={{ marginTop: 14 }}>
              <div className="card-b">
                <EmptyBlock
                  title={t("窗口内没有请求记录")}
                  desc={t("启动网关并调用 /v1/chat/completions（或使用「聊天测试」页）后，这里会显示真实的 token 与延迟分布。")}
                />
              </div>
            </div>
          ) : (
            <>
              <div className="dash-grid" style={{ gridTemplateColumns: "1fr 1.45fr" }}>
                <div className="card">
                  <div className="card-h">
                    <div>
                      <h3>{t("用量分布")}</h3>
                      <div className="sub">{t("按调用密钥聚合（密钥是唯一真实的归属维度）")}</div>
                    </div>
                  </div>
                  <div className="card-b" style={{ paddingTop: 10 }}>
                    {(dash.data?.keys ?? []).length === 0 && <EmptyBlock title={t("暂无密钥用量")} />}
                    {(dash.data?.keys ?? []).map((k, i) => {
                      const share = ov?.tokens ? k.tokens / ov.tokens : 0;
                      return (
                        <div className="ws-row" key={k.keyId}>
                          <span className="nm" style={{ width: 148, overflow: "hidden", textOverflow: "ellipsis" }} data-tip={k.keyName || k.keyId}>
                            {k.keyName || k.keyId}
                          </span>
                          <span className="meta">{t("{a} 次 · 均 {b}ms", { a: k.requests, b: k.avgLatency.toFixed(0) })}</span>
                          <span className="cells">
                            <i style={{ width: 18, background: k.errors > 0 ? "var(--red)" : "var(--green)" }} data-tip={t("失败 {a} 次", { a: k.errors })} />
                            <i style={{ width: 18, background: ramp[lvl(k.tokens)] }} data-tip={t("{a} token", { a: fmtTok(k.tokens) })} />
                          </span>
                          <span className="num">{fmtTok(k.tokens)}<small>{fmtMoney(k.cost)}</small></span>
                          <span className="bar"><i style={{ width: `${Math.min(100, share * 100)}%`, background: MODEL_COLORS[i % MODEL_COLORS.length] }} /></span>
                        </div>
                      );
                    })}
                    <div className="mcards" style={{ marginTop: 12 }}>
                      {topModels.map((m) => (
                        <div className="mcard" key={m.model} data-tip={m.model}>
                          <span className="n">
                            <i className="d" style={{ background: m.color }} />
                            <span>{m.model}</span>
                          </span>
                          <span className="t">
                            {t("{a} · {b} 次", { a: fmtTok(m.tokens), b: m.requests })}
                            {m.errors > 0 ? t(" · 失败 {a}", { a: m.errors }) : ""}
                            {m.priced ? ` · ${fmtMoney(m.cost)}` : ` · ${t("未定价")}`}
                          </span>
                        </div>
                      ))}
                    </div>
                  </div>
                </div>

                <div className="card">
                  <div className="card-h">
                    <div>
                      <h3>{t("日活分布")}</h3>
                      <div className="sub">{t("20 分钟粒度 · 颜色越深 token 越多（悬停查看）")}</div>
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
                      {(dash.data?.heatmapDays ?? []).map((day, d) => {
                        const row = (dash.data?.heatmap ?? [])[d] ?? [];
                        const total = row.reduce((s, v) => s + (v ?? 0), 0);
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
                                  style={{ background: ramp[lvl(v)] }}
                                  data-tip={t("{a} {b}:{c} · {d} token", { a: day, b: hh, c: mm, d: fmtTok(v) })}
                                />
                              );
                            })}
                            <span className="heat-tot">{fmtTok(total)}</span>
                          </div>
                        );
                      })}
                    </div>
                  </div>
                </div>
              </div>

              <div className="dash-grid" style={{ marginTop: 14, gridTemplateColumns: "1.25fr 1fr" }}>
                <div className="card">
                  <div className="card-h">
                    <div>
                      <h3>{t("0–24 时分布 · 按模型")}</h3>
                      <div className="sub">{t("各模型按时段的真实 token 量（纵轴按各自峰值归一）")}</div>
                    </div>
                  </div>
                  <div className="card-b">
                    {topModels.length === 0 ? (
                      <EmptyBlock title={t("暂无模型用量")} />
                    ) : (
                      <>
                        <svg width="100%" height="140" viewBox="0 0 560 140" preserveAspectRatio="none" style={{ display: "block" }}>
                          <g stroke="var(--outline)" strokeDasharray="3 5">
                            {[120, 80, 40].map((y) => <line key={y} x1="0" y1={y} x2="560" y2={y} />)}
                          </g>
                          {topModels.map((m) => {
                            const series = (dash.data?.hourly ?? []).map((h) => h.byModel?.[m.model] ?? 0);
                            const max = Math.max(1, ...series);
                            const pts = series
                              .map((v, i) => `${(i * 560) / 23},${128 - (v / max) * 112}`)
                              .join(" ");
                            return (
                              <polyline
                                key={m.model}
                                points={pts}
                                fill="none"
                                stroke={m.color}
                                strokeWidth="2"
                                strokeLinecap="round"
                                strokeLinejoin="round"
                              />
                            );
                          })}
                        </svg>
                        <div className="axis-lab">
                          <span>00:00</span><span>04:00</span><span>08:00</span><span>12:00</span><span>16:00</span><span>20:00</span><span>23:00</span>
                        </div>
                        <div className="mcards">
                          {topModels.map((m) => (
                            <div className="mcard" key={m.model} data-tip={m.model}>
                              <span className="n">
                                <i className="d" style={{ background: m.color }} />
                                <span>{m.model}</span>
                              </span>
                              <span className="t">{fmtTok(m.tokens)}</span>
                            </div>
                          ))}
                        </div>
                      </>
                    )}
                  </div>
                </div>

                <div className="card">
                  <div className="card-h">
                    <div>
                      <h3>{t("每日模型用量比例")}</h3>
                      <div className="sub">{t("x=日期（最新在右）· y=当日 Token · 颜色=模型")}</div>
                    </div>
                  </div>
                  <div className="card-b">
                    {topModels.length === 0 ? (
                      <EmptyBlock title={t("暂无模型用量")} />
                    ) : (
                      <>
                        <svg width="100%" height="130" viewBox="0 0 460 130" preserveAspectRatio="none" style={{ display: "block" }}>
                          <g stroke="var(--outline)" strokeDasharray="3 5"><line x1="0" y1="110" x2="460" y2="110" /></g>
                          {(dash.data?.dailyModels ?? []).map((d, i, arr) => {
                            const colW = Math.max(4, Math.min(24, 440 / Math.max(1, arr.length)) - 4);
                            const x = 12 + (i * (440 - colW)) / Math.max(1, arr.length - 1);
                            let cursor = 110;
                            const dayTotal = Object.values(d.byModel ?? {}).reduce<number>((s, v) => s + (v ?? 0), 0);
                            const globalMax = Math.max(1, ...arr.map((x) => Object.values(x.byModel ?? {}).reduce<number>((s, v) => s + (v ?? 0), 0)));
                            return (
                              <g key={d.date}>
                                {topModels.map((m) => {
                                  const v = d.byModel?.[m.model] ?? 0;
                                  if (v <= 0) return null;
                                  const h = Math.max(1, (v / globalMax) * 96);
                                  cursor -= h;
                                  return <rect key={m.model} x={x} y={cursor} width={colW} height={h} rx="2" fill={m.color} opacity=".9" />;
                                })}
                                {dayTotal === 0 && (
                                  <rect x={x} y={109} width={colW} height={1} rx="1" fill="var(--outline-2)" />
                                )}
                              </g>
                            );
                          })}
                        </svg>
                        <div className="axis-lab">
                          <span>{dash.data?.dailyModels?.[0]?.date ?? ""}</span>
                          <span>{dash.data?.dailyModels?.[Math.floor((dash.data?.dailyModels.length ?? 1) / 2)]?.date ?? ""}</span>
                          <span>{t("今天")}</span>
                        </div>
                        <div style={{ marginTop: 14, paddingTop: 12, borderTop: "1px dashed var(--outline)" }}>
                          {[
                            [t("窗口内请求总数"), t("{a} 次", { a: ov?.requests ?? 0 })],
                            [t("首字节 / 总延迟"), `${(ov?.firstLatency ?? 0).toFixed(0)}ms / ${(ov?.avgLatency ?? 0).toFixed(0)}ms`],
                            [t("最大单次请求"), topModels.length ? `${fmtTok(Math.max(...topModels.map((m) => m.tokenP90)))} (P90)` : "—"],
                          ].map(([k, v]) => (
                            <div className="flex" key={k} style={{ justifyContent: "space-between", fontSize: 12, color: "var(--text-2)", marginTop: 8 }}>
                              <span>{k}</span>
                              <b style={{ color: "var(--text-1)", fontSize: 12.5 }}>{v}</b>
                            </div>
                          ))}
                        </div>
                      </>
                    )}
                  </div>
                </div>
              </div>

              <div className="card" style={{ marginTop: 14 }}>
                <div className="card-h">
                  <div>
                    <h3>{t("单次请求 Token 分布")}</h3>
                    <div className="sub">{t("各模型 P50 / P90 单次请求 token 量（上限按窗口内最大 P90 归一）")}</div>
                  </div>
                  <span className="muted">{t("深色 = P50 · 浅色 = P90")}</span>
                </div>
                <div className="card-b" style={{ paddingTop: 8 }}>
                  {topModels.length === 0 && <EmptyBlock title={t("暂无模型用量")} />}
                  {topModels.map((m) => {
                    const max = Math.max(1, ...topModels.map((x) => x.tokenP90));
                    return (
                      <div className="pct-row" key={m.model}>
                        <div className="n">
                          <span className="d" style={{ width: 8, height: 8, borderRadius: 3, background: m.color, display: "inline-block" }} />
                          {m.model}
                        </div>
                        <div className="bars">
                          <div className="pb"><i style={{ width: `${(m.tokenP50 / max) * 100}%`, background: m.color }} /></div>
                          <div className="pb"><i style={{ width: `${(m.tokenP90 / max) * 100}%`, background: m.color, opacity: 0.4 }} /></div>
                        </div>
                        <div className="vals">{t("P50 {a} · P90 {b} · 均 {c}", { a: fmtTok(m.tokenP50), b: fmtTok(m.tokenP90), c: fmtTok(Math.round(m.avgTokens)) })}</div>
                      </div>
                    );
                  })}
                </div>
              </div>
              <div className="card" style={{ marginTop: 14 }}>
                <div className="card-h">
                  <div>
                    <h3>{t("会话下钻")}</h3>
                    <div className="sub">
                      {t("密钥（调用方）→ 会话 → 请求 · 点击会话行展开请求明细")}
                      {` · ${t("标题来自本机客户端会话（查无标题时显示 session_id）")}`}
                      {drill.data && ` · ${t("合计 {a}", { a: fmtMoney(drill.data.cost) })}${unpricedList.length > 0 ? t("（{a} 个模型未定价，未计入）", { a: unpricedList.length }) : ""}`}
                      {drill.data && drill.data.inputTokens > 0 &&
                        ` · ${t("缓存命中率 {a}%（{b} / {c}）", { a: drill.data.cacheHitRate.toFixed(1), b: fmtTok(drill.data.cacheTokens), c: fmtTok(drill.data.inputTokens) })}`}
                    </div>
                  </div>
                  <div className="head-actions">
                    <div className="search-box" style={{ width: 200, flex: "none", maxWidth: "none" }}>
                      <Search size={13} strokeWidth={2.2} />
                      <input
                        className="search-input"
                        placeholder={t("搜索会话 / 标题 / 密钥")}
                        value={sessQuery}
                        onChange={(e) => setSessQuery(e.target.value)}
                      />
                    </div>
                    <span className="muted">{t("{a} 个会话", { a: sessions.length })}</span>
                  </div>
                </div>
                <div className="card-b" style={{ maxHeight: 320, overflow: "auto" }}>
                  {drill.error && <ErrorBlock message={drill.error} onRetry={() => void drill.reload()} />}
                  {!drill.settled && !drill.error && <LoadingBlock label={t("会话聚合")} />}
                  {drill.settled && !drill.error && (
                    <table className="tbl">
                      <thead>
                        <tr>
                          <th>{t("会话")}</th><th>{t("密钥")}</th><th>{t("请求数")}</th>
                          <th>{t("Token")}</th><th>{t("金额")}</th><th>{t("输入 / 输出 / 缓存")}</th><th>{t("失败")}</th><th>{t("最后活跃")}</th>
                        </tr>
                      </thead>
                      <tbody>
                        {sessions.map((sess) => (
                          <Fragment key={sess.sessionId}>
                            <tr
                              key={sess.sessionId}
                              className={openSession === sess.sessionId ? "row-expiring" : ""}
                              style={{ cursor: "pointer" }}
                              onClick={() => void toggleSession(sess.sessionId)}
                            >
                              <td className="em" data-tip={sess.title ? `${sess.title}\n${sess.sessionId}` : sess.sessionId} style={{ maxWidth: 180, overflow: "hidden", textOverflow: "ellipsis" }}>
                                {openSession === sess.sessionId ? "▾ " : "▸ "}{sess.title || sess.sessionId}
                              </td>
                              <td>{sess.keyName || "—"}</td>
                              <td className="num">{sess.requests}</td>
                              <td className="num">{fmtTok(sess.tokens)}</td>
                              <td className="num" style={{ color: "var(--text-2)" }}>{fmtMoney(sess.cost)}</td>
                              <td className="num muted">{fmtTok(sess.inputTokens)} / {fmtTok(sess.outputTokens)} / {fmtTok(sess.cacheTokens)}</td>
                              <td className="num">{sess.errors > 0 ? <span style={{ color: "var(--red)" }}>{sess.errors}</span> : "0"}</td>
                              <td className="muted">{sess.lastTs > 0 ? new Date(sess.lastTs * 1000).toLocaleString() : "—"}</td>
                            </tr>
                            {openSession === sess.sessionId && (
                              <tr key={sess.sessionId + "-detail"}>
                                <td colSpan={8} style={{ background: "var(--surface-2)" }}>
                                  {sessionLoading && <span className="muted">{t("加载明细…")}</span>}
                                  {!sessionLoading && (sessionRows ?? []).length === 0 && <span className="muted">{t("无请求明细")}</span>}
                                  {!sessionLoading && (sessionRows ?? []).length > 0 && (
                                    <table className="tbl">
                                      <thead>
                                        <tr>
                                          <th>{t("时间")}</th><th>{t("模型")}</th><th>{t("状态")}</th>
                                          <th>{t("Token")}</th><th>{t("金额")}</th><th>{t("缓存")}</th><th>{t("延迟")}</th>
                                        </tr>
                                      </thead>
                                      <tbody>
                                        {sessionRows.map((r) => (
                                          <tr key={r.id}>
                                            <td className="muted">{r.time}</td>
                                            <td>{r.model || "—"}</td>
                                            <td className="num" style={{ color: r.status >= 400 ? "var(--red)" : "var(--green)" }}>{r.status}</td>
                                            <td className="num">{fmtTok(r.tokens)}</td>
                                            <td className="num" style={{ color: "var(--text-2)" }}>{r.priced ? fmtMoney(r.cost) : t("未定价")}</td>
                                            <td className="num">{fmtTok(r.cacheTokens ?? 0)}</td>
                                            <td className="num">{r.latency.toFixed(0)}ms</td>
                                          </tr>
                                        ))}
                                      </tbody>
                                    </table>
                                  )}
                                </td>
                              </tr>
                            )}
                          </Fragment>
                        ))}
                        {sessions.length === 0 && allSessions.length > 0 && (
                          <tr><td colSpan={8}><EmptyBlock title={t("没有匹配的会话")} /></td></tr>
                        )}
                        {allSessions.length === 0 && (
                          <tr><td colSpan={8}><EmptyBlock title={t("窗口内没有携带 session_id 的请求")} /></td></tr>
                        )}
                      </tbody>
                    </table>
                  )}
                </div>
              </div>

            </>
          )}
        </>
      )}
    </section>
  );
}

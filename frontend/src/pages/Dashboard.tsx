import { useCallback, useEffect, useMemo, useState } from "react";
import { Activity, CircleUserRound, Coins, Power, RefreshCw, TimerReset, Zap } from "lucide-react";
import { gatewayApi, logsApi, statsApi, systemApi } from "../services/api";
import { broadcastRefresh, errText, useAsync } from "../hooks/useAsync";
import { EVENT, onEvent } from "../services/events";
import { toast } from "../components/common/Feedback";
import { EmptyBlock, ErrorBlock, LoadingBlock } from "../components/common/StateBlock";
import type { Dashboard as DashboardData, GatewayStatus, SystemInfo, TaskLog } from "../types";
import { useT } from "../i18n";

const RANGES = [
  { days: 7, label: "近 7 天" },
  { days: 30, label: "近 30 天" },
];

export default function Dashboard() {
  const [days, setDays] = useState(7);
  const [busy, setBusy] = useState(false);
  const t = useT();

  const dash = useAsync<DashboardData>(() => statsApi.dashboard(days), [days]);
  const status = useAsync<GatewayStatus>(() => gatewayApi.status(), []);
  const sys = useAsync<SystemInfo>(() => systemApi.info(), []);
  const tasks = useAsync(() => logsApi.taskLogs({ page: 1, pageSize: 8 }), []);

  const load = useCallback(async () => {
    await Promise.all([dash.reload(), status.reload(), sys.reload(), tasks.reload()]);
  }, [dash.reload, status.reload, sys.reload, tasks.reload]);

  // 30s 自动刷新（DESIGN.md Task 2.1）+ 后端事件即时刷新
  useEffect(() => {
    const timer = setInterval(() => void load(), 30000);
    const onRefresh = () => void load();
    window.addEventListener("app:refresh", onRefresh);
    const offTask = onEvent(EVENT.taskCompleted, () => void load());
    return () => {
      clearInterval(timer);
      window.removeEventListener("app:refresh", onRefresh);
      offTask();
    };
  }, [load]);

  const toggleGateway = async () => {
    const running = status.data?.running;
    setBusy(true);
    try {
      if (running) {
        await gatewayApi.stop();
        toast.info(t("网关已停止"), t("客户端请求将全部失败"));
      } else {
        await gatewayApi.start();
        toast.success(t("网关已启动"), status.data?.listen);
      }
      await status.reload();
      broadcastRefresh();
    } catch (e) {
      toast.error(running ? t("停止失败") : t("启动失败"), errText(e));
    } finally {
      setBusy(false);
    }
  };

  const ov = dash.data?.overview;
  const daily = dash.data?.daily ?? [];
  const hasTraffic = (ov?.requests ?? 0) > 0;

  const poolStats = useMemo(
    () => [
      { name: "在线可用", desc: "正常承接请求", num: status.data?.poolOnline ?? 0, cls: "var(--green)", bg: "var(--green-soft)" },
      { name: "冷却中", desc: "熔断 / 限速恢复中", num: status.data?.poolCooldown ?? 0, cls: "var(--amber)", bg: "var(--amber-soft)" },
      { name: "已禁用", desc: "手动停用", num: status.data?.poolDisabled ?? 0, cls: "var(--red)", bg: "var(--red-soft)" },
      { name: "Token 过期", desc: "需要重新授权", num: status.data?.poolExpired ?? 0, cls: "var(--text-2)", bg: "var(--surface-3)" },
      { name: "有效期未知", desc: "凭证未提供 expiresAt", num: status.data?.poolUnknown ?? 0, cls: "var(--text-2)", bg: "var(--surface-3)" },
    ],
    [status.data],
  );

  return (
    <section className="page">
      <div className="page-head">
        <div>
          <h1>{t("仪表盘")}</h1>
          <p>
            {t("账号池与网关运行总览 · 每 30 秒自动刷新")}
            {dash.data && t(" · 统计口径取近 {n} 天真实记录", { n: days })}
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
            <button className="btn btn-ghost" onClick={() => void load()}>
            <RefreshCw size={13} strokeWidth={2} /> {t("刷新")}
          </button>
          <button className="btn btn-primary" disabled={busy || status.data === null} onClick={toggleGateway}>
            <Power size={13} strokeWidth={2} /> {status.data?.running ? t("停止网关") : t("启动网关")}
          </button>
        </div>
      </div>

      {status.error && <ErrorBlock message={status.error} onRetry={() => void status.reload()} />}

      <div className="kpis">
        <div className="card kpi">
          <div className="top">
            <div className="label">
              <span className="ic" style={{ background: "var(--green-soft)", color: "var(--green)" }}>
                <CircleUserRound size={15} strokeWidth={2} />
              </span>
              {t("在线账号")}
            </div>
            <span className={`badge ${status.data?.running ? "b-green" : "b-gray"}`}>
              <span className="d" />
              {status.data?.running ? t("监听 {addr}", { addr: status.data.listen }) : t("网关停止")}
            </span>
          </div>
          <div className="val">
            {ov?.accountsOnline ?? 0} <small>/ {ov?.accountsTotal ?? 0}</small>
          </div>
          <div className="delta up">
            {ov && ov.accountsTotal > 0
              ? t("池内可用率 {p}%", { p: Math.round((ov.accountsOnline / ov.accountsTotal) * 100) })
              : t("凭证目录暂无账号")}
          </div>
        </div>

        <div className="card kpi">
          <div className="top">
            <div className="label">
              <span className="ic" style={{ background: "var(--blue-soft)", color: "var(--blue)" }}>
                <Activity size={15} strokeWidth={2} />
              </span>
              {t("请求量")}
            </div>
            <span className="badge b-gray">{t("累计 ")}{ov?.requests ?? 0}</span>
          </div>
          <div className="val">{ov?.todayRequests ?? 0}<small> {t("今日")}</small></div>
          <div className="delta up">
            {t("活跃 {d} 天 · 日均 {a} 次", { d: ov?.days ?? 0, a: (ov?.avgPerDay ?? 0).toFixed(1) })}
          </div>
        </div>

        <div className="card kpi">
          <div className="top">
            <div className="label">
              <span className="ic" style={{ background: "var(--primary-soft)", color: "var(--primary)" }}>
                <Zap size={15} strokeWidth={2} />
              </span>
              {t("Token 消耗")}
            </div>
            {ov && ov.peakDate && <span className="badge b-indigo">{t("峰值 {d}", { d: ov.peakDate })}</span>}
          </div>
          <div className="val">{(ov?.todayTokens ?? 0).toLocaleString()}<small> {t("今日")}</small></div>
          <div className="delta up">
            {t("窗口内合计 {a}K · 输入 {b}K / 输出 {c}K", {
              a: ((ov?.tokens ?? 0) / 1000).toFixed(1),
              b: ((ov?.inputTokens ?? 0) / 1000).toFixed(1),
              c: ((ov?.outputTokens ?? 0) / 1000).toFixed(1),
            })}
          </div>
        </div>

        <div className="card kpi">
          <div className="top">
            <div className="label">
              <span className="ic" style={{ background: "var(--accent-soft)", color: "var(--accent)" }}>
                <Coins size={15} strokeWidth={2} />
              </span>
              {t("账号积分余额")}
            </div>
          </div>
          {ov && ov.creditsUnknown > 0 && ov.creditsTotal === 0 ? (
            <>
              <div className="val" style={{ color: "var(--text-3)" }}>—</div>
              <div className="delta down">
                {t("{n} 个账号暂无余额来源：本应用未接入上游余额接口", { n: ov.creditsUnknown })}
              </div>
            </>
          ) : (
            <>
              <div className="val">{ov?.creditsTotal.toLocaleString() ?? 0}</div>
              <div className="delta up">
                {t("{a} 个账号中 {b} 个有真实余额", {
                  a: ov?.accountsTotal ?? 0,
                  b: (ov?.accountsTotal ?? 0) - (ov?.creditsUnknown ?? 0),
                })}
              </div>
            </>
          )}
        </div>
      </div>

      <div className="dash-grid">
        <div className="card">
          <div className="card-h">
            <div>
              <h3>{t("调用趋势")}</h3>
              <div className="sub">{t("近 {d} 天真实请求量与 Token 消耗（无数据的日期按 0 显示）", { d: days })}</div>
            </div>
            <div className="legend">
              <span><i style={{ background: "var(--primary)" }} />{t("请求量")}</span>
              <span><i style={{ background: "var(--accent)" }} />Token</span>
            </div>
          </div>
          <div className="card-b">
            {dash.error ? (
              <ErrorBlock message={dash.error} onRetry={() => void dash.reload()} />
            ) : !dash.settled ? (
              <LoadingBlock />
            ) : !hasTraffic ? (
              <EmptyBlock
                title={t("暂无请求记录")}
                desc={t("网关尚未处理过请求。启动网关后用任意客户端（或在「聊天测试」页）发一次请求，这里会出现真实曲线。")}
              />
            ) : (
              <TrendChart points={daily} />
            )}
            <div className="axis-lab">
              {daily.map((p) => (
                <span key={p.date}>{p.date}</span>
              ))}
            </div>
          </div>
        </div>

        <div className="card">
          <div className="card-h">
            <div>
              <h3>{t("账号池状态")}</h3>
              <div className="sub">{t("{n} 个账号 · 来源为凭证目录下的凭证文件", { n: status.data?.total ?? 0 })}</div>
            </div>
          </div>
          <div className="card-b" style={{ display: "flex", flexDirection: "column", gap: 4 }}>
            {poolStats.map((s) => (
              <div className="stat-row" key={s.name}>
                <span className="ic" style={{ background: s.bg, color: s.cls }}>
                  <span className="d" style={{ width: 10, height: 10, borderRadius: 3, background: "currentColor" }} />
                </span>
                <div>
                  <div className="name">{t(s.name)}</div>
                  <div className="desc">{t(s.desc)}</div>
                </div>
                <div className="num" style={{ color: s.cls }}>{s.num}</div>
              </div>
            ))}
            {(status.data?.poolExpired ?? 0) > 0 && (
              <div style={{ marginTop: 12, padding: "11px 13px", borderRadius: 11, background: "var(--amber-soft)", fontSize: 12, color: "var(--amber)", lineHeight: 1.5 }}>
                {t("{n} 个账号 access token 已过期，请刷新凭证或重新授权", { n: status.data?.poolExpired ?? 0 })}
              </div>
            )}
          </div>
        </div>
      </div>

      <div className="dash-grid" style={{ gridTemplateColumns: "1.35fr 1fr" }}>
        <div className="card">
          <div className="card-h">
            <div>
              <h3>{t("最近任务")}</h3>
              <div className="sub">{t("任务流水（跟随 task:completed 事件实时刷新）")}</div>
            </div>
            <a className="btn btn-soft sm" href="#/logs">{t("查看全部")}</a>
          </div>
          <div className="card-b">
            {tasks.error ? (
              <ErrorBlock message={tasks.error} />
            ) : !tasks.settled ? (
              <LoadingBlock />
            ) : (tasks.data?.items?.length ?? 0) === 0 ? (
              <EmptyBlock title={t("暂无任务记录")} desc={t("定时任务或手动触发后会在这里留下真实执行结果。")} />
            ) : (
              (tasks.data?.items ?? []).map((e, i) => <TaskRow key={e.id} log={e} index={i} />)
            )}
          </div>
        </div>

        <div className="card">
          <div className="card-h">
            <div>
              <h3>{t("系统健康")}</h3>
              <div className="sub">
                {status.data?.running ? t("网关已运行 {u}", { u: status.data.uptime }) : t("网关未运行")}
                {sys.data ? t(" · 进程已运行 {p}", { p: sys.data.processUptime }) : ""}
              </div>
            </div>
          </div>
          <div className="card-b">
            {[
              { k: "成功率", v: `${(status.data?.successRate ?? 0).toFixed(1)}%`, pct: status.data?.successRate ?? 0, c: "var(--green)" },
              { k: "错误请求", v: t("{n} 次", { n: status.data?.errors ?? 0 }), pct: status.data?.requests ? ((status.data.errors / status.data.requests) * 100) : 0, c: "var(--red)" },
              { k: "平均延迟", v: `${(status.data?.avgLatency ?? 0).toFixed(0)} ms`, pct: Math.min(100, (status.data?.avgLatency ?? 0) / 30), c: "var(--blue)" },
              { k: "P90 延迟", v: `${(status.data?.p90 ?? 0).toFixed(0)} ms`, pct: Math.min(100, (status.data?.p90 ?? 0) / 50), c: "var(--primary)" },
              { k: "在途请求", v: String(status.data?.inflight ?? 0), pct: Math.min(100, (status.data?.inflight ?? 0) * 10), c: "var(--accent)" },
              { k: "内存占用", v: sys.data ? `${sys.data.memoryAlloc.toFixed(1)} MB` : "—", pct: sys.data ? Math.min(100, (sys.data.memoryAlloc / 256) * 100) : 0, c: "var(--green)" },
            ].map((r) => (
              <div className="field" key={r.k} style={{ marginBottom: 12 }}>
                <div style={{ display: "flex", justifyContent: "space-between", fontSize: 12, color: "var(--text-2)", marginBottom: 6 }}>
                  <span>{t(r.k)}</span>
                  <b style={{ color: "var(--text-1)" }}>{r.v}</b>
                </div>
                <div className="quota">
                  <div className="qb" style={{ height: 7 }}>
                    <i style={{ width: `${Math.max(0, Math.min(100, r.pct))}%`, background: r.c }} />
                  </div>
                </div>
              </div>
            ))}
            <div className="muted" style={{ fontSize: 11.5, display: "flex", alignItems: "center", gap: 6 }}>
              <TimerReset size={12} strokeWidth={2} />
              {t("全部指标为运行时真实采样（内存 / 协程 / 延迟分位）")}
            </div>
          </div>
        </div>
      </div>
    </section>
  );
}

function TaskRow({ log, index }: { log: TaskLog; index: number }) {
  const color = log.status === "success" ? "var(--green)" : log.status === "failed" ? "var(--red)" : "var(--amber)";
  const label = log.type === "checkin" ? "每日签到" : log.type === "travel" ? "猫猫旅行" : "token 保活";
  const state = log.status === "success" ? "成功" : log.status === "failed" ? "失败" : "跳过";
  const t = useT();
  return (
    <div className="tl-item in" style={{ animationDelay: `${index * 40}ms` }}>
      <span className="tl-dot" style={{ background: color }} />
      <div>
        <div className="t">
          {t(label)} · {log.uid} <span className={`badge ${log.status === "success" ? "b-green" : log.status === "failed" ? "b-red" : "b-amber"}`} style={{ marginLeft: 6 }}><span className="d" />{t(state)}</span>
        </div>
        <div className="m">{log.message}</div>
        <time>{log.time} · {log.trigger === "manual" ? t("手动") : t("排程")} · {log.duration.toFixed(0)}ms</time>
      </div>
    </div>
  );
}

/** 双序列折线图：数据全部来自后端聚合，无数据则不绘制 */
function TrendChart({ points }: { points: { date: string; requests: number; tokens: number }[] }) {
  const W = 640;
  const H = 200;
  const pad = 18;
  const maxReq = Math.max(1, ...points.map((p) => p.requests));
  const maxTok = Math.max(1, ...points.map((p) => p.tokens));

  const xy = (i: number, v: number, max: number) => {
    const x = points.length <= 1 ? W / 2 : pad + (i * (W - pad * 2)) / (points.length - 1);
    const y = H - pad - (v / max) * (H - pad * 2);
    return [x, y] as const;
  };

  const line = (key: "requests" | "tokens", max: number) =>
    points.map((p, i) => xy(i, p[key], max)).map(([x, y]) => `${x.toFixed(1)},${y.toFixed(1)}`).join(" ");

  const reqLine = line("requests", maxReq);
  const tokLine = line("tokens", maxTok);

  return (
    <svg width="100%" height="200" viewBox={`0 0 ${W} ${H}`} preserveAspectRatio="none" style={{ display: "block" }}>
      <defs>
        <linearGradient id="gReq" x1="0" y1="0" x2="0" y2="1">
          <stop offset="0" stopColor="#6366F1" stopOpacity=".26" />
          <stop offset="1" stopColor="#6366F1" stopOpacity="0" />
        </linearGradient>
      </defs>
      <g stroke="var(--outline)" strokeDasharray="3 5">
        {[0.25, 0.5, 0.75, 1].map((r) => (
          <line key={r} x1="0" y1={pad + (H - pad * 2) * (1 - r)} x2={W} y2={pad + (H - pad * 2) * (1 - r)} />
        ))}
      </g>
      {points.length > 1 && (
        <>
          <polygon points={`${reqLine} ${xy(points.length - 1, 0, maxReq)[0]},${H - pad} ${xy(0, 0, maxReq)[0]},${H - pad}`} fill="url(#gReq)" />
          <polyline points={reqLine} fill="none" stroke="#6366F1" strokeWidth="2.4" strokeLinecap="round" strokeLinejoin="round" />
          <polyline points={tokLine} fill="none" stroke="#FF6B4A" strokeWidth="2.4" strokeLinecap="round" strokeLinejoin="round" strokeDasharray="0" />
        </>
      )}
      {points.map((p, i) => {
        const [x, y] = xy(i, p.requests, maxReq);
        return <circle key={p.date} cx={x} cy={y} r="3" fill="#6366F1" />;
      })}
    </svg>
  );
}

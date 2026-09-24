import { useCallback, useEffect, useState } from "react";
import { createPortal } from "react-dom";
import { useLocation } from "react-router-dom";
import { ChevronLeft, ChevronRight, Download, Eraser, FolderOpen, Loader2, RefreshCw, Search, X } from "lucide-react";
import { logsApi } from "../services/api";
import { broadcastRefresh, errText, useAsync } from "../hooks/useAsync";
import { EVENT, onEvent } from "../services/events";
import { confirmDialog, toast } from "../components/common/Feedback";
import { EmptyRow, ErrorBlock, SkeletonRows } from "../components/common/StateBlock";
// 汇总卡 / 类型标签 / 口径文案与账号管理弹窗共用同一份定义，杜绝两处口径漂移
import { CREDIT_TASK_TYPES as TASK_TYPES, CREDIT_TYPE_LABEL as TASK_TYPE_LABEL, CreditObserved, CreditOriginNote, CreditSummaryCards, CreditTypeChips } from "../components/common/creditDetail";
import type { AuditLogPage, CreditDetail, CreditLogPage, LogQuery, RequestLog, RequestLogPage, TaskLogPage } from "../types";
import { useT } from "../i18n";

const STATUS_OPTIONS = [
  { label: "全部状态", value: 0 },
  { label: "2xx 成功", value: 200 },
  { label: "401 未鉴权", value: 401 },
  { label: "403 拒绝", value: 403 },
  { label: "429 限流", value: 429 },
  { label: "5xx 异常", value: 500 },
];

const AUDIT_ACTIONS = [
  { label: "全部操作", value: "" },
  { label: "配置", value: "config" },
  { label: "密钥", value: "key" },
  { label: "账号", value: "account" },
  { label: "日志", value: "log" },
];

const TASK_STATUS = [
  { label: "全部状态", value: 0 },
  { label: "成功", value: 1 },
  { label: "失败", value: 2 },
  { label: "跳过", value: 3 },
];

const TIME_RANGES = [
  { label: "全部时间", hours: 0 },
  { label: "近 1 小时", hours: 1 },
  { label: "近 24 小时", hours: 24 },
  { label: "近 7 天", hours: 24 * 7 },
];

const PAGE_SIZE = 50;

function statusBadge(status: number) {
  if (status === 0) return <span className="badge b-gray"><span className="d" />—</span>;
  if (status < 300) return <span className="badge b-green"><span className="d" />{status}</span>;
  if (status < 500) return <span className="badge b-amber"><span className="d" />{status}</span>;
  return <span className="badge b-red"><span className="d" />{status}</span>;
}

export default function Logs() {
  const location = useLocation();
  // 账号管理弹窗的「在日志页查看全部」会带 { tab: "earn", uid } 过来：
  // 落到「积分明细」页签，并把该账号填进关键字框（可编辑，用户能自己改掉）。
  // 页面按路由 pathname 挂载，所以这里直接当初始值用，不需要额外同步。
  const pre = (location.state ?? null) as { tab?: "earn"; uid?: string } | null;
  const [tab, setTab] = useState<"req" | "task" | "earn" | "credit" | "audit">(pre?.tab ?? "req");
  const [keyword, setKeyword] = useState(pre?.uid ?? "");
  const [status, setStatus] = useState(0);
  const [taskType, setTaskType] = useState("");
  const [taskStatus, setTaskStatus] = useState(0);
  const [auditType, setAuditType] = useState("");
  const [rangeHours, setRangeHours] = useState(0);
  const [page, setPage] = useState(1);
  const [exporting, setExporting] = useState(false);
  const [detail, setDetail] = useState<RequestLog | null>(null);

  const t = useT();

  const from = rangeHours > 0 ? Math.floor(Date.now() / 1000) - rangeHours * 3600 : 0;
  const query: LogQuery = {
    from,
    keyword: keyword.trim(),
    status: tab === "req" ? (status === 500 ? 0 : status) : taskStatus,
    statusIn: tab === "req" && status === 500 ? [500, 501, 502, 503, 504] : undefined,
    type: tab === "task" || tab === "earn" ? taskType : tab === "audit" ? auditType : "",
    page,
    pageSize: PAGE_SIZE,
  };

  const reqs = useAsync<RequestLogPage>(() => logsApi.requestLogs(query), [tab, keyword, status, rangeHours, page]);
  const tasks = useAsync<TaskLogPage>(() => logsApi.taskLogs(query), [tab, keyword, taskType, taskStatus, rangeHours, page]);
  const earns = useAsync<CreditDetail>(() => logsApi.creditDetail(query), [tab, keyword, taskType, rangeHours, page]);
  const credits = useAsync<CreditLogPage>(() => logsApi.creditLogs(query), [tab, keyword, rangeHours, page]);
  const audits = useAsync<AuditLogPage>(() => logsApi.auditLogs(query), [tab, keyword, auditType, rangeHours, page]);

  const load = useCallback(async () => {
    await Promise.all([reqs.reload(), tasks.reload(), earns.reload(), credits.reload(), audits.reload()]);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [reqs.reload, tasks.reload, earns.reload, credits.reload, audits.reload]);

  useEffect(() => {
    const onRefresh = () => void load();
    window.addEventListener("app:refresh", onRefresh);
    const off = onEvent(EVENT.gatewayLog, () => void load());
    const offTask = onEvent(EVENT.taskCompleted, () => void load());
    return () => {
      window.removeEventListener("app:refresh", onRefresh);
      off();
      offTask();
    };
  }, [load]);

  useEffect(() => {
    setPage(1);
  }, [tab, keyword, status, taskType, taskStatus, auditType, rangeHours]);

  const doExport = async (format: "csv" | "json") => {
    if (tab !== "req" && tab !== "task") return;
    setExporting(true);
    try {
      const res = await logsApi.export(format, tab === "req" ? "request" : "task", query);
      if (!res.path) return; // 用户取消
      toast.success(t("已导出 {n} 条 {fmt}", { n: res.count, fmt: format.toUpperCase() }), res.path);
    } catch (e) {
      toast.error(t("导出失败"), errText(e));
    } finally {
      setExporting(false);
    }
  };

  const doClear = async () => {
    if (tab !== "req" && tab !== "task") return;
    const ok = await confirmDialog({
      title: t("清空日志"),
      desc: t("将清空{type}日志，统计页的历史数据会一并归零，不可撤销。", { type: tab === "req" ? t("请求") : t("任务") }),
      danger: true,
      confirmText: t("清空"),
    });
    if (!ok) return;
    try {
      const n = await logsApi.clear(tab === "req" ? "request" : "task");
      toast.success(t("已清空 {n} 条日志", { n }));
      await load();
      broadcastRefresh();
    } catch (e) {
      toast.error(t("清空失败"), errText(e));
    }
  };

  const openExportDir = async () => {
    try {
      toast.info(t("导出目录"), await logsApi.exportDir());
    } catch (e) {
      toast.error(t("获取导出目录失败"), errText(e));
    }
  };

  const active = tab === "req" ? reqs : tab === "task" ? tasks : tab === "earn" ? earns : tab === "credit" ? credits : audits;
  const total =
    tab === "req" ? (reqs.data?.total ?? 0)
    : tab === "task" ? (tasks.data?.total ?? 0)
    : tab === "earn" ? (earns.data?.itemHits ?? 0)
    : tab === "credit" ? (credits.data?.total ?? 0)
    : (audits.data?.total ?? 0);
  const pages = Math.max(1, Math.ceil(total / PAGE_SIZE));

  return (
    <section className="page">
      <div className="page-head">
        <div>
          <h1>{t("日志查看")}</h1>
          <p>
            {t("请求 {req} 条 · 任务 {task} 条 · 积分流水 {credit} 条 · 审计 {audit} 条 · 实时跟随网关与任务事件", {
              req: reqs.data?.total ?? 0,
              task: tasks.data?.total ?? 0,
              credit: credits.data?.total ?? 0,
              audit: audits.data?.total ?? 0,
            })}
          </p>
        </div>
        <div className="head-actions">
          <button className="btn btn-ghost" onClick={() => void load()}>
            <RefreshCw size={13} strokeWidth={2} /> {t("刷新")}
          </button>
          {(tab === "req" || tab === "task") && (
            <>
              <button className="btn btn-ghost" disabled={exporting} onClick={() => void doExport("json")}>{t("导出 JSON")}</button>
              <button className="btn btn-soft" disabled={exporting} onClick={() => void doExport("csv")}>
                {exporting ? <Loader2 size={13} className="spin" /> : <Download size={13} strokeWidth={2} />} {t("导出 CSV")}
              </button>
            </>
          )}
        </div>
      </div>

      {active.error && <ErrorBlock message={active.error} onRetry={() => void load()} />}

      <div className="toolbar">
        <div className="seg">
          <button className={tab === "req" ? "on" : ""} onClick={() => setTab("req")}>{t("请求日志")}</button>
          <button className={tab === "task" ? "on" : ""} onClick={() => setTab("task")}>{t("任务日志")}</button>
          <button className={tab === "earn" ? "on" : ""} onClick={() => setTab("earn")}>{t("积分明细")}</button>
          <button className={tab === "credit" ? "on" : ""} onClick={() => setTab("credit")}>{t("积分流水")}</button>
          <button className={tab === "audit" ? "on" : ""} onClick={() => setTab("audit")}>{t("审计日志")}</button>
        </div>
        <div className="search-box">
          <Search size={13} strokeWidth={2.2} />
          <input
            className="search-input"
            placeholder={
              tab === "req" ? t("关键字 / 密钥 / IP / 模型 / 会话")
              : tab === "task" ? t("关键字 / 账号 / 消息")
              : tab === "earn" ? t("关键字 / 账号 / 领取说明")
              : tab === "credit" ? t("关键字 / 账号 / 备注")
              : t("关键字 / 操作 / 对象")
            }
            value={keyword}
            onChange={(e) => setKeyword(e.target.value)}
          />
        </div>
        <div className="seg">
          {TIME_RANGES.map((r) => (
            <button key={r.hours} className={rangeHours === r.hours ? "on" : ""} onClick={() => setRangeHours(r.hours)}>
              {t(r.label)}
            </button>
          ))}
        </div>
        {tab === "req" ? (
          <div className="seg">
            {STATUS_OPTIONS.map((o) => (
              <button key={o.value} className={status === o.value ? "on" : ""} onClick={() => setStatus(o.value)}>
                {t(o.label)}
              </button>
            ))}
          </div>
        ) : tab === "task" || tab === "earn" ? (
          <>
            <div className="seg">
              {TASK_TYPES.map((o) => (
                <button key={o.value} className={taskType === o.value ? "on" : ""} onClick={() => setTaskType(o.value)}>
                  {t(o.label)}
                </button>
              ))}
            </div>
            {/* 积分明细只统计领取成功的动作，状态过滤对它没有意义，不摆无用控件 */}
            {tab === "task" && (
              <div className="seg">
                {TASK_STATUS.map((o) => (
                  <button key={o.value} className={taskStatus === o.value ? "on" : ""} onClick={() => setTaskStatus(o.value)}>
                    {t(o.label)}
                  </button>
                ))}
              </div>
            )}
          </>
        ) : tab === "audit" ? (
          <div className="seg">
            {AUDIT_ACTIONS.map((o) => (
              <button key={o.value} className={auditType === o.value ? "on" : ""} onClick={() => setAuditType(o.value)}>
                {t(o.label)}
              </button>
            ))}
          </div>
        ) : null}
        <div className="spacer" />
        {(tab === "req" || tab === "task") && (
          <>
            <button className="btn btn-ghost sm" onClick={openExportDir}>
              <FolderOpen size={12} strokeWidth={2} /> {t("导出目录")}
            </button>
            <button className="btn btn-danger-soft sm" onClick={doClear}>
              <Eraser size={12} strokeWidth={2} /> {t("清空")}
            </button>
          </>
        )}
      </div>

      <div className="card" style={{ overflow: "hidden" }}>
        {tab === "req" ? (
          <table className="tbl">
            <thead>
              <tr>
                <th>{t("时间")}</th><th>{t("密钥")}</th><th>{t("模型")}</th><th>{t("状态")}</th>
                <th>{t("Token（输入/输出）")}</th><th>{t("延迟")}</th><th>{t("来源 IP")}</th><th>{t("会话")}</th>
              </tr>
            </thead>
            <tbody>
              {!reqs.settled && <SkeletonRows colSpan={8} rows={5} />}
              {reqs.settled && (reqs.data?.items?.length ?? 0) === 0 && (
                <EmptyRow colSpan={8} text={t("没有匹配的请求日志")} />
              )}
              {(reqs.data?.items ?? []).map((l) => (
                <tr key={l.id} className="row-click" onClick={() => setDetail(l)}>
                  <td className="mono">{l.time}</td>
                  <td className="mono">{l.keyName || "—"}</td>
                  <td>{l.model ? <span className="chip">{l.model}</span> : "—"}</td>
                  <td>{statusBadge(l.status)}</td>
                  <td className="mono">
                    {l.tokens ? `${l.tokens.toLocaleString()} (${l.inputTokens}/${l.outputTokens})` : "—"}
                  </td>
                  <td className="num">{l.latency.toFixed(0)} ms</td>
                  <td className="mono">{l.ip}</td>
                  <td className="mono">{l.sessionId || "—"}</td>
                </tr>
              ))}
            </tbody>
          </table>
        ) : tab === "credit" ? (
          <table className="tbl">
            <thead>
              <tr><th>{t("时间")}</th><th>{t("账号")}</th><th>{t("变动")}</th><th>{t("变动后余额")}</th><th>{t("备注")}</th></tr>
            </thead>
            <tbody>
              {!credits.settled && <SkeletonRows colSpan={5} rows={5} />}
              {credits.settled && (credits.data?.items?.length ?? 0) === 0 && (
                <EmptyRow colSpan={5} text={t("没有积分变动记录（余额观测来自签到 / 保活 / 网关记账）")} />
              )}
              {(credits.data?.items ?? []).map((l) => (
                <tr key={l.id}>
                  <td className="mono">{l.time}</td>
                  <td className="mono">{l.uid}</td>
                  <td className="num" style={{ color: l.delta > 0 ? "var(--red)" : l.delta < 0 ? "var(--green)" : undefined }}>
                    {/* CreditLog.Delta 语义（store.go）：正 = 消耗，负 = 增加 */}
                    {l.delta === 0 ? "0" : `${l.delta > 0 ? "-" : "+"}${Math.abs(l.delta).toLocaleString()}`}
                  </td>
                  <td className="num">{l.balance.toLocaleString()}</td>
                  <td>{l.note || "—"}</td>
                </tr>
              ))}
            </tbody>
          </table>
        ) : tab === "audit" ? (
          <table className="tbl">
            <thead>
              <tr><th>{t("时间")}</th><th>{t("操作")}</th><th>{t("对象")}</th><th>{t("详情")}</th></tr>
            </thead>
            <tbody>
              {!audits.settled && <SkeletonRows colSpan={4} rows={5} />}
              {audits.settled && (audits.data?.items?.length ?? 0) === 0 && (
                <EmptyRow colSpan={4} text={t("没有审计记录（配置 / 密钥 / 账号等敏感操作会记录在此）")} />
              )}
              {(audits.data?.items ?? []).map((l) => (
                <tr key={l.id}>
                  <td className="mono">{l.time}</td>
                  <td><span className="chip">{l.action}</span></td>
                  <td className="mono">{l.target || "—"}</td>
                  <td>{l.detail || "—"}</td>
                </tr>
              ))}
            </tbody>
          </table>
        ) : tab === "earn" ? (
          <div>
            {/* 今日 / 近 7 天跟随下方「账号 / 类型 / 关键字」筛选收窄，但不随时间范围变化 */}
            <CreditSummaryCards data={earns.data} rangeLabel="筛选范围内合计" />
            <CreditTypeChips groups={earns.data?.byTask} />

            {/* 口径必须写在数字前面：说明只统计上游给了数值的动作，并点明有多少条没给 */}
            <CreditOriginNote noAmount={earns.data?.noAmount ?? 0} />

            {/* 观测入账：流水里余额上升的条目（上游异步发放等），来源未确证所以单列 */}
            <CreditObserved data={earns.data} />

            <table className="tbl">
              <thead>
                <tr>
                  <th>{t("时间")}</th><th>{t("任务")}</th><th>{t("触发")}</th>
                  <th>{t("账号")}</th><th>{t("领取积分")}</th><th>{t("领取说明")}</th>
                </tr>
              </thead>
              <tbody>
                {!earns.settled && <SkeletonRows colSpan={6} rows={5} />}
                {earns.settled && (earns.data?.items?.length ?? 0) === 0 && (
                  <EmptyRow colSpan={6} text={t("没有领取到积分的记录（签到 / 成长任务 / 连登 / 抽奖等领取动作会记在这里）")} />
                )}
                {(earns.data?.items ?? []).map((l) => (
                  <tr key={l.id}>
                    <td className="mono">{l.time}</td>
                    <td><span className="chip">{t(TASK_TYPE_LABEL[l.type] ?? l.type)}</span></td>
                    <td>{l.trigger === "manual" ? t("手动") : t("排程")}</td>
                    <td className="mono">{l.uid}</td>
                    <td className="num earn-amount">+{l.credits.toLocaleString()}</td>
                    <td>{l.message}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : (
          <table className="tbl">
            <thead>
              <tr><th>{t("时间")}</th><th>{t("类型")}</th><th>{t("触发")}</th><th>{t("账号")}</th><th>{t("状态")}</th><th>{t("领取积分")}</th><th>{t("消息")}</th><th>{t("耗时")}</th></tr>
            </thead>
            <tbody>
              {!tasks.settled && <SkeletonRows colSpan={8} rows={5} />}
              {tasks.settled && (tasks.data?.items?.length ?? 0) === 0 && (
                <EmptyRow colSpan={8} text={t("没有匹配的任务日志")} />
              )}
              {(tasks.data?.items ?? []).map((l) => (
                <tr key={l.id}>
                  <td className="mono">{l.time}</td>
                  <td><span className="chip">{l.type}</span></td>
                  <td>{l.trigger === "manual" ? t("手动") : t("排程")}</td>
                  <td className="mono">{l.uid}</td>
                  <td>
                    <span className={`badge ${l.status === "success" ? "b-green" : l.status === "failed" ? "b-red" : "b-amber"}`}>
                      <span className="d" />
                      {l.status === "success" ? t("成功") : l.status === "failed" ? t("失败") : t("跳过")}
                    </span>
                  </td>
                  <td className="num earn-amount">{l.credits > 0 ? `+${l.credits.toLocaleString()}` : "—"}</td>
                  <td>{l.message}</td>
                  <td className="num">{l.duration.toFixed(0)} ms</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
        <div className="table-foot">
          <span className="muted">
            {t("共 {total} 条 · 本页 {n} 条", { total, n: active.data?.items?.length ?? 0 })}
            {tab === "req" && reqs.data ? t(" · 本页 Token {t1} · 失败 {t2}", { t1: reqs.data.tokens.toLocaleString(), t2: reqs.data.errors }) : ""}
          </span>
          <span className="spacer" />
          <div className="pager">
            <button disabled={page <= 1} onClick={() => setPage((p) => Math.max(1, p - 1))}>
              <ChevronLeft size={12} strokeWidth={2.4} />
            </button>
            <span>{page} / {pages}</span>
            <button disabled={page >= pages} onClick={() => setPage((p) => Math.min(pages, p + 1))}>
              <ChevronRight size={12} strokeWidth={2.4} />
            </button>
          </div>
        </div>
      </div>

      {detail && createPortal(
        <div className="overlay" onMouseDown={(e) => e.target === e.currentTarget && setDetail(null)}>
          <div className="dialog">
            <div className="dlg-head">
              <div>
                <h3>{t("请求详情")}</h3>
                <p>{detail.id}</p>
              </div>
              <button className="icon-btn" onClick={() => setDetail(null)}><X size={14} strokeWidth={2.2} /></button>
            </div>
            <div className="dlg-body">
              <div className="kv-grid">
                <KV k={t("时间")} v={detail.time} />
                <KV k={t("密钥")} v={detail.keyName || "—"} />
                <KV k={t("密钥 ID")} v={detail.keyId || "—"} />
                <KV k={t("模型")} v={detail.model || "—"} />
                <KV k={t("状态码")} v={String(detail.status)} />
                <KV k={t("Token 合计")} v={detail.tokens.toLocaleString()} />
                <KV k={t("输入 Token")} v={String(detail.inputTokens)} />
                <KV k={t("输出 Token")} v={String(detail.outputTokens)} />
                <KV k={t("总延迟")} v={`${detail.latency.toFixed(1)} ms`} />
                <KV k={t("首字节")} v={detail.firstLatency > 0 ? `${detail.firstLatency.toFixed(1)} ms` : "—"} />
                <KV k={t("来源 IP")} v={detail.ip} />
                <KV k={t("流式")} v={detail.stream ? t("是") : t("否")} />
                <KV k={t("会话 ID")} v={detail.sessionId || "—"} />
                <KV k={t("链路")} v={t("client → gateway → 账号池")} />
              </div>
              {detail.error && (
                <div style={{ marginTop: 12, padding: "10px 12px", borderRadius: 10, background: "var(--red-soft)", fontSize: 12, color: "var(--red)", lineHeight: 1.6 }}>
                  {detail.error}
                </div>
              )}
            </div>
            <div className="dlg-foot">
              <button className="btn btn-primary" onClick={() => setDetail(null)}>{t("关闭")}</button>
            </div>
          </div>
        </div>,
      document.body)}
    </section>
  );
}

function KV({ k, v }: { k: string; v: string }) {
  return (
    <div className="kv">
      <span>{k}</span>
      <b className="mono">{v}</b>
    </div>
  );
}

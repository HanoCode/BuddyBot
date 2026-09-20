// 账号维度的「领取积分明细」弹窗。
//
// 账号管理页三个入口共用它（表格操作列的「领取明细」、最近一次任务结果的「领取 n 分」徽标、
// 账号详情底栏），日志页的「积分明细」页签则是全局视角——两者读同一个后端方法，
// 差别只在有没有带 uid。
//
// 数字全部来自 GetCreditDetail(uid)，不在这里二次求和、也不缓存：口径只有后端一处。
import { useMemo } from "react";
import { createPortal } from "react-dom";
import { useNavigate } from "react-router-dom";
import { Coins, Loader2, RefreshCw, X } from "lucide-react";
import { logsApi } from "../../services/api";
import { useAsync } from "../../hooks/useAsync";
import { EmptyRow, ErrorBlock, SkeletonRows } from "../common/StateBlock";
import type { CreditDetail, LogQuery } from "../../types";
import { useT } from "../../i18n";
import { CreditDayBars, CreditOriginNote, CreditSummaryCards, CreditTypeChips, CREDIT_TYPE_LABEL } from "./creditDetail";

/** 弹窗一次取满：弹窗是「看某账号领了多少」，翻页价值低，超出部分引导去日志页 */
const PAGE_SIZE = 200;

export default function CreditDetailDialog({
  uid,
  name,
  onClose,
}: {
  uid: string;
  name: string;
  onClose: () => void;
}) {
  const t = useT();
  const navigate = useNavigate();
  const q = useMemo<LogQuery>(() => ({ uid, pageSize: PAGE_SIZE }), [uid]);
  const earn = useAsync<CreditDetail>(() => logsApi.creditDetail(q), [uid]);

  const goLogs = () => {
    // 带上 uid 预置：日志页读 state 后直接落到「积分明细」页签并按该账号过滤
    navigate("/logs", { state: { tab: "earn", uid } });
    onClose();
  };

  return createPortal(
    <div className="overlay" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div className="dialog cd-dlg">
        <div className="dlg-head">
          <div>
            <h3>{t("领取积分明细")}</h3>
            <p>{name} · <span className="mono">{uid}</span></p>
          </div>
          <button className="icon-btn" data-tip={t("关闭")} onClick={onClose}>
            <X size={14} strokeWidth={2.2} />
          </button>
        </div>

        <div className="dlg-body">
          {/* 只统计该账号：避免用户把这里的数字理解成全网合计 */}
          <div className="cd-scope">
            <Coins size={13} strokeWidth={2.2} />
            {t("以下只统计该账号的领取动作")}
          </div>

          {earn.error && <ErrorBlock message={earn.error} onRetry={() => void earn.reload()} />}

          <CreditSummaryCards data={earn.data} rangeLabel="累计合计" />
          <CreditTypeChips groups={earn.data?.byTask} />
          <CreditDayBars days={earn.data?.byDay} />
          <CreditOriginNote noAmount={earn.data?.noAmount ?? 0} />

          <table className="tbl">
            <thead>
              <tr>
                <th>{t("时间")}</th><th>{t("任务")}</th><th>{t("触发")}</th>
                <th>{t("领取积分")}</th><th>{t("领取说明")}</th>
              </tr>
            </thead>
            <tbody>
              {!earn.settled && <SkeletonRows colSpan={5} rows={4} />}
              {earn.settled && (earn.data?.items?.length ?? 0) === 0 && (
                <EmptyRow colSpan={5} text={t("该账号还没有领取到积分的记录（签到 / 成长任务 / 连登 / 抽奖等领取动作会记在这里）")} />
              )}
              {(earn.data?.items ?? []).map((l) => (
                <tr key={l.id}>
                  <td className="mono">{l.time}</td>
                  <td><span className="chip">{t(CREDIT_TYPE_LABEL[l.type] ?? l.type)}</span></td>
                  <td>{l.trigger === "manual" ? t("手动") : t("排程")}</td>
                  <td className="num earn-amount">+{l.credits.toLocaleString()}</td>
                  <td>{l.message}</td>
                </tr>
              ))}
            </tbody>
          </table>

          {(earn.data?.items?.length ?? 0) >= PAGE_SIZE && (
            <p className="cd-more">
              {t("仅显示最近 {n} 条，更多请到日志页「积分明细」按账号查看。", { n: PAGE_SIZE })}
            </p>
          )}
        </div>

        <div className="dlg-foot">
          <button className="btn btn-ghost foot-left" onClick={goLogs}>{t("在日志页查看全部")}</button>
          <button className="btn btn-ghost" disabled={earn.loading} onClick={() => void earn.reload()}>
            {earn.loading ? <Loader2 size={13} className="spin" /> : <RefreshCw size={13} strokeWidth={2} />} {t("刷新")}
          </button>
          <button className="btn btn-primary" onClick={onClose}>{t("关闭")}</button>
        </div>
      </div>
    </div>,
    document.body,
  );
}

// 领取积分明细的共用展示件与文案。
//
// 日志页「积分明细」页签与账号管理弹窗共用这里的组件/常量，而不是各写一份：
// 口径说明、任务名映射一旦分叉，两处数字看着一样但解释不同，是最难查的那类问题。
import type { CreditDetail, CreditDetailDay, CreditDetailTask } from "../../types";
import { useT } from "../../i18n";

/** 任务类型下拉与标签共用的同一份定义（空值项供筛选下拉使用，不进标签映射） */
export const CREDIT_TASK_TYPES: { label: string; value: string }[] = [
  { label: "全部类型", value: "" },
  { label: "签到", value: "checkin" },
  { label: "旅行", value: "travel" },
  { label: "保活", value: "keepalive" },
  { label: "活跃地图", value: "activity" },
  { label: "开学季", value: "school" },
  { label: "夜猫子", value: "cat" },
  { label: "成长任务", value: "growth" },
];

/** 任务类型值 → 中文名；遇到后端新增类型时回退显示原值，不吞掉 */
export const CREDIT_TYPE_LABEL: Record<string, string> = Object.fromEntries(
  CREDIT_TASK_TYPES.filter((o) => o.value !== "").map((o) => [o.value, o.label]),
);

/**
 * 口径说明的原文（中文即 i18n key）。
 * 必须写在数字前面：先讲清「只统计上游给了数值的动作」，再点明有多少条没给，
 * 否则用户会把「没统计到」当成「没领到」。
 *
 * 这段文案**不要**往里塞账号等可变片段：key 一变，已翻好的英文就会被打回中文。
 * 账号范围由页面在标题处单独说明（见 CreditDetailDialog 的 .cd-scope）。
 */
export const CREDIT_ORIGIN_NOTE =
  "仅统计上游明确返回积分数值的领取动作（成长任务奖励 / 连登档位 / 抽奖积分 / 礼包补偿）。签到本金、开学季领奖、盲盒物品、试用加油包等上游不返回数值的动作不计入——当前筛选范围内另有 {n} 条执行记录未返回数值。实际到账总额请以「积分流水」的真实余额观测为准。";

/** 三张汇总卡：今日 / 近 7 天 / 范围合计 */
export function CreditSummaryCards({ data, rangeLabel }: { data?: CreditDetail | null; rangeLabel: string }) {
  const t = useT();
  return (
    <div className="earn-cards">
      <div className="earn-card">
        <small>{t("今日领取")}</small>
        <b>{(data?.today ?? 0).toLocaleString()}</b>
        <i>{t("分")}</i>
      </div>
      <div className="earn-card">
        <small>{t("近 7 天领取")}</small>
        <b>{(data?.last7d ?? 0).toLocaleString()}</b>
        <i>{t("分")}</i>
      </div>
      <div className="earn-card">
        <small>{t(rangeLabel)}</small>
        <b>{(data?.credits ?? 0).toLocaleString()}</b>
        <i>{t("分")}</i>
      </div>
    </div>
  );
}

/** 按任务类型的领取汇总小标签（积分降序，后端已排好） */
export function CreditTypeChips({ groups }: { groups?: CreditDetailTask[] | null }) {
  const t = useT();
  if (!groups || groups.length === 0) return null;
  return (
    <div className="earn-tags">
      {groups.map((g) => (
        <span className="earn-tag" key={g.type}>
          {t(CREDIT_TYPE_LABEL[g.type] ?? g.type)}
          <b>+{g.credits.toLocaleString()}</b>
          <i>{t("{n} 次", { n: g.count })}</i>
        </span>
      ))}
    </div>
  );
}

/** 口径说明块 */
export function CreditOriginNote({ noAmount }: { noAmount: number }) {
  const t = useT();
  return <p className="earn-note">{t(CREDIT_ORIGIN_NOTE, { n: noAmount })}</p>;
}

/** 按天领取的迷你条形列表（近 days 天，日期降序）。ByDay 在日志页没有展示位，弹窗里给它 */
export function CreditDayBars({ days, max = 7 }: { days?: CreditDetailDay[] | null; max?: number }) {
  const t = useT();
  const rows = (days ?? []).slice(0, max);
  if (rows.length === 0) return null;
  const peak = Math.max(...rows.map((d) => d.credits), 1);
  return (
    <div className="cd-days">
      <div className="cd-days-h">
        {t("按天汇总")}
        <span className="muted">{rows.length < (days?.length ?? 0) ? t("最近 {n} 天", { n: rows.length }) : ""}</span>
      </div>
      {rows.map((d) => (
        <div className="cd-day" key={d.date}>
          <span className="mono cd-day-d">{d.date.slice(5)}</span>
          <span className="cd-day-b">
            <i style={{ width: `${Math.max(3, Math.round((d.credits / peak) * 100))}%` }} />
          </span>
          <span className="num earn-amount">+{d.credits.toLocaleString()}</span>
          <span className="cd-day-c">{t("{n} 次", { n: d.count })}</span>
        </div>
      ))}
    </div>
  );
}

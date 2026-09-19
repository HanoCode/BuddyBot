import { AlertTriangle, Inbox, Loader2 } from "lucide-react";
import { useT } from "../../i18n";

/**
 * 数据状态统一呈现。
 *
 * 设计原则：界面上的每个数字都必须能追到真数据。因此这里只有三种状态——
 * 加载中 / 真实错误 / 真实的空，绝不出现「用假数据把空态填满」。
 */

export function LoadingBlock({ label }: { label?: string }) {
  const t = useT();
  return (
    <div className="state-block">
      <Loader2 size={18} className="spin" />
      <span>{t(label ?? "加载中")}…</span>
    </div>
  );
}

export function ErrorBlock({ message, onRetry }: { message: string; onRetry?: () => void }) {
  const t = useT();
  return (
    <div className="state-block error">
      <AlertTriangle size={18} />
      <div>
        <div className="sb-title">{t("数据加载失败")}</div>
        <div className="sb-desc">{message}</div>
      </div>
      {onRetry && (
        <button className="btn btn-ghost sm" onClick={onRetry}>
          {t("重试")}
        </button>
      )}
    </div>
  );
}

export function EmptyBlock({
  title,
  desc,
  action,
}: {
  title: string;
  desc?: string;
  action?: React.ReactNode;
}) {
  return (
    <div className="state-block empty">
      <Inbox size={18} />
      <div>
        <div className="sb-title">{title}</div>
        {desc && <div className="sb-desc">{desc}</div>}
      </div>
      {action}
    </div>
  );
}

/** 表格内的空行（保持表格结构） */
export function EmptyRow({ colSpan, text }: { colSpan: number; text: string }) {
  return (
    <tr>
      <td colSpan={colSpan} className="empty-cell">
        {text}
      </td>
    </tr>
  );
}

/** 占位行骨架 */
export function SkeletonRows({ colSpan, rows = 4 }: { colSpan: number; rows?: number }) {
  return (
    <>
      {Array.from({ length: rows }).map((_, i) => (
        <tr key={i}>
          <td colSpan={colSpan}>
            <div className="skel" style={{ height: 22, borderRadius: 8 }} />
          </td>
        </tr>
      ))}
    </>
  );
}

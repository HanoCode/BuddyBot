import { useState } from "react";
import { createPortal } from "react-dom";
import { useT } from "../../i18n";
import { setCloseBehavior, type CloseBehavior } from "../../hooks/useCloseBehavior";

interface Props {
  open: boolean;
  onCancel: () => void;
  /** 用户选定动作（已含「记住选择」落盘），由调用方执行隐藏/退出 */
  onDecide: (behavior: Exclude<CloseBehavior, "ask">) => void;
}

/**
 * 关闭询问弹窗：最小化到托盘 / 退出程序，可选记住选择。
 * 退出路径在此之外仍会经 confirmDialog 二次确认（见 TitleBar.handleClose）。
 */
export default function CloseAskDialog({ open, onCancel, onDecide }: Props) {
  const t = useT();
  const [choice, setChoice] = useState<"tray" | "quit">("tray");
  const [remember, setRemember] = useState(false);
  if (!open) return null;

  const confirm = () => {
    if (remember) setCloseBehavior(choice);
    onDecide(choice);
  };

  // 必须用 Portal 渲染到 body：.titlebar 带 backdrop-filter，会把内部
  // position:fixed 的包含块限制在标题栏上，导致弹窗以标题栏为中心、顶部溢出截断
  return createPortal(
    <div className="overlay" onMouseDown={(e) => e.target === e.currentTarget && onCancel()}>
      <div className="dialog">
        <div className="dlg-head">
          <div>
            <h3>{t("关闭 BuddyBot")}</h3>
            <p>{t("要最小化到托盘继续运行，还是退出程序？")}</p>
          </div>
        </div>
        <div className="dlg-body close-ask-body">
          <button
            className={`close-opt${choice === "tray" ? " on" : ""}`}
            onClick={() => setChoice("tray")}
          >
            <strong>{t("最小化到托盘")}</strong>
            <span>{t("窗口隐藏，网关与定时任务继续在后台运行；点击菜单栏托盘图标可找回窗口")}</span>
          </button>
          <button
            className={`close-opt${choice === "quit" ? " on" : ""}`}
            onClick={() => setChoice("quit")}
          >
            <strong>{t("退出程序")}</strong>
            <span>{t("停止网关并退出 BuddyBot，确认后会再次提醒")}</span>
          </button>
          <label className="close-remember">
            <input type="checkbox" checked={remember} onChange={(e) => setRemember(e.target.checked)} />
            {t("记住我的选择，不再询问（可在 设置 → 通用 里修改）")}
          </label>
        </div>
        <div className="dlg-foot">
          <button className="btn btn-ghost" onClick={onCancel}>
            {t("取消")}
          </button>
          <button className="btn btn-primary" onClick={confirm}>
            {t("确定")}
          </button>
        </div>
      </div>
    </div>,
    document.body,
  );
}

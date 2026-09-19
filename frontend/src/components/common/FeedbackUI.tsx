import { useEffect, useState } from "react";
import { CheckCircle2, AlertTriangle, Info, XCircle, X, Loader2 } from "lucide-react";
import { useToasts, useConfirm, usePrompt } from "./Feedback";
import { useT } from "../../i18n";

const ICONS = {
  success: <CheckCircle2 size={16} strokeWidth={2} />,
  error: <XCircle size={16} strokeWidth={2} />,
  warn: <AlertTriangle size={16} strokeWidth={2} />,
  info: <Info size={16} strokeWidth={2} />,
};

/** 全局 Toast 容器（挂在 App 根部） */
export function ToastHost() {
  const { toasts, dismiss } = useToasts();
  return (
    <div className="toast-host">
      {toasts.map((t) => (
        <div className={`toast toast-${t.kind}`} key={t.id}>
          <span className="t-ic">{ICONS[t.kind]}</span>
          <div className="t-body">
            <div className="t-title">{t.title}</div>
            {t.desc && <div className="t-desc">{t.desc}</div>}
          </div>
          <button className="t-x" onClick={() => dismiss(t.id)}>
            <X size={13} strokeWidth={2.2} />
          </button>
        </div>
      ))}
    </div>
  );
}

/** 全局确认弹窗（挂在 App 根部）；确认键点击后即刻置 busy，防双击重复提交 */
export function ConfirmHost() {
  const { visible, title, desc, danger, confirmText, answer } = useConfirm();
  const t = useT();
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    if (!visible) setBusy(false);
  }, [visible]);
  if (!visible) return null;
  const confirm = () => {
    if (busy) return;
    setBusy(true);
    answer(true);
  };
  return (
    <div className="overlay" onMouseDown={(e) => e.target === e.currentTarget && !busy && answer(false)}>
      <div className="dialog">
        <div className="dlg-head">
          {danger && (
            <span className="dlg-warn-ic">
              <AlertTriangle size={17} strokeWidth={2} />
            </span>
          )}
          <div>
            <h3>{title}</h3>
            <p>{desc}</p>
          </div>
        </div>
        <div className="dlg-foot">
          <button className="btn btn-ghost" disabled={busy} onClick={() => answer(false)}>
            {t("取消")}
          </button>
          <button
            className={danger ? "btn btn-danger" : "btn btn-primary"}
            disabled={busy}
            onClick={confirm}
          >
            {busy && <Loader2 size={13} className="spin" />}
            {confirmText || t("确认")}
          </button>
        </div>
      </div>
    </div>
  );
}

/** 全局输入弹窗（挂在 App 根部）：导出/导入密码等一次性文本输入 */
export function PromptHost() {
  const { visible, title, desc, placeholder, type, confirmText, optional, answer } = usePrompt();
  const t = useT();
  const [value, setValue] = useState("");
  useEffect(() => {
    if (visible) setValue("");
  }, [visible]);
  if (!visible) return null;
  const submit = () => answer(value);
  return (
    <div className="overlay" onMouseDown={(e) => e.target === e.currentTarget && answer(null)}>
      <div className="dialog">
        <div className="dlg-head">
          <div>
            <h3>{title}</h3>
            {desc && <p>{desc}</p>}
          </div>
        </div>
        <div className="dlg-body">
          <input
            className="search-input"
            type={type === "password" ? "password" : "text"}
            placeholder={placeholder}
            autoFocus
            value={value}
            onChange={(e) => setValue(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter" && (optional || value.trim())) submit();
              if (e.key === "Escape") answer(null);
            }}
          />
        </div>
        <div className="dlg-foot">
          <button className="btn btn-ghost" onClick={() => answer(null)}>
            {t("取消")}
          </button>
          <button className="btn btn-primary" disabled={!optional && !value.trim()} onClick={submit}>
            {confirmText || t("确认")}
          </button>
        </div>
      </div>
    </div>
  );
}

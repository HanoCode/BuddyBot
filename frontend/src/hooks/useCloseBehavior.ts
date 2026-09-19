/**
 * 关闭按钮行为偏好（localStorage 持久化，同 wb-theme 等 UI 偏好）。
 *
 * - ask：每次点红绿灯关闭时弹窗询问（默认）
 * - tray：直接隐藏到托盘（网关/定时任务继续后台运行）
 * - quit：退出程序（每次仍弹窗确认，防误触）
 */

export type CloseBehavior = "ask" | "tray" | "quit";

const KEY = "wb-close-behavior";

export function getCloseBehavior(): CloseBehavior {
  const v = localStorage.getItem(KEY);
  if (v === "tray" || v === "quit" || v === "ask") return v;
  return "ask";
}

export function setCloseBehavior(b: CloseBehavior): void {
  localStorage.setItem(KEY, b);
}

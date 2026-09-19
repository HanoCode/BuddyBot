// ============================================================
// 后端 → 前端 事件订阅（Wails Events）
//
// 事件全部来自后端真实动作：网关请求日志、账号状态变化、任务执行完成、
// 聊天流式分片。浏览器环境没有任何后端，因此直接订阅为空实现。
// ============================================================
import { IS_WAILS } from "./api";

export type Unsubscribe = () => void;

type Handler = (data: unknown) => void;

/** 事件名（与 Go internal/core/events.go 一一对应） */
export const EVENT = {
  gatewayLog: "gateway:log",
  accountStatus: "account:statusChanged",
  taskProgress: "task:progress",
  taskCompleted: "task:completed",
  chatToken: "chat:token",
  chatReasoning: "chat:reasoning",
  chatDone: "chat:done",
  injectStatus: "inject:status",
  clientSwitch: "clientswitch:progress",
  appError: "app:error",
  updateProgress: "update:progress",
  updateAvailable: "update:available",
} as const;

/** 订阅后端事件；浏览器环境返回空取消函数 */
export function onEvent(name: string, handler: Handler): Unsubscribe {
  if (!IS_WAILS) return () => undefined;
  let unsub: Unsubscribe | undefined;
  let disposed = false;
  const subscribe = (retries: number) => {
    import("@wailsio/runtime")
      .then(({ Events }) => {
        if (disposed) return;
        // Wails v3 的回调参数是 WailsEvent 实例（{ name, data }），真实负载在 .data 上；
        // 不拆包直接 String(payload) 会得到 "[object Object]"（流式分片逐字堆满气泡）
        unsub = Events.On(name, (ev: unknown) => {
          const payload = (ev as { data?: unknown } | null)?.data;
          handler(payload === undefined || payload === null ? ev : payload);
        }) as Unsubscribe;
      })
      .catch((e) => {
        if (disposed) return;
        // 动态加载失败（启动早期 runtime 未就绪）：有限重试，仍失败则留下痕迹
        if (retries > 0) window.setTimeout(() => subscribe(retries - 1), 1500);
        else console.warn(`[events] 订阅 ${name} 失败，实时事件不可用`, e);
      });
  };
  subscribe(3);
  return () => {
    disposed = true;
    unsub?.();
  };
}

import { create } from "zustand";

// ============================================================
// Toast 通知 + 确认弹窗（全局单例）
// ============================================================

export type ToastKind = "success" | "error" | "info" | "warn";

export interface ToastItem {
  id: number;
  kind: ToastKind;
  title: string;
  desc?: string;
}

interface ToastState {
  toasts: ToastItem[];
  push: (kind: ToastKind, title: string, desc?: string) => void;
  dismiss: (id: number) => void;
}

let nextId = 1;

// error 驻留更久（9s）且叠加上限时优先挤掉 info/success，避免连续操作把关键错误冲掉
const TOAST_TTL: Record<ToastKind, number> = { success: 4200, info: 4200, warn: 6500, error: 9000 };
const EVICT_ORDER: ToastKind[] = ["info", "success", "warn", "error"];

export const useToasts = create<ToastState>((set) => ({
  toasts: [],
  push: (kind, title, desc) => {
    const id = nextId++;
    set((s) => {
      let list = [...s.toasts];
      if (list.length >= 5) {
        // 满员时按「低价值先走」的顺序淘汰，绝不挤掉同类或更高优先级的提示
        const victim = EVICT_ORDER.map((k) => list.findLastIndex((t) => t.kind === k)).find((i) => i >= 0);
        if (victim !== undefined) list.splice(victim, 1);
        else list.shift();
      }
      list.push({ id, kind, title, desc });
      return { toasts: list };
    });
    setTimeout(() => set((s) => ({ toasts: s.toasts.filter((t) => t.id !== id) })), TOAST_TTL[kind]);
  },
  dismiss: (id) => set((s) => ({ toasts: s.toasts.filter((t) => t.id !== id) })),
}));

export const toast = {
  success: (title: string, desc?: string) => useToasts.getState().push("success", title, desc),
  error: (title: string, desc?: string) => useToasts.getState().push("error", title, desc),
  info: (title: string, desc?: string) => useToasts.getState().push("info", title, desc),
  warn: (title: string, desc?: string) => useToasts.getState().push("warn", title, desc),
};

// ---------- 确认弹窗 ----------

export interface ConfirmOptions {
  title: string;
  desc: string;
  danger?: boolean;
  confirmText?: string;
}

interface ConfirmState extends ConfirmOptions {
  visible: boolean;
  resolve: ((ok: boolean) => void) | null;
  ask: (opts: ConfirmOptions) => Promise<boolean>;
  answer: (ok: boolean) => void;
}

export const useConfirm = create<ConfirmState>((set, get) => ({
  visible: false,
  title: "",
  desc: "",
  danger: false,
  confirmText: "确认",
  resolve: null,
  ask: (opts) =>
    new Promise<boolean>((resolve) => {
      set({ ...opts, visible: true, resolve });
    }),
  answer: (ok) => {
    get().resolve?.(ok);
    set({ visible: false, resolve: null });
  },
}));

/** 命令式：await confirmDialog({ title, desc, danger: true }) */
export const confirmDialog = (opts: ConfirmOptions) => useConfirm.getState().ask(opts);

// ---------- 输入弹窗（如导出密码） ----------

export interface PromptOptions {
  title: string;
  desc?: string;
  placeholder?: string;
  /** password = 密码框（圆点回显） */
  type?: "text" | "password";
  confirmText?: string;
  /** 确认为空值时是否允许提交（默认允许，由调用方校验） */
  optional?: boolean;
}

interface PromptState extends PromptOptions {
  visible: boolean;
  resolve: ((value: string | null) => void) | null;
  ask: (opts: PromptOptions) => Promise<string | null>;
  answer: (value: string | null) => void;
}

export const usePrompt = create<PromptState>((set, get) => ({
  visible: false,
  title: "",
  desc: "",
  placeholder: "",
  type: "text",
  confirmText: "确认",
  optional: true,
  resolve: null,
  ask: (opts) =>
    new Promise<string | null>((resolve) => {
      set({ ...opts, visible: true, resolve });
    }),
  answer: (value) => {
    get().resolve?.(value);
    set({ visible: false, resolve: null });
  },
}));

/** 命令式：await promptDialog({ title, type: "password" })；取消返回 null */
export const promptDialog = (opts: PromptOptions) => usePrompt.getState().ask(opts);

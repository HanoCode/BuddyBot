import { useSyncExternalStore } from "react";
import { en as enBase } from "./en";
import { en as enDashboard } from "./en-dashboard";
import { en as enTokens } from "./en-tokens";
import { en as enAccounts } from "./en-accounts";
import { en as enAgents } from "./en-agents";
import { en as enSkills } from "./en-skills";
import { en as enKeys } from "./en-keys";
import { en as enChat } from "./en-chat";
import { en as enLogs } from "./en-logs";
import { en as enPrompts } from "./en-prompts";
import { en as enClientTokens } from "./en-clienttokens";
import { en as enPlugins } from "./en-plugins";

/**
 * 轻量 i18n：中文原文即 key。
 *
 * - zh：直接返回原文（零开销、零遗漏——没翻译的字符串中文下不受影响）；
 * - en：查词典，未收录的 key 原样回退（绝不白屏）；
 * - 词典按模块拆分（en.ts + en-<page>.ts），便于各页面独立维护；
 * - 支持 {name} 插值：t("共 {n} 个账号", { n: total })。
 */

export type Lang = "zh" | "en";

// 合并所有分片词典（key 冲突时后导入覆盖先导入，语义等价即可）
const DICT: Record<string, string> = Object.assign(
  {},
  enBase,
  enDashboard,
  enTokens,
  enAccounts,
  enAgents,
  enSkills,
  enKeys,
  enChat,
  enLogs,
  enPrompts,
  enClientTokens,
  enPlugins,
);

const STORAGE_KEY = "apiary.lang";

function initial(): Lang {
  const saved = localStorage.getItem(STORAGE_KEY);
  if (saved === "zh" || saved === "en") return saved;
  return "zh"; // 中文优先：无历史偏好时默认中文
}

let lang: Lang = initial();
const listeners = new Set<() => void>();

export function getLang(): Lang {
  return lang;
}

export function setLang(next: Lang): void {
  if (next === lang) return;
  lang = next;
  localStorage.setItem(STORAGE_KEY, next);
  document.documentElement.lang = next === "zh" ? "zh-CN" : "en";
  listeners.forEach((fn) => fn());
}

function subscribe(fn: () => void): () => void {
  listeners.add(fn);
  return () => listeners.delete(fn);
}

function interpolate(s: string, p?: Record<string, string | number>): string {
  if (!p) return s;
  let out = s;
  for (const [k, v] of Object.entries(p)) out = out.split(`{${k}}`).join(String(v));
  return out;
}

/** 取词（可在任意模块调用，包括事件回调与 toast 文案） */
export function t(s: string, p?: Record<string, string | number>): string {
  let out = s;
  if (lang === "en") out = DICT[s] ?? s;
  return interpolate(out, p);
}

/**
 * React 组件内取词钩子：订阅语言变化并触发重渲染。
 * 用法：const t = useT();
 */
export function useT(): typeof t {
  useSyncExternalStore(subscribe, getLang, getLang);
  return t;
}

/** 需要直接读取/设置语言的场景（如语言切换按钮）：const [lang, setLang] = useLang(); */
export function useLang(): [Lang, (next: Lang) => void] {
  const current = useSyncExternalStore(subscribe, getLang, getLang);
  return [current, setLang];
}

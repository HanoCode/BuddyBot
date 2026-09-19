import { useEffect, useState } from "react";

export type Theme = "light" | "dark";

const KEY = "wb-theme";

function initial(): Theme {
  const saved = localStorage.getItem(KEY);
  if (saved === "light" || saved === "dark") return saved;
  return "dark"; // 默认深色
}

/** 主题切换：写到 <html data-theme>，CSS variables 全局生效 */
export function useTheme(): [Theme, () => void] {
  const [theme, setTheme] = useState<Theme>(initial);

  useEffect(() => {
    document.documentElement.dataset.theme = theme;
    localStorage.setItem(KEY, theme);
  }, [theme]);

  const toggle = () => setTheme((t) => (t === "dark" ? "light" : "dark"));
  return [theme, toggle];
}

// ============================================================
// 强调色预设（应用内多主题 · P2-8）
// 浅/深主题之上的第二维度：主色族（--primary/--primary-h/--primary-soft）
// 由 <html data-accent> 驱动（见 tokens.css），选择持久化在 localStorage。
// ============================================================

export const ACCENTS = [
  { id: "indigo", name: "靛蓝", color: "#6366F1" },
  { id: "ocean", name: "海蓝", color: "#3B82F6" },
  { id: "emerald", name: "翠绿", color: "#10B981" },
  { id: "violet", name: "紫罗兰", color: "#8B5CF6" },
  { id: "rose", name: "玫红", color: "#F43F5E" },
  { id: "amber", name: "琥珀", color: "#F59E0B" },
] as const;

export type Accent = (typeof ACCENTS)[number]["id"];

const ACCENT_KEY = "wb-accent";

function initialAccent(): Accent {
  const saved = localStorage.getItem(ACCENT_KEY);
  for (const a of ACCENTS) if (saved === a.id) return a.id;
  return "indigo";
}

/** 强调色切换：写到 <html data-accent>，主色族 CSS variables 全局生效 */
export function useAccent(): [Accent, (a: Accent) => void] {
  const [accent, setAccent] = useState<Accent>(initialAccent);

  useEffect(() => {
    if (accent === "indigo") {
      delete document.documentElement.dataset.accent; // 默认色 = 无覆盖
    } else {
      document.documentElement.dataset.accent = accent;
    }
    localStorage.setItem(ACCENT_KEY, accent);
  }, [accent]);

  return [accent, setAccent];
}

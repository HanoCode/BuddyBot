import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useNavigate } from "react-router-dom";
import {
  LayoutDashboard, Coins, Users, KeyRound, MessageSquare, ScrollText, Settings,
  Power, RotateCw, RefreshCw, Moon, Search, CornerDownLeft, ArrowUpDown, SlidersHorizontal,
  Coffee, Blocks, Puzzle, ArrowUpCircle, FolderOpen, Archive,
} from "lucide-react";
import { accountsApi, configApi, gatewayApi, injectApi, keysApi, powerApi, systemApi } from "../../services/api";
import { toast } from "../common/Feedback";
import { useTheme } from "../../hooks/useTheme";
import { useLang, useT } from "../../i18n";
import { NAV as SETTINGS_NAV } from "../../pages/Settings";
import type { AwakeStatus, InjectStatus } from "../../types";

/**
 * ⌘K / Ctrl+K 全局搜索 Command Palette（UI-DESIGN.md §5）。
 *
 * 搜索范围：页面直达 + 全局动作 + 设置分区/条目 + 账号 + 密钥（打开面板时按需拉取一次）。
 * 键盘：↑↓ 选择、Enter 执行、Esc 关闭；点击遮罩关闭。
 */

// 设置条目索引：跳转到条目所属分区（颗粒度到条目，定位到分区）。
// label/nav 均为中文原文（即 i18n key），展示时经 t() 翻译，深链参数保持原 key 不变。
const SETTING_ITEMS: { label: string; nav: string }[] = [
  { label: "监听地址", nav: "网关服务" },
  { label: "根密钥", nav: "网关服务" },
  { label: "凭证目录", nav: "网关服务" },
  { label: "只读模式", nav: "安全与访问" },
  { label: "IP 白名单", nav: "安全与访问" },
  { label: "IP 黑名单", nav: "安全与访问" },
  { label: "系统提示词", nav: "提示词与模型" },
  { label: "会话粘性", nav: "会话粘性" },
  { label: "粘性有效期", nav: "会话粘性" },
  { label: "每日签到时间", nav: "定时任务" },
  { label: "定时刷新", nav: "定时任务" },
  { label: "任务完成推送", nav: "定时任务" },
  { label: "单账号最大并发", nav: "账号池策略" },
  { label: "熔断阈值", nav: "账号池策略" },
  { label: "状态镜像", nav: "状态镜像" },
  { label: "保持系统唤醒", nav: "增强功能" },
  { label: "注入面板", nav: "增强功能" },
  { label: "导出配置", nav: "数据与备份" },
  { label: "立即备份", nav: "数据与备份" },
  { label: "检查更新", nav: "关于" },
];

interface CommandItem {
  id: string;
  label: string;
  hint?: string;
  group: string;
  icon: React.ReactNode;
  run: () => void | Promise<void>;
}

export default function CommandPalette() {
  const navigate = useNavigate();
  const [theme, toggleTheme] = useTheme();
  const [lang] = useLang(); // 订阅语言变化：切语言时触发 items 重算
  const t = useT();
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [active, setActive] = useState(0);
  const [accounts, setAccounts] = useState<{ uid: string; nickname: string; status: string }[]>([]);
  const [keys, setKeys] = useState<{ id: string; name: string; disabled?: boolean }[]>([]);
  const [power, setPower] = useState<AwakeStatus | null>(null);
  const [inject, setInject] = useState<InjectStatus | null>(null);
  const inputRef = useRef<HTMLInputElement>(null);
  const listRef = useRef<HTMLDivElement>(null);

  const go = useCallback(
    (path: string) => {
      setOpen(false);
      navigate(path);
    },
    [navigate],
  );

  const gatewayAction = useCallback(
    async (kind: "start" | "stop" | "restart") => {
      setOpen(false);
      try {
        await gatewayApi[kind]();
        toast.success(
          kind === "start" ? t("网关已启动") : kind === "stop" ? t("网关已停止") : t("网关已重启"),
        );
        window.dispatchEvent(new CustomEvent("app:refresh"));
      } catch (e) {
        toast.error(t("网关操作失败"), String(e));
      }
    },
    [t],
  );

  const items = useMemo<CommandItem[]>(() => {
    const list: CommandItem[] = [
      { id: "page-dashboard", label: t("仪表盘"), group: t("页面"), icon: <LayoutDashboard size={14} strokeWidth={2} />, run: () => go("/") },
      { id: "page-tokens", label: t("Token 消耗"), group: t("页面"), icon: <Coins size={14} strokeWidth={2} />, run: () => go("/tokens") },
      { id: "page-accounts", label: t("账号管理"), group: t("页面"), icon: <Users size={14} strokeWidth={2} />, run: () => go("/accounts") },
      { id: "page-keys", label: t("API 密钥"), group: t("页面"), icon: <KeyRound size={14} strokeWidth={2} />, run: () => go("/keys") },
      { id: "page-chat", label: t("聊天测试"), group: t("页面"), icon: <MessageSquare size={14} strokeWidth={2} />, run: () => go("/chat") },
      { id: "page-logs", label: t("日志查看"), group: t("页面"), icon: <ScrollText size={14} strokeWidth={2} />, run: () => go("/logs") },
      { id: "page-settings", label: t("系统设置"), group: t("页面"), icon: <Settings size={14} strokeWidth={2} />, run: () => go("/settings") },
      { id: "act-gw-start", label: t("启动网关"), group: t("动作"), icon: <Power size={14} strokeWidth={2} />, run: () => gatewayAction("start") },
      { id: "act-gw-stop", label: t("停止网关"), group: t("动作"), icon: <Power size={14} strokeWidth={2} />, run: () => gatewayAction("stop") },
      { id: "act-gw-restart", label: t("重启网关"), group: t("动作"), icon: <RotateCw size={14} strokeWidth={2} />, run: () => gatewayAction("restart") },
      { id: "act-refresh", label: t("刷新全部数据"), group: t("动作"), icon: <RefreshCw size={14} strokeWidth={2} />, run: () => { window.dispatchEvent(new CustomEvent("app:refresh")); setOpen(false); } },
      { id: "act-theme", label: theme === "dark" ? t("切换为浅色主题") : t("切换为深色主题"), group: t("动作"), icon: <Moon size={14} strokeWidth={2} />, run: () => { toggleTheme(); setOpen(false); } },
    ];
    for (const n of SETTINGS_NAV) {
      list.push({
        id: `set-nav-${n}`,
        label: t(n),
        hint: t("设置分区"),
        group: t("设置"),
        icon: <SlidersHorizontal size={14} strokeWidth={2} />,
        run: () => go(`/settings?nav=${encodeURIComponent(n)}`),
      });
    }
    for (const it of SETTING_ITEMS) {
      list.push({
        id: `set-item-${it.label}`,
        label: t(it.label),
        hint: t("设置 · {nav}", { nav: t(it.nav) }),
        group: t("设置"),
        icon: <SlidersHorizontal size={14} strokeWidth={2} />,
        run: () => go(`/settings?nav=${encodeURIComponent(it.nav)}`),
      });
    }
    for (const a of accounts) {
      list.push({
        id: `acct-${a.uid}`,
        label: a.nickname || a.uid,
        hint: t("账号 · {status}", { status: a.status }),
        group: t("账号"),
        icon: <Users size={14} strokeWidth={2} />,
        // 直达详情弹窗：已挂载走事件，新挂载由 Accounts 读取 sessionStorage 兜底
        run: () => {
          sessionStorage.setItem("app:open-account", a.uid);
          window.dispatchEvent(new CustomEvent("app:open-account", { detail: a.uid }));
          go("/accounts");
        },
      });
    }
    for (const k of keys) {
      list.push({
        id: `key-${k.id}`,
        label: k.name,
        hint: t("密钥"),
        group: t("密钥"),
        icon: <KeyRound size={14} strokeWidth={2} />,
        run: () => go("/keys"),
      });
    }
    // 状态相关动作（后端不可达时不展示，避免死条目）
    if (power) {
      list.push({
        id: "act-awake",
        label: power.active ? t("关闭防休眠") : t("开启防休眠"),
        group: t("动作"),
        icon: <Coffee size={14} strokeWidth={2} />,
        run: async () => {
          setOpen(false);
          try {
            const on = !power.active;
            await powerApi.setKeepAwake(on);
            toast.success(on ? t("防休眠已开启") : t("防休眠已关闭"));
          } catch (e) {
            toast.error(t("设置失败"), String(e));
          }
        },
      });
    }
    if (inject) {
      list.push({
        id: "act-inject",
        label: inject.running ? t("停止客户端注入") : t("启动客户端注入"),
        group: t("动作"),
        icon: <Blocks size={14} strokeWidth={2} />,
        run: async () => {
          setOpen(false);
          try {
            if (inject.running) {
              await injectApi.stop();
              toast.success(t("注入已停止"));
            } else {
              await injectApi.start();
              toast.success(t("注入已启动"), t("官方客户端窗口右下角会出现 🧩 面板按钮"));
            }
          } catch (e) {
            toast.error(t("操作失败"), String(e));
          }
        },
      });
    }
    list.push(
      {
        id: "act-update",
        label: t("检查更新"),
        group: t("动作"),
        icon: <ArrowUpCircle size={14} strokeWidth={2} />,
        run: async () => {
          setOpen(false);
          try {
            const u = await systemApi.checkUpdate();
            if (!u.supported) toast.info(t("当前构建不支持自动检查更新"), u.note);
            else if (u.hasUpdate) toast.info(t("发现新版本 {v}", { v: u.latest ?? "" }), t("可在「设置 → 关于」下载安装包"));
            else toast.success(t("已是最新版本"), u.current ? t("当前版本 {v}", { v: u.current }) : undefined);
          } catch (e) {
            toast.error(t("检查更新失败"), String(e));
          }
        },
      },
      {
        id: "act-authdir",
        label: t("打开凭证目录"),
        group: t("动作"),
        icon: <FolderOpen size={14} strokeWidth={2} />,
        run: async () => {
          setOpen(false);
          try {
            await accountsApi.openAuthDir();
          } catch (e) {
            toast.error(t("打开目录失败"), String(e));
          }
        },
      },
      {
        id: "act-backup",
        label: t("立即备份配置"),
        group: t("动作"),
        icon: <Archive size={14} strokeWidth={2} />,
        run: async () => {
          setOpen(false);
          try {
            const p = await configApi.backup();
            toast.success(t("备份完成"), p);
          } catch (e) {
            toast.error(t("备份失败"), String(e));
          }
        },
      },
    );
    // 搜索词转跳：面板输入直接去技能市场搜
    const kw = query.trim();
    if (kw) {
      list.push({
        id: "act-skill-search",
        label: t("在技能市场搜索「{kw}」", { kw }),
        hint: t("技能市场"),
        group: t("动作"),
        icon: <Puzzle size={14} strokeWidth={2} />,
        run: () => go(`/skills?q=${encodeURIComponent(kw)}`),
      });
    }
    return list;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [accounts, keys, go, gatewayAction, theme, toggleTheme, power, inject, query, lang, t]);

  // 模糊匹配：全子序列命中即算（按命中位置紧密度排序）
  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q) return items;
    const scored = items
      .map((it) => {
        const hay = (it.label + " " + (it.hint ?? "")).toLowerCase();
        let i = 0, score = 0, last = -1;
        for (const ch of q) {
          const idx = hay.indexOf(ch, i);
          if (idx < 0) return null;
          score += idx === last + 1 ? 0 : 1;
          last = idx;
          i = idx + 1;
        }
        return { it, score: score + (hay.startsWith(q) ? -1 : 0) };
      })
      .filter(Boolean) as { it: CommandItem; score: number }[];
    return scored.sort((a, b) => a.score - b.score).map((s) => s.it);
  }, [items, query]);

  // 打开时拉取账号/密钥（失败静默：浏览器预览下只展示页面与动作）
  useEffect(() => {
    if (!open) return;
    setQuery("");
    setActive(0);
    requestAnimationFrame(() => inputRef.current?.focus());
    const load = async () => {
      try {
        const res = await accountsApi.list({ page: 1, pageSize: 50 });
        setAccounts((res.items ?? []).map((a) => ({ uid: a.uid, nickname: a.nickname, status: a.status })));
      } catch { /* 浏览器预览无后端 */ }
      try {
        setKeys((await keysApi.list()) ?? []);
      } catch { /* 浏览器预览无后端 */ }
      try {
        setPower(await powerApi.status());
      } catch { /* 浏览器预览无后端 */ }
      try {
        setInject(await injectApi.status());
      } catch { /* 浏览器预览无后端 */ }
    };
    load();
  }, [open]);

  // 全局快捷键：⌘K / Ctrl+K 呼出、Esc 关闭
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") {
        e.preventDefault();
        setOpen((v) => !v);
      }
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, []);

  // 标题栏搜索框点击呼出（自定义事件，不能用合成 KeyboardEvent：
  // 派发在 window 上的事件不会进入 document 的监听器）
  useEffect(() => {
    const onOpen = () => setOpen(true);
    window.addEventListener("app:cmdk", onOpen);
    return () => window.removeEventListener("app:cmdk", onOpen);
  }, []);

  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setOpen(false);
      else if (e.key === "ArrowDown") { e.preventDefault(); setActive((a) => Math.min(a + 1, filtered.length - 1)); }
      else if (e.key === "ArrowUp") { e.preventDefault(); setActive((a) => Math.max(a - 1, 0)); }
      else if (e.key === "Enter" && filtered[active]) {
        e.preventDefault();
        filtered[active].run();
      }
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [open, filtered, active]);

  // 选中项滚动跟随
  useEffect(() => {
    listRef.current?.querySelector("[data-active='true']")?.scrollIntoView({ block: "nearest" });
  }, [active, filtered]);

  if (!open) return null;

  let lastGroup = "";
  return (
    <div className="cmdk-overlay" onMouseDown={() => setOpen(false)}>
      <div className="cmdk" onMouseDown={(e) => e.stopPropagation()}>
        <div className="cmdk-input-row">
          <Search size={15} strokeWidth={2.2} />
          <input
            ref={inputRef}
            className="cmdk-input"
            placeholder={t("搜索页面、账号、密钥、设置、动作…")}
            value={query}
            onChange={(e) => { setQuery(e.target.value); setActive(0); }}
          />
          <kbd>ESC</kbd>
        </div>
        <div className="cmdk-list" ref={listRef}>
          {filtered.length === 0 && <div className="cmdk-empty">{t("没有匹配的结果")}</div>}
          {filtered.map((it, i) => {
            const groupHead = it.group !== lastGroup ? (lastGroup = it.group, true) : false;
            return (
              <div key={it.id}>
                {groupHead && <div className="cmdk-group">{it.group}</div>}
                <button
                  className={`cmdk-item${i === active ? " active" : ""}`}
                  data-active={i === active}
                  onMouseEnter={() => setActive(i)}
                  onClick={() => it.run()}
                >
                  <span className="cmdk-icon">{it.icon}</span>
                  <span className="cmdk-label">{it.label}</span>
                  {it.hint && <span className="cmdk-hint">{it.hint}</span>}
                  {i === active && <CornerDownLeft size={13} strokeWidth={2} style={{ opacity: 0.6 }} />}
                </button>
              </div>
            );
          })}
        </div>
        <div className="cmdk-foot">
          <span><ArrowUpDown size={12} strokeWidth={2} /> {t("选择")}</span>
          <span><CornerDownLeft size={12} strokeWidth={2} /> {t("打开")}</span>
          <span>{t("⌘K 关闭")}</span>
        </div>
      </div>
    </div>
  );
}

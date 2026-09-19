import { Moon, Search, Sun, RefreshCw, Power, RotateCw, ChevronDown, Settings, CircleArrowUp, Loader2, PanelLeftClose, PanelLeftOpen, Languages } from "lucide-react";
import { useTheme } from "../../hooks/useTheme";
import { errText } from "../../hooks/useAsync";
import { useCallback, useEffect, useRef, useState } from "react";
import { useLocation, useNavigate } from "react-router-dom";
import { gatewayApi, systemApi } from "../../services/api";
import { toast } from "../common/Feedback";
import { onEvent } from "../../services/events";
import { setLang, useLang, useT } from "../../i18n";
import type { GatewayStatus } from "../../types";

interface Props {
  collapsed: boolean;
  onToggleCollapse: () => void;
}

export default function TitleBar({ collapsed, onToggleCollapse }: Props) {
  const [theme, toggleTheme] = useTheme();
  const [lang] = useLang();
  const t = useT();
  const [status, setStatus] = useState<GatewayStatus | null>(null);
  const [menuOpen, setMenuOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const [spinning, setSpinning] = useState(false);
  const [checking, setChecking] = useState(false);
  const menuRef = useRef<HTMLDivElement>(null);
  const go = useNavigate();
  const { pathname } = useLocation();

  const refresh = useCallback(async () => {
    const s = await gatewayApi.status();
    setStatus(s);
  }, []);

  useEffect(() => {
    refresh();
    const timer = setInterval(refresh, 5000);
    return () => clearInterval(timer);
  }, [refresh]);

  // 网关事件：实时刷新状态胶囊
  useEffect(() => {
    const off = onEvent("gateway:log", () => refresh());
    return off;
  }, [refresh]);

  // 点击空白关闭菜单
  useEffect(() => {
    const onDoc = (e: MouseEvent) => {
      if (menuOpen && menuRef.current && !menuRef.current.contains(e.target as Node)) setMenuOpen(false);
    };
    document.addEventListener("mousedown", onDoc);
    return () => document.removeEventListener("mousedown", onDoc);
  }, [menuOpen]);

  const act = async (kind: "start" | "stop" | "restart") => {
    setMenuOpen(false);
    setBusy(true);
    try {
      await gatewayApi[kind]();
      await refresh();
      toast.success(
        kind === "start" ? t("网关已启动") : kind === "stop" ? t("网关已停止") : t("网关已重启"),
        kind === "stop" ? undefined : t("监听 {addr}", { addr: status?.listen ?? ":7863" }),
      );
    } catch (e) {
      toast.error(t("操作失败"), String(e));
    } finally {
      setBusy(false);
    }
  };

  const doRefresh = () => {
    setSpinning(true);
    refresh().finally(() => setTimeout(() => setSpinning(false), 500));
    window.dispatchEvent(new CustomEvent("app:refresh"));
  };

  const checkUpdate = async () => {
    if (checking) return;
    setChecking(true);
    try {
      const res = await systemApi.checkUpdate();
      if (res.hasUpdate) toast.info(t("发现新版本 {v}", { v: res.latest ?? "" }), res.note);
      else toast.success(res.note || t("已是最新版本"));
    } catch (e) {
      toast.error(t("检查更新失败"), errText(e));
    } finally {
      setChecking(false);
    }
  };

  const isMac = navigator.platform.toLowerCase().includes("mac");

  // 折叠按钮：紧邻红绿灯，与双击标题折叠等效
  const foldBtn = (
    <button
      className="icon-btn tb-fold"
      data-tip={collapsed ? t("展开侧边栏") : t("折叠侧边栏")}
      onClick={onToggleCollapse}
    >
      {collapsed ? (
        <PanelLeftOpen size={15} strokeWidth={2} />
      ) : (
        <PanelLeftClose size={15} strokeWidth={2} />
      )}
    </button>
  );

  // 检查更新：占据原标题的位置（红绿灯/折叠按钮之后）
  const checkBtn = (
    <button
      className="tb-check"
      data-tip={t("检查更新")}
      disabled={checking}
      onClick={() => void checkUpdate()}
    >
      {checking ? (
        <Loader2 size={13} strokeWidth={2.2} className="spin" />
      ) : (
        <CircleArrowUp size={13} strokeWidth={2.2} />
      )}
      {t("检查更新")}
    </button>
  );

  return (
    <div className="titlebar">
      {isMac ? (
        <div className="tb-left">
          <div className="traffic">
            <span className="tl tl-r" />
            <span className="tl tl-y" />
            <span className="tl tl-g" />
          </div>
          {foldBtn}
          {checkBtn}
        </div>
      ) : (
        <div className="tb-left">
          <div style={{ width: 8 }} />
          {foldBtn}
          {checkBtn}
        </div>
      )}

      <div
        className="tb-search"
        onClick={() => window.dispatchEvent(new CustomEvent("app:cmdk"))}
      >
        <Search size={13} strokeWidth={2.2} />
        {t("搜索账号、密钥、日志…")}
        <kbd>⌘K</kbd>
      </div>

      <div className="tb-right">
        <div className="gw-menu-wrap" ref={menuRef}>
          <button
            className={`gw-pill${status?.running ? "" : " stopped"}${busy ? " busy" : ""}`}
            onClick={() => setMenuOpen((v) => !v)}
            data-tip={t("网关控制")}
          >
            <span className="dot" />
            {status?.running
              ? t("网关运行中 · {listen} · {uptime}", { listen: status.listen ?? ":7863", uptime: status.uptime })
              : t("网关已停止")}
            <ChevronDown size={12} strokeWidth={2.2} style={{ opacity: 0.65 }} />
          </button>
          {menuOpen && (
            <div className="pop-menu">
              <button className="pm-item" onClick={() => act("start")} disabled={status?.running}>
                <Power size={13} strokeWidth={2} /> {t("启动网关")}
              </button>
              <button className="pm-item" onClick={() => act("stop")} disabled={!status?.running}>
                <Power size={13} strokeWidth={2} /> {t("停止网关")}
              </button>
              <div className="pm-sep" />
              <button className="pm-item" onClick={() => act("restart")}>
                <RotateCw size={13} strokeWidth={2} /> {t("重启网关")}
              </button>
            </div>
          )}
        </div>
        <div className="lang-switch" data-tip={t("切换语言")}>
          <Languages size={13} strokeWidth={2} className="lang-globe" />
          <button
            className={`lang-opt${lang === "zh" ? " on" : ""}`}
            onClick={() => setLang("zh")}
          >
            中
          </button>
          <button
            className={`lang-opt${lang === "en" ? " on" : ""}`}
            onClick={() => setLang("en")}
          >
            EN
          </button>
        </div>
        <button
          className={`icon-btn${pathname === "/settings" ? " on" : ""}`}
          data-tip={t("系统设置")}
          onClick={() => go("/settings")}
        >
          <Settings size={15} strokeWidth={2} />
        </button>
        <button className="icon-btn" data-tip={t("刷新")} onClick={doRefresh}>
          <RefreshCw size={15} strokeWidth={2} className={spinning ? "spin" : ""} />
        </button>
        <button className="icon-btn" data-tip={theme === "dark" ? t("切换浅色主题") : t("切换深色主题")} onClick={toggleTheme}>
          {theme === "dark" ? (
            <Sun size={15} strokeWidth={2} />
          ) : (
            <Moon size={15} strokeWidth={2} />
          )}
        </button>
      </div>
    </div>
  );
}

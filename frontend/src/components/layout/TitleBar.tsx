import { Moon, Search, Sun, RefreshCw, Power, RotateCw, ChevronDown, Settings, CircleArrowUp, Loader2, PanelLeftClose, PanelLeftOpen, Languages, Puzzle, X, Minus, Plus, Download, Github } from "lucide-react";
import { Application, Window } from "@wailsio/runtime";
import { useTheme } from "../../hooks/useTheme";
import { getCloseBehavior } from "../../hooks/useCloseBehavior";
import { errText } from "../../hooks/useAsync";
import { useCallback, useEffect, useRef, useState } from "react";
import { useLocation, useNavigate } from "react-router-dom";
import { accountsApi, gatewayApi, injectApi, systemApi, IS_WAILS } from "../../services/api";
import { confirmDialog, toast } from "../common/Feedback";
import CloseAskDialog from "./CloseAskDialog";
import { EVENT, onEvent } from "../../services/events";
import { setLang, useLang, useT } from "../../i18n";
import type { GatewayStatus, InjectStatus, UpdateInfo } from "../../types";

interface Props {
  collapsed: boolean;
  onToggleCollapse: () => void;
}

// GitHub 项目主页（标题栏图标入口）
const REPO_URL = "https://github.com/HanoCode/BuddyBot";

export default function TitleBar({ collapsed, onToggleCollapse }: Props) {
  const [theme, toggleTheme] = useTheme();
  const [lang] = useLang();
  const t = useT();
  const [status, setStatus] = useState<GatewayStatus | null>(null);
  const [inj, setInj] = useState<InjectStatus | null>(null);
  const [injBusy, setInjBusy] = useState(false);
  const [menuOpen, setMenuOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const [spinning, setSpinning] = useState(false);
  const [checking, setChecking] = useState(false);
  // 更新状态：手动检查 / 后台定期扫描（update:available）都会写入，
  // 命中新版本时顶部按钮变为「立即更新」
  const [update, setUpdate] = useState<UpdateInfo | null>(null);
  const [updating, setUpdating] = useState(false);
  const [upStage, setUpStage] = useState<{ stage: string; percent: number } | null>(null);
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

  // 注入状态：低频轮询 + 后端事件即时同步
  const refreshInj = useCallback(async () => {
    try {
      setInj(await injectApi.status());
    } catch {
      /* 设置页已有完整错误展示，这里保持上次状态即可 */
    }
  }, []);
  useEffect(() => {
    void refreshInj();
    const timer = setInterval(refreshInj, 5000);
    const off = onEvent(EVENT.injectStatus, () => void refreshInj());
    return () => {
      clearInterval(timer);
      off();
    };
  }, [refreshInj]);

  // 注入快捷开关：与设置页完全相同的启停路径
  const toggleInject = async () => {
    if (injBusy) return;
    setInjBusy(true);
    try {
      if (inj?.running) {
        await injectApi.stop();
        toast.success(t("注入已停止"));
      } else {
        await injectApi.start();
        toast.success(t("注入已启动"), t("官方客户端窗口右下角会出现 🧩 面板按钮"));
      }
      await refreshInj();
    } catch (e) {
      toast.error(t("注入操作失败"), errText(e));
    } finally {
      setInjBusy(false);
    }
  };

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
      setUpdate(res.hasUpdate ? res : null);
      if (res.hasUpdate) toast.info(t("发现新版本 {v}", { v: res.latest ?? "" }), res.note);
      else toast.success(res.note || t("已是最新版本"));
    } catch (e) {
      toast.error(t("检查更新失败"), errText(e));
    } finally {
      setChecking(false);
    }
  };

  // 后台定期扫描命中新版本：顶部按钮同步变为「立即更新」
  useEffect(() => {
    return onEvent(EVENT.updateAvailable, (data) => {
      const info = data as UpdateInfo | null;
      if (info?.hasUpdate) setUpdate(info);
    });
  }, []);

  // 立即更新：下载（带进度）→ 校验后自动替换重启；与设置→关于同一后端链路
  const doUpdate = async () => {
    if (updating || !update?.downloadUrl) return;
    setUpdating(true);
    try {
      const path = await systemApi.downloadUpdate(update.downloadUrl);
      if (update.autoInstall) {
        toast.info(t("即将退出并安装更新"), t("安装完成后应用会自动重启"));
        await systemApi.installUpdate(path);
        return;
      }
      toast.success(t("安装包已下载"), path);
    } catch (e) {
      toast.error(update.autoInstall ? t("自动更新失败") : t("下载失败"), errText(e));
    } finally {
      setUpdating(false);
      setUpStage(null);
    }
  };

  // 更新下载进度：仅更新期间订阅，展示在标题栏按钮上
  useEffect(() => {
    if (!updating) return;
    return onEvent(EVENT.updateProgress, (data) => {
      const p = data as { stage?: string; percent?: number };
      if (typeof p?.stage !== "string") return;
      setUpStage({ stage: p.stage, percent: Math.min(100, Math.max(0, Math.round(p.percent ?? 0))) });
    });
  }, [updating]);

  const isMac = navigator.platform.toLowerCase().includes("mac");

  // ---------- 窗口控制（无边框窗口的自绘红绿灯） ----------
  const [closeAsk, setCloseAsk] = useState(false);

  // 退出：二次确认后停止网关/注入并退出（防误触）
  const quitAfterConfirm = async () => {
    const ok = await confirmDialog({
      title: t("退出 BuddyBot？"),
      desc: t("退出后将停止网关、注入与所有后台任务。"),
      danger: true,
      confirmText: t("退出程序"),
    });
    if (ok) Application.Quit();
  };

  // 关闭：按偏好执行 —— tray 直接隐藏；quit 弹窗确认后退出；ask 弹窗询问
  const handleClose = async () => {
    if (!IS_WAILS) return;
    const b = getCloseBehavior();
    if (b === "tray") {
      void Window.Hide();
      return;
    }
    if (b === "quit") {
      await quitAfterConfirm();
      return;
    }
    setCloseAsk(true);
  };

  const minimise = () => IS_WAILS && void Window.Minimise();
  const toggleMaximise = () => IS_WAILS && void Window.ToggleMaximise();

  const trafficLights = (
    <div className="traffic">
      <button className="tl tl-r" aria-label={t("关闭")} data-tip={t("关闭")} onClick={() => void handleClose()}>
        <X size={9} strokeWidth={2.6} />
      </button>
      <button className="tl tl-y" aria-label={t("最小化")} data-tip={t("最小化")} onClick={minimise}>
        <Minus size={9} strokeWidth={2.6} />
      </button>
      <button className="tl tl-g" aria-label={t("最大化")} data-tip={t("最大化")} onClick={toggleMaximise}>
        <Plus size={9} strokeWidth={2.6} />
      </button>
    </div>
  );

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

  // 检查更新：占据原标题的位置（红绿灯/折叠按钮之后）；
  // 发现新版本时变为高亮「立即更新」，点击走下载→校验→自动替换重启
  const checkBtn = update?.hasUpdate && update.downloadUrl ? (
    <button
      className="tb-check tb-update"
      data-tip={update.autoInstall ? t("下载、校验后自动替换并重启") : update.note}
      disabled={updating}
      onClick={() => void doUpdate()}
    >
      {updating ? <Loader2 size={13} strokeWidth={2.2} className="spin" /> : <Download size={13} strokeWidth={2.2} />}
      {updating
        ? upStage?.stage === "downloading"
          ? `${t("下载中")} ${upStage.percent}%`
          : t("更新中…")
        : update.autoInstall
          ? t("立即更新")
          : t("去下载新版本")}
    </button>
  ) : (
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
          {trafficLights}
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
        <button
          className={`gw-pill inj${inj?.running ? "" : " stopped"}${injBusy ? " busy" : ""}`}
          onClick={() => void toggleInject()}
          data-tip={inj?.running ? t("停止客户端注入") : t("启动客户端注入")}
        >
          {injBusy ? (
            <Loader2 size={12} strokeWidth={2.4} className="spin" />
          ) : (
            <Puzzle size={12} strokeWidth={2.4} />
          )}
          {inj?.running
            ? t("注入运行中 · {port}", { port: String(inj.port ?? "") })
            : t("注入已停止")}
        </button>
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
          className="icon-btn"
          data-tip={t("GitHub 项目主页")}
          onClick={() => void accountsApi.openURL(REPO_URL).catch(() => {})}
        >
          <Github size={15} strokeWidth={2} />
        </button>
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
        {!isMac && (
          // Windows/Linux 无边框窗口：窗口控制置于标题栏右侧（系统惯例）
          <div className="traffic win">
            <button className="tl tl-y" aria-label={t("最小化")} onClick={minimise}>
              <Minus size={10} strokeWidth={2.4} />
            </button>
            <button className="tl tl-g" aria-label={t("最大化")} onClick={toggleMaximise}>
              <Plus size={10} strokeWidth={2.4} />
            </button>
            <button className="tl tl-r" aria-label={t("关闭")} onClick={() => void handleClose()}>
              <X size={10} strokeWidth={2.4} />
            </button>
          </div>
        )}
      </div>

      <CloseAskDialog
        open={closeAsk}
        onCancel={() => setCloseAsk(false)}
        onDecide={(b) => {
          setCloseAsk(false);
          if (b === "tray") void Window.Hide();
          else void quitAfterConfirm(); // 直接走退出确认，不能回调 handleClose（会重读 ask 偏好再开同一个弹窗）
        }}
      />
    </div>
  );
}

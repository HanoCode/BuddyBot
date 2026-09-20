import { HashRouter, Route, Routes, useLocation } from "react-router-dom";
import { useEffect, useRef, useState } from "react";
import { Info } from "lucide-react";
import TitleBar from "./components/layout/TitleBar";
import Sidebar from "./components/layout/Sidebar";
import CommandPalette from "./components/layout/CommandPalette";
import { ToastHost, ConfirmHost, PromptHost } from "./components/common/FeedbackUI";
import { toast } from "./components/common/Feedback";
import ErrorBoundary from "./components/common/ErrorBoundary";
import { IS_WAILS } from "./services/api";
import { EVENT, onEvent } from "./services/events";
import { useT } from "./i18n";
import Dashboard from "./pages/Dashboard";
import Tokens from "./pages/Tokens";
import ClientTokens from "./pages/ClientTokens";
import Accounts from "./pages/Accounts";
import Agents from "./pages/Agents";
import Skills from "./pages/Skills";
import Prompts from "./pages/Prompts";
import Keys from "./pages/Keys";
import Chat from "./pages/Chat";
import Logs from "./pages/Logs";
import Settings from "./pages/Settings";

function AnimatedRoutes() {
  const location = useLocation();
  return (
    <div className="page-anim" key={location.pathname}>
      <Routes location={location}>
        <Route path="/" element={<Dashboard />} />
        <Route path="/tokens" element={<Tokens />} />
        <Route path="/client-tokens" element={<ClientTokens />} />
        <Route path="/accounts" element={<Accounts />} />
        <Route path="/agents" element={<Agents />} />
        <Route path="/skills" element={<Skills />} />
        <Route path="/prompts" element={<Prompts />} />
        <Route path="/keys" element={<Keys />} />
        <Route path="/chat" element={<Chat />} />
        <Route path="/logs" element={<Logs />} />
        <Route path="/settings" element={<Settings />} />
      </Routes>
    </div>
  );
}

export default function App() {
  const [collapsed, setCollapsed] = useState(false);
  const t = useT();
  // 已提醒过的新版本号（跨渲染保持，同一版本只 toast 一次）
  const lastNotifiedVersion = useRef<string>("");

  // 后端基础设施错误（落盘失败、配置损坏、推送失败等）全局弹出，绝不静默
  useEffect(() => {
    if (!IS_WAILS) return;
    return onEvent(EVENT.appError, (data) => {
      const d = (data ?? {}) as { scope?: string; message?: string };
      if (d.message) toast.error(t("系统错误"), d.message);
    });
  }, [t]);

  // 定期检查更新命中新版本：全局 toast 提醒（同一版本只提醒一次，避免每 6h 轰炸）
  useEffect(() => {
    if (!IS_WAILS) return;
    return onEvent(EVENT.updateAvailable, (data) => {
      const info = (data ?? {}) as { latest?: string; hasUpdate?: boolean };
      if (!info.hasUpdate || !info.latest || info.latest === lastNotifiedVersion.current) return;
      lastNotifiedVersion.current = info.latest;
      toast.info(t("发现新版本 {v}", { v: info.latest }), t("前往 设置 → 关于 可一键更新"));
    });
  }, [t]);

  return (
    <HashRouter>
      <div className={`shell${collapsed ? " collapsed" : ""}`}>
        <TitleBar
          collapsed={collapsed}
          onToggleCollapse={() => setCollapsed((c) => !c)}
        />
        <div className="app-body">
          <Sidebar />
          <main className="main">
            {!IS_WAILS && (
              <div className="env-banner">
                <Info size={15} strokeWidth={2} />
                <span>
                  {t("当前是浏览器预览：界面可用，但拿不到本地后端数据。请用")} <b>wails3 dev</b> {t("或安装包运行以查看真实数据。")}
                </span>
              </div>
            )}
            <ErrorBoundary>
              <AnimatedRoutes />
            </ErrorBoundary>
          </main>
        </div>
        <ToastHost />
        <ConfirmHost />
        <PromptHost />
        <CommandPalette />
      </div>
    </HashRouter>
  );
}

import { useCallback, useEffect, useState } from "react";
import { useLocation } from "react-router-dom";
import {
  LayoutDashboard,
  Users,
  KeyRound,
  MessageSquare,
  ScrollText,
  BarChart3,
  AppWindow,
  Bot,
  Puzzle,
  Server,
  Sparkles,
  Download,
  ArrowUpRight,
} from "lucide-react";
import { accountsApi, gatewayApi, systemApi } from "../../services/api";
import { useT } from "../../i18n";
import type { GatewayStatus } from "../../types";

interface NavItem {
  to: string;
  label: string;
  icon: React.ReactNode;
  badge?: string;
}

const APP_DOWNLOAD_URL = "https://www.workbuddy.cn/events/invite?inviteCode=7gqrh3nhlnfaxlcm";

export default function Sidebar() {
  const { pathname } = useLocation();
  const t = useT();
  const [status, setStatus] = useState<GatewayStatus | null>(null);
  const [accountCount, setAccountCount] = useState<number | null>(null);
  const [version, setVersion] = useState("");

  // 版本号：唯一来源是后端 appVersion（ldflags 可注入），取不到就不显示
  useEffect(() => {
    systemApi.info().then((s) => setVersion(s.version || "")).catch(() => undefined);
  }, []);

  const refresh = useCallback(async () => {
    try {
      const [s, accounts] = await Promise.all([gatewayApi.status(), accountsApi.list()]);
      setStatus(s);
      setAccountCount(accounts.total);
    } catch {
      // 后端不可用（例如浏览器预览）时如实显示为空，不编造数字
      setStatus(null);
      setAccountCount(null);
    }
  }, []);

  useEffect(() => {
    refresh();
    const timer = setInterval(refresh, 10000);
    const onRefresh = () => refresh();
    window.addEventListener("app:refresh", onRefresh);
    return () => {
      clearInterval(timer);
      window.removeEventListener("app:refresh", onRefresh);
    };
  }, [refresh]);

  const NAV_SECTIONS: { title: string; items: NavItem[] }[] = [
    {
      title: t("概览"),
      items: [
        { to: "/", label: t("仪表盘"), icon: <LayoutDashboard size={17} strokeWidth={1.9} /> },
        { to: "/tokens", label: t("Token 消耗"), icon: <BarChart3 size={17} strokeWidth={1.9} /> },
        { to: "/client-tokens", label: t("客户端消耗"), icon: <AppWindow size={17} strokeWidth={1.9} /> },
        {
          to: "/accounts",
          label: t("账号管理"),
          icon: <Users size={17} strokeWidth={1.9} />,
          badge: accountCount === null ? undefined : String(accountCount),
        },
        { to: "/keys", label: t("API 密钥"), icon: <KeyRound size={17} strokeWidth={1.9} /> },
      ],
    },
    {
      title: t("工具"),
      items: [
        { to: "/chat", label: t("聊天测试"), icon: <MessageSquare size={17} strokeWidth={1.9} /> },
        { to: "/agents", label: t("智能体接入"), icon: <Bot size={17} strokeWidth={1.9} /> },
        { to: "/skills", label: t("技能市场"), icon: <Puzzle size={17} strokeWidth={1.9} /> },
        { to: "/prompts", label: t("提效指令库"), icon: <Sparkles size={17} strokeWidth={1.9} /> },
        { to: "/logs", label: t("日志查看"), icon: <ScrollText size={17} strokeWidth={1.9} /> },
      ],
    },
  ];

  const healthy = status?.healthy ?? 0;
  const total = status?.total ?? 0;
  const pct = total ? Math.round((healthy / total) * 100) : 0;

  const openDownload = () => {
    accountsApi.openURL(APP_DOWNLOAD_URL).catch(() => {});
  };

  return (
    <aside className="sidebar">
      <div className="brand">
        <img className="logo-mark" src="/appicon.png" alt="BuddyBot" draggable={false} />
        <div className="brand-text">
          <b>
            BuddyBot
            {version && <span className="tb-ver">v{version}</span>}
          </b>
          <small>{t("WorkBuddy 控制台")}</small>
        </div>
      </div>

      {NAV_SECTIONS.map((sec) => (
        <div className="nav-section" key={sec.title}>
          <div className="nav-label">
            <span>{sec.title}</span>
          </div>
          {sec.items.map((item) => (
            <a
              key={item.to}
              href={`#${item.to}`}
              data-tip={item.label}
              data-tip-pos="right"
              className={`nav-item${pathname === item.to ? " active" : ""}`}
            >
              {item.icon}
              <span>{item.label}</span>
              {item.badge && <div className="nav-badge">{item.badge}</div>}
            </a>
          ))}
        </div>
      ))}

      <div className="side-foot">
        <button
          type="button"
          className="dl-card"
          data-tip={t("应用下载 WorkBuddy")}
          data-tip-pos="right"
          onClick={openDownload}
        >
          <span className="dl-ico">
            <Download size={15} strokeWidth={2.1} />
          </span>
          <span className="dl-text">
            <b>{t("应用下载 WorkBuddy")}</b>
            <small>{t("获取官方客户端")}</small>
          </span>
          <ArrowUpRight className="dl-arrow" size={14} strokeWidth={2.2} />
        </button>
        <div
          className="gw-card"
          data-tip={`${t("网关服务")} · ${status?.running ? t("运行中") : t("已停止")}${status?.listen ? ` · ${t("监听 {addr}", { addr: status.listen })}` : ""}`}
          data-tip-pos="right"
        >
          {/* 折叠态：仅显示图标 + 状态圆点 */}
          <span className="gw-mini">
            <Server size={16} strokeWidth={2} />
            <i className={`gw-dot${status?.running ? "" : " off"}`} />
          </span>
          <div className="gw-full">
            <div className="row">
              <span className="t">{t("网关服务")}</span>
              <span className={`v${status?.running ? "" : " off"}`}>
                {status?.running ? t("运行中") : t("已停止")}
              </span>
            </div>
            <div className="row" style={{ marginTop: 5 }}>
              <span className="t" style={{ fontWeight: 500 }}>{t("监听 {addr}", { addr: status?.listen ?? ":7863" })}</span>
              <span className="t">{t("{n} 账号", { n: total })}</span>
            </div>
            <div className="bar">
              <i style={{ width: `${pct}%` }} />
            </div>
          </div>
        </div>
      </div>
    </aside>
  );
}

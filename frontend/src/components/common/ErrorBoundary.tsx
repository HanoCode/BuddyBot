import { Component, type ReactNode } from "react";
import { RefreshCw, TriangleAlert } from "lucide-react";
import { t } from "../../i18n";

interface Props {
  children: ReactNode;
}

interface State {
  error: Error | null;
}

/** 根级错误边界：任一页面渲染崩溃时给出可恢复的兜底界面，而不是白屏 */
export default class ErrorBoundary extends Component<Props, State> {
  state: State = { error: null };

  static getDerivedStateFromError(error: Error): State {
    return { error };
  }

  componentDidCatch(error: Error, info: unknown) {
    console.error("[ErrorBoundary] 页面渲染崩溃", error, info);
  }

  render() {
    if (!this.state.error) return this.props.children;
    return (
      <div style={{
        display: "flex", flexDirection: "column", alignItems: "center", justifyContent: "center",
        gap: 14, padding: 48, height: "60vh", color: "var(--text-2)",
      }}>
        <TriangleAlert size={36} strokeWidth={1.6} color="var(--amber, #f59e0b)" />
        <div style={{ fontSize: 15, fontWeight: 600, color: "var(--text-1)" }}>{t("页面渲染出错")}</div>
        <div style={{ fontSize: 12.5, fontFamily: "var(--mono)", maxWidth: 560, wordBreak: "break-all", opacity: 0.8 }}>
          {this.state.error.message}
        </div>
        <button className="btn btn-soft" onClick={() => this.setState({ error: null })}>
          <RefreshCw size={13} strokeWidth={2} /> {t("重试")}
        </button>
      </div>
    );
  }
}

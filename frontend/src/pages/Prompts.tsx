import { useMemo, useState } from "react";
import {
  Check, Copy, FileText, Lightbulb, Search, Sparkles, Target, X,
} from "lucide-react";
import { createPortal } from "react-dom";
import rawPrompts from "../data/prompts.json";
import { toast } from "../components/common/Feedback";
import { useT } from "../i18n";

interface PromptItem {
  id: number;
  title: string;
  category: string;
  scene: string;
  prompt: string;
  expected_output: string;
  tip: string;
}

const ITEMS = rawPrompts as PromptItem[];

/** 分类中文标签（顺序即 Tab 顺序，与 JSON 中 10 个分类一一对应） */
const CATEGORY_ZH: Record<string, string> = {
  "deep-research": "深度研究",
  "data-analysis": "数据分析",
  "doc-writing": "文档写作",
  "ppt": "演示文稿",
  "meeting": "会议沟通",
  "email-calendar": "邮件日程",
  "project-mgmt": "项目管理",
  "dev": "研发技术",
  "growth": "增长运营",
  "admin-hr-finance": "行政人事财务",
};

const CATS = Object.keys(CATEGORY_ZH);

/** 详情弹窗：完整指令 + 预期产出 + 使用技巧 + 一键复制 */
function DetailDialog({ item, onClose }: { item: PromptItem; onClose: () => void }) {
  const [copied, setCopied] = useState(false);
  const t = useT();

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(item.prompt);
      setCopied(true);
      toast.success(t("已复制到剪贴板"), item.title);
      setTimeout(() => setCopied(false), 1500);
    } catch {
      toast.error(t("复制失败"), t("浏览器未授权剪贴板访问，请手动选择复制"));
    }
  };

  return createPortal(
    <div className="overlay" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div className="dialog" style={{ maxWidth: 640 }}>
        <div className="dlg-head">
          <div>
            <span className="badge b-indigo" style={{ marginBottom: 6 }}>{t(CATEGORY_ZH[item.category] ?? item.category)}</span>
            <h3>{item.title}</h3>
            <p>{item.scene}</p>
          </div>
          <button className="icon-btn" onClick={onClose}><X size={14} strokeWidth={2.2} /></button>
        </div>
        <div className="dlg-body">
          <div className="pt-block">
            <div className="pt-block-h"><Sparkles size={12} strokeWidth={2.2} />{t("指令内容")}</div>
            <pre className="pt-prompt">{item.prompt}</pre>
          </div>
          <div className="pt-block">
            <div className="pt-block-h"><Target size={12} strokeWidth={2.2} />{t("预期产出")}</div>
            <div className="pt-text">{item.expected_output}</div>
          </div>
          {item.tip && (
            <div className="pt-block">
              <div className="pt-block-h"><Lightbulb size={12} strokeWidth={2.2} />{t("使用技巧")}</div>
              <div className="pt-text">{item.tip}</div>
            </div>
          )}
        </div>
        <div className="dlg-foot">
          <button className="btn btn-ghost" onClick={onClose}>{t("关闭")}</button>
          <button className="btn btn-primary" onClick={() => void copy()}>
            {copied ? <Check size={13} strokeWidth={2.4} /> : <Copy size={13} strokeWidth={2} />}
            {copied ? t("已复制") : t("复制指令")}
          </button>
        </div>
      </div>
    </div>,
    document.body,
  );
}

export default function Prompts() {
  const t = useT();
  const [cat, setCat] = useState<string>("all");
  const [query, setQuery] = useState("");
  const [detail, setDetail] = useState<PromptItem | null>(null);

  // 分类 + 关键词过滤（标题 / 场景 / 指令全文 / 预期产出）
  const list = useMemo(() => {
    const q = query.trim().toLowerCase();
    return ITEMS.filter((it) => {
      if (cat !== "all" && it.category !== cat) return false;
      if (!q) return true;
      return (
        it.title.toLowerCase().includes(q) ||
        it.scene.toLowerCase().includes(q) ||
        it.prompt.toLowerCase().includes(q) ||
        it.expected_output.toLowerCase().includes(q)
      );
    });
  }, [cat, query]);

  return (
    <section className="page">
      <div className="page-head">
        <div>
          <h1>{t("提效指令库")}</h1>
          <p>{t("100 条职场提效 Prompt · 按场景分类，点击卡片查看完整指令并一键复制给 AI")}</p>
        </div>
        <div className="head-actions">
          <div className="search-box" style={{ width: 300, flex: "none", maxWidth: "none" }}>
            <Search size={13} strokeWidth={2.2} />
            <input
              className="search-input"
              placeholder={t("搜索标题、场景或指令关键词…")}
              value={query}
              onChange={(e) => setQuery(e.target.value)}
            />
          </div>
        </div>
      </div>

      {/* 分类 Tab */}
      <div className="flex" style={{ gap: 10, alignItems: "center", marginBottom: 14 }}>
        <div className="seg">
          <button className={cat === "all" ? "on" : ""} onClick={() => setCat("all")}>
            {t("全部 · {n}", { n: ITEMS.length })}
          </button>
          {CATS.map((c) => (
            <button key={c} className={cat === c ? "on" : ""} onClick={() => setCat(c)}>
              {t(CATEGORY_ZH[c])}
            </button>
          ))}
        </div>
      </div>

      {/* 指令卡片网格 */}
      {list.length === 0 ? (
        <div className="state-block" style={{ justifyContent: "center" }}>
          <FileText size={16} strokeWidth={2} />
          <div>
            <div className="sb-title">{t("没有匹配的指令")}</div>
            <div className="sb-desc">{t("换个关键词试试，或切到其他分类浏览")}</div>
          </div>
        </div>
      ) : (
        <div className="pt-grid">
          {list.map((it) => (
            <button key={it.id} className="pt-card" onClick={() => setDetail(it)}>
              <div className="pt-card-top">
                <span className="badge b-indigo">{CATEGORY_ZH[it.category] ?? it.category}</span>
                <span className="pt-num">#{String(it.id).padStart(3, "0")}</span>
              </div>
              <h3>{it.title}</h3>
              <p className="pt-scene">{it.scene}</p>
              <p className="pt-expect">{it.expected_output}</p>
            </button>
          ))}
        </div>
      )}

      {detail && <DetailDialog item={detail} onClose={() => setDetail(null)} />}
    </section>
  );
}

import { useEffect, useMemo, useRef, useState } from "react";
import { ChevronDown, Cpu, Plus, SendHorizontal, Square, Timer, Trash2, TriangleAlert, X } from "lucide-react";
import { marked } from "marked";
import DOMPurify from "dompurify";
import { chatApi, IS_WAILS, NO_BACKEND_MESSAGE, modelsApi } from "../services/api";
import { errText, useAsync } from "../hooks/useAsync";
import { EVENT, onEvent } from "../services/events";
import { confirmDialog, toast } from "../components/common/Feedback";
import type { ChatMessage, ModelInfo } from "../types";
import { useT } from "../i18n";

interface Msg {
  role: "user" | "assistant";
  content: string;
  reasoning?: string;
  meta?: string;
  error?: string;
}

// assistant 气泡的 markdown 渲染（流式期间每个分片也会重渲染，marked 开销可接受）
marked.setOptions({ breaks: true, gfm: true });

function Markdown({ text }: { text: string }) {
  const html = useMemo(
    () => DOMPurify.sanitize(marked.parse(text, { async: false })),
    [text],
  );
  return <div className="md" dangerouslySetInnerHTML={{ __html: html }} />;
}

const SESSION_PREFIX = "wbui-";
const QUICK_PROMPTS_KEY = "wb-quick-prompts";

function loadQuickPrompts(): string[] {
  try {
    const raw = localStorage.getItem(QUICK_PROMPTS_KEY);
    const arr = raw ? JSON.parse(raw) : [];
    return Array.isArray(arr) ? arr.filter((x) => typeof x === "string") : [];
  } catch {
    return [];
  }
}

export default function Chat() {
  const t = useT();
  const [model, setModel] = useState("");
  const [modelOpen, setModelOpen] = useState(false);
  const [modelFilter, setModelFilter] = useState("");
  const [stream, setStream] = useState(true);
  const [sticky, setSticky] = useState(true);
  const [showReasoning, setShowReasoning] = useState(true);
  const [system, setSystem] = useState("");
  const [temperature, setTemperature] = useState(0.7);
  const [maxTokens, setMaxTokens] = useState(0);
  const [input, setInput] = useState("");
  const [messages, setMessages] = useState<Msg[]>([]);
  const [streaming, setStreaming] = useState(false);
  const [usage, setUsage] = useState<{ tokens: number; first: number; total: number } | null>(null);
  const [sessionId] = useState(() => SESSION_PREFIX + Math.random().toString(16).slice(2, 10));

  // 快捷提示词（本地持久化，对齐 WorkDaddy 的可移植部分：暂存常用语句，点击填入输入框）
  const [quick, setQuick] = useState<string[]>(loadQuickPrompts);
  const [quickDraft, setQuickDraft] = useState("");
  useEffect(() => {
    try {
      localStorage.setItem(QUICK_PROMPTS_KEY, JSON.stringify(quick));
    } catch { /* 隐私模式等场景忽略 */ }
  }, [quick]);

  const addQuick = () => {
    const t = quickDraft.trim();
    if (!t) return;
    if (!quick.includes(t)) setQuick((q) => [...q, t]);
    setQuickDraft("");
  };

  const scrollRef = useRef<HTMLDivElement>(null);
  const bufferRef = useRef("");
  const reasoningRef = useRef("");

  const models = useAsync<ModelInfo[]>(() => modelsApi.list(), []);

  // 默认选第一个可用模型（优先本机用过的）
  useEffect(() => {
    if (!model && models.data?.length) setModel(models.data[0].id);
  }, [models.data, model]);

  useEffect(() => {
    const offToken = onEvent(EVENT.chatToken, (data) => {
      bufferRef.current += String(data ?? "");
      setMessages((m) => patchLast(m, { content: bufferRef.current }));
    });
    const offReason = onEvent(EVENT.chatReasoning, (data) => {
      reasoningRef.current += String(data ?? "");
      setMessages((m) => patchLast(m, { reasoning: reasoningRef.current }));
    });
    const offDone = onEvent(EVENT.chatDone, (data) => {
      const d = data as { tokens?: number; latency?: number; firstLatency?: number } | undefined;
      setUsage((u) => ({
        tokens: d?.tokens ?? u?.tokens ?? 0,
        first: d?.firstLatency ?? u?.first ?? 0,
        total: d?.latency ?? u?.total ?? 0,
      }));
    });
    return () => {
      offToken();
      offReason();
      offDone();
    };
  }, []);

  useEffect(() => {
    scrollRef.current?.scrollTo({ top: scrollRef.current.scrollHeight, behavior: "smooth" });
  }, [messages, streaming]);

  const filteredModels = useMemo(() => {
    const kw = modelFilter.trim().toLowerCase();
    const list = models.data ?? [];
    return kw ? list.filter((m) => m.id.toLowerCase().includes(kw)) : list;
  }, [models.data, modelFilter]);

  const send = async () => {
    const text = input.trim();
    if (!text || streaming) return;
    if (!model) {
      toast.warn(t("请先选择模型"));
      return;
    }
    setInput("");
    setUsage(null);

    const history: ChatMessage[] = [
      ...messages.filter((m) => !m.error).map((m) => ({ role: m.role, content: m.content })),
      { role: "user", content: text },
    ];
    setMessages((m) => [...m, { role: "user", content: text }, { role: "assistant", content: "" }]);
    bufferRef.current = "";
    reasoningRef.current = "";
    setStreaming(true);

    try {
      const res = await chatApi.send({
        model,
        system: system.trim() || undefined,
        messages: history,
        temperature,
        maxTokens: maxTokens || undefined,
        stream,
        sessionId: sticky ? sessionId : undefined,
      });
      if (res.aborted) toast.info(t("已停止生成"), t("已生成的部分内容已保留"));
      // 非流式（或流式分片已完整到达）时以最终结果为准
      const abortedTag = res.aborted ? t(" · 已停止") : "";
      const meta = t("{tokens} tok（输入 {input} / 输出 {output}）· 首字节 {first}ms · 总 {total}ms", {
        tokens: res.tokens.toLocaleString(),
        input: res.inputTokens,
        output: res.outputTokens,
        first: Math.round(res.latencyFirst),
        total: Math.round(res.latencyTotal),
      }) + abortedTag;
      setMessages((m) => {
        const copy = [...m];
        const last = copy[copy.length - 1];
        if (last?.role === "assistant") {
          copy[copy.length - 1] = {
            ...last,
            content: res.content,
            reasoning: res.reasoning || last.reasoning,
            meta,
          };
        }
        return copy;
      });
      setUsage(res.aborted ? null : { tokens: res.tokens, first: res.latencyFirst, total: res.latencyTotal });
    } catch (e) {
      const msg = errText(e);
      toast.error(t("请求未成功"), msg);
      setMessages((m) => {
        const copy = [...m];
        const last = copy[copy.length - 1];
        if (last?.role === "assistant" && !last.content) {
          copy[copy.length - 1] = { ...last, error: msg };
        } else {
          copy.push({ role: "assistant", content: "", error: msg });
        }
        return copy;
      });
    } finally {
      setStreaming(false);
    }
  };

  const currentModel = models.data?.find((m) => m.id === model);

  return (
    <section className="page">
      <div className="page-head">
        <div>
          <h1>{t("聊天测试")}</h1>
          <p>
            {t("请求真实发往本机网关 {url} · 走完整鉴权 / 配额 / 账号路由 / 记账链路", { url: "/v1/chat/completions" })}
          </p>
        </div>
        <div className="head-actions">
          {usage && (
            <span className="badge b-indigo">
              {t("{tokens} tok · 首字节 {first}ms · 总 {total}ms", {
                tokens: usage.tokens.toLocaleString(),
                first: Math.round(usage.first),
                total: Math.round(usage.total),
              })}
            </span>
          )}
          <button
            className="btn btn-ghost"
            onClick={async () => {
              if (!messages.length) return;
              const ok = await confirmDialog({
                title: t("清空会话"),
                desc: t("将清除当前全部对话记录，不可恢复。"),
                confirmText: t("清空"),
              });
              if (ok) {
                setMessages([]);
                setUsage(null);
              }
            }}
          >
            <Trash2 size={13} strokeWidth={2} /> {t("清空会话")}
          </button>
        </div>
      </div>

      {!IS_WAILS && (
        <div className="env-banner">
          <TriangleAlert size={14} strokeWidth={2.2} />
          {NO_BACKEND_MESSAGE}
        </div>
      )}

      <div className="chat-wrap">
        <div className="card chat-side">
          <div className="card-b">
            <div className="field">
              <label>{t("模型")}{currentModel ? t(" · 上下文 {n}K", { n: (currentModel.contextLength / 1000).toFixed(0) }) : ""}</label>
              <div className="model-select" onClick={() => setModelOpen((v) => !v)}>
                <span>{model || (models.loading ? t("加载中…") : t("无可用模型"))}</span>
                <ChevronDown size={13} strokeWidth={2.2} />
                {modelOpen && (
                  <div className="pop-menu model-menu" onClick={(e) => e.stopPropagation()}>
                    <input
                      className="tb-search"
                      style={{ margin: "4px 6px", width: "auto" }}
                      placeholder={t("搜索模型")}
                      value={modelFilter}
                      onChange={(e) => setModelFilter(e.target.value)}
                    />
                    {models.error && <div className="pm-item" style={{ color: "var(--red)" }}>{models.error}</div>}
                    {filteredModels.map((m) => (
                      <button
                        key={m.id}
                        className={`pm-item${m.id === model ? " on" : ""}`}
                        onClick={() => { setModel(m.id); setModelOpen(false); }}
                        data-tip={t("上下文 {n} · 最大输出 {m}", { n: m.contextLength, m: m.maxOutputTokens || t("未知") }) + (m.observed ? t(" · 本机已用 {n} 次", { n: m.requests }) : "")}
                      >
                        <span className="pm-label">{m.id}</span>
                        {m.observed && <span className="pm-used">{t("已用")}</span>}
                      </button>
                    ))}
                    {filteredModels.length === 0 && <div className="pm-item muted">{t("没有匹配的模型")}</div>}
                  </div>
                )}
              </div>
            </div>

            <div className="field">
              <label>System Prompt</label>
              <textarea className="input" rows={3} value={system} placeholder={t("留空表示不发送 system 消息")}
                onChange={(e) => setSystem(e.target.value)} />
            </div>

            <div className="field">
              <label>
                Temperature <b className="mono">{temperature.toFixed(1)}</b>
              </label>
              <input type="range" min={0} max={2} step={0.1} value={temperature}
                onChange={(e) => setTemperature(Number(e.target.value))} className="range" />
            </div>

            <div className="field">
              <label>{t("快捷提示词")} <span className="hint-inline">{t("点击填入输入框")}</span></label>
              {quick.length > 0 && (
                <div className="flex" style={{ gap: 6, flexWrap: "wrap", marginBottom: 8 }}>
                  {quick.map((q, i) => (
                    <span className="chip" key={i} style={{ cursor: "pointer", maxWidth: "100%" }}
                      data-tip={q} onClick={() => setInput(q)}>
                      {q.length > 16 ? `${q.slice(0, 16)}…` : q}
                      <button className="chip-x" data-tip={t("删除")}
                        onClick={(e) => { e.stopPropagation(); setQuick(quick.filter((_, j) => j !== i)); }}>
                        <X size={10} strokeWidth={3} />
                      </button>
                    </span>
                  ))}
                </div>
              )}
              <div className="flex" style={{ gap: 6 }}>
                <input
                  className="input"
                  style={{ fontSize: 12 }}
                  placeholder={t("新增一条快捷提示词…")}
                  value={quickDraft}
                  onChange={(e) => setQuickDraft(e.target.value)}
                  onKeyDown={(e) => e.key === "Enter" && (e.preventDefault(), addQuick())}
                />
                <button className="btn btn-ghost sm" disabled={!quickDraft.trim()} onClick={addQuick}>
                  <Plus size={12} strokeWidth={2.4} /> {t("添加")}
                </button>
              </div>
            </div>

            <div className="field">
              <label>max_tokens <span className="hint-inline">{t("0 = 不限制")}</span></label>
              <input className="input" type="number" value={maxTokens}
                onChange={(e) => setMaxTokens(Math.max(0, Number(e.target.value) || 0))} />
            </div>

            <div className="sw-row">
              <div><div className="t">{t("流式输出")}</div><div className="d">{t("SSE 逐段推送")}</div></div>
              <button className={`switch${stream ? " on" : ""}`} onClick={() => setStream(!stream)} />
            </div>
            <div className="sw-row">
              <div><div className="t">{t("会话粘性")}</div><div className="d">{t("携带 session_id")}</div></div>
              <button className={`switch${sticky ? " on" : ""}`} onClick={() => setSticky(!sticky)} />
            </div>
            <div className="sw-row">
              <div><div className="t">{t("推理过程")}</div><div className="d">{t("展示 reasoning_content")}</div></div>
              <button className={`switch${showReasoning ? " on" : ""}`} onClick={() => setShowReasoning(!showReasoning)} />
            </div>
            <div className="flex" style={{ justifyContent: "space-between", marginTop: 10 }}>
              <span className="muted" style={{ fontSize: 11.5 }}>{t("会话 ID")}</span>
              <span className="mono" style={{ fontSize: 11 }}>{sticky ? sessionId : t("未启用")}</span>
            </div>
          </div>
        </div>

        <div className="card chat-main">
          <div className="chat-scroll" ref={scrollRef}>
            {messages.map((m, i) => (
              <div className={`msg ${m.role}`} key={i}>
                <span className="m-av"
                  style={{
                    background: m.role === "user"
                      ? "linear-gradient(135deg,#6366F1,#8B5CF6)"
                      : "linear-gradient(135deg,#FF8A5C,#FF5A3C)",
                  }}>
                  {m.role === "user" ? t("你") : "WB"}
                </span>
                <div>
                  {m.reasoning !== undefined && (
                    <div className="reasoning">
                      <div className="r-head"><Cpu size={12} strokeWidth={2} /> {t("推理过程（reasoning_content）")}</div>
                      <div className="r-body">{showReasoning ? (m.reasoning || t("（等待推理输出）")) : t("（已折叠）")}</div>
                    </div>
                  )}
                  {m.error ? (
                    <div className="bubble" style={{ color: "var(--red)", background: "var(--red-soft)" }}>
                      {m.error}
                    </div>
                  ) : m.role === "assistant" ? (
                    <div className="bubble">
                      {m.content
                        ? <Markdown text={m.content} />
                        : (streaming && i === messages.length - 1 ? <span className="typing"><i /><i /><i /></span> : "")}
                    </div>
                  ) : (
                    <div className="bubble">{m.content}</div>
                  )}
                  {m.meta && (
                    <div className="meta">
                      <span className="badge b-indigo">
                        <Timer size={11} strokeWidth={2} /> {m.meta}
                      </span>
                    </div>
                  )}
                </div>
              </div>
            ))}
            {messages.length === 0 && (
              <div className="chat-empty">
                <SendHorizontal size={26} strokeWidth={1.6} />
                <div>{t("输入消息，请求会真实经过本机网关")}</div>
                <small>
                  {t("网关需处于运行状态且账号池里至少有一个可用账号；否则会返回明确的错误（503 / 401 / 429）。")}
                </small>
              </div>
            )}
          </div>
          <div className="chat-input-bar">
            <div className="chat-box">
              <textarea
                rows={2}
                placeholder={t("输入消息，Enter 发送 · Shift+Enter 换行")}
                value={input}
                onChange={(e) => setInput(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === "Enter" && !e.shiftKey) {
                    e.preventDefault();
                    void send();
                  }
                }}
              />
              <div className="bar">
                <span className="hint">
                  {model || t("未选模型")} · temp {temperature.toFixed(1)} · {stream ? t("流式") : t("非流式")} · {sticky ? t("粘性开") : t("无粘性")}
                </span>
                <button
                  className="send-btn"
                  disabled={false}
                  onClick={() => (streaming ? void chatApi.abort() : void send())}
                  data-tip={streaming ? t("停止生成") : t("发送")}
                >
                  {streaming ? <Square size={14} strokeWidth={2.4} /> : <SendHorizontal size={15} strokeWidth={2.2} />}
                </button>
              </div>
            </div>
          </div>
        </div>
      </div>
    </section>
  );
}

function patchLast(list: Msg[], patch: Partial<Msg>): Msg[] {
  if (!list.length) return list;
  const copy = [...list];
  const last = copy[copy.length - 1];
  if (last.role !== "assistant") return list;
  copy[copy.length - 1] = { ...last, ...patch, error: undefined };
  return copy;
}

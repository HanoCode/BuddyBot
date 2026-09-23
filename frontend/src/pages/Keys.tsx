import { useCallback, useEffect, useMemo, useState } from "react";
import { createPortal } from "react-dom";
import {
  BarChart3, Check, ChevronDown, Copy, KeyRound, Pencil, Loader2, Trash2, X,
} from "lucide-react";
import { gatewayApi, keysApi, modelsApi } from "../services/api";
import { broadcastRefresh, errText, useAsync } from "../hooks/useAsync";
import { confirmDialog, toast } from "../components/common/Feedback";
import { EmptyRow, ErrorBlock, SkeletonRows, EmptyBlock } from "../components/common/StateBlock";
import { useT } from "../i18n";
import type { CreateKeyParams, GatewayStatus, KeyUsage, KeyView, ModelInfo } from "../types";

interface FormState {
  id?: string;
  name: string;
  expires: string;
  models: string[];
  ipWhitelist: string;
  tokenQuota: string;
  creditQuota: string;
  maxIps: string;
  disabled: boolean;
}

const EMPTY: FormState = {
  name: "", expires: "2099-12-31", models: ["*"], ipWhitelist: "",
  tokenQuota: "0", creditQuota: "0", maxIps: "0", disabled: false,
};

export default function Keys() {
  const [form, setForm] = useState<FormState | null>(null);
  const [copied, setCopied] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [usage, setUsage] = useState<{ key: KeyView; data: KeyUsage } | null>(null);

  const t = useT();

  const keys = useAsync<KeyView[]>(() => keysApi.list(), []);
  const models = useAsync<ModelInfo[]>(() => modelsApi.list(), []);
  const gw = useAsync<GatewayStatus>(() => gatewayApi.status(), []);
  const [keyId, setKeyId] = useState("");
  const [connOpen, setConnOpen] = useState(true);

  const load = useCallback(async () => {
    await keys.reload();
  }, [keys.reload]);

  useEffect(() => {
    const onRefresh = () => void load();
    window.addEventListener("app:refresh", onRefresh);
    return () => window.removeEventListener("app:refresh", onRefresh);
  }, [load]);

  const copy = async (text: string, tag: string) => {
    try {
      await navigator.clipboard.writeText(text);
      setCopied(tag);
      toast.success(t("已复制到剪贴板"));
      setTimeout(() => setCopied(null), 1500);
    } catch {
      toast.error(t("复制失败"), t("当前环境不支持剪贴板写入"));
    }
  };

  const submit = async () => {
    if (!form) return;
    if (!form.name.trim()) {
      toast.warn(t("请填写密钥名称"));
      return;
    }
    setSaving(true);
    try {
      const params: CreateKeyParams = {
        name: form.name.trim(),
        expires: form.expires,
        models: form.models.length ? form.models : ["*"],
        ipWhitelist: form.ipWhitelist.split(/[,\s]+/).map((v) => v.trim()).filter(Boolean),
        tokenQuota: Number(form.tokenQuota) || 0,
        creditQuota: Number(form.creditQuota) || 0,
        maxIps: Number(form.maxIps) || 0,
      };
      if (form.id) {
        await keysApi.update(form.id, { ...params, disabled: form.disabled });
        toast.success(t("密钥已更新"), form.name);
        setForm(null);
      } else {
        await keysApi.create(params);
        setForm(null);
        toast.success(t("密钥已创建"), t("完整密钥可在列表中随时复制"));
      }
      await load();
      broadcastRefresh();
    } catch (e) {
      toast.error(t("保存失败"), errText(e));
    } finally {
      setSaving(false);
    }
  };

  const remove = async (k: KeyView) => {
    const ok = await confirmDialog({
      title: t("删除密钥"),
      desc: t("删除「{name}」后，使用该密钥的客户端将立即无法通过网关鉴权。", { name: k.name }),
      danger: true,
      confirmText: t("删除"),
    });
    if (!ok) return;
    try {
      await keysApi.remove(k.id);
      toast.success(t("密钥已删除"), k.name);
      await load();
    } catch (e) {
      toast.error(t("删除失败"), errText(e));
    }
  };

  const toggleDisabled = async (k: KeyView) => {
    try {
      await keysApi.update(k.id, { name: k.name, disabled: !k.disabled });
      toast.success(k.disabled ? t("密钥已启用") : t("密钥已禁用"), k.name);
      await load();
    } catch (e) {
      toast.error(t("操作失败"), errText(e));
    }
  };

  const showUsage = async (k: KeyView) => {
    try {
      setUsage({ key: k, data: await keysApi.stats(k.id) });
    } catch (e) {
      toast.error(t("读取用量失败"), errText(e));
    }
  };

  const rows = keys.data ?? [];

  // 接入信息：Base URL 由网关监听地址推导（":7863" → http://127.0.0.1:7863/v1）
  const baseUrl = useMemo(() => {
    const listen = (gw.data?.listen || ":7863").trim();
    if (listen.includes("://")) return listen.replace(/\/+$/, "") + "/v1";
    let host = listen.startsWith(":") ? "127.0.0.1" + listen : listen;
    if (host.startsWith("0.0.0.0")) host = "127.0.0.1" + host.slice(7);
    return "http://" + host + "/v1";
  }, [gw.data]);
  const selKey = rows.find((r) => r.id === keyId) ?? rows[0];
  const model = models.data?.[0]?.id || "glm-5.2";
  const curl = [
    `curl ${baseUrl}/chat/completions \\`,
    `  -H "Authorization: Bearer ${selKey?.key || "wb-gw-…"}" \\`,
    `  -H "Content-Type: application/json" \\`,
    `  -d '{"model":"${model}","messages":[{"role":"user","content":"你好"}]}'`,
  ].join("\n");

  return (
    <section className="page">
      <div className="page-head">
        <div>
          <h1>{t("API 密钥")}</h1>
          <p>{t("{n} 个密钥 · 网关照此真实鉴权：配额、模型白名单、IP 白名单即时生效", { n: rows.length })}</p>
        </div>
        <div className="head-actions">
          <button className="btn btn-ghost" onClick={() => void load()}>{t("刷新")}</button>
          <button className="btn btn-primary" onClick={() => setForm({ ...EMPTY, models: ["glm-5.2"] })}>{t("＋ 创建密钥")}</button>
        </div>
      </div>

      {/* 接入信息：让任意支持 OpenAI 兼容接口的客户端 / agent 快速接入（卡头可折叠） */}
      <div className="card" style={{ marginBottom: 14 }}>
        <div
          className="card-h"
          style={{ cursor: "pointer", userSelect: "none" }}
          onClick={() => setConnOpen((v) => !v)}
          data-tip={connOpen ? t("收起") : t("展开")}
        >
          <div>
            <h3 className="flex" style={{ gap: 6, alignItems: "center" }}>
              <ChevronDown
                size={15}
                strokeWidth={2.2}
                className="spin-90"
                style={{ transition: "transform .2s", transform: connOpen ? "rotate(180deg)" : undefined, color: "var(--text-3)" }}
              />
              {t("接入信息")}
            </h3>
            <div className="sub">{t("OpenAI 兼容接口：任何支持自定义 Base URL 的客户端或 agent，填入下方地址与密钥即可调用本机网关")}</div>
          </div>
          <div className="flex" style={{ gap: 8, alignItems: "center" }}>
            <span className="badge b-blue"><span className="d" />{t("OpenAI 兼容")}</span>
          </div>
        </div>
        {connOpen && (
        <div className="card-b">
          <div className="flex" style={{ gap: 10, alignItems: "center", padding: "7px 0", borderBottom: "1px solid var(--border)" }}>
            <span style={{ width: 84, flex: "none", fontSize: 12.5, color: "var(--text-2)" }}>{t("Base URL")}</span>
            <code className="mono ellip" style={{ flex: 1, fontSize: 12.5 }}>{baseUrl}</code>
            <button className="icon-btn" data-tip={t("复制 Base URL")} onClick={() => copy(baseUrl, "baseurl")}>
              {copied === "baseurl" ? <Check size={12} strokeWidth={2.4} /> : <Copy size={12} strokeWidth={2} />}
            </button>
          </div>
          <div className="flex" style={{ gap: 10, alignItems: "center", padding: "7px 0", borderBottom: "1px solid var(--border)" }}>
            <span style={{ width: 84, flex: "none", fontSize: 12.5, color: "var(--text-2)" }}>{t("API 密钥")}</span>
            {selKey ? (
              <>
                <select
                  className="mini-input mono"
                  style={{ flex: 1, fontSize: 12.5, minWidth: 0 }}
                  value={selKey.id}
                  onChange={(e) => setKeyId(e.target.value)}
                  data-tip={t("选择要用于示例与复制的密钥")}
                >
                  {rows.map((k) => (
                    <option key={k.id} value={k.id}>{k.name}（{k.mask}）</option>
                  ))}
                </select>
                <button className="icon-btn" data-tip={t("复制完整密钥")} onClick={() => copy(selKey.key || selKey.mask, `connkey-${selKey.id}`)}>
                  {copied === `connkey-${selKey.id}` ? <Check size={12} strokeWidth={2.4} /> : <Copy size={12} strokeWidth={2} />}
                </button>
              </>
            ) : (
              <span style={{ fontSize: 12.5, color: "var(--text-3)" }}>{t("还没有密钥：先在下方创建一个")}</span>
            )}
          </div>
          <div style={{ marginTop: 12 }}>
            <div className="flex" style={{ gap: 8, alignItems: "center", marginBottom: 6 }}>
              <span style={{ fontSize: 12.5, color: "var(--text-2)" }}>{t("curl 示例")}</span>
              <span className="spacer" />
              <button className="btn btn-ghost sm" onClick={() => copy(curl, "curl")}>
                {copied === "curl" ? <Check size={12} strokeWidth={2.4} /> : <Copy size={12} strokeWidth={2} />}
                {t("复制")}
              </button>
            </div>
            <pre className="mono" style={{ margin: 0, padding: "12px 14px", borderRadius: 10, background: "var(--surface-2)", border: "1px solid var(--border)", fontSize: 12, lineHeight: 1.7, overflowX: "auto", whiteSpace: "pre-wrap", wordBreak: "break-all" }}>{curl}</pre>
          </div>
          <div style={{ marginTop: 10, fontSize: 12, color: "var(--text-3)", lineHeight: 1.7 }}>
            {t("可用接口：POST {b}/chat/completions · GET {b}/models；鉴权头为 Authorization: Bearer <密钥>。", { b: baseUrl })}
            <br />
            {t("本机 AI 编程客户端（Claude Code / Codex 等）可在「智能体接入」页一键写入配置，无需手动粘贴。")}
          </div>
        </div>
        )}
      </div>

      {keys.error && <ErrorBlock message={keys.error} onRetry={() => void load()} />}

      <div className="card" style={{ overflow: "hidden" }}>
        <table className="tbl tbl-nowrap">
          <thead>
            <tr>
              <th>{t("名称")}</th><th>{t("密钥")}</th><th>{t("用量 / 配额")}</th><th>{t("模型白名单")}</th>
              <th>{t("IP 白名单")}</th><th>{t("过期时间")}</th><th style={{ width: 140 }}>{t("操作")}</th>
            </tr>
          </thead>
          <tbody>
            {!keys.settled && <SkeletonRows colSpan={7} rows={3} />}
            {keys.settled && rows.length === 0 && (
              <EmptyRow colSpan={7} text={t("还没有密钥：创建一个后即可用它调用本机网关")} />
            )}
            {rows.map((k) => {
              const pct = k.tokenQuota ? Math.min(100, Math.round((k.tokenUsed / k.tokenQuota) * 100)) : 0;
              const color = pct >= 99 ? "var(--red)" : pct >= 70 ? "var(--primary)" : "var(--blue)";
              const soon = k.expires && new Date(k.expires).getTime() - Date.now() < 7 * 86400000;
              const meta = `${k.id} · ${k.created} ${t("创建")}${k.requests > 0 ? ` · ${t("已用 {n} 次", { n: k.requests })}` : ` · ${t("从未使用")}`}`;
              return (
                <tr key={k.id} style={k.disabled ? { opacity: 0.55 } : undefined}>
                  <td>
                    <div className="ellip" style={{ fontWeight: 600 }} data-tip={k.name}>
                      {k.name}
                      {k.disabled && <span className="badge b-gray" style={{ marginLeft: 6 }}><span className="d" />{t("已禁用")}</span>}
                    </div>
                    <div className="mono ellip" style={{ fontSize: 11, color: "var(--text-3)" }} data-tip={meta}>
                      {meta}
                    </div>
                  </td>
                  <td className="mono">
                    {k.mask}
                    <button
                      className="icon-btn"
                      style={{ width: 24, height: 24, verticalAlign: "middle" }}
                      data-tip={t("复制完整密钥")}
                      onClick={() => copy(k.key || k.mask, `key-${k.id}`)}
                    >
                      {copied === `key-${k.id}` ? <Check size={12} strokeWidth={2.4} /> : <Copy size={12} strokeWidth={2} />}
                    </button>
                  </td>
                  <td>
                    {k.tokenQuota > 0 ? (
                      <div className="quota">
                        <div className="qt">
                          <span>{(k.tokenUsed / 1000).toFixed(1)}K / {(k.tokenQuota / 1000).toFixed(0)}K</span>
                          <span>{pct}%</span>
                        </div>
                        <div className="qb"><i style={{ width: `${pct}%`, background: color }} /></div>
                      </div>
                    ) : (
                      <div className="mini-metric">
                        <div className="mm-top"><span>{t("已用")}</span><b>{(k.tokenUsed / 1000).toFixed(1)}K</b></div>
                        <div className="mm-top"><span>{t("积分")}</span><b>{k.creditUsed}</b></div>
                      </div>
                    )}
                  </td>
                  <td>
                    <div className="ellip" data-tip={(k.models ?? []).map((m) => (m === "*" ? t("全部模型") : m)).join("、")}>
                      {(k.models ?? []).map((m) => (
                        <span className="chip" key={m} style={{ marginRight: 4 }}>{m === "*" ? t("全部模型") : m}</span>
                      ))}
                    </div>
                  </td>
                  <td className="mono">
                    <div className="ellip" data-tip={(k.ipWhitelist ?? []).join(", ")}>
                      {(k.ipWhitelist ?? []).length ? (k.ipWhitelist ?? []).join(", ") : <span style={{ color: "var(--text-3)" }}>{t("不限")}</span>}
                    </div>
                  </td>
                  <td className="mono">
                    {k.expires || "—"}
                    {soon && <span className="badge b-amber" style={{ marginLeft: 6 }}><span className="d" />{t("即将到期")}</span>}
                  </td>
                  <td>
                    <div className="flex" style={{ gap: 4 }}>
                      <button className="icon-btn" data-tip={t("用量统计")} onClick={() => void showUsage(k)}>
                        <BarChart3 size={14} strokeWidth={2} />
                      </button>
                      <button className="icon-btn" data-tip={k.disabled ? t("启用") : t("禁用")} onClick={() => void toggleDisabled(k)}>
                        <KeyRound size={14} strokeWidth={2} />
                      </button>
                      <button
                        className="icon-btn"
                        data-tip={t("编辑")}
                        onClick={() =>
                          setForm({
                            id: k.id, name: k.name, expires: k.expires,
                            models: (k.models ?? []).length ? (k.models ?? []) : ["*"],
                            ipWhitelist: (k.ipWhitelist ?? []).join(", "),
                            tokenQuota: String(k.tokenQuota), creditQuota: String(k.creditQuota),
                            maxIps: String(k.maxIps), disabled: k.disabled,
                          })
                        }
                      >
                        <Pencil size={14} strokeWidth={2} />
                      </button>
                      <button className="icon-btn danger" data-tip={t("删除")} onClick={() => void remove(k)}>
                        <Trash2 size={14} strokeWidth={2} />
                      </button>
                    </div>
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>

      <div style={{ marginTop: 14, padding: "13px 16px", borderRadius: 12, background: "var(--primary-soft)", fontSize: 12.5, color: "var(--primary)", lineHeight: 1.7 }}>
        {t("列表中的密钥显示为脱敏掩码，点击复制按钮即可复制完整明文密钥（仅保存在本机）。")}
        {t("网关鉴权使用 SHA-256 摘要。配额按网关实际计费口径累加（每 1000 token 记 1 积分），")}
        {t("Token 或积分配额用尽后网关直接返回 429。白名单外的模型返回 403，IP 不匹配返回 403。")}
      </div>

      {form && createPortal(
        <div className="overlay" onMouseDown={(e) => e.target === e.currentTarget && setForm(null)}>
          <div className="dialog wide">
            <div className="dlg-head">
              <span className="dlg-ic"><KeyRound size={17} strokeWidth={2} /></span>
              <div>
                <h3>{form.id ? t("编辑密钥") : t("创建 API 密钥")}</h3>
                <p>{t("配额与白名单保存后立即对该密钥的后续请求生效")}</p>
              </div>
            </div>
            <div className="dlg-body">
              <Field label={t("名称")} required>
                <input className="input" value={form.name} placeholder={t("例如：CI 流水线")}
                  onChange={(e) => setForm({ ...form, name: e.target.value })} />
              </Field>

              <Field label={t("模型白名单")} hint={t("点击选择，未选择任何项时等同于全部模型")}>
                <div className="flex" style={{ flexWrap: "wrap", gap: 6 }}>
                  <span
                    className={`chip${form.models.includes("*") ? " on" : ""}`}
                    style={{ cursor: "pointer", ...(form.models.includes("*") ? { background: "var(--primary-soft)", color: "var(--primary)" } : {}) }}
                    onClick={() => setForm({ ...form, models: form.models.includes("*") ? [] : ["*"] })}
                  >
                    {t("全部模型")}
                  </span>
                  {!form.models.includes("*") &&
                    (models.data ?? []).slice(0, 24).map((m) => {
                      const on = form.models.includes(m.id);
                      return (
                        <span
                          key={m.id}
                          className="chip"
                          style={{ cursor: "pointer", ...(on ? { background: "var(--primary-soft)", color: "var(--primary)" } : {}) }}
                          onClick={() =>
                            setForm({
                              ...form,
                              models: on ? form.models.filter((x) => x !== m.id) : [...form.models, m.id],
                            })
                          }
                          data-tip={t("上下文 {ctx} · 本机请求 {req} 次", { ctx: m.contextLength, req: m.requests })}
                        >
                          {m.id}
                        </span>
                      );
                    })}
                </div>
                {models.error && <div className="hint-inline">{models.error}</div>}
              </Field>

              <Field label={t("IP 白名单")} hint={t("支持 CIDR，逗号或空格分隔；留空表示不限")}>
                <input className="input" value={form.ipWhitelist} placeholder="192.168.1.0/24, 127.0.0.1"
                  onChange={(e) => setForm({ ...form, ipWhitelist: e.target.value })} />
              </Field>

              <div className="grid-2">
                <Field label={t("Token 配额")} hint={t("0 = 不限")}>
                  <input className="input" type="number" value={form.tokenQuota}
                    onChange={(e) => setForm({ ...form, tokenQuota: e.target.value })} />
                </Field>
                <Field label={t("积分配额")} hint={t("0 = 不限")}>
                  <input className="input" type="number" value={form.creditQuota}
                    onChange={(e) => setForm({ ...form, creditQuota: e.target.value })} />
                </Field>
              </div>
              <div className="grid-2">
                <Field label={t("过期时间")}>
                  <input className="input" type="date" value={form.expires}
                    onChange={(e) => setForm({ ...form, expires: e.target.value })} />
                </Field>
                <Field label={t("最大 IP 数")} hint={t("0 表示不限")}>
                  <input className="input" type="number" value={form.maxIps}
                    onChange={(e) => setForm({ ...form, maxIps: e.target.value })} />
                </Field>
              </div>
              {form.id && (
                <div className="sw-row" style={{ padding: "8px 0" }}>
                  <div>
                    <div className="t">{t("禁用该密钥")}</div>
                    <div className="d">{t("禁用后网关立即拒绝其请求（403）")}</div>
                  </div>
                  <button className={`switch${form.disabled ? " on" : ""}`} onClick={() => setForm({ ...form, disabled: !form.disabled })} />
                </div>
              )}
            </div>
            <div className="dlg-foot">
              <button className="btn btn-ghost" onClick={() => setForm(null)}>{t("取消")}</button>
              <button className="btn btn-primary" disabled={saving} onClick={submit}>
                {saving ? <Loader2 size={13} className="spin" /> : null}
                {form.id ? t("保存修改") : t("创建密钥")}
              </button>
            </div>
          </div>
        </div>,
      document.body)}

      {usage && createPortal(
        <div className="overlay" onMouseDown={(e) => e.target === e.currentTarget && setUsage(null)}>
          <div className="dialog">
            <div className="dlg-head">
              <div>
                <h3>{t("用量统计")} · {usage.key.name}</h3>
                <p>{t("聚合自真实请求日志（含失败请求）")}</p>
              </div>
              <button className="icon-btn" onClick={() => setUsage(null)}><X size={14} strokeWidth={2.2} /></button>
            </div>
            <div className="dlg-body">
              {usage.data.requests === 0 ? (
                <EmptyBlock title={t("该密钥还没有请求记录")} desc={t("用它调用一次网关后即可看到用量。")} />
              ) : (
                <>
                  <div className="kv-grid">
                    <KV k={t("请求数")} v={String(usage.data.requests)} />
                    <KV k={t("成功 / 失败")} v={`${usage.data.success} / ${usage.data.errors}`} />
                    <KV k={t("Token 合计")} v={usage.data.tokens.toLocaleString()} />
                    <KV k={t("积分消耗")} v={String(usage.data.credits)} />
                    <KV k={t("平均延迟")} v={`${usage.data.avgLatency.toFixed(0)} ms`} />
                    <KV k={t("最近使用")} v={usage.data.lastUsed || "—"} />
                  </div>
                  <div style={{ marginTop: 14 }}>
                    {(usage.data.byModel ?? []).map((m) => (
                      <div className="pct-row" key={m.model}>
                        <div className="n">{m.model}</div>
                        <div className="bars">
                          <div className="pb">
                            <i style={{ width: `${usage.data.tokens ? (m.tokens / usage.data.tokens) * 100 : 0}%`, background: "var(--primary)" }} />
                          </div>
                        </div>
                        <div className="vals">{m.requests} {t("次")} · {m.tokens.toLocaleString()} tok{m.errors ? ` · ${t("失败")} ${m.errors}` : ""}</div>
                      </div>
                    ))}
                  </div>
                </>
              )}
            </div>
            <div className="dlg-foot">
              <button className="btn btn-primary" onClick={() => setUsage(null)}>{t("关闭")}</button>
            </div>
          </div>
        </div>,
      document.body)}
    </section>
  );
}

function Field({ label, hint, required, children }: { label: string; hint?: string; required?: boolean; children: React.ReactNode }) {
  return (
    <div className="field">
      <label>
        {label} {required && <span style={{ color: "var(--red)" }}>*</span>}
        {hint && <span className="hint-inline">{hint}</span>}
      </label>
      {children}
    </div>
  );
}

function KV({ k, v }: { k: string; v: string }) {
  return (
    <div className="kv">
      <span>{k}</span>
      <b className="mono">{v}</b>
    </div>
  );
}

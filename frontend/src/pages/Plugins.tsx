import { useCallback, useEffect, useMemo, useState } from "react";
import {
  BadgeCheck, Brain, ChevronDown, ChevronUp, History, ListChecks, Loader2, Plug, Plus,
  RotateCcw, Save, Search, ShieldAlert, ShieldCheck, Terminal, Undo2, X,
} from "lucide-react";
import { pluginsApi } from "../services/api";
import { errText, useAsync } from "../hooks/useAsync";
import { confirmDialog, toast } from "../components/common/Feedback";
import { ErrorBlock } from "../components/common/StateBlock";
import { useT, t as tr } from "../i18n";
import type {
  PluginBackup, PluginSettings, PluginSlashCommand, PluginStatus, PluginTargetStatus,
} from "../types";

/** 插件图标（按 ID 映射，缺失时用通用图标） */
const ICONS: Record<string, React.ReactNode> = {
  mcp: <Plug size={16} strokeWidth={1.9} />,
  rules: <ListChecks size={16} strokeWidth={1.9} />,
  "hook-safety": <ShieldCheck size={16} strokeWidth={1.9} />,
  "hook-context": <Brain size={16} strokeWidth={1.9} />,
  "hook-notify": <BadgeCheck size={16} strokeWidth={1.9} />,
  slash: <Terminal size={16} strokeWidth={1.9} />,
};

/** pluginName 插件 ID → 展示名（回滚列表用） */
function pluginName(id: string): string {
  const names: Record<string, string> = {
    mcp: "MCP 工具接入",
    rules: "规则一源多写",
    "hook-safety": "安全命令自动放行",
    "hook-context": "会话上下文注入",
    "hook-notify": "任务完成推送",
  };
  return names[id] ?? id;
}

export default function Plugins() {
  const t = useT();
  const statuses = useAsync<PluginStatus[]>(() => pluginsApi.statuses(), []);
  const settings = useAsync<PluginSettings>(() => pluginsApi.settings(), []);
  const slash = useAsync<PluginSlashCommand[]>(() => pluginsApi.slashCommands(), []);
  const backups = useAsync<PluginBackup[]>(() => pluginsApi.backups(), []);
  const [busy, setBusy] = useState<string | null>(null);

  // ---------- 插件配置（规则文本 + 放行白名单）----------
  const [rulesText, setRulesText] = useState("");
  const [allow, setAllow] = useState<string[]>([]);
  const [newRule, setNewRule] = useState("");
  const [saving, setSaving] = useState(false);
  const [loaded, setLoaded] = useState(false);
  const [slashOpen, setSlashOpen] = useState(false);
  const [query, setQuery] = useState("");

  useEffect(() => {
    if (loaded || !settings.data) return;
    setRulesText(settings.data.rulesText ?? "");
    setAllow(settings.data.safetyAllowlist ?? []);
    setLoaded(true);
  }, [settings.data, loaded]);

  const dirty = useMemo(() => {
    if (!loaded) return false;
    const cur = settings.data?.safetyAllowlist ?? [];
    return rulesText !== (settings.data?.rulesText ?? "") || JSON.stringify(allow) !== JSON.stringify(cur);
  }, [loaded, rulesText, allow, settings.data]);

  const saveSettings = useCallback(async () => {
    setSaving(true);
    try {
      await pluginsApi.saveSettings({ rulesText, safetyAllowlist: allow });
      toast.success(tr("插件配置已保存"), tr("规则文本与放行白名单已写入本机配置"));
      await Promise.all([settings.reload(), statuses.reload()]);
    } catch (e) {
      toast.error(tr("保存失败"), errText(e));
    } finally {
      setSaving(false);
    }
  }, [rulesText, allow, settings, statuses]);

  const resetAllowlist = useCallback(async () => {
    try {
      const d = await pluginsApi.defaultSettings();
      setAllow(d.safetyAllowlist ?? []);
      toast.info(tr("已填入默认白名单"), tr("确认无误后点「保存配置」生效"));
    } catch (e) {
      toast.error(tr("读取默认值失败"), errText(e));
    }
  }, []);

  const addRule = useCallback(() => {
    const v = newRule.trim();
    if (!v) return;
    setAllow((prev) => (prev.includes(v) ? prev : [...prev, v]));
    setNewRule("");
  }, [newRule]);

  // ---------- 插件接入/卸载 ----------
  const apply = useCallback(
    async (p: PluginStatus) => {
      if (p.installed) {
        const ok = await confirmDialog({
          title: tr("重新写入「{name}」", { name: p.name }),
          desc: tr("将按当前配置重新写入各客户端文件；原文件会先备份，可在本页底部回滚。"),
          confirmText: tr("重新写入"),
        });
        if (!ok) return;
      }
      setBusy(p.id);
      try {
        const res = await pluginsApi.apply(p.id);
        toast.success(
          tr("「{name}」已写入", { name: p.name }),
          [res.detail, res.backupDir ? tr("备份于 {dir}", { dir: res.backupDir }) : ""]
            .filter(Boolean)
            .join(" · "),
        );
        await Promise.all([statuses.reload(), backups.reload()]);
      } catch (e) {
        toast.error(tr("「{name}」写入失败", { name: p.name }), errText(e));
      } finally {
        setBusy(null);
      }
    },
    [statuses, backups],
  );

  const remove = useCallback(
    async (p: PluginStatus) => {
      const ok = await confirmDialog({
        title: tr("卸载「{name}」", { name: p.name }),
        desc: tr("将移除本插件写入的条目/托管块，你原有的配置内容保持不变。"),
        danger: true,
        confirmText: tr("卸载"),
      });
      if (!ok) return;
      setBusy(p.id);
      try {
        const res = await pluginsApi.remove(p.id);
        toast.success(tr("「{name}」已卸载", { name: p.name }), res.detail || "");
        await Promise.all([statuses.reload(), backups.reload()]);
      } catch (e) {
        toast.error(tr("「{name}」卸载失败", { name: p.name }), errText(e));
      } finally {
        setBusy(null);
      }
    },
    [statuses, backups],
  );

  // ---------- 提效指令：按条安装/卸载 ----------
  const toggleSlash = useCallback(
    async (c: PluginSlashCommand) => {
      setBusy("slash-" + c.id);
      try {
        if (c.installed) await pluginsApi.remove("slash", [c.id]);
        else await pluginsApi.apply("slash", [c.id]);
        await Promise.all([slash.reload(), statuses.reload()]);
      } catch (e) {
        toast.error(tr("操作失败"), errText(e));
      } finally {
        setBusy(null);
      }
    },
    [slash, statuses],
  );

  const restoreBackup = useCallback(
    async (b: PluginBackup) => {
      const files = b.files ?? [];
      const ok = await confirmDialog({
        title: tr("回滚备份"),
        desc: tr("将用 {time} 的备份覆盖 {n} 个文件：{files}", {
          time: b.createdAt,
          n: files.length,
          files: files.join("、"),
        }),
        danger: true,
        confirmText: tr("回滚"),
      });
      if (!ok) return;
      setBusy("backup-" + b.target + b.id);
      try {
        const n = await pluginsApi.restore(b.target, b.id);
        toast.success(tr("已回滚 {n} 个文件", { n }), "");
        await Promise.all([statuses.reload(), backups.reload()]);
      } catch (e) {
        toast.error(tr("回滚失败"), errText(e));
      } finally {
        setBusy(null);
      }
    },
    [statuses, backups],
  );

  const slashList = useMemo(() => {
    const q = query.trim().toLowerCase();
    const all = slash.data ?? [];
    if (!q) return all;
    return all.filter(
      (c) =>
        c.title.toLowerCase().includes(q) ||
        c.name.toLowerCase().includes(q) ||
        c.category.toLowerCase().includes(q),
    );
  }, [slash.data, query]);

  const slashInstalled = (slash.data ?? []).filter((c) => c.installed).length;
  const backupList = backups.data ?? [];

  return (
    <div className="page">
      <header className="page-head">
        <div>
          <h1>{t("插件中心")}</h1>
          <p>{t("把 BuddyBot 的能力以插件形式交给编程智能体使用：不接管、不驱动 agent，只提供数据与方法。")}</p>
          <p className="hint">{t("覆盖客户端配置文件的写入都会先备份（可在本页底部回滚）；卸载只移除本插件写入的内容。")}</p>
          <p className="hint">{t("注意：MCP 工具返回的本机内容会进入智能体上下文，并随请求上行到模型服务商。")}</p>
        </div>
      </header>

      <section className="card plug-config">
        <div>
          <h3>{t("插件配置")}</h3>
          <p className="hint">{t("规则文本决定「规则一源多写」同步什么；放行白名单决定哪些命令免确认执行。")}</p>
        </div>
        {settings.error ? (
          <p className="hint">
            {t("插件配置读取失败")}：{errText(settings.error)}
          </p>
        ) : (
          <>
            <label className="plug-field">
              <span>{t("规则文本（写进 CLAUDE.md / AGENTS.md / .cursor/rules 的托管块）")}</span>
              <textarea rows={6} spellCheck={false} value={rulesText} onChange={(e) => setRulesText(e.target.value)} />
            </label>
            <div className="plug-field">
              <span>{t("安全命令放行白名单（前缀匹配；命中且不含组合符号才放行）")}</span>
              <div className="plug-chips">
                {allow.length === 0 && <span className="hint">{t("（空：任何命令都按客户端默认流程询问）")}</span>}
                {allow.map((a) => (
                  <span className="plug-chip" key={a}>
                    {a}
                    <button
                      type="button"
                      aria-label={tr("移除 {v}", { v: a })}
                      onClick={() => setAllow((prev) => prev.filter((x) => x !== a))}
                    >
                      <X size={11} />
                    </button>
                  </span>
                ))}
              </div>
              <div className="plug-add">
                <input
                  value={newRule}
                  placeholder={t("如：go test ./...")}
                  onChange={(e) => setNewRule(e.target.value)}
                  onKeyDown={(e) => {
                    if (e.key === "Enter") {
                      e.preventDefault();
                      addRule();
                    }
                  }}
                />
                <button className="btn" type="button" onClick={addRule} disabled={!newRule.trim()}>
                  <Plus size={14} />
                  {t("添加")}
                </button>
                <button className="btn" type="button" onClick={resetAllowlist}>
                  <RotateCcw size={14} />
                  {t("恢复默认")}
                </button>
              </div>
            </div>
            <p className="plug-warn">
              <ShieldAlert size={14} style={{ flex: "none", marginTop: 2 }} />
              <span>
                {t("放行 = 跳过确认。默认只放行只读检视与不执行项目代码的构建命令；加入 go run / npm test / make 等会执行项目代码，请自行权衡。")}
              </span>
            </p>
            <div className="plug-foot">
              <span className="hint">{dirty ? t("有未保存的修改") : t("配置已是最新")}</span>
              <button className="btn btn-primary" type="button" onClick={saveSettings} disabled={!dirty || saving}>
                {saving ? <Loader2 size={14} className="spin" /> : <Save size={14} />}
                {t("保存配置")}
              </button>
            </div>
          </>
        )}
      </section>

      {statuses.error ? (
        <ErrorBlock message={errText(statuses.error)} onRetry={statuses.reload} />
      ) : statuses.loading && !statuses.data ? (
        <div className="plug-grid">
          {[0, 1, 2, 3].map((i) => (
            <section className="card plug-card" key={i} aria-hidden />
          ))}
        </div>
      ) : (
        <div className="plug-grid">
          {(statuses.data ?? []).map((p) => (
            <section className="card plug-card" key={p.id}>
              <div className="plug-head">
                <span className="plug-ico">{ICONS[p.id] ?? <Plug size={16} strokeWidth={1.9} />}</span>
                <div className="plug-title">
                  <h3>{t(p.name)}</h3>
                  <span className={"chip " + (p.installed ? "chip-on" : "chip-off")}>
                    {p.installed ? t("已接入") : t("未接入")}
                  </span>
                </div>
              </div>
              <p className="plug-desc">{t(p.description)}</p>

              <ul className="plug-targets">
                {(p.targets ?? []).length === 0 && <li className="hint">{t("未检测到支持的客户端")}</li>}
                {(p.targets ?? []).map((tg: PluginTargetStatus) => (
                  <li key={tg.id}>
                    <span className="plug-dot" data-on={tg.applied ? "1" : "0"} />
                    <span className="plug-tname">{tg.name}</span>
                    <span className="hint">
                      {!tg.installed ? t("未检测到安装") : tg.applied ? t("已写入") : t("未写入")}
                      {tg.note ? ` · ${t(tg.note)}` : ""}
                    </span>
                  </li>
                ))}
              </ul>

              {p.detail && <p className="hint plug-detail">{p.detail}</p>}

              <div className="plug-actions">
                <button className="btn btn-primary" disabled={busy === p.id} onClick={() => apply(p)}>
                  {busy === p.id ? <Loader2 size={14} className="spin" /> : <Plug size={14} />}
                  {p.installed ? t("重新写入") : t("一键接入")}
                </button>
                {p.id === "slash" && (
                  <button className="btn" type="button" onClick={() => setSlashOpen((v) => !v)}>
                    {slashOpen ? <ChevronUp size={14} /> : <ChevronDown size={14} />}
                    {t("管理指令")}
                  </button>
                )}
                {p.installed && (
                  <button className="btn" disabled={busy === p.id} onClick={() => remove(p)}>
                    <Undo2 size={14} />
                    {t("卸载")}
                  </button>
                )}
              </div>
            </section>
          ))}
        </div>
      )}

      {slashOpen && (
        <section className="card plug-slash">
          <div className="plug-foot">
            <div>
              <h3>{t("提效指令安装")}</h3>
              <p className="hint">
                {tr("已安装 {n} / {all} 条；安装后可在会话里用 /buddybot-编号-分类 调用。", {
                  n: slashInstalled,
                  all: (slash.data ?? []).length,
                })}
              </p>
            </div>
            <div className="plug-add" style={{ flex: "none" }}>
              <Search size={14} className="hint" />
              <input value={query} placeholder={t("筛选指令")} onChange={(e) => setQuery(e.target.value)} />
            </div>
          </div>
          {slash.error ? (
            <p className="hint">
              {t("指令清单读取失败")}：{errText(slash.error)}
            </p>
          ) : (
            <div className="plug-slash-list">
              {slashList.length === 0 && (
                <p className="hint" style={{ padding: 10 }}>
                  {t("没有匹配的指令")}
                </p>
              )}
              {slashList.map((c) => (
                <div className="plug-slash-row" key={c.id}>
                  <span className="tt">{c.title}</span>
                  <span className="nm">{c.name}</span>
                  <button
                    className={"btn" + (c.installed ? "" : " btn-primary")}
                    disabled={busy === "slash-" + c.id}
                    onClick={() => toggleSlash(c)}
                  >
                    {busy === "slash-" + c.id ? <Loader2 size={13} className="spin" /> : null}
                    {c.installed ? t("卸载") : t("安装")}
                  </button>
                </div>
              ))}
            </div>
          )}
        </section>
      )}

      {backupList.length > 0 && (
        <section className="card plug-slash">
          <div>
            <h3>{t("写入备份与回滚")}</h3>
            <p className="hint">{t("每次写入前的原始文件副本；回滚会用备份覆盖当前文件。")}</p>
          </div>
          {backupList.map((b) => (
            <div className="plug-backup-row" key={b.target + b.id}>
              <History size={14} className="hint" />
              <span className="tt">{t(pluginName(b.plugin))}</span>
              <span className="nm">{b.createdAt}</span>
              <span className="nm">{tr("{n} 个文件", { n: (b.files ?? []).length })}</span>
              <button
                className="btn"
                disabled={busy === "backup-" + b.target + b.id}
                onClick={() => restoreBackup(b)}
              >
                {busy === "backup-" + b.target + b.id ? (
                  <Loader2 size={13} className="spin" />
                ) : (
                  <Undo2 size={13} />
                )}
                {t("回滚")}
              </button>
            </div>
          ))}
        </section>
      )}
    </div>
  );
}

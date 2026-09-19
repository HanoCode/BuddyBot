import { useCallback, useEffect, useMemo, useReducer, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { QRCodeSVG } from "qrcode.react";
import {
  Ban, Check, ChevronLeft, ChevronRight, CircleCheck, Copy, Download, ExternalLink, FolderOpen, Globe, Hourglass, Loader2, MonitorSmartphone, Play,
  RefreshCw, Search, Trash2, TriangleAlert, Upload, X,
} from "lucide-react";
import { accountsApi, clientSwitchApi } from "../services/api";
import { broadcastRefresh, errText, useAsync } from "../hooks/useAsync";
import { EVENT, onEvent } from "../services/events";
import { confirmDialog, promptDialog, toast } from "../components/common/Feedback";
import { EmptyRow, ErrorBlock, SkeletonRows } from "../components/common/StateBlock";
import type { Account, AccountListResult, AccountQuery, AccountStatus, TaskRun, TaskRunDetail, TaskType } from "../types";
import { useT, t } from "../i18n";

// 后端 status 字段是自由字符串，这里按已知取值映射，未知取值兜底为 unknown
const STATUS_BADGE: Record<string, { cls: string; text: string }> = {
  online: { cls: "b-green", text: "在线" },
  cooldown: { cls: "b-amber", text: "冷却" },
  expired: { cls: "b-red", text: "Token 过期" },
  disabled: { cls: "b-gray", text: "已禁用" },
  unknown: { cls: "b-blue", text: "有效期未知" },
  invalid: { cls: "b-red", text: "凭证无效" },
  relogin: { cls: "b-amber", text: "需重新登录" },
};

const FILTERS: { key: "" | AccountStatus; label: string }[] = [
  { key: "", label: "全部" },
  { key: "online", label: "在线" },
  { key: "cooldown", label: "冷却" },
  { key: "expired", label: "过期" },
  { key: "relogin", label: "需重登" },
  { key: "disabled", label: "已禁用" },
  { key: "unknown", label: "未知" },
  { key: "invalid", label: "无效" },
];

const TASK_LABEL: Record<string, string> = {
  checkin: "签到",
  travel: "旅行",
  keepalive: "保活",
  activity: "活跃地图",
  school: "开学季",
  cat: "夜猫子",
  growth: "成长任务",
};

/** 任务中心：全部可手动执行的任务（needSel = 需要先勾选账号，其余未勾选时对全池执行） */
const ALL_TASKS: { type: TaskType; label: string; desc: string; needSel?: boolean }[] = [
  { type: "checkin", label: "每日签到", desc: "签到 + 兑换连登奖励 + 抽奖；国际版账号改领 trial 加油包", needSel: true },
  { type: "travel", label: "猫猫旅行", desc: "巡检旅行状态：无猫领养 / 空闲派出 / 到站领奖", needSel: true },
  { type: "keepalive", label: "Token 保活", desc: "临期自动刷新 token 写回凭证，顺带刷新余额与模型", needSel: true },
  { type: "growth", label: "成长任务中心", desc: "扫描全部账号的成长任务，自动完成可自动化项并领奖" },
  { type: "activity", label: "活跃地图", desc: "完成活跃地图任务并领取奖励" },
  { type: "school", label: "开学季", desc: "开学季任务闭环（含抽奖抽完）" },
  { type: "cat", label: "夜猫子", desc: "夜猫子时段任务" },
];

const AVATAR_COLORS = ["#6366F1,#8B5CF6", "#0EA5E9,#38BDF8", "#F59E0B,#FBBF24", "#8B5CF6,#C084FC", "#10B981,#34D399"];

const PAGE_SIZE = 10;

/** 轮询连续失败多少次才打断（约 6 秒）：单次抖动不打断扫码，持续失败必须露出原因 */
const OAUTH_POLL_FAIL_LIMIT = 3;
const OAUTH_AUTO_REGEN = 2; // 过期后自动重取码的上限（每次约 15 分钟有效期）

/** 国际版可选地区（与后端 internationalRegions / 官网短名单一致）。国际版新号
 * 必须先完成地区注册，否则聊天报 14017；地区一变就要重新申请授权码，先选好再扫。 */
const INTERNATIONAL_REGIONS = [
  { code: "HK", label: "中国香港" },
  { code: "MO", label: "中国澳门" },
  { code: "SG", label: "新加坡" },
  { code: "TH", label: "泰国" },
  { code: "PH", label: "菲律宾" },
  { code: "MY", label: "马来西亚" },
  { code: "ID", label: "印度尼西亚" },
] as const;

export default function Accounts() {
  const t = useT();
  const [keyword, setKeyword] = useState("");
  const [status, setStatus] = useState<"" | AccountStatus>("");
  const [page, setPage] = useState(1);
  // 到期优先排序：有积分到期信息的账号排前（对齐 workbuddy-switch 的到期优先视图）
  const [expirySort, setExpirySort] = useState(false);
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [detail, setDetail] = useState<Account | null>(null);
  const [lastRun, setLastRun] = useState<TaskRun | null>(null);
  const [acctNames, setAcctNames] = useState<Record<string, string>>({});
  const [taskDetail, setTaskDetail] = useState<TaskRunDetail | null>(null);
  const [taskDetailType, setTaskDetailType] = useState<string>("");
  const [importing, setImporting] = useState(false);
  const [taskCenter, setTaskCenter] = useState(false);
  // 任务中心实时进度（对齐 panel 队列的逐项可见性）：task:progress 事件驱动
  const [prog, setProg] = useState<null | { type: string; index: number; total: number }>(null);
  const [progRows, setProgRows] = useState<{ uid: string; status: string; message: string }[]>([]);
  const [busyTypes, setBusyTypes] = useState<string[]>([]);
  // 接入账号弹窗：pick 选站点/地区 → waiting 出二维码等扫码 → success / error 终态
  const [oauth, setOauth] = useState<null | {
    region: "cn" | "global";
    phase: "pick" | "waiting" | "success" | "error";
    elapsed: number;
    url?: string;
    message?: string; // error 阶段的失败原因
    nickname?: string; // success 阶段展示
    updated?: boolean; // true = 重新授权已存在的账号
    note?: string; // 附带说明（国际版地区注册 / trial 结果）
  }>(null);
  const [globalRegion, setGlobalRegion] = useState(""); // 国际版地区代码（如 HK）
  const [pickedGlobal, setPickedGlobal] = useState(false); // pick 阶段展开了国际版地区选择
  const [deepRefreshing, setDeepRefreshing] = useState(false);
  // 一键全部执行：待执行任务队列（串行，前一个完成后再提交下一个）
  const [runAllBusy, setRunAllBusy] = useState(false);
  const runQueueRef = useRef<TaskType[]>([]);
  const runUidsRef = useRef<string[]>([]);
  const runCurrentRef = useRef<TaskType | null>(null);
  // 当前进度归属的任务类型（新任务首个 progress 事件到达时重置逐账号进度区）
  const progTypeRef = useRef<string | null>(null);
  // 逐账号终态刷新防抖：progress 事件密集时合并为一次列表重载
  const listRefreshTimer = useRef<number | null>(null);
  // 任务中心「今日已完成」标记：type → 最近一次成功执行的 HH:MM（来源=调度器 history）
  const [doneToday, setDoneToday] = useState<Record<string, string>>({});
  const syncDoneToday = useCallback((st: { history?: { type?: string; startedAt?: string; success?: number }[] }) => {
    const p = (n: number) => String(n).padStart(2, "0");
    const now = new Date();
    const today = `${now.getFullYear()}-${p(now.getMonth() + 1)}-${p(now.getDate())}`;
    const map: Record<string, string> = {};
    for (const r of st.history ?? []) {
      // history 新记录在前，首个命中的即是该任务今天最近一次成功运行
      if (r.type && (r.success ?? 0) > 0 && typeof r.startedAt === "string" && r.startedAt.startsWith(today) && !map[r.type]) {
        map[r.type] = r.startedAt.slice(11, 16);
      }
    }
    setDoneToday(map);
  }, []);
  const markTaskDone = (type: string) => {
    const p = (n: number) => String(n).padStart(2, "0");
    const now = new Date();
    setDoneToday((prev) => ({ ...prev, [type]: `${p(now.getHours())}:${p(now.getMinutes())}` }));
  };

  const query = useMemo<AccountQuery>(
    () => ({ keyword: keyword.trim(), status, page, pageSize: PAGE_SIZE, sort: expirySort ? "credit_expiry" : "" }),
    [keyword, status, page, expirySort],
  );

  const list = useAsync<AccountListResult>(() => accountsApi.list(query), [query]);

  const load = useCallback(async () => {
    await list.reload();
  }, [list.reload]);

  useEffect(() => {
    const onRefresh = () => void load();
    window.addEventListener("app:refresh", onRefresh);
    // 账号状态或凭证变化（含导入）后刷新
    const off = onEvent(EVENT.accountStatus, () => void load());
    return () => {
      window.removeEventListener("app:refresh", onRefresh);
      off();
    };
  }, [load]);

  useEffect(() => {
    setSelected(new Set());
  }, [query]);

  // 任务结果明细用昵称展示：lastRun 变化时拉一次 uid→昵称映射（失败静默，兜底显示短 UID）
  useEffect(() => {
    if (!lastRun) return;
    accountsApi
      .list({ page: 1, pageSize: 1000 })
      .then((res) => {
        const map: Record<string, string> = {};
        for (const a of res.items ?? []) map[a.uid] = a.nickname || "";
        setAcctNames(map);
      })
      .catch(() => undefined);
  }, [lastRun]);

  const nameOf = (uid: string) => acctNames[uid] || t("账号 {uid}", { uid: uid.slice(0, 8) });

  const rows = list.data?.items ?? [];
  const total = list.data?.total ?? 0;
  const pages = Math.max(1, Math.ceil(total / PAGE_SIZE));

  const toggle = (uid: string) =>
    setSelected((prev) => {
      const next = new Set(prev);
      next.has(uid) ? next.delete(uid) : next.add(uid);
      return next;
    });

  const allSelected = rows.length > 0 && rows.every((a) => selected.has(a.uid));
  const toggleAll = () =>
    setSelected((prev) => (rows.length > 0 && rows.every((a) => prev.has(a.uid)) ? new Set() : new Set(rows.map((a) => a.uid))));

  // 任务全部异步执行：提交后立即返回，结果通过 task:completed 事件回填（见下方订阅）
  const runTask = async (taskType: TaskType, uids: string[]): Promise<boolean> => {
    if (taskType === "checkin" || taskType === "travel" || taskType === "keepalive") {
      if (!uids.length) {
        toast.warn(t("请先选择账号"));
        return false;
      }
    }
    try {
      await accountsApi.runTaskAsync(taskType, uids);
      toast.info(t("{label}已在后台执行", { label: t(TASK_LABEL[taskType] ?? taskType) }), t("完成后结果会显示在「最近一次任务结果」"));
      return true;
    } catch (e) {
      toast.error(t("任务提交失败"), errText(e));
      return false;
    }
  };

  // 一键全部执行：按队列串行推进（提交失败自动跳过，继续下一个）
  const advanceRunAll = () => {
    const next = runQueueRef.current.shift();
    if (!next) {
      setRunAllBusy(false);
      runCurrentRef.current = null;
      return;
    }
    runCurrentRef.current = next;
    void runTask(next, runUidsRef.current).then((ok) => {
      if (!ok) advanceRunAll();
    });
  };

  const runAllTasks = () => {
    const runnable = ALL_TASKS
      .filter((t) => !(t.needSel && selected.size === 0) && !busyTypes.includes(t.type))
      .map((t) => t.type);
    if (!runnable.length) {
      toast.warn(t("没有可执行的任务"), selected.size === 0 ? t("签到 / 旅行 / 保活需先在列表勾选账号") : undefined);
      return;
    }
    runQueueRef.current = runnable;
    runUidsRef.current = [...selected];
    setProgRows([]); // 新一轮：清空上一轮逐账号进度
    progTypeRef.current = null;
    setRunAllBusy(true);
    advanceRunAll();
  };

  // 后台任务逐账号进度：实时更新任务中心的进度视图
  useEffect(() => {
    const off = onEvent(EVENT.taskProgress, (p) => {
      const d = (p ?? {}) as { type?: string; index?: number; total?: number; uid?: string; status?: string; message?: string };
      if (typeof d.type !== "string" || typeof d.uid !== "string") return;
      // 新任务开始（含一键全部执行的队列接续）：清空上一任务的逐账号进度
      if (progTypeRef.current !== d.type) {
        progTypeRef.current = d.type;
        setProgRows([{ uid: d.uid, status: d.status ?? "", message: d.message ?? "" }]);
      } else {
        setProgRows((rows) => [...rows.filter((r) => r.uid !== d.uid), { uid: d.uid!, status: d.status ?? "", message: d.message ?? "" }]);
      }
      setProg({ type: d.type, index: d.index ?? 0, total: d.total ?? 0 });
      // 逐账号终态（后端此时已完成该账号的状态/积分落盘）：防抖刷新账号列表，
      // 保证「每个账号执行完就能看到状态变化」，而不是等整个任务结束才刷新
      if (d.status === "success" || d.status === "failed" || d.status === "skipped") {
        if (listRefreshTimer.current !== null) window.clearTimeout(listRefreshTimer.current);
        listRefreshTimer.current = window.setTimeout(() => {
          listRefreshTimer.current = null;
          void load();
          broadcastRefresh();
        }, 400);
      }
    });
    return () => {
      off();
      if (listRefreshTimer.current !== null) window.clearTimeout(listRefreshTimer.current);
    };
  }, [load]);

  // 后台任务完成：拉取完整运行记录（含逐账号明细）回填结果卡片，并刷新列表
  useEffect(() => {
    const off = onEvent(EVENT.taskCompleted, (p) => {
      const { type, success, failed, skipped } = (p ?? {}) as {
        type?: string; success?: number; failed?: number; skipped?: number;
      };
      if (typeof type !== "string") return;
      setProg(null);
      progTypeRef.current = null; // 同类型任务再跑一轮时，首个 progress 事件会重置进度区
      const label = t(TASK_LABEL[type] ?? type);
      const summary = `${t("成功")} ${success ?? 0} · ${t("失败")} ${failed ?? 0} · ${t("跳过")} ${skipped ?? 0}`;
      if ((success ?? 0) > 0) {
        toast.success(t("{label}完成：{summary}", { label, summary }));
        markTaskDone(type); // 行内立即显示「已完成」，不等 taskStatus 往返
      }
      else toast.info(t("{label}结束：{summary}", { label, summary }));
      accountsApi
        .taskStatus()
        .then((st) => {
          setBusyTypes(st.busy ?? []);
          syncDoneToday(st as { history?: { type?: string; startedAt?: string; success?: number }[] });
          if (st.lastRun && st.lastRun.type === type) setLastRun(st.lastRun);
        })
        .catch(() => undefined);
      void load();
      broadcastRefresh();
      // 一键全部执行：队列中当前任务完成即推进（type 匹配避免手动任务误触发）；
      // 最后一个任务完成时队列已空，advanceRunAll 内部负责收尾解除「全部执行中」
      if (type === runCurrentRef.current) advanceRunAll();
    });
    return off;
  }, [load]);

  // 打开任务中心时同步一次执行中状态与最近结果
  const openTaskCenter = () => {
    setTaskCenter(true);
    accountsApi
      .taskStatus()
      .then((st) => {
        setBusyTypes(st.busy ?? []);
        syncDoneToday(st as { history?: { type?: string; startedAt?: string; success?: number }[] });
        if (st.lastRun) setLastRun(st.lastRun);
      })
      .catch(() => undefined);
  };

  // 深度刷新：List 只读本地缓存，真实状态/余额须走 keepalive 通道逐账号查上游
  // （总是刷新余额与模型，token 仅临期才刷新；自带逐账号进度事件）。已选只刷已选，未选刷全池。
  const deepRefresh = async () => {
    if (deepRefreshing || busyTypes.includes("keepalive")) return;
    setDeepRefreshing(true);
    try {
      await accountsApi.runTaskAsync("keepalive", [...selected]); // 空数组 = 后端全池执行
      toast.info(t("正在重新获取账号状态与余额"), t("逐账号后台执行，完成后列表自动更新"));
    } catch (e) {
      toast.error(t("刷新任务提交失败"), errText(e));
    } finally {
      setDeepRefreshing(false);
    }
  };

  // 禁用/启用：禁用账号不参与网关选号与定时任务（保留凭证，可随时恢复）
  const toggleDisabled = async (acc: Account) => {
    const next = acc.status !== "disabled";
    try {
      await accountsApi.setDisabled(acc.uid, next);
      toast.success(next ? t("账号已禁用") : t("账号已启用"), acc.nickname || acc.uid);
      await load();
      broadcastRefresh();
    } catch (e) {
      toast.error(next ? t("禁用失败") : t("启用失败"), errText(e));
    }
  };

  const removeOne = async (acc: Account) => {
    const ok = await confirmDialog({
      title: t("删除账号"),
      desc: t("将删除凭证文件 {cred}，该账号会立即从池中移除且不可撤销。", { cred: acc.credential }),
      danger: true,
      confirmText: t("删除"),
    });
    if (!ok) return;
    try {
      await accountsApi.remove(acc.uid);
      toast.success(t("账号已删除"), acc.credential);
      await load();
    } catch (e) {
      toast.error(t("删除失败"), errText(e));
    }
  };

  const [deleting, setDeleting] = useState(false);
  const removeSelected = async () => {
    const uids = [...selected];
    const ok = await confirmDialog({
      title: t("删除 {n} 个账号", { n: uids.length }),
      desc: t("将删除这些账号的凭证文件，操作不可撤销。"),
      danger: true,
      confirmText: t("删除"),
    });
    if (!ok) return;
    setDeleting(true);
    try {
      const failed: string[] = [];
      for (const uid of uids) {
        try {
          await accountsApi.remove(uid);
        } catch (e) {
          failed.push(`${uid}: ${errText(e)}`);
        }
      }
      if (failed.length === uids.length) toast.error(t("删除失败"), failed[0]);
      else if (failed.length) toast.warn(t("已删除 {a} 个，{b} 个失败", { a: uids.length - failed.length, b: failed.length }), failed.slice(0, 3).join("；"));
      else toast.success(t("已删除 {n} 个账号", { n: uids.length }));
      setSelected(new Set());
      await load();
    } finally {
      setDeleting(false);
    }
  };

  const openAuthDir = async () => {
    try {
      const dir = await accountsApi.openAuthDir();
      toast.info(t("已打开凭证目录"), dir);
    } catch (e) {
      toast.error(t("打开目录失败"), errText(e));
    }
  };

  // 设备授权：startOAuth 由上游签发 state 并返回官方授权页地址。弹窗内直接渲染
  // 二维码（手机扫码即登录，微信扫码/手机号登录都在官方页内完成），保留浏览器打开兜底；
  // 轮询 PollOAuth 检测结果，凭证由后端自动落盘并广播账号事件。
  // 轮询失败不静默吞掉（workbuddy-manager issue #26 的教训：落盘失败被藏起来，
  // 用户会一直重扫却永不成功）——连续 POLL_FAIL_LIMIT 次失败即中断并报真实原因。
  const oauthTimer = useRef<number | null>(null);
  const oauthBusy = useRef(false); // 上一次轮询还在飞时跳过本轮，避免请求叠加
  const oauthFails = useRef(0); // 连续失败计数：任何一次正常响应即清零
  const oauthRetries = useRef(0); // 过期自动重取码次数（上限 OAUTH_AUTO_REGEN，超过才进 error）
  const stopOAuthPolling = () => {
    if (oauthTimer.current) {
      clearInterval(oauthTimer.current);
      oauthTimer.current = null;
    }
    oauthBusy.current = false;
    oauthFails.current = 0;
  };
  useEffect(() => stopOAuthPolling, []);

  const startOAuth = async (region: "cn" | "global", regionCode = "") => {
    if (region === "global" && !regionCode) {
      setPickedGlobal(true);
      toast.warn(t("请先选择国际版地区"), t("未完成地区注册的国际版账号，登录后聊天会报 14017"));
      return;
    }
    try {
      const hint = await accountsApi.startOAuth(region);
      stopOAuthPolling();
      setOauth({ region, phase: "waiting", elapsed: 0, url: hint.url });
      oauthTimer.current = window.setInterval(async () => {
        if (oauthBusy.current) return;
        oauthBusy.current = true;
        setOauth((o) => (o && o.phase === "waiting" ? { ...o, elapsed: o.elapsed + 2 } : o));
        try {
          const res = await accountsApi.pollOAuth(region, region === "global" ? regionCode : "");
          oauthFails.current = 0;
          if (res?.status === "success") {
            stopOAuthPolling();
            oauthRetries.current = 0;
            setOauth({ region, phase: "success", elapsed: 0, nickname: res.nickname, updated: res.updated, note: res.note });
            toast.success(res.updated ? t("凭证已更新") : t("登录成功"), `${res.nickname || res.uid} ${t("已加入账号池")}`);
            accountsApi.focusMainWindow().catch(() => undefined);
            await load();
            broadcastRefresh();
            window.setTimeout(() => setOauth((o) => (o?.phase === "success" ? null : o)), 2400);
          } else if (res?.status === "expired") {
            if (oauthRetries.current < OAUTH_AUTO_REGEN) {
              // 二维码过期：自动重新生成，用户端无感知，无需重新选站点
              oauthRetries.current += 1;
              stopOAuthPolling();
              toast.info(t("二维码已过期，已自动重新生成（第 {n} 次）", { n: oauthRetries.current }), t("请重新扫码，链接约 15 分钟有效"));
              void startOAuth(region, regionCode);
            } else {
              stopOAuthPolling();
              setOauth((o) => (o ? { ...o, phase: "error", message: "授权已过期（已自动重取 2 次仍超时），请重新生成二维码" } : o));
            }
          } else if (res?.status === "invalid") {
            stopOAuthPolling();
            setOauth((o) => (o ? { ...o, phase: "error", message: "授权会话已丢失（应用可能重启过），请重新发起" } : o));
          }
          // pending → 继续等待
        } catch (e) {
          oauthFails.current += 1;
          if (oauthFails.current >= OAUTH_POLL_FAIL_LIMIT) {
            stopOAuthPolling();
            setOauth((o) => (o ? { ...o, phase: "error", message: errText(e) } : o));
          }
        } finally {
          oauthBusy.current = false;
        }
      }, 2000);
    } catch (e) {
      toast.error(t("发起授权失败"), errText(e));
    }
  };

  // 复制授权链接（对齐 gui LoginWizard：剪贴板不可用时提示手动复制）。
  const [copied, setCopied] = useState(false);
  const copyOAuthURL = async () => {
    if (!oauth?.url) return;
    try {
      await navigator.clipboard.writeText(oauth.url);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 2000);
      toast.success(t("已复制授权链接"), t("可在任意浏览器打开并完成登录"));
    } catch {
      toast.warn(t("复制失败"), t("请手动选中下方链接复制"));
    }
  };

  // 手动重新生成二维码：重置自动重取计数
  const regenOAuth = () => {
    if (!oauth) return;
    oauthRetries.current = 0;
    void startOAuth(oauth.region, oauth.region === "global" ? globalRegion : "");
  };

  // 应用内登录窗口：手机号+短信登录完全在应用内完成（WebView 加载官方授权页）
  const openInAppLogin = async () => {
    if (!oauth) return;
    try {
      await accountsApi.openLoginWindow(oauth.region);
      toast.info(t("已打开应用内登录窗口"), t("手机号登录可在窗口内完成，登录后凭证自动落盘"));
    } catch (e) {
      toast.error(t("打开应用内登录失败"), errText(e));
    }
  };

  const importFile = () => {
    const input = document.createElement("input");
    input.type = "file";
    input.accept = ".json,application/json";
    input.multiple = true;
    input.onchange = async () => {
      const files = Array.from(input.files ?? []);
      if (!files.length) return;
      setImporting(true);
      const okFiles: string[] = [];
      const skipped: string[] = [];
      for (const f of files) {
        try {
          const text = await f.text();
          // v2 加密导出信封：先要密码再解密导入（isEncrypted: 有 version>=2 字段）
          let password = "";
          try {
            const probe = JSON.parse(text) as { version?: number };
            if ((probe.version ?? 0) >= 2) {
              const pwd = await promptDialog({
                title: t("导入加密文件"),
                desc: t("该文件为加密导出，请输入导出时的密码"),
                type: "password",
                optional: false,
                confirmText: t("解密导入"),
              });
              if (pwd === null) {
                skipped.push(`${f.name}：${t("已取消")}`);
                continue;
              }
              password = pwd;
            }
          } catch {
            // 非 JSON 结构交由后端校验并报错
          }
          const res = await accountsApi.importCredentials(f.name, text, password);
          okFiles.push(...(res.files ?? []));
          skipped.push(...(res.skipped ?? []));
        } catch (e) {
          skipped.push(`${f.name}：${errText(e)}`);
        }
      }
      setImporting(false);
      if (okFiles.length) toast.success(t("已导入 {n} 份凭证", { n: okFiles.length }), okFiles.join("、"));
      if (skipped.length) toast.warn(t("跳过 {n} 份", { n: skipped.length }), skipped[0]);
      await load();
      broadcastRefresh();
    };
    input.click();
  };

  // 加密导出：输入密码 → AES-256-GCM 信封；留空走明文导出（需二次确认风险）。
  const exportCredentials = async () => {
    const pwd = await promptDialog({
      title: t("导出凭证"),
      desc: t("输入导出密码将生成加密文件（AES-256-GCM）；留空则导出明文 JSON。密码不会写入文件，忘记将无法恢复。"),
      placeholder: t("导出密码（可留空）"),
      type: "password",
      optional: true,
      confirmText: t("导出"),
    });
    if (pwd === null) return; // 用户取消
    let encrypted = pwd.trim() !== "";
    if (!encrypted) {
      const ok = await confirmDialog({
        title: t("明文导出确认"),
        desc: t("明文文件包含可直接登录的 token，任何拿到该文件的人都能使用这些账号。确定继续？"),
        danger: true,
        confirmText: t("明文导出"),
      });
      if (!ok) return;
    }
    try {
      const text = await accountsApi.exportCredentials(pwd.trim());
      const blob = new Blob([text], { type: "application/json" });
      const url = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = `workbuddy-credentials-${Date.now()}.json`;
      a.click();
      URL.revokeObjectURL(url);
      if (encrypted) toast.success(t("已加密导出"), t("文件已用密码加密，请妥善保管密码与文件"));
      else toast.warn(t("已明文导出"), t("文件包含明文 token，请像保管密码一样保管它"));
    } catch (e) {
      toast.error(t("导出失败"), errText(e));
    }
  };

  // 详情弹窗：记录最后一次请求的 uid，慢响应不得覆盖用户后来点开的账号
  const detailReq = useRef("");
  const refreshDetail = async (uid: string) => {
    detailReq.current = uid;
    try {
      const acc = await accountsApi.detail(uid);
      if (detailReq.current === uid) setDetail(acc);
    } catch (e) {
      toast.error(t("读取详情失败"), errText(e));
    }
  };

  // ⌘K 账号直达：打开面板选中的账号详情。
  // 已挂载时收 app:open-account 事件；新挂载兜底读 sessionStorage（导航后事件可能早于挂载丢失）
  useEffect(() => {
    const open = (uid: string) => {
      if (uid) void refreshDetail(uid);
    };
    const onEvent = (e: Event) => open((e as CustomEvent<string>).detail);
    const pending = sessionStorage.getItem("app:open-account");
    if (pending) {
      sessionStorage.removeItem("app:open-account");
      open(pending);
    }
    window.addEventListener("app:open-account", onEvent);
    return () => window.removeEventListener("app:open-account", onEvent);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // 立即查询上游真实积分余额（剩余/已用/临期），不等 keepalive 周期。
  // 查询中按 uid 隔离（多行并发互不干扰）；成功后回写列表，积分列即时更新。
  const [credLoading, setCredLoading] = useState<Record<string, boolean>>({});
  const queryCredit = async (uid: string) => {
    detailReq.current = uid;
    setCredLoading((m) => ({ ...m, [uid]: true }));
    try {
      const acc = await accountsApi.refreshCredit(uid);
      if (detailReq.current === uid) setDetail((d) => (d && d.uid === uid ? acc : d));
      toast.success(t("余额已更新"), `${acc.nickname || acc.uid}：${t("{n} 分", { n: acc.credits.toLocaleString() })}`);
      await load();
    } catch (e) {
      toast.error(t("余额查询失败"), errText(e));
    } finally {
      setCredLoading((m) => ({ ...m, [uid]: false }));
    }
  };

  // 设为客户端登录：把该账号凭证写入官方登录位并重启客户端（先 precheck 展示风险）。
  const switchClient = async (acc: Account) => {
    let pre;
    try {
      pre = await clientSwitchApi.precheck(acc.uid);
    } catch (e) {
      toast.error(t("切换前检查失败"), errText(e));
      return;
    }
    if (pre.warnings && pre.warnings.includes("拒绝")) {
      toast.error(t("无法切换"), pre.warnings);
      return;
    }
    const ok = await confirmDialog({
      title: t("设为客户端登录"),
      desc: t(
        "将备份官方登录文件 → 退出官方客户端 → 写入 {name} 的登录凭证 → 重启客户端。当前客户端会话会被关闭，请先保存工作。",
        { name: acc.nickname || acc.uid },
      ),
      danger: false,
      confirmText: t("开始切换"),
    });
    if (!ok) return;
    toast.info(t("切换执行中…"), pre.officialAuthFile);
    try {
      await clientSwitchApi.switch(acc.uid);
      toast.success(t("切换完成"), t("官方客户端已重启，请在新窗口确认登录状态"));
    } catch (e) {
      toast.error(t("切换失败"), errText(e));
    }
  };

  // 客户端切换进度：每步实时 toast（备份/退出/写入/启动）
  useEffect(() => {
    const off = onEvent(EVENT.clientSwitch, (p) => {
      const d = (p ?? {}) as { step?: string; message?: string };
      if (d.step === "error") toast.error(t("客户端切换"), d.message ?? "");
      else if (d.step && d.step !== "done" && d.message) toast.info(t("客户端切换"), d.message);
    });
    return off;
  }, []);
  // 打开详情弹窗时若从未查询过余额，自动触发一次（effect 只随 uid 变化重跑，无回环）。
  const detailUid = detail?.uid;
  useEffect(() => {
    if (detailUid) {
      setDetail((d) => {
        if (d && d.uid === detailUid && !d.creditsKnown) void queryCredit(detailUid);
        return d;
      });
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [detailUid]);

  return (
    <section className="page">
      <div className="page-head">
        <div>
          <h1>{t("账号管理")}</h1>
          <p>
            {t("来源：凭证目录")} {list.data?.authDir ?? "—"} · {t("共")} {total} {t("个（在线")} {list.data?.onlineCount ?? 0} {t("个）")}
          </p>
        </div>
        <div className="head-actions">
          <button className="btn btn-ghost" onClick={openAuthDir}>
            <FolderOpen size={13} strokeWidth={2} /> {t("凭证目录")}
          </button>
          <button className="btn btn-ghost" disabled={importing} onClick={importFile}>
            {importing ? <Loader2 size={13} className="spin" /> : <Upload size={13} strokeWidth={2} />} {t("导入凭证")}
          </button>
          <button className="btn btn-ghost" onClick={exportCredentials}>
            <Download size={13} strokeWidth={2} /> {t("导出凭证")}
          </button>
          <button
            className="btn btn-soft"
            disabled={!selected.size }
            data-tip={selected.size ? t("对已选 {n} 个账号执行签到", { n: selected.size }) : t("先勾选账号（或点表头全选）")}
            onClick={() => runTask("checkin", [...selected])}
          >
            <Play size={13} strokeWidth={2} /> {t("批量签到")}{selected.size > 0 ? ` (${selected.size})` : ""}
          </button>
          <button
            className="btn btn-primary"
            onClick={() => {
              setPickedGlobal(false);
              setOauth({ region: "cn", phase: "pick", elapsed: 0 });
            }}
          >＋ {t("接入账号")}</button>
        </div>
      </div>

      {list.error && <ErrorBlock message={list.error} onRetry={() => void load()} />}

      {lastRun && (
        <div className="card" style={{ marginBottom: 14 }}>
          <div className="card-h">
            <div>
              <h3>{t("最近一次任务结果")}</h3>
              <div className="sub">
                {lastRun.trigger === "manual" ? t("手动触发") : t("排程触发")} · {lastRun.startedAt} · {t("耗时")} {lastRun.duration.toFixed(0)}ms
              </div>
            </div>
            <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
              <span className={`badge ${lastRun.failed > 0 ? "b-red" : lastRun.success > 0 ? "b-green" : "b-amber"}`}>
                <span className="d" />{t("成功")} {lastRun.success} · {t("失败")} {lastRun.failed} · {t("跳过")} {lastRun.skipped}
              </span>
              <button className="icon-btn" data-tip={t("关闭结果卡片")} onClick={() => setLastRun(null)}>
                <X size={14} strokeWidth={2.2} />
              </button>
            </div>
          </div>
          <div className="card-b" style={{ maxHeight: 240, overflow: "auto" }}>
            {(lastRun.details ?? []).map((d) => (
              <div
                className={`task-detail-row${d.status === "failed" ? " clickable" : ""}`}
                key={d.uid + d.message}
                data-tip={d.status === "failed" ? t("点击查看失败详情") : undefined}
                onClick={() => {
                  if (d.status !== "failed") return;
                  setTaskDetailType(lastRun?.type ?? "");
                  setTaskDetail(d);
                }}
              >
                <button
                  className="td-uid"
                  data-tip={t("查看账号详情（{uid}）", { uid: d.uid })}
                  onClick={(e) => {
                    e.stopPropagation();
                    void refreshDetail(d.uid);
                  }}
                >
                  {nameOf(d.uid)}
                </button>
                <span className={`badge ${d.status === "success" ? "b-green" : d.status === "failed" ? "b-red" : "b-amber"}`}>
                  <span className="d" />{d.status === "success" ? t("成功") : d.status === "failed" ? t("失败") : t("跳过")}
                </span>
                <span className="td-msg">{d.message}</span>
                {d.status === "failed" && <span className="td-more">{t("详情")} ›</span>}
              </div>
            ))}
          </div>
        </div>
      )}

      <div className="toolbar">
        <div className="search-box">
          <Search size={13} strokeWidth={2.2} />
          <input
            className="search-input"
            placeholder={t("搜索 UID / 昵称 / 凭证文件")}
            value={keyword}
            onChange={(e) => {
              setKeyword(e.target.value);
              setPage(1);
            }}
          />
        </div>
        <div className="seg">
          {FILTERS.map((f) => (
            <button
              key={f.key || "all"}
              className={status === f.key ? "on" : ""}
              onClick={() => {
                setStatus(f.key);
                setPage(1);
              }}
            >
              {t(f.label)}
            </button>
          ))}
        </div>
        <button
          className={`btn btn-ghost sm${expirySort ? " active" : ""}`}
          data-tip={t("按积分到期时间排序：最早到期的账号排最前")}
          onClick={() => {
            setExpirySort((v) => !v);
            setPage(1);
          }}
        >
          <Hourglass size={12} strokeWidth={2} /> {t("到期优先")}
        </button>
        <div className="spacer" />
        <span className="muted" style={{ fontSize: 12 }}>{t("共 {n} 条", { n: total })}</span>
      </div>

      <div className="card" style={{ overflow: "hidden" }}>
        <table className="tbl">
          <thead>
            <tr>
              <th style={{ width: 38 }}>
                <span className={`cb${allSelected ? " on" : ""}`} data-tip={t("全选本页")} onClick={toggleAll}>
                  {allSelected && <Check size={11} strokeWidth={3} />}
                </span>
              </th>
              <th>{t("账号")}</th><th>{t("区域")}</th><th>{t("状态")}</th><th>{t("积分余额")}</th>
              <th>{t("Token 有效期")}</th><th>{t("最近签到")}</th><th>{t("最近活动")}</th><th style={{ width: 158 }}>{t("操作")}</th>
            </tr>
          </thead>
          <tbody>
            {!list.settled && <SkeletonRows colSpan={10} />}
            {list.settled && rows.length === 0 && (
              <EmptyRow
                colSpan={10}
                text={total === 0 && !keyword && !status
                  ? t("凭证目录还没有账号：点击「导入凭证」或「接入账号」把凭证放进来")
                  : t("没有匹配的账号")}
              />
            )}
            {rows.map((a, i) => {
              const [c1, c2] = AVATAR_COLORS[i % AVATAR_COLORS.length].split(",");
              const isSel = selected.has(a.uid);
              const title = a.nickname || a.uid;
              const expiringSoon = a.creditsKnown && (a.creditsExpiring ?? 0) > 0;
              return (
                <tr key={a.uid} className={`${isSel ? "row-sel" : ""}${expiringSoon ? " row-expiring" : ""}`}>
                  <td>
                    <span className={`cb${isSel ? " on" : ""}`} onClick={() => toggle(a.uid)}>
                      {isSel && <Check size={11} strokeWidth={3} />}
                    </span>
                  </td>
                  <td>
                    <div className="acct-cell" style={{ cursor: "pointer" }} onClick={() => void refreshDetail(a.uid)}>
                      <span className="avatar" style={{ background: `linear-gradient(135deg,${c1},${c2})` }}>
                        {title.slice(0, 2).toUpperCase()}
                      </span>
                      <div>
                        <div className="em">{title}</div>
                        <div className="uid">{a.uid} · {a.credential}</div>
                      </div>
                    </div>
                  </td>
                  <td><span className="chip">{a.realm.toUpperCase()}</span></td>
                  <td>
                    <StatusBadge account={a} />
                  </td>
                  <td className="num">
                    <CreditCell account={a} loading={!!credLoading[a.uid]} onRefresh={() => void queryCredit(a.uid)} />
                  </td>
                  <td>{a.hasToken ? <TokenExpiryCell expiresIn={a.expiresIn} expiry={a.tokenExpiry} /> : <span className="muted">{t("无 accessToken")}</span>}</td>
                  <td className="mono">{a.lastCheckin || "—"}</td>
                  <td className="mono">{a.lastActivity || "—"}</td>
                  <td>
                    <div className="flex" style={{ gap: 4 }}>
                      <button
                        className="icon-btn"
                        data-tip={busyTypes.includes("checkin") ? t("签到执行中") : t("立即签到")}
                        disabled={busyTypes.includes("checkin")}
                        onClick={() => runTask("checkin", [a.uid])}
                      >
                        <Check size={14} strokeWidth={2} />
                      </button>
                      <button
                        className="icon-btn"
                        data-tip={a.status === "disabled" ? t("启用账号") : t("禁用账号")}
                        onClick={() => void toggleDisabled(a)}
                      >
                        {a.status === "disabled" ? <CircleCheck size={14} strokeWidth={2} /> : <Ban size={14} strokeWidth={2} />}
                      </button>
                      <button className="icon-btn" data-tip={t("刷新详情")} onClick={() => void refreshDetail(a.uid)}>
                        <RefreshCw size={14} strokeWidth={2} />
                      </button>
                      <button
                        className="icon-btn"
                        data-tip={t("设为客户端登录（写入官方登录位并重启客户端）")}
                        onClick={() => void switchClient(a)}
                      >
                        <MonitorSmartphone size={14} strokeWidth={2} />
                      </button>
                      <button className="icon-btn danger" data-tip={t("删除")} onClick={() => removeOne(a)}>
                        <Trash2 size={14} strokeWidth={2} />
                      </button>
                    </div>
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
        <div className="table-foot">
          <span className="muted">{t("已选 {n} 个账号", { n: selected.size })}</span>
          <span className="spacer" />
          <div className="pager">
            <button disabled={page <= 1} onClick={() => setPage((p) => Math.max(1, p - 1))}>
              <ChevronLeft size={12} strokeWidth={2.4} />
            </button>
            <span>{page} / {pages}</span>
            <button disabled={page >= pages} onClick={() => setPage((p) => Math.min(pages, p + 1))}>
              <ChevronRight size={12} strokeWidth={2.4} />
            </button>
          </div>
          <button
            className="btn btn-ghost sm"
            disabled={deepRefreshing || busyTypes.includes("keepalive")}
            data-tip={busyTypes.includes("keepalive") ? t("刷新执行中") : t("重新拉取上游真实状态与余额（已选账号只刷已选，未选则刷全池）")}
            onClick={() => void deepRefresh()}
          >
            {deepRefreshing || busyTypes.includes("keepalive") ? <Loader2 size={12} className="spin" /> : <RefreshCw size={12} strokeWidth={2} />}
            {t("刷新")}
          </button>
          <button className="btn btn-soft sm" disabled={!selected.size} data-tip={t("对已选账号执行猫猫旅行")} onClick={() => runTask("travel", [...selected])}>{t("批量旅行")}{selected.size > 0 ? ` (${selected.size})` : ""}</button>
          <button className="btn btn-soft sm" disabled={!selected.size} data-tip={t("对已选账号执行保活（含余额/模型刷新）")} onClick={() => runTask("keepalive", [...selected])}>{t("批量保活")}{selected.size > 0 ? ` (${selected.size})` : ""}</button>
          <button
            className="btn btn-soft sm"
            data-tip={t("打开任务中心：查看并执行全部可运行任务")}
            onClick={openTaskCenter}
          >
            <Play size={12} strokeWidth={2} /> {t("任务中心…")}
          </button>
          <button className="btn btn-danger-soft sm" disabled={!selected.size || deleting} onClick={removeSelected}>
            {deleting ? t("删除中…") : t("删除")}
          </button>
        </div>
      </div>

      {detail && createPortal(
        <div className="overlay" onMouseDown={(e) => e.target === e.currentTarget && setDetail(null)}>
          <div className="dialog wide">
            <div className="dlg-head">
              <div>
                <h3>{t("账号详情")}</h3>
                <p>{detail.credential} · {detail.realm.toUpperCase()}</p>
              </div>
              <button className="icon-btn" onClick={() => setDetail(null)}><X size={14} strokeWidth={2.2} /></button>
            </div>
            <div className="dlg-body">
              <div className="kv-grid">
                <KV k="UID" v={detail.uid} />
                <KV k="昵称" v={detail.nickname || "—"} />
                <KV k="企业 ID" v={detail.enterpriseId || "—"} />
                <KV k="状态" v={STATUS_BADGE[detail.status] ? t(STATUS_BADGE[detail.status]!.text) : detail.status} />
                <KV k="access token" v={detail.hasToken ? t("存在") : t("缺失")} />
                <KV k="refresh token" v={detail.hasRefresh ? t("存在") : t("缺失")} />
                <KV k="有效期" v={detail.tokenExpiry || t("凭证未提供")} />
                <KV k="剩余" v={detail.expiresIn > 0 ? formatLeft(detail.expiresIn) : "—"} />
                <KV
                  k="积分余额"
                  v={
                    detail.creditsKnown
                      ? t("{n} 分", { n: detail.credits.toLocaleString() })
                      : credLoading
                        ? t("查询中…")
                        : t("待查询")
                  }
                />
                {detail.creditsKnown && (
                  <>
                    <KV k="已用积分" v={t("{n} 分", { n: (detail.creditsUsed ?? 0).toLocaleString() })} />
                    <KV
                      k="72h 内到期"
                      v={(detail.creditsExpiring ?? 0) > 0 ? t("{n} 分", { n: (detail.creditsExpiring ?? 0).toLocaleString() }) : t("无")}
                    />
                  </>
                )}
                <KV k="最近活动" v={detail.lastActivity || "—"} />
                <KV k="在途请求" v={String(detail.inflight)} />
              </div>
              {detail.note && (
                <div style={{ marginTop: 12, padding: "10px 12px", borderRadius: 10, background: "var(--surface-2)", fontSize: 12, color: "var(--text-2)", lineHeight: 1.6 }}>
                  {detail.note}
                </div>
              )}
            </div>
            <div className="dlg-foot">
              <button
                className="btn btn-ghost"
                disabled={!!credLoading[detail.uid]}
                onClick={() => void queryCredit(detail.uid)}
              >
                {credLoading[detail.uid] ? <Loader2 size={13} className="spin" /> : null}
                {credLoading[detail.uid] ? t("查询中…") : t("查余额")}
              </button>
              <button className="btn btn-ghost" onClick={() => void switchClient(detail)}>
                <MonitorSmartphone size={13} strokeWidth={2} /> {t("设为客户端登录")}
              </button>
              <button className="btn btn-ghost" onClick={() => void refreshDetail(detail.uid)}>{t("刷新")}</button>
              <button className="btn btn-primary" onClick={() => setDetail(null)}>{t("关闭")}</button>
            </div>
          </div>
        </div>,
      document.body)}

      {taskDetail && createPortal(
        <div className="overlay" onMouseDown={(e) => e.target === e.currentTarget && setTaskDetail(null)}>
          <div className="dialog">
            <div className="dlg-head">
              <div>
                <h3>{t("任务失败详情")}</h3>
                <p>{nameOf(taskDetail.uid)} · {t(TASK_LABEL[taskDetailType || lastRun?.type || ""] ?? "任务")}</p>
              </div>
              <button className="icon-btn" onClick={() => setTaskDetail(null)}><X size={14} strokeWidth={2.2} /></button>
            </div>
            <div className="dlg-body">
              <div className="kv-grid">
                <KV k="账号" v={nameOf(taskDetail.uid)} />
                <KV k="UID" v={taskDetail.uid} />
                <KV k="结果" v={t("失败")} />
                <KV k="触发" v={taskDetailType && lastRun?.type === taskDetailType ? (lastRun.trigger === "manual" ? t("手动") : t("排程")) : "—"} />
                <KV k="开始时间" v={taskDetailType && lastRun?.type === taskDetailType ? lastRun?.startedAt ?? "—" : "—"} />
                <KV k="批次耗时" v={taskDetailType && lastRun?.type === taskDetailType ? `${(lastRun?.duration ?? 0).toFixed(0)} ms` : "—"} />
              </div>
              <div className="fail-msg">{taskDetail.message}</div>
            </div>
            <div className="dlg-foot">
              <button
                className="btn btn-ghost"
                data-tip={t("重新执行{label}（仅该账号）", { label: t(TASK_LABEL[taskDetailType || lastRun?.type || ""] ?? "任务") })}
                onClick={() => {
                  const type = taskDetailType || lastRun?.type;
                  if (!type) {
                    toast.warn(t("无法确定任务类型，请在任务中心重新执行"));
                    return;
                  }
                  setTaskDetail(null);
                  void runTask(type as TaskType, [taskDetail.uid]);
                }}
              >
                <Play size={12} strokeWidth={2} /> {t("重新执行")}
              </button>
              <button className="btn btn-ghost" onClick={() => void refreshDetail(taskDetail.uid)}>{t("打开账号详情")}</button>
              <button className="btn btn-primary" onClick={() => setTaskDetail(null)}>{t("关闭")}</button>
            </div>
          </div>
        </div>,
      document.body)}

      {taskCenter && createPortal(
        <div className="overlay" onMouseDown={(e) => e.target === e.currentTarget && setTaskCenter(false)}>
          <div className="dialog tc-dlg">
            <div className="dlg-head">
              <div>
                <h3>{t("任务中心")}</h3>
                <p>
                  {t("任务在后台异步执行，完成后结果自动显示在「最近一次任务结果」")}
                </p>
              </div>
              <button className="icon-btn" onClick={() => setTaskCenter(false)}><X size={14} strokeWidth={2.2} /></button>
            </div>
            <div className="dlg-body">
              <div className="tc-scope">
                {selected.size > 0
                  ? t("已勾选 {n} 个账号：签到 / 旅行 / 保活只对这些账号执行", { n: selected.size })
                  : t("未勾选账号：签到 / 旅行 / 保活需先在列表勾选，其余任务对全池执行")}
              </div>
              {(prog || progRows.length > 0) && (
                <div className="tc-prog">
                  <div className="tc-prog-head">
                    {prog ? (
                      <>
                        <Loader2 size={13} className="spin" />
                        <b>{t("{label} 执行中", { label: t(TASK_LABEL[prog.type] ?? prog.type) })}</b>
                        <span className="mono">{prog.index}/{prog.total}</span>
                      </>
                    ) : (
                      <b>{t("上一轮逐账号结果")}</b>
                    )}
                  </div>
                  <div className="tc-prog-rows">
                    {progRows.map((r) => (
                      <div
                        className={`task-detail-row${r.status === "failed" ? " clickable" : ""}`}
                        key={r.uid}
                        data-tip={r.status === "failed" ? t("点击查看失败详情") : undefined}
                        onClick={() => {
                          if (r.status !== "failed") return;
                          setTaskDetailType(prog?.type ?? "");
                          setTaskDetail({ uid: r.uid, status: r.status, message: r.message });
                        }}
                      >
                        <button
                          className="td-uid"
                          data-tip={t("查看账号详情（{uid}）", { uid: r.uid })}
                          onClick={(e) => {
                            e.stopPropagation();
                            void refreshDetail(r.uid);
                          }}
                        >
                          {nameOf(r.uid)}
                        </button>
                        <span className={`badge ${r.status === "success" ? "b-green" : r.status === "failed" ? "b-red" : "b-amber"}`}>
                          <span className="d" />{r.status === "success" ? t("成功") : r.status === "failed" ? t("失败") : t("跳过")}
                        </span>
                        <span className="td-msg">{r.message}</span>
                        {r.status === "failed" && <span className="td-more">{t("详情")} ›</span>}
                      </div>
                    ))}
                  </div>
                </div>
              )}
              <div className="tc-list">
                {ALL_TASKS.map((task) => {
                  const locked = (task.needSel && selected.size === 0) || busyTypes.includes(task.type);
                  return (
                    <div className={`tc-item${locked ? " locked" : ""}`} key={task.type}>
                      <div className="tc-info">
                        <div className="tc-name">{t(task.label)}</div>
                        <div className="tc-desc">{t(task.desc)}</div>
                      </div>
                      {busyTypes.includes(task.type) ? (
                        <span className="badge b-amber"><span className="d" />{t("执行中")}</span>
                      ) : doneToday[task.type] ? (
                        <span className="badge b-green" data-tip={t("今天 {time} 已成功执行过", { time: doneToday[task.type] })}>
                          <span className="d" />{t("已完成 {time}", { time: doneToday[task.type] })}
                        </span>
                      ) : (
                        <span className="chip">{selected.size > 0 ? t("已选 {n} 个", { n: selected.size }) : task.needSel ? t("需先勾选") : t("全池")}</span>
                      )}
                      <button
                        className="btn btn-soft sm"
                        disabled={locked}
                        data-tip={busyTypes.includes(task.type) ? t("该任务正在执行中") : locked ? t("先在列表勾选账号") : doneToday[task.type] ? t("今天已执行过，可再次执行{label}", { label: t(task.label) }) : t("执行{label}", { label: t(task.label) })}
                        onClick={() => {
                          setProgRows([]); // 新一轮：清空上一轮逐账号进度
                          void runTask(task.type, [...selected]);
                        }}
                      >
                        <Play size={12} strokeWidth={2} /> {doneToday[task.type] ? t("再执行") : t("执行")}
                      </button>
                    </div>
                  );
                })}
              </div>
            </div>
            <div className="dlg-foot">
              <button
                className="btn btn-primary foot-left"
                disabled={runAllBusy}
                data-tip={runAllBusy ? t("全部任务正在按队列执行") : t("按顺序执行当前所有可运行任务")}
                onClick={runAllTasks}
              >
                {runAllBusy ? <Loader2 size={13} className="spin" /> : <Play size={12} strokeWidth={2} />}
                {runAllBusy ? t("全部执行中…") : t("一键全部执行")}
              </button>
              <button className="btn btn-ghost" onClick={() => openTaskCenter()}>{t("刷新状态")}</button>
              <button className="btn btn-ghost" onClick={() => setTaskCenter(false)}>{t("关闭")}</button>
            </div>
          </div>
        </div>,
      document.body)}

      {oauth && createPortal(
        <div className="overlay" onMouseDown={(e) => e.target === e.currentTarget && (stopOAuthPolling(), setOauth(null))}>
          <div className="dialog oauth-dlg">
            <div className="dlg-head">
              <div>
                <h3>{t("接入账号")}</h3>
                <p>{t("选择官方登录站点，授权后凭证会自动进入账号池")}</p>
              </div>
              <button
                className="icon-btn"
                onClick={() => {
                  stopOAuthPolling();
                  setOauth(null);
                }}
              >
                <X size={14} strokeWidth={2.2} />
              </button>
            </div>
            <div className="dlg-body">
              {oauth.phase === "pick" && (
                <>
                  <div className="oauth-tip">
                    {t("微信扫码 / 手机号登录均在官方页面完成，本应用不介入账号密码；下方二维码与链接等价，扫码即登录。")}
                  </div>
                  <div className="oauth-region">
                    <button
                      className="oauth-card"
                      onClick={() => void startOAuth("cn")}
                    >
                      <span className="oauth-card-top"><Globe size={14} strokeWidth={2} /><b>{t("国内版")}</b></span>
                      <span className="oauth-card-url">copilot.tencent.com</span>
                    </button>
                    <button
                      className="oauth-card"
                      onClick={() => void startOAuth("global", globalRegion)}
                    >
                      <span className="oauth-card-top"><Globe size={14} strokeWidth={2} /><b>{t("国际版")}</b></span>
                      <span className="oauth-card-url">www.workbuddy.ai</span>
                    </button>
                  </div>
                  {pickedGlobal && (
                    <div className="oauth-region-picker">
                      <div className="muted" style={{ fontSize: 12 }}>
                        {t("国际版账号归属地区（未注册地区的账号聊天会报 14017）：")}
                      </div>
                      <div className="flex" style={{ gap: 8 }}>
                        <select
                          className="search-input"
                          value={globalRegion}
                          onChange={(e) => setGlobalRegion(e.target.value)}
                          style={{ flex: 1 }}
                        >
                          <option value="">{t("选择地区…")}</option>
                          {INTERNATIONAL_REGIONS.map((r) => (
                            <option key={r.code} value={r.code}>{t(r.label)}（{r.code}）</option>
                          ))}
                        </select>
                        <button
                          className="btn btn-primary sm"
                          disabled={!globalRegion}
                          onClick={() => void startOAuth("global", globalRegion)}
                        >
                          {t("生成二维码")}
                        </button>
                      </div>
                    </div>
                  )}
                </>
              )}
              {oauth.phase === "waiting" && (
                <div className="oauth-waiting">
                  <div className="oauth-waiting-main">
                    <div className="oauth-qr">
                      {oauth.url && <QRCodeSVG value={oauth.url} size={156} level="M" />}
                    </div>
                    <div className="oauth-waiting-info">
                      <div className="oauth-waiting-head">
                        <Loader2 size={18} className="spin" />
                        <div>
                          <b>{t("等待扫码授权…")}</b>
                          <div className="muted" style={{ fontSize: 12, marginTop: 4 }}>
                            {t("用手机扫上方二维码（{region}）· 已等待 {sec} 秒", { region: oauth.region === "cn" ? t("国内版") : `${t("国际版")} · ${globalRegion}`, sec: oauth.elapsed })}
                          </div>
                        </div>
                      </div>
                      {oauth.url && <div className="oauth-url mono" data-tip={oauth.url}>{oauth.url}</div>}
                    </div>
                  </div>
                  {oauth.url && (
                    <div className="flex" style={{ gap: 8 }}>
                        <button className="btn btn-primary sm" onClick={() => void openInAppLogin()}>
                          <MonitorSmartphone size={12} strokeWidth={2} /> {t("应用内登录（手机号）")}
                        </button>
                        <button className="btn btn-soft sm" onClick={() => void copyOAuthURL()}>
                          {copied ? <Check size={12} strokeWidth={2.4} /> : <Copy size={12} strokeWidth={2} />}
                          {copied ? t("已复制") : t("复制链接")}
                        </button>
                        <button className="btn btn-ghost sm" onClick={() => oauth.url && void accountsApi.openURL(oauth.url)}>
                          <ExternalLink size={12} strokeWidth={2} /> {t("在浏览器打开")}
                        </button>
                      </div>
                  )}
                  <div className="muted" style={{ fontSize: 12, lineHeight: 1.6 }}>
                    {t("在手机上完成登录后，凭证会自动落盘并加入账号池，本窗口将自动关闭。二维码约 15 分钟有效，过期请重新生成。")}
                  </div>
                  <div className="flex" style={{ gap: 8 }}>
                    <button className="btn btn-ghost sm" onClick={() => void accountsApi.openAuthDir()}>{t("打开凭证目录")}</button>
                    <button
                      className="btn btn-ghost sm"
                      onClick={() => {
                        stopOAuthPolling();
                        oauthRetries.current = 0;
                        setPickedGlobal(oauth.region === "global");
                        setOauth({ region: oauth.region, phase: "pick", elapsed: 0 });
                      }}
                    >
                      {t("重新选择站点")}
                    </button>
                  </div>
                </div>
              )}
              {oauth.phase === "success" && (
                <div className="oauth-final">
                  <div className="oauth-qr">
                    <CircleCheck size={64} strokeWidth={1.6} />
                  </div>
                  <div>
                    <b>{oauth.updated ? t("凭证已更新") : t("添加成功")}</b>
                    <div className="muted" style={{ fontSize: 12, marginTop: 4 }}>
                      {(oauth.nickname || t("账号"))} {t("已加入账号池")}
                    </div>
                    {oauth.note && (
                      <div className="muted" style={{ fontSize: 12, marginTop: 4, lineHeight: 1.6 }}>{oauth.note}</div>
                    )}
                  </div>
                </div>
              )}
              {oauth.phase === "error" && (
                <>
                  <div className="oauth-final">
                    <div className="oauth-qr">
                      <TriangleAlert size={56} strokeWidth={1.6} />
                    </div>
                    <div>
                      <b>{t("授权未完成")}</b>
                      <div className="muted" style={{ fontSize: 12, marginTop: 4, lineHeight: 1.6 }}>{t(oauth.message ?? "")}</div>
                    </div>
                  </div>
                  <div className="flex" style={{ gap: 8 }}>
                    <button className="btn btn-primary sm" onClick={regenOAuth}>
                      {t("重新生成二维码")}
                    </button>
                    <button
                      className="btn btn-ghost sm"
                      onClick={() => {
                        stopOAuthPolling();
                        oauthRetries.current = 0;
                        setPickedGlobal(oauth.region === "global");
                        setOauth({ region: oauth.region, phase: "pick", elapsed: 0 });
                      }}
                    >
                      {t("重新选择站点")}
                    </button>
                  </div>
                </>
              )}
            </div>
          </div>
        </div>,
      document.body)}
    </section>
  );
}

function formatLeft(secs: number): string {
  const d = Math.floor(secs / 86400);
  if (d >= 1) return t("{d} 天 {h} 小时", { d, h: Math.floor((secs % 86400) / 3600) });
  return t("{h} 小时 {m} 分", { h: Math.floor(secs / 3600), m: Math.floor((secs % 3600) / 60) });
}

/** 状态徽章：冷却中的账号在徽章内显示动态倒计时（UI-DESIGN.md §4，account:statusChanged 事件驱动列表刷新） */
function StatusBadge({ account }: { account: Account }) {
  const t = useT();
  if (account.status === "cooldown" && account.cooldownUntil) {
    return <CooldownBadge until={account.cooldownUntil} note={account.note} />;
  }
  const badge = STATUS_BADGE[account.status] ?? STATUS_BADGE.unknown;
  return (
    <span className={`badge ${badge.cls}`} data-tip={account.note}>
      <span className="d" />
      {t(badge.text)}
    </span>
  );
}

function CooldownBadge({ until, note }: { until: string; note?: string }) {
  const t = useT();
  const [, tick] = useReducer((x: number) => x + 1, 0);
  useEffect(() => {
    const timer = setInterval(tick, 1000);
    return () => clearInterval(timer);
  }, []);
  const target = new Date(until).getTime();
  const remain = Math.max(0, Math.floor((target - Date.now()) / 1000));
  const text =
    remain <= 0
      ? t("冷却到期")
      : remain >= 3600
        ? t("冷却 {h}h{m}m", { h: Math.floor(remain / 3600), m: String(Math.floor((remain % 3600) / 60)).padStart(2, "0") })
        : t("冷却 {mm}:{ss}", { mm: String(Math.floor(remain / 60)).padStart(2, "0"), ss: String(remain % 60).padStart(2, "0") });
  return (
    <span className={`badge ${remain <= 0 ? "b-green" : "b-amber"}`} data-tip={note}>
      <span className="d" />
      {text}
    </span>
  );
}

/** 积分列：余额 + 临期预警。72h 内有到期积分时黄色副行提示并给「建议优先使用」
 * 徽标（数据来自调度器巡检 patrolExpiry，对齐 workbuddy-switch 的到期优先视图）。
 * 无到期信息时 data-tip 展示最早到期日。 */
function CreditCell({ account, loading, onRefresh }: { account: Account; loading: boolean; onRefresh: () => void }) {
  const t = useT();
  const expiring = account.creditsExpiring ?? 0;
  const expireDay = account.creditsExpireDay ?? "";
  const tip = expireDay
    ? t("最早 {day} 到期 {n} 分", { day: expireDay, n: (account.creditsExpireRemain ?? 0).toLocaleString() })
    : account.creditsKnown ? t("点击刷新上游真实余额") : t("立即查询上游真实余额");
  return (
    <div style={{ display: "inline-flex", alignItems: "center", gap: 6 }}>
      <button
        className="btn btn-ghost sm"
        disabled={loading}
        data-tip={tip}
        onClick={onRefresh}
      >
        {loading
          ? <Loader2 size={12} className="spin" />
          : account.creditsKnown ? account.credits.toLocaleString() : t("查询")}
      </button>
      {account.creditsKnown && expiring > 0 && (
        <span className="badge b-amber" data-tip={t("72h 内到期 {n} 分，先用这部分", { n: expiring.toLocaleString() })}>
          <span className="d" />{t("建议优先")}
        </span>
      )}
    </div>
  );
}

function TokenExpiryCell({ expiresIn, expiry }: { expiresIn: number; expiry: string }) {  const t = useT();
  if (expiresIn <= 0) {
    return (
      <div className="quota">
        <div className="qt"><span>{t("已过期")}</span><span>0%</span></div>
        <div className="qb"><i style={{ width: 0 }} /></div>
      </div>
    );
  }
  // 以 30 天为刻度展示剩余比例（凭证有效期通常是短期 access token）
  const pct = Math.min(100, Math.round((expiresIn / (30 * 86400)) * 100));
  const color = expiresIn < 7 * 86400 ? "var(--red)" : expiresIn < 21 * 86400 ? "var(--amber)" : "var(--green)";
  return (
    <div className="quota" data-tip={expiry}>
      <div className="qt"><span>{formatLeft(expiresIn)}</span><span>{pct}%</span></div>
      <div className="qb"><i style={{ width: `${pct}%`, background: color }} /></div>
    </div>
  );
}

function KV({ k, v }: { k: string; v: string }) {
  const t = useT();
  return (
    <div className="kv">
      <span>{t(k)}</span>
      <b className="mono">{v}</b>
    </div>
  );
}

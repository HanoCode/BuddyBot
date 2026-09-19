// ============================================================
// WorkBuddy Desktop 悬浮机器人（注入到官方客户端页面内运行）
//
// 参考 WorkDaddy 悬浮组件的做法：
//   1. CSS 绘制的机器人按钮（圆角外壳 + 眨眼双目 + 天线），可拖拽，
//      释放后水平吸附回右缘，垂直位置持久化
//   2. 闲置 5 秒自动停靠进右缘（只露头部），鼠标靠近边缘展开
//   3. 点击机器人弹出白色主题多 Tab 面板（账号 / 任务 / 模型 / 设置），
//      面板打开时机器人隐藏，关闭后恢复
//   4. 账号池有 72h 内到期积分时机器人头顶亮琥珀色徽标
//   5. 通过 CDP binding（window.__wbdesk）把动作回传给桌面端
//
// 幂等：重复注入前先执行 __wbdeskCleanup 清理旧实例。
// 与官方 DOM 的耦合只集中在「确认按钮文案」启发式，客户端升级
// 失效时只影响免打扰，不影响其余功能。
// ============================================================
(function () {
  "use strict";
  if (window.__wbdeskCleanup) { try { window.__wbdeskCleanup(); } catch (e) {} }

  var BINDING = "__wbdesk";
  var EDGE_GAP = 18;          // 机器人距右/下缘的间距
  var DRAG_THRESHOLD = 6;     // 拖拽判定阈值（px），以内视为点击
  var QUIET_AFTER_MS = 5000;  // 闲置多久自动停靠
  var FAB_W = 44, FAB_H = 34; // 机器人本体尺寸（未缩放）

  var TASK_LABEL = {
    checkin: "每日签到", travel: "猫猫旅行", keepalive: "保活刷新",
    activity: "活跃地图", school: "开学季", cat: "夜猫子", growth: "成长任务",
  };
  // 面板可执行的任务（与桌面端 binding 契约一致，全量 7 种）
  var TASKS = ["checkin", "travel", "keepalive", "activity", "school", "cat", "growth"];

  var state = {
    enh: { dnd: false, file: false, cmd: false, del: false, sys: false, resume: false, quote: false, nav: false },
    sys: { awake: false }, // 防休眠等系统状态（桌面端推送）
    accounts: [],
    pool: [],
    tasks: null,        // { busy:[], history:[] }（桌面端推送）
    models: null,       // { url, models:[] }（桌面端推送）
    tab: "account",     // 当前 Tab
    open: false,        // 面板是否打开
    dragging: false,
    moved: false,
    suppressClick: false,
    quiet: false,       // 是否已停靠
    near: false,        // 指针是否靠近右缘（停靠唤醒区）
    quietTimer: null,
    cleanup: [],
  };

  // ---------- 回传桌面端 ----------
  function send(action, data) {
    try {
      window[BINDING](JSON.stringify(Object.assign({ action: action }, data || {})));
    } catch (e) { /* binding 未注册时静默 */ }
  }

  // ---------- 样式 ----------
  var css = [
    /* ---- 机器人按钮 ---- */
    "#wbdesk-fab{position:fixed;z-index:2147483647;right:" + EDGE_GAP + "px;bottom:" + EDGE_GAP + "px;",
    "width:" + FAB_W + "px;height:" + FAB_H + "px;cursor:grab;touch-action:none;user-select:none;-webkit-user-select:none;",
    "transition:right .45s cubic-bezier(.22,1,.36,1),transform .45s cubic-bezier(.22,1,.36,1),opacity .18s ease;will-change:right,transform;}",
    "#wbdesk-fab.dragging{cursor:grabbing;transition:none;}",
    "#wbdesk-fab.snapping{transition:right .45s cubic-bezier(.22,1,.36,1),transform .45s cubic-bezier(.22,1,.36,1);}",
    "#wbdesk-fab.hidden{opacity:0;pointer-events:none;}",
    /* 停靠：向右平移出窗口，只露 6px 头部（用 transform 避开内联 right 的优先级）；天线隐藏 */
    "#wbdesk-fab.quiet{transform:translateX(" + (FAB_W - 6 + EDGE_GAP) + "px);}",
    "#wbdesk-fab.quiet .wb-antenna,#wbdesk-fab.quiet .wb-antenna-dot{opacity:0;}",
    ".wb-robot{position:relative;width:100%;height:100%;border-radius:15px;",
    "background:linear-gradient(150deg,var(--rb-1,#2b3550),var(--rb-2,#1c2438));",
    "box-shadow:inset 0 2px 0 rgba(255,255,255,.14),0 8px 18px rgba(28,36,56,.5);}",
    ".wb-antenna{position:absolute;left:50%;top:-9px;width:5px;height:11px;transform:translateX(-50%);",
    "border-radius:5px;background:var(--rb-1,#2b3550);transition:opacity .3s;pointer-events:none;}",
    ".wb-antenna-dot{position:absolute;left:50%;top:-15px;width:9px;height:9px;transform:translateX(-50%);",
    "border-radius:50%;background:var(--rb-dot,#478cbf);box-shadow:0 0 8px var(--rb-dot-glow,rgba(71,140,191,.9));",
    "transition:opacity .3s;pointer-events:none;animation:wb-glow 3.2s ease-in-out infinite;}",
    ".wb-face{position:absolute;inset:0;display:flex;align-items:center;justify-content:center;gap:14px;}",
    ".wb-eye{display:block;width:10px;height:15px;border-radius:5px;background:#eaf2fb;",
    "animation:wb-blink 7s infinite;transition:transform .18s ease;}",
    "#wbdesk-fab:hover .wb-eye{transform:scale(1.18);}",
    "@keyframes wb-blink{0%,43%,47%,100%{transform:scaleY(1)}45%{transform:scaleY(.15)}}",
    "@keyframes wb-glow{0%,100%{box-shadow:0 0 6px rgba(71,140,191,.7)}50%{box-shadow:0 0 12px rgba(71,140,191,1)}}",
    /* 到期提醒徽标 */
    "#wbdesk-fab-badge{display:none;position:absolute;right:-4px;top:-4px;width:12px;height:12px;",
    "border-radius:50%;background:#f59e0b;border:2px solid rgba(255,255,255,.85);",
    "box-shadow:0 1px 6px rgba(245,158,11,.6);}",
    "#wbdesk-fab-badge.show{display:block;}",
    /* ---- 多 Tab 面板（CSS 变量主题化，参考 WorkDaddy 主题系统） ---- */
    "#wbdesk-panel{--p-fg:#f2f3f5;--p-sub:#8b8f99;--p-line:rgba(255,255,255,.08);--p-line-soft:rgba(255,255,255,.06);",
    "--p-card:rgba(255,255,255,.05);--p-row:rgba(255,255,255,.06);--p-input:rgba(255,255,255,.06);",
    "--p-btn:#478cbf;--p-ghost:rgba(71,140,191,.22);--p-ghost-fg:#8fc1e3;",
    "--p-bg:rgba(19,20,23,.78);--p-tab-on-bg:#f2f3f5;--p-tab-on-fg:#17181c;",
    "--p-shadow:0 16px 48px rgba(0,0,0,.55);--p-border:rgba(255,255,255,.09);--p-sw:#3a3d46;--p-sw-on:#e8eaf0;",
    "position:fixed;z-index:2147483647;right:" + EDGE_GAP + "px;bottom:" + (EDGE_GAP + 10) + "px;width:420px;height:560px;max-height:88vh;",
    "display:none;flex-direction:column;border-radius:16px;overflow:hidden;",
    "font:12.5px/1.6 -apple-system,'Segoe UI',sans-serif;color:var(--p-fg);",
    /* 半透明毛玻璃：backdrop-filter 失效时由 .78 透明度兜底 */
    "background:var(--p-bg);-webkit-backdrop-filter:blur(24px) saturate(1.4);backdrop-filter:blur(24px) saturate(1.4);",
    "box-shadow:var(--p-shadow);border:1px solid var(--p-border);background-size:cover;background-position:center;}",
    /* 浅色主题（主题 Tab 或跟随系统切换） */
    "#wbdesk-panel.light{--p-fg:#1b1e24;--p-sub:#69707d;--p-line:rgba(0,0,0,.09);--p-line-soft:rgba(0,0,0,.06);",
    "--p-card:rgba(0,0,0,.035);--p-row:rgba(0,0,0,.04);--p-input:rgba(0,0,0,.05);",
    "--p-bg:rgba(248,249,252,.84);--p-tab-on-bg:#1b1e24;--p-tab-on-fg:#ffffff;",
    "--p-shadow:0 16px 48px rgba(31,41,55,.28);--p-border:rgba(0,0,0,.08);--p-sw:#c9cdd6;--p-sw-on:#1b1e24;}",
    /* 壁纸生效时压暗/提亮底色保证可读性（覆盖毛玻璃底） */
    "#wbdesk-panel.walled{background-color:rgba(15,17,22,.66);}",
    "#wbdesk-panel.light.walled{background-color:rgba(248,249,252,.72);}",
    "#wbdesk-panel .p-head{display:flex;justify-content:space-between;align-items:center;padding:12px 14px 8px;}",
    "#wbdesk-panel .p-title{font-size:13.5px;font-weight:600;display:flex;align-items:center;gap:6px;}",
    "#wbdesk-panel .p-close{border:none;background:transparent;color:var(--p-sub);font-size:14px;cursor:pointer;",
    "padding:2px 6px;border-radius:6px;line-height:1;}",
    "#wbdesk-panel .p-close:hover{background:var(--p-row);color:var(--p-fg);}",
    "#wbdesk-panel .p-tabs{display:flex;gap:4px;padding:2px 10px 8px;border-bottom:1px solid var(--p-line);}",
    "#wbdesk-panel .p-tab{flex:1;border:none;background:transparent;cursor:pointer;font-size:12px;color:var(--p-sub);",
    "padding:5px 0 6px;display:flex;align-items:center;justify-content:center;gap:4px;border-radius:8px;}",
    "#wbdesk-panel .p-tab.on{background:var(--p-tab-on-bg);color:var(--p-tab-on-fg);font-weight:600;}",
    "#wbdesk-panel .p-body{flex:1;min-height:0;padding:10px 14px 14px;overflow:auto;}",
    "#wbdesk-panel .p-body::-webkit-scrollbar{width:4px;}",
    "#wbdesk-panel .p-body::-webkit-scrollbar-thumb{background:rgba(128,128,128,.35);border-radius:2px;}",
    "#wbdesk-panel .p-body::-webkit-scrollbar-track{background:transparent;}",
    "#wbdesk-panel .pane{display:none;}",
    "#wbdesk-panel .pane.on{display:block;}",
    "#wbdesk-panel .row{display:flex;justify-content:space-between;align-items:center;gap:8px;padding:4px 0;}",
    "#wbdesk-panel .blk-t{font-weight:600;font-size:12.5px;margin:2px 0 4px;}",
    "#wbdesk-panel .acct{padding:6px 8px;border:1px solid var(--p-line);border-radius:8px;margin:4px 0;",
    "display:flex;justify-content:space-between;align-items:center;gap:6px;background:var(--p-row);}",
    "#wbdesk-panel button{border:none;border-radius:6px;padding:3px 8px;cursor:pointer;font-size:11.5px;",
    "background:var(--p-btn);color:#fff;}",
    "#wbdesk-panel button:disabled{opacity:.55;cursor:default;}",
    "#wbdesk-panel button.ghost{background:var(--p-ghost);color:var(--p-ghost-fg);}",
    "#wbdesk-panel button.danger{background:rgba(239,68,68,.16);color:#f87171;}",
    "#wbdesk-panel input{width:100%;box-sizing:border-box;border:1px solid var(--p-line);border-radius:6px;",
    "padding:4px 6px;font-size:12px;margin:6px 0;background:var(--p-input);color:var(--p-fg);}",
    "#wbdesk-panel input::placeholder{color:var(--p-sub);}",
    "#wbdesk-panel .sw{width:34px;height:19px;border-radius:10px;background:var(--p-sw);position:relative;",
    "cursor:pointer;flex:none;transition:background .15s;}",
    "#wbdesk-panel .sw.on{background:var(--p-sw-on);}",
    "#wbdesk-panel .sw i{position:absolute;top:2px;left:2px;width:15px;height:15px;border-radius:50%;background:#9aa0ad;",
    "transition:left .15s;}",
    "#wbdesk-panel .sw.on i{left:17px;background:#17181c;}",
    "#wbdesk-panel .sec{border-top:1px solid var(--p-line);margin:8px 0;padding-top:8px;}",
    "#wbdesk-panel .tip{color:var(--p-sub);font-size:11px;}",
    "#wbdesk-panel .badge{display:inline-block;border-radius:6px;padding:1px 6px;font-size:10.5px;font-weight:600;}",
    "#wbdesk-panel .badge.ok{background:rgba(16,185,129,.2);color:#34d399;}",
    "#wbdesk-panel .badge.busy{background:rgba(245,158,11,.2);color:#fbbf24;}",
    "#wbdesk-panel .badge.fail{background:rgba(239,68,68,.18);color:#f87171;}",
    "#wbdesk-panel .badge.dim{background:rgba(128,128,128,.18);color:var(--p-sub);}",
    "#wbdesk-panel .gw{font:11px/1.4 ui-monospace,SFMono-Regular,monospace;background:var(--p-input);",
    "border-radius:8px;padding:6px 8px;cursor:pointer;word-break:break-all;color:var(--p-fg);}",
    "#wbdesk-panel .gw:hover{background:var(--p-ghost);}",
    /* 增强分组卡片（参考 WorkDaddy「增强」页：深色圆角卡片 + 分隔线） */
    "#wbdesk-panel .card{background:var(--p-card);border-radius:12px;padding:2px 12px;margin:0 0 10px;}",
    "#wbdesk-panel .card .row{padding:9px 0;border-bottom:1px solid var(--p-line-soft);}",
    "#wbdesk-panel .card .row:last-child{border-bottom:none;}",
    /* 增强分组头（标题 + 已开启计数 + 全部开） */
    "#wbdesk-panel .grp{display:flex;justify-content:space-between;align-items:center;margin:2px 0 6px;}",
    "#wbdesk-panel .grp-t{font-weight:600;font-size:12.5px;}",
    "#wbdesk-panel .grp-n{color:var(--p-sub);font-size:11px;font-weight:400;margin-left:4px;}",
    "#wbdesk-panel .lbl b{font-weight:600;}",
    "#wbdesk-panel .lbl em{font-style:normal;color:var(--p-sub);font-size:11px;margin-left:5px;}",
    /* ---- 主题 Tab（外观 / 头像 / 壁纸，参考 WorkDaddy「主题」页） ---- */
    "#wbdesk-panel .seg2{display:flex;gap:4px;background:var(--p-input);border-radius:8px;padding:2px;}",
    "#wbdesk-panel .seg2 button{flex:1;background:transparent;color:var(--p-sub);border-radius:6px;padding:4px 0;}",
    "#wbdesk-panel .seg2 button.on{background:var(--p-tab-on-bg);color:var(--p-tab-on-fg);font-weight:600;}",
    "#wbdesk-panel .swatches{display:flex;gap:8px;flex-wrap:wrap;padding:2px 0;}",
    "#wbdesk-panel .swatch{width:26px;height:26px;border-radius:9px;cursor:pointer;border:none;padding:0;",
    "outline:2px solid transparent;outline-offset:1px;}",
    "#wbdesk-panel .swatch.on{outline-color:var(--p-fg);}",
    "#wbdesk-panel .walls{display:grid;grid-template-columns:repeat(5,1fr);gap:8px;padding:2px 0;}",
    "#wbdesk-panel .wall{height:44px;border-radius:9px;cursor:pointer;border:none;padding:0;position:relative;",
    "outline:2px solid transparent;outline-offset:1px;display:flex;align-items:center;justify-content:center;}",
    "#wbdesk-panel .wall.on{outline-color:var(--p-fg);}",
    "#wbdesk-panel .wall span{font-size:10px;color:rgba(255,255,255,.85);text-shadow:0 1px 3px rgba(0,0,0,.5);}",
    /* 模型搜索 + 行操作 */
    "#wbdesk-panel .mname{overflow:hidden;text-overflow:ellipsis;white-space:nowrap;}",
    /* 引用消息悬浮按钮 */
    "#wbdesk-quote{position:fixed;z-index:2147483647;display:none;border:none;border-radius:14px;",
    "background:#f2f3f5;color:#17181c;font-size:11.5px;padding:4px 12px;cursor:pointer;",
    "box-shadow:0 4px 14px rgba(0,0,0,.45);font-family:inherit;}",
    "#wbdesk-quote:hover{background:#fff;}",
    /* 主题 Tab：外观分段选择 / 机器人配色色板 / 壁纸宫格 */
    "#wbdesk-panel .seg2{display:flex;gap:4px;flex:none;}",
    "#wbdesk-panel .seg2 button{background:rgba(255,255,255,.08);color:#8b8f99;padding:3px 10px;}",
    "#wbdesk-panel .seg2 button.on{background:#478cbf;color:#fff;}",
    "#wbdesk-panel .swatches{display:flex;gap:8px;padding:4px 0 6px;}",
    "#wbdesk-panel .swatches span{width:26px;height:26px;border-radius:50%;cursor:pointer;border:2px solid transparent;box-shadow:inset 0 1px 0 rgba(255,255,255,.25);}",
    "#wbdesk-panel .swatches span.on{border-color:#f2f3f5;}",
    "#wbdesk-panel .walls{display:grid;grid-template-columns:repeat(4,1fr);gap:6px;padding:4px 0 6px;}",
    "#wbdesk-panel .walls span{height:44px;border-radius:8px;cursor:pointer;border:2px solid transparent;background-size:cover;background-position:center;}",
    "#wbdesk-panel .walls span.on{border-color:#478cbf;}",
    /* 面板内 toast（备份/切换失败等即时反馈） */
    "#wbdesk-toast{position:fixed;z-index:2147483647;left:50%;top:18px;transform:translateX(-50%);max-width:70vw;",
    "background:rgba(220,38,38,.92);color:#fff;font:12px/1.5 -apple-system,sans-serif;padding:8px 14px;border-radius:10px;",
    "box-shadow:0 8px 24px rgba(0,0,0,.4);display:none;word-break:break-all;}",
    /* 会话消息索引条（参考 WorkDaddy：贴消息列表左缘的定位 rail） */
    "#wbdesk-mnav{position:fixed;z-index:2147483646;display:none;width:18px;padding:6px 0;border-radius:9px;",
    "background:rgba(19,20,23,.35);-webkit-backdrop-filter:blur(8px);backdrop-filter:blur(8px);",
    "flex-direction:column;align-items:center;gap:4px;max-height:60vh;overflow:hidden;}",
    "#wbdesk-mnav .mk{width:8px;height:3px;border-radius:2px;background:rgba(255,255,255,.4);cursor:pointer;flex:none;",
    "transition:width .12s,background .12s;border:none;padding:0;}",
    "#wbdesk-mnav .mk.on{width:14px;background:#478cbf;}",
    "#wbdesk-mnav-tip{position:fixed;z-index:2147483647;display:none;max-width:260px;background:rgba(19,20,23,.92);",
    "color:#f2f3f5;font:11px/1.5 -apple-system,sans-serif;padding:6px 9px;border-radius:8px;",
    "box-shadow:0 6px 20px rgba(0,0,0,.4);pointer-events:none;word-break:break-all;}",
    /* 面板浅色模式（.light 覆盖关键表面色） */
    "#wbdesk-panel.light{background:rgba(246,247,250,.82);color:#1f2430;}",
    "#wbdesk-panel.light .p-close{color:#9aa0ad;}",
    "#wbdesk-panel.light .p-close:hover{background:rgba(0,0,0,.06);color:#1f2430;}",
    "#wbdesk-panel.light .p-tabs{border-bottom-color:rgba(0,0,0,.08);}",
    "#wbdesk-panel.light .p-tab{color:#7a8091;}",
    "#wbdesk-panel.light .p-tab.on{background:#1f2430;color:#fff;}",
    "#wbdesk-panel.light .card{background:rgba(255,255,255,.72);box-shadow:0 1px 3px rgba(0,0,0,.05);}",
    "#wbdesk-panel.light .card .row{border-bottom-color:rgba(0,0,0,.06);}",
    "#wbdesk-panel.light .acct{background:rgba(255,255,255,.8);border-color:rgba(0,0,0,.1);}",
    "#wbdesk-panel.light input{background:#fff;border-color:rgba(0,0,0,.14);color:#1f2430;}",
    "#wbdesk-panel.light input::placeholder{color:#9aa0ad;}",
    "#wbdesk-panel.light .tip,#wbdesk-panel.light .grp-n,#wbdesk-panel.light .lbl em{color:#7a8091;}",
    "#wbdesk-panel.light .sec{border-top-color:rgba(0,0,0,.1);}",
    "#wbdesk-panel.light .gw{background:rgba(0,0,0,.05);color:#3a4150;}",
    "#wbdesk-panel.light .sw{background:#c9ccd6;}",
    "#wbdesk-panel.light .sw.on{background:#478cbf;}",
    "#wbdesk-panel.light .sw i{background:#fff;}",
    "#wbdesk-panel.light .sw.on i{background:#fff;}",
    "#wbdesk-panel.light .seg2 button{background:rgba(0,0,0,.06);color:#7a8091;}",
    "#wbdesk-panel.light .badge.dim{background:rgba(0,0,0,.07);color:#7a8091;}",
    "#wbdesk-panel.light .p-body::-webkit-scrollbar-thumb{background:rgba(0,0,0,.18);}",
    "@media(prefers-reduced-motion:reduce){#wbdesk-fab,.wb-eye,.wb-antenna-dot{animation:none!important;transition:none!important}}",
  ].join("");
  var style = document.createElement("style");
  style.textContent = css;
  document.head.appendChild(style);
  state.cleanup.push(function () { style.remove(); });

  // ---------- 悬浮机器人 ----------
  var fab = document.createElement("div");
  fab.id = "wbdesk-fab";
  fab.title = "BuddyBot 悬浮机器人，点击打开面板，可拖动";
  fab.innerHTML =
    '<span id="wbdesk-fab-badge" title="有积分将在 72h 内到期"></span>' +
    '<div class="wb-robot">' +
    '<span class="wb-antenna"></span><span class="wb-antenna-dot"></span>' +
    '<div class="wb-face"><span class="wb-eye"></span><span class="wb-eye"></span></div>' +
    "</div>";
  document.body.appendChild(fab);
  state.cleanup.push(function () { fab.remove(); });

  // ---------- 多 Tab 面板 ----------
  var ICON = {
    account: '<svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M20 21v-2a4 4 0 0 0-4-4H8a4 4 0 0 0-4 4v2"/><circle cx="12" cy="7" r="4"/></svg>',
    task: '<svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 8V4H9"/><rect x="4" y="8" width="16" height="12" rx="3"/><path d="M2 12v4M22 12v4M9 12v2M15 12v2M9 17h6"/></svg>',
    theme: '<svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="13.5" cy="6.5" r=".5" fill="currentColor"/><circle cx="17.5" cy="10.5" r=".5" fill="currentColor"/><circle cx="8.5" cy="7.5" r=".5" fill="currentColor"/><circle cx="6.5" cy="12.5" r=".5" fill="currentColor"/><path d="M12 2C6.5 2 2 6.5 2 12s4.5 10 10 10c.926 0 1.648-.746 1.648-1.688 0-.437-.18-.835-.437-1.125-.29-.289-.438-.652-.438-1.125a1.64 1.64 0 0 1 1.668-1.668h1.996c3.051 0 5.555-2.503 5.555-5.554C21.965 6.012 17.461 2 12 2z"/></svg>',
    model: '<svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="m12 2 9 5-9 5-9-5 9-5Z"/><path d="m3 12 9 5 9-5"/><path d="m3 17 9 5 9-5"/></svg>',
    enh: '<svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M13 2 3 14h9l-1 8 10-12h-9l1-8z"/></svg>',
    setting: '<svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 1 1-4 0v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06a1.65 1.65 0 0 0 .33-1.82 1.65 1.65 0 0 0-1.51-1H3a2 2 0 1 1 0-4h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06a1.65 1.65 0 0 0 1.82.33H9a1.65 1.65 0 0 0 1-1.51V3a2 2 0 1 1 4 0v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06a1.65 1.65 0 0 0-.33 1.82V9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 1 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1z"/></svg>',
  };

  var panel = document.createElement("div");
  panel.id = "wbdesk-panel";
  panel.innerHTML =
    '<div class="p-head"><div class="p-title">🤖 BuddyBot</div>' +
    '<button class="p-close" id="wbdesk-close" title="关闭">✕</button></div>' +
    '<div class="p-tabs">' +
    tabBtn("account", "账号") + tabBtn("task", "任务") + tabBtn("theme", "主题") + tabBtn("model", "模型") + tabBtn("enh", "增强") + tabBtn("setting", "设置") +
    "</div>" +
    '<div class="p-body">' +
    /* 账号 Tab */
    '<div class="pane" data-pane="account">' +
    '<div class="row"><span class="blk-t">账号池账号</span><button class="ghost" id="wbdesk-pool-refresh">刷新</button></div>' +
    '<div id="wbdesk-pool"></div>' +
    '<div class="tip">实时余额与当前登录标记；「设为登录」把该账号写入官方登录位并重启客户端（当前登录文件会先自动备份）。</div>' +
    '<div class="sec"></div>' +
    '<div class="row"><span class="blk-t">登录态备份</span><button class="ghost" id="wbdesk-refresh">刷新</button></div>' +
    '<input id="wbdesk-name" placeholder="当前账号备注名（备份用，可留空）"/>' +
    '<button id="wbdesk-backup" style="width:100%">备份当前登录</button>' +
    '<div id="wbdesk-list"></div>' +
    '<div class="tip">备份=快照官方登录文件（无需注入）；切换=校验后写入登录位并重启客户端，跨通道账号会被拒绝。</div>' +
    "</div>" +
    /* 任务 Tab（全量任务 + 一键执行） */
    '<div class="pane" data-pane="task">' +
    '<div class="row"><span class="blk-t">全部任务（全池执行）</span>' +
    '<span><button class="ghost" id="wbdesk-tasks-refresh">刷新</button> ' +
    '<button id="wbdesk-task-all" data-tip="按顺序执行全部可运行任务">▶ 全部执行</button></span></div>' +
    '<div id="wbdesk-tasks"></div>' +
    '<div class="sec"></div>' +
    '<div class="blk-t">最近运行</div>' +
    '<div id="wbdesk-history"></div>' +
    "</div>" +
    /* 主题 Tab（外观 / 头像 / 壁纸，参考 WorkDaddy「主题」页） */
    '<div class="pane" data-pane="theme">' +
    '<div class="grp"><span class="grp-t">外观</span></div>' +
    '<div class="card"><div class="row">' +
    '<span class="lbl"><b>面板主题</b><em>深色 / 浅色 / 跟随系统</em></span>' +
    '<span class="seg2" id="wbdesk-mode">' +
    '<button data-mode="dark">深色</button><button data-mode="light">浅色</button><button data-mode="auto">自动</button>' +
    "</span></div></div>" +
    '<div class="grp"><span class="grp-t">头像</span></div>' +
    '<div class="card"><div class="row"><span class="lbl"><b>机器人配色</b><em>悬浮机器人与面板标识</em></span></div>' +
    '<div class="swatches" id="wbdesk-avatars"></div></div>' +
    '<div class="grp"><span class="grp-t">壁纸</span></div>' +
    '<div class="card"><div class="row"><span class="lbl"><b>面板壁纸</b><em>预设渐变或自定义图片</em></span></div>' +
    '<div class="walls" id="wbdesk-walls"></div>' +
    '<div class="row" style="padding:8px 0 6px">' +
    '<button class="ghost" id="wbdesk-wall-file" style="flex:1">上传自定义壁纸</button>' +
    '<button class="danger" id="wbdesk-wall-clear">还原</button></div>' +
    '<div class="tip">自定义图片保存在本机（约 1.5MB 内），不上传服务器。</div>' +
    "</div>" +
    "</div>" +
    /* 模型 Tab */
    '<div class="pane" data-pane="model">' +
    '<div class="row"><span class="blk-t">网关接入地址</span><button class="ghost" id="wbdesk-models-refresh">刷新</button></div>' +
    '<div class="gw" id="wbdesk-gw" title="点击复制">—</div>' +
    '<div class="tip" style="margin:4px 0 8px">OpenAI 兼容端点，点击复制；密钥在桌面端「API 密钥」创建。</div>' +
    '<div class="row" style="margin:2px 0 4px"><span class="blk-t" style="margin:0">可用模型（<span id="wbdesk-model-count">0</span>）</span></div>' +
    '<input id="wbdesk-model-search" placeholder="搜索模型 ID…"/>' +
    '<div id="wbdesk-models"></div>' +
    '<div class="tip">点击模型行复制 ID；网关模型供外部工具（Claude Code 等）接入使用。</div>' +
    "</div>" +
    /* 增强 Tab（分组开关参考 WorkDaddy「增强」页） */
    '<div class="pane" data-pane="enh">' +
    '<div class="grp"><span class="grp-t">免打扰<span class="grp-n" id="wbdesk-dnd-n"></span></span>' +
    '<button class="ghost" id="wbdesk-dnd-all">全部开</button></div>' +
    '<div class="card">' +
    '<div class="row"><span class="lbl"><b>弹窗自动点允许</b><em>确认弹窗自动允许</em></span><span class="sw" data-enh="dnd"><i></i></span></div>' +
    '<div class="row"><span class="lbl"><b>沙箱外写文件免确认</b><em>工作区外文件直接写入</em></span><span class="sw" data-enh="file"><i></i></span></div>' +
    '<div class="row"><span class="lbl"><b>常用命令行免确认</b><em>常用命令直接执行</em></span><span class="sw" data-enh="cmd"><i></i></span></div>' +
    '<div class="row"><span class="lbl"><b>大批量删除免确认</b><em>批量删除直接进回收站</em></span><span class="sw" data-enh="del"><i></i></span></div>' +
    '<div class="row"><span class="lbl"><b>系统级工具放行</b><em>系统管理命令直接执行</em></span><span class="sw" data-enh="sys"><i></i></span></div>' +
    "</div>" +
    '<div class="grp"><span class="grp-t">会话</span></div>' +
    '<div class="card">' +
    '<div class="row"><span class="lbl"><b>继续异常中断会话</b><em>检测到中断自动继续</em></span><span class="sw" data-enh="resume"><i></i></span></div>' +
    '<div class="row"><span class="lbl"><b>引用消息文本</b><em>选中文字一键插入输入框</em></span><span class="sw" data-enh="quote"><i></i></span></div>' +
    '<div class="row"><span class="lbl"><b>会话消息索引</b><em>悬浮定位条，点击跳到任意消息</em></span><span class="sw" data-enh="nav"><i></i></span></div>' +
    "</div>" +
    "</div>" +
    /* 设置 Tab（面板偏好 + 电脑 + 关于） */
    '<div class="pane" data-pane="setting">' +
    '<div class="grp"><span class="grp-t">面板</span></div>' +
    '<div class="card">' +
    '<div class="row"><span class="lbl"><b>机器人自动停靠</b><em>闲置 5 秒收进右缘</em></span><span class="sw" id="wbdesk-autodock"><i></i></span></div>' +
    "</div>" +
    '<div class="grp"><span class="grp-t">电脑</span></div>' +
    '<div class="card">' +
    '<div class="row"><span class="lbl"><b>防休眠</b><em>任务执行期间系统不休眠</em></span><span class="sw" id="wbdesk-awake"><i></i></span></div>' +
    "</div>" +
    '<div class="grp"><span class="grp-t">关于</span></div>' +
    '<div class="card">' +
    '<div class="row"><span class="lbl"><b>BuddyBot 悬浮机器人</b><em>由桌面端注入 · 主题/外观在「主题」Tab</em></span></div>' +
    '<div class="tip">完整设置（网关 / 定时任务 / 备份 / 更新）请打开桌面端 BuddyBot 控制台。</div>' +
    "</div>" +
    "</div>" +
    "</div>";
  document.body.appendChild(panel);
  state.cleanup.push(function () { panel.remove(); });

  function tabBtn(tab, label) {
    return '<button class="p-tab" data-tab="' + tab + '">' + ICON[tab] + "<span>" + label + "</span></button>";
  }

  panel.querySelector(".p-tabs").addEventListener("click", function (e) {
    var b = e.target.closest(".p-tab");
    if (!b) return;
    state.tab = b.getAttribute("data-tab");
    panel.querySelectorAll(".p-tab").forEach(function (t) {
      t.classList.toggle("on", t.getAttribute("data-tab") === state.tab);
    });
    panel.querySelectorAll(".pane").forEach(function (p) {
      p.classList.toggle("on", p.getAttribute("data-pane") === state.tab);
    });
  });
  setTab("account");
  function setTab(tab) {
    var b = panel.querySelector('[data-tab="' + tab + '"]');
    if (b) b.click();
  }

  // ---------- 面板开合 ----------
  function setOpen(open) {
    state.open = open;
    panel.style.display = open ? "flex" : "none";
    fab.classList.toggle("hidden", open);
    if (open) {
      undock();
      send("tasks", {}); // 打开面板即校准任务状态（含今日已完成徽标）
    }
  }

  fab.addEventListener("click", function () {
    if (state.suppressClick || state.moved) {
      state.suppressClick = false;
      state.moved = false;
      return;
    }
    setOpen(!state.open);
  });
  panel.querySelector("#wbdesk-close").addEventListener("click", function () { setOpen(false); });

  // ---------- 拖拽：可自由移动，释放后水平吸附回右缘，垂直位置持久化 ----------
  var FAB_POS_KEY = "wbdesk-fab-bottom";
  var drag = null;
  var snapTimer = null;

  function clamp(v, min, max) { return Math.max(min, Math.min(max, v)); }

  function applyFabPosition(right, bottom) {
    var maxRight = Math.max(EDGE_GAP, window.innerWidth - FAB_W - EDGE_GAP);
    var maxBottom = Math.max(EDGE_GAP, window.innerHeight - FAB_H - EDGE_GAP);
    fab.style.right = clamp(right, EDGE_GAP, maxRight) + "px";
    fab.style.bottom = clamp(bottom, EDGE_GAP, maxBottom) + "px";
  }

  function savedBottom() {
    try {
      var v = Number(localStorage.getItem(FAB_POS_KEY));
      return isFinite(v) && v > 0 ? v : EDGE_GAP;
    } catch (e) { return EDGE_GAP; }
  }

  function positionFab() {
    if (state.open || state.dragging) return;
    applyFabPosition(EDGE_GAP, savedBottom());
  }
  positionFab();

  fab.addEventListener("pointerdown", function (e) {
    if (e.button !== undefined && e.button !== 0) return;
    state.dragging = true;
    state.moved = false;
    state.suppressClick = false;
    fab.classList.add("dragging");
    var rect = fab.getBoundingClientRect();
    drag = {
      id: e.pointerId,
      x: e.clientX, y: e.clientY,
      startRight: window.innerWidth - rect.right,
      startBottom: window.innerHeight - rect.bottom,
    };
    try { fab.setPointerCapture(e.pointerId); } catch (err) {}
    if (e.preventDefault) e.preventDefault();
  });

  fab.addEventListener("pointermove", function (e) {
    if (!drag || e.pointerId !== drag.id) return;
    var dx = e.clientX - drag.x, dy = e.clientY - drag.y;
    if (Math.max(Math.abs(dx), Math.abs(dy)) >= DRAG_THRESHOLD) {
      state.moved = true;
      state.suppressClick = true;
      undock(); // 拖动即唤醒
    }
    applyFabPosition(drag.startRight - dx, drag.startBottom - dy);
    if (e.preventDefault) e.preventDefault();
  });

  function finishDrag() {
    if (!drag) return;
    drag = null;
    state.dragging = false;
    fab.classList.remove("dragging");
    // 水平吸附回右缘，垂直位置保留并持久化
    var rect = fab.getBoundingClientRect();
    var bottom = clamp(window.innerHeight - rect.bottom, EDGE_GAP,
      Math.max(EDGE_GAP, window.innerHeight - FAB_H - EDGE_GAP));
    fab.classList.add("snapping");
    applyFabPosition(EDGE_GAP, bottom);
    try { localStorage.setItem(FAB_POS_KEY, String(Math.round(bottom))); } catch (e) {}
    if (snapTimer) clearTimeout(snapTimer);
    snapTimer = setTimeout(function () { fab.classList.remove("snapping"); }, 500);
    wake();
  }
  fab.addEventListener("pointerup", finishDrag);
  fab.addEventListener("pointercancel", finishDrag);

  // ---------- 自动停靠：闲置 5 秒收进右缘，指针靠近边缘展开 ----------
  var AUTODOCK_KEY = "wbdesk-autodock";
  var autoDock = true;
  try { autoDock = localStorage.getItem(AUTODOCK_KEY) !== "0"; } catch (e) {}

  function centerY() {
    var bottom = parseFloat(fab.style.bottom) || EDGE_GAP;
    return window.innerHeight - bottom - FAB_H / 2;
  }

  function wake() {
    if (state.quietTimer) { clearTimeout(state.quietTimer); state.quietTimer = null; }
    if (!autoDock) return;
    if (!state.quiet && !state.near && !state.open && !state.dragging) {
      state.quietTimer = setTimeout(function () {
        state.quietTimer = null;
        if (!state.quiet && !state.near && !state.open && !state.dragging) {
          state.quiet = true;
          fab.classList.add("quiet");
        }
      }, QUIET_AFTER_MS);
    }
  }

  function undock() {
    if (state.quiet) { state.quiet = false; fab.classList.remove("quiet"); }
    wake();
  }

  state.cleanup.push(function () {
    if (state.quietTimer) clearTimeout(state.quietTimer);
    if (snapTimer) clearTimeout(snapTimer);
  });

  var onWinMove = function (e) {
    if (state.open || state.dragging || !autoDock) return;
    var near = e.clientX >= window.innerWidth - 24 &&
      Math.abs(e.clientY - centerY()) <= FAB_W / 2 + 24;
    if (near !== state.near) {
      state.near = near;
      if (near) undock();
      wake();
    }
  };
  window.addEventListener("pointermove", onWinMove, { capture: true, passive: true });
  state.cleanup.push(function () { window.removeEventListener("pointermove", onWinMove, true); });

  window.addEventListener("resize", function () { positionFab(); });

  // ---------- 面板交互：账号 ----------
  panel.querySelector("#wbdesk-backup").addEventListener("click", function () {
    var name = panel.querySelector("#wbdesk-name").value.trim();
    send("backup", { name: name });
    panel.querySelector("#wbdesk-name").value = "";
  });
  panel.querySelector("#wbdesk-refresh").addEventListener("click", function () { send("list", {}); });
  panel.querySelector("#wbdesk-pool-refresh").addEventListener("click", function () { send("pool", {}); });
  // 池账号「设为登录」：直接以该账号重启官方客户端（当前登录自动备份）
  panel.querySelector("#wbdesk-pool").addEventListener("click", function (e) {
    var b = e.target.closest("button[data-login-uid]");
    if (b && !b.disabled) {
      b.disabled = true;
      b.textContent = "切换中…";
      send("switch_uid", { uid: b.getAttribute("data-login-uid") });
    }
  });
  panel.querySelector("#wbdesk-list").addEventListener("click", function (e) {
    var del = e.target.closest("button[data-del]");
    if (del) { send("del_backup", { id: del.getAttribute("data-del") }); return; }
    var b = e.target.closest("button[data-acct]");
    if (b) send("switch", { id: b.getAttribute("data-acct") });
  });

  // ---------- 面板交互：设置（增强开关，参考 WorkDaddy「增强」页） ----------
  var ENH_KEYS = ["dnd", "file", "cmd", "del", "sys", "resume", "quote", "nav"];
  var DND_GROUP = ["dnd", "file", "cmd", "del", "sys"];

  function setEnh(key, on) {
    state.enh[key] = on;
    var sw = panel.querySelector('.sw[data-enh="' + key + '"]');
    if (sw) sw.classList.toggle("on", on);
    if (DND_GROUP.indexOf(key) >= 0) updateDndCount();
  }

  function updateDndCount() {
    var n = 0;
    DND_GROUP.forEach(function (k) { if (state.enh[k]) n++; });
    var el = panel.querySelector("#wbdesk-dnd-n");
    if (el) el.textContent = "已开启 " + n + " / " + DND_GROUP.length;
  }

  panel.addEventListener("click", function (e) {
    var sw = e.target.closest(".sw[data-enh]");
    if (!sw) return;
    var key = sw.getAttribute("data-enh");
    setEnh(key, !state.enh[key]);
    send("enh", { key: key, on: state.enh[key] });
  });

  panel.querySelector("#wbdesk-dnd-all").addEventListener("click", function () {
    DND_GROUP.forEach(function (k) {
      if (!state.enh[k]) { setEnh(k, true); send("enh", { key: k, on: true }); }
    });
  });

  // ---------- 面板交互：设置（自动停靠为面板本地偏好） ----------
  var autoDockSw = panel.querySelector("#wbdesk-autodock");
  autoDockSw.classList.toggle("on", autoDock);
  autoDockSw.addEventListener("click", function () {
    autoDock = !autoDock;
    autoDockSw.classList.toggle("on", autoDock);
    try { localStorage.setItem(AUTODOCK_KEY, autoDock ? "1" : "0"); } catch (e) {}
    if (autoDock) wake();
    else undock();
  });

  // ---------- 面板交互：系统（防休眠，状态由桌面端推送） ----------
  var awakeSw = panel.querySelector("#wbdesk-awake");
  awakeSw.addEventListener("click", function () {
    var on = !state.sys.awake;
    state.sys.awake = on;
    awakeSw.classList.toggle("on", on);
    send("awake", { on: on });
    if (on) toast("已开启防休眠，任务执行期间系统不会休眠");
  });
  window.__wbdeskSetSys = function (s) {
    s = s || {};
    state.sys.awake = !!s.awake;
    awakeSw.classList.toggle("on", state.sys.awake);
  };

  // ---------- 面板交互：主题（外观 / 机器人配色 / 面板壁纸，参考 WorkDaddy「主题」页） ----------
  var MODE_KEY = "wbdesk-panel-mode";
  var ROBOT_KEY = "wbdesk-robot-color";
  var WALL_KEY = "wbdesk-panel-wall";

  // 机器人配色预设（与桌面端 App 图标同族的几何色系）
  var ROBOT_PRESETS = [
    { id: "ink", name: "墨蓝", c1: "#2b3550", c2: "#1c2438" },
    { id: "coral", name: "珊瑚橙", c1: "#e8735a", c2: "#c9563f" },
    { id: "mint", name: "薄荷绿", c1: "#3aa88f", c2: "#2b8571" },
    { id: "violet", name: "堇紫", c1: "#6d5bd0", c2: "#5343ab" },
    { id: "graphite", name: "石墨", c1: "#3c4250", c2: "#262b36" },
  ];
  // 面板壁纸预设（渐变）；自定义图片经 canvas 压缩后存本机
  var WALL_PRESETS = [
    { id: "none", name: "无", css: "" },
    { id: "aurora", name: "极光", css: "linear-gradient(160deg,#1b2a4a 0%,#274b6d 45%,#3aa88f 100%)" },
    { id: "dusk", name: "暮色", css: "linear-gradient(150deg,#3c2438 0%,#6d3b5b 50%,#e8735a 100%)" },
    { id: "ocean", name: "深海", css: "linear-gradient(170deg,#0f2438 0%,#1b3a5c 55%,#478cbf 100%)" },
    { id: "slate", name: "石墨", css: "linear-gradient(180deg,#262b36 0%,#3c4250 100%)" },
  ];

  function applyPanelMode(mode) {
    var light = mode === "light" ||
      (mode === "auto" && window.matchMedia && window.matchMedia("(prefers-color-scheme: light)").matches);
    panel.classList.toggle("light", !!light);
    panel.querySelectorAll("#wbdesk-mode button").forEach(function (b) {
      b.classList.toggle("on", b.getAttribute("data-mode") === mode);
    });
    try { localStorage.setItem(MODE_KEY, mode); } catch (e) {}
  }
  panel.querySelector("#wbdesk-mode").addEventListener("click", function (e) {
    var b = e.target.closest("button[data-mode]");
    if (b) applyPanelMode(b.getAttribute("data-mode"));
  });
  if (window.matchMedia) {
    var mq = window.matchMedia("(prefers-color-scheme: light)");
    var onScheme = function () { applyPanelMode(getMode()); };
    if (mq.addEventListener) mq.addEventListener("change", onScheme);
    state.cleanup.push(function () { if (mq.removeEventListener) mq.removeEventListener("change", onScheme); });
  }
  function getMode() {
    try { return localStorage.getItem(MODE_KEY) || "dark"; } catch (e) { return "dark"; }
  }

  function applyRobotColor(id) {
    var p = ROBOT_PRESETS.filter(function (x) { return x.id === id; })[0] || ROBOT_PRESETS[0];
    var fabEl = document.getElementById("wbdesk-fab");
    if (fabEl) {
      var shell = fabEl.querySelector(".wb-robot");
      if (shell) shell.style.background = "linear-gradient(150deg," + p.c1 + "," + p.c2 + ")";
      var ant = fabEl.querySelector(".wb-antenna");
      if (ant) ant.style.background = p.c1;
    }
    panel.querySelectorAll("#wbdesk-avatars span").forEach(function (s) {
      s.classList.toggle("on", s.getAttribute("data-robot") === p.id);
    });
    try { localStorage.setItem(ROBOT_KEY, p.id); } catch (e) {}
  }
  (function () {
    var box = panel.querySelector("#wbdesk-avatars");
    box.innerHTML = ROBOT_PRESETS.map(function (p) {
      return '<span data-robot="' + p.id + '" title="' + p.name + '" style="background:linear-gradient(150deg,' + p.c1 + "," + p.c2 + ')"></span>';
    }).join("");
    box.addEventListener("click", function (e) {
      var s = e.target.closest("span[data-robot]");
      if (s) applyRobotColor(s.getAttribute("data-robot"));
    });
  })();

  function applyWall(wall) {
    wall = wall || { preset: "none" };
    var css = "";
    if (wall.custom) {
      css = "linear-gradient(rgba(19,20,23,.72),rgba(19,20,23,.72)),url('" + wall.custom + "') center/cover no-repeat";
    } else {
      var p = WALL_PRESETS.filter(function (x) { return x.id === wall.preset; })[0] || WALL_PRESETS[0];
      css = p.css;
    }
    panel.style.background = css; // 空 = 还原 CSS 默认半透明毛玻璃
    panel.querySelectorAll("#wbdesk-walls span").forEach(function (s) {
      s.classList.toggle("on", s.getAttribute("data-wall") === (wall.custom ? "custom" : (wall.preset || "none")));
    });
    try { localStorage.setItem(WALL_KEY, JSON.stringify(wall)); } catch (e) {
      if (wall.custom) toast("壁纸图片过大，本机保存失败（本次预览仍生效）");
    }
  }
  (function () {
    var box = panel.querySelector("#wbdesk-walls");
    box.innerHTML = WALL_PRESETS.map(function (p) {
      return '<span data-wall="' + p.id + '" title="' + p.name + '" style="background:' + (p.css || "rgba(255,255,255,.06)") + '"></span>';
    }).join("");
    box.addEventListener("click", function (e) {
      var s = e.target.closest("span[data-wall]");
      if (s) applyWall({ preset: s.getAttribute("data-wall") });
    });
    panel.querySelector("#wbdesk-wall-file").addEventListener("click", function () {
      var inp = document.createElement("input");
      inp.type = "file";
      inp.accept = "image/png,image/jpeg,image/webp";
      inp.onchange = function () {
        var f = inp.files && inp.files[0];
        if (!f) return;
        var rd = new FileReader();
        rd.onload = function () {
          var img = new Image();
          img.onload = function () {
            // 压缩到最长边 1280，webp 0.8，控制 localStorage 占用
            var scale = Math.min(1, 1280 / Math.max(img.width, img.height));
            var cv = document.createElement("canvas");
            cv.width = Math.round(img.width * scale);
            cv.height = Math.round(img.height * scale);
            cv.getContext("2d").drawImage(img, 0, 0, cv.width, cv.height);
            applyWall({ preset: "custom", custom: cv.toDataURL("image/webp", 0.8) });
          };
          img.src = rd.result;
        };
        rd.readAsDataURL(f);
      };
      inp.click();
    });
    panel.querySelector("#wbdesk-wall-clear").addEventListener("click", function () { applyWall({ preset: "none" }); });
  })();
  (function restoreTheme() {
    applyPanelMode(getMode());
    var robot = "ink";
    try { robot = localStorage.getItem(ROBOT_KEY) || "ink"; } catch (e) {}
    applyRobotColor(robot);
    var wall = null;
    try { wall = JSON.parse(localStorage.getItem(WALL_KEY) || "null"); } catch (e) {}
    applyWall(wall && (wall.custom || wall.preset !== "none") ? wall : { preset: "none" });
  })();

  // ---------- 面板交互：任务 ----------
  panel.querySelector("#wbdesk-tasks-refresh").addEventListener("click", function () { send("tasks", {}); });
  panel.querySelector("#wbdesk-task-all").addEventListener("click", function () { send("task_all", {}); });
  panel.querySelector("#wbdesk-tasks").addEventListener("click", function (e) {
    var b = e.target.closest("button[data-task]");
    if (b && !b.disabled) send("task", { task: b.getAttribute("data-task") });
  });

  // ---------- 面板交互：模型 ----------
  panel.querySelector("#wbdesk-models-refresh").addEventListener("click", function () { send("models", {}); });
  panel.querySelector("#wbdesk-gw").addEventListener("click", function () {
    var url = state.models && state.models.url;
    if (!url) return;
    copyText(url);
    var gw = panel.querySelector("#wbdesk-gw");
    gw.title = "已复制 ✓";
    setTimeout(function () { gw.title = "点击复制"; }, 1200);
  });

  function copyText(text) {
    try {
      if (navigator.clipboard && navigator.clipboard.writeText) {
        navigator.clipboard.writeText(text).catch(function () { fallbackCopy(text); });
        return;
      }
    } catch (e) {}
    fallbackCopy(text);
  }
  function fallbackCopy(text) {
    var ta = document.createElement("textarea");
    ta.value = text;
    ta.style.cssText = "position:fixed;opacity:0;";
    document.body.appendChild(ta);
    ta.select();
    try { document.execCommand("copy"); } catch (e) {}
    ta.remove();
  }

  // ---------- 桌面端 → 面板：账号 ----------
  window.__wbdeskSetAccounts = function (list) {
    state.accounts = list || [];
    var box = panel.querySelector("#wbdesk-list");
    box.innerHTML = state.accounts.length
      ? state.accounts.map(function (a) {
          return '<div class="acct"><span title="' + a.uid + '" style="overflow:hidden;text-overflow:ellipsis;white-space:nowrap">' + escapeHtml(a.name) +
            ' <span class="tip">' + escapeHtml(a.time || "") + "</span></span>" +
            '<span style="display:flex;gap:5px;flex:none">' +
            '<button class="ghost" data-del="' + escapeHtml(a.id) + '" title="删除该备份">删</button>' +
            '<button data-acct="' + escapeHtml(a.id) + '">切换</button></span></div>';
        }).join("")
      : '<div class="tip">暂无备份。提示：要切号可直接用上方池账号的「设为登录」，无需先备份。</div>';
  };
  window.__wbdeskSetEnh = function (e) {
    e = e || {};
    ENH_KEYS.forEach(function (k) { setEnh(k, !!e[k]); });
  };

  window.__wbdeskSetPool = function (list) {
    state.pool = list || [];
    var expiring = false;
    var box = panel.querySelector("#wbdesk-pool");
    box.innerHTML = state.pool.length
      ? state.pool.map(function (a) {
          var exp = (a.creditsExpiring || 0) > 0;
          if (exp) expiring = true;
          var cur = !!a.current;
          return '<div class="acct" style="' + (exp ? "border-color:#f59e0b;background:rgba(245,158,11,.08);" : "") + '">' +
            '<span title="' + escapeHtml(a.uid || "") + '" style="overflow:hidden;text-overflow:ellipsis;white-space:nowrap">' +
            (cur ? '<span class="badge ok" style="margin-right:4px">当前</span>' : "") +
            escapeHtml(a.name || "账号") +
            (exp ? ' <span style="color:#fbbf24;font-weight:600">⚠ 72h 内到期 ' + a.creditsExpiring + "</span>" : "") +
            "</span>" +
            '<span style="display:flex;align-items:center;gap:6px;flex:none">' +
            '<b>' + (a.creditsKnown ? a.credits.toLocaleString() + " 分" : "—") + "</b>" +
            (cur ? "" : '<button class="ghost" data-login-uid="' + escapeHtml(a.uid || "") + '">设为登录</button>') +
            "</span></div>";
        }).join("")
      : '<div class="tip">账号池暂无账号（桌面端「深度刷新」后显示）</div>';
    // 有临期积分时机器人头顶亮徽标
    var badge = document.getElementById("wbdesk-fab-badge");
    if (badge) badge.classList.toggle("show", expiring);
  };

  // ---------- 桌面端 → 面板：任务 ----------
  window.__wbdeskSetTasks = function (view) {
    state.tasks = view || { busy: [], history: [] };
    var busy = state.tasks.busy || [];
    var history = state.tasks.history || [];

    // 快捷任务行：名称 + 今日已完成徽标 + 执行按钮
    var box = panel.querySelector("#wbdesk-tasks");
    box.innerHTML = TASKS.map(function (t) {
      var isBusy = busy.indexOf(t) >= 0;
      var done = todayRun(history, t);
      var badge = isBusy
        ? '<span class="badge busy">执行中…</span>'
        : (done ? '<span class="badge ok" title="' + escapeHtml(done.startedAt) + ' 已成功执行">✓ ' + escapeHtml(done.time) + "</span>" : "");
      return '<div class="row" style="padding:5px 0"><span>' + TASK_LABEL[t] + " " + badge + "</span>" +
        '<button data-task="' + t + '"' + (isBusy ? " disabled" : "") + ">" + (isBusy ? "执行中" : "执行") + "</button></div>";
    }).join("");

    // 最近运行列表
    var hbox = panel.querySelector("#wbdesk-history");
    hbox.innerHTML = history.length
      ? history.map(function (r) {
          var cls = r.failed > 0 ? "fail" : (r.success > 0 ? "ok" : "dim");
          var txt = r.failed > 0 ? "失败 " + r.failed : (r.success > 0 ? "成功 " + r.success : "跳过 " + r.skipped);
          return '<div class="row" style="padding:3px 0"><span>' +
            (TASK_LABEL[r.type] || r.type) + ' <span class="tip">' + escapeHtml(shortTime(r.startedAt)) +
            "</span></span><span class=\"badge " + cls + "\">" + txt + "</span></div>";
        }).join("")
      : '<div class="tip">暂无运行记录</div>';
  };

  // 今天内 success>0 的最近一次运行（口径与桌面端任务中心一致）
  function todayRun(history, type) {
    var today = new Date();
    var pad = function (n) { return (n < 10 ? "0" : "") + n; };
    var day = today.getFullYear() + "-" + pad(today.getMonth() + 1) + "-" + pad(today.getDate());
    for (var i = 0; i < history.length; i++) {
      var r = history[i];
      if (r.type !== type || !(r.success > 0)) continue;
      if (String(r.startedAt || "").indexOf(day) !== 0) continue;
      return { startedAt: r.startedAt, time: shortTime(r.startedAt) };
    }
    return null;
  }

  function shortTime(s) {
    // "2026-09-19 09:00:05" → "09:00"
    var m = String(s || "").match(/(\d{2}:\d{2})/);
    return m ? m[1] : String(s || "");
  }

  // ---------- 桌面端 → 面板：模型 ----------
  window.__wbdeskSetModels = function (view) {
    state.models = view || { url: "", models: [] };
    panel.querySelector("#wbdesk-gw").textContent = state.models.url || "—";
    renderModels();
  };

  function renderModels() {
    var all = (state.models && state.models.models) || [];
    var kw = "";
    try { kw = (panel.querySelector("#wbdesk-model-search").value || "").trim().toLowerCase(); } catch (e) {}
    var models = kw ? all.filter(function (m) { return m.id.toLowerCase().indexOf(kw) >= 0; }) : all;
    panel.querySelector("#wbdesk-model-count").textContent = all.length;
    var box = panel.querySelector("#wbdesk-models");
    box.innerHTML = models.length
      ? models.slice(0, 80).map(function (m) {
          var stat = m.requests > 0
            ? '<span class="tip">' + m.requests + " 次 · " + fmtTokens(m.tokens) + "</span>"
            : '<span class="tip">' + (m.observed ? "仅观测" : "未使用") + "</span>";
          return '<div class="row acct" data-mid="' + escapeHtml(m.id) + '" title="点击复制模型 ID" style="cursor:pointer;padding:5px 8px">' +
            '<span style="overflow:hidden;text-overflow:ellipsis;white-space:nowrap">' + escapeHtml(m.id) + "</span>" + stat + "</div>";
        }).join("") +
        (models.length > 80 ? '<div class="tip" style="text-align:center">仅显示前 80 个，共 ' + models.length + " 个</div>" : "")
      : '<div class="tip">' + (kw ? "无匹配模型" : "暂无模型（桌面端定时刷新或保活后同步）") + "</div>";
  }
  panel.querySelector("#wbdesk-model-search").addEventListener("input", renderModels);
  panel.querySelector("#wbdesk-models").addEventListener("click", function (e) {
    var row = e.target.closest("[data-mid]");
    if (!row) return;
    copyText(row.getAttribute("data-mid"));
    toast("已复制模型 ID：" + row.getAttribute("data-mid"));
  });

  // ---------- 面板内 toast ----------
  var toastEl = null, toastTimer = null;
  function toast(msg) {
    if (!toastEl) {
      toastEl = document.createElement("div");
      toastEl.id = "wbdesk-toast";
      document.body.appendChild(toastEl);
      state.cleanup.push(function () { if (toastEl) { toastEl.remove(); toastEl = null; } });
    }
    toastEl.textContent = msg;
    toastEl.style.display = "block";
    if (toastTimer) clearTimeout(toastTimer);
    toastTimer = setTimeout(function () { if (toastEl) toastEl.style.display = "none"; }, 3200);
  }
  // 桌面端错误透传（备份失败 / 切换失败等，页面里直接可见）
  window.__wbdeskToast = function (msg) { toast(String(msg || "")); };

  function fmtTokens(n) {
    if (!n) return "0";
    if (n >= 1e6) return (n / 1e6).toFixed(1) + "M";
    if (n >= 1e3) return (n / 1e3).toFixed(1) + "K";
    return String(n);
  }

  function escapeHtml(s) {
    return String(s == null ? "" : s).replace(/[&<>"]/g, function (c) {
      return { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[c];
    });
  }

  // ---------- 免打扰：权限弹窗自动确认（分类开关参考 WorkDaddy「增强」页） ----------
  var CONFIRM_TEXTS = ["确定", "确认", "允许", "同意", "继续", "好的", "知道了", "始终允许",
    "Allow", "OK", "Confirm", "Accept", "Continue"];
  // 分类开关 → 弹窗文本启发式匹配（弹窗内任意文案命中即放行该弹窗）
  var ENH_PATTERNS = {
    file: /沙箱|工作区外|写入文件|写文件|文件写入|修改文件|outside\s+(of\s+)?(the\s+)?(workspace|sandbox)|write\s+to\s+file/i,
    cmd: /命令|终端|脚本|执行.{0,6}(命令|指令)|command|terminal|shell\s+command/i,
    del: /删除|回收站|清空|移除|批量删|delete|trash|remove\s+\d+/i,
    sys: /系统管理|系统工具|系统设置|管理员|sudo|system\s+(admin|tool)|elevated/i,
  };
  var seenDialogs = new WeakSet();
  var lastScan = 0;

  function visible(el) {
    var r = el.getBoundingClientRect();
    return r.width > 0 && r.height > 0;
  }

  function enhAllows(rootText) {
    if (state.enh.dnd) return true; // 总开关：全部弹窗自动允许
    for (var k in ENH_PATTERNS) {
      if (state.enh[k] && ENH_PATTERNS[k].test(rootText)) return true;
    }
    return false;
  }

  function isConfirmButton(el) {
    if (!el || el.tagName !== "BUTTON") return false;
    var t = (el.textContent || "").trim();
    if (CONFIRM_TEXTS.indexOf(t) < 0) return false;
    if (!visible(el) || el.disabled) return false;
    var cls = (el.className || "").toLowerCase();
    // 纯文本的「确定」可能是正文，要求它看起来像个按钮
    return cls.indexOf("btn") >= 0 || cls.indexOf("button") >= 0 || cls.indexOf("primary") >= 0 ||
      el.closest('[role="dialog"],[class*="modal"],[class*="dialog"],[class*="popup"],[class*="drawer"]');
  }

  function dialogRoot(el) {
    return el.closest('[role="dialog"],[class*="modal"],[class*="dialog"],[class*="popup"],[class*="drawer"]') || el;
  }

  function autoConfirm() {
    if (!state.enh.dnd && !state.enh.file && !state.enh.cmd && !state.enh.del && !state.enh.sys) return;
    var now = Date.now();
    if (now - lastScan < 600) return;
    lastScan = now;
    var buttons = document.querySelectorAll("button");
    for (var i = 0; i < buttons.length; i++) {
      var b = buttons[i];
      if (!isConfirmButton(b)) continue;
      var root = dialogRoot(b);
      if (seenDialogs.has(root)) continue;
      if (!enhAllows(root.textContent || "")) continue;
      seenDialogs.add(root);
      b.click();
      send("dnd_clicked", { text: (b.textContent || "").trim() });
      break; // 每轮只点一个，下一轮扫描继续
    }
  }

  // ---------- 会话：继续异常中断（检测中断提示，自动点「继续 / 重试」） ----------
  var RESUME_BTN = /^(继续|重试|恢复|继续生成|重新连接|重连|Resume|Retry|Continue)$/;
  var RESUME_CTX = /中断|断开|异常|连接已?断|会话已?停止/;
  var resumeSeen = new WeakSet();
  var lastResumeScan = 0;

  function autoResume() {
    if (!state.enh.resume) return;
    var now = Date.now();
    if (now - lastResumeScan < 1500) return;
    lastResumeScan = now;
    var cands = document.querySelectorAll('button,[role="button"],a');
    for (var i = 0; i < cands.length; i++) {
      var el = cands[i];
      if (panel.contains(el) || fab.contains(el)) continue;
      var t = (el.textContent || "").trim();
      if (!RESUME_BTN.test(t) || !visible(el)) continue;
      var box = el.closest('[role="alert"],[class*="banner"],[class*="notice"],[class*="toast"],[class*="error"],[class*="interrupt"]') ||
        el.parentElement;
      if (!box || !RESUME_CTX.test(box.textContent || "")) continue;
      if (resumeSeen.has(el)) continue;
      resumeSeen.add(el);
      el.click();
      send("enh_clicked", { key: "resume", text: t });
      break;
    }
  }

  function enhTick() { autoConfirm(); autoResume(); navTick(); }

  // ---------- 会话：消息索引（悬浮定位条，机制参考 WorkDaddy 会话定位） ----------
  // 完整索引来自 React fiber 挖出的 controller.messageStore；DOM 仅用于定位 rail 与滚动高亮。
  var mnav = null, mnavTip = null;
  var navState = { turns: [], handle: null, surface: null };

  function findReactProp(rootEl, test) {
    if (!rootEl) return null;
    var fk = Object.keys(rootEl).filter(function (k) {
      return k.indexOf("__reactFiber$") === 0 || k.indexOf("__reactInternalInstance$") === 0;
    })[0];
    var fiber = fk ? rootEl[fk] : null;
    var hops = 0;
    while (fiber && hops++ < 400) {
      var props = fiber.memoizedProps;
      if (props) {
        for (var k in props) {
          var v = props[k];
          if (v && typeof v === "object" && test(v)) return v;
        }
      }
      fiber = fiber.return;
    }
    return null;
  }

  function navCollect() {
    var surface = document.querySelector("div.cr-message-list");
    if (!surface) return null;
    var host = surface.parentElement || surface;
    var ctrl = findReactProp(host, function (v) {
      return v.messageStore && typeof v.messageStore.getState === "function" && v.conversationId;
    });
    if (!ctrl) return null;
    var handle = findReactProp(host, function (v) {
      return typeof v.scrollToMessage === "function" && typeof v.getScrollMetrics === "function";
    });
    if (!handle) return null;
    var msgs = [];
    try { msgs = ctrl.messageStore.getState().messages || []; } catch (e) {}
    var turns = [];
    for (var i = 0; i < msgs.length; i++) {
      var m = msgs[i];
      if (m.messageType !== "user") continue;
      var text = String(m.text || (typeof m.content === "string" ? m.content : "") || "").trim();
      if (!text || text.indexOf("req-") === 0) continue; // 发送中的临时消息
      turns.push({ id: m.id || m.messageId, text: text });
    }
    return { surface: surface, handle: handle, turns: turns };
  }

  function navTick() {
    if (!state.enh.nav || state.open) { navHide(); return; }
    var found = navCollect();
    if (!found || !found.turns.length) { navHide(); return; }
    var rail = ensureMnav();
    var changed = navState.turns.length !== found.turns.length ||
      (found.turns[0] && navState.turns[0] && found.turns[0].id !== navState.turns[0].id);
    navState.turns = found.turns;
    navState.handle = found.handle;
    if (navState.surface !== found.surface) {
      if (navState.surface) navState.surface.removeEventListener("scroll", navScrollTick);
      found.surface.addEventListener("scroll", navScrollTick, { passive: true });
      navState.surface = found.surface;
    }
    rail.style.display = "flex";
    var r = found.surface.getBoundingClientRect();
    rail.style.left = Math.max(4, r.left + 10) + "px";
    rail.style.top = (r.top + r.height / 2) + "px";
    rail.style.transform = "translateY(-50%)";
    if (changed || rail.childElementCount !== found.turns.length) {
      rail.innerHTML = found.turns.map(function (t, i) {
        return '<button class="mk" data-i="' + i + '"></button>';
      }).join("");
      navScrollTick();
    }
  }

  function navHide() {
    if (mnav) mnav.style.display = "none";
    if (mnavTip) mnavTip.style.display = "none";
  }

  function navScrollTick() {
    if (!mnav || mnav.style.display === "none" || !navState.surface || !navState.turns.length) return;
    var el = navState.surface;
    var max = el.scrollHeight - el.clientHeight;
    var ratio = max > 0 ? el.scrollTop / max : 0;
    var idx = Math.max(0, Math.min(navState.turns.length - 1, Math.floor(ratio * navState.turns.length)));
    var marks = mnav.querySelectorAll(".mk");
    for (var i = 0; i < marks.length; i++) marks[i].classList.toggle("on", i === idx);
  }

  function ensureMnav() {
    if (mnav) return mnav;
    mnav = document.createElement("div");
    mnav.id = "wbdesk-mnav";
    mnavTip = document.createElement("div");
    mnavTip.id = "wbdesk-mnav-tip";
    document.body.appendChild(mnav);
    document.body.appendChild(mnavTip);
    state.cleanup.push(function () {
      if (navState.surface) { try { navState.surface.removeEventListener("scroll", navScrollTick); } catch (e) {} }
      if (mnav) { mnav.remove(); mnav = null; }
      if (mnavTip) { mnavTip.remove(); mnavTip = null; }
    });
    mnav.addEventListener("click", function (e) {
      var b = e.target.closest(".mk");
      if (!b) return;
      var t = navState.turns[parseInt(b.getAttribute("data-i"), 10) || 0];
      if (t && navState.handle) {
        try { navState.handle.scrollToMessage(t.id, { behavior: "auto", block: "start" }); } catch (err) {}
      }
    });
    mnav.addEventListener("mouseover", function (e) {
      var b = e.target.closest(".mk");
      if (!b || !mnavTip) return;
      var t = navState.turns[parseInt(b.getAttribute("data-i"), 10) || 0];
      if (!t || !t.text) return;
      var r = b.getBoundingClientRect();
      mnavTip.textContent = t.text.slice(0, 240);
      mnavTip.style.display = "block";
      mnavTip.style.left = Math.min(window.innerWidth - 280, r.right + 8) + "px";
      mnavTip.style.top = Math.max(8, r.top - 10) + "px";
    });
    mnav.addEventListener("mouseleave", function () { if (mnavTip) mnavTip.style.display = "none"; });
    return mnav;
  }

  var mo = new MutationObserver(enhTick);
  mo.observe(document.documentElement, { childList: true, subtree: true });
  var timer = setInterval(enhTick, 1500); // 兜底轮询（动画结束后才出现的弹窗）
  state.cleanup.push(function () { mo.disconnect(); clearInterval(timer); });

  // ---------- 会话：引用消息文本（选中文字 → 悬浮「引用」→ 插入输入框） ----------
  var quotePill = null;
  var lastQuoteText = "";
  var selTimer = null;

  function ensureQuotePill() {
    if (quotePill) return quotePill;
    quotePill = document.createElement("button");
    quotePill.id = "wbdesk-quote";
    quotePill.textContent = "引用";
    quotePill.addEventListener("mousedown", function (e) { e.preventDefault(); }); // 保住选区
    quotePill.addEventListener("click", function () {
      insertQuote();
      hideQuotePill();
    });
    document.body.appendChild(quotePill);
    state.cleanup.push(function () { if (quotePill) { quotePill.remove(); quotePill = null; } });
    return quotePill;
  }

  function hideQuotePill() { if (quotePill) quotePill.style.display = "none"; }

  function onSelectionChange() {
    if (!state.enh.quote || state.open) { hideQuotePill(); return; }
    var sel = window.getSelection();
    var txt = sel && sel.rangeCount && !sel.isCollapsed ? String(sel).trim() : "";
    if (!txt || txt.length > 4000) { hideQuotePill(); return; }
    var range = sel.getRangeAt(0);
    if (panel.contains(range.commonAncestorContainer) || fab.contains(range.commonAncestorContainer)) {
      hideQuotePill();
      return;
    }
    var rect = range.getBoundingClientRect();
    if (!rect || (!rect.width && !rect.height)) { hideQuotePill(); return; }
    lastQuoteText = txt;
    var pill = ensureQuotePill();
    pill.style.display = "block";
    pill.style.left = clamp(rect.left + rect.width / 2 - 24, 8, window.innerWidth - 56) + "px";
    pill.style.top = (rect.top < 42 ? rect.bottom + 8 : rect.top - 34) + "px";
  }

  function findChatInput() {
    var list = document.querySelectorAll('textarea,[contenteditable="true"]');
    for (var i = 0; i < list.length; i++) {
      var el = list[i];
      if (!visible(el) || panel.contains(el)) continue;
      if (el.getBoundingClientRect().top > window.innerHeight * 0.3) return el; // 输入框一般在下半屏
    }
    return list.length ? list[0] : null;
  }

  function insertQuote() {
    var el = findChatInput();
    if (!el || !lastQuoteText) return;
    var quoted = "> " + lastQuoteText.split("\n").join("\n> ").slice(0, 2000) + "\n";
    if (el.tagName === "TEXTAREA" || el.tagName === "INPUT") {
      var proto = el.tagName === "TEXTAREA" ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
      var setter = Object.getOwnPropertyDescriptor(proto, "value").set; // 绕过 React 覆写的 value
      setter.call(el, (el.value ? el.value.replace(/\n$/, "") + "\n" : "") + quoted);
      el.dispatchEvent(new Event("input", { bubbles: true }));
      el.focus();
      try { el.selectionStart = el.selectionEnd = el.value.length; } catch (e) {}
    } else {
      el.focus();
      try { document.execCommand("insertText", false, quoted); } catch (e) {}
    }
  }

  document.addEventListener("selectionchange", function () {
    if (selTimer) clearTimeout(selTimer);
    selTimer = setTimeout(onSelectionChange, 120);
  });
  window.addEventListener("scroll", hideQuotePill, true);
  state.cleanup.push(function () {
    if (selTimer) clearTimeout(selTimer);
    window.removeEventListener("scroll", hideQuotePill, true);
  });

  // ---------- 清理 ----------
  window.__wbdeskCleanup = function () {
    state.cleanup.forEach(function (fn) { try { fn(); } catch (e) {} });
    delete window.__wbdeskCleanup;
    delete window.__wbdeskSetAccounts;
    delete window.__wbdeskSetEnh;
    delete window.__wbdeskSetPool;
    delete window.__wbdeskSetTasks;
    delete window.__wbdeskSetModels;
    delete window.__wbdeskSetSys;
    delete window.__wbdeskToast;
  };

  wake(); // 首次注入启动闲置计时
})();

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

  // 代际令牌：每次注入递增。周期任务（头像保活/增强轮询/宠物动画）回调里
  // 校验代数，不匹配即自动停摆——否则重复注入会累积多份旧定时器
  // （cleanup 只能清 DOM 与已注册回调，清不掉旧闭包里的 interval），
  // 新旧实例会互相打架（典型症状：头像被旧实例反复改 src 导致不停跳动）。
  var GEN = (window.__wbdeskGen = (window.__wbdeskGen || 0) + 1);

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
    "#wbdesk-panel .seg2{display:flex;gap:4px;background:var(--p-input);border-radius:8px;padding:2px;flex:none;}",
    "#wbdesk-panel .seg2 button{flex:1;background:transparent;color:var(--p-sub);border-radius:6px;padding:4px 0;}",
    "#wbdesk-panel .seg2 button.on{background:var(--p-tab-on-bg);color:var(--p-tab-on-fg);font-weight:600;}",
    /* 主题外观色卡（作用于 WorkBuddy 本体） */
    "#wbdesk-panel .themes{display:grid;grid-template-columns:repeat(5,1fr);gap:8px;padding:9px 0;}",
    "#wbdesk-panel .theme-opt{background:transparent;border:2px solid transparent;border-radius:10px;padding:4px 2px;cursor:pointer;text-align:center;}",
    "#wbdesk-panel .theme-opt.on{border-color:var(--p-btn);}",
    "#wbdesk-panel .theme-opt .chip{height:26px;border-radius:6px;display:block;border:1px solid rgba(128,128,128,.25);}",
    "#wbdesk-panel .theme-opt .tname{font-size:10.5px;color:var(--p-sub);display:block;margin-top:3px;white-space:nowrap;overflow:hidden;text-overflow:ellipsis;}",
    "#wbdesk-panel .theme-opt.on .tname{color:var(--p-fg);font-weight:600;}",
    /* 宠物皮肤选择（Codex 宠物精灵格式） */
    "#wbdesk-panel .petgrid{display:grid;grid-template-columns:repeat(4,1fr);gap:8px;padding:9px 0;}",
    "#wbdesk-panel .petopt{background:transparent;border:2px solid transparent;border-radius:10px;padding:4px 2px;cursor:pointer;text-align:center;}",
    "#wbdesk-panel .petopt.on{border-color:var(--p-btn);}",
    "#wbdesk-panel .petopt img{width:100%;height:44px;object-fit:contain;display:block;}",
    "#wbdesk-panel .petopt .pname{font-size:10.5px;color:var(--p-sub);display:block;margin-top:2px;white-space:nowrap;overflow:hidden;text-overflow:ellipsis;}",
    "#wbdesk-panel .petopt.on .pname{color:var(--p-fg);font-weight:600;}",
    "#wbdesk-panel .petopt .petoff{width:100%;height:44px;display:flex;align-items:center;justify-content:center;background:var(--p-input);border-radius:8px;}",
    "#wbdesk-panel .petopt{position:relative;}",
    "#wbdesk-panel .petopt .pet-del{position:absolute;top:2px;right:2px;width:16px;height:16px;border-radius:50%;background:rgba(0,0,0,.55);color:#fff;font-size:10px;line-height:16px;text-align:center;display:none;}",
    "#wbdesk-panel .petopt:hover .pet-del{display:block;}",
    "#wbdesk-panel .petopt.pet-upload{border-style:dashed;border-color:rgba(128,128,128,.4);cursor:pointer;}",
    "#wbdesk-panel .petopt.pet-upload .petoff{background:transparent;color:var(--p-sub);font-size:18px;}",
    "#wbdesk-panel .petopt.pet-upload.drag{border-color:var(--p-btn);}",
    /* 宠物精灵生效：隐藏 CSS 五官与天线，外壳让位给精灵图逐帧动画 */
    "#wbdesk-fab.petsprite .wb-antenna,#wbdesk-fab.petsprite .wb-antenna-dot,#wbdesk-fab.petsprite .wb-face{display:none;}",
    "#wbdesk-fab.petsprite .wb-robot{background:transparent!important;box-shadow:none;border-radius:0;}",
    "#wbdesk-fab.petsprite .wb-sprite{position:absolute;left:50%;bottom:0;transform:translateX(-50%);background-repeat:no-repeat;}",
    "#wbdesk-fab.quiet.petsprite{transform:translateX(" + (FAB_W - 6 + EDGE_GAP + 10) + "px);}",
    /* 头像：预设图片 + 自定义上传（参考 WorkDaddy 头像选择） */
    "#wbdesk-panel .avats{display:flex;gap:8px;flex-wrap:wrap;padding:4px 0 2px;align-items:center;}",
    "#wbdesk-panel .avat{width:44px;height:44px;border-radius:12px;cursor:pointer;border:2px solid transparent;padding:0;background:transparent;position:relative;flex:none;}",
    "#wbdesk-panel .avat img{width:100%;height:100%;border-radius:10px;display:block;object-fit:cover;}",
    "#wbdesk-panel .avat.on{border-color:var(--p-btn);}",
    "#wbdesk-panel .avat .avat-del{position:absolute;right:-6px;top:-6px;width:16px;height:16px;border-radius:50%;",
    "background:rgba(239,68,68,.92);color:#fff;font-size:10px;line-height:15px;text-align:center;display:none;padding:0;}",
    "#wbdesk-panel .avat:hover .avat-del{display:block;}",
    "#wbdesk-panel .avat.add{display:flex;align-items:center;justify-content:center;background:var(--p-input);",
    "color:var(--p-sub);font-size:20px;line-height:1;}",
    "#wbdesk-panel .avat.add:hover{color:var(--p-fg);}",
    /* 壁纸：预设图片宫格 / 自定义上传（参考 WorkDaddy 壁纸选择） */
    "#wbdesk-panel .bg-src{margin:2px 0 8px;}",
    "#wbdesk-panel .walls{display:grid;grid-template-columns:repeat(4,1fr);gap:8px;padding:2px 0;}",
    "#wbdesk-panel .wall{height:52px;border-radius:9px;overflow:hidden;cursor:pointer;border:none;padding:0;position:relative;",
    "outline:2px solid transparent;outline-offset:1px;background:var(--p-input);}",
    "#wbdesk-panel .wall img{width:100%;height:100%;object-fit:cover;display:block;}",
    "#wbdesk-panel .wall.on{outline-color:var(--p-btn);}",
    "#wbdesk-panel .wall .wall-del{position:absolute;right:3px;top:3px;width:16px;height:16px;border-radius:50%;",
    "background:rgba(0,0,0,.55);color:#fff;border:none;font-size:10px;line-height:15px;padding:0;display:none;}",
    "#wbdesk-panel .wall:hover .wall-del{display:block;}",
    "#wbdesk-panel .wall-upload{grid-column:1/-1;height:52px;border:1.5px dashed var(--p-line);border-radius:9px;",
    "display:flex;align-items:center;justify-content:center;color:var(--p-sub);font-size:11px;cursor:pointer;text-align:center;}",
    "#wbdesk-panel .wall-upload.drag{border-color:var(--p-btn);color:var(--p-ghost-fg);}",
    /* 壁纸蒙版 / 毛玻璃滑块（参考 WorkDaddy 背景蒙版 / 背景毛玻璃） */
    "#wbdesk-panel .slider-row{padding:8px 0 2px;gap:10px;}",
    "#wbdesk-panel input[type=range]{-webkit-appearance:none;appearance:none;width:120px;height:4px;border-radius:2px;",
    "background:var(--p-sw);outline:none;margin:0;padding:0;border:none;flex:none;cursor:pointer;}",
    "#wbdesk-panel input[type=range]::-webkit-slider-thumb{-webkit-appearance:none;width:14px;height:14px;border-radius:50%;",
    "background:var(--p-btn);cursor:pointer;border:none;}",
    "#wbdesk-panel .slider-val{font-size:11px;color:var(--p-sub);width:34px;text-align:right;flex:none;}",
    /* 官方默认头像（还原 WorkBuddy 官方头像） */
    "#wbdesk-panel .avatar-off{width:100%;height:100%;display:flex;align-items:center;justify-content:center;background:var(--p-input);border-radius:10px;font-size:10.5px;color:var(--p-sub);}",
    /* 模型搜索 + 行操作 */
    "#wbdesk-panel .mname{overflow:hidden;text-overflow:ellipsis;white-space:nowrap;}",
    /* 引用消息悬浮按钮 */
    "#wbdesk-quote{position:fixed;z-index:2147483647;display:none;border:none;border-radius:14px;",
    "background:#f2f3f5;color:#17181c;font-size:11.5px;padding:4px 12px;cursor:pointer;",
    "box-shadow:0 4px 14px rgba(0,0,0,.45);font-family:inherit;}",
    "#wbdesk-quote:hover{background:#fff;}",
    /* 主题 Tab：外观分段选择 / 头像图片 / 壁纸宫格样式见上方主题区块 */
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
    /* 主题 Tab（外观 / 头像 / 壁纸，全部作用于 WorkBuddy 本体，参考 WorkDaddy「主题」页） */
    '<div class="pane" data-pane="theme">' +
    '<div class="grp"><span class="grp-t">外观</span></div>' +
    '<div class="card"><div class="themes" id="wbdesk-themes"></div>' +
    '<div class="tip">外观作用于 WorkBuddy 界面：自定义主题注入色板并联动官方深浅色（防弹回），刷新后自动恢复。</div></div>' +
    '<div class="grp"><span class="grp-t">宠物</span></div>' +
    '<div class="card"><div class="row"><span class="lbl"><b>悬浮机器人皮肤</b><em>Codex 宠物精灵动画（素材见 pets 目录 LICENSE）</em></span></div>' +
    '<div class="petgrid" id="wbdesk-petgrid"><div class="tip" style="grid-column:1/-1">宠物加载中…</div></div>' +
    "</div>" +
    '<div class="grp"><span class="grp-t">头像</span></div>' +
    '<div class="card"><div class="row"><span class="lbl"><b>用户头像</b><em>替换 WorkBuddy 左下角用户菜单头像</em></span></div>' +
    '<div class="avats" id="wbdesk-avatars"></div>' +
    '<input type="file" id="wbdesk-avatar-file" accept="image/png,image/jpeg,image/webp" style="display:none">' +
    "</div>" +
    '<div class="grp"><span class="grp-t">壁纸</span></div>' +
    '<div class="card">' +
    '<div class="row"><span class="lbl"><b>WorkBuddy 壁纸</b><em>铺在 WorkBuddy 界面底层</em></span></div>' +
    '<div class="seg2 bg-src" id="wbdesk-wall-src">' +
    '<button data-src="preset">预设壁纸</button><button data-src="custom">自定义壁纸</button>' +
    "</div>" +
    '<div class="walls" id="wbdesk-walls"><div class="tip" style="grid-column:1/-1">壁纸加载中…</div></div>' +
    '<div class="row slider-row">' +
    '<span class="lbl"><b>背景蒙版</b><em>压暗壁纸保证可读</em></span>' +
    '<input type="range" id="wbdesk-mask" min="0" max="100" step="1" value="30">' +
    '<span class="slider-val" id="wbdesk-mask-val">30%</span></div>' +
    '<div class="row slider-row">' +
    '<span class="lbl"><b>背景毛玻璃</b><em>模糊壁纸</em></span>' +
    '<input type="range" id="wbdesk-blur" min="0" max="100" step="1" value="0">' +
    '<span class="slider-val" id="wbdesk-blur-val">0%</span></div>' +
    '<div class="row"><span class="lbl"><b>消息文字阴影</b><em>壁纸下提升消息可读性</em></span>' +
    '<span class="sw" id="wbdesk-tshadow"><i></i></span></div>' +
    '<div class="row" style="padding:8px 0 2px">' +
    '<button class="ghost" id="wbdesk-wall-reset" style="flex:1">不使用壁纸</button></div>' +
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
    if (state.tab === "theme") { ensureWalls(); ensurePets(); } // 首次打开主题页时拉取壁纸库与宠物库
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
      state.petWaveUntil = Date.now() + 1600; // 宠物挥手迎宾
      if (state.pet) petStart();
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

  // ---------- 面板交互：主题（外观 / 头像 / 壁纸，参考 WorkDaddy「主题」页） ----------
  var AVATAR_SEL_KEY = "wbdesk-avatar-sel";
  var AVATAR_CUSTOM_KEY = "wbdesk-avatars-custom";
  var AVATAR_MAX = 8;  // 自定义头像上限
  var WALL_MAX = 8;    // 自定义壁纸上限

  // ===== 外观（作用于 WorkBuddy 本体：色板注入 + 原生深浅色联动，选择持久化在桌面端） =====

  var THEMES = [
    { id: "default", name: "官方浅色", c1: "#f7f8fa", c2: "#e2e5ea" },
    { id: "dark", name: "官方深色", c1: "#262a31", c2: "#12141a" },
    { id: "eye-care", name: "护眼绿", c1: "#f0f5ec", c2: "#3b6d11" },
    { id: "cyber-purple", name: "赛博紫", c1: "#1a1729", c2: "#7f77dd" },
    { id: "glass", name: "毛玻璃", c1: "#17171b", c2: "#5a5a66" },
  ];
  var themeState = { id: "", wallpaper: "", mask: 30, blur: 0, textShadow: true };

  function renderThemes() {
    var box = panel.querySelector("#wbdesk-themes");
    if (!box) return;
    box.innerHTML = THEMES.map(function (t) {
      var on = themeState.id === t.id;
      return '<button class="theme-opt' + (on ? " on" : "") + '" data-theme-id="' + t.id + '" title="' + t.name + '">' +
        '<span class="chip" style="background:linear-gradient(135deg,' + t.c1 + " 0 55%," + t.c2 + " 55% 100%)\"></span>" +
        '<span class="tname">' + t.name + "</span></button>";
    }).join("");
  }
  panel.querySelector("#wbdesk-themes").addEventListener("click", function (e) {
    var b = e.target.closest("[data-theme-id]");
    if (b) send("theme_apply", { id: b.getAttribute("data-theme-id") });
  });
  // 桌面端推送主题状态（注入后 / 每次应用后）
  window.__wbdeskSetTheme = function (v) {
    if (!v) return;
    themeState = v;
    renderThemes();
    renderWalls();
    syncThemeControls();
  };
  function syncThemeControls() {
    var m = panel.querySelector("#wbdesk-mask"), mv = panel.querySelector("#wbdesk-mask-val");
    var b = panel.querySelector("#wbdesk-blur"), bv = panel.querySelector("#wbdesk-blur-val");
    var sw = panel.querySelector("#wbdesk-tshadow");
    if (m) m.value = themeState.mask;
    if (mv) mv.textContent = themeState.mask + "%";
    if (b) b.value = themeState.blur;
    if (bv) bv.textContent = themeState.blur + "%";
    if (sw) sw.classList.toggle("on", !!themeState.textShadow);
  }
  panel.querySelector("#wbdesk-tshadow").addEventListener("click", function () {
    send("theme_cfg", { on: !themeState.textShadow });
  });

  // ===== 头像（官方默认 / 预设 / 自定义上传；替换 WorkBuddy 左下角用户菜单头像，参考 WorkDaddy） =====

  // 预设头像：几何机器人肖像（SVG 图片，配色与桌面端 App 图标同族）
  var AVATAR_PRESETS = [
    { id: "ink", name: "墨蓝", c1: "#2b3550", c2: "#1c2438", dot: "#478cbf" },
    { id: "coral", name: "珊瑚橙", c1: "#e8735a", c2: "#c9563f", dot: "#ffd9a0" },
    { id: "mint", name: "薄荷绿", c1: "#3aa88f", c2: "#2b8571", dot: "#b8f0d4" },
    { id: "violet", name: "堇紫", c1: "#6d5bd0", c2: "#5343ab", dot: "#c9b8ff" },
    { id: "graphite", name: "石墨", c1: "#3c4250", c2: "#262b36", dot: "#8fc1e3" },
  ];

  function robotAvatarSVG(p) {
    var svg = '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 88 88">' +
      '<defs><linearGradient id="g" x1="0" y1="0" x2="1" y2="1">' +
      '<stop offset="0" stop-color="' + p.c1 + '"/><stop offset="1" stop-color="' + p.c2 + '"/>' +
      "</linearGradient></defs>" +
      '<rect x="41" y="6" width="6" height="14" rx="3" fill="' + p.c1 + '"/>' +
      '<circle cx="44" cy="8" r="5" fill="' + p.dot + '"/>' +
      '<rect x="8" y="20" width="72" height="60" rx="18" fill="url(#g)"/>' +
      '<rect x="22" y="40" width="9" height="16" rx="4.5" fill="#eaf2fb"/>' +
      '<rect x="57" y="40" width="9" height="16" rx="4.5" fill="#eaf2fb"/>' +
      "</svg>";
    return "data:image/svg+xml," + encodeURIComponent(svg);
  }

  var avatarCustom = []; // 自定义头像 dataURL 列表
  try { avatarCustom = JSON.parse(localStorage.getItem(AVATAR_CUSTOM_KEY) || "[]") || []; } catch (e) {}
  var avatarSel = null; // {type:"official"} | {type:"preset",id} | {type:"custom",idx}
  try { avatarSel = JSON.parse(localStorage.getItem(AVATAR_SEL_KEY) || "null"); } catch (e) {}
  if (!avatarSel || (avatarSel.type === "preset" && !AVATAR_PRESETS.filter(function (x) { return x.id === avatarSel.id; })[0])) {
    avatarSel = { type: "official" }; // 首次使用 / 非法值：不覆盖官方头像
  }

  var AVATAR_TARGET_SEL = ".user-menu-trigger-avatar, .cr-agent .cr-agent__logo"; // 用户菜单头像 + 对话消息机器人头像（内联 SVG）
  var avatarOrigURL = null; // 首次捕获的官方头像（面板卡片预览用）

  function avatarURL(sel) {
    if (!sel) return "";
    if (sel.type === "custom" && avatarCustom[sel.idx] != null) return avatarCustom[sel.idx];
    var p = AVATAR_PRESETS.filter(function (x) { return x.id === sel.id; })[0] || AVATAR_PRESETS[0];
    return robotAvatarSVG(p);
  }

  function saveAvatarCustom() {
    try { localStorage.setItem(AVATAR_CUSTOM_KEY, JSON.stringify(avatarCustom)); } catch (e) {
      toast("头像保存失败，请清理存储空间后重试");
    }
  }

  // WorkDaddy 同款覆盖层方案：不改官方 <img> 的 src（React 重渲染会立刻写回），
  // 而是隐藏官方图（visibility 保留占位防塌陷）+ 在容器内叠加自定义 <img>。
  function rememberOrigAvatar(wrap) {
    var imgs = wrap.querySelectorAll("img:not([data-wbdesk-avatar-app]):not([data-wbdesk-orig])");
    for (var i = 0; i < imgs.length; i++) {
      imgs[i].setAttribute("data-wbdesk-orig", "1");
      if (avatarOrigURL == null) avatarOrigURL = imgs[i].getAttribute("src") || "";
    }
    // 对话消息头像（.cr-agent__logo）是内联 SVG 而非 <img>，同样标记以便隐藏/还原
    var svgs = wrap.querySelectorAll("svg:not([data-wbdesk-orig])");
    for (var s = 0; s < svgs.length; s++) svgs[s].setAttribute("data-wbdesk-orig", "1");
  }

  function restoreAvatarDom() {
    var wraps = document.querySelectorAll(AVATAR_TARGET_SEL);
    for (var w = 0; w < wraps.length; w++) {
      var wrap = wraps[w];
      var apps = wrap.querySelectorAll("img[data-wbdesk-avatar-app]");
      for (var a = 0; a < apps.length; a++) apps[a].remove();
      var origs = wrap.querySelectorAll("[data-wbdesk-orig]");
      for (var b = 0; b < origs.length; b++) {
        origs[b].style.visibility = "";
        origs[b].removeAttribute("data-wbdesk-orig");
      }
      if (wrap.style.backgroundImage) wrap.style.backgroundImage = "";
      if (wrap.getAttribute("data-wbdesk-pos")) {
        wrap.style.position = "";
        wrap.removeAttribute("data-wbdesk-pos");
      }
    }
  }

  function applyAvatarToWrap(wrap, url) {
    rememberOrigAvatar(wrap);
    // 隐藏官方图（不能 display:none——容器尺寸由它撑开，会塌陷 0×0；SVG 同理）
    var origs = wrap.querySelectorAll("[data-wbdesk-orig]");
    for (var i = 0; i < origs.length; i++) {
      if (origs[i].style.visibility !== "hidden") origs[i].style.visibility = "hidden";
    }
    if (wrap.style.backgroundImage && wrap.style.backgroundImage !== "none") wrap.style.backgroundImage = "none";
    if (getComputedStyle(wrap).position === "static") {
      wrap.style.position = "relative";
      wrap.setAttribute("data-wbdesk-pos", "1");
    }
    var app = wrap.querySelector("img[data-wbdesk-avatar-app]");
    if (!app) {
      app = document.createElement("img");
      app.setAttribute("data-wbdesk-avatar-app", "1");
      app.alt = "";
      wrap.appendChild(app);
    }
    var st = app.style;
    st.cssText = "width:100%;height:100%;border-radius:50%;position:absolute;inset:0;object-fit:cover;pointer-events:none;";
    if (app.getAttribute("src") !== url) app.setAttribute("src", url);
  }

  function applyAvatar() {
    var custom = avatarSel && avatarSel.type !== "official";
    var url = custom ? avatarURL(avatarSel) : "";
    var wraps = document.querySelectorAll(AVATAR_TARGET_SEL);
    if (!wraps.length) { renderAvatars(); return; }
    if (!custom) {
      // 官方默认：还原官方头像（含对话消息 SVG 头像）
      restoreAvatarDom();
      renderAvatars();
      try { localStorage.setItem(AVATAR_SEL_KEY, JSON.stringify(avatarSel)); } catch (e) {}
      return;
    }
    for (var w = 0; w < wraps.length; w++) applyAvatarToWrap(wraps[w], url);
    renderAvatars();
    try { localStorage.setItem(AVATAR_SEL_KEY, JSON.stringify(avatarSel)); } catch (e) {}
  }
  // 2s 保活：React 重渲染/切会话会重建头像节点，定时整刷（幂等，参考 WorkDaddy setBuildInterval）
  var avatarTimer = setInterval(function () {
    if (window.__wbdeskGen !== GEN) { clearInterval(avatarTimer); return; } // 旧实例自动停摆
    applyAvatar();
  }, 2000);
  state.cleanup.push(function () { clearInterval(avatarTimer); });

  function renderAvatars() {
    var box = panel.querySelector("#wbdesk-avatars");
    if (!box) return;
    var html = '<button class="avat' + (avatarSel.type === "official" ? " on" : "") + '" data-avatar-official="1" title="官方默认">' +
      '<span class="avatar-off">官方</span></button>';
    html += AVATAR_PRESETS.map(function (p) {
      var on = avatarSel.type === "preset" && avatarSel.id === p.id;
      return '<button class="avat' + (on ? " on" : "") + '" data-avatar-preset="' + p.id + '" title="' + p.name + '">' +
        '<img src="' + robotAvatarSVG(p) + '" alt="' + p.name + '"></button>';
    }).join("");
    html += avatarCustom.map(function (url, i) {
      var on = avatarSel.type === "custom" && avatarSel.idx === i;
      return '<button class="avat' + (on ? " on" : "") + '" data-avatar-custom="' + i + '" title="自定义头像">' +
        '<img src="' + url + '" alt="自定义头像">' +
        '<span class="avat-del" data-avatar-del="' + i + '" title="删除该头像">✕</span></button>';
    }).join("");
    html += '<button class="avat add" id="wbdesk-avatar-add" title="添加头像">+</button>';
    box.innerHTML = html;
  }

  panel.querySelector("#wbdesk-avatars").addEventListener("click", function (e) {
    var del = e.target.closest("[data-avatar-del]");
    if (del) {
      var di = parseInt(del.getAttribute("data-avatar-del"), 10);
      avatarCustom.splice(di, 1);
      saveAvatarCustom();
      if (avatarSel.type === "custom") {
        if (avatarSel.idx === di) avatarSel = { type: "official" };
        else if (avatarSel.idx > di) avatarSel = { type: "custom", idx: avatarSel.idx - 1 };
      }
      applyAvatar();
      return;
    }
    if (e.target.closest("[data-avatar-official]")) { avatarSel = { type: "official" }; applyAvatar(); return; }
    var pre = e.target.closest("[data-avatar-preset]");
    if (pre) { avatarSel = { type: "preset", id: pre.getAttribute("data-avatar-preset") }; applyAvatar(); return; }
    var cus = e.target.closest("[data-avatar-custom]");
    if (cus) { avatarSel = { type: "custom", idx: parseInt(cus.getAttribute("data-avatar-custom"), 10) }; applyAvatar(); return; }
    if (e.target.closest("#wbdesk-avatar-add")) panel.querySelector("#wbdesk-avatar-file").click();
  });
  panel.querySelector("#wbdesk-avatar-file").addEventListener("change", function () {
    var f = this.files && this.files[0];
    this.value = "";
    if (!f) return;
    var rd = new FileReader();
    rd.onload = function () {
      var img = new Image();
      img.onload = function () {
        try {
          // 居中裁剪成 96x96，webp 0.85，控制本机占用（同 WorkDaddy 头像处理）
          var size = Math.min(img.width, img.height);
          var cv = document.createElement("canvas");
          cv.width = 96; cv.height = 96;
          cv.getContext("2d").drawImage(img, (img.width - size) / 2, (img.height - size) / 2, size, size, 0, 0, 96, 96);
          if (avatarCustom.length >= AVATAR_MAX) { toast("自定义头像最多 " + AVATAR_MAX + " 张，请先删除再上传"); return; }
          avatarCustom.push(cv.toDataURL("image/webp", 0.85));
          saveAvatarCustom();
          avatarSel = { type: "custom", idx: avatarCustom.length - 1 };
          applyAvatar();
        } catch (e) {
          toast("头像图片处理失败");
        }
      };
      img.onerror = function () { toast("头像图片读取失败"); };
      img.src = rd.result;
    };
    rd.readAsDataURL(f);
  });

  // ===== 壁纸（铺在 WorkBuddy #root；选择 / 上传 / 蒙版 / 毛玻璃全部下发桌面端持久化并经 CDP 应用） =====

  var walls = [];             // 桌面端推送的壁纸库 [{id:"preset:…"/"custom:…", name, url}]
  var wallSrcView = "preset"; // 壁纸区当前视图：预设 / 自定义
  var wallsReq = false;       // 本轮注入是否已请求过壁纸库

  function customCount() {
    var n = 0;
    for (var i = 0; i < walls.length; i++) if (walls[i].id.indexOf("custom:") === 0) n++;
    return n;
  }

  function renderWalls() {
    var box = panel.querySelector("#wbdesk-walls");
    if (!box) return;
    panel.querySelectorAll("#wbdesk-wall-src button").forEach(function (b) {
      b.classList.toggle("on", b.getAttribute("data-src") === wallSrcView);
    });
    if (wallSrcView === "preset") {
      var presets = walls.filter(function (w) { return w.id.indexOf("preset:") === 0; });
      box.innerHTML = presets.length
        ? presets.map(function (w) {
            var on = themeState.wallpaper === w.id;
            return '<button class="wall' + (on ? " on" : "") + '" data-wall-id="' + w.id + '" title="' + w.name + '">' +
              '<img src="' + w.url + '" alt="' + w.name + '"></button>';
          }).join("")
        : '<div class="tip" style="grid-column:1/-1">' + (wallsReq ? "暂无内置壁纸" : "壁纸加载中…") + "</div>";
      return;
    }
    var customs = walls.filter(function (w) { return w.id.indexOf("custom:") === 0; });
    var html = '<div class="wall-upload" id="wbdesk-wall-upload" title="点击选择图片，或拖拽到此处">' +
      "点击或拖拽上传壁纸（PNG / JPG / WebP，保存在本机）</div>";
    html += customs.map(function (w) {
      var on = themeState.wallpaper === w.id;
      return '<button class="wall' + (on ? " on" : "") + '" data-wall-id="' + w.id + '" title="自定义壁纸">' +
        '<img src="' + w.url + '" alt="自定义壁纸">' +
        '<span class="wall-del" data-wall-del="' + w.id + '" title="删除该壁纸">✕</span></button>';
    }).join("");
    if (!customs.length) {
      html += '<div class="tip" style="grid-column:1/-1">还没有自定义壁纸，先上传一张（最多 ' + WALL_MAX + " 张）</div>";
    }
    box.innerHTML = html;
  }

  function readWallFile(f) {
    if (!f) return;
    if (!/^image\/(png|jpe?g|webp)$/i.test(f.type)) { toast("仅支持 PNG / JPG / WebP"); return; }
    var rd = new FileReader();
    rd.onload = function () {
      var img = new Image();
      img.onload = function () {
        try {
          // 压缩到最长边 1280，webp 0.8（落盘在桌面端，应用时按需经 CDP 下发）
          var scale = Math.min(1, 1280 / Math.max(img.width, img.height));
          var cv = document.createElement("canvas");
          cv.width = Math.round(img.width * scale);
          cv.height = Math.round(img.height * scale);
          cv.getContext("2d").drawImage(img, 0, 0, cv.width, cv.height);
          send("wall_add", { dataUrl: cv.toDataURL("image/webp", 0.8) }); // 落盘后桌面端自动应用并回推列表
        } catch (e) { toast("图片处理失败"); }
      };
      img.onerror = function () { toast("图片读取失败"); };
      img.src = rd.result;
    };
    rd.readAsDataURL(f);
  }

  panel.querySelector("#wbdesk-wall-src").addEventListener("click", function (e) {
    var b = e.target.closest("button[data-src]");
    if (!b) return;
    wallSrcView = b.getAttribute("data-src");
    renderWalls();
  });

  (function wireWalls() {
    var card = panel.querySelector("#wbdesk-wall-src").closest(".card");
    card.addEventListener("click", function (e) {
      var del = e.target.closest("[data-wall-del]");
      if (del) { send("wall_del", { id: del.getAttribute("data-wall-del") }); return; }
      var w = e.target.closest("[data-wall-id]");
      if (w) { send("theme_wall", { wall: w.getAttribute("data-wall-id") }); return; }
      if (e.target.closest("#wbdesk-wall-upload")) {
        if (customCount() >= WALL_MAX) { toast("自定义壁纸最多 " + WALL_MAX + " 张，请先删除再上传"); return; }
        var inp = panel.querySelector("#wbdesk-wall-file");
        if (inp) inp.click();
      }
    });
    // 拖拽上传区随 renderWalls 重建，用事件委托挂拖拽
    card.addEventListener("dragover", function (e) {
      var up2 = e.target.closest("#wbdesk-wall-upload");
      if (!up2) return;
      e.preventDefault();
      up2.classList.add("drag");
    });
    card.addEventListener("dragleave", function (e) {
      var up2 = e.target.closest("#wbdesk-wall-upload");
      if (up2) up2.classList.remove("drag");
    });
    card.addEventListener("drop", function (e) {
      var up2 = e.target.closest("#wbdesk-wall-upload");
      if (!up2) return;
      e.preventDefault();
      up2.classList.remove("drag");
      var f = e.dataTransfer && e.dataTransfer.files && e.dataTransfer.files[0];
      readWallFile(f);
    });
  })();
  // 自定义壁纸文件选择器（隐藏 input，由上传区触发）
  (function () {
    var inp = document.createElement("input");
    inp.type = "file";
    inp.id = "wbdesk-wall-file";
    inp.accept = "image/png,image/jpeg,image/webp";
    inp.style.display = "none";
    inp.addEventListener("change", function () {
      var f = inp.files && inp.files[0];
      inp.value = "";
      readWallFile(f);
    });
    panel.querySelector(".p-body").appendChild(inp);
  })();

  panel.querySelector("#wbdesk-wall-reset").addEventListener("click", function () {
    send("theme_wall", { wall: "" });
  });

  // 蒙版 / 毛玻璃滑块：即时更新显示，停止 300ms 后下发（同 WorkDaddy 防抖）
  (function wireSliders() {
    var maskTimer = null, blurTimer = null;
    panel.querySelector("#wbdesk-mask").addEventListener("input", function () {
      panel.querySelector("#wbdesk-mask-val").textContent = this.value + "%";
      clearTimeout(maskTimer);
      maskTimer = setTimeout(function () {
        send("theme_cfg", { mask: parseInt(panel.querySelector("#wbdesk-mask").value, 10) });
      }, 300);
    });
    panel.querySelector("#wbdesk-blur").addEventListener("input", function () {
      panel.querySelector("#wbdesk-blur-val").textContent = this.value + "%";
      clearTimeout(blurTimer);
      blurTimer = setTimeout(function () {
        send("theme_cfg", { blur: parseInt(panel.querySelector("#wbdesk-blur").value, 10) });
      }, 300);
    });
  })();

  // 壁纸库由桌面端按需推送（对应 WorkDaddy 的 daemon /api/wallpapers 通道）
  function ensureWalls() {
    if (wallsReq) return;
    wallsReq = true;
    send("walls", {});
  }
  window.__wbdeskSetWalls = function (list) {
    walls = list || [];
    renderWalls();
  };

  // ===== 宠物（Codex 宠物精灵格式：192×208/帧、8 列 × 9 状态行，参考 workbuddy/pets） =====

  var pets = [];        // 桌面端推送的内置宠物 [{id,name,preview,sprite}]
  var petsReq = false;  // 本轮注入是否已请求过宠物库

  // 帧表（与 petdex-desktop sprite.zig 一致）：[帧列, 停留 ms]
  var PET_ANIM = {
    idle:    { row: 0, frames: [[0, 280], [1, 110], [2, 110], [3, 140], [4, 140], [5, 320]] },
    waving:  { row: 3, frames: [[0, 140], [1, 140], [2, 140], [3, 280]] },
    jumping: { row: 4, frames: [[0, 140], [1, 140], [2, 140], [3, 140], [4, 280]] },
    failed:  { row: 5, frames: [[0, 140], [1, 140], [2, 140], [3, 140], [4, 140], [5, 140], [6, 140], [7, 240]] },
    waiting: { row: 6, frames: [[0, 150], [1, 150], [2, 150], [3, 150], [4, 150], [5, 260]] },
    running: { row: 7, frames: [[0, 120], [1, 120], [2, 120], [3, 120], [4, 120], [5, 220]] },
  };
  var petAnim = { state: "", frame: 0, timer: null };

  function petDesired() {
    if (Date.now() < (state.petWaveUntil || 0)) return "waving";
    var busy = (state.tasks && state.tasks.busy) || [];
    if (busy.length > 0) return "running";
    return "idle";
  }

  function petStop() {
    if (petAnim.timer) { clearTimeout(petAnim.timer); petAnim.timer = null; }
  }

  function petTick() {
    petAnim.timer = null;
    if (window.__wbdeskGen !== GEN) return; // 旧实例自动停摆
    if (!state.pet) return;
    var sp = fab.querySelector(".wb-sprite");
    if (!sp || !sp._petFrame) return;
    var want = petDesired();
    if (want !== petAnim.state) { petAnim.state = want; petAnim.frame = 0; }
    var def = PET_ANIM[petAnim.state] || PET_ANIM.idle;
    var row = def.row < (sp._petFrame.rows || 11) ? def.row : 0; // 自定义素材行数不足时回落 idle
    var f = def.frames[petAnim.frame % def.frames.length];
    sp.style.backgroundPosition = "-" + (f[0] * sp._petFrame.w) + "px -" + (row * sp._petFrame.h) + "px";
    petAnim.frame++;
    petAnim.timer = setTimeout(petTick, f[1]);
  }
  function petStart() { if (!petAnim.timer) petTick(); }
  state.cleanup.push(petStop); // 宠物动画是 setTimeout 链，清理时必须显式停掉

  function petSheetRows(meta, cb) {
    // 精灵图行数按素材实际尺寸自适应（内置 8×11；自定义可能不同行数）
    if (meta._rows) return cb(meta._rows);
    var img = new Image();
    img.onload = function () {
      var fw = img.naturalWidth / 8;
      var fh = fw * 208 / 192; // 标准帧高（192:208）
      var rows = Math.max(9, Math.round(img.naturalHeight / (fh || 1)));
      meta._rows = rows;
      cb(rows);
    };
    img.onerror = function () { cb(11); };
    img.src = meta.sprite;
  }

  function applyPetVisual() {
    var sp = fab.querySelector(".wb-sprite");
    if (!state.pet) {
      fab.classList.remove("petsprite");
      if (sp) sp.remove();
      petStop();
      return;
    }
    var meta = null;
    for (var i = 0; i < pets.length; i++) if (pets[i].id === state.pet) { meta = pets[i]; break; }
    if (!meta || !meta.sprite) {
      // 素材未到位（如刚恢复选择）：先请求宠物库，数据回推后重放
      ensurePets();
      return;
    }
    petSheetRows(meta, function (rows) {
      if (state.pet !== meta.id) return; // 加载期间用户已切换
      var sp2 = fab.querySelector(".wb-sprite");
      fab.classList.add("petsprite");
      if (!sp2) {
        sp2 = document.createElement("div");
        sp2.className = "wb-sprite";
        fab.querySelector(".wb-robot").appendChild(sp2);
      }
      var h = 62, w = Math.round(62 * 192 / 208); // 帧显示尺寸（保持 192:208 比例）
      sp2._petFrame = { w: w, h: h, rows: rows };
      sp2.style.width = w + "px";
      sp2.style.height = h + "px";
      sp2.style.backgroundImage = "url('" + meta.sprite + "')";
      sp2.style.backgroundSize = (w * 8) + "px " + (h * rows) + "px";
      petAnim.state = "";
      petStart();
    });
  }

  function renderPetGrid() {
    var box = panel.querySelector("#wbdesk-petgrid");
    if (!box) return;
    var html = '<button class="petopt' + (!state.pet ? " on" : "") + '" data-pet-id="" title="经典机器人">' +
      '<span class="petoff">经典</span><span class="pname">经典机器人</span></button>';
    html += pets.map(function (p) {
      var on = state.pet === p.id;
      var del = p.custom ? '<span class="pet-del" data-pet-del="' + p.id + '" title="删除该宠物">✕</span>' : "";
      return '<button class="petopt' + (on ? " on" : "") + '" data-pet-id="' + p.id + '" title="' + p.name + '">' +
        '<img src="' + p.preview + '" alt="' + p.name + '">' + del + '<span class="pname">' + p.name + "</span></button>";
    }).join("");
    html += '<div class="petopt pet-upload" id="wbdesk-pet-upload" title="选择 Codex 宠物精灵图（spritesheet，8 列布局）">' +
      '<span class="petoff">＋</span><span class="pname">上传自定义</span></div>';
    box.innerHTML = html;
  }

  // 自定义宠物上传：读取图片 → 裁第一帧做预览 → 交桌面端落盘并应用
  function uploadPetFile(f) {
    if (!f) return;
    if (!/^image\/(webp|png|jpeg)$/i.test(f.type)) { toast("仅支持 WebP / PNG / JPG 精灵图"); return; }
    var rd = new FileReader();
    rd.onload = function () {
      var img = new Image();
      img.onload = function () {
        try {
          var fw = img.naturalWidth / 8;                 // 8 列
          var fh = Math.round(fw * 208 / 192);           // 标准帧高
          if (fh < 8 || img.naturalHeight < fh) { toast("图片尺寸不像精灵图（需 8 列横排网格）"); return; }
          var cv = document.createElement("canvas");
          cv.width = Math.round(fw); cv.height = fh;
          cv.getContext("2d").drawImage(img, 0, 0, cv.width, cv.height);
          var preview = cv.toDataURL("image/webp", 0.85);
          var name = (f.name || "").replace(/\.[a-z0-9]+$/i, "") || "自定义宠物";
          send("pet_add", { name: name, dataUrl: preview, spriteUrl: rd.result });
          toast("已添加自定义宠物：" + name);
        } catch (e) { toast("精灵图处理失败"); }
      };
      img.onerror = function () { toast("图片读取失败"); };
      img.src = rd.result;
    };
    rd.readAsDataURL(f);
  }

  function ensurePets() {
    if (petsReq) return;
    petsReq = true;
    send("pets", {});
  }
  window.__wbdeskSetPets = function (list) {
    pets = list || [];
    renderPetGrid();
    if (state.pet) applyPetVisual(); // 素材到位后重放选中宠物
  };
  window.__wbdeskSetPet = function (v) {
    state.pet = (v && v.id) || "";
    renderPetGrid();
    applyPetVisual();
    if (state.pet && !pets.length) ensurePets();
  };
  panel.querySelector("#wbdesk-petgrid").addEventListener("click", function (e) {
    var del = e.target.closest("[data-pet-del]");
    if (del) {
      e.stopPropagation();
      send("pet_del", { id: del.getAttribute("data-pet-del") });
      if (state.pet === del.getAttribute("data-pet-del")) state.pet = "";
      return;
    }
    if (e.target.closest("#wbdesk-pet-upload")) {
      var inp = panel.querySelector("#wbdesk-pet-file");
      if (inp) inp.click();
      return;
    }
    var b = e.target.closest("[data-pet-id]");
    if (!b) return;
    state.pet = b.getAttribute("data-pet-id");
    applyPetVisual();
    renderPetGrid();
    send("pet_apply", { id: state.pet });
  });
  // 自定义宠物文件选择器（隐藏 input，由上传卡触发；支持拖拽）
  (function () {
    var inp = document.createElement("input");
    inp.type = "file";
    inp.id = "wbdesk-pet-file";
    inp.accept = "image/webp,image/png,image/jpeg";
    inp.style.display = "none";
    inp.addEventListener("change", function () {
      var f = inp.files && inp.files[0];
      inp.value = "";
      uploadPetFile(f);
    });
    panel.querySelector(".p-body").appendChild(inp);
    var grid = panel.querySelector("#wbdesk-petgrid");
    grid.addEventListener("dragover", function (e) {
      var up = e.target.closest("#wbdesk-pet-upload");
      if (!up) return;
      e.preventDefault();
      up.classList.add("drag");
    });
    grid.addEventListener("dragleave", function (e) {
      var up = e.target.closest("#wbdesk-pet-upload");
      if (up) up.classList.remove("drag");
    });
    grid.addEventListener("drop", function (e) {
      var up = e.target.closest("#wbdesk-pet-upload");
      if (!up) return;
      e.preventDefault();
      up.classList.remove("drag");
      var f = e.dataTransfer && e.dataTransfer.files && e.dataTransfer.files[0];
      uploadPetFile(f);
    });
  })();
  state.cleanup.push(function () { petStop(); });

  (function restoreTheme() {
    renderThemes();
    syncThemeControls();
    applyAvatar();
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
    if (state.pet) petStart(); // 任务运行状态切换宠物动画（running/idle）
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
    delete window.__wbdeskSetWalls;
    delete window.__wbdeskSetTheme;
    delete window.__wbdeskSetPet;
    delete window.__wbdeskSetPets;
    delete window.__wbdeskSetSys;
    delete window.__wbdeskToast;
  };

  wake(); // 首次注入启动闲置计时
})();

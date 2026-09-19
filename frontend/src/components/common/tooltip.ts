/**
 * 全局 Tooltip：data-tip 属性驱动，事件委托 + 单例浮层。
 * - fixed 定位：不受滚动容器 overflow 裁剪，也不用给宿主元素加 position
 * - 优先显示在目标上方，空间不足自动翻到下方，水平方向夹在视口内
 * - 悬停 350ms 后出现，移出/滚动/点击立即消失
 * 用法：<button data-tip="提示文字">…</button>（替代原生 title 属性）
 */
let el: HTMLDivElement | null = null;
let timer = 0;
let cur: Element | null = null;

function ensureEl(): HTMLDivElement {
  if (!el) {
    el = document.createElement("div");
    el.className = "app-tip";
    el.setAttribute("role", "tooltip");
    document.body.appendChild(el);
  }
  return el;
}

function show(target: Element) {
  const tip = ensureEl();
  tip.textContent = target.getAttribute("data-tip") || "";
  if (!tip.textContent) return;
  tip.classList.add("show");
  const r = target.getBoundingClientRect();
  const tr = tip.getBoundingClientRect();
  const margin = 9;
  const pos = target.getAttribute("data-tip-pos") || "top";
  if (pos === "right") {
    // 侧边栏场景：气泡显示在目标右侧垂直居中，放不下翻到左侧
    let x = r.right + margin;
    let y = r.top + r.height / 2 - tr.height / 2;
    y = Math.max(8, Math.min(y, window.innerHeight - tr.height - 8));
    if (x + tr.width > window.innerWidth - 8) x = r.left - tr.width - margin;
    tip.style.left = `${Math.round(x)}px`;
    tip.style.top = `${Math.round(y)}px`;
    tip.classList.remove("below");
    return;
  }
  let x = r.left + r.width / 2 - tr.width / 2;
  x = Math.max(8, Math.min(x, window.innerWidth - tr.width - 8));
  let y = r.top - tr.height - margin;
  const below = y < 8;
  if (below) y = r.bottom + margin;
  tip.style.left = `${Math.round(x)}px`;
  tip.style.top = `${Math.round(y)}px`;
  tip.classList.toggle("below", below);
}

function hide() {
  if (timer) {
    clearTimeout(timer);
    timer = 0;
  }
  cur = null;
  el?.classList.remove("show");
}

export function installTooltip() {
  document.addEventListener("mouseover", (e) => {
    const t = (e.target as Element | null)?.closest?.("[data-tip]");
    if (!t || t === cur) return;
    if (!t.getAttribute("data-tip")) {
      hide();
      return;
    }
    if (timer) clearTimeout(timer);
    cur = t;
    timer = window.setTimeout(() => {
      if (cur === t) show(t);
    }, 350);
  });
  document.addEventListener("mouseout", (e) => {
    const t = (e.target as Element | null)?.closest?.("[data-tip]");
    if (t && t === cur) {
      const to = e.relatedTarget;
      if (!(to instanceof Element && t.contains(to))) hide();
    }
  });
  document.addEventListener("mousedown", hide);
  window.addEventListener("scroll", hide, true);
  window.addEventListener("blur", hide);
}

import { useCallback, useEffect, useRef, useState } from "react";

export interface AsyncState<T> {
  data: T | null;
  error: string | null;
  loading: boolean;
  /** 首次加载完成（含失败）后为 true —— 用于区分「加载中」与「确实没有数据」 */
  settled: boolean;
  reload: () => Promise<void>;
}

/**
 * 统一的数据加载：真实错误原样暴露给界面，不做静默兜底。
 *
 * - 出错时 data 保持为上一次成功值（或 null），error 为后端返回的真实错误文本；
 * - 组件卸载后不再 setState，避免无效渲染。
 */
export function useAsync<T>(loader: () => Promise<T>, deps: unknown[] = []): AsyncState<T> {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [settled, setSettled] = useState(false);
  const alive = useRef(true);
  const seq = useRef(0); // 请求序号：只采纳最后一次请求的结果，快速切筛选/分页时旧响应不覆盖新数据
  const loaderRef = useRef(loader);
  loaderRef.current = loader;

  const run = useCallback(async () => {
    const id = ++seq.current;
    setLoading(true);
    try {
      const res = await loaderRef.current();
      if (!alive.current || id !== seq.current) return;
      setData(res);
      setError(null);
    } catch (e) {
      if (!alive.current || id !== seq.current) return;
      setError(errText(e));
    } finally {
      if (alive.current && id === seq.current) {
        setLoading(false);
        setSettled(true);
      }
    }
  }, []);

  useEffect(() => {
    alive.current = true;
    void run();
    return () => {
      alive.current = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, deps);

  return { data, error, loading, settled, reload: run };
}

/** 把任意抛出物转成可读错误文本 */
export function errText(e: unknown): string {
  if (e instanceof Error) return e.message;
  if (typeof e === "string") return e;
  try {
    return JSON.stringify(e);
  } catch {
    return String(e);
  }
}

/** 订阅「全局刷新」事件（页面内改动后广播，其它页面即时同步） */
export function broadcastRefresh(): void {
  window.dispatchEvent(new CustomEvent("app:refresh"));
}

import { useCallback, useEffect, useRef, useState } from 'react';
import { getCached, isFresh, setCached, subscribeRevision } from './cache';

interface Options {
  /** Cache key; omit to disable caching. */
  cacheKey?: string;
  /** Refetch interval in ms; omit or 0 to disable. */
  refreshMs?: number;
  /** Skip the first fetch (used with manual triggers). */
  enabled?: boolean;
}

interface Result<T> {
  data: T | null;
  error: string | null;
  loading: boolean;
  reload: () => Promise<void>;
  /** True while a background refetch is running with stale data shown. */
  refreshing: boolean;
}

// useQuery: fetch-with-cache hook. Shows cached data immediately on mount so
// navigating back to a page does not blank out (fixes the white-flash bug).
export function useQuery<T>(fn: () => Promise<T>, options: Options = {}): Result<T> {
  const { cacheKey, refreshMs = 0, enabled = true } = options;
  const [data, setData] = useState<T | null>(() => (cacheKey ? getCached<T>(cacheKey) ?? null : null));
  const [error, setError] = useState<string | null>(null);
  // Only show the loading skeleton when we have nothing to render at all.
  const [loading, setLoading] = useState(() => data === null);
  const [refreshing, setRefreshing] = useState(false);
  const fnRef = useRef(fn);
  fnRef.current = fn;
  const dataRef = useRef<T | null>(data);
  dataRef.current = data;
  const mounted = useRef(true);
  const seq = useRef(0);

  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);

  const run = useCallback(async (force = false) => {
    if (!force && cacheKey && isFresh(cacheKey) && getCached<T>(cacheKey) !== undefined) {
      setData(getCached<T>(cacheKey) ?? null);
      setLoading(false);
      return;
    }
    const requestId = ++seq.current;
    // Stale-while-revalidate: keep the previous data visible, never blank the
    // page just because a background refresh started.
    if (dataRef.current !== null) setRefreshing(true);
    try {
      const result = await fnRef.current();
      if (!mounted.current || requestId !== seq.current) return;
      // Only real keys are cached: the previous `__anon__<random>` fallback
      // filled the 64-entry store with junk and evicted useful entries.
      if (cacheKey) setCached(cacheKey, result);
      setData(result);
      setError(null);
    } catch (err) {
      if (!mounted.current || requestId !== seq.current) return;
      setError(err instanceof Error ? err.message : 'Request failed');
    } finally {
      if (mounted.current && requestId === seq.current) {
        setLoading(false);
        setRefreshing(false);
      }
    }
  }, [cacheKey]);

  const reload = useCallback(() => run(true), [run]);

  useEffect(() => {
    if (!enabled) return;
    void run();
  }, [enabled, run]);

  // Refetch when any mutation lands (api.ts bumps the global revision).
  useEffect(() => {
    if (!enabled) return;
    return subscribeRevision(() => {
      void run(true);
    });
  }, [enabled, run]);

  useEffect(() => {
    if (!refreshMs || !enabled) return;
    const id = window.setInterval(() => void run(true), refreshMs);
    return () => window.clearInterval(id);
  }, [refreshMs, enabled, run]);

  return { data, error, loading, reload, refreshing };
}

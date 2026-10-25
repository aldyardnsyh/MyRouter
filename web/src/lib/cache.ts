// Tiny staleness-while-revalidate cache so route switches render instantly
// with the previous data instead of flashing a skeleton (UX fix: page
// transitions no longer blink blank while refetching).

interface Entry<T> {
  data: T;
  at: number;
}

const store = new Map<string, Entry<unknown>>();
const TTL = 30_000;

export function getCached<T>(key: string): T | undefined {
  const hit = store.get(key);
  if (!hit) return undefined;
  return hit.data as T;
}

export function setCached<T>(key: string, data: T): void {
  store.set(key, { data, at: Date.now() });
  if (store.size > 64) {
    // Drop oldest entries.
    const oldest = [...store.entries()].sort((a, b) => a[1].at - b[1].at)[0];
    if (oldest) store.delete(oldest[0]);
  }
}

export function isFresh(key: string): boolean {
  const hit = store.get(key);
  return !!hit && Date.now() - hit.at < TTL;
}

export function invalidate(prefix: string): void {
  for (const k of store.keys()) {
    if (k.startsWith(prefix)) store.delete(k);
  }
}

// Global data revision. Every successful mutation bumps it; mounted queries
// subscribe and refetch immediately. Without this, a provider created on one
// page stayed invisible on Models/Routes/Dashboard until their 30s cache TTL
// expired (the "pages don't sync" bug).
let revision = 0;
const listeners = new Set<() => void>();

export function bumpRevision(): void {
  revision += 1;
  for (const fn of listeners) fn();
}

export function subscribeRevision(fn: () => void): () => void {
  listeners.add(fn);
  return () => {
    listeners.delete(fn);
  };
}

export function clearCache(): void {
  store.clear();
}

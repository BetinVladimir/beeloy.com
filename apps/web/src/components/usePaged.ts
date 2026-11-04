"use client";
import { useCallback, useEffect, useRef, useState } from "react";
import { ApiError, api } from "@/lib/api";

/** Cursor-paginated list with "load more". `key` resets the list when filters change. */
export function usePaged<T>(path: string, query: Record<string, string | number | undefined>, key: string, initial?: { items: T[]; next: string | null }) {
  const [items, setItems] = useState<T[]>(initial?.items ?? []);
  const [next, setNext] = useState<string | null>(initial?.next ?? null);
  const [loading, setLoading] = useState(!initial);
  const [error, setError] = useState<ApiError | null>(null);
  const first = useRef(true);
  const seq = useRef(0);
  const queryRef = useRef(query);
  queryRef.current = query;

  const load = useCallback(async (cursor?: string | null) => {
    const my = ++seq.current;
    setLoading(true);
    setError(null);
    try {
      const r = await api<T[]>(path, { query: { ...queryRef.current, cursor: cursor ?? undefined } });
      if (my !== seq.current) return;
      setItems((prev) => (cursor ? [...prev, ...r.data] : r.data));
      setNext(r.next_cursor ?? null);
    } catch (e) {
      if (my === seq.current) setError(e instanceof ApiError ? e : new ApiError(0, "unavailable", "network"));
    } finally {
      if (my === seq.current) setLoading(false);
    }
  }, [path]);

  useEffect(() => {
    if (first.current && initial) {
      first.current = false;
      return;
    }
    first.current = false;
    void load(null);
  }, [key, load]); // eslint-disable-line react-hooks/exhaustive-deps

  return { items, next, loading, error, more: () => next && load(next), reload: () => load(null), setItems };
}

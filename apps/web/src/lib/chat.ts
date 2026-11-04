import type { Message } from "@/contracts/api-types";

/** Merge messages without duplicates (by id, pending ones by client_id) and order them by seq (pending last). */
export function mergeMessages(existing: (Message & { pending?: boolean; failed?: boolean })[], incoming: Message[]) {
  const byId = new Map<string, Message & { pending?: boolean; failed?: boolean }>();
  const byClient = new Map<string, string>();
  for (const m of existing) {
    byId.set(m.id, m);
    byClient.set(m.client_id, m.id);
  }
  for (const m of incoming) {
    const pendingId = byClient.get(m.client_id);
    if (pendingId && pendingId !== m.id) byId.delete(pendingId); // the server copy replaces the optimistic one
    byId.set(m.id, { ...m });
    byClient.set(m.client_id, m.id);
  }
  return [...byId.values()].sort((a, b) => {
    const ap = a.pending || a.failed ? 1 : 0;
    const bp = b.pending || b.failed ? 1 : 0;
    if (ap !== bp) return ap - bp;
    return a.seq - b.seq || a.created_at.localeCompare(b.created_at);
  });
}

/** Reconnect delay: 1,2,4,8,16,30 s with ±20 % jitter. */
export function backoffDelay(attempt: number, random: () => number = Math.random): number {
  const base = Math.min(30, 2 ** Math.max(0, attempt)) * 1000;
  return Math.round(base * (0.8 + 0.4 * random()));
}

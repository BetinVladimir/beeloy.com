"use client";
import { create } from "zustand";
import type { Message } from "@/contracts/api-types";
import { mergeMessages } from "@/lib/chat";

type M = Message & { pending?: boolean; failed?: boolean };

interface ChatState {
  unread: number;
  requests: number;
  tick: number; // bumped when the conversation list must be reloaded
  messages: Record<string, M[]>;
  readSeq: Record<string, number>; // peer's read cursor per conversation (for "read" marks)
  setCounters: (unread: number, requests: number) => void;
  bump: () => void;
  merge: (convId: string, incoming: Message[]) => void;
  addPending: (convId: string, m: M) => void;
  markFailed: (convId: string, clientId: string) => void;
  setPeerRead: (convId: string, seq: number) => void;
  reset: () => void;
}

export const useChat = create<ChatState>((set) => ({
  unread: 0,
  requests: 0,
  tick: 0,
  messages: {},
  readSeq: {},
  setCounters: (unread, requests) => set({ unread, requests }),
  bump: () => set((s) => ({ tick: s.tick + 1 })),
  merge: (convId, incoming) => set((s) => ({ messages: { ...s.messages, [convId]: mergeMessages(s.messages[convId] ?? [], incoming) } })),
  addPending: (convId, m) => set((s) => ({ messages: { ...s.messages, [convId]: mergeMessages(s.messages[convId] ?? [], [m]).map((x) => (x.client_id === m.client_id && !x.seq ? { ...x, pending: true } : x)) } })),
  markFailed: (convId, clientId) =>
    set((s) => ({ messages: { ...s.messages, [convId]: (s.messages[convId] ?? []).map((m) => (m.client_id === clientId ? { ...m, pending: false, failed: true } : m)) } })),
  setPeerRead: (convId, seq) => set((s) => ({ readSeq: { ...s.readSeq, [convId]: Math.max(s.readSeq[convId] ?? 0, seq) } })),
  reset: () => set({ unread: 0, requests: 0, messages: {}, readSeq: {} }),
}));

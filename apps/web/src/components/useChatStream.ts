"use client";
import { useEffect } from "react";
import type { Message } from "@/contracts/api-types";
import { api } from "@/lib/api";
import { backoffDelay } from "@/lib/chat";
import { useChat } from "@/stores/chat";

/**
 * Keeps one EventSource open for the signed-in user. After every (re)connect the client reloads the counters and the
 * open conversations, so events missed while offline are recovered with GET …/messages?after_seq=.
 */
export function useChatStream(userId: string | null, EventSourceImpl: typeof EventSource | undefined = typeof EventSource !== "undefined" ? EventSource : undefined) {
  useEffect(() => {
    if (!userId || !EventSourceImpl) return;
    let es: EventSource | null = null;
    let attempt = 0;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let closed = false;

    const refreshCounters = async () => {
      try {
        const r = await api<{ total: number; requests: number }>("/api/v1/conversations/unread-count");
        useChat.getState().setCounters(r.data.total, r.data.requests);
      } catch {
        /* ignore: next event or reconnect will retry */
      }
    };
    const catchUp = async () => {
      const state = useChat.getState();
      await Promise.all(
        Object.entries(state.messages).map(async ([convId, msgs]) => {
          const last = Math.max(0, ...msgs.filter((m) => !m.pending && !m.failed).map((m) => m.seq));
          try {
            const r = await api<Message[]>(`/api/v1/conversations/${convId}/messages`, { query: { after_seq: last, limit: 100 } });
            if (r.data.length) useChat.getState().merge(convId, r.data);
          } catch {
            /* ignore */
          }
        }),
      );
    };

    const connect = () => {
      if (closed) return;
      es = new EventSourceImpl("/api/v1/events", { withCredentials: true });
      es.addEventListener("ready", () => {
        attempt = 0;
        void refreshCounters();
        void catchUp();
        useChat.getState().bump();
      });
      es.addEventListener("message", (ev) => {
        const d = JSON.parse((ev as MessageEvent).data) as { conversation_id: string; message: Message };
        useChat.getState().merge(d.conversation_id, [d.message]);
        void refreshCounters();
        useChat.getState().bump();
      });
      es.addEventListener("read", (ev) => {
        const d = JSON.parse((ev as MessageEvent).data) as { conversation_id: string; reader_id: string; read_seq: number };
        if (d.reader_id !== userId) useChat.getState().setPeerRead(d.conversation_id, d.read_seq);
        void refreshCounters();
      });
      es.addEventListener("conversation", () => {
        void refreshCounters();
        useChat.getState().bump();
      });
      es.onerror = () => {
        es?.close();
        es = null;
        if (closed) return;
        timer = setTimeout(connect, backoffDelay(attempt++));
      };
    };
    // A dropped network does not always make EventSource fail (the stalled stream can silently lose events):
    // when the browser says it is back online, reconnect at once; "ready" then resynchronises from PostgreSQL.
    const reconnectNow = () => {
      if (closed) return;
      if (timer) clearTimeout(timer);
      es?.close();
      attempt = 0;
      connect();
    };
    const onVisible = () => {
      if (document.visibilityState === "visible") {
        void refreshCounters();
        void catchUp();
      }
    };
    window.addEventListener("online", reconnectNow);
    document.addEventListener("visibilitychange", onVisible);
    connect();
    return () => {
      window.removeEventListener("online", reconnectNow);
      document.removeEventListener("visibilitychange", onVisible);
      closed = true;
      if (timer) clearTimeout(timer);
      es?.close();
      useChat.getState().reset();
    };
  }, [userId, EventSourceImpl]);
}

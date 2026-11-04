"use client";
import { Alert, Avatar, Badge, Button, Group, Indicator, Paper, ScrollArea, Stack, Tabs, Text, Textarea, Title } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import Link from "next/link";
import { useCallback, useEffect, useRef, useState } from "react";
import type { Conversation, Message } from "@/contracts/api-types";
import { ApiError, api } from "@/lib/api";
import { formatDate } from "@/lib/i18n";
import { useChat } from "@/stores/chat";
import { useLocale, useRequireAuth, useT } from "./Providers";
import { ReportDialog } from "./ReportDialog";
import { ErrorAlert, ListSkeleton, LoadMore, Spinner, errorText } from "./States";
import { usePaged } from "./usePaged";

type Folder = "inbox" | "requests" | "sent";

function ConversationList({ folder }: { folder: Folder }) {
  const t = useT();
  const locale = useLocale();
  const tick = useChat((s) => s.tick);
  const { items, next, loading, error, more, reload } = usePaged<Conversation>("/api/v1/conversations", { folder, limit: 20 }, `${folder}:${tick}`);
  return (
    <Stack>
      {error && <ErrorAlert error={error} onRetry={reload} />}
      {loading && items.length === 0 && <ListSkeleton />}
      {!loading && !error && items.length === 0 && <Text c="dimmed">{t("messages.empty")}</Text>}
      {items.map((c) => (
        <Paper key={c.id} withBorder p="sm" component={Link} href={`/${locale}/messages/${c.id}`} style={{ textDecoration: "none", color: "inherit" }}>
          <Group wrap="nowrap">
            <Indicator disabled={c.unread_count === 0 || c.muted} label={c.unread_count} size={16}>
              <Avatar src={c.peer.avatar_url ?? undefined} alt="" radius="xl" />
            </Indicator>
            <div style={{ minWidth: 0, flex: 1 }}>
              <Group gap={6}><Text fw={600}>{c.peer.nickname}</Text><Text size="sm" c="dimmed">@{c.peer.slug}</Text>{c.muted && <Badge size="xs" color="gray">{t("messages.muted")}</Badge>}</Group>
              <Text size="sm" c="dimmed" truncate>{c.last_message_preview}</Text>
            </div>
          </Group>
        </Paper>
      ))}
      <LoadMore next={next} loading={loading} onMore={() => void more()} />
    </Stack>
  );
}

export function ChatPage() {
  const t = useT();
  const { ready } = useRequireAuth();
  const requests = useChat((s) => s.requests);
  const [tab, setTab] = useState<string | null>("inbox");
  if (!ready) return <Spinner />;
  return (
    <Stack>
      <Title order={1}>{t("messages.title")}</Title>
      <Tabs value={tab} onChange={setTab}>
        <Tabs.List>
          <Tabs.Tab value="inbox">{t("messages.inbox")}</Tabs.Tab>
          <Tabs.Tab value="requests" rightSection={requests > 0 ? <Badge size="xs" circle color="red">{requests}</Badge> : null}>{t("messages.requests")}</Tabs.Tab>
          <Tabs.Tab value="sent">{t("messages.sent")}</Tabs.Tab>
        </Tabs.List>
        {(["inbox", "requests", "sent"] as Folder[]).map((f) => <Tabs.Panel key={f} value={f} pt="md">{tab === f && <ConversationList folder={f} />}</Tabs.Panel>)}
      </Tabs>
    </Stack>
  );
}

export function ChatView({ id }: { id: string }) {
  const t = useT();
  const locale = useLocale();
  const { me, ready } = useRequireAuth();
  const messages = useChat((s) => s.messages[id]) ?? [];
  const peerRead = useChat((s) => s.readSeq[id] ?? 0);
  const tick = useChat((s) => s.tick);
  const [conv, setConv] = useState<Conversation | null>(null);
  const [error, setError] = useState<ApiError | null>(null);
  const [text, setText] = useState("");
  const [oldest, setOldest] = useState<number | null>(null);
  const [more, setMore] = useState(false);
  const bottom = useRef<HTMLDivElement>(null);

  const loadConv = useCallback(async () => {
    try {
      const r = await api<Conversation[]>("/api/v1/conversations", { query: { limit: 50 } });
      let c = r.data.find((x) => x.id === id);
      if (!c) { // requests / sent folders
        for (const f of ["requests", "sent"]) {
          const rr = await api<Conversation[]>("/api/v1/conversations", { query: { folder: f, limit: 50 } });
          c = rr.data.find((x) => x.id === id);
          if (c) break;
        }
      }
      if (c) { setConv(c); setError(null); } else setError(new ApiError(404, "not_found", "not found"));
    } catch (e) {
      // a dropped network is not a verdict on the conversation: keep what is shown and retry when the browser is back online
      if (e instanceof ApiError) setError(e);
    }
  }, [id]);

  useEffect(() => { if (ready) void loadConv(); }, [ready, loadConv, tick]);
  useEffect(() => {
    const again = () => { if (ready) void loadConv(); };
    window.addEventListener("online", again);
    return () => window.removeEventListener("online", again);
  }, [ready, loadConv]);

  useEffect(() => {
    if (!ready) return;
    api<Message[]>(`/api/v1/conversations/${id}/messages`, { query: { limit: 30 } })
      .then((r) => {
        useChat.getState().merge(id, r.data);
        setOldest(r.data[0]?.seq ?? null);
        setMore(r.data.length === 30);
      })
      .catch((e) => { if (e instanceof ApiError) setError(e); });
  }, [ready, id]);

  const lastSeq = Math.max(0, ...messages.filter((m) => !m.pending && !m.failed).map((m) => m.seq));
  const lastOtherSeq = Math.max(0, ...messages.filter((m) => m.sender_id !== me?.id).map((m) => m.seq));
  useEffect(() => {
    if (!ready || lastOtherSeq === 0 || document.visibilityState !== "visible") return;
    const timer = setTimeout(() => {
      void api(`/api/v1/conversations/${id}/read`, { method: "POST", body: { seq: lastOtherSeq } }).catch(() => {});
    }, 500);
    return () => clearTimeout(timer);
  }, [ready, id, lastOtherSeq]);
  useEffect(() => { bottom.current?.scrollIntoView?.({ block: "end" }); }, [messages.length]);

  const send = async (clientId: string, body: string) => {
    try {
      const r = await api<Message>(`/api/v1/conversations/${id}/messages`, { method: "POST", body: { client_id: clientId, body } });
      useChat.getState().merge(id, [r.data]);
    } catch (e) {
      useChat.getState().markFailed(id, clientId);
      notifications.show({ color: "red", message: errorText(t, e as ApiError) });
    }
  };
  const submit = () => {
    const body = text.trim();
    if (!body || !me) return;
    const clientId = crypto.randomUUID();
    useChat.getState().addPending(id, { id: `pending-${clientId}`, conversation_id: id, seq: 0, sender_id: me.id, client_id: clientId, body, deleted: false, created_at: new Date().toISOString(), pending: true } as Message);
    setText("");
    void send(clientId, body);
  };

  const act = async (path: string, method = "POST") => {
    try { await api(path, { method }); await loadConv(); useChat.getState().bump(); } catch (e) { notifications.show({ color: "red", message: errorText(t, e as ApiError) }); }
  };

  if (!ready) return <Spinner />;
  if (error) return <ErrorAlert error={error} />;
  if (!conv) return <Spinner />;
  const sentByMe = messages.filter((m) => m.sender_id === me?.id && !m.pending && !m.failed);
  const myCount = sentByMe.length;
  const locked = conv.status === "request" && conv.initiated_by_me && myCount >= 3;
  const declined = conv.status === "declined" && conv.initiated_by_me;
  return (
    <Stack>
      <Group justify="space-between">
        <Group>
          <Avatar src={conv.peer.avatar_url ?? undefined} alt="" radius="xl" />
          <div>
            <Link href={`/${locale}/people/${conv.peer.slug}`}><Text fw={700}>{conv.peer.nickname}</Text></Link>
            <Text size="sm" c="dimmed">@{conv.peer.slug}</Text>
          </div>
        </Group>
        <Group gap="xs">
          <Button size="compact-xs" variant="subtle" onClick={() => void act(`/api/v1/conversations/${id}/mute`, conv.muted ? "DELETE" : "PUT")}>{conv.muted ? t("messages.unmute") : t("messages.mute")}</Button>
          <Button size="compact-xs" variant="subtle" color="red" onClick={() => void act(`/api/v1/users/${conv.peer.user_id}/block`, conv.peer.blocked_by_me ? "DELETE" : "PUT")}>{conv.peer.blocked_by_me ? t("messages.unblock") : t("messages.block")}</Button>
        </Group>
      </Group>
      {conv.status === "request" && !conv.initiated_by_me && (
        <Alert color="brand" title={t("messages.request_title", { name: conv.peer.nickname })}>
          <Group><Button size="xs" onClick={() => void act(`/api/v1/conversations/${id}/accept`)}>{t("messages.accept")}</Button>
            <Button size="xs" variant="default" onClick={() => void act(`/api/v1/conversations/${id}/decline`)}>{t("messages.decline")}</Button></Group>
        </Alert>
      )}
      {conv.status === "request" && conv.initiated_by_me && <Alert color="blue" role="status">{t("messages.waiting_for_reply")}</Alert>}
      <ScrollArea.Autosize mah="60vh">
        <Stack gap="xs" aria-live="polite">
          {more && oldest && <Button variant="subtle" size="xs" onClick={async () => {
            const r = await api<Message[]>(`/api/v1/conversations/${id}/messages`, { query: { before_seq: oldest, limit: 30 } });
            useChat.getState().merge(id, r.data);
            setOldest(r.data[0]?.seq ?? oldest);
            setMore(r.data.length === 30);
          }}>{t("messages.older")}</Button>}
          {messages.map((m) => {
            const mine = m.sender_id === me?.id;
            return (
              <Group key={m.id} justify={mine ? "flex-end" : "flex-start"}>
                <Paper p="xs" withBorder maw="75%" bg={mine ? "var(--mantine-color-brand-light)" : undefined}>
                  {m.deleted ? <Text fs="italic" c="dimmed" size="sm">{t("messages.deleted")}</Text> : <Text style={{ whiteSpace: "pre-wrap", overflowWrap: "anywhere" }}>{m.body}</Text>}
                  <Group gap={6} justify="flex-end">
                    <Text size="xs" c="dimmed">{formatDate(m.created_at, locale, { timeStyle: "short" })}</Text>
                    {mine && m.seq > 0 && m.seq <= peerRead && <Text size="xs" c="dimmed">✓✓</Text>}
                    {(m as { pending?: boolean }).pending && <Text size="xs" c="dimmed">{t("messages.pending")}</Text>}
                    {(m as { failed?: boolean }).failed && <Button size="compact-xs" color="red" variant="subtle" onClick={() => { useChat.getState().merge(id, []); void send(m.client_id, m.body); }}>{t("messages.retry")}</Button>}
                    {!mine && !m.deleted && <ReportDialog targetType="message" targetId={m.id} />}
                  </Group>
                </Paper>
              </Group>
            );
          })}
          <div ref={bottom} />
        </Stack>
      </ScrollArea.Autosize>
      {declined ? <Alert color="red">{t("error.conversation_declined")}</Alert> : (
        <Stack gap="xs">
          {locked && <Alert color="gray" role="status">{t("error.request_pending")}</Alert>}
          <Textarea value={text} onChange={(e) => setText(e.currentTarget.value)} placeholder={t("messages.placeholder")} aria-label={t("messages.placeholder")} autosize minRows={1} maxRows={6} maxLength={4000} disabled={locked}
            onKeyDown={(e) => { if (e.key === "Enter" && !e.shiftKey) { e.preventDefault(); submit(); } }} />
          <Group><Button onClick={submit} disabled={locked || !text.trim()}>{t("messages.send")}</Button><Text size="xs" c="dimmed">{lastSeq > 0 ? `#${lastSeq}` : ""}</Text></Group>
        </Stack>
      )}
    </Stack>
  );
}

"use client";
import { Anchor, Avatar, Button, Group, Paper, Stack, Text, Textarea, Title } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import Link from "next/link";
import { Fragment, useState } from "react";
import type { Author, Comment } from "@/contracts/api-types";
import { ApiError, api } from "@/lib/api";
import { formatDate } from "@/lib/i18n";
import { hasRole, useLocale, useSession, useT } from "./Providers";
import { ReportDialog } from "./ReportDialog";
import { ErrorAlert, LoadMore, errorText, fieldMessages } from "./States";
import { usePaged } from "./usePaged";

const MENTION = /(^|[^A-Za-z0-9_-])@([a-z0-9]+(?:-[a-z0-9]+)*)/g;

/** Plain text with clickable @slug mentions (never HTML). */
export function MentionText({ text, locale }: { text: string; locale: string }) {
  const parts: React.ReactNode[] = [];
  let last = 0;
  for (const m of text.matchAll(MENTION)) {
    const start = (m.index ?? 0) + m[1].length;
    parts.push(text.slice(last, start));
    parts.push(
      <Anchor key={start} component={Link} href={`/${locale}/people/${m[2]}`}>@{m[2]}</Anchor>,
    );
    last = start + 1 + m[2].length;
  }
  parts.push(text.slice(last));
  return <Text style={{ whiteSpace: "pre-wrap", overflowWrap: "anywhere" }}>{parts.map((p, i) => <Fragment key={i}>{p}</Fragment>)}</Text>;
}

/** Avatar + nickname + @slug; both link to the profile. A deleted author has no link. */
export function AuthorChip({ author, locale, size = "sm" }: { author: Author; locale: string; size?: "sm" | "md" }) {
  const t = useT();
  if (!author.slug) return <Text size={size} c="dimmed">{author.nickname || t("author.deleted")}</Text>;
  const href = `/${locale}/people/${author.slug}`;
  return (
    <Group gap={6} wrap="nowrap">
      <Avatar size={size === "sm" ? 22 : 30} src={author.avatar_url ?? undefined} alt="" radius="xl" />
      <Anchor component={Link} href={href} fw={600} size={size}>{author.nickname}</Anchor>
      <Anchor component={Link} href={href} c="dimmed" size={size}>@{author.slug}</Anchor>
    </Group>
  );
}

function CommentForm({ targetType, targetId, parentId, onPosted }: { targetType: "article" | "idea"; targetId: string; parentId?: string; onPosted: (c: Comment) => void }) {
  const t = useT();
  const [body, setBody] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<ApiError | null>(null);
  return (
    <form
      onSubmit={async (e) => {
        e.preventDefault();
        setBusy(true);
        setErr(null);
        try {
          const r = await api<Comment>("/api/v1/comments", { method: "POST", body: { target_type: targetType, target_id: targetId, parent_id: parentId, body } });
          setBody("");
          onPosted(r.data);
        } catch (e2) {
          setErr(e2 as ApiError);
        } finally {
          setBusy(false);
        }
      }}
    >
      <Stack gap="xs">
        <Textarea value={body} onChange={(e) => setBody(e.currentTarget.value)} minRows={2} autosize maxLength={2000} aria-label={t("comments.placeholder")} placeholder={t("comments.placeholder")}
          error={err ? fieldMessages(t, err).body ?? errorText(t, err) : undefined} />
        <Group><Button type="submit" size="xs" loading={busy} disabled={body.trim().length === 0}>{parentId ? t("comments.reply") : t("comments.post")}</Button></Group>
      </Stack>
    </form>
  );
}

function CommentItem({ c, targetType, targetId, canReply, onRemoved }: { c: Comment; targetType: "article" | "idea"; targetId: string; canReply: boolean; onRemoved: (id: string, status: string) => void }) {
  const t = useT();
  const locale = useLocale();
  const { me } = useSession();
  const [showReplies, setShowReplies] = useState(false);
  const [replying, setReplying] = useState(false);
  const [extra, setExtra] = useState<Comment[]>([]);
  const replies = usePaged<Comment>(`/api/v1/comments/${c.id}/replies`, { limit: 20 }, showReplies ? "on" : "off");
  const own = me && c.author.user_id === me.id;
  const mod = hasRole(me, "moderator");
  const placeholder = c.status !== "visible";
  return (
    <Paper withBorder p="sm" id={`comment-${c.id}`}>
      <Stack gap={6}>
        <Group justify="space-between">
          <AuthorChip author={c.author} locale={locale} />
          <Text size="xs" c="dimmed"><time dateTime={c.created_at}>{formatDate(c.created_at, locale, { dateStyle: "medium", timeStyle: "short" })}</time></Text>
        </Group>
        {placeholder ? <Text c="dimmed" fs="italic">{t(c.status === "hidden" ? "comments.hidden_placeholder" : "comments.deleted_placeholder")}</Text> : <MentionText text={c.body} locale={locale} />}
        {!placeholder && (
          <Group gap="xs">
            {canReply && me && !c.parent_id && <Button size="compact-xs" variant="subtle" onClick={() => setReplying((v) => !v)}>{t("comments.reply")}</Button>}
            {c.reply_count > 0 && !c.parent_id && <Button size="compact-xs" variant="subtle" onClick={() => setShowReplies((v) => !v)}>{t("comments.replies", { count: c.reply_count })}</Button>}
            {me && !own && <ReportDialog targetType="comment" targetId={c.id} />}
            {(own || mod) && (
              <Button size="compact-xs" variant="subtle" color="red" onClick={async () => {
                try { await api(`/api/v1/comments/${c.id}`, { method: "DELETE" }); onRemoved(c.id, "deleted"); } catch (e) { notifications.show({ color: "red", message: errorText(t, e as ApiError) }); }
              }}>{t("common.delete")}</Button>
            )}
            {mod && !own && (
              <Button size="compact-xs" variant="subtle" color="orange" onClick={async () => {
                try { await api(`/api/v1/mod/comments/${c.id}/hide`, { method: "POST" }); onRemoved(c.id, "hidden"); } catch (e) { notifications.show({ color: "red", message: errorText(t, e as ApiError) }); }
              }}>{t("comments.hide")}</Button>
            )}
          </Group>
        )}
        {replying && <CommentForm targetType={targetType} targetId={targetId} parentId={c.id} onPosted={(n) => { setExtra((x) => [...x, n]); setReplying(false); setShowReplies(true); }} />}
        {showReplies && (
          <Stack gap="xs" ml="md">
            {[...replies.items, ...extra.filter((e) => !replies.items.some((r) => r.id === e.id))].map((r) => (
              <CommentItem key={r.id} c={r} targetType={targetType} targetId={targetId} canReply={false} onRemoved={(id, st) => { replies.setItems((l) => l.map((x) => (x.id === id ? { ...x, status: st as Comment["status"], body: "" } : x))); setExtra((l) => l.map((x) => (x.id === id ? { ...x, status: st as Comment["status"], body: "" } : x))); }} />
            ))}
            <LoadMore next={replies.next} loading={replies.loading} onMore={() => void replies.more()} />
          </Stack>
        )}
      </Stack>
    </Paper>
  );
}

export function CommentThread({ targetType, targetId }: { targetType: "article" | "idea"; targetId: string }) {
  const t = useT();
  const locale = useLocale();
  const { me, loading: sessionLoading } = useSession();
  const { items, next, loading, error, more, reload, setItems } = usePaged<Comment>("/api/v1/comments", { target_type: targetType, target_id: targetId, limit: 20 }, targetId);
  return (
    <Stack component="section" aria-label={t("comments.title")} mt="xl">
      <Title order={2} fz="h3">{t("comments.title")}</Title>
      {!sessionLoading && (me && me.status === "active"
        ? <CommentForm targetType={targetType} targetId={targetId} onPosted={(c) => setItems((l) => [...l, c])} />
        : <Text c="dimmed" size="sm">{!me ? <Anchor component={Link} href={`/${locale}/login`}>{t("comments.sign_in")}</Anchor> : null}</Text>)}
      {error && <ErrorAlert error={error} onRetry={reload} />}
      {!loading && !error && items.length === 0 && <Text c="dimmed">{t("comments.empty")}</Text>}
      {items.map((c) => <CommentItem key={c.id} c={c} targetType={targetType} targetId={targetId} canReply onRemoved={(id, st) => setItems((l) => l.map((x) => (x.id === id ? { ...x, status: st as Comment["status"], body: "" } : x)))} />)}
      <LoadMore next={next} loading={loading} onMore={() => void more()} />
    </Stack>
  );
}

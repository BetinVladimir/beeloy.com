"use client";
import { Badge, Button, Group, Paper, Select, Stack, Text, Textarea, Timeline, Title } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import Link from "next/link";
import { useState } from "react";
import type { HistoryItem, Idea, IdeaStatus } from "@/contracts/api-types";
import { IDEA_TRANSITIONS, NOTE_REQUIRED } from "@/contracts/idea-transitions";
import { ApiError, api } from "@/lib/api";
import { formatDate } from "@/lib/i18n";
import { AuthorChip } from "./CommentThread";
import { useLocale, useSession, useT, hasRole } from "./Providers";
import { errorText } from "./States";

const COLORS: Record<IdeaStatus, string> = { submitted: "gray", under_review: "blue", needs_info: "orange", planned: "cyan", in_progress: "indigo", delivered: "green", declined: "red", duplicate: "gray" };

export function StatusBadge({ status }: { status: IdeaStatus }) {
  const t = useT();
  return <Badge color={COLORS[status]} variant="light">{t(`ideas.status.${status}`)}</Badge>;
}

export function VoteButton({ idea, onChange }: { idea: Idea; onChange: (voted: boolean, count: number) => void }) {
  const t = useT();
  const locale = useLocale();
  const { me } = useSession();
  const [busy, setBusy] = useState(false);
  const own = !!me && idea.author.user_id === me.id;
  const closed = ["delivered", "declined", "duplicate"].includes(idea.status);
  const label = idea.viewer_voted ? t("ideas.unvote") : t("ideas.vote");
  if (!me) {
    return <Button component={Link} href={`/${locale}/login`} size="xs" variant="default">{t("ideas.vote")} · {idea.votes_count}</Button>;
  }
  return (
    <Button
      size="xs"
      variant={idea.viewer_voted ? "filled" : "default"}
      disabled={own || closed || busy}
      aria-pressed={idea.viewer_voted}
      onClick={async () => {
        const next = !idea.viewer_voted;
        onChange(next, idea.votes_count + (next ? 1 : -1)); // optimistic
        setBusy(true);
        try {
          await api(`/api/v1/ideas/${idea.id}/vote`, { method: next ? "PUT" : "DELETE" });
        } catch (e) {
          onChange(idea.viewer_voted, idea.votes_count); // roll back
          notifications.show({ color: "red", message: errorText(t, e as ApiError) });
        } finally {
          setBusy(false);
        }
      }}
    >
      {label} · {t("ideas.votes", { count: idea.votes_count })}
    </Button>
  );
}

export function IdeaCard({ idea, onChange }: { idea: Idea; onChange?: (i: Idea) => void }) {
  const t = useT();
  const locale = useLocale();
  return (
    <Paper withBorder p="md" component="article">
      <Stack gap="xs">
        <Group justify="space-between">
          <StatusBadge status={idea.status} />
          <Text size="xs" c="dimmed">{t(`ideas.category.${idea.category}`)} · <time dateTime={idea.created_at}>{formatDate(idea.created_at, locale)}</time></Text>
        </Group>
        <Title order={3} fz="lg"><Link href={`/${locale}/ideas/${idea.slug}`} style={{ color: "inherit", textDecoration: "none" }} lang={idea.lang}>{idea.title}</Link></Title>
        <Text size="sm" lineClamp={3} lang={idea.lang}>{idea.body}</Text>
        <Group justify="space-between">
          <AuthorChip author={idea.author} locale={locale} />
          {onChange && <VoteButton idea={idea} onChange={(v, c) => onChange({ ...idea, viewer_voted: v, votes_count: c })} />}
        </Group>
      </Stack>
    </Paper>
  );
}

export function StatusTimeline({ history }: { history: HistoryItem[] }) {
  const t = useT();
  const locale = useLocale();
  return (
    <Timeline active={history.length} bulletSize={14} lineWidth={2}>
      {history.map((h, i) => (
        <Timeline.Item key={i} title={t(`ideas.status.${h.to}`)}>
          {h.note && <Text size="sm">{h.note}</Text>}
          <Text size="xs" c="dimmed">{formatDate(h.at, locale, { dateStyle: "medium", timeStyle: "short" })}</Text>
        </Timeline.Item>
      ))}
    </Timeline>
  );
}

/** Moderator tools: only the transitions the server will accept are offered (the server still enforces them). */
export function ModerationActions({ idea, onUpdated }: { idea: Idea; onUpdated: (i: Idea) => void }) {
  const t = useT();
  const { me } = useSession();
  const [to, setTo] = useState<string | null>(null);
  const [note, setNote] = useState("");
  const [dup, setDup] = useState("");
  const [reply, setReply] = useState(idea.team_reply ?? "");
  const [busy, setBusy] = useState(false);
  if (!hasRole(me, "moderator")) return null;
  const options = IDEA_TRANSITIONS[idea.status];
  const fail = (e: unknown) => notifications.show({ color: "red", message: errorText(t, e as ApiError) });
  return (
    <Paper withBorder p="md">
      <Stack>
        <Title order={3} fz="md">{t("ideas.moderation")}</Title>
        {options.length === 0 ? <Text size="sm" c="dimmed">{t("ideas.no_transitions")}</Text> : (
          <>
            <Select label={t("ideas.set_status")} data={options.map((s) => ({ value: s, label: t(`ideas.status.${s}`) }))} value={to} onChange={setTo} />
            {to && NOTE_REQUIRED.includes(to as IdeaStatus) && <Textarea label={t("ideas.note")} value={note} onChange={(e) => setNote(e.currentTarget.value)} />}
            {to === "duplicate" && <Textarea label={t("ideas.duplicate_of_id")} value={dup} onChange={(e) => setDup(e.currentTarget.value)} autosize minRows={1} />}
            <Group><Button disabled={!to} loading={busy} onClick={async () => {
              setBusy(true);
              try { const r = await api<Idea>(`/api/v1/ideas/${idea.id}/status`, { method: "POST", body: { to, note, duplicate_of_id: dup || undefined } }); onUpdated(r.data); setTo(null); setNote(""); } catch (e) { fail(e); } finally { setBusy(false); }
            }}>{t("common.save")}</Button></Group>
          </>
        )}
        <Textarea label={t("ideas.team_reply")} value={reply} onChange={(e) => setReply(e.currentTarget.value)} autosize minRows={2} />
        <Group><Button variant="default" disabled={reply.trim() === ""} onClick={async () => {
          try { const r = await api<Idea>(`/api/v1/ideas/${idea.id}/reply`, { method: "PUT", body: { text: reply } }); onUpdated(r.data); } catch (e) { fail(e); }
        }}>{t("ideas.save_reply")}</Button></Group>
      </Stack>
    </Paper>
  );
}

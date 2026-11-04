"use client";
import { Alert, Anchor, Paper, Stack, Text, Title } from "@mantine/core";
import Link from "next/link";
import { useState } from "react";
import type { HistoryItem, Idea } from "@/contracts/api-types";
import { AuthorChip, CommentThread } from "./CommentThread";
import { ModerationActions, StatusBadge, StatusTimeline, VoteButton } from "./IdeaParts";
import { useLocale, useT } from "./Providers";
import { ReportDialog } from "./ReportDialog";
import { useSession } from "./Providers";

export function IdeaDetail({ initial, history }: { initial: Idea; history: HistoryItem[] }) {
  const t = useT();
  const locale = useLocale();
  const { me } = useSession();
  const [idea, setIdea] = useState(initial);
  return (
    <Stack>
      <StatusBadge status={idea.status} />
      <Title order={1} lang={idea.lang}>{idea.title}</Title>
      <AuthorChip author={idea.author} locale={locale} size="md" />
      <Text style={{ whiteSpace: "pre-wrap" }} lang={idea.lang}>{idea.body}</Text>
      {idea.duplicate_of_slug && <Alert color="gray">{t("ideas.duplicate_of")} <Anchor component={Link} href={`/${locale}/ideas/${idea.duplicate_of_slug}`}>{idea.duplicate_of_slug}</Anchor></Alert>}
      {idea.team_reply && (
        <Paper withBorder p="md" bg="var(--mantine-color-brand-light)">
          <Text fw={700} size="sm">{t("ideas.team_reply")}</Text>
          <Text style={{ whiteSpace: "pre-wrap" }}>{idea.team_reply}</Text>
        </Paper>
      )}
      <VoteButton idea={idea} onChange={(v, c) => setIdea({ ...idea, viewer_voted: v, votes_count: c })} />
      {me && idea.author.user_id !== me.id && <ReportDialog targetType="idea" targetId={idea.id} />}
      <ModerationActions idea={idea} onUpdated={setIdea} />
      <Title order={2} fz="h3">{t("ideas.history")}</Title>
      <StatusTimeline history={history} />
      <CommentThread targetType="idea" targetId={idea.id} />
    </Stack>
  );
}

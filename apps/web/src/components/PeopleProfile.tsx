"use client";
import { Avatar, Button, Group, Stack, Tabs, Text, Title } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import Link from "next/link";
import { useRouter } from "next/navigation";
import type { Comment, Idea, PublicProfile } from "@/contracts/api-types";
import { ApiError, api } from "@/lib/api";
import { formatDate } from "@/lib/i18n";
import { MentionText } from "./CommentThread";
import { IdeaCard } from "./IdeaParts";
import { useLocale, useSession, useT } from "./Providers";
import { ReportDialog } from "./ReportDialog";
import { ErrorAlert, ListSkeleton, LoadMore, errorText } from "./States";
import { usePaged } from "./usePaged";

interface ProfileComment extends Comment { target_slug: string }

function IdeasTab({ slug }: { slug: string }) {
  const t = useT();
  const { items, next, loading, error, more, reload } = usePaged<Idea>(`/api/v1/users/${slug}/ideas`, { limit: 10 }, slug);
  return (
    <Stack>
      {error && <ErrorAlert error={error} onRetry={reload} />}
      {loading && items.length === 0 && <ListSkeleton />}
      {!loading && !error && items.length === 0 && <Text c="dimmed">{t("people.empty_ideas")}</Text>}
      {items.map((i) => <IdeaCard key={i.id} idea={i} />)}
      <LoadMore next={next} loading={loading} onMore={() => void more()} />
    </Stack>
  );
}

function CommentsTab({ slug }: { slug: string }) {
  const t = useT();
  const locale = useLocale();
  const { items, next, loading, error, more, reload } = usePaged<ProfileComment>(`/api/v1/users/${slug}/comments`, { limit: 10 }, slug);
  return (
    <Stack>
      {error && <ErrorAlert error={error} onRetry={reload} />}
      {loading && items.length === 0 && <ListSkeleton />}
      {!loading && !error && items.length === 0 && <Text c="dimmed">{t("people.empty_comments")}</Text>}
      {items.map((c) => (
        <Stack key={c.id} gap={2}>
          <MentionText text={c.body} locale={locale} />
          <Text size="xs" c="dimmed">
            <Link href={`/${locale}/${c.target_type === "idea" ? "ideas" : "news"}/${c.target_slug}#comment-${c.id}`}>{formatDate(c.created_at, locale)}</Link>
          </Text>
        </Stack>
      ))}
      <LoadMore next={next} loading={loading} onMore={() => void more()} />
    </Stack>
  );
}

export function PeopleProfile({ profile }: { profile: PublicProfile }) {
  const t = useT();
  const locale = useLocale();
  const router = useRouter();
  const { me } = useSession();
  const own = me?.id === profile.id;
  return (
    <Stack>
      <Group align="flex-start" wrap="nowrap">
        <Avatar size={72} src={profile.avatar_url ?? undefined} alt="" radius="xl" />
        <Stack gap={2}>
          <Title order={1}>{profile.nickname}</Title>
          <Text c="dimmed">@{profile.slug}</Text>
          <Text size="sm" c="dimmed">{t("people.member")} · {t("people.joined", { date: formatDate(profile.created_at, locale) })}</Text>
        </Stack>
      </Group>
      {profile.bio && <Text style={{ whiteSpace: "pre-wrap" }}>{profile.bio}</Text>}
      {!own && (
        <Group>
          <Button onClick={async () => {
            if (!me) { router.push(`/${locale}/login`); return; }
            try {
              const r = await api<{ id: string }>("/api/v1/conversations", { method: "POST", body: { user_id: profile.id } });
              router.push(`/${locale}/messages/${r.data.id}`);
            } catch (e) { notifications.show({ color: "red", message: errorText(t, e as ApiError) }); }
          }}>{t("people.message")}</Button>
          {me && <ReportDialog targetType="user" targetId={profile.id} />}
        </Group>
      )}
      {profile.activity_visible ? (
        <Tabs defaultValue="ideas">
          <Tabs.List>
            <Tabs.Tab value="ideas">{t("people.tab_ideas")}</Tabs.Tab>
            <Tabs.Tab value="comments">{t("people.tab_comments")}</Tabs.Tab>
          </Tabs.List>
          <Tabs.Panel value="ideas" pt="md"><IdeasTab slug={profile.canonical_slug} /></Tabs.Panel>
          <Tabs.Panel value="comments" pt="md"><CommentsTab slug={profile.canonical_slug} /></Tabs.Panel>
        </Tabs>
      ) : <Text c="dimmed">{t("people.activity_hidden")}</Text>}
    </Stack>
  );
}

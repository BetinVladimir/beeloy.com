"use client";
import { Alert, Badge, Button, Group, Modal, Paper, Radio, Select, Stack, Table, Text, TextInput, Textarea, Title } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { useEffect, useState } from "react";
import type { AdminUser, Category, EditorArticle as EA, EditorListItem, PersonHit, Report, RevisionRow } from "@/contracts/api-types";
import { ApiError, api } from "@/lib/api";
import { LOCALE_LIST } from "@/lib/locales";
import { Markdown } from "./Markdown";
import { hasRole, useLocale, useRequireAuth, useSession, useT } from "./Providers";
import { ErrorAlert, ListSkeleton, LoadMore, Spinner, errorText, fieldMessages } from "./States";
import { usePaged } from "./usePaged";

const fail = (t: (k: string) => string, e: unknown) => notifications.show({ color: "red", message: errorText(t, e as ApiError) });

function Guard({ role, children }: { role: "editor" | "moderator" | "admin"; children: React.ReactNode }) {
  const t = useT();
  const { me, ready } = useRequireAuth();
  if (!ready) return <Spinner />;
  if (!hasRole(me, role)) return <Alert color="red" role="alert">{t("error.forbidden")}</Alert>;
  return <>{children}</>;
}

function ArticleTable({ path, base }: { path: string; base: string }) {
  const t = useT();
  const locale = useLocale();
  const { items, next, loading, error, more, reload } = usePaged<EditorListItem>(path, { limit: 20 }, path);
  return (
    <Stack>
      {error && <ErrorAlert error={error} onRetry={reload} />}
      {loading && items.length === 0 && <ListSkeleton />}
      {!loading && !error && items.length === 0 && <Text c="dimmed">{t("common.empty")}</Text>}
      {items.length > 0 && (
        <Table.ScrollContainer minWidth={640}>
          <Table>
            <Table.Thead><Table.Tr><Table.Th>{t("editor.field.title")}</Table.Th><Table.Th>{t("editor.status")}</Table.Th><Table.Th>{t("editor.rev")}</Table.Th><Table.Th>{t("editor.source_locale")}</Table.Th></Table.Tr></Table.Thead>
            <Table.Tbody>
              {items.map((a) => (
                <Table.Tr key={a.id}>
                  <Table.Td><Link href={`/${locale}/${base}/${a.id}`}>{a.title || a.slug}</Link></Table.Td>
                  <Table.Td><Badge variant="light">{t(`editor.status.${a.status}`)}</Badge>{a.pending && a.status === "published" && <Badge ml={4} color="orange">{t("mod.pending_changes")}</Badge>}</Table.Td>
                  <Table.Td>{a.published_rev}/{a.rev}</Table.Td>
                  <Table.Td>{a.source_locale}</Table.Td>
                </Table.Tr>
              ))}
            </Table.Tbody>
          </Table>
        </Table.ScrollContainer>
      )}
      <LoadMore next={next} loading={loading} onMore={() => void more()} />
    </Stack>
  );
}

export function EditorPage() {
  const t = useT();
  const locale = useLocale();
  const router = useRouter();
  const [open, setOpen] = useState(false);
  const [cats, setCats] = useState<Category[]>([]);
  const [category, setCategory] = useState<string | null>(null);
  const [sourceLocale, setSourceLocale] = useState<string | null>("bg");
  const [title, setTitle] = useState("");
  const [err, setErr] = useState<ApiError | null>(null);
  useEffect(() => { api<Category[]>("/api/v1/categories").then((r) => setCats(r.data)).catch(() => {}); }, []);
  const fe = fieldMessages(t, err);
  return (
    <Guard role="editor">
      <Stack>
        <Group justify="space-between"><Title order={1}>{t("editor.title")}</Title><Button onClick={() => setOpen(true)}>{t("editor.new")}</Button></Group>
        <ArticleTable path="/api/v1/editor/articles" base="editor" />
        <Modal opened={open} onClose={() => setOpen(false)} title={t("editor.new")}>
          <Stack>
            <Select label={t("editor.category")} data={cats.map((c) => ({ value: c.id, label: c.names[locale] ?? c.names.en }))} value={category} onChange={setCategory} error={fe.category_id} />
            <Select label={t("editor.source_locale")} data={LOCALE_LIST.map((l) => ({ value: l.code, label: l.name }))} value={sourceLocale} onChange={setSourceLocale} searchable error={fe.source_locale} />
            <TextInput label={t("editor.title_for_slug")} value={title} onChange={(e) => setTitle(e.currentTarget.value)} error={fe.title_for_slug} />
            <Button onClick={async () => {
              setErr(null);
              try { const r = await api<{ id: string }>("/api/v1/editor/articles", { method: "POST", body: { category_id: category, source_locale: sourceLocale, title_for_slug: title } }); router.push(`/${locale}/editor/${r.data.id}`); } catch (e) { setErr(e as ApiError); }
            }}>{t("common.save")}</Button>
          </Stack>
        </Modal>
      </Stack>
    </Guard>
  );
}

const STATUS_LABEL = (r?: RevisionRow) => (!r ? "missing" : r.stale ? "stale" : r.origin);

export function EditorArticle({ id }: { id: string }) {
  const t = useT();
  const [data, setData] = useState<EA | null>(null);
  const [error, setError] = useState<ApiError | null>(null);
  const [title, setTitle] = useState("");
  const [summary, setSummary] = useState("");
  const [body, setBody] = useState("");
  const [preview, setPreview] = useState(false);
  const [tr, setTr] = useState<{ locale: string; title: string; summary: string; body: string } | null>(null);
  const [fieldErr, setFieldErr] = useState<ApiError | null>(null);
  const load = async () => {
    try {
      const r = await api<EA>(`/api/v1/editor/articles/${id}`);
      setData(r.data);
      if (r.data.source) { setTitle(r.data.source.title); setSummary(r.data.source.summary); setBody(r.data.source.body_md); }
    } catch (e) { setError(e as ApiError); }
  };
  useEffect(() => { void load(); }, [id]); // eslint-disable-line react-hooks/exhaustive-deps
  if (error) return <ErrorAlert error={error} />;
  if (!data) return <Spinner />;
  const a = data.article;
  const latest = (loc: string) => data.revisions.find((r) => r.locale === loc);
  const fe = fieldMessages(t, fieldErr);
  return (
    <Guard role="editor">
      <Stack>
        <Group justify="space-between">
          <Title order={1}>{title || a.slug}</Title>
          <Badge size="lg" variant="light">{t(`editor.status.${a.status}`)} · {a.published_rev}/{a.rev}</Badge>
        </Group>
        <Textarea label={t("editor.field.title")} value={title} onChange={(e) => setTitle(e.currentTarget.value)} autosize minRows={1} error={fe.title} />
        <Textarea label={t("editor.field.summary")} value={summary} onChange={(e) => setSummary(e.currentTarget.value)} autosize minRows={2} error={fe.summary} />
        <Textarea label={t("editor.field.body")} value={body} onChange={(e) => setBody(e.currentTarget.value)} autosize minRows={10} error={fe.body_md} />
        <Group>
          <Button variant="default" onClick={() => setPreview((v) => !v)}>{t("editor.preview")}</Button>
          <Button onClick={async () => {
            setFieldErr(null);
            try { await api(`/api/v1/editor/articles/${id}/source`, { method: "PUT", body: { title, summary, body_md: body } }); notifications.show({ message: t("editor.saved") }); await load(); } catch (e) { setFieldErr(e as ApiError); fail(t, e); }
          }}>{t("common.save")}</Button>
          <Button color="green" disabled={a.status !== "draft" || a.rev < 1} onClick={async () => {
            try { await api(`/api/v1/editor/articles/${id}/submit`, { method: "POST" }); await load(); } catch (e) { fail(t, e); }
          }}>{t("editor.submit")}</Button>
        </Group>
        {preview && <Paper withBorder p="md"><Markdown>{body}</Markdown></Paper>}
        <Title order={2} fz="h3">{t("editor.translations")}</Title>
        <Text size="sm" c="dimmed">{t("editor.translations.cli_hint")}</Text>
        <Table.ScrollContainer minWidth={420}>
          <Table>
            <Table.Tbody>
              {LOCALE_LIST.filter((l) => l.code !== a.source_locale).map((l) => {
                const r = latest(l.code);
                return (
                  <Table.Tr key={l.code}>
                    <Table.Td>{l.name} ({l.code})</Table.Td>
                    <Table.Td><Badge color={r ? (r.stale ? "orange" : r.origin === "machine" ? "brand" : "green") : "gray"} variant="light">{t(`editor.locale_status.${STATUS_LABEL(r)}`)}</Badge></Table.Td>
                    <Table.Td><Button size="compact-xs" variant="subtle" disabled={a.rev < 1} onClick={() => setTr({ locale: l.code, title: "", summary: "", body: "" })}>{t("editor.translate_by_hand")}</Button></Table.Td>
                  </Table.Tr>
                );
              })}
            </Table.Tbody>
          </Table>
        </Table.ScrollContainer>
        <Modal opened={!!tr} onClose={() => setTr(null)} title={tr?.locale} size="lg">
          {tr && (
            <Stack>
              <TextInput label={t("editor.field.title")} value={tr.title} onChange={(e) => setTr({ ...tr, title: e.currentTarget.value })} />
              <Textarea label={t("editor.field.summary")} value={tr.summary} onChange={(e) => setTr({ ...tr, summary: e.currentTarget.value })} autosize minRows={2} />
              <Textarea label={t("editor.field.body")} value={tr.body} onChange={(e) => setTr({ ...tr, body: e.currentTarget.value })} autosize minRows={8} />
              <Button onClick={async () => {
                try { await api(`/api/v1/editor/articles/${id}/translations/${tr.locale}`, { method: "PUT", body: { title: tr.title, summary: tr.summary, body_md: tr.body } }); setTr(null); await load(); } catch (e) { fail(t, e); }
              }}>{t("common.save")}</Button>
            </Stack>
          )}
        </Modal>
      </Stack>
    </Guard>
  );
}

export function ModArticleQueue() {
  const t = useT();
  return (
    <Guard role="moderator">
      <Stack>
        <Title order={1}>{t("mod.articles_title")}</Title>
        <ArticleTable path="/api/v1/mod/articles" base="mod/articles" />
      </Stack>
    </Guard>
  );
}

export function ModArticleReview({ id }: { id: string }) {
  const t = useT();
  const [data, setData] = useState<EA | null>(null);
  const [error, setError] = useState<ApiError | null>(null);
  const [rev, setRev] = useState<string | null>(null);
  const [view, setView] = useState<{ locale: string; title: string; body_md: string; origin: string } | null>(null);
  const [vis, setVis] = useState("public");
  const [query, setQuery] = useState("");
  const [people, setPeople] = useState<PersonHit[]>([]);
  const [viewers, setViewers] = useState<PersonHit[]>([]);
  const [note, setNote] = useState("");
  const load = async () => {
    try { const r = await api<EA>(`/api/v1/editor/articles/${id}`); setData(r.data); setRev(String(r.data.article.rev)); setVis(r.data.article.visibility); } catch (e) { setError(e as ApiError); }
  };
  useEffect(() => { void load(); }, [id]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    if (query.trim().length < 2) { setPeople([]); return; }
    const h = setTimeout(() => api<PersonHit[]>("/api/v1/search/people", { query: { q: query, limit: 5 } }).then((r) => setPeople(r.data)).catch(() => {}), 300);
    return () => clearTimeout(h);
  }, [query]);
  if (error) return <ErrorAlert error={error} />;
  if (!data) return <Spinner />;
  const a = data.article;
  const revs = [...new Set(data.revisions.map((r) => r.rev))].sort((x, y) => y - x);
  const locs = data.revisions.filter((r) => String(r.rev) === rev);
  return (
    <Guard role="moderator">
      <Stack>
        <Group justify="space-between"><Title order={1}>{a.slug}</Title><Badge size="lg" variant="light">{t(`editor.status.${a.status}`)} · {a.published_rev}/{a.rev}</Badge></Group>
        <Select label={t("editor.rev")} data={revs.map((r) => ({ value: String(r), label: `#${r}` }))} value={rev} onChange={setRev} allowDeselect={false} w={160} />
        <Group gap="xs">
          {locs.map((r) => (
            <Button key={r.locale} size="compact-sm" variant={view?.locale === r.locale ? "filled" : "default"} onClick={async () => {
              const f = await api<{ title: string; body_md: string; origin: string }>(`/api/v1/editor/articles/${id}/revisions/${rev}/${r.locale}`);
              setView({ locale: r.locale, ...f.data });
            }}>{r.locale} · {t(`editor.locale_status.${r.origin}`)}</Button>
          ))}
        </Group>
        {view && <Paper withBorder p="md"><Badge mb="xs">{t(`editor.locale_status.${view.origin}`)}</Badge><Title order={2} fz="h3">{view.title}</Title><Markdown lang={view.locale}>{view.body_md}</Markdown></Paper>}
        <Paper withBorder p="md">
          <Stack>
            <Radio.Group label={t("mod.visibility")} value={vis} onChange={setVis}>
              <Group mt="xs"><Radio value="public" label={t("mod.public")} /><Radio value="restricted" label={t("mod.restricted")} /></Group>
            </Radio.Group>
            {vis === "restricted" && (
              <Stack gap="xs">
                <TextInput label={t("mod.viewers")} value={query} onChange={(e) => setQuery(e.currentTarget.value)} placeholder={t("search.placeholder")} />
                <Group gap="xs">{people.map((p) => <Button key={p.id} size="compact-xs" variant="light" onClick={() => { setViewers((v) => (v.some((x) => x.id === p.id) ? v : [...v, p])); setQuery(""); }}>+ @{p.slug}</Button>)}</Group>
                <Group gap="xs">{viewers.map((p) => <Badge key={p.id} rightSection={<span role="button" aria-label={t("common.delete")} style={{ cursor: "pointer" }} onClick={() => setViewers((v) => v.filter((x) => x.id !== p.id))}>×</span>}>@{p.slug}</Badge>)}
                  {data.viewer_ids.length > 0 && viewers.length === 0 && <Text size="xs" c="dimmed">{t("mod.viewers_current", { count: data.viewer_ids.length })}</Text>}</Group>
              </Stack>
            )}
            <Group>
              <Button disabled={!rev} onClick={async () => {
                try { await api(`/api/v1/mod/articles/${id}/publish`, { method: "POST", body: { rev: Number(rev), visibility: vis, viewer_ids: vis === "restricted" ? viewers.map((v) => v.id) : undefined } }); notifications.show({ message: t("mod.published") }); await load(); } catch (e) { fail(t, e); }
              }}>{t("mod.publish")}</Button>
              <Button variant="default" disabled={a.status !== "published"} onClick={async () => { try { await api(`/api/v1/mod/articles/${id}/unpublish`, { method: "POST" }); await load(); } catch (e) { fail(t, e); } }}>{t("mod.unpublish")}</Button>
            </Group>
            <Textarea label={t("mod.note")} value={note} onChange={(e) => setNote(e.currentTarget.value)} />
            <Group><Button color="red" variant="light" disabled={a.status !== "in_review"} onClick={async () => { try { await api(`/api/v1/mod/articles/${id}/reject`, { method: "POST", body: { note } }); await load(); } catch (e) { fail(t, e); } }}>{t("mod.reject")}</Button></Group>
          </Stack>
        </Paper>
      </Stack>
    </Guard>
  );
}

export function ReportsTable() {
  const t = useT();
  const [status, setStatus] = useState<string | null>("open");
  const { items, next, loading, error, more, reload, setItems } = usePaged<Report>("/api/v1/mod/reports", { status: status ?? "open", limit: 20 }, status ?? "");
  const [open, setOpen] = useState<Report | null>(null);
  const [note, setNote] = useState("");
  return (
    <Guard role="moderator">
      <Stack>
        <Title order={1}>{t("mod.reports_title")}</Title>
        <Select data={["open", "resolved", "dismissed"].map((s) => ({ value: s, label: t(`mod.report_status.${s}`) }))} value={status} onChange={setStatus} allowDeselect={false} w={200} aria-label={t("editor.status")} />
        {error && <ErrorAlert error={error} onRetry={reload} />}
        {loading && items.length === 0 && <ListSkeleton />}
        {!loading && items.length === 0 && <Text c="dimmed">{t("common.empty")}</Text>}
        {items.map((r) => (
          <Paper key={r.id} withBorder p="sm">
            <Group justify="space-between">
              <div><Badge>{r.target_type}</Badge> <Badge color="red" variant="light">{t(`report.reason.${r.reason}`)}</Badge><Text size="sm">{r.details}</Text></div>
              <Button size="xs" variant="default" onClick={async () => { const d = await api<Report>(`/api/v1/mod/reports/${r.id}`); setOpen(d.data); setNote(""); }}>{t("mod.open_report")}</Button>
            </Group>
          </Paper>
        ))}
        <LoadMore next={next} loading={loading} onMore={() => void more()} />
        <Modal opened={!!open} onClose={() => setOpen(null)} title={open?.target_type} size="lg">
          {open && (
            <Stack>
              {open.snapshot && open.snapshot.map((m) => <Paper key={m.seq} withBorder p="xs"><Text size="xs" c="dimmed">#{m.seq}</Text><Text style={{ whiteSpace: "pre-wrap" }}>{m.body}</Text></Paper>)}
              <Textarea label={t("mod.note")} value={note} onChange={(e) => setNote(e.currentTarget.value)} />
              <Group>
                {(["resolved", "dismissed"] as const).map((res) => (
                  <Button key={res} variant={res === "resolved" ? "filled" : "default"} onClick={async () => {
                    try { await api(`/api/v1/mod/reports/${open.id}/resolve`, { method: "POST", body: { resolution: res, note } }); setItems((l) => l.filter((x) => x.id !== open.id)); setOpen(null); } catch (e) { fail(t, e); }
                  }}>{t(res === "resolved" ? "mod.resolve" : "mod.dismiss")}</Button>
                ))}
              </Group>
            </Stack>
          )}
        </Modal>
      </Stack>
    </Guard>
  );
}

export function UsersTable() {
  const t = useT();
  const { me } = useSession();
  const [q, setQ] = useState("");
  const [applied, setApplied] = useState("");
  const { items, next, loading, error, more, reload } = usePaged<AdminUser>("/api/v1/admin/users", { q: applied || undefined, limit: 20 }, applied);
  const [target, setTarget] = useState<AdminUser | null>(null);
  const [role, setRole] = useState<string | null>("moderator");
  const [reason, setReason] = useState("");
  return (
    <Guard role="admin">
      <Stack>
        <Title order={1}>{t("admin.users_title")}</Title>
        <form onSubmit={(e) => { e.preventDefault(); setApplied(q); }}><Group><TextInput value={q} onChange={(e) => setQ(e.currentTarget.value)} placeholder={t("admin.search")} aria-label={t("admin.search")} /><Button type="submit" variant="default">{t("search.submit")}</Button></Group></form>
        {error && <ErrorAlert error={error} onRetry={reload} />}
        {loading && items.length === 0 && <ListSkeleton />}
        <Table.ScrollContainer minWidth={560}>
          <Table>
            <Table.Tbody>
              {items.map((u) => (
                <Table.Tr key={u.id}>
                  <Table.Td>{u.nickname} <Text span c="dimmed">@{u.slug}</Text></Table.Td>
                  <Table.Td><Badge variant="light" color={u.status === "active" ? "green" : "red"}>{u.status}</Badge></Table.Td>
                  <Table.Td>{u.roles.map((r) => <Badge key={r.role + (r.scope_id ?? "")} mr={4}>{r.role}</Badge>)}</Table.Td>
                  <Table.Td><Button size="compact-xs" variant="subtle" disabled={u.id === me?.id} onClick={() => setTarget(u)}>{t("admin.manage")}</Button></Table.Td>
                </Table.Tr>
              ))}
            </Table.Tbody>
          </Table>
        </Table.ScrollContainer>
        <LoadMore next={next} loading={loading} onMore={() => void more()} />
        <Modal opened={!!target} onClose={() => setTarget(null)} title={target ? `@${target.slug}` : ""}>
          {target && (
            <Stack>
              <Select label={t("admin.role")} data={["editor", "moderator", "admin"].map((r) => ({ value: r, label: r }))} value={role} onChange={setRole} allowDeselect={false} />
              <Button onClick={async () => {
                try { await api("/api/v1/admin/role-bindings", { method: "POST", body: { user_id: target.id, role, scope_type: "global" } }); notifications.show({ message: t("common.saved") }); setTarget(null); reload(); } catch (e) { fail(t, e); }
              }}>{t("admin.grant")}</Button>
              <TextInput label={t("mod.reason")} value={reason} onChange={(e) => setReason(e.currentTarget.value)} />
              <Group>
                <Button color="red" variant="light" onClick={async () => { try { await api(`/api/v1/mod/users/${target.id}/block`, { method: "POST", body: { reason } }); setTarget(null); reload(); } catch (e) { fail(t, e); } }}>{t("mod.block_user")}</Button>
                <Button variant="default" onClick={async () => { try { await api(`/api/v1/mod/users/${target.id}/block`, { method: "DELETE" }); setTarget(null); reload(); } catch (e) { fail(t, e); } }}>{t("mod.unblock_user")}</Button>
              </Group>
            </Stack>
          )}
        </Modal>
      </Stack>
    </Guard>
  );
}

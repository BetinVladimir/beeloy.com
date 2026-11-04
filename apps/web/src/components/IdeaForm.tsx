"use client";
import { Button, Select, Stack, Textarea, TextInput } from "@mantine/core";
import { useRouter } from "next/navigation";
import { useState } from "react";
import type { Idea } from "@/contracts/api-types";
import { IDEA_CATEGORIES } from "@/contracts/idea-transitions";
import { ApiError, api } from "@/lib/api";
import { useLocale, useRequireAuth, useT } from "./Providers";
import { Spinner, errorText, fieldMessages } from "./States";

export function IdeaForm() {
  const t = useT();
  const locale = useLocale();
  const router = useRouter();
  const { ready } = useRequireAuth();
  const [title, setTitle] = useState("");
  const [body, setBody] = useState("");
  const [category, setCategory] = useState<string | null>("product");
  const [err, setErr] = useState<ApiError | null>(null);
  const [busy, setBusy] = useState(false);
  if (!ready) return <Spinner />;
  const fe = fieldMessages(t, err);
  return (
    <form onSubmit={async (e) => {
      e.preventDefault();
      setBusy(true);
      setErr(null);
      try {
        const r = await api<Idea>("/api/v1/ideas", { method: "POST", body: { title, body, category, locale } });
        router.push(`/${locale}/ideas/${r.data.slug}`);
      } catch (e2) { setErr(e2 as ApiError); } finally { setBusy(false); }
    }}>
      <Stack maw={720}>
        <TextInput label={t("ideas.field.title")} value={title} onChange={(e) => setTitle(e.currentTarget.value)} maxLength={120} required error={fe.title} />
        <Textarea label={t("ideas.field.body")} value={body} onChange={(e) => setBody(e.currentTarget.value)} minRows={6} autosize maxLength={5000} required error={fe.body} />
        <Select label={t("ideas.field.category")} data={IDEA_CATEGORIES.map((c) => ({ value: c, label: t(`ideas.category.${c}`) }))} value={category} onChange={setCategory} allowDeselect={false} error={fe.category} />
        {err && !err.fields && <Textarea readOnly variant="unstyled" value={errorText(t, err)} aria-live="polite" autosize />}
        <Button type="submit" loading={busy}>{t("ideas.submit")}</Button>
      </Stack>
    </form>
  );
}

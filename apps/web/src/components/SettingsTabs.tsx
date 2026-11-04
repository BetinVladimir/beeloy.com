"use client";
import { Alert, Button, Group, Select, Stack, Switch, Tabs, Text, TextInput, Textarea, Title } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import { useEffect, useState } from "react";
import type { Identity, Me } from "@/contracts/api-types";
import { ApiError, api } from "@/lib/api";
import { LOCALE_LIST } from "@/lib/locales";
import { useLocale, useRequireAuth, useSession, useT } from "./Providers";
import { Spinner, errorText, fieldMessages } from "./States";

const PROVIDERS = ["google", "facebook"] as const;

function LoginMethods() {
  const t = useT();
  const [items, setItems] = useState<Identity[] | null>(null);
  const [err, setErr] = useState<ApiError | null>(null);
  const load = async () => {
    try { setItems((await api<Identity[]>("/api/v1/me/identities")).data); } catch (e) { setErr(e as ApiError); }
  };
  useEffect(() => { void load(); }, []);
  const link = async (p: string) => {
    setErr(null);
    try {
      const r = await api<{ authorization_url: string }>(`/api/v1/me/identities/${p}/link`, { method: "POST", body: {} });
      window.location.assign(r.data.authorization_url);
    } catch (e) { setErr(e as ApiError); }
  };
  const unlink = async (id: string) => {
    setErr(null);
    try { await api(`/api/v1/me/identities/${id}`, { method: "DELETE" }); await load(); } catch (e) { setErr(e as ApiError); }
  };
  if (!items) return <Spinner />;
  const providerLabel = (p: string) => (p === "google" ? t("settings.identities.google") : p === "facebook" ? t("settings.identities.facebook") : t("settings.identities.email_otp"));
  const has = (p: string) => items.some((i) => i.provider === p);
  return (
    <Stack maw={560}>
      <Text size="sm">{t("settings.identities.hint")}</Text>
      {items.map((i) => (
        <Group key={i.id} justify="space-between">
          <Text>{providerLabel(i.provider)}</Text>
          <Button size="xs" variant="default" disabled={items.length < 2} onClick={() => void unlink(i.id)}>{t("settings.identities.unlink")}</Button>
        </Group>
      ))}
      {PROVIDERS.filter((p) => !has(p)).map((p) => (
        <Group key={p}><Button variant="default" onClick={() => void link(p)}>{t("settings.identities.link", { provider: providerLabel(p) })}</Button></Group>
      ))}
      {err && <Alert color="red" role="alert">{err.status === 409 ? t("settings.identities.last") : errorText(t, err)}</Alert>}
    </Stack>
  );
}

function Profile({ me }: { me: Me }) {
  const t = useT();
  const { refresh } = useSession();
  const [nickname, setNickname] = useState(me.nickname);
  const [bio, setBio] = useState(me.bio);
  const [loc, setLoc] = useState<string | null>(me.locale);
  const [pub, setPub] = useState(me.profile_public);
  const [slug, setSlug] = useState(me.slug);
  const [slugState, setSlugState] = useState<"" | "ok" | "taken">("");
  const [err, setErr] = useState<ApiError | null>(null);
  const [slugErr, setSlugErr] = useState<ApiError | null>(null);

  useEffect(() => {
    if (slug === me.slug || slug.length < 3) { setSlugState(""); return; }
    const id = setTimeout(async () => {
      try {
        const r = await api<{ available: boolean }>("/api/v1/slugs/check", { query: { type: "user", slug } });
        setSlugState(r.data.available ? "ok" : "taken");
      } catch { setSlugState(""); }
    }, 400);
    return () => clearTimeout(id);
  }, [slug, me.slug]);

  const fe = fieldMessages(t, err);
  const se = fieldMessages(t, slugErr);
  return (
    <Stack maw={560}>
      <Alert color="blue" variant="light">{t("settings.profile.change_nickname_hint")}</Alert>
      <TextInput label={t("settings.profile.nickname")} value={nickname} onChange={(e) => setNickname(e.currentTarget.value)} maxLength={100} error={fe.nickname} />
      <Textarea label={t("settings.profile.bio")} value={bio} onChange={(e) => setBio(e.currentTarget.value)} maxLength={500} autosize minRows={3} error={fe.bio} />
      <Select label={t("settings.profile.locale")} data={LOCALE_LIST.map((l) => ({ value: l.code, label: l.name }))} value={loc} onChange={setLoc} allowDeselect={false} searchable />
      <Switch checked={pub} onChange={(e) => setPub(e.currentTarget.checked)} label={t("settings.profile.public")} description={t("settings.profile.public_hint")} />
      <Group><Button onClick={async () => {
        setErr(null);
        try { await api("/api/v1/me", { method: "PATCH", body: { nickname, bio, locale: loc, profile_public: pub } }); await refresh(); notifications.show({ message: t("common.saved") }); } catch (e) { setErr(e as ApiError); }
      }}>{t("common.save")}</Button></Group>
      <Title order={3} fz="md" mt="md">{t("settings.profile.slug")}</Title>
      <TextInput value={slug} onChange={(e) => setSlug(e.currentTarget.value.toLowerCase())} leftSection="@" maxLength={32} aria-label={t("settings.profile.slug")}
        description={t("settings.profile.slug_hint")} error={se.slug ?? (slugState === "taken" ? t("field.taken") : slugErr ? errorText(t, slugErr) : undefined)} />
      <Group><Button variant="default" disabled={slug === me.slug || slugState === "taken"} onClick={async () => {
        setSlugErr(null);
        try { await api("/api/v1/me/slug", { method: "PUT", body: { slug } }); await refresh(); notifications.show({ message: t("common.saved") }); } catch (e) { setSlugErr(e as ApiError); }
      }}>{t("settings.profile.slug_change")}</Button></Group>
    </Stack>
  );
}

function Privacy({ me }: { me: Me }) {
  const t = useT();
  const locale = useLocale();
  const { refresh } = useSession();
  const [confirm, setConfirm] = useState("");
  const pending = me.status === "pending_deletion";
  return (
    <Stack maw={560}>
      <Title order={3} fz="md">{t("settings.privacy.export")}</Title>
      <Text size="sm">{t("settings.privacy.export_hint")}</Text>
      <Group><Button variant="default" component="a" href="/api/v1/me/export" download="beeloy-export.json">{t("settings.privacy.export")}</Button></Group>
      <Title order={3} fz="md" mt="md">{t("settings.privacy.delete")}</Title>
      {pending ? (
        <>
          <Alert color="orange" role="alert">{t("settings.privacy.pending")}</Alert>
          <Group><Button onClick={async () => { await api("/api/v1/me/restore", { method: "POST" }); await refresh(); }}>{t("settings.privacy.restore")}</Button></Group>
        </>
      ) : (
        <>
          <Text size="sm">{t("settings.privacy.delete_hint")}</Text>
          <TextInput label={t("settings.privacy.delete_confirm")} value={confirm} onChange={(e) => setConfirm(e.currentTarget.value)} />
          <Group><Button color="red" disabled={confirm !== "DELETE"} onClick={async () => { await api("/api/v1/me", { method: "DELETE" }); await refresh(); }}>{t("settings.privacy.delete")}</Button></Group>
        </>
      )}
      <Text size="xs" c="dimmed">{locale}</Text>
    </Stack>
  );
}

export function SettingsTabs() {
  const t = useT();
  const { me, ready } = useRequireAuth();
  if (!ready || !me) return <Spinner />;
  return (
    <Stack>
      {me.status === "pending_deletion" && <Alert color="orange" role="alert">{t("settings.privacy.pending")}</Alert>}
      <Title order={1}>{t("settings.title")}</Title>
      <Tabs defaultValue={me.status === "pending_deletion" ? "privacy" : "profile"}>
        <Tabs.List>
          <Tabs.Tab value="profile" disabled={me.status !== "active"}>{t("settings.tab.profile")}</Tabs.Tab>
          <Tabs.Tab value="identities" disabled={me.status !== "active"}>{t("settings.tab.identities")}</Tabs.Tab>
          <Tabs.Tab value="privacy">{t("settings.tab.privacy")}</Tabs.Tab>
        </Tabs.List>
        <Tabs.Panel value="profile" pt="md"><Profile me={me} /></Tabs.Panel>
        <Tabs.Panel value="identities" pt="md"><LoginMethods /></Tabs.Panel>
        <Tabs.Panel value="privacy" pt="md"><Privacy me={me} /></Tabs.Panel>
      </Tabs>
    </Stack>
  );
}

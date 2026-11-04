"use client";
import { Alert, Anchor, Button, Checkbox, Collapse, PinInput, Stack, Text, TextInput, Title } from "@mantine/core";
import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { Suspense, useEffect, useState } from "react";
import type { Me } from "@/contracts/api-types";
import { ApiError, api } from "@/lib/api";
import { sanitizeReturnTo } from "@/lib/returnTo";
import { useLocale, useSession, useT } from "./Providers";
import { errorText, fieldMessages } from "./States";

function Form() {
  const t = useT();
  const locale = useLocale();
  const router = useRouter();
  const sp = useSearchParams();
  const { refresh } = useSession();
  const [step, setStep] = useState<"email" | "code">("email");
  const [email, setEmail] = useState("");
  const [code, setCode] = useState("");
  const [accept, setAccept] = useState(false);
  const [nickname, setNickname] = useState("");
  const [slug, setSlug] = useState("");
  const [slugState, setSlugState] = useState<"" | "ok" | "taken">("");
  const [custom, setCustom] = useState(false);
  const [wait, setWait] = useState(0);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<ApiError | null>(null);

  useEffect(() => {
    if (wait <= 0) return;
    const id = setTimeout(() => setWait((w) => w - 1), 1000);
    return () => clearTimeout(id);
  }, [wait]);

  useEffect(() => {
    if (!slug || slug.length < 3) { setSlugState(""); return; }
    const id = setTimeout(async () => {
      try {
        const r = await api<{ available: boolean }>("/api/v1/slugs/check", { query: { type: "user", slug } });
        setSlugState(r.data.available ? "ok" : "taken");
      } catch { setSlugState(""); }
    }, 400);
    return () => clearTimeout(id);
  }, [slug]);

  const request = async () => {
    setBusy(true); setErr(null);
    try {
      const r = await api<{ retry_after: number }>("/api/v1/auth/otp/request", { method: "POST", body: { email, locale } });
      setWait(r.data.retry_after);
      setStep("code");
    } catch (e) { setErr(e as ApiError); } finally { setBusy(false); }
  };

  const verify = async (value: string) => {
    setBusy(true); setErr(null);
    try {
      await api<{ user: Me }>("/api/v1/auth/otp/verify", {
        method: "POST", body: { email, code: value, locale, accept_terms: accept, nickname: nickname || undefined, slug: slug || undefined },
      });
      await refresh();
      router.replace(sanitizeReturnTo(sp.get("return_to"), `/${locale}`));
    } catch (e) { setErr(e as ApiError); setCode(""); } finally { setBusy(false); }
  };

  const complete = async () => {
    setBusy(true); setErr(null);
    try {
      await api<{ user: Me }>("/api/v1/auth/oauth/complete", {
        method: "POST", body: { locale, accept_terms: accept, nickname: nickname || undefined, slug: slug || undefined },
      });
      await refresh();
      router.replace(sanitizeReturnTo(sp.get("return_to"), `/${locale}`));
    } catch (e) { setErr(e as ApiError); } finally { setBusy(false); }
  };

  const fe = fieldMessages(t, err);
  const onboarding = sp.get("step") === "onboarding";
  const oauthFailed = sp.get("error") === "oauth";
  const social = (p: "google" | "facebook") =>
    `/api/v1/auth/oauth/${p}/start?${new URLSearchParams({ locale, return_to: sanitizeReturnTo(sp.get("return_to"), `/${locale}`) }).toString()}`;
  const terms = (
    <Checkbox checked={accept} onChange={(e) => setAccept(e.currentTarget.checked)} error={fe.accept_terms}
      label={<>{t("login.accept_terms")} (<Anchor component={Link} href={`/${locale}/legal/terms`} target="_blank">{t("legal.terms.title")}</Anchor>, <Anchor component={Link} href={`/${locale}/legal/privacy`} target="_blank">{t("legal.privacy.title")}</Anchor>)</>} />
  );
  const publicName = (
    <>
      <Anchor component="button" type="button" size="sm" onClick={() => setCustom((v) => !v)}>{t("login.public_name_title")}</Anchor>
      <Collapse expanded={custom}>
        <Stack>
          <TextInput label={t("login.nickname")} value={nickname} onChange={(e) => setNickname(e.currentTarget.value)} maxLength={100} />
          <TextInput label={t("login.slug")} value={slug} onChange={(e) => setSlug(e.currentTarget.value.toLowerCase())} maxLength={32} leftSection="@"
            error={fe.slug ?? (slugState === "taken" ? t("field.taken") : undefined)} description={slugState === "ok" ? t("login.slug_available") : undefined} />
        </Stack>
      </Collapse>
    </>
  );
  return (
    <Stack maw={420}>
      <Title order={1}>{t("login.title")}</Title>
      {oauthFailed && <Alert color="red" role="alert">{t("login.oauth_failed")}</Alert>}
      {onboarding ? (
        <Stack>
          <Text size="sm">{t("login.onboarding_hint")}</Text>
          {terms}
          {publicName}
          <Button loading={busy} disabled={!accept} onClick={() => void complete()}>{t("login.confirm")}</Button>
        </Stack>
      ) : step === "email" ? (
        <form onSubmit={(e) => { e.preventDefault(); void request(); }}>
          <Stack>
            <TextInput type="email" label={t("login.email")} value={email} onChange={(e) => setEmail(e.currentTarget.value)} required autoComplete="email" error={fe.email} />
            <Button type="submit" loading={busy} disabled={!email}>{t("login.send_code")}</Button>
            <Text size="sm" ta="center">{t("login.or")}</Text>
            <Button component="a" href={social("google")} variant="default">{t("login.google")}</Button>
            <Button component="a" href={social("facebook")} variant="default">{t("login.facebook")}</Button>
          </Stack>
        </form>
      ) : (
        <Stack>
          <Text size="sm">{t("login.code_sent", { email })}</Text>
          <Text size="sm" id="code-label">{t("login.code")}</Text>
          <PinInput length={6} type="number" oneTimeCode value={code} onChange={setCode} onComplete={(v) => accept && void verify(v)} ariaLabel={t("login.code")} disabled={busy} />
          {terms}
          {publicName}
          <Button loading={busy} disabled={code.length !== 6 || !accept} onClick={() => void verify(code)}>{t("login.confirm")}</Button>
          <Button variant="subtle" disabled={wait > 0 || busy} onClick={() => void request()}>{wait > 0 ? t("login.resend_in", { seconds: wait }) : t("login.resend")}</Button>
        </Stack>
      )}
      {err && !err.fields && <Alert color="red" role="alert">{errorText(t, err)}</Alert>}
      {err?.code === "invalid_code" && <Alert color="red" role="alert">{t("error.invalid_code")}</Alert>}
      {err?.status === 409 && <Alert color="red" role="alert">{t("error.slug_taken")}</Alert>}
    </Stack>
  );
}

export function LoginForm() {
  return <Suspense><Form /></Suspense>;
}

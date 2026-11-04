"use client";
import { Anchor, Button, Group, Paper, Text } from "@mantine/core";
import Link from "next/link";
import { useEffect, useState } from "react";
import { setCookie } from "@/lib/cookies";
import { useLocale, useT } from "./Providers";

export const CONSENT_COOKIE = "bl_consent";
export type ConsentCategory = "necessary" | "analytics";

export function readConsent(cookie: string): { necessary: true; analytics: boolean } | null {
  const m = cookie.split("; ").find((c) => c.startsWith(CONSENT_COOKIE + "="));
  if (!m) return null;
  // value format "v1:necessary" – analytics is never enabled in the MVP
  return m.split("=")[1].startsWith("v1:") ? { necessary: true, analytics: false } : null;
}

/** Categories that may be loaded. In the MVP analytics is always false: nothing optional exists. */
export function useConsent(): Record<ConsentCategory, boolean> {
  return { necessary: true, analytics: false };
}

export function CookieBanner() {
  const t = useT();
  const locale = useLocale();
  const [visible, setVisible] = useState(false);
  useEffect(() => {
    setVisible(readConsent(document.cookie) === null);
  }, []);
  if (!visible) return null;
  return (
    <Paper role="region" aria-label={t("cookie.banner.title")} withBorder shadow="md" p="md" pos="fixed" bottom={12} left={12} right={12} style={{ zIndex: 300 }}>
      <Group justify="space-between" wrap="wrap">
        <Text size="sm" maw={720}>
          {t("cookie.banner.text")}{" "}
          <Anchor component={Link} href={`/${locale}/legal/cookies`}>{t("cookie.banner.more")}</Anchor>
        </Text>
        <Button
          size="xs"
          onClick={() => {
            setCookie(CONSENT_COOKIE, "v1:necessary", 180);
            setVisible(false);
          }}
        >
          {t("cookie.banner.ok")}
        </Button>
      </Group>
    </Paper>
  );
}

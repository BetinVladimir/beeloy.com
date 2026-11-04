"use client";
import { Alert, Button, Center, Loader, Skeleton, Stack, Text } from "@mantine/core";
import type { ApiError } from "@/lib/api";
import { useT } from "./Providers";

export function errorKey(e: { code: string } | null | undefined): string {
  return `error.${e?.code ?? "internal"}`;
}

/** Message for an API error: a specific reason ("slug_taken", "request_pending"…) wins over the generic code. */
export function errorText(t: (k: string) => string, e: { code: string; message?: string } | null | undefined): string {
  if (e && /^[a-z_]+$/.test(e.message ?? "") && ["conflict", "forbidden", "rate_limited", "validation_failed"].includes(e.code)) {
    const specific = `error.${e.message}`;
    const text = t(specific);
    if (text !== specific) return text;
  }
  return t(errorKey(e));
}

export function ErrorAlert({ error, onRetry }: { error: ApiError; onRetry?: () => void }) {
  const t = useT();
  return (
    <Alert color="red" role="alert" title={t("error.title")}>
      <Stack gap="xs" align="flex-start">
        <Text size="sm">{errorText(t, error)}</Text>
        {onRetry && <Button size="xs" variant="light" onClick={onRetry}>{t("common.retry")}</Button>}
      </Stack>
    </Alert>
  );
}

export function ListSkeleton({ rows = 3 }: { rows?: number }) {
  return <Stack aria-busy="true">{Array.from({ length: rows }, (_, i) => <Skeleton key={i} h={72} />)}</Stack>;
}

export function Spinner() {
  return <Center py="xl"><Loader /></Center>;
}

export function LoadMore({ next, loading, onMore }: { next: string | null; loading: boolean; onMore: () => void }) {
  const t = useT();
  if (!next) return null;
  return <Center><Button variant="default" loading={loading} onClick={onMore}>{t("common.load_more")}</Button></Center>;
}

/** Maps a validation error to field messages. */
export function fieldMessages(t: (k: string) => string, e: ApiError | null): Record<string, string> {
  const out: Record<string, string> = {};
  if (e?.fields) for (const [k, code] of Object.entries(e.fields)) out[k] = t(`field.${code}`);
  return out;
}

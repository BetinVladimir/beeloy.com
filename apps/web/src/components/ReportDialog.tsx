"use client";
import { Button, Modal, Select, Stack, Textarea } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import { useState } from "react";
import { ApiError, api } from "@/lib/api";
import { useT } from "./Providers";
import { errorText } from "./States";

const REASONS = ["spam", "abuse", "illegal", "other"] as const;

export function ReportDialog({ targetType, targetId, label }: { targetType: "message" | "idea" | "user" | "article" | "comment"; targetId: string; label?: string }) {
  const t = useT();
  const [open, setOpen] = useState(false);
  const [reason, setReason] = useState<string>("spam");
  const [details, setDetails] = useState("");
  const [busy, setBusy] = useState(false);
  return (
    <>
      <Button size="compact-xs" variant="subtle" color="gray" onClick={() => setOpen(true)}>{label ?? t("report.action")}</Button>
      <Modal opened={open} onClose={() => setOpen(false)} title={t("report.title")}>
        <Stack>
          <Select label={t("report.reason")} data={REASONS.map((r) => ({ value: r, label: t(`report.reason.${r}`) }))} value={reason} onChange={(v) => setReason(v ?? "spam")} allowDeselect={false} />
          <Textarea label={t("report.details")} value={details} onChange={(e) => setDetails(e.currentTarget.value)} maxLength={1000} />
          <Button loading={busy} onClick={async () => {
            setBusy(true);
            try {
              await api("/api/v1/reports", { method: "POST", body: { target_type: targetType, target_id: targetId, reason, details } });
              notifications.show({ message: t("report.sent") });
              setOpen(false);
            } catch (e) {
              notifications.show({ color: "red", message: errorText(t, e as ApiError) });
            } finally {
              setBusy(false);
            }
          }}>{t("report.send")}</Button>
        </Stack>
      </Modal>
    </>
  );
}

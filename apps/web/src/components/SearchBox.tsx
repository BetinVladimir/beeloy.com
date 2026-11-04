"use client";
import { ActionIcon, TextInput } from "@mantine/core";
import { IconSearch } from "@tabler/icons-react";
import { useRouter } from "next/navigation";
import { useState } from "react";
import { useLocale, useT } from "./Providers";

export function SearchBox() {
  const t = useT();
  const locale = useLocale();
  const router = useRouter();
  const [q, setQ] = useState("");
  return (
    <form
      role="search"
      onSubmit={(e) => {
        e.preventDefault();
        const text = q.trim();
        if (text.length >= 2) router.push(`/${locale}/search?q=${encodeURIComponent(text)}`);
      }}
    >
      <TextInput
        size="xs"
        value={q}
        onChange={(e) => setQ(e.currentTarget.value)}
        placeholder={t("search.placeholder")}
        aria-label={t("search.placeholder")}
        rightSection={<ActionIcon type="submit" variant="subtle" aria-label={t("search.submit")}><IconSearch size={14} /></ActionIcon>}
      />
    </form>
  );
}

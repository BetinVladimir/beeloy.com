"use client";
import { Button, Menu, ScrollArea } from "@mantine/core";
import { IconLanguage } from "@tabler/icons-react";
import { usePathname, useRouter } from "next/navigation";
import { setCookie } from "@/lib/cookies";
import { LOCALE_LIST, isLocale } from "@/lib/locales";
import { useLocale, useT } from "./Providers";

export function switchLocalePath(pathname: string, to: string): string {
  const parts = pathname.split("/");
  if (isLocale(parts[1])) parts[1] = to;
  else parts.splice(1, 0, to);
  return parts.join("/") || `/${to}`;
}

export function LocaleSwitcher() {
  const t = useT();
  const locale = useLocale();
  const router = useRouter();
  const pathname = usePathname();

  const choose = (code: string) => {
    setCookie("bl_locale", code, 365);
    router.push(switchLocalePath(pathname, code) + window.location.search);
  };

  return (
    <Menu position="bottom-end" withinPortal>
      <Menu.Target>
        <Button variant="default" size="xs" leftSection={<IconLanguage size={16} />} aria-label={t("nav.language")}>
          {locale.toUpperCase()}
        </Button>
      </Menu.Target>
      <Menu.Dropdown>
        <ScrollArea.Autosize mah={320}>
          {LOCALE_LIST.map((l) => (
            <Menu.Item key={l.code} onClick={() => choose(l.code)} fw={l.code === locale ? 700 : 400} lang={l.code}>
              {l.name}
            </Menu.Item>
          ))}
        </ScrollArea.Autosize>
      </Menu.Dropdown>
    </Menu>
  );
}

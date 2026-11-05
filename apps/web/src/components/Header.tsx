"use client";
import { ActionIcon, Anchor, Badge, Box, Burger, Button, Container, Drawer, Group, Menu, Stack, useComputedColorScheme, useMantineColorScheme } from "@mantine/core";
import { useDisclosure } from "@mantine/hooks";
import { IconMessage, IconMoon, IconSun, IconUser } from "@tabler/icons-react";
import Link from "next/link";
import { LocaleSwitcher } from "./LocaleSwitcher";
import { hasRole, useLocale, useSession, useT } from "./Providers";
import { SearchBox } from "./SearchBox";
import { useChat } from "@/stores/chat";

export function Header() {
  const t = useT();
  const locale = useLocale();
  const { me, loading, logout } = useSession();
  const unread = useChat((s) => s.unread + s.requests);
  const [opened, { toggle, close }] = useDisclosure(false);
  const { setColorScheme } = useMantineColorScheme();
  const scheme = useComputedColorScheme("light");
  const home = `/${locale}`;

  const links = (
    <>
      <Anchor component={Link} href={`${home}/news`} onClick={close} underline="never" c="inherit">{t("nav.news")}</Anchor>
      <Anchor component={Link} href={`${home}/articles`} onClick={close} underline="never" c="inherit">{t("nav.articles")}</Anchor>
      <Anchor component={Link} href={`${home}/ideas`} onClick={close} underline="never" c="inherit">{t("nav.ideas")}</Anchor>
    </>
  );

  return (
    <header>
      <Container size="lg" py="sm">
        <Group justify="space-between" wrap="nowrap">
          <Link href={home} style={{ display: "flex" }}>
            <Box component="img" src="/logo.svg" alt={t("app.name")} h={40} w="auto" darkHidden />
            <Box component="img" src="/logo-white.svg" alt={t("app.name")} h={40} w="auto" lightHidden />
          </Link>
          <Group gap="xs" wrap="nowrap">
            <Group visibleFrom="sm"><SearchBox /></Group>
            <LocaleSwitcher />
            <ActionIcon variant="default" aria-label={t("nav.toggle_theme")} onClick={() => setColorScheme(scheme === "dark" ? "light" : "dark")}>
              <Box component="span" lightHidden><IconSun size={18} /></Box>
              <Box component="span" darkHidden><IconMoon size={18} /></Box>
            </ActionIcon>
            {!loading && me && (
              <>
                <ActionIcon component={Link} href={`${home}/messages`} variant="default" aria-label={t("nav.messages")} pos="relative">
                  <IconMessage size={18} />
                  {unread > 0 && <Badge size="xs" circle color="red" pos="absolute" top={-6} right={-6}>{unread}</Badge>}
                </ActionIcon>
                <Menu position="bottom-end">
                  <Menu.Target>
                    <ActionIcon variant="default" aria-label={t("nav.account")}><IconUser size={18} /></ActionIcon>
                  </Menu.Target>
                  <Menu.Dropdown>
                    <Menu.Item component={Link} href={`${home}/people/${me.slug}`}>{t("nav.profile")}</Menu.Item>
                    <Menu.Item component={Link} href={`${home}/messages`}>{t("nav.messages")}</Menu.Item>
                    <Menu.Item component={Link} href={`${home}/settings`}>{t("nav.settings")}</Menu.Item>
                    {hasRole(me, "editor") && <Menu.Item component={Link} href={`${home}/editor`}>{t("nav.editor")}</Menu.Item>}
                    {hasRole(me, "moderator") && <Menu.Item component={Link} href={`${home}/mod/articles`}>{t("nav.moderation")}</Menu.Item>}
                    {hasRole(me, "admin") && <Menu.Item component={Link} href={`${home}/admin/users`}>{t("nav.admin")}</Menu.Item>}
                    <Menu.Divider />
                    <Menu.Item onClick={() => void logout()}>{t("nav.logout")}</Menu.Item>
                  </Menu.Dropdown>
                </Menu>
              </>
            )}
            {!loading && !me && <Button component={Link} href={`${home}/login`} size="xs">{t("nav.login")}</Button>}
            <Burger opened={opened} onClick={toggle} hiddenFrom="sm" size="sm" aria-label={t("nav.menu")} />
          </Group>
        </Group>
      </Container>
      <nav aria-label={t("nav.menu")} style={{ borderTop: "1px solid var(--mantine-color-default-border)", borderBottom: "1px solid var(--mantine-color-default-border)" }}>
        <Container size="lg">
          <Group gap="xl" h={44} visibleFrom="sm" fw={600}>{links}</Group>
        </Container>
      </nav>
      <Drawer opened={opened} onClose={close} title={t("app.name")} size="xs">
        <Stack>
          <SearchBox />
          {links}
        </Stack>
      </Drawer>
    </header>
  );
}

import { Container, Group, Text } from "@mantine/core";
import { LinkAnchor } from "./Links";
import type { TFunction } from "@/lib/i18n";

export function Footer({ t, locale }: { t: TFunction; locale: string }) {
  return (
    <footer>
      <Container size="lg" py="xl">
        <Group justify="space-between">
          <Text size="sm" c="dimmed">© Beeloy</Text>
          <Group gap="md">
            {(["privacy", "cookies", "terms", "rules"] as const).map((d) => (
              <LinkAnchor key={d} href={`/${locale}/legal/${d}`} size="sm">{t(`legal.${d}.title`)}</LinkAnchor>
            ))}
          </Group>
        </Group>
      </Container>
    </footer>
  );
}

import { Stack, Text, Title } from "@mantine/core";
import { LinkButton } from "@/components/Links";

export default function NotFound() {
  return (
    <Stack align="flex-start" py="xl">
      <Title order={1}>404</Title>
      <Text>Page not found.</Text>
      <LinkButton href="/" variant="default">Beeloy</LinkButton>
    </Stack>
  );
}

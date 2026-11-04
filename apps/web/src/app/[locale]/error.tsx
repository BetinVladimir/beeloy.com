"use client";
import { Button, Stack, Text, Title } from "@mantine/core";

export default function Error({ reset }: { error: Error; reset: () => void }) {
  return (
    <Stack align="flex-start" py="xl" role="alert">
      <Title order={1}>Something went wrong</Title>
      <Text>Please try again.</Text>
      <Button onClick={reset}>Retry</Button>
    </Stack>
  );
}

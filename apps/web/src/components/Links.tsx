"use client";
import { Anchor, Button, type AnchorProps, type ButtonProps } from "@mantine/core";
import Link from "next/link";
import type { ReactNode } from "react";

// Server components cannot hand `component={Link}` (a function) to Mantine's client components, so they use these.
export function LinkButton({ href, children, ...rest }: ButtonProps & { href: string; children?: ReactNode }) {
  return <Button component={Link} href={href} {...rest}>{children}</Button>;
}

export function LinkAnchor({ href, children, ...rest }: AnchorProps & { href: string; children?: ReactNode }) {
  return <Anchor component={Link} href={href} {...rest}>{children}</Anchor>;
}

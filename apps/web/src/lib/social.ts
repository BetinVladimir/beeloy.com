export type SocialProvider = "google" | "facebook";

/**
 * Social sign-in providers switched on for this deployment: NEXT_PUBLIC_SOCIAL_PROVIDERS="google,facebook".
 * Empty (the default) disables the buttons and the "link" actions; the API also rejects providers without credentials.
 */
export function enabledSocial(): SocialProvider[] {
  const raw = process.env.NEXT_PUBLIC_SOCIAL_PROVIDERS ?? "";
  return raw.split(",").map((s) => s.trim()).filter((s): s is SocialProvider => s === "google" || s === "facebook");
}

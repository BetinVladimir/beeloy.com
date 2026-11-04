/** Only same-site paths are accepted as return targets (prevents open redirects). */
export function sanitizeReturnTo(value: string | null | undefined, fallback: string): string {
  if (!value) return fallback;
  if (value.length > 200) return fallback;
  if (!value.startsWith("/") || value.startsWith("//")) return fallback;
  if (/[\\\u0000-\u001f\u007f]/.test(value)) return fallback;
  return value;
}

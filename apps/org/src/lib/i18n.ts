import fallbackEn from "@/contracts/fallback-en.json";

export type Messages = Record<string, string>;
export type TFunction = (key: string) => string;

const API = () => (process.env.I18N_API_ORIGIN ?? "http://localhost:8080").replace(/\/$/, "");

/** Dictionary of a locale from the public i18n API of beeloy.com (merged over the bundled complete English one). */
export async function loadMessages(locale: string, fetchImpl: typeof fetch = fetch): Promise<Messages> {
  const base = fallbackEn as Messages;
  try {
    const res = await fetchImpl(`${API()}/api/v1/i18n/${locale}`, { next: { revalidate: 300 } } as RequestInit);
    if (!res.ok) return base;
    return { ...base, ...((await res.json()) as Messages) };
  } catch {
    return base;
  }
}

export function createT(messages: Messages): TFunction {
  return (key) => messages[key] ?? key;
}

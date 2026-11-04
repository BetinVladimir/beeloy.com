import { createT, loadMessages } from "./i18n";
import type { Locale } from "./locales";

/** Server components: translator for a locale. */
export async function getT(locale: Locale) {
  return createT(await loadMessages(locale), locale);
}

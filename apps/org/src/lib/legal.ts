/**
 * Terms, Privacy and Cookie policies are maintained on the legal site (https://legal.beeloy.org/{locale}/{doc}),
 * not in the app dictionaries. The community rules have no document there yet and stay in the app.
 */
const LEGAL_SITE = "https://legal.beeloy.org";
const LEGAL_DOCS = new Set(["terms", "privacy", "cookies"]);
/** Languages the legal site publishes; any other interface language links to the English text. */
const LEGAL_LOCALES = new Set(["bg", "cs", "da", "de", "el", "en", "es", "et", "fi", "fr", "ga", "hr", "hu", "it", "lt", "lv", "mt", "nl", "pl", "pt", "ro", "ru", "sk", "sl", "sv"]);

export function legalUrl(locale: string, doc: string): string | null {
  if (!LEGAL_DOCS.has(doc)) return null;
  return `${LEGAL_SITE}/${LEGAL_LOCALES.has(locale) ? locale : "en"}/${doc}`;
}

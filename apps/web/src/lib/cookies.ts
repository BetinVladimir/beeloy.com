/** Writes a first-party, strictly functional cookie (browser only). */
export function setCookie(name: string, value: string, days: number): void {
  document.cookie = `${name}=${value}; Path=/; Max-Age=${days * 86400}; SameSite=Lax`;
}

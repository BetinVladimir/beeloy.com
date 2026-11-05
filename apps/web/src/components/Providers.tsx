"use client";
import { MantineProvider, ColorSchemeScript } from "@mantine/core";
import { Notifications } from "@mantine/notifications";
import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from "react";
import { usePathname, useRouter } from "next/navigation";
import type { Me } from "@/contracts/api-types";
import { api } from "@/lib/api";
import { createT, type Messages, type TFunction } from "@/lib/i18n";
import { sanitizeReturnTo } from "@/lib/returnTo";
import { cssVariablesResolver, theme } from "@/ui/theme";
import { useChatStream } from "./useChatStream";

interface I18nValue { t: TFunction; locale: string; messages: Messages }
const I18nContext = createContext<I18nValue>({ t: (k) => k, locale: "en", messages: {} });
export const useT = () => useContext(I18nContext).t;
export const useLocale = () => useContext(I18nContext).locale;

interface SessionValue { me: Me | null; loading: boolean; refresh: () => Promise<void>; logout: () => Promise<void> }
const SessionContext = createContext<SessionValue>({ me: null, loading: true, refresh: async () => {}, logout: async () => {} });
export const useSession = () => useContext(SessionContext);

export function hasRole(me: Me | null, role: "editor" | "moderator" | "admin"): boolean {
  return !!me && me.status === "active" && me.roles.some((r) => r.role === role || (role !== "admin" && r.role === "admin"));
}

/** Client-side guard (UX only: the API enforces every permission). */
export function useRequireAuth(): { me: Me | null; ready: boolean } {
  const { me, loading } = useSession();
  const router = useRouter();
  const pathname = usePathname();
  const locale = useLocale();
  useEffect(() => {
    if (!loading && !me) router.replace(`/${locale}/login?return_to=${encodeURIComponent(sanitizeReturnTo(pathname, `/${locale}`))}`);
  }, [loading, me, router, pathname, locale]);
  return { me, ready: !loading && !!me };
}

function StreamMount({ me }: { me: Me | null }) {
  useChatStream(me?.status === "active" ? me.id : null);
  return null;
}

export function Providers({ locale, messages, children }: { locale: string; messages: Messages; children: ReactNode }) {
  const t = useMemo(() => createT(messages, locale), [messages, locale]);
  const [me, setMe] = useState<Me | null>(null);
  const [loading, setLoading] = useState(true);

  const refresh = useCallback(async () => {
    try {
      const r = await api<Me>("/api/v1/me");
      setMe(r.data);
    } catch {
      setMe(null);
    } finally {
      setLoading(false);
    }
  }, []);
  useEffect(() => {
    void refresh();
  }, [refresh]);

  const logout = useCallback(async () => {
    try {
      await api("/api/v1/auth/logout", { method: "POST" });
    } finally {
      setMe(null);
    }
  }, []);

  return (
    <I18nContext.Provider value={{ t, locale, messages }}>
      <MantineProvider theme={theme} cssVariablesResolver={cssVariablesResolver} defaultColorScheme="dark">
        <Notifications position="top-right" />
        <SessionContext.Provider value={{ me, loading, refresh, logout }}>
          <StreamMount me={me} />
          {children}
        </SessionContext.Provider>
      </MantineProvider>
    </I18nContext.Provider>
  );
}

export { ColorSchemeScript };

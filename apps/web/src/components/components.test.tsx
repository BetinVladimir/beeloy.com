import { act, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { ArticleBody } from "./ArticleBody";
import { AuthorChip, MentionText } from "./CommentThread";
import { CookieBanner, readConsent, useConsent } from "./CookieBanner";
import { IdeaCard, ModerationActions, StatusBadge, VoteButton } from "./IdeaParts";
import { LoginForm } from "./LoginForm";
import { switchLocalePath } from "./LocaleSwitcher";
import { Markdown } from "./Markdown";
import { errorText } from "./States";
import { useChatStream } from "./useChatStream";
import { useChat } from "@/stores/chat";
import { IDEA_STATUSES, IDEA_TRANSITIONS } from "@/contracts/idea-transitions";
import type { Idea } from "@/contracts/api-types";
import { json, me, mockFetch, renderApp } from "@/test-utils";
import { renderHook } from "@testing-library/react";

let searchParams = new URLSearchParams();
vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() }),
  usePathname: () => "/en/x",
  useSearchParams: () => searchParams,
}));

const idea = (o: Partial<Idea> = {}): Idea => ({ id: "i1", slug: "s", author: { user_id: "u2", nickname: "Bob", slug: "bob" }, title: "T", body: "B", category: "product", lang: "en", status: "submitted", team_reply: null, team_reply_at: null, duplicate_of_slug: null, votes_count: 3, created_at: "2026-01-01T00:00:00Z", updated_at: "2026-01-01T00:00:00Z", viewer_voted: false, ...o });

describe("MentionText / AuthorChip", () => {
  it("turns @slug into profile links and leaves other text alone", () => {
    renderApp(<MentionText locale="bg" text={"hi @business-expert and mail foo@bar.com, @Upper no"} />);
    const link = screen.getByRole("link", { name: "@business-expert" });
    expect(link).toHaveAttribute("href", "/bg/people/business-expert");
    expect(screen.queryByRole("link", { name: "@bar" })).toBeNull();
    expect(screen.getAllByRole("link")).toHaveLength(1);
  });
  it("renders HTML-looking text as text", () => {
    const { container } = renderApp(<MentionText locale="en" text={'<img src=x onerror=alert(1)> @ok'} />);
    expect(container.querySelector("img")).toBeNull();
  });
  it("shows nickname and @slug as links; a deleted author has no link", () => {
    renderApp(<AuthorChip author={{ user_id: "u", nickname: "Business Expert", slug: "business-expert" }} locale="bg" />);
    expect(screen.getByRole("link", { name: "Business Expert" })).toHaveAttribute("href", "/bg/people/business-expert");
    expect(screen.getByRole("link", { name: "@business-expert" })).toHaveAttribute("href", "/bg/people/business-expert");
  });
  it("deleted author", () => {
    renderApp(<AuthorChip author={{ user_id: null, nickname: "Deleted user", slug: "" }} locale="en" />);
    expect(screen.getByText("Deleted user")).toBeInTheDocument();
    expect(screen.queryByRole("link")).toBeNull();
  });
});

describe("Markdown safety", () => {
  it("does not render scripts, iframes, javascript: links or event handlers", () => {
    const evil = `<script>window.hacked=1</script>\n\n<iframe src="https://evil"></iframe>\n\n[click](javascript:alert(1))\n\n![x](x" onerror="alert(1))\n\n<img src=x onerror=alert(1)>\n\n[ok](https://example.com)`;
    const { container } = renderApp(<Markdown>{evil}</Markdown>);
    expect(container.querySelector("script")).toBeNull();
    expect(container.querySelector("iframe")).toBeNull();
    expect(container.querySelector("[onerror]")).toBeNull();
    expect(container.querySelector('a[href^="javascript:"]')).toBeNull();
    const ok = screen.getByRole("link", { name: "ok" });
    expect(ok).toHaveAttribute("rel", expect.stringContaining("nofollow"));
    expect((window as unknown as { hacked?: number }).hacked).toBeUndefined();
  });
});

describe("ArticleBody notices", () => {
  const base = { id: "a", slug: "s", category_key: "news", locale: "en", title: "Title", summary: "Sum", body_md: "Body", visibility: "public" as const, cover_url: null, published_at: "2026-01-01T00:00:00Z", author_name: "Ann", author_slug: "ann", origin: "human" as const, available_locales: ["en"], fallback_from: null };
  it("warns about machine translations and fallbacks", () => {
    renderApp(<ArticleBody article={{ ...base, origin: "machine", fallback_from: "fr", available_locales: ["en", "de"] }} locale="fr" categoryLabel="News" />);
    expect(screen.getByText(/translated automatically/i)).toBeInTheDocument();
    expect(screen.getByText(/not available in your language/i)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Deutsch" })).toHaveAttribute("href", "/de/news/s");
  });
  it("shows nothing extra for a human original", () => {
    renderApp(<ArticleBody article={base} locale="en" categoryLabel="News" />);
    expect(screen.queryByText(/translated automatically/i)).toBeNull();
    expect(screen.getByRole("link", { name: "Ann" })).toHaveAttribute("href", "/en/people/ann");
  });
});

describe("VoteButton", () => {
  it("updates optimistically and rolls back on error", async () => {
    mockFetch((url) => (url.endsWith("/me") ? json({ data: me }) : url.includes("/vote") ? json({ error: { code: "conflict", message: "closed" } }, 409) : undefined));
    const calls: [boolean, number][] = [];
    renderApp(<VoteButton idea={idea()} onChange={(v, c) => calls.push([v, c])} />);
    const btn = await screen.findByRole("button", { name: /vote/i });
    await userEvent.click(btn);
    await waitFor(() => expect(calls).toHaveLength(2));
    expect(calls[0]).toEqual([true, 4]);
    expect(calls[1]).toEqual([false, 3]);
  });
  it("is disabled for the own idea and for closed ideas", async () => {
    mockFetch((url) => (url.endsWith("/me") ? json({ data: { ...me, id: "u2" } }) : undefined));
    renderApp(<VoteButton idea={idea()} onChange={() => {}} />);
    expect(await screen.findByRole("button", { name: /vote/i })).toBeDisabled();
  });
  it("sends guests to the login page", async () => {
    mockFetch();
    renderApp(<VoteButton idea={idea()} onChange={() => {}} />);
    expect(await screen.findByRole("link", { name: /vote/i })).toHaveAttribute("href", "/en/login");
  });
});

describe("ModerationActions", () => {
  it("is invisible for ordinary members", async () => {
    mockFetch((url) => (url.endsWith("/me") ? json({ data: me }) : undefined));
    const { container } = renderApp(<ModerationActions idea={idea()} onUpdated={() => {}} />);
    await new Promise((r) => setTimeout(r, 30));
    expect(container.textContent).not.toContain("Moderation");
  });
  it("offers only allowed transitions for a moderator", async () => {
    mockFetch((url) => (url.endsWith("/me") ? json({ data: { ...me, roles: [{ role: "moderator", scope_type: "global", scope_id: null }] } }) : undefined));
    renderApp(<ModerationActions idea={idea({ status: "planned" })} onUpdated={() => {}} />);
    const select = (await screen.findAllByLabelText("Change status"))[0];
    await userEvent.click(select);
    // Mantine renders options in a portal with role="option"; hidden ones are filtered by accessible name
    const names = (await screen.findAllByRole("option", { hidden: true })).map((o) => o.textContent);
    expect(names).toEqual(["In progress", "Declined"]); // planned -> in_progress | declined only
  });
  it("closed ideas have no transitions", async () => {
    mockFetch((url) => (url.endsWith("/me") ? json({ data: { ...me, roles: [{ role: "admin", scope_type: "global", scope_id: null }] } }) : undefined));
    renderApp(<ModerationActions idea={idea({ status: "delivered" })} onUpdated={() => {}} />);
    expect(await screen.findByText(/closed/i)).toBeInTheDocument();
  });
  it("transition table matches the backend (8 statuses; terminal ones are closed)", () => {
    expect(IDEA_STATUSES).toHaveLength(8);
    for (const s of ["delivered", "declined", "duplicate"] as const) expect(IDEA_TRANSITIONS[s]).toEqual([]);
    expect(IDEA_TRANSITIONS.submitted).toEqual(["under_review", "needs_info", "declined", "duplicate"]);
  });
});

describe("IdeaCard / StatusBadge", () => {
  it("shows the status and the author with @slug", () => {
    renderApp(<><StatusBadge status="in_progress" /><IdeaCard idea={idea()} /></>);
    expect(screen.getAllByText("In progress").length).toBeGreaterThan(0);
    expect(screen.getByRole("link", { name: "@bob" })).toHaveAttribute("href", "/en/people/bob");
  });
});

describe("LoginForm", () => {
  it("walks from e-mail to code and requires accepting the terms", async () => {
    const { calls } = mockFetch((url) => {
      if (url.includes("/otp/request")) return json({ data: { retry_after: 60 } }, 202);
      if (url.includes("/otp/verify")) return json({ data: { user: me, is_new: true } });
      if (url.includes("/slugs/check")) return json({ data: { available: true } });
      return undefined;
    });
    renderApp(<LoginForm />);
    await userEvent.type(await screen.findByLabelText(/^Email/), "a@b.co");
    await userEvent.click(screen.getByRole("button", { name: "Send code" }));
    expect(await screen.findByText(/We sent a 6-digit code to a@b.co/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Send again in/ })).toBeDisabled();
    const confirm = screen.getByRole("button", { name: "Confirm" });
    expect(confirm).toBeDisabled();
    const digits = screen.getAllByLabelText("Code from the email");
    expect(digits.length).toBe(6);
    await userEvent.click(screen.getByRole("checkbox"));
    await userEvent.click(digits[0]);
    await userEvent.keyboard("123456");
    await waitFor(() => expect(calls.some((c) => c.url.includes("/otp/verify"))).toBe(true));
    const body = JSON.parse(String(calls.find((c) => c.url.includes("/otp/verify"))!.init!.body));
    expect(body).toMatchObject({ email: "a@b.co", code: "123456", accept_terms: true });
  });
  it("finishes a social sign-up only after the terms are accepted", async () => {
    searchParams = new URLSearchParams("step=onboarding");
    try {
      const { calls } = mockFetch((url) => (url.includes("/oauth/complete") ? json({ data: { user: me, is_new: true } }) : undefined));
      renderApp(<LoginForm />);
      const confirm = await screen.findByRole("button", { name: "Confirm" });
      expect(confirm).toBeDisabled();
      expect(screen.queryByLabelText(/^Email/)).toBeNull();
      await userEvent.click(screen.getByRole("checkbox"));
      await userEvent.click(confirm);
      await waitFor(() => expect(calls.some((c) => c.url.includes("/oauth/complete"))).toBe(true));
      expect(JSON.parse(String(calls.find((c) => c.url.includes("/oauth/complete"))!.init!.body))).toMatchObject({ accept_terms: true });
    } finally {
      searchParams = new URLSearchParams();
    }
  });
  it("shows a neutral error after a failed provider sign-in", async () => {
    searchParams = new URLSearchParams("error=oauth");
    try {
      mockFetch();
      renderApp(<LoginForm />);
      expect(await screen.findByText(/did not work/)).toBeInTheDocument();
    } finally {
      searchParams = new URLSearchParams();
    }
  });
  it("offers e-mail code, Google and Facebook; the social buttons are plain navigations to the API", async () => {
    mockFetch();
    renderApp(<LoginForm />);
    await screen.findByLabelText(/^Email/);
    const g = screen.getByRole("link", { name: "Continue with Google" });
    const f = screen.getByRole("link", { name: "Continue with Facebook" });
    expect(g.getAttribute("href")).toMatch(/^\/api\/v1\/auth\/oauth\/google\/start\?/);
    expect(f.getAttribute("href")).toMatch(/^\/api\/v1\/auth\/oauth\/facebook\/start\?/);
    expect(g.getAttribute("href")).toContain("return_to=%2Fen");
  });
});

describe("misc", () => {
  it("keeps the path when switching language", () => {
    expect(switchLocalePath("/bg/news/slug", "de")).toBe("/de/news/slug");
    expect(switchLocalePath("/bg", "de")).toBe("/de");
    expect(switchLocalePath("/news", "de")).toBe("/de/news");
  });
  it("cookie consent reads the v1 marker and never enables analytics", () => {
    expect(readConsent("a=1; bl_consent=v1:necessary")).toEqual({ necessary: true, analytics: false });
    expect(readConsent("a=1")).toBeNull();
    expect(readConsent("bl_consent=v0:all")).toBeNull();
    expect(renderHook(() => useConsent()).result.current).toEqual({ necessary: true, analytics: false });
  });
  it("cookie banner appears until accepted", async () => {
    mockFetch();
    document.cookie = "bl_consent=; Max-Age=0; Path=/";
    renderApp(<CookieBanner />);
    await userEvent.click(await screen.findByRole("button", { name: "OK" }));
    expect(document.cookie).toContain("bl_consent=v1:necessary");
    expect(screen.queryByRole("button", { name: "OK" })).toBeNull();
  });
  it("errorText prefers a specific reason over the generic code", () => {
    const t = (k: string) => ({ "error.slug_taken": "taken!", "error.conflict": "conflict", "error.not_found": "nf", "error.internal": "int" })[k] ?? k;
    expect(errorText(t, { code: "conflict", message: "slug_taken" })).toBe("taken!");
    expect(errorText(t, { code: "conflict", message: "unknown_reason" })).toBe("conflict");
    expect(errorText(t, { code: "not_found", message: "not found" })).toBe("nf");
    expect(errorText(t, null)).toBe("int");
  });
});

describe("useChatStream", () => {
  class FakeES {
    static all: FakeES[] = [];
    listeners: Record<string, ((e: MessageEvent) => void)[]> = {};
    onerror: (() => void) | null = null;
    closed = false;
    constructor(public url: string, public init?: EventSourceInit) { FakeES.all.push(this); }
    addEventListener(n: string, f: (e: MessageEvent) => void) { (this.listeners[n] ??= []).push(f); }
    close() { this.closed = true; }
    emit(n: string, data: unknown) { for (const f of this.listeners[n] ?? []) f({ data: JSON.stringify(data) } as MessageEvent); }
  }
  const M = (id: string, seq: number, client: string) => ({ id, conversation_id: "c1", seq, sender_id: "u2", client_id: client, body: "hi", deleted: false, created_at: "2026-01-01T00:00:00Z" });

  it("catches up after ready, deduplicates events and reconnects with backoff", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    FakeES.all = [];
    useChat.getState().reset();
    useChat.getState().merge("c1", [M("m1", 1, "k1")]);
    const { calls } = mockFetch((url) => {
      if (url.includes("unread-count")) return json({ data: { total: 2, requests: 1 } });
      if (url.includes("/c1/messages")) return json({ data: [M("m2", 2, "k2")] });
      return undefined;
    });
    renderHook(() => useChatStream("u1", FakeES as unknown as typeof EventSource));
    expect(FakeES.all).toHaveLength(1);
    expect(FakeES.all[0].url).toBe("/api/v1/events");
    expect(FakeES.all[0].init?.withCredentials).toBe(true);
    await act(async () => { FakeES.all[0].emit("ready", {}); await vi.advanceTimersByTimeAsync(10); });
    expect(useChat.getState().unread).toBe(2);
    expect(useChat.getState().requests).toBe(1);
    expect(calls.some((c) => c.url.includes("after_seq=1"))).toBe(true); // catch-up from the last known seq
    expect(useChat.getState().messages.c1.map((m) => m.id)).toEqual(["m1", "m2"]);
    await act(async () => { FakeES.all[0].emit("message", { conversation_id: "c1", message: M("m2", 2, "k2") }); await vi.advanceTimersByTimeAsync(10); });
    expect(useChat.getState().messages.c1).toHaveLength(2); // no duplicate
    // connection drops: a new EventSource is created after a backoff delay
    await act(async () => { FakeES.all[0].onerror?.(); });
    expect(FakeES.all[0].closed).toBe(true);
    expect(FakeES.all).toHaveLength(1);
    await act(async () => { await vi.advanceTimersByTimeAsync(1500); });
    expect(FakeES.all).toHaveLength(2);
    vi.useRealTimers();
  });
  it("does not connect for guests", () => {
    FakeES.all = [];
    renderHook(() => useChatStream(null, FakeES as unknown as typeof EventSource));
    expect(FakeES.all).toHaveLength(0);
  });
});

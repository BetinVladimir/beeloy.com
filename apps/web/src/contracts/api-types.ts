// Mirrors docs/tz/05-api-contract.md. Keep in sync with the Go structs (checked by the contract test).

export interface Role { role: "editor" | "moderator" | "admin"; scope_type: "global" | "category"; scope_id: string | null }

export interface Me {
  id: string; nickname: string; bio: string; avatar_url: string | null; slug: string; locale: string
  status: "active" | "blocked" | "pending_deletion" | "deleted"; profile_public: boolean; roles: Role[]; created_at: string
}

export interface PublicProfile {
  id: string; nickname: string; slug: string; canonical_slug: string; avatar_url: string | null; bio: string
  created_at: string; activity_visible: boolean
}

export interface Category { id: string; key: string; names: Record<string, string> }

export interface ArticleListItem {
  id: string; slug: string; category_key: string; locale: string; title: string; summary: string
  cover_url: string | null; published_at: string; fallback_from: string | null
}

export interface ArticleView {
  id: string; slug: string; category_key: string; locale: string; title: string; summary: string; body_md: string
  visibility: "public" | "restricted"; cover_url: string | null; published_at: string; author_name: string; author_slug: string
  origin: "human" | "machine"; available_locales: string[]; fallback_from: string | null
}

export type IdeaStatus = "submitted" | "under_review" | "needs_info" | "planned" | "in_progress" | "delivered" | "declined" | "duplicate"

export interface Author { user_id: string | null; nickname: string; slug: string; avatar_url?: string | null }

export interface Idea {
  id: string; slug: string; author: Author; title: string; body: string; category: string; lang: string; status: IdeaStatus
  team_reply: string | null; team_reply_at: string | null; duplicate_of_slug: string | null; votes_count: number
  created_at: string; updated_at: string; viewer_voted: boolean
}

export interface HistoryItem { from: IdeaStatus | null; to: IdeaStatus; note: string; at: string }

export interface Comment {
  id: string; target_type: "article" | "idea"; target_id: string; parent_id: string | null; author: Author
  body: string; status: "visible" | "hidden" | "deleted"; reply_count: number; created_at: string
}

export interface PersonHit { id: string; nickname: string; slug: string; avatar_url: string | null }

export interface Peer { user_id: string; nickname: string; slug: string; avatar_url: string | null; blocked_by_me: boolean }

export interface Conversation {
  id: string; status: "request" | "accepted" | "declined"; initiated_by_me: boolean; peer: Peer
  last_seq: number; last_message_at: string | null; unread_count: number; last_message_preview: string; muted: boolean
}

export interface Message {
  id: string; conversation_id: string; seq: number; sender_id: string | null; client_id: string
  body: string; deleted: boolean; created_at: string
}

export interface EditorListItem {
  id: string; slug: string; category_key: string; source_locale: string; status: "draft" | "in_review" | "published" | "archived"
  visibility: "public" | "restricted"; rev: number; published_rev: number; title: string; pending: boolean; updated_at: string
}

export interface RevisionRow { rev: number; locale: string; origin: "human" | "machine"; title: string; summary: string; created_at: string; stale: boolean; model: string; sha256: string; size: number }

export interface EditorArticle {
  article: { id: string; slug: string; status: EditorListItem["status"]; visibility: "public" | "restricted"; rev: number; published_rev: number; source_locale: string; category_id: string }
  source: { title: string; summary: string; body_md: string } | null
  revisions: RevisionRow[]
  viewer_ids: string[]
}

export interface Identity { id: string; provider: "email_otp" | "google" | "facebook"; created_at: string }

export interface Report {
  id: string; reporter_id: string; target_type: string; target_id: string; reason: string; details: string; status: string
  resolution_note: string; created_at: string; snapshot?: { seq: number; sender_id: string | null; body: string; created_at: string }[]
}

export interface AdminUser { id: string; nickname: string; slug: string; status: string; roles: Role[]; created_at: string }

export interface ApiErrorBody { error: { code: string; message: string; fields?: Record<string, string> } }

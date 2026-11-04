import type { IdeaStatus } from "./api-types";

// Allowed status changes (mirror of backend/internal/ideas/model.go; the server enforces them).
export const IDEA_TRANSITIONS: Record<IdeaStatus, IdeaStatus[]> = {
  submitted: ["under_review", "needs_info", "declined", "duplicate"],
  needs_info: ["under_review", "declined"],
  under_review: ["planned", "needs_info", "declined", "duplicate"],
  planned: ["in_progress", "declined"],
  in_progress: ["delivered", "planned"],
  delivered: [],
  declined: [],
  duplicate: [],
};

export const IDEA_STATUSES = Object.keys(IDEA_TRANSITIONS) as IdeaStatus[];
export const IDEA_CATEGORIES = ["product", "community", "content", "other"] as const;
export const NOTE_REQUIRED: IdeaStatus[] = ["declined", "duplicate"];

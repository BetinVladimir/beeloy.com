package ideas

import (
	"context"
	"net/netip"
	"strings"
	"time"
	"unicode/utf8"

	"beeloy.com/backend/internal/acl"
	"beeloy.com/backend/internal/apperr"
	"beeloy.com/backend/internal/audit"
	"beeloy.com/backend/internal/identity"
	"beeloy.com/backend/internal/locales"
	"beeloy.com/backend/internal/platform/db"
	"beeloy.com/backend/internal/platform/ids"
	"beeloy.com/backend/internal/platform/outbox"
	"beeloy.com/backend/internal/platform/pagination"
	"beeloy.com/backend/internal/platform/ratelimit"
	"beeloy.com/backend/internal/registry"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const dailyLimit = 5

var ruleCreate = ratelimit.Rule{Name: "idea_create", Limit: dailyLimit, Window: 24 * time.Hour}

type Service struct {
	Pool     *pgxpool.Pool
	Repo     Repo
	Slugs    *identity.SlugService
	Authz    acl.Authorizer
	Audit    audit.Service
	Lim      *ratelimit.Limiter
	Registry registry.Repo
	Kick     func()
}

func (s *Service) kick() {
	if s.Kick != nil {
		s.Kick()
	}
}

func runes(s string) int { return utf8.RuneCountInString(s) }

func validate(title, body, category string, fields map[string]string) {
	switch n := runes(title); {
	case n == 0:
		fields["title"] = "required"
	case n < 5:
		fields["title"] = "too_short"
	case n > 120:
		fields["title"] = "too_long"
	}
	switch n := runes(body); {
	case n == 0:
		fields["body"] = "required"
	case n < 20:
		fields["body"] = "too_short"
	case n > 5000:
		fields["body"] = "too_long"
	}
	if !categories[category] {
		fields["category"] = "invalid_value"
	}
}

// Create adds an idea (status submitted). At most 5 ideas per user per 24 hours.
func (s *Service) Create(ctx context.Context, subj acl.Subject, req CreateReq) (Idea, error) {
	if err := s.Authz.Require(&subj, acl.PermIdeaCreate, acl.Resource{}); err != nil {
		return Idea{}, err
	}
	req.Title, req.Body = strings.TrimSpace(req.Title), strings.TrimSpace(req.Body)
	fields := map[string]string{}
	validate(req.Title, req.Body, req.Category, fields)
	if !locales.Known(req.Locale) {
		fields["locale"] = "invalid_value"
	}
	if len(fields) > 0 {
		return Idea{}, apperr.Validation(fields)
	}
	if n, err := s.Repo.CountCreatedSince(ctx, s.Pool, subj.UserID, time.Now().Add(-24*time.Hour)); err != nil {
		return Idea{}, err
	} else if n >= dailyLimit {
		return Idea{}, apperr.RateLimited("idea_create", 3600)
	}
	if s.Lim != nil {
		if err := s.Lim.Check(ctx, ruleCreate, subj.UserID.String(), false); err != nil {
			return Idea{}, err
		}
	}
	i := Idea{ID: ids.New(), Author: Author{UserID: &subj.UserID}, Title: req.Title, Body: req.Body, Category: req.Category, Lang: req.Locale, Status: Submitted}
	err := db.WithTx(ctx, s.Pool, func(tx pgx.Tx) error {
		if err := s.Repo.Insert(ctx, tx, i); err != nil {
			return err
		}
		slug, err := s.Slugs.GenerateUnique(ctx, tx, identity.EntityIdea, req.Title)
		if err != nil {
			return err
		}
		if err := s.Slugs.Assign(ctx, tx, identity.EntityIdea, i.ID, slug); err != nil {
			return err
		}
		if err := s.Repo.InsertHistory(ctx, tx, i.ID, ids.New(), nil, Submitted, &subj.UserID, ""); err != nil {
			return err
		}
		if err := s.Registry.Upsert(ctx, tx, registry.FromIdea(i.ID, &subj.UserID)); err != nil {
			return err
		}
		return outbox.Add(ctx, tx, outbox.Event{Type: "idea.submitted", AggregateType: "idea", AggregateID: i.ID, Payload: map[string]any{"author_id": subj.UserID}})
	})
	if err != nil {
		return Idea{}, err
	}
	s.kick()
	return s.Repo.GetByID(ctx, s.Pool, i.ID, &subj.UserID)
}

// Edit lets the author change the idea while it is submitted or needs_info (needs_info -> under_review).
func (s *Service) Edit(ctx context.Context, subj acl.Subject, id uuid.UUID, req EditReq) (Idea, error) {
	if subj.Status != "active" {
		return Idea{}, apperr.ErrForbidden
	}
	err := db.WithTx(ctx, s.Pool, func(tx pgx.Tx) error {
		st, author, err := s.Repo.LockStatus(ctx, tx, id)
		if err != nil {
			return err
		}
		if author == nil || *author != subj.UserID {
			return apperr.ErrForbidden
		}
		if st != Submitted && st != NeedsInfo {
			return apperr.Conflict("not_editable")
		}
		cur, err := s.Repo.GetByID(ctx, tx, id, nil)
		if err != nil {
			return err
		}
		title, body, cat := cur.Title, cur.Body, cur.Category
		if req.Title != nil {
			title = strings.TrimSpace(*req.Title)
		}
		if req.Body != nil {
			body = strings.TrimSpace(*req.Body)
		}
		if req.Category != nil {
			cat = *req.Category
		}
		fields := map[string]string{}
		validate(title, body, cat, fields)
		if len(fields) > 0 {
			return apperr.Validation(fields)
		}
		if err := s.Repo.UpdateContent(ctx, tx, id, title, body, cat); err != nil {
			return err
		}
		if st == NeedsInfo {
			from := NeedsInfo
			if err := s.Repo.UpdateStatus(ctx, tx, id, NeedsInfo, UnderReview, nil); err != nil {
				return err
			}
			return s.Repo.InsertHistory(ctx, tx, id, ids.New(), &from, UnderReview, &subj.UserID, "edited by author")
		}
		return nil
	})
	if err != nil {
		return Idea{}, err
	}
	return s.Repo.GetByID(ctx, s.Pool, id, &subj.UserID)
}

// SetStatus applies a validated transition (moderator/admin).
func (s *Service) SetStatus(ctx context.Context, subj acl.Subject, ip netip.Addr, id uuid.UUID, to Status, note string, duplicateOf *uuid.UUID) (Idea, error) {
	if err := s.Authz.Require(&subj, acl.PermIdeaSetStatus, acl.Resource{}); err != nil {
		return Idea{}, err
	}
	note = strings.TrimSpace(note)
	if runes(note) > 500 {
		return Idea{}, apperr.Field("note", "too_long")
	}
	if (to == Declined || to == Duplicate) && runes(note) < 10 {
		return Idea{}, apperr.Field("note", "too_short")
	}
	err := db.WithTx(ctx, s.Pool, func(tx pgx.Tx) error {
		from, _, err := s.Repo.LockStatus(ctx, tx, id)
		if err != nil {
			return err
		}
		if !CanTransition(from, to) {
			return apperr.Conflict("invalid_transition")
		}
		var dup *uuid.UUID
		if to == Duplicate {
			if duplicateOf == nil {
				return apperr.Field("duplicate_of_id", "required")
			}
			if *duplicateOf == id {
				return apperr.Field("duplicate_of_id", "invalid_value")
			}
			main, err := s.Repo.GetByID(ctx, tx, *duplicateOf, nil)
			if err != nil || main.Status == Duplicate {
				return apperr.Field("duplicate_of_id", "invalid_value")
			}
			dup = duplicateOf
		}
		if err := s.Repo.UpdateStatus(ctx, tx, id, from, to, dup); err != nil {
			return err
		}
		if err := s.Repo.InsertHistory(ctx, tx, id, ids.New(), &from, to, &subj.UserID, note); err != nil {
			return err
		}
		if err := outbox.Add(ctx, tx, outbox.Event{Type: "idea.status_changed", AggregateType: "idea", AggregateID: id,
			Payload: map[string]any{"from": from, "to": to, "actor_id": subj.UserID}}); err != nil {
			return err
		}
		return s.Audit.Record(ctx, tx, audit.Input{ActorID: &subj.UserID, Action: audit.ActionIdeaStatus, TargetType: "idea", TargetID: id.String(),
			Meta: map[string]any{"from": from, "to": to}, IP: ip})
	})
	if err != nil {
		return Idea{}, err
	}
	s.kick()
	return s.Repo.GetByID(ctx, s.Pool, id, &subj.UserID)
}

// Reply sets the team reply.
func (s *Service) Reply(ctx context.Context, subj acl.Subject, ip netip.Addr, id uuid.UUID, text string) (Idea, error) {
	if err := s.Authz.Require(&subj, acl.PermIdeaReply, acl.Resource{}); err != nil {
		return Idea{}, err
	}
	text = strings.TrimSpace(text)
	if n := runes(text); n < 1 {
		return Idea{}, apperr.Field("text", "required")
	} else if n > 2000 {
		return Idea{}, apperr.Field("text", "too_long")
	}
	if _, err := s.Repo.GetByID(ctx, s.Pool, id, nil); err != nil {
		return Idea{}, err
	}
	err := db.WithTx(ctx, s.Pool, func(tx pgx.Tx) error {
		if err := s.Repo.SetReply(ctx, tx, id, text, subj.UserID); err != nil {
			return err
		}
		return s.Audit.Record(ctx, tx, audit.Input{ActorID: &subj.UserID, Action: audit.ActionIdeaReply, TargetType: "idea", TargetID: id.String(), IP: ip})
	})
	if err != nil {
		return Idea{}, err
	}
	return s.Repo.GetByID(ctx, s.Pool, id, &subj.UserID)
}

func (s *Service) voteCheck(ctx context.Context, subj acl.Subject, id uuid.UUID) error {
	if subj.Status != "active" {
		return apperr.ErrForbidden
	}
	i, err := s.Repo.GetByID(ctx, s.Pool, id, nil)
	if err != nil {
		return err
	}
	if i.Author.UserID != nil && *i.Author.UserID == subj.UserID {
		return apperr.Conflict("own_idea")
	}
	if i.Status.Terminal() {
		return apperr.Conflict("closed")
	}
	return nil
}

// Vote is idempotent: repeating it does not change the counter.
func (s *Service) Vote(ctx context.Context, subj acl.Subject, id uuid.UUID) error {
	if err := s.voteCheck(ctx, subj, id); err != nil {
		return err
	}
	return db.WithTx(ctx, s.Pool, func(tx pgx.Tx) error {
		ok, err := s.Repo.InsertVote(ctx, tx, id, subj.UserID)
		if err != nil || !ok {
			return err
		}
		return s.Repo.AddVotesCount(ctx, tx, id, 1)
	})
}

func (s *Service) Unvote(ctx context.Context, subj acl.Subject, id uuid.UUID) error {
	if subj.Status != "active" {
		return apperr.ErrForbidden
	}
	if _, err := s.Repo.GetByID(ctx, s.Pool, id, nil); err != nil {
		return err
	}
	return db.WithTx(ctx, s.Pool, func(tx pgx.Tx) error {
		ok, err := s.Repo.DeleteVote(ctx, tx, id, subj.UserID)
		if err != nil || !ok {
			return err
		}
		return s.Repo.AddVotesCount(ctx, tx, id, -1)
	})
}

func (s *Service) List(ctx context.Context, viewer *acl.Subject, f ListFilter, cur pagination.Cursor, limit int) ([]Idea, string, error) {
	var vid *uuid.UUID
	if viewer != nil {
		vid = &viewer.UserID
	}
	items, err := s.Repo.List(ctx, s.Pool, f, cur, limit+1, vid)
	if err != nil {
		return nil, "", err
	}
	page, more := pagination.Page(items, limit)
	next := ""
	if more {
		last := page[len(page)-1]
		c := pagination.Cursor{T: last.CreatedAt, ID: last.ID}
		if f.Sort == "top" {
			v := int64(last.VotesCount)
			c.V = &v
		}
		next = pagination.Encode(c)
	}
	return page, next, nil
}

func (s *Service) GetBySlug(ctx context.Context, viewer *acl.Subject, slug string) (Idea, []HistoryItem, string, error) {
	id, canonical, err := s.Slugs.Resolve(ctx, s.Pool, identity.EntityIdea, slug)
	if err != nil {
		return Idea{}, nil, "", err
	}
	var vid *uuid.UUID
	if viewer != nil {
		vid = &viewer.UserID
	}
	i, err := s.Repo.GetByID(ctx, s.Pool, id, vid)
	if err != nil {
		return Idea{}, nil, "", err
	}
	h, err := s.Repo.ListHistory(ctx, s.Pool, id)
	return i, h, canonical, err
}

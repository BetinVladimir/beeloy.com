package messenger

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"beeloy.com/backend/internal/acl"
	"beeloy.com/backend/internal/apperr"
	"beeloy.com/backend/internal/platform/db"
	"beeloy.com/backend/internal/platform/ids"
	"beeloy.com/backend/internal/platform/outbox"
	"beeloy.com/backend/internal/platform/pagination"
	"beeloy.com/backend/internal/platform/ratelimit"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

const (
	maxBody         = 4000
	requestMessages = 3 // messages the initiator may send before the recipient answers
	massThreshold   = 5 // same text to this many different conversations within an hour
)

var (
	ruleSend      = ratelimit.Rule{Name: "msg_send", Limit: 30, Window: time.Minute}
	ruleNewYoung  = ratelimit.Rule{Name: "conv_new_young", Limit: 5, Window: 24 * time.Hour}
	ruleNewNormal = ratelimit.Rule{Name: "conv_new", Limit: 20, Window: 24 * time.Hour}
	urlRe         = regexp.MustCompile(`(?i)https?://|www\.`)
)

type Service struct {
	Pool  *pgxpool.Pool
	Repo  Repo
	Authz acl.Authorizer
	RDB   *redis.Client
	Lim   *ratelimit.Limiter
	Kick  func()
}

func (s *Service) kick() {
	if s.Kick != nil {
		s.Kick()
	}
}

func cleanBody(s string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' {
			return -1
		}
		return r
	}, s))
}

func (s *Service) accountAge(ctx context.Context, id uuid.UUID) (time.Duration, error) {
	var created time.Time
	if err := s.Pool.QueryRow(ctx, `SELECT created_at FROM users WHERE id=$1`, id).Scan(&created); err != nil {
		return 0, err
	}
	return time.Since(created), nil
}

// StartConversation opens (or returns) a dialog with any active user. New dialogs start as a request and are rate limited.
func (s *Service) StartConversation(ctx context.Context, subj acl.Subject, peerID uuid.UUID) (Conversation, bool, error) {
	if err := s.Authz.Require(&subj, acl.PermMessageSend, acl.Resource{}); err != nil {
		return Conversation{}, false, err
	}
	if peerID == subj.UserID {
		return Conversation{}, false, apperr.Field("user_id", "invalid_value")
	}
	st, err := s.Repo.PeerStatus(ctx, s.Pool, peerID)
	if err != nil || st != "active" {
		return Conversation{}, false, apperr.Field("user_id", "invalid_value")
	}
	if blocked, err := s.Repo.IsBlocked(ctx, s.Pool, subj.UserID, peerID); err != nil {
		return Conversation{}, false, err
	} else if blocked {
		return Conversation{}, false, apperr.Forbidden("blocked")
	}
	if c, err := s.Repo.FindDirect(ctx, s.Pool, subj.UserID, peerID); err == nil {
		return c, false, nil
	} else if !errors.Is(err, apperr.ErrNotFound) {
		return Conversation{}, false, err
	}
	age, err := s.accountAge(ctx, subj.UserID)
	if err != nil {
		return Conversation{}, false, err
	}
	rule := ruleNewNormal
	if age < 24*time.Hour {
		rule = ruleNewYoung
	}
	if err := s.Lim.Check(ctx, rule, subj.UserID.String(), true); err != nil {
		return Conversation{}, false, err
	}
	return s.Repo.GetOrCreateDirect(ctx, s.Pool, subj.UserID, peerID)
}

// Send is idempotent per (conversation, sender, client_id).
func (s *Service) Send(ctx context.Context, subj acl.Subject, convID, clientID uuid.UUID, rawBody string) (Message, bool, error) {
	if err := s.Authz.Require(&subj, acl.PermMessageSend, acl.Resource{}); err != nil {
		return Message{}, false, err
	}
	conv, err := s.Repo.GetForMember(ctx, s.Pool, convID, subj.UserID)
	if err != nil {
		return Message{}, false, err
	}
	// retry of an already stored message: answer before any limit is touched
	if m, err := s.Repo.FindByClientID(ctx, s.Pool, convID, subj.UserID, clientID); err == nil {
		return m, false, nil
	}
	body := cleanBody(rawBody)
	switch n := utf8.RuneCountInString(body); {
	case n == 0:
		return Message{}, false, apperr.Field("body", "required")
	case n > maxBody:
		return Message{}, false, apperr.Field("body", "too_long")
	}
	if err := s.Lim.Check(ctx, ruleSend, subj.UserID.String(), true); err != nil {
		return Message{}, false, err
	}
	if blocked, err := s.Repo.IsBlocked(ctx, s.Pool, subj.UserID, conv.Peer.UserID); err != nil {
		return Message{}, false, err
	} else if blocked {
		return Message{}, false, apperr.Forbidden("blocked")
	}
	if st, err := s.Repo.PeerStatus(ctx, s.Pool, conv.Peer.UserID); err != nil || st != "active" {
		return Message{}, false, apperr.Forbidden("recipient_unavailable")
	}
	age, err := s.accountAge(ctx, subj.UserID)
	if err != nil {
		return Message{}, false, err
	}
	// links from a new account to someone who has never written back look like spam
	if age < 24*time.Hour && urlRe.MatchString(body) {
		if n, err := s.Repo.CountMessagesBy(ctx, s.Pool, convID, conv.Peer.UserID); err != nil {
			return Message{}, false, err
		} else if n == 0 {
			return Message{}, false, apperr.Field("body", "links_not_allowed")
		}
	}
	// mass-mailing guard: the same text to many different conversations
	sum := sha256.Sum256([]byte(strings.ToLower(body)))
	key := "dup:" + subj.UserID.String() + ":" + hex.EncodeToString(sum[:8])
	pipe := s.RDB.TxPipeline()
	pipe.SAdd(ctx, key, convID.String())
	pipe.ExpireNX(ctx, key, time.Hour)
	card := pipe.SCard(ctx, key)
	if _, err := pipe.Exec(ctx); err != nil {
		return Message{}, false, apperr.ErrUnavailable.Wrap(err)
	}
	if card.Val() >= massThreshold {
		return Message{}, false, apperr.RateLimited("mass messaging", 3600)
	}

	var out Message
	created := false
	err = db.WithTx(ctx, s.Pool, func(tx pgx.Tx) error {
		status, initiator, err := s.Repo.LockConversation(ctx, tx, convID)
		if err != nil {
			return err
		}
		if m, err := s.Repo.FindByClientID(ctx, tx, convID, subj.UserID, clientID); err == nil {
			out = m
			return nil
		}
		// the state may have changed while limits were checked: block and recipient state are re-read under the lock
		if blocked, err := s.Repo.IsBlocked(ctx, tx, subj.UserID, conv.Peer.UserID); err != nil {
			return err
		} else if blocked {
			return apperr.Forbidden("blocked")
		}
		if st, err := s.Repo.PeerStatus(ctx, tx, conv.Peer.UserID); err != nil || st != "active" {
			return apperr.Forbidden("recipient_unavailable")
		}
		iAmInitiator := initiator == subj.UserID
		switch {
		case status == "declined" && iAmInitiator:
			return apperr.Forbidden("conversation_declined")
		case status == "request" && iAmInitiator:
			n, err := s.Repo.CountMessagesBy(ctx, tx, convID, subj.UserID)
			if err != nil {
				return err
			}
			if n >= requestMessages {
				return apperr.Conflict("request_pending")
			}
		case status != "accepted" && !iAmInitiator: // an answer accepts the conversation
			if _, err := s.Repo.SetStatus(ctx, tx, convID, status, "accepted"); err != nil {
				return err
			}
			if err := outbox.Add(ctx, tx, outbox.Event{Type: "conversation.status_changed", AggregateType: "conversation", AggregateID: convID,
				Payload: map[string]any{"status": "accepted", "actor_id": subj.UserID, "conversation_id": convID, "peer_id": conv.Peer.UserID}}); err != nil {
				return err
			}
		}
		seq, err := s.Repo.NextSeq(ctx, tx, convID)
		if err != nil {
			return err
		}
		m := Message{ID: ids.New(), ConversationID: convID, Seq: seq, SenderID: &subj.UserID, ClientID: clientID, Body: body}
		if err := s.Repo.InsertMessage(ctx, tx, m); err != nil {
			return err
		}
		if _, err := s.Repo.SetReadSeq(ctx, tx, convID, subj.UserID, seq); err != nil {
			return err
		}
		if err := outbox.Add(ctx, tx, outbox.Event{Type: "message.created", AggregateType: "conversation", AggregateID: convID,
			Payload: map[string]any{"message_id": m.ID, "sender_id": subj.UserID, "recipient_id": conv.Peer.UserID, "seq": seq, "conversation_id": convID}}); err != nil {
			return err
		}
		out, created = m, true
		return nil
	})
	if err != nil {
		if db.IsUniqueViolation(err, "") { // lost a race on the idempotency key
			if m, e := s.Repo.FindByClientID(ctx, s.Pool, convID, subj.UserID, clientID); e == nil {
				return m, false, nil
			}
		}
		return Message{}, false, err
	}
	if created {
		s.kick()
		out.CreatedAt = time.Now()
		if m, e := s.Repo.GetMessage(ctx, s.Pool, out.ID); e == nil {
			out = m
		}
	}
	return out, created, nil
}

func (s *Service) History(ctx context.Context, subj acl.Subject, convID uuid.UUID, before, after *int64, limit int) ([]Message, error) {
	if _, err := s.Repo.GetForMember(ctx, s.Pool, convID, subj.UserID); err != nil {
		return nil, err
	}
	return s.Repo.ListMessages(ctx, s.Pool, convID, before, after, limit)
}

func (s *Service) MarkRead(ctx context.Context, subj acl.Subject, convID uuid.UUID, seq int64) error {
	conv, err := s.Repo.GetForMember(ctx, s.Pool, convID, subj.UserID)
	if err != nil {
		return err
	}
	if seq < 0 {
		return apperr.Field("seq", "invalid_value")
	}
	return db.WithTx(ctx, s.Pool, func(tx pgx.Tx) error {
		adv, err := s.Repo.SetReadSeq(ctx, tx, convID, subj.UserID, seq)
		if err != nil || !adv {
			return err
		}
		if seq > conv.LastSeq {
			seq = conv.LastSeq
		}
		defer s.kick()
		return outbox.Add(ctx, tx, outbox.Event{Type: "message.read", AggregateType: "conversation", AggregateID: convID,
			Payload: map[string]any{"reader_id": subj.UserID, "peer_id": conv.Peer.UserID, "read_seq": seq, "conversation_id": convID}})
	})
}

func (s *Service) Conversations(ctx context.Context, subj acl.Subject, folder string, cur pagination.Cursor, limit int) ([]Conversation, string, error) {
	if folder != "" && folder != "inbox" && folder != "requests" && folder != "sent" {
		return nil, "", apperr.ErrBadRequest.WithMessage("bad folder")
	}
	items, err := s.Repo.List(ctx, s.Pool, subj.UserID, folder, cur, limit+1)
	if err != nil {
		return nil, "", err
	}
	page, more := pagination.Page(items, limit)
	next := ""
	if more {
		last := page[len(page)-1]
		t := last.LastMessageAt
		if t == nil {
			z := time.Time{}
			t = &z
		}
		next = pagination.Encode(pagination.Cursor{T: *t, ID: last.ID})
	}
	return page, next, nil
}

// Respond lets the recipient accept or decline a request; repeating the same decision is harmless.
func (s *Service) Respond(ctx context.Context, subj acl.Subject, convID uuid.UUID, accept bool) error {
	if err := s.Authz.Require(&subj, acl.PermMessageSend, acl.Resource{}); err != nil {
		return err
	}
	conv, err := s.Repo.GetForMember(ctx, s.Pool, convID, subj.UserID)
	if err != nil {
		return err
	}
	if conv.InitiatedByMe {
		return apperr.ErrForbidden
	}
	to := "declined"
	if accept {
		to = "accepted"
	}
	if conv.Status == to {
		return nil
	}
	err = db.WithTx(ctx, s.Pool, func(tx pgx.Tx) error {
		ok, err := s.Repo.SetStatus(ctx, tx, convID, conv.Status, to)
		if err != nil || !ok {
			return err
		}
		return outbox.Add(ctx, tx, outbox.Event{Type: "conversation.status_changed", AggregateType: "conversation", AggregateID: convID,
			Payload: map[string]any{"status": to, "actor_id": subj.UserID, "conversation_id": convID, "peer_id": conv.Peer.UserID}})
	})
	if err == nil {
		s.kick()
	}
	return err
}

func (s *Service) BlockUser(ctx context.Context, subj acl.Subject, target uuid.UUID) error {
	if subj.Status != "active" {
		return apperr.ErrForbidden
	}
	if target == subj.UserID {
		return apperr.Field("user_id", "invalid_value")
	}
	if _, err := s.Repo.PeerStatus(ctx, s.Pool, target); err != nil {
		return err
	}
	return s.Repo.Block(ctx, s.Pool, subj.UserID, target)
}

func (s *Service) UnblockUser(ctx context.Context, subj acl.Subject, target uuid.UUID) error {
	if subj.Status != "active" {
		return apperr.ErrForbidden
	}
	return s.Repo.Unblock(ctx, s.Pool, subj.UserID, target)
}

func (s *Service) SetMuted(ctx context.Context, subj acl.Subject, convID uuid.UUID, muted bool) error {
	return s.Repo.SetMuted(ctx, s.Pool, convID, subj.UserID, muted)
}

func (s *Service) Counters(ctx context.Context, subj acl.Subject) (total, requests int64, err error) {
	if total, err = s.Repo.UnreadTotal(ctx, s.Pool, subj.UserID); err != nil {
		return
	}
	requests, err = s.Repo.PendingRequests(ctx, s.Pool, subj.UserID)
	return
}

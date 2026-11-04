// Package messenger implements 1:1 conversations with message requests, history, read cursors, blocks and SSE delivery.
package messenger

import (
	"context"
	"time"

	"beeloy.com/backend/internal/apperr"
	"beeloy.com/backend/internal/platform/db"
	"beeloy.com/backend/internal/platform/ids"
	"beeloy.com/backend/internal/platform/pagination"
	"github.com/google/uuid"
)

type PeerInfo struct {
	UserID      uuid.UUID `json:"user_id"`
	Nickname    string    `json:"nickname"`
	Slug        string    `json:"slug"`
	AvatarURL   *string   `json:"avatar_url"`
	BlockedByMe bool      `json:"blocked_by_me"`
}

type Conversation struct {
	ID                 uuid.UUID  `json:"id"`
	Status             string     `json:"status"` // request | accepted | declined
	InitiatedByMe      bool       `json:"initiated_by_me"`
	Peer               PeerInfo   `json:"peer"`
	LastSeq            int64      `json:"last_seq"`
	LastMessageAt      *time.Time `json:"last_message_at"`
	UnreadCount        int64      `json:"unread_count"`
	LastMessagePreview string     `json:"last_message_preview"`
	Muted              bool       `json:"muted"`
}

type Message struct {
	ID             uuid.UUID  `json:"id"`
	ConversationID uuid.UUID  `json:"conversation_id"`
	Seq            int64      `json:"seq"`
	SenderID       *uuid.UUID `json:"sender_id"`
	ClientID       uuid.UUID  `json:"client_id"`
	Body           string     `json:"body"`
	Deleted        bool       `json:"deleted"`
	CreatedAt      time.Time  `json:"created_at"`
}

type Repo struct{}

func order(a, b uuid.UUID) (low, high uuid.UUID) {
	if a.String() < b.String() {
		return a, b
	}
	return b, a
}

const convSelect = `SELECT c.id, c.status, (c.initiator_id = $1), p.id, p.nickname, COALESCE(ps.slug,''), p.avatar_url,
	EXISTS(SELECT 1 FROM user_blocks b WHERE b.blocker_id = $1 AND b.blocked_id = p.id),
	c.last_seq, c.last_message_at, GREATEST(c.last_seq - m.last_read_seq, 0), COALESCE((SELECT left(x.body, 80) FROM messages x WHERE x.conversation_id = c.id AND x.deleted_at IS NULL ORDER BY x.seq DESC LIMIT 1), ''), m.muted
	FROM conversations c
	JOIN conversation_members m ON m.conversation_id = c.id AND m.user_id = $1
	JOIN users p ON p.id = CASE WHEN c.user_low = $1 THEN c.user_high ELSE c.user_low END
	LEFT JOIN slugs ps ON ps.entity_type = 'user' AND ps.entity_id = p.id AND ps.is_current`

func scanConv(r interface{ Scan(...any) error }) (Conversation, error) {
	var c Conversation
	err := r.Scan(&c.ID, &c.Status, &c.InitiatedByMe, &c.Peer.UserID, &c.Peer.Nickname, &c.Peer.Slug, &c.Peer.AvatarURL, &c.Peer.BlockedByMe,
		&c.LastSeq, &c.LastMessageAt, &c.UnreadCount, &c.LastMessagePreview, &c.Muted)
	return c, err
}

// FindDirect returns the conversation of two users as seen by `me` (ErrNotFound if none).
func (Repo) FindDirect(ctx context.Context, q db.DBTX, me, peer uuid.UUID) (Conversation, error) {
	low, high := order(me, peer)
	c, err := scanConv(q.QueryRow(ctx, convSelect+` WHERE c.user_low = $2 AND c.user_high = $3`, me, low, high))
	if db.IsNoRows(err) {
		return c, apperr.ErrNotFound
	}
	return c, err
}

// GetOrCreateDirect creates a conversation with status 'request' (initiator = initiator) or returns the existing one.
func (r Repo) GetOrCreateDirect(ctx context.Context, q db.DBTX, initiator, peer uuid.UUID) (Conversation, bool, error) {
	low, high := order(initiator, peer)
	id := ids.New()
	tag, err := q.Exec(ctx, `INSERT INTO conversations (id, user_low, user_high, initiator_id) VALUES ($1,$2,$3,$4) ON CONFLICT (user_low, user_high) DO NOTHING`,
		id, low, high, initiator)
	if err != nil {
		return Conversation{}, false, err
	}
	created := tag.RowsAffected() == 1
	if created {
		if _, err := q.Exec(ctx, `INSERT INTO conversation_members (conversation_id, user_id) VALUES ($1,$2),($1,$3)`, id, low, high); err != nil {
			return Conversation{}, false, err
		}
	}
	c, err := r.FindDirect(ctx, q, initiator, peer)
	return c, created, err
}

func (Repo) GetForMember(ctx context.Context, q db.DBTX, convID, userID uuid.UUID) (Conversation, error) {
	c, err := scanConv(q.QueryRow(ctx, convSelect+` WHERE c.id = $2`, userID, convID))
	if db.IsNoRows(err) {
		return c, apperr.ErrNotFound
	}
	return c, err
}

// LockConversation locks the conversation row for the duration of the transaction.
func (Repo) LockConversation(ctx context.Context, q db.DBTX, convID uuid.UUID) (status string, initiator uuid.UUID, err error) {
	err = q.QueryRow(ctx, `SELECT status, initiator_id FROM conversations WHERE id = $1 FOR UPDATE`, convID).Scan(&status, &initiator)
	if db.IsNoRows(err) {
		err = apperr.ErrNotFound
	}
	return
}

func (Repo) List(ctx context.Context, q db.DBTX, userID uuid.UUID, folder string, cur pagination.Cursor, limit int) ([]Conversation, error) {
	var where string
	switch folder {
	case "requests":
		where = `c.status = 'request' AND c.initiator_id <> $1`
	case "sent":
		where = `c.status IN ('request','declined') AND c.initiator_id = $1`
	default:
		where = `c.status = 'accepted'`
	}
	var curT *time.Time
	if !cur.IsZero() {
		curT = &cur.T
	}
	rows, err := q.Query(ctx, convSelect+` WHERE `+where+` AND ($2::timestamptz IS NULL OR (COALESCE(c.last_message_at, c.created_at), c.id) < ($2, $3))
		ORDER BY COALESCE(c.last_message_at, c.created_at) DESC, c.id DESC LIMIT $4`, userID, curT, cur.ID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Conversation{}
	for rows.Next() {
		c, err := scanConv(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (Repo) NextSeq(ctx context.Context, q db.DBTX, convID uuid.UUID) (int64, error) {
	var seq int64
	err := q.QueryRow(ctx, `UPDATE conversations SET last_seq = last_seq + 1, last_message_at = now() WHERE id = $1 RETURNING last_seq`, convID).Scan(&seq)
	return seq, err
}

func (Repo) InsertMessage(ctx context.Context, q db.DBTX, m Message) error {
	_, err := q.Exec(ctx, `INSERT INTO messages (id, conversation_id, seq, sender_id, client_id, body) VALUES ($1,$2,$3,$4,$5,$6)`,
		m.ID, m.ConversationID, m.Seq, m.SenderID, m.ClientID, m.Body)
	return err
}

const msgCols = `id, conversation_id, seq, sender_id, client_id, CASE WHEN deleted_at IS NULL THEN body ELSE '' END, deleted_at IS NOT NULL, created_at`

func scanMsg(r interface{ Scan(...any) error }) (Message, error) {
	var m Message
	err := r.Scan(&m.ID, &m.ConversationID, &m.Seq, &m.SenderID, &m.ClientID, &m.Body, &m.Deleted, &m.CreatedAt)
	return m, err
}

func (Repo) FindByClientID(ctx context.Context, q db.DBTX, convID, sender, clientID uuid.UUID) (Message, error) {
	m, err := scanMsg(q.QueryRow(ctx, `SELECT `+msgCols+` FROM messages WHERE conversation_id=$1 AND sender_id=$2 AND client_id=$3`, convID, sender, clientID))
	if db.IsNoRows(err) {
		return m, apperr.ErrNotFound
	}
	return m, err
}

func (Repo) GetMessage(ctx context.Context, q db.DBTX, id uuid.UUID) (Message, error) {
	m, err := scanMsg(q.QueryRow(ctx, `SELECT `+msgCols+` FROM messages WHERE id=$1`, id))
	if db.IsNoRows(err) {
		return m, apperr.ErrNotFound
	}
	return m, err
}

// ListMessages returns messages in ascending seq order. before: the newest `limit` messages with seq < before;
// after: messages with seq > after (oldest first).
func (Repo) ListMessages(ctx context.Context, q db.DBTX, convID uuid.UUID, before, after *int64, limit int) ([]Message, error) {
	var rows interface {
		Next() bool
		Scan(...any) error
		Err() error
		Close()
	}
	var err error
	switch {
	case after != nil:
		rows, err = q.Query(ctx, `SELECT `+msgCols+` FROM messages WHERE conversation_id=$1 AND seq > $2 ORDER BY seq ASC LIMIT $3`, convID, *after, limit)
	case before != nil:
		rows, err = q.Query(ctx, `SELECT * FROM (SELECT `+msgCols+` FROM messages WHERE conversation_id=$1 AND seq < $2 ORDER BY seq DESC LIMIT $3) t ORDER BY seq ASC`, convID, *before, limit)
	default:
		rows, err = q.Query(ctx, `SELECT * FROM (SELECT `+msgCols+` FROM messages WHERE conversation_id=$1 ORDER BY seq DESC LIMIT $2) t ORDER BY seq ASC`, convID, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Message{}
	for rows.Next() {
		m, err := scanMsg(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// SetReadSeq moves the cursor forward only and never beyond last_seq; reports whether it advanced.
func (Repo) SetReadSeq(ctx context.Context, q db.DBTX, convID, userID uuid.UUID, seq int64) (bool, error) {
	tag, err := q.Exec(ctx, `UPDATE conversation_members m SET last_read_seq = LEAST($3, c.last_seq)
		FROM conversations c WHERE c.id = m.conversation_id AND m.conversation_id = $1 AND m.user_id = $2 AND LEAST($3, c.last_seq) > m.last_read_seq`, convID, userID, seq)
	return tag.RowsAffected() == 1, err
}

func (Repo) IsBlocked(ctx context.Context, q db.DBTX, a, b uuid.UUID) (bool, error) {
	var ok bool
	err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM user_blocks WHERE (blocker_id=$1 AND blocked_id=$2) OR (blocker_id=$2 AND blocked_id=$1))`, a, b).Scan(&ok)
	return ok, err
}

func (Repo) Block(ctx context.Context, q db.DBTX, blocker, blocked uuid.UUID) error {
	_, err := q.Exec(ctx, `INSERT INTO user_blocks (blocker_id, blocked_id) VALUES ($1,$2) ON CONFLICT DO NOTHING`, blocker, blocked)
	return err
}

func (Repo) Unblock(ctx context.Context, q db.DBTX, blocker, blocked uuid.UUID) error {
	_, err := q.Exec(ctx, `DELETE FROM user_blocks WHERE blocker_id=$1 AND blocked_id=$2`, blocker, blocked)
	return err
}

func (Repo) SetMuted(ctx context.Context, q db.DBTX, convID, userID uuid.UUID, muted bool) error {
	tag, err := q.Exec(ctx, `UPDATE conversation_members SET muted=$3 WHERE conversation_id=$1 AND user_id=$2`, convID, userID, muted)
	if err == nil && tag.RowsAffected() == 0 {
		return apperr.ErrNotFound
	}
	return err
}

// SetStatus changes the conversation status if it is currently `from`.
func (Repo) SetStatus(ctx context.Context, q db.DBTX, convID uuid.UUID, from, to string) (bool, error) {
	tag, err := q.Exec(ctx, `UPDATE conversations SET status=$3, decided_at=now() WHERE id=$1 AND status=$2`, convID, from, to)
	return tag.RowsAffected() == 1, err
}

func (Repo) CountMessagesBy(ctx context.Context, q db.DBTX, convID, sender uuid.UUID) (int, error) {
	var n int
	err := q.QueryRow(ctx, `SELECT count(*) FROM messages WHERE conversation_id=$1 AND sender_id=$2`, convID, sender).Scan(&n)
	return n, err
}

// UnreadTotal sums unread messages of accepted, non-muted conversations.
func (Repo) UnreadTotal(ctx context.Context, q db.DBTX, userID uuid.UUID) (int64, error) {
	var n int64
	err := q.QueryRow(ctx, `SELECT COALESCE(sum(GREATEST(c.last_seq - m.last_read_seq, 0)), 0) FROM conversation_members m
		JOIN conversations c ON c.id = m.conversation_id WHERE m.user_id=$1 AND NOT m.muted AND c.status = 'accepted'`, userID).Scan(&n)
	return n, err
}

func (Repo) PendingRequests(ctx context.Context, q db.DBTX, userID uuid.UUID) (int64, error) {
	var n int64
	err := q.QueryRow(ctx, `SELECT count(*) FROM conversations c JOIN conversation_members m ON m.conversation_id=c.id AND m.user_id=$1
		WHERE c.status='request' AND c.initiator_id <> $1 AND NOT m.muted`, userID).Scan(&n)
	return n, err
}

func (Repo) PeerStatus(ctx context.Context, q db.DBTX, id uuid.UUID) (string, error) {
	var st string
	err := q.QueryRow(ctx, `SELECT status FROM users WHERE id=$1`, id).Scan(&st)
	if db.IsNoRows(err) {
		return "", apperr.ErrNotFound
	}
	return st, err
}

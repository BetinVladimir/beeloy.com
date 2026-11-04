package ideas

import (
	"context"
	"time"

	"beeloy.com/backend/internal/apperr"
	"beeloy.com/backend/internal/platform/db"
	"beeloy.com/backend/internal/platform/pagination"
	"github.com/google/uuid"
)

type Repo struct{}

const ideaSelect = `SELECT i.id, COALESCE(s.slug,''), i.author_id, COALESCE(u.nickname,''), COALESCE(us.slug,''), COALESCE(u.status,'deleted'),
	i.title, i.body, i.category, i.lang, i.status, i.team_reply, i.team_reply_at,
	(SELECT ds.slug FROM slugs ds WHERE ds.entity_type='idea' AND ds.entity_id=i.duplicate_of AND ds.is_current),
	i.votes_count, i.created_at, i.updated_at,
	(CASE WHEN $1::uuid IS NULL THEN false ELSE EXISTS(SELECT 1 FROM idea_votes v WHERE v.idea_id=i.id AND v.user_id=$1) END)
	FROM ideas i
	LEFT JOIN slugs s ON s.entity_type='idea' AND s.entity_id=i.id AND s.is_current
	LEFT JOIN users u ON u.id=i.author_id
	LEFT JOIN slugs us ON us.entity_type='user' AND us.entity_id=u.id AND us.is_current`

func scanIdea(r interface{ Scan(...any) error }) (Idea, error) {
	var i Idea
	var ustatus string
	err := r.Scan(&i.ID, &i.Slug, &i.Author.UserID, &i.Author.Nickname, &i.Author.Slug, &ustatus, &i.Title, &i.Body, &i.Category, &i.Lang, &i.Status,
		&i.TeamReply, &i.TeamReplyAt, &i.DuplicateOfSlug, &i.VotesCount, &i.CreatedAt, &i.UpdatedAt, &i.ViewerVoted)
	if i.Author.UserID == nil || ustatus == "deleted" {
		i.Author = Author{Nickname: "Deleted user"}
	}
	return i, err
}

func (Repo) Insert(ctx context.Context, q db.DBTX, i Idea) error {
	_, err := q.Exec(ctx, `INSERT INTO ideas (id, author_id, title, body, category, lang, status) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		i.ID, i.Author.UserID, i.Title, i.Body, i.Category, i.Lang, i.Status)
	return err
}

func (Repo) GetByID(ctx context.Context, q db.DBTX, id uuid.UUID, viewer *uuid.UUID) (Idea, error) {
	i, err := scanIdea(q.QueryRow(ctx, ideaSelect+` WHERE i.id = $2`, viewer, id))
	if db.IsNoRows(err) {
		return i, apperr.ErrNotFound
	}
	return i, err
}

func (Repo) List(ctx context.Context, q db.DBTX, f ListFilter, cur pagination.Cursor, limit int, viewer *uuid.UUID) ([]Idea, error) {
	var curT *time.Time
	var curV *int64
	if !cur.IsZero() {
		curT, curV = &cur.T, cur.V
	}
	where := ` WHERE ($2::text IS NULL OR i.status = $2) AND ($3::text IS NULL OR i.category = $3) AND ($4::uuid IS NULL OR i.author_id = $4)`
	var st *string
	if f.Status != nil {
		s := string(*f.Status)
		st = &s
	}
	var rows interface {
		Next() bool
		Scan(...any) error
		Err() error
		Close()
	}
	var err error
	if f.Sort == "top" {
		var v int64
		if curV != nil {
			v = *curV
		}
		rows, err = q.Query(ctx, ideaSelect+where+` AND ($5::timestamptz IS NULL OR (i.votes_count, i.created_at, i.id) < ($6, $5, $7))
			ORDER BY i.votes_count DESC, i.created_at DESC, i.id DESC LIMIT $8`, viewer, st, f.Category, f.AuthorID, curT, v, cur.ID, limit)
	} else {
		rows, err = q.Query(ctx, ideaSelect+where+` AND ($5::timestamptz IS NULL OR (i.created_at, i.id) < ($5, $6))
			ORDER BY i.created_at DESC, i.id DESC LIMIT $7`, viewer, st, f.Category, f.AuthorID, curT, cur.ID, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Idea{}
	for rows.Next() {
		i, err := scanIdea(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

func (Repo) UpdateContent(ctx context.Context, q db.DBTX, id uuid.UUID, title, body, category string) error {
	_, err := q.Exec(ctx, `UPDATE ideas SET title=$2, body=$3, category=$4, updated_at=now() WHERE id=$1`, id, title, body, category)
	return err
}

// UpdateStatus is an optimistic transition: it only applies if the idea is still in `from`.
func (Repo) UpdateStatus(ctx context.Context, q db.DBTX, id uuid.UUID, from, to Status, duplicateOf *uuid.UUID) error {
	tag, err := q.Exec(ctx, `UPDATE ideas SET status=$3, duplicate_of=$4, updated_at=now() WHERE id=$1 AND status=$2`, id, from, to, duplicateOf)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return apperr.Conflict("invalid_transition")
	}
	return nil
}

func (Repo) InsertHistory(ctx context.Context, q db.DBTX, id uuid.UUID, hid uuid.UUID, from *Status, to Status, actor *uuid.UUID, note string) error {
	_, err := q.Exec(ctx, `INSERT INTO idea_status_history (id, idea_id, from_status, to_status, actor_id, note) VALUES ($1,$2,$3,$4,$5,$6)`, hid, id, from, to, actor, note)
	return err
}

func (Repo) ListHistory(ctx context.Context, q db.DBTX, id uuid.UUID) ([]HistoryItem, error) {
	rows, err := q.Query(ctx, `SELECT from_status, to_status, note, created_at FROM idea_status_history WHERE idea_id=$1 ORDER BY created_at, id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []HistoryItem{}
	for rows.Next() {
		var h HistoryItem
		if err := rows.Scan(&h.From, &h.To, &h.Note, &h.At); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func (Repo) SetReply(ctx context.Context, q db.DBTX, id uuid.UUID, text string, by uuid.UUID) error {
	_, err := q.Exec(ctx, `UPDATE ideas SET team_reply=$2, team_reply_by=$3, team_reply_at=now(), updated_at=now() WHERE id=$1`, id, text, by)
	return err
}

func (Repo) InsertVote(ctx context.Context, q db.DBTX, ideaID, userID uuid.UUID) (bool, error) {
	tag, err := q.Exec(ctx, `INSERT INTO idea_votes (idea_id, user_id) VALUES ($1,$2) ON CONFLICT DO NOTHING`, ideaID, userID)
	return tag.RowsAffected() == 1, err
}

func (Repo) DeleteVote(ctx context.Context, q db.DBTX, ideaID, userID uuid.UUID) (bool, error) {
	tag, err := q.Exec(ctx, `DELETE FROM idea_votes WHERE idea_id=$1 AND user_id=$2`, ideaID, userID)
	return tag.RowsAffected() == 1, err
}

func (Repo) AddVotesCount(ctx context.Context, q db.DBTX, ideaID uuid.UUID, delta int) error {
	_, err := q.Exec(ctx, `UPDATE ideas SET votes_count = GREATEST(votes_count + $2, 0) WHERE id=$1`, ideaID, delta)
	return err
}

func (Repo) CountCreatedSince(ctx context.Context, q db.DBTX, userID uuid.UUID, since time.Time) (int, error) {
	var n int
	err := q.QueryRow(ctx, `SELECT count(*) FROM ideas WHERE author_id=$1 AND created_at >= $2`, userID, since).Scan(&n)
	return n, err
}

// LockForUpdate serializes concurrent status changes of one idea inside a transaction.
func (Repo) LockStatus(ctx context.Context, q db.DBTX, id uuid.UUID) (Status, *uuid.UUID, error) {
	var st Status
	var author *uuid.UUID
	err := q.QueryRow(ctx, `SELECT status, author_id FROM ideas WHERE id=$1 FOR UPDATE`, id).Scan(&st, &author)
	if db.IsNoRows(err) {
		return st, nil, apperr.ErrNotFound
	}
	return st, author, err
}

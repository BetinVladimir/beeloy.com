package privacy

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"beeloy.com/backend/internal/platform/s3store"
	"github.com/google/uuid"
)

const journalPrefix = "deletion-journal/"

// Journal records completed account purges outside PostgreSQL so they can be re-applied after a restore from an older backup (04 §5).
type Journal interface {
	// Record is idempotent: recording the same user twice is not an error.
	Record(ctx context.Context, userID uuid.UUID, at time.Time) error
	List(ctx context.Context) ([]JournalEntry, error)
}

type JournalEntry struct {
	UserID   uuid.UUID `json:"user_id"`
	PurgedAt time.Time `json:"purged_at"`
}

// S3Journal keeps one AES-256-GCM encrypted object per purged user. It holds only the pseudonymous user id and the time.
type S3Journal struct {
	Store s3store.ObjectStore
	aead  cipher.AEAD
}

// NewS3Journal takes a 64-character hex key (32 bytes).
func NewS3Journal(store s3store.ObjectStore, hexKey string) (*S3Journal, error) {
	key, err := hex.DecodeString(strings.TrimSpace(hexKey))
	if err != nil || len(key) != 32 {
		return nil, errors.New("deletion journal key must be 64 hex characters")
	}
	blk, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(blk)
	if err != nil {
		return nil, err
	}
	return &S3Journal{Store: store, aead: g}, nil
}

func (j *S3Journal) Record(ctx context.Context, userID uuid.UUID, at time.Time) error {
	raw, err := json.Marshal(JournalEntry{UserID: userID, PurgedAt: at.UTC()})
	if err != nil {
		return err
	}
	nonce := make([]byte, j.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	// The object key is the (authenticated) additional data, so an entry cannot be moved to another key.
	key := journalPrefix + userID.String() + ".enc"
	sealed := j.aead.Seal(nonce, nonce, raw, []byte(key))
	if err := j.Store.PutIfAbsent(ctx, key, sealed, "application/octet-stream"); err != nil && !errors.Is(err, s3store.ErrConflict) {
		return err
	}
	return nil
}

func (j *S3Journal) List(ctx context.Context) ([]JournalEntry, error) {
	var keys []string
	if err := j.Store.List(ctx, journalPrefix, func(o s3store.Object) error {
		keys = append(keys, o.Key)
		return nil
	}); err != nil {
		return nil, err
	}
	out := make([]JournalEntry, 0, len(keys))
	for _, k := range keys {
		data, err := j.Store.Get(ctx, k, 4096)
		if err != nil {
			return nil, fmt.Errorf("journal %s: %w", k, err)
		}
		ns := j.aead.NonceSize()
		if len(data) <= ns {
			return nil, fmt.Errorf("journal %s: truncated", k)
		}
		raw, err := j.aead.Open(nil, data[:ns], data[ns:], []byte(k))
		if err != nil {
			return nil, fmt.Errorf("journal %s: %w", k, err)
		}
		var e JournalEntry
		if err := json.Unmarshal(raw, &e); err != nil {
			return nil, fmt.Errorf("journal %s: %w", k, err)
		}
		out = append(out, e)
	}
	return out, nil
}

// ReplayDeletions re-applies every journaled purge to the (restored) database before traffic is opened.
// Users that do not exist or are already deleted are skipped by PurgeUser; the first hard error stops the run.
func (s *Service) ReplayDeletions(ctx context.Context) (applied int, err error) {
	if s.Journal == nil {
		return 0, errors.New("deletion journal is not configured")
	}
	entries, err := s.Journal.List(ctx)
	if err != nil {
		return 0, err
	}
	for _, e := range entries {
		var live bool
		if err := s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1 AND status <> 'deleted')`, e.UserID).Scan(&live); err != nil {
			return applied, err
		}
		if !live {
			continue
		}
		if err := s.purge(ctx, e.UserID, false); err != nil {
			return applied, fmt.Errorf("replay %s: %w", e.UserID, err)
		}
		applied++
	}
	return applied, nil
}

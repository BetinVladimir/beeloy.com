package identity

import (
	"context"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"beeloy.com/backend/internal/apperr"
	"beeloy.com/backend/internal/audit"
	"beeloy.com/backend/internal/platform/crypto"
	"beeloy.com/backend/internal/platform/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/text/unicode/norm"
)

type EntityType string

const (
	EntityUser    EntityType = "user"
	EntityIdea    EntityType = "idea"
	EntityArticle EntityType = "article"
)

const userSlugChangeInterval = 30 * 24 * time.Hour

var slugRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

var reserved = map[string]struct{}{}

func init() {
	for _, s := range strings.Fields(`admin administrator api app auth about beeloy blog contact cookies edit editor help ideas login logout
		messages mod moderator me new news org people privacy root rules search settings sitemap static support system terms u user users www
		null undefined anonymous staff team official`) {
		reserved[s] = struct{}{}
	}
}

func Normalize(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

func limits(t EntityType) (min, max int) {
	if t == EntityUser {
		return 3, 32
	}
	return 3, 80
}

// Validate checks format, length and (for users) the reserved list. The error carries field "slug".
func Validate(t EntityType, s string) error {
	s = Normalize(s)
	min, max := limits(t)
	n := utf8.RuneCountInString(s)
	switch {
	case s == "":
		return apperr.Field("slug", "required")
	case !slugRe.MatchString(s):
		return apperr.Field("slug", "invalid_format")
	case n < min:
		return apperr.Field("slug", "too_short")
	case n > max:
		return apperr.Field("slug", "too_long")
	}
	if t == EntityUser {
		if _, bad := reserved[s]; bad {
			return apperr.Field("slug", "reserved")
		}
	}
	return nil
}

var translit = map[rune]string{
	'а': "a", 'б': "b", 'в': "v", 'г': "g", 'д': "d", 'е': "e", 'ё': "e", 'ж': "zh", 'з': "z", 'и': "i", 'й': "y", 'к': "k", 'л': "l", 'м': "m",
	'н': "n", 'о': "o", 'п': "p", 'р': "r", 'с': "s", 'т': "t", 'у': "u", 'ф': "f", 'х': "h", 'ц': "ts", 'ч': "ch", 'ш': "sh", 'щ': "sht",
	'ъ': "a", 'ы': "y", 'ь': "", 'э': "e", 'ю': "yu", 'я': "ya", 'є': "ye", 'і': "i", 'ї': "yi", 'ґ': "g",
	'ß': "ss", 'ł': "l", 'đ': "d", 'ø': "o", 'æ': "ae", 'œ': "oe", 'þ': "th", 'ð': "d", 'ħ': "h", 'ı': "i",
}

// Slugify turns arbitrary text into a candidate slug (ASCII, lowercase, hyphen separated). Unknown scripts are dropped.
func Slugify(s string) string {
	var b strings.Builder
	for _, r := range norm.NFD.String(strings.ToLower(s)) {
		switch {
		case unicode.Is(unicode.Mn, r):
			continue
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
		case r == ' ' || r == '_' || r == '-' || r == '.':
			b.WriteByte('-')
		default:
			if t, ok := translit[r]; ok {
				b.WriteString(t)
			}
		}
	}
	out := regexp.MustCompile(`-+`).ReplaceAllString(b.String(), "-")
	return strings.Trim(out, "-")
}

type SlugService struct {
	Pool  *pgxpool.Pool
	Audit audit.Service
}

// IsAvailable: valid, not reserved and never used (current or historical) by anyone.
func (s *SlugService) IsAvailable(ctx context.Context, q db.DBTX, t EntityType, slug string) (bool, error) {
	slug = Normalize(slug)
	if Validate(t, slug) != nil {
		return false, nil
	}
	var exists bool
	err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM slugs WHERE entity_type = $1 AND slug = $2)`, t, slug).Scan(&exists)
	return !exists, err
}

// GenerateUnique builds a free slug from source: base, base-2 … base-20, then base-<4 random chars>.
func (s *SlugService) GenerateUnique(ctx context.Context, q db.DBTX, t EntityType, source string) (string, error) {
	min, max := limits(t)
	base := Slugify(source)
	if len(base) < min {
		base = map[EntityType]string{EntityUser: "member", EntityIdea: "idea", EntityArticle: "article"}[t]
	}
	cut := func(b string, room int) string {
		if len(b) > max-room {
			b = strings.Trim(b[:max-room], "-")
		}
		return b
	}
	try := func(c string) (bool, error) {
		if Validate(t, c) != nil {
			return false, nil
		}
		return s.IsAvailable(ctx, q, t, c)
	}
	if ok, err := try(cut(base, 0)); err != nil || ok {
		return cut(base, 0), err
	}
	for i := 2; i <= 20; i++ {
		c := cut(base, 4) + "-" + strconv.Itoa(i)
		if ok, err := try(c); err != nil || ok {
			return c, err
		}
	}
	for i := 0; i < 20; i++ {
		tok, err := crypto.RandomToken(6)
		if err != nil {
			return "", err
		}
		r := strings.ToLower(regexp.MustCompile(`[^a-z0-9]`).ReplaceAllString(tok, ""))
		if len(r) < 4 {
			continue
		}
		c := cut(base, 5) + "-" + r[:4]
		if ok, err := try(c); err != nil || ok {
			return c, err
		}
	}
	return "", apperr.ErrInternal.WithMessage("cannot generate slug")
}

// GenerateForUser: an explicitly chosen nickname is slugified; otherwise the slug is user-<8 random [a-z0-9]>.
func (s *SlugService) GenerateForUser(ctx context.Context, q db.DBTX, nickname string, explicit bool) (string, error) {
	if explicit {
		if base := Slugify(nickname); len(base) >= 3 {
			if _, bad := reserved[base]; !bad {
				return s.GenerateUnique(ctx, q, EntityUser, nickname)
			}
		}
	}
	for i := 0; i < 20; i++ {
		tok, err := crypto.RandomToken(12)
		if err != nil {
			return "", err
		}
		r := regexp.MustCompile(`[^a-z0-9]`).ReplaceAllString(strings.ToLower(tok), "")
		if len(r) < 8 {
			continue
		}
		c := "user-" + r[:8]
		if ok, err := s.IsAvailable(ctx, q, EntityUser, c); err != nil || ok {
			return c, err
		}
	}
	return "", apperr.ErrInternal.WithMessage("cannot generate slug")
}

// Assign makes slug the current slug of the entity. A slug used by another entity is "slug_taken";
// an entity may re-activate its own old slug.
func (s *SlugService) Assign(ctx context.Context, q db.DBTX, t EntityType, entityID uuid.UUID, slug string) error {
	slug = Normalize(slug)
	if err := Validate(t, slug); err != nil {
		return err
	}
	var owner uuid.UUID
	err := q.QueryRow(ctx, `SELECT entity_id FROM slugs WHERE entity_type = $1 AND slug = $2`, t, slug).Scan(&owner)
	if err == nil && owner != entityID {
		return apperr.Conflict("slug_taken")
	}
	if err != nil && !db.IsNoRows(err) {
		return err
	}
	if _, err := q.Exec(ctx, `UPDATE slugs SET is_current = false WHERE entity_type = $1 AND entity_id = $2 AND is_current AND slug <> $3`, t, entityID, slug); err != nil {
		return err
	}
	if err == nil { // re-activation of own old slug
		_, e := q.Exec(ctx, `UPDATE slugs SET is_current = true, created_at = now() WHERE entity_type = $1 AND slug = $2`, t, slug)
		return e
	}
	_, e := q.Exec(ctx, `INSERT INTO slugs (entity_type, slug, entity_id) VALUES ($1,$2,$3)`, t, slug, entityID)
	if db.IsUniqueViolation(e, "") {
		return apperr.Conflict("slug_taken")
	}
	return e
}

// Resolve returns the entity id and the canonical (current) slug for any current or historical slug.
func (s *SlugService) Resolve(ctx context.Context, q db.DBTX, t EntityType, slug string) (uuid.UUID, string, error) {
	var id uuid.UUID
	var canonical string
	err := q.QueryRow(ctx, `SELECT s.entity_id, c.slug FROM slugs s
		JOIN slugs c ON c.entity_type = s.entity_type AND c.entity_id = s.entity_id AND c.is_current
		WHERE s.entity_type = $1 AND s.slug = $2`, t, Normalize(slug)).Scan(&id, &canonical)
	if db.IsNoRows(err) {
		return uuid.Nil, "", apperr.ErrNotFound
	}
	return id, canonical, err
}

// Current returns the current slug of an entity ("" if none).
func (s *SlugService) Current(ctx context.Context, q db.DBTX, t EntityType, id uuid.UUID) (string, error) {
	var slug string
	err := q.QueryRow(ctx, `SELECT slug FROM slugs WHERE entity_type = $1 AND entity_id = $2 AND is_current`, t, id).Scan(&slug)
	if db.IsNoRows(err) {
		return "", nil
	}
	return slug, err
}

// ChangeUserSlug lets a user change their slug: the first change is free; later ones at most once per 30 days.
func (s *SlugService) ChangeUserSlug(ctx context.Context, userID uuid.UUID, ip netip.Addr, newSlug string) error {
	newSlug = Normalize(newSlug)
	if err := Validate(EntityUser, newSlug); err != nil {
		return err
	}
	return db.WithTx(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "slug:"+userID.String()); err != nil {
			return err
		}
		var cur string
		var curSince, userSince time.Time
		var total int
		err := tx.QueryRow(ctx, `SELECT slug, created_at, (SELECT count(*) FROM slugs WHERE entity_type='user' AND entity_id=$1),
			(SELECT created_at FROM users WHERE id=$1) FROM slugs WHERE entity_type='user' AND entity_id=$1 AND is_current FOR UPDATE`, userID).Scan(&cur, &curSince, &total, &userSince)
		if err != nil && !db.IsNoRows(err) {
			return err
		}
		if cur == newSlug {
			return nil
		}
		firstFree := total == 1 && time.Since(userSince) < 24*time.Hour // one free manual change in the first 24 h
		if !firstFree && time.Since(curSince) < userSlugChangeInterval {
			return apperr.Conflict("slug_change_too_soon")
		}
		if err := s.Assign(ctx, tx, EntityUser, userID, newSlug); err != nil {
			return err
		}
		return s.Audit.Record(ctx, tx, audit.Input{ActorID: &userID, Action: audit.ActionSlugChange, TargetType: "user", TargetID: userID.String(), IP: ip})
	})
}

package contentsync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"beeloy.com/backend/internal/articles"
	"beeloy.com/backend/internal/audit"
	"beeloy.com/backend/internal/identity"
	"beeloy.com/backend/internal/locales"
	"beeloy.com/backend/internal/platform/db"
	"beeloy.com/backend/internal/platform/outbox"
	"beeloy.com/backend/internal/platform/s3store"
	"beeloy.com/backend/internal/registry"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/errgroup"
)

// ArticleSyncer registers articles found in S3 (part B). It NEVER publishes: new articles become in_review,
// existing ones keep status, visibility and published_rev.
type ArticleSyncer struct {
	Src      s3store.ObjectStore
	Pool     *pgxpool.Pool
	Store    *Store
	Repo     articles.Repo
	Slugs    *identity.SlugService
	Registry registry.Repo
	Kick     func()
	Log      *slog.Logger
}

// ImmutabilityError: a registered revision file was replaced in the bucket. The article keeps serving what the database knows.
type ImmutabilityError struct {
	Key    string
	Rev    int
	Locale string
}

func (e *ImmutabilityError) Error() string {
	return fmt.Sprintf("immutability violated: rev-%d/%s changed in the bucket", e.Rev, e.Locale)
}

// alertIntegrity tells the operator: an ERROR log line and an audit event (visible to admins in the audit log),
// at most one event per article and file per day so that the one-minute retry does not flood the log.
func (a *ArticleSyncer) alertIntegrity(ctx context.Context, id uuid.UUID, iv *ImmutabilityError) {
	a.log().Error("content-sync: INTEGRITY VIOLATION, revision file changed in the bucket", "article", id, "key", iv.Key)
	var recent bool
	if err := a.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM audit_events WHERE action=$1 AND target_id=$2 AND meta->>'key'=$3 AND created_at > now() - interval '24 hours')`,
		audit.ActionContentIntegrity, id.String(), iv.Key).Scan(&recent); err != nil || recent {
		return
	}
	if err := (audit.Service{}).Record(ctx, a.Pool, audit.Input{Action: audit.ActionContentIntegrity, TargetType: "article", TargetID: id.String(), Meta: map[string]any{"key": iv.Key, "rev": iv.Rev, "locale": iv.Locale}}); err != nil {
		a.log().Error("content-sync: cannot record the integrity event", "err", err)
	}
}

func fingerprint(objs []s3store.Object) string {
	lines := make([]string, len(objs))
	for i, o := range objs {
		lines[i] = o.Key + "|" + o.ETag
	}
	sort.Strings(lines)
	h := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(h[:])
}

func (a *ArticleSyncer) log() *slog.Logger {
	if a.Log != nil {
		return a.Log
	}
	return slog.Default()
}

// RunOnce processes every article whose S3 objects changed since the last run.
func (a *ArticleSyncer) RunOnce(ctx context.Context) (processed, failed int, err error) {
	release, ok, err := a.Store.TryLock(ctx, LockArticles)
	if err != nil {
		return 0, 0, err
	}
	if !ok {
		return 0, 0, nil
	}
	defer release()
	run, err := a.Store.StartRun(ctx)
	if err != nil {
		return 0, 0, err
	}
	defer func() {
		res := ResultOK
		ee := ""
		if err != nil || failed > 0 {
			res = ResultFailed
			if err != nil {
				ee = err.Error()
			}
		}
		_ = a.Store.FinishRun(context.Background(), run, res, fmt.Sprintf("articles:%d", processed), ee)
	}()

	byArticle := map[uuid.UUID][]s3store.Object{}
	if err = a.Src.List(ctx, "articles/", func(o s3store.Object) error {
		parts := strings.SplitN(o.Key, "/", 3)
		if len(parts) < 3 {
			return nil
		}
		id, e := uuid.Parse(parts[1])
		if e != nil {
			a.log().Warn("content-sync: ignoring key with bad article id", "key", o.Key)
			return nil
		}
		byArticle[id] = append(byArticle[id], o)
		return nil
	}); err != nil {
		return 0, 0, err
	}
	idsSorted := make([]uuid.UUID, 0, len(byArticle))
	for id := range byArticle {
		idsSorted = append(idsSorted, id)
	}
	sort.Slice(idsSorted, func(i, j int) bool { return idsSorted[i].String() < idsSorted[j].String() })

	for _, id := range idsSorted {
		if ctx.Err() != nil {
			return processed, failed, ctx.Err()
		}
		changed, e := a.syncOne(ctx, id, byArticle[id])
		switch {
		case e != nil:
			failed++
			a.log().Warn("content-sync: article skipped", "article", id, "err", e)
		case changed:
			processed++
		}
	}
	if processed > 0 && a.Kick != nil {
		a.Kick()
	}
	return processed, failed, nil
}

func (a *ArticleSyncer) syncOne(ctx context.Context, id uuid.UUID, objs []s3store.Object) (bool, error) {
	var hasManifest bool
	for _, o := range objs {
		if o.Key == articles.ManifestKey(id) {
			hasManifest = true
		}
	}
	if !hasManifest {
		return false, errors.New("manifest.json is missing")
	}
	fp := fingerprint(objs)
	if old, err := a.Repo.Fingerprint(ctx, a.Pool, id); err != nil {
		return false, err
	} else if old == fp {
		return false, nil
	}
	raw, err := a.Src.Get(ctx, articles.ManifestKey(id), articles.MaxManifestBytes)
	if err != nil {
		return false, fmt.Errorf("manifest: %w", err)
	}
	var m articles.Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return false, fmt.Errorf("manifest: %w", err)
	}
	if err := articles.ValidateManifest(m, id, locales.Known); err != nil {
		return false, fmt.Errorf("manifest: %w", err)
	}
	cat, err := a.Repo.GetCategoryByKey(ctx, a.Pool, m.CategoryKey)
	if err != nil {
		return false, fmt.Errorf("unknown category %q", m.CategoryKey)
	}

	// what the database already knows (revision files are immutable: their sha256 must still match)
	var dbRev int
	_ = a.Pool.QueryRow(ctx, `SELECT rev FROM articles WHERE id = $1`, id).Scan(&dbRev)
	if m.Rev < dbRev {
		return false, fmt.Errorf("manifest rev %d is older than the registered rev %d", m.Rev, dbRev)
	}
	knownSHA := map[string]string{}
	if revs, err := a.Repo.ListRevisions(ctx, a.Pool, id); err == nil {
		for _, r := range revs {
			knownSHA[fmt.Sprintf("%d/%s", r.Rev, r.Locale)] = r.SHA256
		}
	} else {
		return false, err
	}
	type job struct {
		rev    int
		locale string
	}
	var jobs []job
	for _, o := range objs {
		_, rev, loc, ok := articles.ParseLocaleKey(o.Key)
		if !ok {
			continue
		}
		if rev > m.Rev || !locales.Known(loc) {
			a.log().Warn("content-sync: ignoring file", "key", o.Key)
			continue
		}
		jobs = append(jobs, job{rev, loc})
	}
	type loaded struct {
		file articles.LocaleFile
		raw  []byte
	}
	results := make([]*loaded, len(jobs))
	eg, gctx := errgroup.WithContext(ctx)
	eg.SetLimit(downloadWorkers)
	for i, j := range jobs {
		eg.Go(func() error {
			b, err := a.Src.Get(gctx, articles.LocaleKey(id, j.rev, j.locale), articles.MaxLocaleFileBytes)
			if err != nil {
				return fmt.Errorf("read rev-%d/%s: %w", j.rev, j.locale, err)
			}
			sum := sha256.Sum256(b)
			if old, ok := knownSHA[fmt.Sprintf("%d/%s", j.rev, j.locale)]; ok {
				if old != hex.EncodeToString(sum[:]) {
					return &ImmutabilityError{Key: articles.LocaleKey(id, j.rev, j.locale), Rev: j.rev, Locale: j.locale}
				}
				return nil // unchanged, already registered
			}
			var f articles.LocaleFile
			if err := json.Unmarshal(b, &f); err != nil {
				return fmt.Errorf("invalid rev-%d/%s: %w", j.rev, j.locale, err)
			}
			if err := articles.ValidateLocaleFile(f, b, id, j.locale, j.rev); err != nil {
				return fmt.Errorf("invalid rev-%d/%s: %w", j.rev, j.locale, err)
			}
			results[i] = &loaded{f, b}
			return nil
		})
	}
	// any unreadable, invalid or altered file fails the whole article: the fingerprint is not stored, so the next run retries
	if err := eg.Wait(); err != nil {
		var iv *ImmutabilityError
		if errors.As(err, &iv) {
			a.alertIntegrity(ctx, id, iv)
		}
		return false, err
	}

	err = db.WithTx(ctx, a.Pool, func(tx pgx.Tx) error {
		if err := a.Slugs.Assign(ctx, tx, identity.EntityArticle, id, m.Slug); err != nil {
			return fmt.Errorf("slug %q: %w", m.Slug, err)
		}
		created, err := a.Repo.UpsertFromManifest(ctx, tx, m, cat.ID)
		if err != nil {
			return err
		}
		art, err := a.Repo.GetArticle(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := a.Registry.Upsert(ctx, tx, registry.FromArticle(id, art.AuthorID, string(art.Status), string(art.Visibility))); err != nil {
			return err
		}
		inserted := 0
		for _, r := range results {
			if r == nil {
				continue
			}
			sum := sha256.Sum256(r.raw)
			row := articles.RevisionRow{ArticleID: id, Rev: r.file.Rev, Locale: r.file.Locale, Origin: r.file.Origin, Model: r.file.Model,
				Title: r.file.Title, Summary: r.file.Summary, SHA256: hex.EncodeToString(sum[:]), Size: len(r.raw)}
			// existing revision rows are never replaced
			ok, err := a.Repo.UpsertRevision(ctx, tx, row, articles.SearchText(r.file), false)
			if err != nil {
				return err
			}
			if ok {
				inserted++
			}
		}
		if art.PublishedRev > 0 {
			if err := a.Repo.RefreshLive(ctx, tx, id); err != nil {
				return err
			}
		}
		if err := a.Repo.SetFingerprint(ctx, tx, id, fp); err != nil {
			return err
		}
		if created || inserted > 0 {
			return outbox.Add(ctx, tx, outbox.Event{Type: "article.revision_registered", AggregateType: "article", AggregateID: id,
				Payload: map[string]any{"rev": m.Rev, "source": "sync"}})
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	return true, nil
}

// Loop runs RunOnce every interval until ctx is cancelled.
func (a *ArticleSyncer) Loop(ctx context.Context, interval time.Duration) {
	for {
		if _, _, err := a.RunOnce(ctx); err != nil && ctx.Err() == nil {
			a.log().Error("content-sync: articles run failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

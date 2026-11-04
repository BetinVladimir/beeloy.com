package contentsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"beeloy.com/backend/internal/locales"
	"beeloy.com/backend/internal/platform/s3store"
	"golang.org/x/sync/errgroup"
)

const (
	uiPrefix        = "i18n/"
	manifestKey     = uiPrefix + "manifest.json"
	maxManifestUI   = 256 * 1024
	downloadWorkers = 6
	keepReleases    = 5
)

// Syncer synchronizes UI dictionaries (part A).
type Syncer struct {
	Src        s3store.ObjectStore
	Store      *Store
	Required   []string      // locale codes every release must contain
	RetryDelay time.Duration // base delay for S3 retries (default 1s)
	OnActivate func()        // optional (cache invalidation)
	Log        *slog.Logger
}

func (s *Syncer) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

func (s *Syncer) retry(ctx context.Context, fn func() error) error {
	d := s.RetryDelay
	if d == 0 {
		d = time.Second
	}
	var err error
	for i := 0; i < 3; i++ {
		if err = fn(); err == nil || errors.Is(err, s3store.ErrNotFound) || errors.Is(err, s3store.ErrTooLarge) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(d << i):
		}
	}
	return err
}

// RunOnce performs one synchronization. The active version never changes unless a complete valid release is activated.
func (s *Syncer) RunOnce(ctx context.Context) (res Result, version string, err error) {
	release, ok, err := s.Store.TryLock(ctx, LockUI)
	if err != nil {
		return ResultFailed, "", err
	}
	if !ok {
		return ResultNoop, "", nil
	}
	defer release()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	run, err := s.Store.StartRun(ctx)
	if err != nil {
		return ResultFailed, "", err
	}
	defer func() {
		v := version
		if v != "" {
			v = "ui:" + v
		}
		ee := ""
		if err != nil {
			ee = err.Error()
		}
		_ = s.Store.FinishRun(context.Background(), run, res, v, ee)
	}()

	var raw []byte
	if err = s.retry(ctx, func() (e error) { raw, e = s.Src.Get(ctx, manifestKey, maxManifestUI); return }); err != nil {
		return ResultFailed, "", fmt.Errorf("manifest: %w", err)
	}
	var m Manifest
	if err = json.Unmarshal(raw, &m); err != nil {
		return ResultFailed, "", fmt.Errorf("manifest: %w", err)
	}
	version = m.Version
	if err = ValidateManifest(m, s.requiredLocales()); err != nil {
		return ResultFailed, version, err
	}
	active, _, err := s.Store.CurrentActive(ctx)
	if err != nil {
		return ResultFailed, version, err
	}
	if m.Version == active {
		return ResultNoop, version, nil
	}
	st, found, err := s.Store.GetRelease(ctx, m.Version)
	if err != nil {
		return ResultFailed, version, err
	}
	if found {
		if st != "failed" && st != "staged" {
			s.log().Warn("content-sync: version already known, versions must not be reused", "version", m.Version, "status", st)
			return ResultNoop, version, nil
		}
		if err = s.Store.DeleteUnusable(ctx, m.Version); err != nil {
			return ResultFailed, version, err
		}
	}

	bundles := map[string]Bundle{}
	eg, gctx := errgroup.WithContext(ctx)
	eg.SetLimit(downloadWorkers)
	results := make([]Bundle, len(m.Locales))
	for i, it := range m.Locales {
		eg.Go(func() error {
			var b []byte
			if err := s.retry(gctx, func() (e error) { b, e = s.Src.Get(gctx, uiPrefix+it.File, it.Size); return }); err != nil {
				return fmt.Errorf("%s: %w", it.Code, err)
			}
			bundle, err := ValidateBundle(it, b)
			if err != nil {
				return err
			}
			results[i] = bundle
			return nil
		})
	}
	if err = eg.Wait(); err != nil {
		_ = s.Store.MarkFailed(ctx, m.Version, err.Error())
		return ResultFailed, version, err
	}
	for _, b := range results {
		bundles[b.Locale] = b
	}
	if problems := ValidateSet(m, bundles); len(problems) > 0 {
		err = fmt.Errorf("release %s is invalid: %s", m.Version, strings.Join(problems, "; "))
		_ = s.Store.MarkFailed(ctx, m.Version, err.Error())
		return ResultFailed, version, err
	}
	if err = s.Store.StageRelease(ctx, m, raw, bundles); err != nil {
		return ResultFailed, version, err
	}
	if err = s.Store.Activate(ctx, m.Version); err != nil {
		_ = s.Store.MarkFailed(ctx, m.Version, err.Error())
		return ResultFailed, version, err
	}
	if s.OnActivate != nil {
		s.OnActivate()
	}
	if e := s.Store.PruneOld(ctx, keepReleases); e != nil {
		s.log().Warn("content-sync: prune failed", "err", e)
	}
	s.log().Info("content-sync: dictionaries activated", "version", m.Version)
	return ResultOK, version, nil
}

func (s *Syncer) requiredLocales() []string {
	if len(s.Required) > 0 {
		return s.Required
	}
	return locales.Codes()
}

// Rollback re-activates a known release (superseded or active) without touching S3.
func (s *Syncer) Rollback(ctx context.Context, version string) error {
	st, found, err := s.Store.GetRelease(ctx, version)
	if err != nil {
		return err
	}
	if !found || (st != "superseded" && st != "active") {
		return fmt.Errorf("release %q cannot be rolled back to (status %q)", version, st)
	}
	if err := s.Store.Activate(ctx, version); err != nil {
		return err
	}
	if s.OnActivate != nil {
		s.OnActivate()
	}
	return nil
}

// Loop runs RunOnce every interval until ctx is cancelled (first run immediately).
func (s *Syncer) Loop(ctx context.Context, interval time.Duration) {
	for {
		if _, _, err := s.RunOnce(ctx); err != nil && ctx.Err() == nil {
			s.log().Error("content-sync: ui run failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

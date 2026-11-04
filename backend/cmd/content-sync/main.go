// Command content-sync synchronizes S3 content into PostgreSQL.
//
//	content-sync run                 UI dictionaries every SYNC_INTERVAL_UI, articles every SYNC_INTERVAL_ARTICLES
//	content-sync once ui|articles    one run (exit code 1 on failure)
//	content-sync rollback <version>  re-activate a known dictionary release
//	content-sync status              active dictionary version and the last runs
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"beeloy.com/backend/internal/config"
	"beeloy.com/backend/internal/contentsync"
	"beeloy.com/backend/internal/i18nstore"
	"beeloy.com/backend/internal/identity"
	"beeloy.com/backend/internal/locales"
	"beeloy.com/backend/internal/platform/db"
	"beeloy.com/backend/internal/platform/logx"
	"beeloy.com/backend/internal/platform/maintenance"
	"beeloy.com/backend/internal/platform/outbox"
	"beeloy.com/backend/internal/platform/redis"
	"beeloy.com/backend/internal/platform/s3store"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "content-sync:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: content-sync run | once ui|articles | seed-ui | rollback <version> | status")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	slog.SetDefault(logx.New(cfg.LogLevel))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	pool, err := db.NewPool(ctx, cfg.DatabaseURL, 5)
	if err != nil {
		return err
	}
	defer pool.Close()
	store := &contentsync.Store{Pool: pool}
	ui := &contentsync.Syncer{Store: store}

	needS3 := args[0] == "run" || args[0] == "once" || args[0] == "seed-ui"
	var art *contentsync.ArticleSyncer
	if needS3 {
		if err := cfg.RequireS3(); err != nil {
			return err
		}
		src, err := s3store.New(ctx, s3store.Config{Endpoint: cfg.S3.Endpoint, Region: cfg.S3.Region, Bucket: cfg.S3.Bucket, AccessKey: cfg.S3.AccessKey, SecretKey: cfg.S3.SecretKey, Prefix: cfg.S3.Prefix})
		if err != nil {
			return err
		}
		ui.Src = src
		art = &contentsync.ArticleSyncer{Src: src, Pool: pool, Store: store, Slugs: &identity.SlugService{Pool: pool}}
	}

	// SEED_UI_TEMPLATE=true (dev compose): publish the template release once if the bucket has no i18n manifest.
	if needS3 && os.Getenv("SEED_UI_TEMPLATE") == "true" {
		if ok, err := contentsync.SeedTemplate(ctx, ui.Src, i18nstore.FallbackEN(), locales.Source(), locales.Codes()); err != nil {
			return fmt.Errorf("seed: %w", err)
		} else if ok {
			slog.Info("content-sync: published the template UI release", "version", contentsync.SeedVersion)
		}
	}

	switch args[0] {
	case "status":
		v, _, err := store.CurrentActive(ctx)
		if err != nil {
			return err
		}
		runs, err := store.LastRuns(ctx, 5)
		if err != nil {
			return err
		}
		fmt.Printf("active dictionaries: %q\n", v)
		for _, r := range runs {
			res, ver, er := "-", "-", ""
			if r.Result != nil {
				res = *r.Result
			}
			if r.Version != nil {
				ver = *r.Version
			}
			if r.Error != nil {
				er = *r.Error
			}
			fmt.Printf("%s  %-6s %-24s %s\n", r.StartedAt.Format(time.RFC3339), res, ver, er)
		}
		return nil
	case "seed-ui":
		ok, err := contentsync.SeedTemplate(ctx, ui.Src, i18nstore.FallbackEN(), locales.Source(), locales.Codes())
		fmt.Printf("template release published: %v\n", ok)
		return err
	case "rollback":
		if len(args) < 2 {
			return errors.New("rollback needs a version")
		}
		return ui.Rollback(ctx, args[1])
	case "once":
		if len(args) < 2 {
			return errors.New("once needs ui or articles")
		}
		switch args[1] {
		case "ui":
			res, v, err := ui.RunOnce(ctx)
			fmt.Printf("ui: %s %s\n", res, v)
			return err
		case "articles":
			p, f, err := art.RunOnce(ctx)
			fmt.Printf("articles: processed=%d failed=%d\n", p, f)
			if err == nil && f > 0 {
				err = fmt.Errorf("%d articles failed", f)
			}
			return err
		}
		return fmt.Errorf("unknown target %q", args[1])
	case "run":
		rdb, err := redis.New(ctx, cfg.RedisURL)
		if err != nil {
			return err
		}
		defer rdb.Close()
		relay := outbox.NewRelay(pool, rdb)
		art.Kick = relay.Kick
		var healthy atomic.Int64 // unix time of the last successful run of either part
		started := time.Now()
		var wg sync.WaitGroup
		wg.Add(3)
		go func() { defer wg.Done(); relay.Run(ctx) }()
		go func() {
			defer wg.Done()
			loop(ctx, cfg.SyncIntervalUI, func() error {
				if maintenance.On(ctx, rdb) {
					return nil // write freeze for a consistent backup
				}
				_, _, err := ui.RunOnce(ctx)
				if err == nil {
					healthy.Store(time.Now().Unix())
				}
				return err
			})
		}()
		go func() {
			defer wg.Done()
			loop(ctx, cfg.SyncIntervalArticles, func() error {
				if maintenance.On(ctx, rdb) {
					return nil
				}
				_, _, err := art.RunOnce(ctx)
				if err == nil {
					healthy.Store(time.Now().Unix())
				}
				return err
			})
		}()
		mux := http.NewServeMux()
		mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
			last := time.Unix(healthy.Load(), 0)
			if time.Since(started) > 5*time.Minute && time.Since(last) > 3*cfg.SyncIntervalUI {
				http.Error(w, "stale", http.StatusServiceUnavailable)
				return
			}
			w.Write([]byte("ok"))
		})
		mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
			runs, _ := store.LastRuns(r.Context(), 10)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(runs)
		})
		srv := &http.Server{Addr: ":8081", Handler: mux, ReadHeaderTimeout: 5 * time.Second}
		go func() { _ = srv.ListenAndServe() }()
		<-ctx.Done()
		shCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shCtx)
		wg.Wait()
		return nil
	}
	return fmt.Errorf("unknown command %q", args[0])
}

func loop(ctx context.Context, every time.Duration, fn func() error) {
	for {
		if err := fn(); err != nil && ctx.Err() == nil {
			slog.Error("content-sync: run failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
	}
}

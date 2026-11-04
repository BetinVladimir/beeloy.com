// Command api runs the Beeloy HTTP API. Subcommands: bootstrap-admin --email <email>.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"beeloy.com/backend/internal/config"
	"beeloy.com/backend/internal/platform/db"
	"beeloy.com/backend/internal/platform/logx"
	pmail "beeloy.com/backend/internal/platform/mail"
	"beeloy.com/backend/internal/platform/outbox"
	"beeloy.com/backend/internal/platform/redis"
	"beeloy.com/backend/internal/platform/s3store"
	"beeloy.com/backend/internal/privacy"
	"beeloy.com/backend/internal/server"
)

// healthcheck is used by the container probe (the distroless image has no curl): GET /readyz on the local listener.
func healthcheck() error {
	addr := os.Getenv("HTTP_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}
	c := http.Client{Timeout: 2 * time.Second}
	resp, err := c.Get("http://" + addr + "/readyz")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("readyz: status %d", resp.StatusCode)
	}
	return nil
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		if err := healthcheck(); err != nil {
			fmt.Fprintln(os.Stderr, "healthcheck:", err)
			os.Exit(1)
		}
		return
	}
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "api:", err)
		os.Exit(1)
	}
}

func mailer(cfg *config.Config) pmail.Mailer {
	if cfg.SMTP.Host == "" {
		return pmail.LogMailer{LogBody: cfg.IsDev()}
	}
	plain := cfg.IsDev() && (cfg.SMTP.Host == "localhost" || cfg.SMTP.Host == "127.0.0.1" || cfg.SMTP.Host == "mailpit")
	return &pmail.SMTPMailer{Host: cfg.SMTP.Host, Port: cfg.SMTP.Port, User: cfg.SMTP.User, Pass: cfg.SMTP.Pass, From: cfg.SMTP.From, Plain: plain}
}

func run(args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	slog.SetDefault(logx.New(cfg.LogLevel))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := db.NewPool(ctx, cfg.DatabaseURL, cfg.DBMaxConns)
	if err != nil {
		return err
	}
	defer pool.Close()
	rdb, err := redis.New(ctx, cfg.RedisURL)
	if err != nil {
		return err
	}
	defer rdb.Close()

	deps := server.Deps{Cfg: cfg, Pool: pool, Redis: rdb, Mailer: mailer(cfg)}
	if cfg.S3.Endpoint != "" {
		if err := cfg.RequireS3(); err != nil {
			return err
		}
		st, err := s3store.New(ctx, s3store.Config{Endpoint: cfg.S3.Endpoint, Region: cfg.S3.Region, Bucket: cfg.S3.Bucket, AccessKey: cfg.S3.AccessKey, SecretKey: cfg.S3.SecretKey, Prefix: cfg.S3.Prefix})
		if err != nil {
			return err
		}
		deps.Store = st
	}
	if cfg.JournalS3.Endpoint != "" {
		jc := cfg.JournalS3
		js, err := s3store.New(ctx, s3store.Config{Endpoint: jc.Endpoint, Region: jc.Region, Bucket: jc.Bucket, AccessKey: jc.AccessKey, SecretKey: jc.SecretKey, Prefix: jc.Prefix})
		if err != nil {
			return err
		}
		if deps.Journal, err = privacy.NewS3Journal(js, cfg.DeletionJournalKey); err != nil {
			return err
		}
	}
	if cfg.AppEnv == "prod" && deps.Journal == nil {
		return errors.New("JOURNAL_S3_ENDPOINT, JOURNAL_S3_BUCKET and DELETION_JOURNAL_KEY (64 hex chars) are required in prod")
	}
	srv, err := server.Build(ctx, deps)
	if err != nil {
		return err
	}

	if len(args) > 0 {
		switch args[0] {
		case "bootstrap-admin":
			fs := flag.NewFlagSet("bootstrap-admin", flag.ExitOnError)
			email := fs.String("email", "", "administrator e-mail")
			_ = fs.Parse(args[1:])
			if *email == "" {
				return errors.New("--email is required")
			}
			if err := srv.BootstrapAdmin(ctx, *email); err != nil {
				return err
			}
			fmt.Println("administrator created; sign in with the e-mail code")
			return nil
		case "run-job":
			// Runs one background job once (operations, tests): api run-job PurgeDeletedAccounts
			if len(args) < 2 {
				return errors.New("run-job needs a job name")
			}
			ran, err := srv.Jobs.RunTask(ctx, args[1])
			fmt.Printf("job %s ran=%v\n", args[1], ran)
			return err
		case "replay-deletions":
			// Run after restoring PostgreSQL from a backup and before opening traffic (infra/OPERATIONS.md).
			if srv.Privacy.Journal == nil {
				return errors.New("JOURNAL_S3_* and DELETION_JOURNAL_KEY are required")
			}
			n, err := srv.Privacy.ReplayDeletions(ctx)
			if err != nil {
				return err
			}
			fmt.Printf("deletions re-applied: %d\n", n)
			return nil
		case "outbox":
			st, err := outbox.GetStatus(ctx, pool, rdb)
			if err != nil {
				return err
			}
			fmt.Printf("unpublished=%d oldest=%s stream=%d dead=%d\n", st.Unpublished, st.OldestAge.Round(time.Second), st.StreamLen, st.DeadLen)
			return nil
		default:
			return fmt.Errorf("unknown command %q", args[0])
		}
	}

	httpSrv := &http.Server{Addr: cfg.HTTPAddr, Handler: srv.Router, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 120 * time.Second}
	srv.StartBackground(ctx)
	metricsSrv := &http.Server{Addr: cfg.MetricsAddr, Handler: srv.MetricsHandler(), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = metricsSrv.ListenAndServe() }()
	defer metricsSrv.Close()
	errCh := make(chan error, 1)
	go func() { errCh <- httpSrv.ListenAndServe() }()
	slog.Info("api listening", "addr", cfg.HTTPAddr, "env", cfg.AppEnv)
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}
	slog.Info("shutting down")
	shCtx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	err = httpSrv.Shutdown(shCtx)
	srv.Wait()
	return err
}

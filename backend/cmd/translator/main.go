// Command translator translates articles and UI dictionaries with a local Ollama model (operator tool).
//
//	translator check
//	translator article --id <uuid> [--locales de,fr|all]
//	translator ui --source <en.json> --version <v> [--out <dir>] [--publish]
//
// Exit codes: 0 success, 1 at least one locale failed, 2 usage/configuration/connection error.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"beeloy.com/backend/internal/config"
	"beeloy.com/backend/internal/platform/s3store"
	"beeloy.com/backend/internal/translator"
)

func fail(code int, a ...any) {
	fmt.Fprintln(os.Stderr, a...)
	os.Exit(code)
}

func main() {
	if len(os.Args) < 2 {
		fail(2, "usage: translator check | article --id <uuid> [--locales ..] | ui --source <file> --version <v> [--out dir] [--publish]")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	cfg, err := loadConfig()
	if err != nil {
		fail(2, "translator:", err)
	}
	if err := cfg.RequireOllama(); err != nil {
		fail(2, "translator:", err)
	}
	client := translator.NewClient(cfg.Ollama.URL, cfg.Ollama.Model, cfg.Ollama.RequestTimeout)
	keep := glossary()
	tr := &translator.Translator{LLM: client, Model: cfg.Ollama.Model, Keep: keep, Log: func(f string, a ...any) { fmt.Printf(f+"\n", a...) }}

	openStore := func() s3store.ObjectStore {
		if err := cfg.RequireS3(); err != nil {
			fail(2, "translator:", err)
		}
		st, err := s3store.New(ctx, s3store.Config{Endpoint: cfg.S3.Endpoint, Region: cfg.S3.Region, Bucket: cfg.S3.Bucket, AccessKey: cfg.S3.AccessKey, SecretKey: cfg.S3.SecretKey, Prefix: cfg.S3.Prefix})
		if err != nil {
			fail(2, "translator:", err)
		}
		return st
	}

	switch os.Args[1] {
	case "check":
		if err := client.Ping(ctx); err != nil {
			fail(2, "translator:", err)
		}
		st := openStore()
		if err := st.List(ctx, "articles/", func(s3store.Object) error { return errStop }); err != nil && err != errStop {
			fail(2, "translator: S3:", err)
		}
		fmt.Println("ok: Ollama and S3 are reachable, model", cfg.Ollama.Model, "is installed")
	case "article":
		fs := flag.NewFlagSet("article", flag.ExitOnError)
		id := fs.String("id", "", "article id")
		loc := fs.String("locales", "all", "comma separated locales or all")
		_ = fs.Parse(os.Args[2:])
		if *id == "" {
			fail(2, "--id is required")
		}
		var targets []string
		if *loc != "all" {
			targets = strings.Split(*loc, ",")
		}
		res, err := tr.RunArticle(ctx, openStore(), *id, targets)
		if err != nil && len(res) == 0 {
			fail(2, "translator:", err)
		}
		os.Exit(report(res, err))
	case "ui":
		fs := flag.NewFlagSet("ui", flag.ExitOnError)
		src := fs.String("source", "../contracts/messages/en.json", "source dictionary")
		version := fs.String("version", "", "release version")
		out := fs.String("out", "./dist/i18n", "output directory")
		publish := fs.Bool("publish", false, "upload to S3 (manifest last)")
		loc := fs.String("locales", "all", "comma separated locales or all")
		_ = fs.Parse(os.Args[2:])
		if *version == "" {
			fail(2, "--version is required")
		}
		o := translator.UIOptions{SourcePath: *src, Version: *version, OutDir: *out, Publish: *publish}
		if *loc != "all" {
			o.Targets = strings.Split(*loc, ",")
		}
		if *publish {
			o.Store = openStore()
		}
		res, err := tr.RunUI(ctx, o)
		os.Exit(report(res, err))
	default:
		fail(2, "unknown command", os.Args[1])
	}
}

var errStop = fmt.Errorf("stop")

func report(res []translator.LocaleResult, err error) int {
	failed := 0
	for _, r := range res {
		fmt.Printf("%-4s %-10s %s\n", r.Locale, r.Status, r.Reason)
		if r.Status == translator.Failed {
			failed++
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "translator:", err)
		return 1
	}
	if failed > 0 {
		return 1
	}
	return 0
}

func glossary() []string {
	for _, p := range []string{"../contracts/glossary.json", "contracts/glossary.json"} {
		if b, err := os.ReadFile(p); err == nil {
			var g struct {
				Keep []string `json:"keep"`
			}
			if json.Unmarshal(b, &g) == nil {
				return g.Keep
			}
		}
	}
	return []string{"Beeloy"}
}

// loadConfig reads only what the translator needs (it does not require database settings).
func loadConfig() (*config.Config, error) {
	get := func(k string) string {
		if v := os.Getenv(k); v != "" {
			return v
		}
		switch k {
		case "APP_ENV":
			return "dev"
		case "PUBLIC_BASE_URL", "ALLOWED_ORIGINS":
			return "http://localhost"
		case "DATABASE_URL", "REDIS_URL":
			return "unused"
		case "OTP_HMAC_SECRET":
			return strings.Repeat("x", 32)
		}
		return ""
	}
	return config.LoadFrom(get)
}

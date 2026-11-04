// Package config loads and validates process configuration from environment variables.
package config

import (
	"errors"
	"fmt"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"
)

type SMTP struct {
	Host, User, Pass, From string
	Port                   int
}

type S3 struct {
	Endpoint, Region, Bucket, AccessKey, SecretKey, Prefix string
}

type Ollama struct {
	URL, Model     string
	RequestTimeout time.Duration
}

type Config struct {
	AppEnv         string
	HTTPAddr       string
	MetricsAddr    string
	PublicBaseURL  string
	AllowedOrigins []string

	DatabaseURL string
	DBMaxConns  int
	RedisURL    string

	OTPHMACSecret []byte
	SMTP          SMTP

	TermsVersion                           string
	GoogleClientID, GoogleClientSecret     string
	FacebookClientID, FacebookClientSecret string
	PrivacyVersion                         string

	LogLevel          string
	TrustedProxyCIDRs []netip.Prefix
	ProxySharedSecret string

	S3 S3
	// JournalS3 is a separate (backup) bucket for the encrypted deletion journal; DeletionJournalKey is 64 hex chars.
	JournalS3            S3
	DeletionJournalKey   string
	SyncIntervalUI       time.Duration
	SyncIntervalArticles time.Duration
	Ollama               Ollama
}

func (c *Config) IsDev() bool  { return c.AppEnv == "dev" }
func (c *Config) IsProd() bool { return c.AppEnv == "prod" }

// Load reads the process environment.
func Load() (*Config, error) { return LoadFrom(os.Getenv) }

// LoadFrom reads configuration through get (testable). It reports all problems at once.
func LoadFrom(get func(string) string) (*Config, error) {
	var errs []string
	req := func(key string) string {
		v := strings.TrimSpace(get(key))
		if v == "" {
			errs = append(errs, key+" is required")
		}
		return v
	}
	opt := func(key, def string) string {
		if v := strings.TrimSpace(get(key)); v != "" {
			return v
		}
		return def
	}
	dur := func(key, def string) time.Duration {
		v := opt(key, def)
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			errs = append(errs, key+" must be a positive duration")
		}
		return d
	}
	num := func(key string, def int) int {
		v := strings.TrimSpace(get(key))
		if v == "" {
			return def
		}
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			errs = append(errs, key+" must be a positive integer")
			return def
		}
		return n
	}

	c := &Config{}
	c.AppEnv = req("APP_ENV")
	switch c.AppEnv {
	case "", "dev", "staging", "prod":
	default:
		errs = append(errs, "APP_ENV must be dev, staging or prod")
	}
	c.HTTPAddr = opt("HTTP_ADDR", ":8080")
	c.MetricsAddr = opt("METRICS_ADDR", ":9090")
	c.PublicBaseURL = strings.TrimRight(req("PUBLIC_BASE_URL"), "/")
	for _, o := range strings.Split(req("ALLOWED_ORIGINS"), ",") {
		if o = strings.TrimRight(strings.TrimSpace(o), "/"); o != "" {
			c.AllowedOrigins = append(c.AllowedOrigins, o)
		}
	}
	c.DatabaseURL = req("DATABASE_URL")
	c.DBMaxConns = num("DB_MAX_CONNS", 20)
	c.RedisURL = req("REDIS_URL")

	secret := req("OTP_HMAC_SECRET")
	if secret != "" && len(secret) < 32 {
		errs = append(errs, "OTP_HMAC_SECRET must be at least 32 characters")
	}
	c.OTPHMACSecret = []byte(secret)

	dev := c.AppEnv == "dev"
	smtpReq := req
	if dev {
		smtpReq = func(k string) string { return strings.TrimSpace(get(k)) }
	}
	c.SMTP = SMTP{Host: smtpReq("SMTP_HOST"), User: strings.TrimSpace(get("SMTP_USER")), Pass: get("SMTP_PASS"), From: smtpReq("SMTP_FROM"), Port: num("SMTP_PORT", 587)}

	c.GoogleClientID, c.GoogleClientSecret = opt("GOOGLE_CLIENT_ID", ""), opt("GOOGLE_CLIENT_SECRET", "")
	c.FacebookClientID, c.FacebookClientSecret = opt("FACEBOOK_CLIENT_ID", ""), opt("FACEBOOK_CLIENT_SECRET", "")
	c.TermsVersion = opt("TERMS_VERSION", "")
	c.PrivacyVersion = opt("PRIVACY_VERSION", "")
	if dev {
		if c.TermsVersion == "" {
			c.TermsVersion = "dev"
		}
		if c.PrivacyVersion == "" {
			c.PrivacyVersion = "dev"
		}
	} else {
		if c.TermsVersion == "" {
			errs = append(errs, "TERMS_VERSION is required")
		}
		if c.PrivacyVersion == "" {
			errs = append(errs, "PRIVACY_VERSION is required")
		}
	}

	c.LogLevel = opt("LOG_LEVEL", "info")
	for _, p := range strings.Split(get("TRUSTED_PROXY_CIDRS"), ",") {
		if p = strings.TrimSpace(p); p == "" {
			continue
		}
		pfx, err := netip.ParsePrefix(p)
		if err != nil {
			errs = append(errs, "TRUSTED_PROXY_CIDRS: bad CIDR "+p)
			continue
		}
		c.TrustedProxyCIDRs = append(c.TrustedProxyCIDRs, pfx)
	}
	c.ProxySharedSecret = get("PROXY_SHARED_SECRET")
	if c.AppEnv == "prod" {
		if len(c.TrustedProxyCIDRs) == 0 {
			errs = append(errs, "TRUSTED_PROXY_CIDRS is required in prod")
		}
		if len(c.ProxySharedSecret) < 32 {
			errs = append(errs, "PROXY_SHARED_SECRET (>= 32 chars) is required in prod")
		}
	}

	c.S3 = S3{Endpoint: strings.TrimSpace(get("S3_ENDPOINT")), Region: opt("S3_REGION", "us-east-1"), Bucket: strings.TrimSpace(get("S3_BUCKET")),
		AccessKey: strings.TrimSpace(get("S3_ACCESS_KEY")), SecretKey: get("S3_SECRET_KEY"), Prefix: strings.TrimSpace(get("S3_PREFIX"))}
	c.JournalS3 = S3{Endpoint: strings.TrimSpace(get("JOURNAL_S3_ENDPOINT")), Region: opt("JOURNAL_S3_REGION", "us-east-1"), Bucket: strings.TrimSpace(get("JOURNAL_S3_BUCKET")),
		AccessKey: strings.TrimSpace(get("JOURNAL_S3_ACCESS_KEY")), SecretKey: get("JOURNAL_S3_SECRET_KEY"), Prefix: strings.TrimSpace(get("JOURNAL_S3_PREFIX"))}
	c.DeletionJournalKey = strings.TrimSpace(get("DELETION_JOURNAL_KEY"))
	c.SyncIntervalUI = dur("SYNC_INTERVAL_UI", "5m")
	c.SyncIntervalArticles = dur("SYNC_INTERVAL_ARTICLES", "1m")
	c.Ollama = Ollama{URL: strings.TrimRight(strings.TrimSpace(get("OLLAMA_URL")), "/"), Model: strings.TrimSpace(get("OLLAMA_MODEL")), RequestTimeout: dur("TRANSLATOR_REQUEST_TIMEOUT", "10m")}

	if len(errs) > 0 {
		return nil, fmt.Errorf("config: %w", errors.New(strings.Join(errs, "; ")))
	}
	return c, nil
}

// RequireS3 checks that the S3 settings needed by api, content-sync and translator are present.
func (c *Config) RequireS3() error {
	var m []string
	if c.S3.Endpoint == "" {
		m = append(m, "S3_ENDPOINT")
	}
	if c.S3.Bucket == "" {
		m = append(m, "S3_BUCKET")
	}
	if c.S3.AccessKey == "" {
		m = append(m, "S3_ACCESS_KEY")
	}
	if c.S3.SecretKey == "" {
		m = append(m, "S3_SECRET_KEY")
	}
	if len(m) > 0 {
		return fmt.Errorf("config: missing %s", strings.Join(m, ", "))
	}
	return nil
}

// RequireOllama checks the settings needed by the translator CLI.
func (c *Config) RequireOllama() error {
	if c.Ollama.URL == "" || c.Ollama.Model == "" {
		return errors.New("config: OLLAMA_URL and OLLAMA_MODEL are required")
	}
	return nil
}

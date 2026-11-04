package config

import (
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func devEnv() map[string]string {
	return map[string]string{
		"APP_ENV": "dev", "PUBLIC_BASE_URL": "http://localhost:3000/", "ALLOWED_ORIGINS": "http://localhost:3000, https://beeloy.com/",
		"DATABASE_URL": "postgres://x", "REDIS_URL": "redis://x", "OTP_HMAC_SECRET": strings.Repeat("s", 32),
	}
}

func TestLoadDevOK(t *testing.T) {
	c, err := LoadFrom(env(devEnv()))
	if err != nil {
		t.Fatal(err)
	}
	if !c.IsDev() || c.PublicBaseURL != "http://localhost:3000" || len(c.AllowedOrigins) != 2 || c.AllowedOrigins[1] != "https://beeloy.com" {
		t.Fatalf("bad config: %+v", c)
	}
	if c.HTTPAddr != ":8080" || c.DBMaxConns != 20 || c.TermsVersion != "dev" {
		t.Fatalf("defaults: %+v", c)
	}
}

func TestLoadMissingReportsAll(t *testing.T) {
	_, err := LoadFrom(env(map[string]string{"APP_ENV": "dev"}))
	if err == nil {
		t.Fatal("want error")
	}
	for _, k := range []string{"PUBLIC_BASE_URL", "ALLOWED_ORIGINS", "DATABASE_URL", "REDIS_URL", "OTP_HMAC_SECRET"} {
		if !strings.Contains(err.Error(), k) {
			t.Errorf("error must mention %s: %v", k, err)
		}
	}
}

func TestProdRequirements(t *testing.T) {
	m := devEnv()
	m["APP_ENV"] = "prod"
	_, err := LoadFrom(env(m))
	if err == nil {
		t.Fatal("prod without proxy/smtp/terms must fail")
	}
	for _, k := range []string{"TRUSTED_PROXY_CIDRS", "PROXY_SHARED_SECRET", "SMTP_HOST", "TERMS_VERSION"} {
		if !strings.Contains(err.Error(), k) {
			t.Errorf("error must mention %s: %v", k, err)
		}
	}
	m["SMTP_HOST"], m["SMTP_FROM"] = "smtp", "no-reply@beeloy.com"
	m["TERMS_VERSION"], m["PRIVACY_VERSION"] = "1", "1"
	m["TRUSTED_PROXY_CIDRS"] = "173.245.48.0/20, 2400:cb00::/32"
	m["PROXY_SHARED_SECRET"] = strings.Repeat("p", 32)
	c, err := LoadFrom(env(m))
	if err != nil || len(c.TrustedProxyCIDRs) != 2 {
		t.Fatalf("prod config: %v %+v", err, c)
	}
}

func TestBadValues(t *testing.T) {
	m := devEnv()
	m["APP_ENV"] = "weird"
	m["OTP_HMAC_SECRET"] = "short"
	m["DB_MAX_CONNS"] = "-3"
	m["SYNC_INTERVAL_UI"] = "soon"
	_, err := LoadFrom(env(m))
	if err == nil {
		t.Fatal("want error")
	}
	for _, s := range []string{"APP_ENV must", "OTP_HMAC_SECRET must", "DB_MAX_CONNS", "SYNC_INTERVAL_UI"} {
		if !strings.Contains(err.Error(), s) {
			t.Errorf("missing %q in %v", s, err)
		}
	}
}

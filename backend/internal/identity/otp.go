package identity

import (
	"context"
	"encoding/hex"
	"log/slog"
	"net/mail"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"beeloy.com/backend/internal/apperr"
	"beeloy.com/backend/internal/locales"
	"beeloy.com/backend/internal/platform/crypto"
	"beeloy.com/backend/internal/platform/logx"
	pmail "beeloy.com/backend/internal/platform/mail"
	"beeloy.com/backend/internal/platform/ratelimit"
	"github.com/redis/go-redis/v9"
)

const (
	otpTTL         = 10 * time.Minute
	otpCooldown    = 60 * time.Second
	otpMaxAttempts = 5
	otpLen         = 6
)

var (
	ruleOTPEmail  = ratelimit.Rule{Name: "otp_email", Limit: 5, Window: time.Hour}
	ruleOTPIP     = ratelimit.Rule{Name: "otp_ip", Limit: 20, Window: time.Hour}
	ruleVerifyIP  = ratelimit.Rule{Name: "otp_verify_ip", Limit: 10, Window: time.Hour}
	otpCooldownKV = "otp:cool:"
	otpKey        = "otp:"
)

// otpStore replaces the challenge atomically (a stale verifier cannot delete a fresh one).
var otpStore = redis.NewScript(`
redis.call('DEL', KEYS[1])
redis.call('HSET', KEYS[1], 'h', ARGV[1], 'attempts', 0)
redis.call('EXPIRE', KEYS[1], ARGV[2])
return 1`)

// otpCheck compares and consumes in one step: exactly one concurrent verifier can succeed; the last allowed wrong attempt burns the code.
var otpCheck = redis.NewScript(`
local h = redis.call('HGET', KEYS[1], 'h')
if not h then return 0 end
local n = redis.call('HINCRBY', KEYS[1], 'attempts', 1)
if n > tonumber(ARGV[2]) then redis.call('DEL', KEYS[1]) return 0 end
local a, b, d = h, ARGV[1], 0
if #a ~= #b then redis.call('DEL', KEYS[1]) return 0 end
for i = 1, #a do if a:byte(i) ~= b:byte(i) then d = 1 end end
if d == 0 then redis.call('DEL', KEYS[1]) return 1 end
if n >= tonumber(ARGV[2]) then redis.call('DEL', KEYS[1]) end
return 0`)

// OTPService issues and checks one-time codes. State lives in Redis; if Redis fails the service fails closed (503).
type OTPService struct {
	RDB    *redis.Client
	Mailer pmail.Mailer
	Secret []byte
	Lim    *ratelimit.Limiter
	Slugs  *SlugService
}

// NormalizeEmail lowercases and validates an address (plain address only, <= 254 chars).
func NormalizeEmail(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return "", apperr.Field("email", "required")
	}
	a, err := mail.ParseAddress(s)
	if err != nil || len(s) > 254 || a.Address != s || a.Name != "" {
		return "", apperr.Field("email", "invalid_format")
	}
	return s, nil
}

func (s *OTPService) mac(email, code string) string {
	return hex.EncodeToString(crypto.HMACSHA256(s.Secret, []byte(email+":"+code)))
}

// Request sends a code. The response never reveals whether the address is known; retryAfter is always 60.
func (s *OTPService) Request(ctx context.Context, email, locale string, ip netip.Addr) (int, error) {
	email, err := NormalizeEmail(email)
	if err != nil {
		return 0, err
	}
	if err := s.Lim.Check(ctx, ruleOTPEmail, email, true); err != nil {
		return 0, err
	}
	if err := s.Lim.Check(ctx, ruleOTPIP, ip.String(), true); err != nil {
		return 0, err
	}
	fresh, err := s.RDB.SetNX(ctx, otpCooldownKV+email, 1, otpCooldown).Result()
	if err != nil {
		return 0, apperr.ErrUnavailable.Wrap(err)
	}
	if !fresh { // cooldown: do not send again, answer exactly like a success
		return int(otpCooldown / time.Second), nil
	}
	code, err := crypto.RandomDigits(otpLen)
	if err != nil {
		return 0, err
	}
	if err := otpStore.Run(ctx, s.RDB, []string{otpKey + email}, s.mac(email, code), int(otpTTL/time.Second)).Err(); err != nil {
		return 0, apperr.ErrUnavailable.Wrap(err)
	}
	if !locales.Known(locale) {
		locale = "en"
	}
	subj, body := OTPMessage(locale, code, strconv.Itoa(int(otpTTL/time.Minute)))
	if err := s.Mailer.Send(ctx, pmail.Message{To: email, Subject: subj, TextBody: body}); err != nil {
		slog.Error("otp mail failed", "to", logx.MaskEmail(email), "err", err) // response stays 202: no enumeration
	}
	return int(otpCooldown / time.Second), nil
}

// Precheck validates a requested registration slug without consuming the code.
func (s *OTPService) Precheck(ctx context.Context, slug string) error {
	if slug == "" {
		return nil
	}
	if err := Validate(EntityUser, slug); err != nil {
		return err
	}
	ok, err := s.Slugs.IsAvailable(ctx, s.Slugs.Pool, EntityUser, slug)
	if err != nil {
		return err
	}
	if !ok {
		return apperr.Conflict("slug_taken")
	}
	return nil
}

// Verify checks the code and consumes it. Any failure is the same invalid_code error.
func (s *OTPService) Verify(ctx context.Context, email, code string, ip netip.Addr) (string, error) {
	email, err := NormalizeEmail(email)
	if err != nil {
		return "", err
	}
	if err := s.Lim.Check(ctx, ruleVerifyIP, ip.String(), true); err != nil {
		return "", err
	}
	res, err := otpCheck.Run(ctx, s.RDB, []string{otpKey + email}, s.mac(email, strings.TrimSpace(code)), otpMaxAttempts).Int()
	if err != nil {
		return "", apperr.ErrUnavailable.Wrap(err)
	}
	if res != 1 {
		return "", apperr.ErrInvalidCode
	}
	return email, nil
}

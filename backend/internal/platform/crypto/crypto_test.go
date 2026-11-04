package crypto

import (
	"strings"
	"testing"
)

func TestRandomDigits(t *testing.T) {
	seen0 := false
	for i := 0; i < 300; i++ {
		s, err := RandomDigits(6)
		if err != nil || len(s) != 6 {
			t.Fatalf("bad %q %v", s, err)
		}
		if strings.Trim(s, "0123456789") != "" {
			t.Fatalf("non-digit in %q", s)
		}
		if s[0] == '0' {
			seen0 = true
		}
	}
	if !seen0 {
		t.Error("leading zeros must be possible")
	}
}

func TestTokenAndHash(t *testing.T) {
	a, _ := RandomToken(32)
	b, _ := RandomToken(32)
	if a == b || len(a) != 43 {
		t.Fatalf("token: %q %q", a, b)
	}
	if !ConstTimeEqual(SHA256([]byte("x")), SHA256([]byte("x"))) || ConstTimeEqual(SHA256([]byte("x")), SHA256([]byte("y"))) {
		t.Error("sha compare")
	}
	if !ConstTimeEqual(HMACSHA256([]byte("k"), []byte("m")), HMACSHA256([]byte("k"), []byte("m"))) || ConstTimeEqual(HMACSHA256([]byte("k"), []byte("m")), HMACSHA256([]byte("k2"), []byte("m"))) {
		t.Error("hmac compare")
	}
}

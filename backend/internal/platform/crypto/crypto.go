// Package crypto holds small, safe primitives used by auth code.
package crypto

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"math/big"
)

// RandomToken returns nBytes of CSPRNG output encoded as unpadded base64url.
func RandomToken(nBytes int) (string, error) {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func SHA256(b []byte) []byte { h := sha256.Sum256(b); return h[:] }

func HMACSHA256(secret, msg []byte) []byte {
	m := hmac.New(sha256.New, secret)
	m.Write(msg)
	return m.Sum(nil)
}

func ConstTimeEqual(a, b []byte) bool { return subtle.ConstantTimeCompare(a, b) == 1 }

// RandomDigits returns n uniformly distributed decimal digits (leading zeros allowed).
func RandomDigits(n int) (string, error) {
	out := make([]byte, n)
	ten := big.NewInt(10)
	for i := range out {
		v, err := rand.Int(rand.Reader, ten)
		if err != nil {
			return "", err
		}
		out[i] = byte('0' + v.Int64())
	}
	return string(out), nil
}

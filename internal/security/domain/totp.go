package domain

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"fmt"
	"time"
)

type codeKey struct{}

func WithCode(ctx context.Context, code string) context.Context {
	return context.WithValue(ctx, codeKey{}, code)
}
func CodeFrom(ctx context.Context) string { v, _ := ctx.Value(codeKey{}).(string); return v }

// Code follows RFC6238/RFC4226 dynamic truncation. SHA1 is required for standard authenticator interoperability.
func Code(secret []byte, counter uint64, digits int) (string, error) {
	if len(secret) < 20 || (digits != 6 && digits != 8) {
		return "", errors.New("TOTP_NOT_CONFIGURED")
	}
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], counter)
	mac := hmac.New(sha1.New, secret)
	mac.Write(b[:])
	hash := mac.Sum(nil)
	offset := hash[len(hash)-1] & 15
	n := binary.BigEndian.Uint32(hash[offset:offset+4]) & 0x7fffffff
	mod := uint32(1000000)
	if digits == 8 {
		mod = 100000000
	}
	return fmt.Sprintf("%0*d", digits, n%mod), nil
}
func Match(secret []byte, code string, at time.Time) (uint64, error) {
	if len(code) != 6 || at.Unix() < 30 {
		return 0, errors.New("TOTP_INVALID")
	}
	for _, c := range code {
		if c < '0' || c > '9' {
			return 0, errors.New("TOTP_INVALID")
		}
	}
	counter := at.Unix() / 30
	for _, shift := range []int64{0, -1, 1} {
		v := uint64(counter + shift)
		expected, err := Code(secret, v, 6)
		if err != nil {
			return 0, err
		}
		if subtle.ConstantTimeCompare([]byte(expected), []byte(code)) == 1 {
			return v, nil
		}
	}
	return 0, errors.New("TOTP_INVALID")
}

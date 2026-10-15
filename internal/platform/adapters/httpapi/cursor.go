package httpapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"github.com/ex-business/ExCommission/internal/platform/domain"
	"strings"
)

type Cursor struct {
	Tenant, App, Root, At string
	Offset, Limit         int
}
type CursorCodec struct{ Key []byte }

func (c CursorCodec) Encode(value Cursor) (string, error) {
	if len(c.Key) < 32 {
		return "", domain.ErrValidation
	}
	body, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, c.Key)
	mac.Write(body)
	return base64.RawURLEncoding.EncodeToString(body) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}
func (c CursorCodec) Decode(token string, expected Cursor) (Cursor, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 || len(c.Key) < 32 {
		return Cursor{}, domain.ErrValidation
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return Cursor{}, domain.ErrValidation
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Cursor{}, domain.ErrValidation
	}
	mac := hmac.New(sha256.New, c.Key)
	mac.Write(body)
	if !hmac.Equal(mac.Sum(nil), signature) {
		return Cursor{}, domain.ErrValidation
	}
	var value Cursor
	if err = json.Unmarshal(body, &value); err != nil {
		return Cursor{}, domain.ErrValidation
	}
	if value.Tenant != expected.Tenant || value.App != expected.App || value.Root != expected.Root || value.At != expected.At || value.Limit != expected.Limit || value.Offset < 0 || value.Offset > 1000000 {
		return Cursor{}, domain.ErrValidation
	}
	return value, nil
}

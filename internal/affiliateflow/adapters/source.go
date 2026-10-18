package adapters

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	model "github.com/ex-business/ExCommission/internal/affiliateflow/domain"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	"io"
)

type HMACSource struct {
	Scope  foundation.Scope
	Source string
	Key    []byte
}

func (s HMACSource) Verify(ctx context.Context, scope foundation.Scope, raw []byte, signature string) (model.Event, error) {
	var e model.Event
	if scope != s.Scope || len(s.Key) < 32 {
		return e, errors.New("PERMISSION_DENIED")
	}
	expected := hmac.New(sha256.New, s.Key)
	expected.Write([]byte("affiliate-business-v1\n" + scope.Tenant + "\n" + scope.App + "\n" + s.Source + "\n"))
	expected.Write(raw)
	sig, err := hex.DecodeString(signature)
	if err != nil || !hmac.Equal(sig, expected.Sum(nil)) {
		return e, errors.New("SOURCE_SIGNATURE_INVALID")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&e) != nil || d.Decode(new(any)) != io.EOF || e.Source != s.Source {
		return e, foundation.ErrValidation
	}
	e.At = e.At.UTC()
	return e, nil
}

func (s HMACSource) VerifyWatermark(ctx context.Context, scope foundation.Scope, raw []byte, signature string) (model.Watermark, error) {
	var out model.Watermark
	if scope != s.Scope || len(s.Key) < 32 {
		return out, errors.New("PERMISSION_DENIED")
	}
	mac := hmac.New(sha256.New, s.Key)
	mac.Write([]byte("affiliate-watermark-v1\n" + scope.Tenant + "\n" + scope.App + "\n" + s.Source + "\n"))
	mac.Write(raw)
	sig, e := hex.DecodeString(signature)
	if e != nil || !hmac.Equal(sig, mac.Sum(nil)) {
		return out, errors.New("SOURCE_SIGNATURE_INVALID")
	}
	var required map[string]json.RawMessage
	if json.Unmarshal(raw, &required) != nil {
		return out, foundation.ErrValidation
	}
	for _, field := range []string{"source_id", "business_day", "fact_count", "facts_digest"} {
		v, ok := required[field]
		if !ok || bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			return out, foundation.ErrValidation
		}
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&out) != nil || d.Decode(new(any)) != io.EOF || out.Source != s.Source {
		return out, foundation.ErrValidation
	}
	return out, nil
}

// VerifyReversal 使用独立签名前缀，原成功事件签名不能复用为撤销指令。
func (s HMACSource) VerifyReversal(ctx context.Context, scope foundation.Scope, raw []byte, signature string) (model.Reversal, error) {
	var out model.Reversal
	if scope != s.Scope || len(s.Key) < 32 {
		return out, errors.New("PERMISSION_DENIED")
	}
	mac := hmac.New(sha256.New, s.Key)
	mac.Write([]byte("affiliate-reversal-v1\n" + scope.Tenant + "\n" + scope.App + "\n" + s.Source + "\n"))
	mac.Write(raw)
	sig, e := hex.DecodeString(signature)
	if e != nil || !hmac.Equal(sig, mac.Sum(nil)) {
		return out, errors.New("SOURCE_SIGNATURE_INVALID")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&out) != nil || d.Decode(new(any)) != io.EOF || out.Source != s.Source {
		return out, foundation.ErrValidation
	}
	out.At = out.At.UTC()
	return out, nil
}

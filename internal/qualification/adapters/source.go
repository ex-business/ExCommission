package adapters

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	model "github.com/ex-business/ExCommission/internal/qualification/domain"
	"io"
)

// HMACSource 只接收已成功充值、已结算有效投注的不可变事实，独立密钥及前缀隔离注册和计佣签名。
// 新来源实现同一Source端口；不得仅凭客户端传入的“成功”字段认定可信。
type HMACSource struct {
	Scope    foundation.Scope
	SourceID string
	Key      []byte
}

func (s HMACSource) Verify(_ context.Context, scope foundation.Scope, raw []byte, signature string) (model.Fact, error) {
	var fact model.Fact
	if scope != s.Scope || len(s.Key) < 32 {
		return fact, errors.New("PERMISSION_DENIED")
	}
	mac := hmac.New(sha256.New, s.Key)
	mac.Write([]byte("qualification-v1\n" + scope.Tenant + "\n" + scope.App + "\n" + s.SourceID + "\n"))
	mac.Write(raw)
	supplied, err := hex.DecodeString(signature)
	if err != nil || !hmac.Equal(supplied, mac.Sum(nil)) {
		return fact, errors.New("SOURCE_SIGNATURE_INVALID")
	}
	if _, err = foundation.CanonicalJSON(raw); err != nil {
		return fact, foundation.ErrValidation
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&fact); err != nil {
		return fact, foundation.ErrValidation
	}
	var extra any
	if err = decoder.Decode(&extra); !errors.Is(err, io.EOF) || fact.Source != s.SourceID {
		return fact, foundation.ErrValidation
	}
	return fact, nil
}

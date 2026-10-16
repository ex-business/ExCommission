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
	model "github.com/ex-business/ExCommission/internal/registration/domain"
	"io"
)

// HMACSource 只适配已确认成功的注册事件；独立密钥和作用域前缀阻止跨业务、跨站点复用签名。
// 后续消息总线/会员平台适配器实现同一 Source，不向领域引入 HTTP 或会员密码。
type HMACSource struct {
	Scope    foundation.Scope
	SourceID string
	Key      []byte
}

func (s HMACSource) Verify(_ context.Context, scope foundation.Scope, raw []byte, signature string) (model.Event, error) {
	var event model.Event
	if scope != s.Scope || len(s.Key) < 32 {
		return event, errors.New("PERMISSION_DENIED")
	}
	mac := hmac.New(sha256.New, s.Key)
	mac.Write([]byte("registration-v1\n" + scope.Tenant + "\n" + scope.App + "\n" + s.SourceID + "\n"))
	mac.Write(raw)
	supplied, err := hex.DecodeString(signature)
	if err != nil || !hmac.Equal(supplied, mac.Sum(nil)) {
		return event, errors.New("SOURCE_SIGNATURE_INVALID")
	}
	// 先校验原始字节签名，再拒绝重复键、未知字段及多个 JSON 对象。
	if _, err = foundation.CanonicalJSON(raw); err != nil {
		return event, foundation.ErrValidation
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&event); err != nil {
		return event, foundation.ErrValidation
	}
	var extra any
	if err = decoder.Decode(&extra); !errors.Is(err, io.EOF) || event.SourceID != s.SourceID {
		return event, foundation.ErrValidation
	}
	return event, nil
}

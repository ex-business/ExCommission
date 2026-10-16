package adapters

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	"strings"
	"testing"
)

func TestRegistrationSourceBoundary(t *testing.T) {
	scope := foundation.Scope{Tenant: "site-a", App: "commission"}
	source := HMACSource{Scope: scope, SourceID: "sandbox", Key: []byte(strings.Repeat("r", 32))}
	sign := func(raw []byte) string {
		mac := hmac.New(sha256.New, source.Key)
		mac.Write([]byte("registration-v1\nsite-a\ncommission\nsandbox\n"))
		mac.Write(raw)
		return hex.EncodeToString(mac.Sum(nil))
	}
	raw := []byte(`{"schema_version":"1","source_id":"sandbox","event_id":"event","external_user_id":"member","display_name":"会员","registered_at":"2026-10-16T00:00:00Z","invitation_code":null}`)
	if _, err := source.Verify(context.Background(), scope, raw, sign(raw)); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name      string
		scope     foundation.Scope
		body      []byte
		signature string
	}{
		{"跨站点", foundation.Scope{Tenant: "site-b", App: "commission"}, raw, sign(raw)},
		{"跨应用", foundation.Scope{Tenant: "site-a", App: "other"}, raw, sign(raw)},
		{"请求内容篡改", scope, append(append([]byte{}, raw...), byte(' ')), sign(raw)},
		{"无签名", scope, raw, ""},
		{"未知字段", scope, []byte(`{"source_id":"sandbox","password":"forbidden"}`), sign([]byte(`{"source_id":"sandbox","password":"forbidden"}`))},
		{"重复键", scope, []byte(`{"source_id":"sandbox","source_id":"sandbox"}`), sign([]byte(`{"source_id":"sandbox","source_id":"sandbox"}`))},
		{"多对象", scope, []byte(`{} {}`), sign([]byte(`{} {}`))},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := source.Verify(context.Background(), test.scope, test.body, test.signature); err == nil {
				t.Fatal("非法来源被接受")
			}
		})
	}
}

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

func TestQualificationSourceBoundary(t *testing.T) {
	scope := foundation.Scope{Tenant: "site", App: "app"}
	source := HMACSource{Scope: scope, SourceID: "sandbox", Key: []byte(strings.Repeat("q", 32))}
	sign := func(prefix string, raw []byte) string {
		mac := hmac.New(sha256.New, source.Key)
		mac.Write([]byte(prefix + "\nsite\napp\nsandbox\n"))
		mac.Write(raw)
		return hex.EncodeToString(mac.Sum(nil))
	}
	raw := []byte(`{"schema_version":"1","policy_id":"p","source_id":"sandbox","event_id":"event","business_id":"order","event_type":"DEPOSIT_SUCCEEDED","external_user_id":"member","business_time":"2026-10-16T00:00:00Z","amount_minor":"5000","currency":"USD","venue_type":null}`)
	if _, err := source.Verify(context.Background(), scope, raw, sign("qualification-v1", raw)); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Verify(context.Background(), scope, raw, sign("registration-v1", raw)); err == nil {
		t.Fatal("注册签名跨业务复用")
	}
	if _, err := source.Verify(context.Background(), foundation.Scope{Tenant: "other", App: "app"}, raw, sign("qualification-v1", raw)); err == nil {
		t.Fatal("跨站点签名复用")
	}
	duplicate := []byte(`{"source_id":"sandbox","source_id":"sandbox"}`)
	if _, err := source.Verify(context.Background(), scope, duplicate, sign("qualification-v1", duplicate)); err == nil {
		t.Fatal("重复键未拒绝")
	}
	unknown := []byte(`{"source_id":"sandbox","password":"not-allowed"}`)
	if _, err := source.Verify(context.Background(), scope, unknown, sign("qualification-v1", unknown)); err == nil {
		t.Fatal("未知字段未拒绝")
	}
}

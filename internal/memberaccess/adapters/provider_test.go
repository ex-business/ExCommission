package adapters

import (
	"context"
	"encoding/json"
	principal "github.com/ex-business/ExCommission/internal/identity/domain"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestProviderRequiresTrustedScopeAndResolvedMember(t *testing.T) {
	now := time.Now()
	payload := map[string]any{"active": true, "sub": "external-member", "iss": "issuer", "aud": "commission", "exp": now.Add(time.Hour).Unix(), "tenant_id": "tenant", "app_id": "app", "source_id": "sandbox"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, secret, ok := r.BasicAuth()
		if !ok || user != "client" || secret != strings.Repeat("s", 32) {
			t.Error("接入凭据缺失")
		}
		json.NewEncoder(w).Encode(payload)
	}))
	defer server.Close()
	cfg := Config{Endpoint: server.URL, ClientID: "client", Secret: strings.Repeat("s", 32), Issuer: "issuer", Audience: "commission", Source: "sandbox", LocalSandbox: true}
	calls := 0
	resolve := func(ctx context.Context, source, external string) (principal.Principal, error) {
		calls++
		if source != "sandbox" || external != "external-member" {
			t.Fatal("未使用受控来源", source, external)
		}
		return principal.Principal{Tenant: "tenant", App: "app", Subject: "member-trusted", MemberUserID: "usr-trusted"}, nil
	}
	p, e := New(cfg, "tenant", "app", resolve, nil)
	if e != nil {
		t.Fatal(e)
	}
	p.Now = func() time.Time { return now }
	out, e := p.Authenticate(context.Background(), "Bearer ms_test")
	if e != nil || out.MemberUserID != "usr-trusted" || calls != 1 {
		t.Fatal(out, e, calls)
	}
	for _, field := range []string{"iss", "aud", "tenant_id", "app_id", "source_id", "sub"} {
		old := payload[field]
		payload[field] = ""
		before := calls
		if _, e = p.Authenticate(context.Background(), "Bearer ms_test"); e == nil || calls != before {
			t.Fatal("不可信响应进入绑定", field, e)
		}
		payload[field] = old
	}
	payload["exp"] = now.Unix()
	if _, e = p.Authenticate(context.Background(), "Bearer ms_test"); e == nil {
		t.Fatal("过期令牌通过")
	}
	payload["exp"] = now.Add(time.Hour).Unix()
	payload["active"] = false
	if _, e = p.Authenticate(context.Background(), "Bearer ms_test"); e == nil {
		t.Fatal("已撤销令牌通过")
	}
	for _, endpoint := range []string{"http://example.com/introspect", "http://127.0.0.1.evil.test/", "ftp://127.0.0.1/", "https://user:password@example.com/", "http://localhost/"} {
		cfg.Endpoint = endpoint
		if _, e = New(cfg, "tenant", "app", resolve, nil); e == nil {
			t.Fatal("不安全地址通过", endpoint)
		}
	}
	cfg.Endpoint = server.URL
	cfg.LocalSandbox = false
	if _, e = New(cfg, "tenant", "app", resolve, nil); e == nil {
		t.Fatal("HTTP必须显式启用本地沙箱")
	}
}

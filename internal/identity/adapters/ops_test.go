package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/ex-business/ExCommission/internal/identity/domain"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOpsIdentityScopeRevocationAndLegacy(t *testing.T) {
	active := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("s", 32) {
			w.WriteHeader(401)
			return
		}
		if !active {
			w.WriteHeader(401)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"subject": "ops-12", "tenant_id": "tenant-a", "app_id": "app", "permissions": []string{"affiliate:configure"}, "expires": time.Now().Unix() + 600})
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "ops.json")
	raw, _ := json.Marshal(OpsConfig{Endpoint: server.URL + "/internal/commission/introspect", Secret: strings.Repeat("s", 32)})
	os.WriteFile(path, raw, 0600)
	legacy := NewLocalProvider(map[string]domain.Principal{strings.Repeat("l", 32): {Subject: "legacy", Tenant: "tenant-a", App: "app"}})
	provider, e := LoadOpsProvider(path, legacy)
	if e != nil {
		t.Fatal(e)
	}
	p, e := provider.Authenticate(context.Background(), "Bearer ops1.test")
	if e != nil {
		t.Fatal(e)
	}
	if e = Authorize(context.Background(), nil, p, "tenant-a", "app", "affiliate:configure"); e != nil {
		t.Fatal(e)
	}
	// 既有跨站路由可能复制并改写Principal，必须仍受内省原始站点约束。
	moved := p
	moved.Tenant = "tenant-b"
	if !errors.Is(Authorize(context.Background(), nil, moved, "tenant-b", "app", "affiliate:configure"), domain.ErrPermission) {
		t.Fatal("复制身份扩大了目标站点权限")
	}
	if !errors.Is(p.Authorize("tenant-b", "app", "affiliate:configure"), domain.ErrPermission) {
		t.Fatal("跨站未拒绝")
	}
	raw, _ = json.Marshal(p)
	if strings.Contains(string(raw), "ops1.test") {
		t.Fatal("凭据进入JSON响应")
	}
	active = false
	if _, e = provider.Authenticate(context.Background(), "Bearer ops1.test"); !errors.Is(e, domain.ErrUnauthenticated) {
		t.Fatal("撤销会话仍有效")
	}
	if p, e = provider.Authenticate(context.Background(), "Bearer "+strings.Repeat("l", 32)); e != nil || p.Subject != "legacy" {
		t.Fatal("旧身份受影响")
	}
	os.Chmod(path, 0644)
	if _, e = LoadOpsProvider(path, nil); e == nil {
		t.Fatal("接受了公开配置文件")
	}
}
func TestOpsStepUpErrorCodes(t *testing.T) {
	for _, code := range []string{"TOTP_INVALID", "TOTP_REPLAY", "TOTP_RATE_LIMITED"} {
		t.Run(code, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(403)
				json.NewEncoder(w).Encode(map[string]string{"error": code})
			}))
			defer server.Close()
			path := filepath.Join(t.TempDir(), "ops.json")
			raw, _ := json.Marshal(OpsConfig{Endpoint: server.URL + "/internal/commission/introspect", Secret: strings.Repeat("s", 32)})
			os.WriteFile(path, raw, 0600)
			e := OpsStepUp(context.Background(), path, domain.Principal{OpsCredential: "ops1.test"}, "123456", "toggle", "idempotency", map[string]string{"x": "y"})
			if e == nil || e.Error() != code {
				t.Fatalf("二次验证错误码丢失 %v", e)
			}
		})
	}
}

func TestGenericOpsRolesMappedByBusiness(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"subject": "ops-1", "site_id": "1000", "roles": []string{"viewer"}, "tenant_id": "forged", "app_id": "forged", "permissions": []string{"member:claim", "identity:approve"}, "expires": time.Now().Unix() + 600})
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "generic.json")
	raw, _ := json.Marshal(OpsConfig{Endpoint: server.URL + "/internal/identity/introspect", Secret: strings.Repeat("s", 32), Sites: map[string]OpsSite{"1000": {Tenant: "business-tenant", App: "business-app"}}})
	os.WriteFile(path, raw, 0600)
	provider, e := LoadOpsProvider(path, nil)
	if e != nil {
		t.Fatal(e)
	}
	p, e := provider.Authenticate(context.Background(), "Bearer ops1.test")
	if e != nil {
		t.Fatal(e)
	}
	if p.Tenant != "business-tenant" || p.App != "business-app" {
		t.Fatal("业务作用域未由本系统映射")
	}
	if p.Authorize(p.Tenant, p.App, "affiliate:read") != nil {
		t.Fatal("只读映射失败")
	}
	if p.Authorize(p.Tenant, p.App, "member:claim") == nil || p.Authorize(p.Tenant, p.App, "identity:approve") == nil {
		t.Fatal("信任了外部服务自报业务权限")
	}
}

// 恢复原后台权限时仍按职责授权，不能因功能恢复授予会员身份或跨站操作能力。
func TestOpsBusinessPermissionRestoration(t *testing.T) {
	tests := []struct {
		role        string
		allow, deny []string
	}{
		{"viewer", strings.Fields("plans:read reports:read statements:read risk:read"), strings.Fields("plans:write reports:export payments:write workflow:decide")},
		{"applicant", strings.Fields("plans:read plans:write plans:simulate reports:read reports:export reports:download users:sync milestones:write statements:write payments:write payments:verify payments:reconcile refunds:write events:write tasks:recover recoveries:request adjustments:request risk:write platform:request workflow:cancel"), strings.Fields("plans:approve statements:approve risk:approve recoveries:approve")},
		{"approver", strings.Fields("plans:read plans:approve statements:approve reports:read milestones:approve recoveries:approve adjustments:approve risk:approve platform:approve workflow:recover workflow:decide"), strings.Fields("plans:write plans:simulate payments:write events:write reports:download")},
		{"unknown", nil, strings.Fields("plans:read reports:read payments:write")},
	}
	for _, tt := range tests {
		t.Run(tt.role, func(t *testing.T) {
			p := domain.Principal{Subject: "ops-1", Tenant: "a", App: "app", OpsTenant: "a", OpsApp: "app", OpsCredential: "ops1.test", Permissions: OpsPermissions([]string{tt.role})}
			for _, permission := range tt.allow {
				if err := p.Authorize("a", "app", permission); err != nil {
					t.Errorf("缺少原业务权限 %s", permission)
				}
			}
			for _, permission := range append(tt.deny, "member:claim", "member:refer", "identity:approve", "identity:request", "affiliate:ingest") {
				if p.Authorize("a", "app", permission) == nil {
					t.Errorf("角色越权 %s", permission)
				}
			}
			for _, permission := range tt.allow {
				if p.Authorize("b", "app", permission) == nil {
					t.Errorf("跨站未拒绝 %s", permission)
				}
			}
		})
	}
}

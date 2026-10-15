package application

import (
	"context"
	local "github.com/ex-business/ExCommission/internal/identity/adapters"
	"github.com/ex-business/ExCommission/internal/identity/domain"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type authorizer struct{}

func (authorizer) Authorize(_ context.Context, p domain.Principal, tenant, app, permission string) error {
	return p.Authorize(tenant, app, permission)
}
func TestRequestAuthenticationAndScope(t *testing.T) {
	token := strings.Repeat("a", 32)
	p := domain.Principal{Tenant: "t", App: "a", Subject: "s", Permissions: []string{"read"}}
	provider := local.NewLocalProvider(map[string]domain.Principal{token: p})
	called := 0
	handler := Require(provider, authorizer{}, "read", func(w http.ResponseWriter, r *http.Request, p domain.Principal, requestID string) {
		called++
		w.WriteHeader(204)
	})
	for _, tc := range []struct {
		token, tenant string
		status        int
	}{{"", "", 401}, {"Bearer " + token, "other", 403}, {"Bearer " + token, "t", 204}} {
		request := httptest.NewRequest("GET", "/resource", nil)
		request.Header.Set("Authorization", tc.token)
		request.Header.Set("X-Tenant-ID", tc.tenant)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != tc.status {
			t.Fatalf("got %d expected %d", response.Code, tc.status)
		}
	}
	if called != 1 {
		t.Fatalf("unauthorized handler executed %d", called)
	}
}

package monitoring

import (
	auth "github.com/ex-business/ExCommission/internal/identity/application"
	principal "github.com/ex-business/ExCommission/internal/identity/domain"
	"net/http"
	"net/http/httptest"
	"testing"
)

// 权限按信息类别分开，不能把tasks:read扩成财务或审计查询权限。
func TestMonitoringRequiresSeparatePermissions(t *testing.T) {
	mux := http.NewServeMux()
	seen := map[string]string{}
	require := func(permission string, next auth.AuthorizedHandler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { seen[r.URL.Path] = permission; w.WriteHeader(403) })
	}
	Install(mux, require, nil)
	for kind, permission := range map[string]string{"tasks": "tasks:read", "payments": "payments:read", "trace": "audit:read"} {
		r := httptest.NewRequest("GET", "/v1/monitoring/"+kind, nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != 403 || seen[r.URL.Path] != permission {
			t.Fatalf("权限分项错误 %s", kind)
		}
	}
	_ = principal.Principal{}
}
func TestRequestIDDoesNotPermitQuerySyntax(t *testing.T) {
	for _, v := range []string{"", "a OR 1=1", "../1000", "x\nAuthorization", "a&site=other"} {
		if requestPattern.MatchString(v) {
			t.Fatal("非法编号被接受")
		}
	}
}

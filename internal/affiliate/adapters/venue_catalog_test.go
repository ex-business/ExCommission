package adapters

import (
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	"os"
	"path/filepath"
	"testing"
)

func TestExternalVenuesScopeAndLogoValidation(t *testing.T) {
	file := filepath.Join(t.TempDir(), "venues.json")
	t.Setenv("AFFILIATE_VENUE_CATALOG_FILE", file)
	body := `[{"tenant_id":"one","app_id":"app","venues":[{"venue_type_id":"CUSTOM","name":"自定义场馆","icon_url":"https://example.test/logo.svg"}]}]`
	if e := os.WriteFile(file, []byte(body), 0600); e != nil {
		t.Fatal(e)
	}
	rows, ok, e := externalVenues(foundation.Scope{Tenant: "one", App: "app"})
	if e != nil || !ok || len(rows) != 1 || rows[0].ID != "CUSTOM" {
		t.Fatal(rows, ok, e)
	}
	_, ok, e = externalVenues(foundation.Scope{Tenant: "other", App: "app"})
	if e != nil || ok {
		t.Fatal("跨站目录泄露", ok, e)
	}
	for _, bad := range []string{`[{"tenant_id":"one","app_id":"app","venues":[{"venue_type_id":"CUSTOM","name":"自定义","icon_url":"javascript:alert(1)"}]}]`, `[{"tenant_id":"one","app_id":"app","venues":[]}]`} {
		if e := os.WriteFile(file, []byte(bad), 0600); e != nil {
			t.Fatal(e)
		}
		if _, _, e = externalVenues(foundation.Scope{Tenant: "one", App: "app"}); e == nil {
			t.Fatal("无效目录未拒绝")
		}
	}
}

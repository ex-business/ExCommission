package adapters

import (
	principal "github.com/ex-business/ExCommission/internal/identity/domain"
	"os"
	"path/filepath"
	"testing"
)

func TestSiteCatalogExplicitOwnerAndPrivateFile(t *testing.T) {
	p := principal.Principal{Tenant: "one", App: "commission", Subject: "local-operator"}
	file := filepath.Join(t.TempDir(), "access.json")
	body := []byte(`[{"owner_tenant_id":"one","owner_app_id":"commission","subject_id":"local-operator","targets":[{"tenant_id":"two","app_id":"commission"}]}]`)
	if err := os.WriteFile(file, body, 0600); err != nil {
		t.Fatal(err)
	}
	c := SiteCatalog{AccessFile: file}
	targets, err := c.Targets(p)
	if err != nil || len(targets) != 2 {
		t.Fatal(targets, err)
	}
	// 同名后台身份不等于跨站授权，站点、应用和主体三者必须全部吻合。
	for _, other := range []principal.Principal{{Tenant: "two", App: "commission", Subject: "local-operator"}, {Tenant: "one", App: "other", Subject: "local-operator"}, {Tenant: "one", App: "commission", Subject: "other"}} {
		targets, err = c.Targets(other)
		if err != nil || len(targets) != 1 || targets[0].Tenant != other.Tenant || targets[0].App != other.App {
			t.Fatal(targets, err)
		}
	}
	if err = os.Chmod(file, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Targets(p); err == nil {
		t.Fatal("公开可读授权文件未拒绝")
	}
}

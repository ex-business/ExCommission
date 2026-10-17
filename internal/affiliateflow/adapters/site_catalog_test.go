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

func TestSiteCatalogReadDoesNotGrantWrite(t *testing.T) {
	p := principal.Principal{Tenant: "one", App: "app", Subject: "operator"}
	file := filepath.Join(t.TempDir(), "access.json")
	body := `[{"owner_tenant_id":"one","owner_app_id":"app","subject_id":"operator","targets":[{"tenant_id":"readonly","app_id":"app"}],"writable_targets":[{"tenant_id":"writable","app_id":"app"}]}]`
	if e := os.WriteFile(file, []byte(body), 0600); e != nil {
		t.Fatal(e)
	}
	c := SiteCatalog{AccessFile: file}
	for _, target := range []SiteTarget{{"one", "app"}, {"readonly", "app"}, {"writable", "app"}, {"unknown", "app"}} {
		allowed, e := c.Writable(p, target)
		want := target.Tenant == "one" || target.Tenant == "writable"
		if e != nil || allowed != want {
			t.Fatal(target, allowed, e)
		}
	}
	targets, e := c.Targets(p)
	if e != nil || len(targets) != 3 {
		t.Fatal(targets, e)
	}
	p.Subject = "other"
	allowed, e := c.Writable(p, SiteTarget{"writable", "app"})
	if e != nil || allowed {
		t.Fatal("同站其他主体不得继承写权限", allowed, e)
	}
}

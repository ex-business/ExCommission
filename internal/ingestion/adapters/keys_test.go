package adapters

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
)

func TestKeyRingScopeAndPrivateFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.json")
	keys := []Key{{Ref: "deposit", Tenant: "1000", App: "commission", Secret: strings.Repeat("a", 32)}}
	raw, _ := json.Marshal(keys)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := FindKey(path, foundation.Scope{Tenant: "1000", App: "commission"}, "deposit"); err != nil {
		t.Fatal(err)
	}
	for _, sc := range []foundation.Scope{{Tenant: "2000", App: "commission"}, {Tenant: "1000", App: "other"}} {
		if _, err := FindKey(path, sc, "deposit"); err == nil {
			t.Fatal("跨站或跨应用读取密钥")
		}
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := KeyRing(path); err == nil {
		t.Fatal("可公开读取的密钥文件未拒绝")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(raw, []byte(` {}`)...), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := KeyRing(path); err == nil {
		t.Fatal("密钥文件尾随内容未拒绝")
	}
}

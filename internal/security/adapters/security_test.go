package adapters

import (
	"context"
	"encoding/base32"
	"encoding/json"
	identity "github.com/ex-business/ExCommission/internal/identity/domain"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	app "github.com/ex-business/ExCommission/internal/security/application"
	model "github.com/ex-business/ExCommission/internal/security/domain"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestTOTPBoundReplayRateAndClockRollback(t *testing.T) {
	if os.Getenv("TEST_MYSQL_ADDRESS") == "" {
		t.Skip("MySQL required")
	}
	ctx := context.Background()
	store, e := mysql.Open(ctx, os.Getenv("TEST_MYSQL_ADDRESS"), "excommission", os.Getenv("MYSQL_PASSWORD"), "excommission")
	if e != nil {
		t.Fatal(e)
	}
	defer store.DB.Close()
	if e = store.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	scope := foundation.Scope{Tenant: "test-totp-" + strconv.FormatInt(time.Now().UnixNano(), 10), App: "app"}
	defer func() {
		for _, table := range []string{"security_totp_attempts", "security_totp_uses"} {
			if _, e := store.DB.Exec("DELETE FROM "+table+" WHERE tenant_id=?", scope.Tenant); e != nil {
				t.Error(e)
			}
		}
	}()
	seed := []byte("12345678901234567890")
	entries := []Entry{{Tenant: scope.Tenant, App: scope.App, Subject: "operator", Secret: base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(seed)}}
	body, _ := json.Marshal(entries)
	file := filepath.Join(t.TempDir(), "totp.json")
	os.WriteFile(file, body, 0600)
	at := time.Unix(1234567890, 0)
	service := app.Service{UOW: store, Repo: Repository{}, Secrets: FileSecrets{Path: file}, Now: func() time.Time { return at }}
	p := identity.Principal{Tenant: scope.Tenant, App: scope.App, Subject: "operator"}
	payload := map[string]string{"operation": "off"}
	check := func(code, action, key, want string) {
		t.Helper()
		err := service.Verify(model.WithCode(ctx, code), p, action, key, payload)
		if want == "" {
			if err != nil {
				t.Fatal(err)
			}
		} else if err == nil || err.Error() != want {
			t.Fatalf("want %s got %v", want, err)
		}
	}
	check("", "switch", "key", "TOTP_REQUIRED")
	code, _ := model.Code(seed, uint64(at.Unix()/30), 6)
	check(code, "switch", "key", "")
	check(code, "switch", "key", "")
	check(code, "switch", "changed", "TOTP_REPLAY")
	check(code, "different-action", "key", "TOTP_REPLAY")
	check("not-valid", "switch", "key", "TOTP_INVALID")
	check(code, "switch", "key", "TOTP_RATE_LIMITED")
	at = at.Add(time.Minute)
	fresh, _ := model.Code(seed, uint64(at.Unix()/30), 6)
	check(fresh, "switch", "fresh", "")
	at = at.Add(-time.Minute)
	check(code, "switch", "new-after-clock-rollback", "TOTP_REPLAY")
	p.App = "other"
	if _, e := (FileSecrets{Path: file}).Secret(ctx, foundation.Scope{Tenant: p.Tenant, App: p.App}, p.Subject); e == nil {
		t.Fatal("cross scope secret")
	}
	p.App = scope.App
	if e = os.Chmod(file, 0644); e != nil {
		t.Fatal(e)
	}
	check(fresh, "switch", "key", "TOTP_NOT_CONFIGURED")
	if strings.Contains(string(body), "password") {
		t.Fatal("wrong fixture")
	}
}

package adapters

import (
	"context"
	"errors"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	app "github.com/ex-business/ExCommission/internal/wallet/application"
	model "github.com/ex-business/ExCommission/internal/wallet/domain"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestWalletConcurrentCreditsOverflowAndAtomicHistory(t *testing.T) {
	if os.Getenv("TEST_MYSQL_ADDRESS") == "" {
		t.Skip("MySQL required")
	}
	ctx := context.Background()
	store, err := mysql.Open(ctx, os.Getenv("TEST_MYSQL_ADDRESS"), "excommission", os.Getenv("MYSQL_PASSWORD"), "excommission")
	if err != nil {
		t.Fatal(err)
	}
	defer store.DB.Close()
	if err = store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	scope := foundation.Scope{Tenant: "test-wallet-" + strconv.FormatInt(time.Now().UnixNano(), 10), App: "app"}
	defer func() {
		for _, table := range []string{"wallet_accounts", "wallet_transactions", "wallet_changes"} {
			if _, e := store.DB.Exec("DELETE FROM "+table+" WHERE tenant_id=?", scope.Tenant); e != nil {
				t.Error(e)
			}
		}
	}()
	service := app.Service{Repo: Repository{}}
	credit := func(ref, amount string, fail bool) error {
		return store.Within(ctx, func(tx db.Executor) error {
			e := service.Credit(ctx, tx, scope, model.Credit{UserID: "member", Currency: "USD", Amount: amount, Reference: ref, PaymentID: ref, StatementID: "statement"}, time.Now())
			if e != nil {
				return e
			}
			if fail {
				return errors.New("rollback")
			}
			return nil
		})
	}
	if err = credit("rollback", "500", true); err == nil {
		t.Fatal("rollback expected")
	}
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Go(func() {
			if e := credit("credit-"+strconv.Itoa(i), "2", false); e != nil {
				t.Error(e)
			}
		})
	}
	wg.Wait()
	for range 20 {
		wg.Go(func() {
			if e := credit("same-reference", "1", false); e != nil {
				t.Error(e)
			}
		})
	}
	wg.Wait()
	var balance string
	var count int
	if e := store.DB.QueryRow(`SELECT CAST(balance AS CHAR) FROM wallet_accounts WHERE tenant_id=?`, scope.Tenant).Scan(&balance); e != nil || balance != "101" {
		t.Fatal(balance, e)
	}
	for _, table := range []string{"wallet_transactions", "wallet_changes"} {
		if e := store.DB.QueryRow("SELECT COUNT(*) FROM "+table+" WHERE tenant_id=?", scope.Tenant).Scan(&count); e != nil || count != 51 {
			t.Fatal(table, count, e)
		}
	}
	if err = credit("same-reference", "2", false); !errors.Is(err, foundation.ErrConflict) {
		t.Fatal("changed duplicate accepted", err)
	}
	if _, err = store.DB.Exec(`UPDATE wallet_accounts SET balance=? WHERE tenant_id=?`, strings.Repeat("9", 38), scope.Tenant); err != nil {
		t.Fatal(err)
	}
	if err = credit("overflow", "1", false); err == nil || err.Error() != "AMOUNT_OUT_OF_RANGE" {
		t.Fatal(err)
	}
	if err = credit("invalid", "01", false); !errors.Is(err, foundation.ErrValidation) {
		t.Fatal(err)
	}
}

package adapters

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	model "github.com/ex-business/ExCommission/internal/executionrouting/domain"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
)

// 只使用独立测试租户，真实事务验证互斥、回滚和升级前历史保护，不操作已有业务或钱包。
func TestOwnershipTransactionsAndLegacy(t *testing.T) {
	address := os.Getenv("TEST_MYSQL_ADDRESS")
	if address == "" {
		t.Skip("需要MySQL测试连接")
	}
	ctx := context.Background()
	store, err := mysql.Open(ctx, address, "excommission", os.Getenv("MYSQL_PASSWORD"), "excommission")
	if err != nil {
		t.Fatal(err)
	}
	defer store.DB.Close()
	if err = store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	repo := Repository{}
	if err = repo.Ensure(ctx, store.DB); err != nil {
		t.Fatal(err)
	}
	sc := foundation.Scope{Tenant: fmt.Sprintf("test-execution-%d", time.Now().UnixNano()), App: "commission"}
	defer func() {
		for _, table := range []string{"execution_route_receipts", "execution_route_versions", "execution_route_heads", "execution_ownership", "integration_event_keys", "affiliate_business_facts"} {
			if _, e := store.DB.Exec("DELETE FROM "+table+" WHERE tenant_id=?", sc.Tenant); e != nil {
				t.Error(e)
			}
		}
	}()
	acquire := func(scope foundation.Scope, c model.Claim) error {
		return store.Within(ctx, func(tx db.Executor) error { return repo.Acquire(ctx, tx, scope, c, time.Now()) })
	}
	c := model.Claim{Source: "sandbox", EventID: "e", BusinessID: "b", Kind: "BET_SETTLED", Engine: model.General, At: time.Now()}
	if err = acquire(sc, c); err != nil {
		t.Fatal(err)
	}
	if err = acquire(sc, c); err != nil {
		t.Fatal("原引擎重试必须兼容", err)
	}
	other := c
	other.Engine = model.Affiliate
	other.EventID = "new-event"
	if err = acquire(sc, other); !errors.Is(err, model.ErrOwned) {
		t.Fatal("换事件ID不能跨引擎", err)
	}
	other = c
	other.Engine = model.Affiliate
	other.BusinessID = "new-business"
	if err = acquire(sc, other); !errors.Is(err, model.ErrOwned) {
		t.Fatal("复用事件ID不能跨引擎", err)
	}
	other = c
	other.Engine = model.Affiliate
	other.Source = "another-source"
	if err = acquire(sc, other); err != nil {
		t.Fatal("独立来源应通过", err)
	}
	other = c
	other.Engine = model.Affiliate
	isolated := sc
	isolated.App = "another-app"
	if err = acquire(isolated, other); err != nil {
		t.Fatal("应用应隔离", err)
	}
	otherTenant := sc
	otherTenant.Tenant += "-isolated"
	if err = acquire(otherTenant, other); err != nil {
		t.Fatal("站点租户应隔离", err)
	}
	defer store.DB.Exec("DELETE FROM execution_route_receipts WHERE tenant_id=?", otherTenant.Tenant)
	defer store.DB.Exec("DELETE FROM execution_ownership WHERE tenant_id=?", otherTenant.Tenant)
	defer store.DB.Exec("DELETE FROM execution_route_heads WHERE tenant_id=?", otherTenant.Tenant)
	// 外层失败必须释放归属，不能因失败尝试阻塞正确引擎。
	rollback := c
	rollback.BusinessID = "rollback"
	rollback.EventID = "rollback"
	sentinel := errors.New("测试事务回滚")
	err = store.Within(ctx, func(tx db.Executor) error {
		if e := repo.Acquire(ctx, tx, sc, rollback, time.Now()); e != nil {
			return e
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	rollback.Engine = model.Affiliate
	if err = acquire(sc, rollback); err != nil {
		t.Fatal("回滚后应可受理", err)
	}
	// 同一事务多次调用及不同引擎并发，只能有一个成功提交。
	race := c
	race.EventID = "race"
	race.BusinessID = "race"
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, engine := range []string{model.General, model.Affiliate} {
		wg.Add(1)
		go func(engine string) { defer wg.Done(); v := race; v.Engine = engine; results <- acquire(sc, v) }(engine)
	}
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for e := range results {
		if e == nil {
			success++
		} else if errors.Is(e, model.ErrOwned) {
			conflict++
		} else {
			t.Fatal(e)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("并发受理应仅一个引擎胜出: %d/%d", success, conflict)
	}
	// 尚未登记归属的旧通用事件也不能进入代理引擎。
	if _, err = store.DB.Exec(`INSERT INTO integration_event_keys VALUES(?,?,?,?,?,?,?,NULL)`, sc.Tenant, sc.App, "sandbox", "legacy-general", "legacy-general", "BET_SETTLED", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	legacy := c
	legacy.Engine = model.Affiliate
	legacy.EventID = "legacy-general"
	legacy.BusinessID = "legacy-general"
	if err = acquire(sc, legacy); !errors.Is(err, model.ErrOwned) {
		t.Fatal("旧通用事件不得双计", err)
	}
	if _, err = store.DB.Exec(`INSERT INTO affiliate_business_facts (tenant_id,app_id,source_id,event_id,business_id,kind,user_id,facts_hash,body,business_time,received_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, sc.Tenant, sc.App, "sandbox", "legacy-affiliate", "legacy-affiliate", "BET_SETTLED", "member", "hash", `{}`, time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}
	legacy.Engine = model.General
	legacy.EventID = "legacy-affiliate"
	legacy.BusinessID = "legacy-affiliate"
	if err = acquire(sc, legacy); !errors.Is(err, model.ErrOwned) {
		t.Fatal("旧代理事实不得双计", err)
	}
}

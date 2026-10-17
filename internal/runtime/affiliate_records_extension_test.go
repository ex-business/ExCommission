package runtime

import (
	"context"
	"encoding/json"
	repo "github.com/ex-business/ExCommission/internal/affiliateflow/adapters"
	model "github.com/ex-business/ExCommission/internal/affiliateflow/domain"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAffiliateSortedRecordsProfileScopeAndFilters(t *testing.T) {
	if os.Getenv("TEST_MYSQL_ADDRESS") == "" {
		t.Skip("需要真实MySQL")
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
	sc := foundation.Scope{Tenant: "test-affiliate-records-" + newID(), App: "test"}
	defer func() {
		for _, table := range []string{"affiliate_reward_history", "affiliate_business_rewards", "users_external"} {
			store.DB.Exec("DELETE FROM "+table+" WHERE tenant_id=?", sc.Tenant)
		}
	}()
	at := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	_, e = store.DB.Exec(`INSERT INTO users_external VALUES(?,?,?,'sandbox','external-member','真实展示名称',?)`, sc.Tenant, sc.App, "member", at)
	if e != nil {
		t.Fatal(e)
	}
	last := at
	file := filepath.Join(t.TempDir(), "profiles.json")
	vip := 2
	raw, _ := json.Marshal([]any{map[string]any{"tenant_id": sc.Tenant, "app_id": sc.App, "profiles": []any{map[string]any{"source_id": "sandbox", "external_user_id": "external-member", "display_id": "1234567", "display_name": "代理资料", "vip_level": vip, "avatar_url": "/avatar.svg", "last_login_at": last, "last_login_ip": "203.0.113.8"}}}, map[string]any{"tenant_id": "other", "app_id": sc.App, "profiles": []any{map[string]any{"source_id": "sandbox", "external_user_id": "external-member", "display_id": "9999999", "display_name": "越站资料", "vip_level": nil, "avatar_url": "", "last_login_at": nil, "last_login_ip": ""}}}})
	if e = os.WriteFile(file, raw, 0600); e != nil {
		t.Fatal(e)
	}
	t.Setenv("AFFILIATE_MEMBER_CATALOG_FILE", file)
	r := repo.Repository{}
	for i, amount := range []string{"100", "200", "200"} {
		claim := at.Add(time.Duration(i) * time.Minute)
		reward := model.Reward{ID: strings.Repeat(string(rune('a'+i)), 64), UserID: "member", AgentID: "agent", Kind: "COMMISSION", Day: at.Format("2006-01-02"), Currency: "USD", Amount: amount, Status: "CLAIMED", UnlockedAt: claim, ClaimedAt: &claim, ClaimID: "claim-" + string(rune('a'+i)), ClaimIP: "203.0.113.10", UpdatedAt: claim, Revision: 1}
		if e = r.SaveReward(ctx, store.DB, sc, reward); e != nil {
			t.Fatal(e)
		}
	}
	for _, sort := range []string{"amount_desc", "time_desc"} {
		f := model.RecordFilter{From: at.Add(-time.Minute), To: at.Add(time.Hour), AsOf: at.Add(59 * time.Minute), Limit: 1, Sort: sort}
		seen := []string{}
		for {
			page, err := r.Records(ctx, store.DB, sc, f, false)
			if err != nil {
				t.Fatal(err)
			}
			if len(page.Rows) != 1 || page.Summary[0].Total != "500" {
				t.Fatal(page)
			}
			row := page.Rows[0]
			if row.Profile.DisplayID != "1234567" || row.Profile.LastIP != "" || row.ClaimIP != "" {
				t.Fatal("资料串站或权限失效", row)
			}
			seen = append(seen, row.ID)
			if page.Next == nil {
				break
			}
			f.After = *page.Next
			f.Snapshot = page.Snapshot
		}
		if len(seen) != 3 || seen[0] != strings.Repeat("c", 64) || seen[1] != strings.Repeat("b", 64) || seen[2] != strings.Repeat("a", 64) {
			t.Fatal("金额同值时日期次序或分页重复", seen)
		}
	}
	f := model.RecordFilter{From: at.Add(-time.Minute), To: at.Add(time.Hour), AsOf: at.Add(59 * time.Minute), Limit: 100, ClaimID: "claim-b", ClaimIP: "203.0.113.10", TimeField: "claimed_at", BusinessDay: at.Format("2006-01-02")}
	page, err := r.Records(ctx, store.DB, sc, f, true)
	if err != nil || len(page.Rows) != 1 || page.Rows[0].Profile.LastIP != "203.0.113.8" {
		t.Fatal(page, err)
	}
	f.ClaimID = ""
	f.After = "tampered"
	f.Sort = "amount_desc"
	if _, err = r.Records(ctx, store.DB, sc, f, true); err == nil {
		t.Fatal("伪造游标被接受")
	}
}

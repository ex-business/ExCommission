package runtime

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	qrepo "github.com/ex-business/ExCommission/internal/qualification/adapters"
	q "github.com/ex-business/ExCommission/internal/qualification/domain"
	registration "github.com/ex-business/ExCommission/internal/registration/domain"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMemberQualificationFactsAndHistory(t *testing.T) {
	if os.Getenv("TEST_MYSQL_ADDRESS") == "" {
		t.Skip("需要独立本地MySQL")
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
	tenant := "test-qualification-" + newID()
	app := "commission"
	token := strings.Repeat("o", 32)
	key := strings.Repeat("q", 32)
	registrationKey := strings.Repeat("r", 32)
	// 随机测试租户隔离所有规则、会员和事实，退出只清理本租户，保留页面演示数据。
	defer func() {
		rows, e := store.DB.Query(`SELECT TABLE_NAME FROM information_schema.columns WHERE TABLE_SCHEMA=DATABASE() AND COLUMN_NAME='tenant_id'`)
		if e != nil {
			t.Error(e)
			return
		}
		tables := []string{}
		for rows.Next() {
			var table string
			if e = rows.Scan(&table); e != nil {
				t.Error(e)
				return
			}
			if !regexp.MustCompile(`^[a-z_]+$`).MatchString(table) {
				t.Fatal("非法表名")
			}
			tables = append(tables, table)
		}
		rows.Close()
		for _, table := range tables {
			if _, e = store.DB.Exec("DELETE FROM `"+table+"` WHERE tenant_id=?", tenant); e != nil {
				t.Error(e)
			}
		}
	}()
	clock := time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC)
	old := timeSource
	timeSource = func() time.Time { return clock }
	defer func() { timeSource = old }()
	for name, value := range map[string]string{"APP_MODE": "local", "LOCAL_TENANT": tenant, "LOCAL_APP": app, "LOCAL_OPERATOR_TOKEN": token, "LOCAL_APPROVER_TOKEN": strings.Repeat("a", 32), "LOCAL_CURSOR_KEY": strings.Repeat("c", 32), "LOCAL_SOURCE_KEY": strings.Repeat("s", 32), "LOCAL_PAYMENT_KEY": strings.Repeat("p", 32), "LOCAL_USERS_FILE": "", "LOCAL_REGISTRATION_ENABLED": "true", "LOCAL_REGISTRATION_KEY": registrationKey, "LOCAL_REGISTRATION_URL": "", "LOCAL_QUALIFICATION_ENABLED": "true", "LOCAL_QUALIFICATION_KEY": key, "AUTH_LOCAL_ACCOUNTS_FILE": "", "AUTH_EXTERNAL_FILE": ""} {
		t.Setenv(name, value)
	}
	handler, err := businessHandler(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path, idem string, input any, prefix, signKey, credential string) (int, map[string]any) {
		raw, _ := json.Marshal(input)
		req := httptest.NewRequest(method, path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", idem)
		if credential != "" {
			req.Header.Set("Authorization", "Bearer "+credential)
		}
		if prefix != "" {
			mac := hmac.New(sha256.New, []byte(signKey))
			mac.Write([]byte(prefix + "\n" + tenant + "\n" + app + "\nsandbox\n"))
			mac.Write(raw)
			header := "X-Qualification-Signature"
			if prefix == "registration-v1" {
				header = "X-Registration-Signature"
			}
			req.Header.Set(header, hex.EncodeToString(mac.Sum(nil)))
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		answer := map[string]any{}
		if e := json.Unmarshal(rec.Body.Bytes(), &answer); e != nil {
			t.Error(e)
		}
		return rec.Code, answer
	}
	check := func(code int, answer map[string]any) map[string]any {
		t.Helper()
		if code != 200 {
			t.Fatalf("HTTP %d: %v", code, answer)
		}
		return answer["data"].(map[string]any)
	}
	register := func(external string, invite *string) map[string]any {
		t.Helper()
		return check(call("POST", "/v1/registrations", newID(), registration.Event{SchemaVersion: "1", SourceID: "sandbox", EventID: newID(), ExternalUserID: external, DisplayName: "达标测试会员", RegisteredAt: clock, InvitationCode: invite}, "registration-v1", registrationKey, token))
	}
	parent := register("parent", nil)
	invitation := check(call("POST", "/v1/registration-invitations", newID(), registration.InvitationRequest{SourceID: "sandbox", AgentID: parent["agent_id"].(string)}, "", "", token))
	invite := invitation["invitation_code"].(string)
	register("child", &invite)
	policy := q.Policy{ID: "q1", Source: "sandbox", Currency: "USD", Deposit: "5000", Bet: "10000", Venues: []string{"SPORTS"}}
	check(call("POST", "/v1/member-qualification-policies", newID(), policy, "", "", token))
	changed := policy
	changed.Deposit = "1"
	code, _ := call("POST", "/v1/member-qualification-policies", newID(), changed, "", "", token)
	if code != 409 {
		t.Fatalf("旧门槛被覆盖 %d", code)
	}
	query := func(external string) map[string]any {
		t.Helper()
		return check(call("GET", "/v1/member-qualifications?policy_id=q1&source_id=sandbox&external_user_id="+url.QueryEscape(external), "", nil, "", "", token))
	}
	if zero := query("child"); zero["deposit_total_minor"] != "0" || zero["is_effective"] != false {
		t.Fatal(zero)
	}
	registeredAt := clock
	clock = clock.Add(24 * time.Hour)
	fact := func(business, kind, amount string, seconds int) q.Fact {
		f := q.Fact{Schema: "1", PolicyID: "q1", Source: "sandbox", EventID: newID(), BusinessID: business, Type: kind, ExternalUser: "child", At: registeredAt.Add(time.Duration(seconds) * time.Second), Amount: amount, Currency: "USD"}
		if kind == "BET_SETTLED" {
			venue := "SPORTS"
			f.Venue = &venue
		}
		return f
	}
	send := func(f q.Fact) (int, map[string]any) {
		return call("POST", "/v1/member-qualification-facts", newID(), f, "qualification-v1", key, token)
	}
	bet := fact("bet1", "BET_SETTLED", "9000", 1)
	first := check(send(bet))
	if first["is_effective"] != false {
		t.Fatal(first)
	}
	deposit := fact("deposit1", "DEPOSIT_SUCCEEDED", "3000", 2)
	check(send(deposit))
	check(send(fact("deposit2", "DEPOSIT_SUCCEEDED", "2000", 3)))
	state := query("child")
	if state["is_effective"] != false || state["deposit_total_minor"] != "5000" {
		t.Fatal(state)
	}
	finalBet := fact("bet2", "BET_SETTLED", "1000", 4)
	qualified := check(send(finalBet))
	if qualified["is_effective"] != true || qualified["qualified_at"] != finalBet.At.Format(time.RFC3339Nano) || qualified["revision"] != float64(4) {
		t.Fatal(qualified)
	}
	// 事件号与真实业务号双重防重，换投递键或事件别名都不能再次累计或发布outbox。
	check(send(finalBet))
	alias := finalBet
	alias.EventID = newID()
	same := check(send(alias))
	if same["revision"] != float64(4) || same["first_observed_at"] != qualified["first_observed_at"] {
		t.Fatal(same)
	}
	conflict := alias
	conflict.BusinessID = "other-bet"
	code, _ = send(conflict)
	if code != 409 {
		t.Fatalf("来源事件号复用 %d", code)
	}
	conflict = deposit
	conflict.EventID = newID()
	conflict.Amount = "9999"
	code, _ = send(conflict)
	if code != 409 {
		t.Fatalf("订单金额被改写 %d", code)
	}
	late := fact("late-bet", "BET_SETTLED", "1000", 0)
	late.At = registeredAt.Add(500 * time.Millisecond)
	corrected := check(send(late))
	want := registeredAt.Add(3 * time.Second).Format(time.RFC3339Nano)
	if corrected["qualified_at"] != want || corrected["first_observed_at"] != qualified["first_observed_at"] {
		t.Fatal("迟到事实的业务达标时点或首次观测错误", corrected)
	}
	// 不同事件同时累计同一会员，必须包含所有已提交事实，不能受旧快照影响丢失金额。
	var wg sync.WaitGroup
	failures := make(chan string, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		f := fact(fmt.Sprintf("parallel-%d", i), "DEPOSIT_SUCCEEDED", "1", 10+i)
		go func(f q.Fact) {
			defer wg.Done()
			code, answer := send(f)
			if code != 200 {
				failures <- fmt.Sprintf("%d:%v", code, answer)
			}
		}(f)
	}
	wg.Wait()
	close(failures)
	for failure := range failures {
		t.Error(failure)
	}
	state = query("child")
	if state["deposit_total_minor"] != "5008" || state["valid_bet_total_minor"] != "11000" || state["revision"] != float64(13) {
		t.Fatal("并发累计丢失", state)
	}
	var count int
	if err = store.DB.QueryRow(`SELECT COUNT(*) FROM transaction_outbox WHERE tenant_id=? AND topic='member.qualification.updated.v1'`, tenant).Scan(&count); err != nil || count != 13 {
		t.Fatalf("outbox防重 %d %v", count, err)
	}
	for _, invalid := range []q.Fact{fact("failed", "DEPOSIT_FAILED", "100", 20), fact("negative", "DEPOSIT_SUCCEEDED", "-1", 20), fact("fractional", "DEPOSIT_SUCCEEDED", "1.5", 20)} {
		code, _ = send(invalid)
		if code != 400 {
			t.Fatalf("非法事实未拒绝 %d", code)
		}
	}
	wrongVenue := fact("venue", "BET_SETTLED", "100", 20)
	venue := "CASINO"
	wrongVenue.Venue = &venue
	code, _ = send(wrongVenue)
	if code != 400 {
		t.Fatalf("非参与场馆未拒绝 %d", code)
	}
	wrongCurrency := fact("currency", "DEPOSIT_SUCCEEDED", "100", 20)
	wrongCurrency.Currency = "BTC"
	code, _ = send(wrongCurrency)
	if code != 400 {
		t.Fatalf("跨币种事实未拒绝 %d", code)
	}
	code, _ = call("POST", "/v1/member-qualification-facts", newID(), finalBet, "qualification-v1", "wrong-key", token)
	if code != 403 {
		t.Fatalf("错误签名 %d", code)
	}
	code, _ = call("POST", "/v1/member-qualification-facts", newID(), finalBet, "qualification-v1", key, "")
	if code != 401 {
		t.Fatalf("匿名接入 %d", code)
	}
	if final := query("child"); final["revision"] != float64(13) || final["deposit_total_minor"] != "5008" {
		t.Fatal("非法事实未回滚", final)
	}
	rootDeposit := fact("root-deposit", "DEPOSIT_SUCCEEDED", "5000", 21)
	rootDeposit.ExternalUser = "parent"
	check(send(rootDeposit))
	rootBet := fact("root-bet", "BET_SETTLED", "10000", 22)
	rootBet.ExternalUser = "parent"
	rootState := check(send(rootBet))
	if rootState["is_effective"] != false || rootState["qualified_at"] != nil {
		t.Fatal("非邀请注册被算为有效邀请会员", rootState)
	}

	// 四类统计按真实达标时间筛选；累计身份仍永久有效，范围只影响当期人数。
	cutoff := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		kind string
		want int
	}{{q.NaturalDay, 1}, {q.NaturalWeek, 2}, {q.NaturalMonth, 3}, {q.Forever, 4}} {
		configured := policy
		configured.ID = "stats-" + c.kind
		configured.Statistics = &q.StatisticsPolicy{Range: c.kind, Timezone: "UTC"}
		check(call("POST", "/v1/member-qualification-policies", newID(), configured, "", "", token))
		got := check(call("GET", "/v1/member-qualification-policies/"+configured.ID, "", nil, "", "", token))
		if got["member_statistics"].(map[string]any)["range"] != c.kind {
			t.Fatal(got)
		}
		altered := configured
		copy := *configured.Statistics
		copy.Range = q.Forever
		if c.kind == q.Forever {
			copy.Range = q.NaturalDay
		}
		altered.Statistics = &copy
		status, _ := call("POST", "/v1/member-qualification-policies", newID(), altered, "", "", token)
		if status != 409 {
			t.Fatal("创建后统计范围被覆盖", status)
		}
		copy = *configured.Statistics
		copy.Timezone = "Asia/Shanghai"
		altered.Statistics = &copy
		status, _ = call("POST", "/v1/member-qualification-policies", newID(), altered, "", "", token)
		if status != 409 {
			t.Fatal("创建后统计时区被覆盖", status)
		}
		scope := foundation.Scope{Tenant: tenant, App: app}
		err = store.Within(ctx, func(tx db.Executor) error {
			repo := qrepo.Repository{}
			times := []time.Time{cutoff.Add(-time.Hour), time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC), time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC), time.Date(2026, 9, 30, 8, 0, 0, 0, time.UTC), cutoff}
			for i, at := range times {
				id := fmt.Sprintf("stats-member-%d", i)
				if e := repo.LockMember(ctx, tx, scope, configured.ID, "sandbox", id); e != nil {
					return e
				}
				state := q.Status{PolicyID: configured.ID, Source: "sandbox", ExternalUser: id, AgentID: id, UserID: id, Currency: "USD", Effective: true, QualifiedAt: &at, FirstObservedAt: &clock, Revision: 1}
				if e := repo.SaveStatus(ctx, tx, scope, state, clock); e != nil {
					return e
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		path := "/v1/member-qualification-statistics?policy_id=" + configured.ID + "&source_id=sandbox&as_of=" + url.QueryEscape(cutoff.Format(time.RFC3339Nano))
		data := check(call("GET", path, "", nil, "", "", token))
		if data["count"] != float64(c.want) || len(data["agent_ids"].([]any)) != c.want {
			t.Fatal("统计范围筛选错误", c.kind, data)
		}
		if data["window"].(map[string]any)["range"] != c.kind {
			t.Fatal(data)
		}
	}
	// 旧规则未携带范围继续永久口径，不能因升级丢失原累计会员。
	legacy := check(call("GET", "/v1/member-qualification-statistics?policy_id=q1&source_id=sandbox&as_of="+url.QueryEscape(cutoff.Format(time.RFC3339Nano)), "", nil, "", "", token))
	if legacy["window"].(map[string]any)["range"] != q.Forever || legacy["count"] != float64(1) {
		t.Fatal("旧规则兼容失败", legacy)
	}
	code, _ = call("GET", "/v1/member-qualification-statistics?policy_id=q1&source_id=other&as_of="+url.QueryEscape(cutoff.Format(time.RFC3339Nano)), "", nil, "", "", token)
	if code != 400 {
		t.Fatal("跨来源读取未拒绝", code)
	}
	code, _ = call("GET", "/v1/member-qualification-statistics?policy_id=q1&source_id=sandbox&as_of=2099-01-01T00:00:00Z", "", nil, "", "", token)
	if code != 400 {
		t.Fatal("未来截止未拒绝", code)
	}
	code, _ = call("GET", "/v1/member-qualification-statistics?policy_id=q1&source_id=sandbox&as_of="+url.QueryEscape(cutoff.Format(time.RFC3339Nano)), "", nil, "", "", "")
	if code != 401 {
		t.Fatal("匿名统计未拒绝", code)
	}
	t.Log("双门槛、首次达标时点、迟到事实、并发累计、双键幂等、场馆/币种/来源隔离与原子回滚通过")
}

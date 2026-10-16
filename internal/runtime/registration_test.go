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
	model "github.com/ex-business/ExCommission/internal/registration/domain"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRegistrationInvitationLifecycle(t *testing.T) {
	if os.Getenv("TEST_MYSQL_ADDRESS") == "" {
		t.Skip("需要独立本地 MySQL")
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
	tenant := "test-registration-" + newID()
	app := "registration"
	token := strings.Repeat("o", 32)
	signatureKey := strings.Repeat("r", 32)
	// 测试只清理随机租户的数据，绝不触碰页面演示租户。
	defer func() {
		rows, e := store.DB.Query(`SELECT TABLE_NAME FROM information_schema.columns WHERE TABLE_SCHEMA=DATABASE() AND COLUMN_NAME='tenant_id'`)
		if e != nil {
			t.Error(e)
			return
		}
		var tables []string
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
	for k, v := range map[string]string{"APP_MODE": "local", "LOCAL_TENANT": tenant, "LOCAL_APP": app, "LOCAL_OPERATOR_TOKEN": token, "LOCAL_APPROVER_TOKEN": strings.Repeat("a", 32), "LOCAL_CURSOR_KEY": strings.Repeat("c", 32), "LOCAL_SOURCE_KEY": strings.Repeat("s", 32), "LOCAL_PAYMENT_KEY": strings.Repeat("p", 32), "LOCAL_USERS_FILE": "", "LOCAL_REGISTRATION_ENABLED": "true", "LOCAL_REGISTRATION_KEY": signatureKey, "LOCAL_REGISTRATION_URL": "https://members.example/register?channel=test", "AUTH_LOCAL_ACCOUNTS_FILE": "", "AUTH_EXTERNAL_FILE": ""} {
		t.Setenv(k, v)
	}
	handler, err := businessHandler(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	send := func(path, key string, input any, credential, signingScope string) (int, map[string]any) {
		raw, _ := json.Marshal(input)
		req := httptest.NewRequest("POST", path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", key)
		if credential != "" {
			req.Header.Set("Authorization", "Bearer "+credential)
		}
		if signingScope != "" {
			mac := hmac.New(sha256.New, []byte(signatureKey))
			mac.Write([]byte("registration-v1\n" + signingScope + "\n" + app + "\nsandbox\n"))
			mac.Write(raw)
			req.Header.Set("X-Registration-Signature", hex.EncodeToString(mac.Sum(nil)))
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		var answer map[string]any
		if e := json.Unmarshal(rec.Body.Bytes(), &answer); e != nil {
			t.Error(e)
		}
		return rec.Code, answer
	}
	check := func(status int, body map[string]any) map[string]any {
		t.Helper()
		if status != 200 {
			t.Fatalf("请求失败 %d: %v", status, body)
		}
		return body["data"].(map[string]any)
	}
	event := func(user string, code *string) model.Event {
		return model.Event{SchemaVersion: "1", SourceID: "sandbox", EventID: newID(), ExternalUserID: user, DisplayName: "测试注册会员", RegisteredAt: clockUTC(), InvitationCode: code}
	}
	rootEvent := event("root", nil)
	status, body := send("/v1/registrations", "anonymous", rootEvent, "", tenant)
	if status != 401 {
		t.Fatalf("匿名注册=%d", status)
	}
	status, body = send("/v1/registrations", "unsigned", rootEvent, token, "")
	if status != 403 {
		t.Fatalf("无签名注册=%d", status)
	}
	status, body = send("/v1/registrations", "wrong-scope", rootEvent, token, "other-site")
	if status != 403 {
		t.Fatalf("跨站签名=%d", status)
	}
	status, body = send("/v1/registrations", "root", rootEvent, token, tenant)
	root := check(status, body)
	if root["parent_agent_id"] != nil {
		t.Fatal(root)
	}
	status, body = send("/v1/registration-invitations", "invite", model.InvitationRequest{SourceID: "sandbox", AgentID: root["agent_id"].(string)}, token, "")
	invitation := check(status, body)
	code := invitation["invitation_code"].(string)
	if !strings.Contains(invitation["registration_url"].(string), "channel=test&ic="+code) {
		t.Fatal(invitation)
	}
	childEvent := event("child", &code)
	status, body = send("/v1/registrations", "child", childEvent, token, tenant)
	child := check(status, body)
	if child["parent_agent_id"] != root["agent_id"] {
		t.Fatal(child)
	}
	// 即使改投递键/事件号也返回首次结果；旧事件别名不能挪给另一个会员。
	childEvent.EventID = "alias-event"
	status, body = send("/v1/registrations", "child-alias", childEvent, token, tenant)
	again := check(status, body)
	if again["agent_id"] != child["agent_id"] {
		t.Fatal(again)
	}
	aliasConflict := childEvent
	aliasConflict.ExternalUserID = "attacker"
	status, _ = send("/v1/registrations", "alias-reuse", aliasConflict, token, tenant)
	if status != 409 {
		t.Fatalf("事件号复用=%d", status)
	}
	altered := childEvent
	altered.InvitationCode = nil
	status, _ = send("/v1/registrations", "change-parent", altered, token, tenant)
	if status != 409 {
		t.Fatalf("改上级=%d", status)
	}
	unknown := "unknown-code"
	invalid := event("invalid-code", &unknown)
	status, _ = send("/v1/registrations", "invalid", invalid, token, tenant)
	if status != 404 {
		t.Fatalf("未知凭证=%d", status)
	}
	future := event("future", nil)
	future.RegisteredAt = clockUTC().Add(time.Hour)
	status, _ = send("/v1/registrations", "future", future, token, tenant)
	if status != 400 {
		t.Fatalf("未来时间=%d", status)
	}
	before := event("before-invite", &code)
	before.RegisteredAt = rootEvent.RegisteredAt
	status, _ = send("/v1/registrations", "before", before, token, tenant)
	if status != 400 {
		t.Fatalf("倒填邀请=%d", status)
	}
	// 唯一注册事实与空间锁同时覆盖并发不同幂等键，验证只有一个用户/代理/初始历史。
	parallel := event("concurrent", &code)
	var wg sync.WaitGroup
	failures := make(chan string, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s, b := send("/v1/registrations", fmt.Sprintf("parallel-%d", i), parallel, token, tenant)
			if s != 200 {
				failures <- fmt.Sprintf("%d:%v", s, b)
			}
		}(i)
	}
	wg.Wait()
	close(failures)
	for failure := range failures {
		t.Error(failure)
	}
	var count int
	if err = store.DB.QueryRow(`SELECT COUNT(*) FROM registration_receipts WHERE tenant_id=? AND external_user_id='concurrent'`, tenant).Scan(&count); err != nil || count != 1 {
		t.Fatalf("并发结果 %d %v", count, err)
	}
	// 注册完成通知只为三位新代理各写一条，重复注册与事件别名不会再次投递。
	if err = store.DB.QueryRow(`SELECT COUNT(*) FROM transaction_outbox WHERE tenant_id=? AND topic='registration.completed.v1'`, tenant).Scan(&count); err != nil || count != 3 {
		t.Fatalf("注册 outbox 防重 %d %v", count, err)
	}

	// 停用事务先锁住邀请人，注册必须等待其提交并重读状态，不能读取旧 ACTIVE 完成绑定。
	disabling, err := store.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer disabling.Rollback()
	if _, err = disabling.Exec(`UPDATE agent_accounts SET status='DISABLED' WHERE tenant_id=? AND agent_id=?`, tenant, root["agent_id"]); err != nil {
		t.Fatal(err)
	}
	concurrentStop := event("concurrent-disable", &code)
	stopResult := make(chan int, 1)
	go func() {
		code, _ := send("/v1/registrations", "concurrent-disable", concurrentStop, token, tenant)
		stopResult <- code
	}()
	select {
	case code := <-stopResult:
		t.Fatalf("注册未等待邀请人停用事务，返回 %d", code)
	case <-time.After(200 * time.Millisecond):
	}
	if err = disabling.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-stopResult:
		if code != 409 {
			t.Fatalf("并发停用后注册=%d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("停用提交后注册未完成")
	}
	if err = store.DB.QueryRow(`SELECT COUNT(*) FROM users_external WHERE tenant_id=? AND external_user_id='concurrent-disable'`, tenant).Scan(&count); err != nil || count != 0 {
		t.Fatalf("并发停用失败未回滚 %d %v", count, err)
	}
	// 邀请人停用后不能继续使用旧码；失败事务也不得留下用户行。
	if _, err = store.DB.Exec(`UPDATE agent_accounts SET status='DISABLED' WHERE tenant_id=? AND agent_id=?`, tenant, root["agent_id"]); err != nil {
		t.Fatal(err)
	}
	blocked := event("disabled-inviter", &code)
	status, _ = send("/v1/registrations", "disabled", blocked, token, tenant)
	if status != 409 {
		t.Fatalf("停用邀请人=%d", status)
	}
	if err = store.DB.QueryRow(`SELECT COUNT(*) FROM users_external WHERE tenant_id=? AND external_user_id='disabled-inviter'`, tenant).Scan(&count); err != nil || count != 0 {
		t.Fatalf("失败未回滚 %d %v", count, err)
	}
	t.Log("注册无邀请/邀请归属/签名隔离/别名防重/关系不可覆盖/并发/失败回滚全部通过")
}

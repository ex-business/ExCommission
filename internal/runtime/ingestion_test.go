package runtime

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	affiliate "github.com/ex-business/ExCommission/internal/affiliate/domain"
	flowrepo "github.com/ex-business/ExCommission/internal/affiliateflow/adapters"
	flowmodel "github.com/ex-business/ExCommission/internal/affiliateflow/domain"
	identity "github.com/ex-business/ExCommission/internal/identity/domain"
	repo "github.com/ex-business/ExCommission/internal/ingestion/adapters"
	model "github.com/ex-business/ExCommission/internal/ingestion/domain"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// 使用独立租户真实MySQL验证配置发布后接入现有事实链路，不直接插入奖励或钱包数据。
func TestConfigurableIngestionApprovalMappingAndBoundaries(t *testing.T) {
	if os.Getenv("TEST_MYSQL_ADDRESS") == "" {
		t.Skip("需要MySQL测试连接")
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
	sc := foundation.Scope{Tenant: "test-ingestion-" + newID(), App: "commission"}
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
				break
			}
			if regexp.MustCompile(`^[a-z_]+$`).MatchString(table) {
				tables = append(tables, table)
			}
		}
		rows.Close()
		for _, table := range tables {
			if _, e = store.DB.Exec("DELETE FROM `"+table+"` WHERE tenant_id=?", sc.Tenant); e != nil {
				t.Error(e)
			}
		}
	}()
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	previous := timeSource
	timeSource = func() time.Time { return now }
	defer func() { timeSource = previous }()
	op, checker := strings.Repeat("o", 32), strings.Repeat("a", 32)
	secret := strings.Repeat("s", 32)
	regKey := strings.Repeat("r", 32)
	keys := filepath.Join(t.TempDir(), "keys.json")
	raw, _ := json.Marshal([]repo.Key{{Ref: "sports-key", Tenant: sc.Tenant, App: sc.App, Secret: secret}})
	if e = os.WriteFile(keys, raw, 0600); e != nil {
		t.Fatal(e)
	}
	for k, v := range map[string]string{"APP_MODE": "local", "LOCAL_SOURCE_KEY": strings.Repeat("x", 32), "LOCAL_PAYMENT_KEY": strings.Repeat("p", 32), "LOCAL_TENANT": sc.Tenant, "LOCAL_APP": sc.App, "LOCAL_OPERATOR_TOKEN": op, "LOCAL_APPROVER_TOKEN": checker, "LOCAL_CURSOR_KEY": strings.Repeat("c", 32), "LOCAL_AFFILIATE_FLOW_ENABLED": "true", "LOCAL_AFFILIATE_FLOW_KEY": strings.Repeat("f", 32), "AFFILIATE_CLIENT_CONTEXT_KEY": strings.Repeat("i", 32), "LOCAL_REGISTRATION_ENABLED": "true", "LOCAL_REGISTRATION_KEY": regKey, "LOCAL_REGISTRATION_URL": "http://127.0.0.1:18286/", "LOCAL_INGESTION_ENABLED": "true", "AFFILIATE_INGESTION_KEYS_FILE": keys, "OPS_IDENTITY_CONFIG_FILE": "", "LOCAL_USERS_FILE": "", "AUTH_LOCAL_ACCOUNTS_FILE": "", "AUTH_EXTERNAL_FILE": "", "AUTH_MEMBER_SOURCE_FILE": "", "AUTH_TOTP_FILE": "", "AUTH_TOTP_SECRETS": ""} {
		t.Setenv(k, v)
	}
	handler, e := businessHandler(ctx, store)
	if e != nil {
		t.Fatal(e)
	}
	call := func(method, path, token string, in any, want int, key, signature, header string) map[string]any {
		t.Helper()
		raw, _ := json.Marshal(in)
		r := httptest.NewRequest(method, path, bytes.NewReader(raw))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		if key == "" {
			key = newID()
		}
		r.Header.Set("Idempotency-Key", key)
		if signature != "" {
			r.Header.Set(header, signature)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s got%d want%d %s", method, path, w.Code, want, w.Body)
		}
		var out map[string]any
		if e = json.Unmarshal(w.Body.Bytes(), &out); e != nil {
			t.Fatal(e)
		}
		if d, ok := out["data"].(map[string]any); ok {
			return d
		}
		return out
	}
	sign := func(prefix, source string, in any, key string) string {
		raw, _ := json.Marshal(in)
		mac := hmac.New(sha256.New, []byte(key))
		mac.Write([]byte(prefix + "\n" + sc.Tenant + "\n" + sc.App + "\n" + source + "\n"))
		mac.Write(raw)
		return hex.EncodeToString(mac.Sum(nil))
	}
	approve := func(p map[string]any, token string, want int) {
		call("POST", "/v1/approvals/"+p["approval_id"].(string)+"/decisions", token, map[string]any{"decision": "APPROVE", "expected_revision": 0, "object_hash": p["object_hash"], "reason": "独立复核接入配置"}, want, "", "", "")
	}
	cfg := affiliateTestConfig()
	cfg["currency"] = "USD"
	// 真实配置审批同时验证扩展方案能进入发布快照，而非仅领域试算可用。
	policy := &affiliate.CommissionPolicy{Depth: 4}
	for i := 0; i < 2; i++ {
		policy.Grades = append(policy.Grades, affiliate.CommissionGradePolicy{Level: i, Layers: []affiliate.CommissionLayerRule{{Algorithm: "PROPORTIONAL", Rate: 100000}, {Algorithm: "FIXED", Fixed: "77"}, {Algorithm: "TIERED", Tiers: []affiliate.CommissionTier{{From: "0", Rate: 200000}}}, {Algorithm: "FORMULA", Formula: &affiliate.Formula{Op: "DIV", Args: []affiliate.Formula{{Op: "VOLUME"}, {Op: "CONST", Value: "2"}}}, Cap: "99"}}})
	}
	cfg["commission_policy"] = policy
	pending := call("POST", "/v1/affiliate/config-requests", op, map[string]any{"config": cfg, "expected_revision": 0, "reason": "初始化业务配置"}, 202, "", "", "")
	approve(pending, checker, 200)
	preview := call("GET", "/v1/affiliate/calculator?qualified_members=0&layer_minor=200&layer_minor=200&layer_minor=200&layer_minor=200", op, nil, 200, "", "", "")
	if preview["estimate"].(map[string]any)["commission_minor"] != "236" {
		t.Fatal("扩展方案后台试算错误", preview)
	}
	call("GET", "/v1/me/affiliate/calculator?qualified_members=0&layer_minor=200&layer_minor=200&layer_minor=200&layer_minor=200", op, nil, 403, "", "", "")
	call("GET", "/v1/affiliate/calculator?qualified_members=0&level1_minor=200&level2_minor=200&level3_minor=200", op, nil, 400, "", "", "")
	// 修改统计口径只返回独立新方案策略；预检不得更新当前配置或申请审批。
	cfg["grade_period"] = "MONTH"
	changePreview := call("POST", "/v1/affiliate/config-preview", op, map[string]any{"config": cfg, "expected_revision": 1, "reason": "独立方案预检"}, 200, "", "", "")
	if changePreview["requires_independent_plan"] != true {
		t.Fatal("统计周期变更未要求独立方案", changePreview)
	}
	call("POST", "/v1/affiliate/config-preview", checker, map[string]any{"config": cfg, "expected_revision": 1, "reason": "越权预检"}, 403, "", "", "")
	call("POST", "/v1/affiliate/config-requests", op, map[string]any{"config": cfg, "expected_revision": 1, "reason": "拒绝改写旧口径"}, 409, "", "", "")
	cfg["grade_period"] = "WEEK"
	registration := map[string]any{"schema_version": "1", "source_id": "sandbox", "event_id": "reg-member", "external_user_id": "member", "display_name": "接入测试会员", "registered_at": now.Format(time.RFC3339Nano), "invitation_code": nil}
	call("POST", "/v1/registrations", op, registration, 200, "", sign("registration-v1", "sandbox", registration, regKey), "X-Registration-Signature")
	rule := model.Rule{EventType: "payment.done", Version: "1", Target: "DEPOSIT_SUCCEEDED", AmountUnit: "MAJOR", CurrencyScale: 2, Fields: map[string]string{"event_id": "id", "business_id": "order.no", "external_user_id": "member", "currency": "currency", "amount_minor": "amount", "business_time": "time"}}
	config := model.Config{Sources: []model.Source{{ID: "sports", Name: "真实事件适配测试", Enabled: true, Subject: "local-operator", MemberSource: "sandbox", KeyRef: "sports-key", Rules: []model.Rule{rule}}}}
	data := map[string]any{"id": "external-event", "order": map[string]string{"no": "order1"}, "member": "member", "currency": "USD", "amount": "1.23", "time": now.Add(time.Minute).Format(time.RFC3339Nano)}
	body := map[string]any{"source_id": "sports", "event_type": "payment.done", "schema_version": "1", "data": data}
	now = now.Add(10 * time.Minute)
	call("POST", "/v1/affiliate/ingestion/preview", op, map[string]any{"config": config, "envelope": body}, 200, "", "", "")
	// 来源声明精度必须服从本站权威币种，不能通过映射放大或缩小金额。
	config.Sources[0].Rules[0].CurrencyScale = 18
	call("POST", "/v1/affiliate/ingestion/preview", op, map[string]any{"config": config, "envelope": body}, 400, "", "", "")
	config.Sources[0].Rules[0].CurrencyScale = 2
	submit := map[string]any{"config": config, "expected_revision": 0, "reason": "验证配置发布与事件映射"}
	call("POST", "/v1/affiliate/ingestion/config-requests", checker, submit, 403, "", "", "")
	pending = call("POST", "/v1/affiliate/ingestion/config-requests", op, submit, 202, "", "", "")
	approve(pending, op, 403)
	approve(pending, checker, 200)
	head := call("GET", "/v1/affiliate/ingestion/config", op, nil, 200, "", "", "")
	if head["revision"].(float64) != 1 {
		t.Fatal("发布修订不正确")
	}
	path := "/v1/affiliate/business-facts"
	sig := sign("affiliate-ingestion-v1", "sports", body, secret)
	call("POST", path, op, body, 403, "", "bad", "X-Affiliate-Signature")
	call("POST", path, checker, body, 403, "", sig, "X-Affiliate-Signature")
	result := call("POST", path, op, body, 200, "fact-key", sig, "X-Affiliate-Signature")
	if result["accepted"] != true {
		t.Fatal("自定义事件未真正接入")
	}
	call("POST", path, op, body, 200, "fact-key", sig, "X-Affiliate-Signature")
	data["unmapped"] = "changed"
	call("POST", path, op, body, 409, "fact-key", sign("affiliate-ingestion-v1", "sports", body, secret), "X-Affiliate-Signature")
	call("POST", path, op, body, 409, "other-key", sign("affiliate-ingestion-v1", "sports", body, secret), "X-Affiliate-Signature")
	delete(data, "unmapped")
	records := call("GET", "/v1/affiliate/ingestion/records", op, nil, 200, "", "", "")
	if len(records["rows"].([]any)) != 1 {
		t.Fatal("重复事件写入了多份记录")
	}
	registry := ingestionRegistry(store)
	if _, e = registry.Resolve(ctx, identity.Principal{Tenant: sc.Tenant + "-other", App: sc.App, Subject: "local-operator"}, "sports"); e == nil {
		t.Fatal("跨站来源可读取")
	}
	if _, e = registry.Resolve(ctx, identity.Principal{Tenant: sc.Tenant, App: sc.App, Subject: "ops-1", OpsCredential: "ops1.test"}, "sports"); e == nil {
		t.Fatal("运营人员获得来源身份")
	}
	// 停用仍须独立审批，并且不能改变当日曾启用来源的关账要求。
	config.Sources[0].Enabled = false
	submit = map[string]any{"config": config, "expected_revision": 1, "reason": "停止新增事件"}
	pending = call("POST", "/v1/affiliate/ingestion/config-requests", op, submit, 202, "", "", "")
	approve(pending, checker, 200)
	call("POST", path, op, body, 403, "disabled", sig, "X-Affiliate-Signature")
	start := time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)
	ids, e := (repo.Repository{}).RequiredSources(ctx, store.DB, sc, start, start.Add(24*time.Hour))
	if e != nil || len(ids) != 1 || ids[0] != "sports" {
		t.Fatalf("停用绕过日结来源 %+v %v", ids, e)
	}
	// 新模块不影响旧sandbox字节签名协议。
	legacy := map[string]any{"source_id": "sandbox", "event_id": "old-event", "business_id": "old-order", "external_user_id": "member", "kind": "DEPOSIT_SUCCEEDED", "currency": "USD", "amount_minor": "100", "valid_bet_minor": "0", "venue_type_id": "", "business_time": now.Add(-time.Minute).Format(time.RFC3339Nano)}
	call("POST", path, op, legacy, 200, "", sign("affiliate-business-v1", "sandbox", legacy, strings.Repeat("f", 32)), "X-Affiliate-Signature")
	// 实际关闭来源并推动日结：sandbox已关闭仍不得越过sports，停用来源仍可提交历史日水位。
	now = start.Add(25 * time.Hour)
	mark := func(source, key string) {
		count, digest, err := (flowrepo.Repository{}).Manifest(ctx, store.DB, sc, source, start, start.Add(24*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		body := map[string]any{"source_id": source, "business_day": "2026-10-18", "fact_count": count, "facts_digest": digest}
		call("POST", "/v1/affiliate/source-watermarks", op, body, 200, "", sign("affiliate-watermark-v1", source, body, key), "X-Affiliate-Signature")
	}
	mark("sandbox", strings.Repeat("f", 32))
	flowService := affiliateFlowService(store)
	if err := flowService.RunOnce(ctx, sc); err != nil {
		t.Fatal(err)
	}
	// 水位未完整属于正常等待，worker返回成功；应检查业务日未冻结，而不是期待错误。
	if _, err := flowService.DailyRepo.Daily(ctx, store.DB, sc, "2026-10-18"); err == nil {
		t.Fatal("缺少自定义来源水位仍完成日结")
	}
	mark("sports", secret)
	if err := flowService.RunOnce(ctx, sc); err != nil {
		t.Fatal(err)
	}
	if _, err := flowService.DailyRepo.Daily(ctx, store.DB, sc, "2026-10-18"); err != nil {
		t.Fatal("来源完整后未冻结业务日", err)
	}
	// 模拟验签后配置已经停用，事务内Guard必须再次拒绝；使用正常业务事务，不直接写资金。
	rawBody, _ := json.Marshal(body)
	if err := store.Within(ctx, func(tx db.Executor) error {
		return registry.Guard(ctx, tx, identity.Principal{Tenant: sc.Tenant, App: sc.App, Subject: "local-operator"}, rawBody, sig, flowmodel.Event{Source: "sports"})
	}); err == nil {
		t.Fatal("停用后事务复核未拒绝")
	}
	var amount string
	if e = store.DB.QueryRow(`SELECT JSON_UNQUOTE(JSON_EXTRACT(body,'$.amount_minor')) FROM affiliate_business_facts WHERE tenant_id=? AND app_id=? AND source_id='sports'`, sc.Tenant, sc.App).Scan(&amount); e != nil {
		t.Fatal(e)
	}
	if amount != "123" {
		t.Fatalf("金额映射不正确 %s", amount)
	}
}

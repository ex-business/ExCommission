package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func testRule() Rule {
	return Rule{EventType: "order.done", Version: "v1", Target: "BET_SETTLED", AmountUnit: "MAJOR", CurrencyScale: 2, Fields: map[string]string{"event_id": "id", "business_id": "order.no", "external_user_id": "member", "currency": "currency", "amount_minor": "amount", "valid_bet_minor": "valid", "venue_type_id": "venue", "business_time": "at"}}
}
func testConfig() Config {
	return Config{Sources: []Source{{ID: "sports", Name: "体育服务", Enabled: true, Subject: "sports-service", MemberSource: "sandbox", KeyRef: "sports-key", Rules: []Rule{testRule()}}}}
}
func TestMappingExactAmounts(t *testing.T) {
	r := testRule()
	raw := json.RawMessage(`{"id":"e1","order":{"no":"b1"},"member":"m1","currency":"USD","amount":"9007199254740993.12","valid":"10.00","venue":"SPORTS","at":"2026-10-17T10:00:00Z"}`)
	f, e := r.Map(raw)
	if e != nil || f.Amount != "900719925474099312" || f.ValidBet != "1000" {
		t.Fatalf("精确映射失败 %+v %v", f, e)
	}
	for _, bad := range []string{`9007199254740993.125`, `-1`, `1e3`, `1/2`, `1.`, `+1`} {
		v := strings.Replace(string(raw), "9007199254740993.12", bad, 1)
		if _, e = r.Map([]byte(v)); e == nil {
			t.Fatalf("接受错误金额 %s", bad)
		}
	}
	if n, e := money("0010", "MINOR", 2); e != nil || n != "10" {
		t.Fatal("金额错误使用非十进制")
	}
	if n, e := money("1.000", "MAJOR", 2); e != nil || n != "100" {
		t.Fatal("精确零尾数转换失败")
	}
	if _, e = r.Map([]byte(strings.Replace(string(raw), `"member":"m1"`, `"member":123`, 1))); e == nil {
		t.Fatal("接受数值编号")
	}
}
func TestConfigRulesAndImmutableVersions(t *testing.T) {
	c := testConfig()
	if c.Validate() != nil {
		t.Fatal("合法配置被拒绝")
	}
	for _, change := range []func(*Config){func(c *Config) { c.Sources[0].ID = "sandbox" }, func(c *Config) { c.Sources[0].Subject = "ops-1" }, func(c *Config) { c.Sources[0].Rules[0].Fields["wallet_balance"] = "amount" }, func(c *Config) { c.Sources[0].Rules[0].Fields["amount_minor"] = "data[0].money" }, func(c *Config) { c.Sources[0].Rules[0].Target = "CREDIT_WALLET" }} {
		n := testConfig()
		change(&n)
		if n.Validate() == nil {
			t.Fatal("危险配置未拒绝")
		}
	}
	n := testConfig()
	n.Sources[0].Rules[0].Fields["amount_minor"] = "other"
	if ValidateChange(c, n) == nil {
		t.Fatal("允许覆盖已发布版本")
	}
	n = testConfig()
	n.Sources[0].Enabled = false
	if ValidateChange(c, n) != nil {
		t.Fatal("不允许停用来源")
	}
	n = testConfig()
	r := testRule()
	r.Version = "v2"
	r.Fields["amount_minor"] = "other"
	n.Sources[0].Rules = append(n.Sources[0].Rules, r)
	if ValidateChange(c, n) != nil {
		t.Fatal("新事件版本无法扩展")
	}
}
func TestEnvelopeBounds(t *testing.T) {
	for _, raw := range []string{`{"source_id":"sports","event_type":"order.done","schema_version":"v1","data":{}} {}`, `{"source_id":"sports","event_type":"order.done","schema_version":"v1","data":{},"tenant_id":"forged"}`} {
		if _, e := DecodeEnvelope([]byte(raw)); e == nil {
			t.Fatal("接受非法封装")
		}
	}
}

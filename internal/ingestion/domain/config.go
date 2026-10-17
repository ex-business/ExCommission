// Package domain 定义可配置数据接入契约；配置只做字段转换，不执行脚本或资金操作。
package domain

import (
	"bytes"
	"encoding/json"
	"errors"
	affiliate "github.com/ex-business/ExCommission/internal/affiliate/domain"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	"io"
	"math/big"
	"regexp"
	"strings"
	"time"
)

var ErrConfig = errors.New("VALIDATION_FAILED:INGESTION_CONFIG_INVALID")
var ErrEvent = errors.New("VALIDATION_FAILED:INGESTION_EVENT_INVALID")
var pathPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*){0,7}$`)
var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

// Rule 使用固定目标事实和明确的金额单位；外部事件名可以自由定义，计算含义必须由引擎支持。
type Rule struct {
	EventType     string            `json:"event_type"`
	Version       string            `json:"schema_version"`
	Target        string            `json:"target_kind"`
	Fields        map[string]string `json:"fields"`
	AmountUnit    string            `json:"amount_unit"`
	CurrencyScale int               `json:"currency_scale"`
}
type Source struct {
	ID           string `json:"source_id"`
	Name         string `json:"name"`
	Enabled      bool   `json:"enabled"`
	Subject      string `json:"subject_id"`
	MemberSource string `json:"member_source_id"`
	KeyRef       string `json:"key_ref"`
	Rules        []Rule `json:"rules"`
}
type Config struct {
	Sources []Source `json:"sources"`
}
type Envelope struct {
	Source    string          `json:"source_id"`
	EventType string          `json:"event_type"`
	Version   string          `json:"schema_version"`
	Data      json.RawMessage `json:"data"`
}
type Fact struct {
	Identity     *affiliate.RiskIdentity `json:"risk_identity,omitempty"`
	EventID      string                  `json:"event_id"`
	BusinessID   string                  `json:"business_id"`
	ExternalUser string                  `json:"external_user_id"`
	Kind         string                  `json:"kind"`
	Currency     string                  `json:"currency"`
	Amount       string                  `json:"amount_minor"`
	ValidBet     string                  `json:"valid_bet_minor"`
	Venue        string                  `json:"venue_type_id"`
	At           time.Time               `json:"business_time"`
}

func (c Config) Validate() error {
	if len(c.Sources) > 64 {
		return ErrConfig
	}
	seen := map[string]bool{}
	for _, s := range c.Sources {
		// 保留旧sandbox接入；来源身份必须是服务身份，不能通过配置给运营人员代投递权限。
		if !idPattern.MatchString(s.ID) || s.ID == "sandbox" || seen[s.ID] || len(s.Name) == 0 || len(s.Name) > 120 || !idPattern.MatchString(s.Subject) || strings.HasPrefix(s.Subject, "ops-") || !idPattern.MatchString(s.MemberSource) || !idPattern.MatchString(s.KeyRef) || len(s.Rules) == 0 || len(s.Rules) > 32 {
			return ErrConfig
		}
		seen[s.ID] = true
		rules := map[string]bool{}
		for _, r := range s.Rules {
			key := r.EventType + "/" + r.Version
			if !idPattern.MatchString(r.EventType) || !idPattern.MatchString(r.Version) || rules[key] || r.CurrencyScale < 0 || r.CurrencyScale > 18 || (r.AmountUnit != "MINOR" && r.AmountUnit != "MAJOR") {
				return ErrConfig
			}
			rules[key] = true
			if r.Target != "DEPOSIT_SUCCEEDED" && r.Target != "BET_SETTLED" && r.Target != "WITHDRAWAL_SUCCEEDED" {
				return ErrConfig
			}
			for k, path := range r.Fields {
				if !strings.Contains("|event_id|business_id|external_user_id|currency|amount_minor|valid_bet_minor|venue_type_id|business_time|ip_hash|device_hash|account_hash|", "|"+k+"|") || !pathPattern.MatchString(path) {
					return ErrConfig
				}
			}
			for _, k := range []string{"event_id", "business_id", "external_user_id", "currency", "amount_minor", "business_time"} {
				if r.Fields[k] == "" {
					return ErrConfig
				}
			}
			if r.Target == "BET_SETTLED" && (r.Fields["valid_bet_minor"] == "" || r.Fields["venue_type_id"] == "") {
				return ErrConfig
			}
		}
	}
	return nil
}

// DecodeEnvelope 拒绝多份JSON和未知顶层字段；业务正文仅按发布版本显式取值，不执行表达式。
func DecodeEnvelope(raw []byte) (Envelope, error) {
	var out Envelope
	if len(raw) > 65536 {
		return out, ErrEvent
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&out) != nil || d.Decode(new(any)) != io.EOF || !idPattern.MatchString(out.Source) || !idPattern.MatchString(out.EventType) || !idPattern.MatchString(out.Version) {
		return out, ErrEvent
	}
	var obj map[string]any
	if json.Unmarshal(out.Data, &obj) != nil || obj == nil {
		return out, ErrEvent
	}
	return out, nil
}
func field(data map[string]any, path string) (string, error) {
	var v any = data
	for _, part := range strings.Split(path, ".") {
		m, ok := v.(map[string]any)
		if !ok {
			return "", ErrEvent
		}
		v, ok = m[part]
		if !ok {
			return "", ErrEvent
		}
	}
	// 金额与编号只接收字符串，避免JSON浮点精度丢失；来源必须按契约序列化。
	s, ok := v.(string)
	if !ok || len(s) == 0 || len(s) > 256 {
		return "", ErrEvent
	}
	return s, nil
}

// 金额只用十进制整数移位，禁止浮点、指数或隐式八进制；精度超出时拒绝而不舍入。
func money(raw, unit string, scale int) (string, error) {
	if scale < 0 || scale > 18 || len(raw) == 0 || len(raw) > 80 {
		return "", ErrEvent
	}
	parts := strings.Split(raw, ".")
	if len(parts) > 2 || parts[0] == "" {
		return "", ErrEvent
	}
	for _, p := range parts {
		if p == "" {
			return "", ErrEvent
		}
		for _, r := range p {
			if r < '0' || r > '9' {
				return "", ErrEvent
			}
		}
	}
	digits := parts[0]
	if unit == "MINOR" {
		if len(parts) != 1 {
			return "", ErrEvent
		}
	} else if unit == "MAJOR" {
		fraction := ""
		if len(parts) == 2 {
			fraction = parts[1]
		}
		if len(fraction) > scale {
			if strings.Trim(fraction[scale:], "0") != "" {
				return "", ErrEvent
			}
			fraction = fraction[:scale]
		}
		digits += fraction + strings.Repeat("0", scale-len(fraction))
	} else {
		return "", ErrEvent
	}
	n, ok := new(big.Int).SetString(digits, 10)
	if !ok {
		return "", ErrEvent
	}
	return n.String(), nil
}
func (r Rule) Map(raw json.RawMessage) (Fact, error) {
	var out Fact
	var data map[string]any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if d.Decode(&data) != nil || data == nil {
		return out, ErrEvent
	}
	values := map[string]string{}
	for k, path := range r.Fields {
		v, e := field(data, path)
		if e != nil {
			return out, e
		}
		values[k] = v
	}
	out = Fact{EventID: values["event_id"], BusinessID: values["business_id"], ExternalUser: values["external_user_id"], Kind: r.Target, Currency: values["currency"], Venue: values["venue_type_id"], ValidBet: "0"}
	var e error
	out.Amount, e = money(values["amount_minor"], r.AmountUnit, r.CurrencyScale)
	if e != nil {
		return out, e
	}
	if r.Target == "BET_SETTLED" {
		out.ValidBet, e = money(values["valid_bet_minor"], r.AmountUnit, r.CurrencyScale)
		if e != nil {
			return out, e
		}
	}
	out.At, e = time.Parse(time.RFC3339Nano, values["business_time"])
	if e != nil {
		return out, ErrEvent
	}
	if out.At.Year() < 1000 || out.EventID == "" || out.BusinessID == "" || out.ExternalUser == "" || out.Currency == "" {
		return out, ErrEvent
	}
	if !foundation.ValidID(out.EventID) || !foundation.ValidID(out.BusinessID) || !foundation.ValidID(out.ExternalUser) || !out.At.Equal(out.At.Truncate(time.Microsecond)) {
		return out, ErrEvent
	}
	if values["ip_hash"] != "" || values["device_hash"] != "" || values["account_hash"] != "" {
		out.Identity = &affiliate.RiskIdentity{IP: values["ip_hash"], Device: values["device_hash"], Account: values["account_hash"]}
		if out.Identity.Validate() != nil {
			return out, ErrEvent
		}
	}
	return out, nil
}

// ValidateChange 已发布的事件版本和会员映射不可改写；变更结构须增加schema_version，停用来源保留其历史。
func ValidateChange(old, next Config) error {
	if e := next.Validate(); e != nil {
		return e
	}
	sources := map[string]Source{}
	for _, s := range next.Sources {
		sources[s.ID] = s
	}
	for _, before := range old.Sources {
		after, ok := sources[before.ID]
		if !ok || before.Subject != after.Subject || before.MemberSource != after.MemberSource {
			return ErrConfig
		}
		for _, r := range before.Rules {
			found := false
			for _, n := range after.Rules {
				if n.EventType == r.EventType && n.Version == r.Version {
					a, _ := json.Marshal(r)
					b, _ := json.Marshal(n)
					if !bytes.Equal(a, b) {
						return ErrConfig
					}
					found = true
				}
			}
			if !found {
				return ErrConfig
			}
		}
	}
	return nil
}

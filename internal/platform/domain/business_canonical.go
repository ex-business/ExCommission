package domain

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"
	"time"
)

// BusinessCanonicalJSON 在严格schema校验后投影：十进制转atoms、整数字符串去前导零、时间转UTC。
// 投影只用于摘要与幂等内容，不替代保存的原始业务单位和冻结配置。
func BusinessCanonicalJSON(body []byte) ([]byte, error) {
	canonical, err := CanonicalJSON(body)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(canonical))
	decoder.UseNumber()
	var object map[string]any
	if err = decoder.Decode(&object); err != nil {
		return nil, err
	}
	if err = projectBusiness(object); err != nil {
		return nil, err
	}
	return json.Marshal(object)
}
func projectBusiness(value any) error {
	switch node := value.(type) {
	case []any:
		for _, child := range node {
			if err := projectBusiness(child); err != nil {
				return err
			}
		}
	case map[string]any:
		metric, _ := node["metric"].(string)
		for key, child := range node {
			if key == "request_id" || key == "transport_signature" {
				delete(node, key)
				continue
			}
			text, isString := child.(string)
			decimal := key == "basis_amount" || key == "quantity" || key == "unit_price" || key == "lower" || key == "upper" || key == "refund_basis_total" || key == "refund_quantity_total" || key == "value" && (metric == "basis_amount" || metric == "quantity" || metric == "PERSONAL_NET_BASIS" || metric == "TEAM_NET_BASIS")
			if decimal && isString {
				parsed, err := ParseDecimal(text)
				if err != nil {
					return err
				}
				node[key] = parsed.Atoms().String()
				continue
			}
			if isString && (key == "minor_units" || strings.HasSuffix(key, "_minor") || strings.HasSuffix(key, "_ppm") || key == "weight" || key == "value" && metric == "ACTIVE_DIRECT_COUNT") {
				if text == "" {
					return ErrValidation
				}
				for _, r := range text {
					if r < '0' || r > '9' {
						return ErrValidation
					}
				}
				trimmed := strings.TrimLeft(text, "0")
				if trimmed == "" {
					trimmed = "0"
				}
				node[key] = trimmed
				continue
			}
			if isString && (key == "business_time" || key == "effective_from" || key == "effective_to" || key == "cutoff" || key == "value" && metric == "business_time") {
				parsed, err := time.Parse(time.RFC3339Nano, text)
				if err != nil || parsed.Nanosecond()%1000 != 0 {
					return ErrValidation
				}
				node[key] = parsed.UTC().Format("2006-01-02T15:04:05.000000Z")
				continue
			}
			if key == "agent_ids" {
				if ids, ok := child.([]any); ok {
					sort.SliceStable(ids, func(i, j int) bool { a, _ := ids[i].(string); b, _ := ids[j].(string); return a < b })
				}
			}
			if err := projectBusiness(child); err != nil {
				return err
			}
		}
	}
	return nil
}

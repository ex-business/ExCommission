package domain

import (
	"bytes"
	"encoding/json"
	"fmt"
	plan "github.com/ex-business/ExCommission/internal/plans/domain"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	"math/big"
	"strings"
)

func Percent(ppm string) string {
	n, ok := new(big.Int).SetString(ppm, 10)
	if !ok || n.Sign() < 0 {
		return "未配置"
	}
	whole, part := new(big.Int), new(big.Int)
	whole.QuoRem(n, big.NewInt(10000), part)
	fraction := strings.TrimRight(strings.Repeat("0", 4-len(part.String()))+part.String(), "0")
	if fraction == "" {
		return whole.String() + "%"
	}
	return whole.String() + "." + fraction + "%"
}
func MoneyText(minor, currency string) string {
	e, ok := foundation.CurrencyExponents()[currency]
	if !ok {
		return minor + " 最小单位 " + currency
	}
	v := minor
	if len(v) <= e {
		v = strings.Repeat("0", e-len(v)+1) + v
	}
	if e > 0 {
		v = v[:len(v)-e] + "." + v[len(v)-e:]
	}
	return v + " " + currency
}
func conditionText(c plan.Condition) string {
	if c.Logic != "" {
		if len(c.Children) == 0 {
			return "业务满足方案参与条件"
		}
		children := []string{}
		for _, child := range c.Children {
			children = append(children, conditionText(child))
		}
		switch c.Logic {
		case "NOT":
			return "不满足（" + strings.Join(children, "、") + "）"
		case "OR":
			return "（" + strings.Join(children, " 或 ") + "）"
		default:
			return "（" + strings.Join(children, " 且 ") + "）"
		}
	}
	metric := map[string]string{"basis_amount": "计佣金额", "quantity": "业务数量", "confirmed": "业务确认", "product_code": "场馆", "scene": "业务场景", "business_time": "业务时间"}[c.Metric]
	if metric == "" {
		metric = c.Metric
	}
	op := map[string]string{"EQ": "等于", "NE": "不等于", "GT": "大于", "GTE": "不少于", "LT": "小于", "LTE": "不超过", "IN": "属于"}[c.Operator]
	var value any
	decoder := json.NewDecoder(bytes.NewReader(c.Value))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return "符合已生效的业务条件"
	}
	return metric + op + fmt.Sprint(value)
}
func PublicRules(c plan.Config) Rules {
	out := Rules{Plan: c.Name, Currency: c.Currency, Minimum: c.Settlement.Minimum, Rules: []Rule{}}
	names := map[string]string{"PERCENT": "比例佣金", "FIXED": "固定奖励", "UNIT_PRICE": "按业务数量奖励", "INDEPENDENT": "分层独立佣金", "DIFFERENTIAL": "级差佣金", "MARGINAL": "阶梯累进佣金", "HIGHEST_RATE": "最高档佣金", "WEIGHTED": "按权重分配奖励"}
	for _, r := range c.Rules {
		description := []string{}
		currency := r.Parameters.RewardCurrency
		if currency == "" {
			currency = c.Currency
		}
		switch r.Algorithm {
		case "PERCENT":
			description = append(description, "计佣比例 "+Percent(r.Parameters.RatePPM))
		case "FIXED":
			description = append(description, "每次奖励 "+MoneyText(r.Parameters.AmountMinor, currency))
		case "UNIT_PRICE":
			description = append(description, "每 "+r.Parameters.QuantityUnit+" 奖励 "+r.Parameters.UnitPrice+" "+currency)
		case "WEIGHTED":
			description = append(description, "奖励池 "+MoneyText(r.Parameters.PoolMinor, currency)+"，按已审核权重分配")
		case "INDEPENDENT", "DIFFERENTIAL":
			for _, rate := range r.Parameters.Rates {
				depth := ""
				if rate.Depth != nil {
					depth = fmt.Sprintf("，第%d层", *rate.Depth)
				}
				description = append(description, rate.Product+"，等级"+rate.Grade+depth+"："+Percent(rate.PPM))
			}
		case "MARGINAL", "HIGHEST_RATE":
			for _, tier := range r.Parameters.Tiers {
				upper := "无上限"
				if tier.Upper != nil {
					upper = *tier.Upper
				}
				description = append(description, tier.Lower+" 至 "+upper+"："+Percent(tier.PPM))
			}
		}
		description = append(description, conditionText(r.Condition), "按已审核资格和实际关系层级结算")
		if r.BeneficiaryCap != nil {
			description = append(description, "单人上限 "+MoneyText(*r.BeneficiaryCap, currency))
		}
		out.Rules = append(out.Rules, Rule{Name: names[r.Algorithm], Description: strings.Join(description, "；")})
	}
	return out
}

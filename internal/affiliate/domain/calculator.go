package domain

import (
	"math/big"
)

// CommissionGrade 与实际日结共用定级条件，计算器不维护另一套等级或费率。
func CommissionGrade(c Config, members int, total *big.Int) Level {
	grade := c.Levels[0]
	for _, v := range c.Levels {
		threshold, _ := Money(v.Turnover, false)
		if members >= v.Members && total.Cmp(threshold) >= 0 {
			grade = v
		}
	}
	return grade
}

// CommissionAmount 逐层汇总后向下取最小货币单位，避免前端浮点与日结金额不一致。
func CommissionAmount(v *big.Int, rate int) *big.Int {
	return new(big.Int).Quo(new(big.Int).Mul(v, big.NewInt(int64(rate))), big.NewInt(1000000))
}

type Estimate struct {
	Layers   []LayerCommission `json:"layers,omitempty"`
	Next     *GradeGap         `json:"next_level,omitempty"`
	Level    int               `json:"level"`
	Currency string            `json:"currency"`
	Scale    int               `json:"currency_scale"`
	Rates    [3]int            `json:"rates_ppm"`
	Amounts  [3]string         `json:"layer_commission_minor"`
	Total    string            `json:"commission_minor"`
}

// EstimateCommission 兼容原三层调用；扩展方案必须提交完整层级，禁止悄悄漏算。
func EstimateCommission(c Config, members int, volumes [3]string) (Estimate, error) {
	return EstimateCommissionLayers(c, members, volumes[:])
}
func EstimateCommissionLayers(c Config, members int, volumes []string) (Estimate, error) {
	out := Estimate{Currency: c.Currency, Scale: c.CurrencyScale, Amounts: [3]string{"0", "0", "0"}}
	if e := c.Normalize(); e != nil {
		return out, e
	}
	if members < 0 || members > 1000000000 || len(volumes) != c.DepthLimit() {
		return out, ErrValidation
	}
	total := big.NewInt(0)
	values := make([]*big.Int, len(volumes))
	for i, v := range volumes {
		n, e := Money(v, false)
		if e != nil {
			return out, e
		}
		values[i] = n
		total.Add(total, n)
	}
	if len(total.String()) > 38 {
		return out, ErrValidation
	}
	grade := CommissionGrade(c, members, total)
	out.Level = grade.Number
	rules := c.LayerRules(grade.Number)
	sum := big.NewInt(0)
	for i, v := range values {
		n, e := rules[i].Calculate(v, members, i+1)
		if e != nil {
			return out, e
		}
		if i < 3 {
			out.Amounts[i] = n.String()
			out.Rates[i] = rules[i].Rate
		}
		out.Layers = append(out.Layers, LayerCommission{Depth: i + 1, Volume: v.String(), Algorithm: rules[i].Algorithm, Rate: rules[i].Rate, Amount: n.String()})
		sum.Add(sum, n)
	}
	if len(sum.String()) > 38 {
		return out, ErrValidation
	}
	out.Total = sum.String()
	for _, level := range c.Levels {
		if level.Number > grade.Number {
			n, _ := Money(level.Turnover, false)
			missing := new(big.Int).Sub(n, total)
			if missing.Sign() < 0 {
				missing.SetInt64(0)
			}
			count := level.Members - members
			if count < 0 {
				count = 0
			}
			out.Next = &GradeGap{Level: level.Number, Members: count, Volume: missing.String()}
			break
		}
	}
	return out, nil
}

type GradeGap struct {
	Level   int    `json:"level"`
	Members int    `json:"missing_members"`
	Volume  string `json:"missing_valid_bet_minor"`
}

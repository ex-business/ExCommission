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
	Level    int       `json:"level"`
	Currency string    `json:"currency"`
	Scale    int       `json:"currency_scale"`
	Rates    [3]int    `json:"rates_ppm"`
	Amounts  [3]string `json:"layer_commission_minor"`
	Total    string    `json:"commission_minor"`
}

func EstimateCommission(c Config, members int, volumes [3]string) (Estimate, error) {
	out := Estimate{Currency: c.Currency, Scale: c.CurrencyScale}
	if e := c.Normalize(); e != nil {
		return out, e
	}
	if members < 0 || members > 1000000000 {
		return out, ErrValidation
	}
	total := big.NewInt(0)
	values := [3]*big.Int{}
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
	out.Rates = grade.Rates
	amount := big.NewInt(0)
	for i, v := range values {
		n := CommissionAmount(v, grade.Rates[i])
		out.Amounts[i] = n.String()
		amount.Add(amount, n)
	}
	if len(amount.String()) > 38 {
		return out, ErrValidation
	}
	out.Total = amount.String()
	return out, nil
}

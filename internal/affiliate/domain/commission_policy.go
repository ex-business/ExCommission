package domain

import (
	"math/big"
)

// CommissionPolicy 是可选的新规则版本；缺省继续执行原三层比例规则，不改变历史配置。
type CommissionPolicy struct {
	Depth  int                     `json:"depth"`
	Grades []CommissionGradePolicy `json:"grades"`
}
type CommissionGradePolicy struct {
	Level  int                   `json:"level"`
	Layers []CommissionLayerRule `json:"layers"`
}
type CommissionLayerRule struct {
	Algorithm string           `json:"algorithm"`
	Rate      int              `json:"rate_ppm,omitempty"`
	Fixed     string           `json:"fixed_minor,omitempty"`
	Tiers     []CommissionTier `json:"tiers,omitempty"`
	Formula   *Formula         `json:"formula,omitempty"`
	Cap       string           `json:"cap_minor,omitempty"`
}
type CommissionTier struct {
	From string `json:"from_minor"`
	To   string `json:"to_minor,omitempty"`
	Rate int    `json:"rate_ppm"`
}

// Formula 仅支持白名单算术树，金额始终为最小单位有理数；不执行脚本、不读取外部数据。
type Formula struct {
	Op    string    `json:"op"`
	Value string    `json:"value,omitempty"`
	Args  []Formula `json:"args,omitempty"`
}
type LayerCommission struct {
	Depth     int    `json:"depth"`
	Volume    string `json:"valid_bet_minor"`
	Algorithm string `json:"algorithm"`
	Rate      int    `json:"rate_ppm"`
	Amount    string `json:"commission_minor"`
}

func (c Config) DepthLimit() int {
	if c.Commission != nil {
		return c.Commission.Depth
	}
	return 3
}
func (c Config) LayerRules(level int) []CommissionLayerRule {
	if c.Commission != nil {
		for _, g := range c.Commission.Grades {
			if g.Level == level {
				return g.Layers
			}
		}
		return nil
	}
	for _, g := range c.Levels {
		if g.Number == level {
			out := make([]CommissionLayerRule, 3)
			for i, r := range g.Rates {
				out[i] = CommissionLayerRule{Algorithm: "PROPORTIONAL", Rate: r}
			}
			return out
		}
	}
	return nil
}
func (p CommissionPolicy) Validate(levels []Level) error {
	if p.Depth < 1 || p.Depth > 64 || len(p.Grades) != len(levels) {
		return ErrValidation
	}
	seen := map[int]bool{}
	for _, g := range p.Grades {
		if g.Level < 0 || g.Level >= len(levels) || seen[g.Level] || len(g.Layers) != p.Depth {
			return ErrValidation
		}
		seen[g.Level] = true
		for _, r := range g.Layers {
			if e := r.Validate(); e != nil {
				return e
			}
		}
	}
	return nil
}
func (r CommissionLayerRule) Validate() error {
	if r.Cap != "" {
		if _, e := Money(r.Cap, false); e != nil {
			return e
		}
	}
	switch r.Algorithm {
	case "PROPORTIONAL":
		if r.Rate < 0 || r.Rate > 1000000 || r.Fixed != "" || len(r.Tiers) > 0 || r.Formula != nil {
			return ErrValidation
		}
	case "FIXED":
		if _, e := Money(r.Fixed, false); e != nil {
			return e
		}
		if r.Rate != 0 || len(r.Tiers) > 0 || r.Formula != nil {
			return ErrValidation
		}
	case "TIERED":
		if r.Rate != 0 || r.Fixed != "" || r.Formula != nil || len(r.Tiers) == 0 || len(r.Tiers) > 64 {
			return ErrValidation
		}
		next := "0"
		for i, t := range r.Tiers {
			lo, e := Money(t.From, false)
			if e != nil || t.From != next || t.Rate < 0 || t.Rate > 1000000 {
				return ErrValidation
			}
			if t.To == "" {
				if i != len(r.Tiers)-1 {
					return ErrValidation
				}
			} else {
				hi, e := Money(t.To, true)
				if e != nil || hi.Cmp(lo) <= 0 || i == len(r.Tiers)-1 {
					return ErrValidation
				}
			}
			next = t.To
		}
	case "FORMULA":
		if r.Rate != 0 || r.Fixed != "" || len(r.Tiers) > 0 || r.Formula == nil {
			return ErrValidation
		}
		n := 0
		if e := r.Formula.validate(0, &n); e != nil {
			return e
		}
		low, high, err := r.Formula.bounds()
		if err != nil || low.Sign() < 0 {
			return ErrValidation
		}
		// 设置明确封顶时允许中间公式超过存储上限，最终金额由封顶收敛。
		if r.Cap == "" {
			max := new(big.Int).Exp(big.NewInt(10), big.NewInt(38), nil)
			max.Sub(max, big.NewInt(1))
			if high.Cmp(new(big.Rat).SetInt(max)) > 0 {
				return ErrValidation
			}
		}

	default:
		return ErrValidation
	}
	return nil
}
func (f Formula) validate(depth int, n *int) error {
	*n++
	if depth > 12 || *n > 64 {
		return ErrValidation
	}
	switch f.Op {
	case "CONST":
		if len(f.Args) > 0 {
			return ErrValidation
		}
		_, e := Money(f.Value, false)
		return e
	case "VOLUME", "MEMBERS", "DEPTH":
		if f.Value != "" || len(f.Args) > 0 {
			return ErrValidation
		}
	case "ADD", "SUB", "MUL", "DIV", "MIN", "MAX":
		if f.Value != "" || len(f.Args) != 2 {
			return ErrValidation
		}
		for _, a := range f.Args {
			if e := a.validate(depth+1, n); e != nil {
				return e
			}
		}
	default:
		return ErrValidation
	}
	return nil
}
func (f Formula) eval(volume *big.Int, members, depth int) (*big.Rat, error) {
	var out *big.Rat
	switch f.Op {
	case "CONST":
		n, _ := Money(f.Value, false)
		out = new(big.Rat).SetInt(n)
	case "VOLUME":
		out = new(big.Rat).SetInt(volume)
	case "MEMBERS":
		out = new(big.Rat).SetInt64(int64(members))
	case "DEPTH":
		out = new(big.Rat).SetInt64(int64(depth))
	default:
		a, e := f.Args[0].eval(volume, members, depth)
		if e != nil {
			return nil, e
		}
		b, e := f.Args[1].eval(volume, members, depth)
		if e != nil {
			return nil, e
		}
		out = new(big.Rat)
		switch f.Op {
		case "ADD":
			out.Add(a, b)
		case "SUB":
			out.Sub(a, b)
		case "MUL":
			out.Mul(a, b)
		case "DIV":
			if b.Sign() == 0 {
				return nil, ErrValidation
			}
			out.Quo(a, b)
		case "MIN":
			out.Set(a)
			if b.Cmp(a) < 0 {
				out.Set(b)
			}
		case "MAX":
			out.Set(a)
			if b.Cmp(a) > 0 {
				out.Set(b)
			}
		}
	}
	// 限制中间结果复杂度，防止小表达式反复乘法消耗无界CPU或内存。
	if out.Num().BitLen() > 1024 || out.Denom().BitLen() > 1024 {
		return nil, ErrValidation
	}
	return out, nil
}
func (r CommissionLayerRule) Calculate(volume *big.Int, members, depth int) (*big.Int, error) {
	if e := r.Validate(); e != nil {
		return nil, e
	}
	// 无有效业务量的层级不发固定或公式奖励，避免空团队凭空产出。
	if volume.Sign() == 0 {
		return big.NewInt(0), nil
	}
	out := new(big.Int)
	switch r.Algorithm {
	case "PROPORTIONAL":
		out = CommissionAmount(volume, r.Rate)
	case "FIXED":
		out, _ = Money(r.Fixed, false)
	case "TIERED":
		sum := new(big.Rat)
		for _, t := range r.Tiers {
			lo, _ := Money(t.From, false)
			if volume.Cmp(lo) <= 0 {
				break
			}
			hi := new(big.Int).Set(volume)
			if t.To != "" {
				v, _ := Money(t.To, false)
				if v.Cmp(hi) < 0 {
					hi = v
				}
			}
			part := new(big.Int).Sub(hi, lo)
			part.Mul(part, big.NewInt(int64(t.Rate)))
			sum.Add(sum, new(big.Rat).SetFrac(part, big.NewInt(1000000)))
		}
		out.Quo(sum.Num(), sum.Denom())
	case "FORMULA":
		v, e := r.Formula.eval(volume, members, depth)
		if e != nil || v.Sign() < 0 {
			return nil, ErrValidation
		}
		out.Quo(v.Num(), v.Denom())
	}
	if r.Cap != "" {
		cap, _ := Money(r.Cap, false)
		if out.Cmp(cap) > 0 {
			out = cap
		}
	}
	if out.Sign() < 0 || len(out.String()) > 38 {
		return nil, ErrValidation
	}
	return out, nil
}

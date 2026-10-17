package domain

import "math/big"

// bounds 证明所有合法业务输入下的数值范围；允许中间负数用于MAX(0,差值)，但除数区间不能含零。
// 这比只用几组样例试算更严格，避免已审批公式在某笔真实业务上永久阻塞历史日结。
func (f Formula) bounds() (*big.Rat, *big.Rat, error) {
	integer := func(n int64) *big.Rat { return new(big.Rat).SetInt64(n) }
	maximum := func() *big.Rat {
		n := new(big.Int).Exp(big.NewInt(10), big.NewInt(38), nil)
		n.Sub(n, big.NewInt(1))
		return new(big.Rat).SetInt(n)
	}
	switch f.Op {
	case "CONST":
		n, e := Money(f.Value, false)
		if e != nil {
			return nil, nil, e
		}
		v := new(big.Rat).SetInt(n)
		return v, new(big.Rat).Set(v), nil
	case "VOLUME":
		return integer(1), maximum(), nil // 零业务量在规则执行前直接返回零，不执行公式。
	case "MEMBERS":
		return integer(0), integer(1000000000), nil
	case "DEPTH":
		return integer(1), integer(64), nil
	}
	a, b, e := f.Args[0].bounds()
	if e != nil {
		return nil, nil, e
	}
	c, d, e := f.Args[1].bounds()
	if e != nil {
		return nil, nil, e
	}
	var lo, hi *big.Rat
	switch f.Op {
	case "ADD":
		lo = new(big.Rat).Add(a, c)
		hi = new(big.Rat).Add(b, d)
	case "SUB":
		lo = new(big.Rat).Sub(a, d)
		hi = new(big.Rat).Sub(b, c)
	case "MIN":
		lo = new(big.Rat).Set(a)
		if c.Cmp(a) < 0 {
			lo.Set(c)
		}
		hi = new(big.Rat).Set(b)
		if d.Cmp(b) < 0 {
			hi.Set(d)
		}
	case "MAX":
		lo = new(big.Rat).Set(a)
		if c.Cmp(a) > 0 {
			lo.Set(c)
		}
		hi = new(big.Rat).Set(b)
		if d.Cmp(b) > 0 {
			hi.Set(d)
		}
	case "MUL", "DIV":
		if f.Op == "DIV" && c.Sign() <= 0 && d.Sign() >= 0 {
			return nil, nil, ErrValidation
		}
		for _, x := range []*big.Rat{a, b} {
			for _, y := range []*big.Rat{c, d} {
				v := new(big.Rat)
				if f.Op == "MUL" {
					v.Mul(x, y)
				} else {
					v.Quo(x, y)
				}
				if lo == nil || v.Cmp(lo) < 0 {
					lo = new(big.Rat).Set(v)
				}
				if hi == nil || v.Cmp(hi) > 0 {
					hi = new(big.Rat).Set(v)
				}
			}
		}
	default:
		return nil, nil, ErrValidation
	}
	for _, v := range []*big.Rat{lo, hi} {
		if v.Num().BitLen() > 1024 || v.Denom().BitLen() > 1024 {
			return nil, nil, ErrValidation
		}
	}
	return lo, hi, nil
}

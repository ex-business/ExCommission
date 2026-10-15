package domain

import (
	"errors"
	plan "github.com/ex-business/ExCommission/internal/plans/domain"
	"math/big"
	"sort"
)

func scaled(numerator, denominator *big.Int, exponent int) *big.Int {
	value := new(big.Int).Mul(numerator, new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(exponent)), nil))
	return value.Quo(value, denominator)
}
func TierTarget(basis Decimal, tiers []plan.Tier, highest bool, exponent int) (*big.Int, error) {
	atoms := basis.Atoms()
	numerator := new(big.Int)
	for _, tier := range tiers {
		lower, err := ParseDecimal(tier.Lower)
		if err != nil {
			return nil, err
		}
		rate, err := plan.PPM(tier.PPM)
		if err != nil {
			return nil, err
		}
		var upper *big.Int
		if tier.Upper != nil {
			u, err := ParseDecimal(*tier.Upper)
			if err != nil {
				return nil, err
			}
			upper = u.Atoms()
		}
		if highest {
			if atoms.Cmp(lower.Atoms()) >= 0 && (upper == nil || atoms.Cmp(upper) < 0) {
				return percent(atoms, rate, exponent), nil
			}
			continue
		}
		if atoms.Cmp(lower.Atoms()) <= 0 {
			continue
		}
		end := atoms
		if upper != nil && end.Cmp(upper) > 0 {
			end = upper
		}
		segment := new(big.Int).Sub(end, lower.Atoms())
		numerator.Add(numerator, new(big.Int).Mul(segment, big.NewInt(rate)))
	}
	if highest {
		return nil, errors.New("RATE_MISSING")
	}
	return scaled(numerator, big.NewInt(100000000000000), exponent), nil
}

type WeightedCandidate struct {
	ID, Weight string
	Eligible   bool
}
type WeightedResult struct {
	Targets  map[string]*big.Int
	Platform *big.Int
}

func AllocateWeighted(pool *big.Int, candidates []WeightedCandidate) (WeightedResult, error) {
	result := WeightedResult{Targets: map[string]*big.Int{}, Platform: new(big.Int)}
	if pool == nil || pool.Sign() < 0 || len(candidates) == 0 {
		return result, errors.New("VALIDATION_FAILED")
	}
	type share struct {
		id                        string
		amount, remainder, weight *big.Int
		eligible                  bool
	}
	shares := make([]share, 0, len(candidates))
	sum := new(big.Int)
	ids := map[string]bool{}
	for _, candidate := range candidates {
		weight, err := plan.Unsigned(candidate.Weight)
		if err != nil || weight.Sign() <= 0 || candidate.ID == "" || ids[candidate.ID] {
			return result, errors.New("VALIDATION_FAILED")
		}
		ids[candidate.ID] = true
		sum.Add(sum, weight)
		shares = append(shares, share{id: candidate.ID, weight: weight, eligible: candidate.Eligible})
	}
	distributed := new(big.Int)
	for i := range shares {
		numerator := new(big.Int).Mul(pool, shares[i].weight)
		shares[i].amount = new(big.Int)
		shares[i].remainder = new(big.Int)
		shares[i].amount.QuoRem(numerator, sum, shares[i].remainder)
		distributed.Add(distributed, shares[i].amount)
	}
	remainder := new(big.Int).Sub(pool, distributed)
	sort.Slice(shares, func(i, j int) bool {
		cmp := shares[i].remainder.Cmp(shares[j].remainder)
		if cmp != 0 {
			return cmp > 0
		}
		return shares[i].id < shares[j].id
	})
	for i := int64(0); i < remainder.Int64(); i++ {
		shares[i].amount.Add(shares[i].amount, big.NewInt(1))
	}
	for _, share := range shares {
		if share.eligible {
			result.Targets[share.id] = share.amount
		} else {
			result.Targets[share.id] = new(big.Int)
			result.Platform.Add(result.Platform, share.amount)
		}
	}
	return result, nil
}

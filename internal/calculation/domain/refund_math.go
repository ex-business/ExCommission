package domain

import (
	"errors"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	"math/big"
)

func RemainingBasis(original, total Decimal) (Decimal, error) {
	if total.Atoms().Cmp(original.Atoms()) > 0 {
		return Decimal{}, errors.New("REFUND_EXCEEDS_ORIGINAL")
	}
	return foundation.DecimalFromAtoms(new(big.Int).Sub(original.Atoms(), total.Atoms()))
}
func AllocateRefund(oldTarget, paid, unpaid, newTarget *big.Int) (reduction, newUnpaid, debt *big.Int, err error) {
	for _, value := range []*big.Int{oldTarget, paid, unpaid, newTarget} {
		if value == nil || value.Sign() < 0 {
			return nil, nil, nil, errors.New("VALIDATION_FAILED")
		}
	}
	if newTarget.Cmp(oldTarget) > 0 {
		return nil, nil, nil, errors.New("NON_MONOTONIC_REFUND")
	}
	difference := new(big.Int).Sub(oldTarget, newTarget)
	reduction = new(big.Int).Set(difference)
	if reduction.Cmp(unpaid) > 0 {
		reduction.Set(unpaid)
	}
	debt = new(big.Int).Sub(difference, reduction)
	if debt.Cmp(paid) > 0 {
		return nil, nil, nil, errors.New("CONSERVATION_VIOLATION")
	}
	newUnpaid = new(big.Int).Sub(unpaid, reduction)
	return reduction, newUnpaid, debt, nil
}
func Conserves(net, debt, paid, recovered, pending, available, reserved *big.Int) bool {
	for _, value := range []*big.Int{net, debt, paid, recovered, pending, available, reserved} {
		if value == nil || value.Sign() < 0 {
			return false
		}
	}
	left := new(big.Int).Add(net, debt)
	right := new(big.Int).Sub(paid, recovered)
	right.Add(right, pending)
	right.Add(right, available)
	right.Add(right, reserved)
	return left.Cmp(right) == 0
}

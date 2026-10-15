package domain

import foundation "github.com/ex-business/ExCommission/internal/platform/domain"

type Decimal = foundation.Decimal

var ErrDecimal = foundation.ErrDecimal

func ParseDecimal(value string) (Decimal, error) { return foundation.ParseDecimal(value) }

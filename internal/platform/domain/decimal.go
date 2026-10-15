package domain

import (
	"errors"
	"math/big"
	"regexp"
	"strings"
)

var ErrDecimal = errors.New("VALIDATION_FAILED: invalid decimal")
var decimalPattern = regexp.MustCompile(`^[0-9]{1,18}(\.[0-9]{1,8})?$`)

// Decimal 是非负八位定点值；不暴露可变的整数引用。
type Decimal struct{ atoms big.Int }

func ParseDecimal(value string) (Decimal, error) {
	if !decimalPattern.MatchString(value) {
		return Decimal{}, ErrDecimal
	}
	parts := strings.SplitN(value, ".", 2)
	fraction := ""
	if len(parts) == 2 {
		fraction = parts[1]
	}
	n, ok := new(big.Int).SetString(parts[0]+fraction+strings.Repeat("0", 8-len(fraction)), 10)
	if !ok {
		return Decimal{}, ErrDecimal
	}
	return Decimal{atoms: *n}, nil
}

func (d Decimal) Atoms() *big.Int { return new(big.Int).Set(&d.atoms) }

// Minor 将目标最终向下舍入；乘除计算应保留有理数后再调用对应目标舍入逻辑。
func (d Decimal) Minor(exponent int) (*big.Int, error) {
	if exponent < 0 || exponent > 8 {
		return nil, ErrDecimal
	}
	divisor := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(8-exponent)), nil)
	return new(big.Int).Quo(d.Atoms(), divisor), nil
}

func DecimalFromAtoms(atoms *big.Int) (Decimal, error) {
	if atoms == nil || atoms.Sign() < 0 || len(atoms.String()) > 26 {
		return Decimal{}, ErrDecimal
	}
	return Decimal{atoms: *new(big.Int).Set(atoms)}, nil
}
func (d Decimal) String() string {
	whole, fraction := new(big.Int), new(big.Int)
	whole.QuoRem(d.Atoms(), big.NewInt(100000000), fraction)
	digits := fraction.String()
	return whole.String() + "." + strings.Repeat("0", 8-len(digits)) + digits
}

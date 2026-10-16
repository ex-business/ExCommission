package domain

import (
	money "github.com/ex-business/ExCommission/internal/payments/domain"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	"math/big"
	"time"
	"unicode/utf8"
)

type Instruction struct {
	ID                string      `json:"instruction_id"`
	StatementID       string      `json:"statement_id"`
	Amount            money.Money `json:"amount"`
	Channel           string      `json:"channel_id"`
	StatementRevision uint64      `json:"expected_statement_revision"`
	Remark            string      `json:"remark"`
	Status            string      `json:"status"`
	Revision          uint64      `json:"revision"`
	PaymentID         *string     `json:"payment_id"`
	ErrorCode         *string     `json:"error_code"`
	Created           time.Time   `json:"created_at"`
}
type Prepare struct {
	StatementID       string      `json:"statement_id"`
	Amount            money.Money `json:"amount"`
	Channel           string      `json:"channel_id"`
	StatementRevision uint64      `json:"expected_statement_revision"`
	Remark            string      `json:"remark"`
}
type Edit struct {
	Revision uint64      `json:"expected_revision"`
	Amount   money.Money `json:"amount"`
	Remark   string      `json:"remark"`
}
type Action struct {
	ID       string `json:"instruction_id"`
	Revision uint64 `json:"expected_revision"`
	Reason   string `json:"reason"`
}

func ValidAmount(m money.Money) bool {
	n, ok := new(big.Int).SetString(m.Minor, 10)
	return foundation.ValidID(m.Currency) && ok && n.Sign() > 0 && len(m.Minor) <= 38 && n.String() == m.Minor
}
func ValidRemark(s string) bool { return utf8.ValidString(s) && utf8.RuneCountInString(s) <= 1000 }

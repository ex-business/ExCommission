package domain

import "time"

type Credit struct{ UserID, Currency, Amount, Reference, PaymentID, StatementID string }
type Account struct {
	Currency string `json:"currency"`
	Balance  string `json:"balance_minor"`
	Revision uint64 `json:"revision"`
	Type     string `json:"wallet_type"`
}
type Entry struct {
	ID          string    `json:"transaction_id"`
	Currency    string    `json:"currency"`
	Amount      string    `json:"amount_minor"`
	Before      string    `json:"balance_before_minor,omitempty"`
	After       string    `json:"balance_after_minor,omitempty"`
	PaymentID   string    `json:"payment_id,omitempty"`
	StatementID string    `json:"statement_id,omitempty"`
	Created     time.Time `json:"created_at"`
}

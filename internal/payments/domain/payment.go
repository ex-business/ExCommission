package domain

import "time"

type Money struct {
	Currency string `json:"currency"`
	Minor    string `json:"minor_units"`
}
type Allocation struct {
	EntitlementID string `json:"entitlement_id"`
	Amount        string `json:"minor_units"`
}
type Payment struct {
	ID             string       `json:"payment_id"`
	StatementID    string       `json:"statement_id"`
	MerchantID     string       `json:"merchant_payment_id"`
	ChannelID      string       `json:"channel_id"`
	Currency       string       `json:"currency"`
	Amount         string       `json:"minor_units"`
	Status         string       `json:"status"`
	Reconciliation string       `json:"reconciliation_status"`
	Receipt        *string      `json:"receipt_ref"`
	Revision       uint64       `json:"revision"`
	Created        time.Time    `json:"created_at"`
	Allocations    []Allocation `json:"allocations"`
}
type Outcome struct {
	CallbackID string  `json:"callback_id"`
	MerchantID string  `json:"merchant_payment_id"`
	Currency   string  `json:"currency"`
	Amount     string  `json:"minor_units"`
	Status     string  `json:"status"`
	Receipt    *string `json:"receipt_ref"`
}

func Terminal(status string) bool { return status == "SUCCEEDED" || status == "FAILED_FINAL" }

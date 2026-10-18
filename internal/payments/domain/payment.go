package domain

import (
	"encoding/json"
	"time"
)

type Money struct {
	Currency string `json:"currency"`
	Minor    string `json:"minor_units"`
}
type Allocation struct {
	EntitlementID string `json:"entitlement_id"`
	Amount        string `json:"minor_units"`
}
type Payment struct {
	// 外部钱包路由信息只供服务端使用，不接受浏览器传入或序列化返回。
	WalletTenant   string       `json:"-"`
	WalletApp      string       `json:"-"`
	WalletAccount  string       `json:"-"`
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

func (p Payment) MarshalJSON() ([]byte, error) {
	type plain Payment
	var receipt map[string]string
	if p.Receipt != nil {
		receipt = map[string]string{"receipt_ref": *p.Receipt}
	}
	return json.Marshal(struct {
		plain
		Money   Money             `json:"amount"`
		Receipt map[string]string `json:"receipt"`
	}{plain(p), Money{Currency: p.Currency, Minor: p.Amount}, receipt})
}

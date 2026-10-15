package domain

import "time"

type Refund struct {
	Source     string    `json:"source_id"`
	ID         string    `json:"refund_id"`
	BusinessID string    `json:"original_business_id"`
	EventType  string    `json:"original_event_type"`
	Total      string    `json:"refund_basis_total"`
	Currency   string    `json:"currency"`
	Time       time.Time `json:"business_time"`
	Reason     string    `json:"reason"`
	Quantity   string    `json:"refund_quantity_total,omitempty"`
	Unit       string    `json:"quantity_unit,omitempty"`
}
type RefundRecord struct {
	ID      string    `json:"refund_task_id"`
	Input   Refund    `json:"refund"`
	Status  string    `json:"status"`
	Reason  *string   `json:"reason_code"`
	Created time.Time `json:"created_at"`
}

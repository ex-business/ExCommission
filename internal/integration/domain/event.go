package domain

import "time"

type Payload struct {
	Basis        string `json:"basis_amount"`
	Currency     string `json:"basis_currency"`
	Quantity     string `json:"quantity,omitempty"`
	QuantityUnit string `json:"quantity_unit,omitempty"`
	Confirmed    *bool  `json:"confirmed,omitempty"`
}
type Event struct {
	Schema       string    `json:"schema_version"`
	Source       string    `json:"source_id"`
	ID           string    `json:"event_id"`
	BusinessID   string    `json:"business_id"`
	Type         string    `json:"event_type"`
	Time         time.Time `json:"business_time"`
	ExternalUser string    `json:"external_user_id"`
	Product      string    `json:"product_code"`
	Scene        string    `json:"scene"`
	Revision     string    `json:"source_revision"`
	Payload      Payload   `json:"payload"`
}

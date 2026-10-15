package domain

import "time"

type Restriction struct {
	EntitlementID string     `json:"entitlement_id"`
	RiskID        string     `json:"risk_id"`
	Reason        string     `json:"reason_code"`
	Created       time.Time  `json:"created_at"`
	Closed        *time.Time `json:"closed_at"`
}
type Hold struct {
	ID            string `json:"risk_id"`
	EntitlementID string `json:"entitlement_id"`
	Reason        string `json:"reason_code"`
	Evidence      string `json:"evidence_ref"`
	Revision      uint64 `json:"revision"`
	Status        string `json:"status"`
}

package domain

import "time"

type Entitlement struct {
	Minimum             string    `json:"minimum_payout_minor"`
	Maximum             *string   `json:"max_payment_minor"`
	PaymentHold         bool      `json:"payment_hold"`
	RestrictedPending   string    `json:"restricted_pending_minor"`
	RestrictedAvailable string    `json:"restricted_available_minor"`
	RuleID              string    `json:"rule_id"`
	ID                  string    `json:"entitlement_id"`
	TaskID              string    `json:"task_id"`
	AgentID             string    `json:"agent_id"`
	Currency            string    `json:"currency"`
	Target              string    `json:"target_minor"`
	Pending             string    `json:"pending_minor"`
	Available           string    `json:"available_minor"`
	Reserved            string    `json:"reserved_minor"`
	Paid                string    `json:"paid_minor"`
	Debt                string    `json:"debt_minor"`
	Recovered           string    `json:"recovered_minor"`
	Revision            uint64    `json:"revision"`
	BusinessTime        time.Time `json:"business_time"`
	Frozen              bool      `json:"frozen"`
}

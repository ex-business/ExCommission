package domain

type Item struct {
	ID       string `json:"entitlement_id"`
	Amount   string `json:"minor_units"`
	Revision uint64 `json:"entitlement_revision"`
}
type Statement struct {
	Minimum  string  `json:"minimum_payout_minor"`
	Maximum  *string `json:"max_payment_minor"`
	ID       string  `json:"statement_id"`
	AgentID  string  `json:"agent_id"`
	Currency string  `json:"currency"`
	Period   string  `json:"period"`
	Status   string  `json:"status"`
	Revision uint64  `json:"revision"`
	Hash     string  `json:"content_hash"`
	Total    string  `json:"total_minor"`
	Items    []Item  `json:"items"`
}

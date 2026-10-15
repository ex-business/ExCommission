package domain

import "time"

type Filters struct {
	From      *time.Time `json:"from,omitempty"`
	To        *time.Time `json:"to,omitempty"`
	PlanID    string     `json:"plan_id,omitempty"`
	VersionID string     `json:"version_id,omitempty"`
	AgentID   string     `json:"agent_id,omitempty"`
	Currency  string     `json:"currency,omitempty"`
}
type Report struct {
	Kind      string              `json:"kind"`
	Rows      []map[string]string `json:"rows"`
	AsOf      time.Time           `json:"as_of"`
	Watermark string              `json:"source_watermark"`
	Catalog   string              `json:"currency_catalog_version"`
	Metrics   map[string]string   `json:"metrics"`
}
type Export struct {
	ID      string    `json:"export_id"`
	Kind    string    `json:"kind"`
	Status  string    `json:"status"`
	Hash    string    `json:"snapshot_hash"`
	AsOf    time.Time `json:"as_of"`
	Expires time.Time `json:"expires_at"`
	CSV     []byte    `json:"-"`
}

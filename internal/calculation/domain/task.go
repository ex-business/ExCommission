package domain

import "time"

type Task struct {
	ID           string     `json:"task_id"`
	EventID      string     `json:"event_id"`
	Source       string     `json:"source_id"`
	SourceEvent  string     `json:"source_event_id"`
	BusinessID   string     `json:"business_id"`
	EventType    string     `json:"event_type"`
	BusinessTime time.Time  `json:"business_time"`
	EventBody    []byte     `json:"-"`
	Status       string     `json:"status"`
	Reason       *string    `json:"reason_code"`
	Snapshot     []byte     `json:"-"`
	SnapshotHash *string    `json:"snapshot_hash"`
	Result       []byte     `json:"-"`
	Revision     uint64     `json:"revision"`
	Fencing      uint64     `json:"-"`
	Lease        *time.Time `json:"-"`
	Retries      int        `json:"retry_count"`
}

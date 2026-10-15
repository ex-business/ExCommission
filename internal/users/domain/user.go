package domain

import "time"

type Record struct {
	ExternalID  string `json:"external_user_id"`
	DisplayName string `json:"display_name"`
}
type SyncPage struct {
	Records    []Record
	NextCursor *string
	Watermark  string
}

type Binding struct {
	AgentID  string
	From     time.Time
	To       *time.Time
	Evidence string
}

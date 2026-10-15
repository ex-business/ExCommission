package domain

type Record struct {
	ExternalID  string `json:"external_user_id"`
	DisplayName string `json:"display_name"`
}
type SyncPage struct {
	Records    []Record
	NextCursor *string
	Watermark  string
}
